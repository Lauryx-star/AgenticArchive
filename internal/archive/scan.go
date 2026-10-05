package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ScanReport struct {
	Running   bool   `json:"running"`
	Current   string `json:"current,omitempty"`
	Updated   int    `json:"updated"`
	Queued    int    `json:"queued"`
	Unchanged int    `json:"unchanged"`
	Deleted   int    `json:"deleted"`
	Failed    int    `json:"failed"`
	Finished  string `json:"finished,omitempty"`
	Error     string `json:"error,omitempty"`
}
type Scanner struct {
	Store      *Store
	Root       string
	Extractor  Extractor
	AllowEmpty bool
	mu         sync.Mutex
	report     ScanReport
}

func (s *Scanner) Status() ScanReport          { s.mu.Lock(); defer s.mu.Unlock(); return s.report }
func (s *Scanner) update(fn func(*ScanReport)) { s.mu.Lock(); defer s.mu.Unlock(); fn(&s.report) }

func fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Scan removes missing documents only after a complete, successful directory walk.
// Full verification also hashes files whose size and timestamp have not changed.
func (s *Scanner) Scan(ctx context.Context, full bool) (err error) {
	s.mu.Lock()
	if s.report.Running {
		s.mu.Unlock()
		return fmt.Errorf("a scan is already running")
	}
	s.report = ScanReport{Running: true}
	s.mu.Unlock()
	defer func() {
		s.update(func(r *ScanReport) {
			r.Running = false
			r.Current = ""
			r.Finished = time.Now().UTC().Format(time.RFC3339)
			if err != nil {
				r.Error = err.Error()
			}
		})
	}()
	if err = s.Store.enrollSource(ctx, s.Root); err != nil {
		return err
	}
	rootInfo, err := os.Stat(s.Root)
	if err != nil {
		return fmt.Errorf("source directory unavailable: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("source is not a directory")
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(s.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		s.update(func(r *ScanReport) { r.Current = rel })
		info, err := entry.Info()
		if err != nil {
			return err
		}
		modified := info.ModTime().UTC().Format(time.RFC3339Nano)
		old, err := s.Store.ByPath(ctx, rel)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if !full && (old.Status == "ready" || old.Status == "queued" || old.Status == "processing" || old.Status == "error") && old.Size == info.Size() && old.Modified == modified {
			// Failed legacy imports have no durable job yet.
			if old.Status == "error" {
				return s.discover(ctx, rel, false, false)
			}
			s.update(func(r *ScanReport) { r.Unchanged++ })
			return nil
		}
		hash, err := fingerprint(path)
		if err != nil {
			return err
		}
		// Only queue a stable observed version. Processing happens independently.
		after, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			s.update(func(r *ScanReport) { r.Failed++ })
			return nil
		}
		if err := s.Store.CheckSource(ctx, s.Root); err != nil {
			return err
		}
		queued, err := s.Store.Enqueue(ctx, Document{Path: rel, Size: info.Size(), Modified: modified, Fingerprint: hash}, false, false)
		if err == nil {
			s.update(func(r *ScanReport) {
				if queued {
					r.Queued++
				} else {
					r.Unchanged++
				}
			})
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("scan incomplete; deletion skipped: %w", err)
	}
	// Recheck availability before pruning; a failed or disconnected mount must not look empty.
	currentRoot, statErr := os.Stat(s.Root)
	if statErr != nil {
		return fmt.Errorf("source unavailable; deletion skipped: %w", statErr)
	}
	if !os.SameFile(rootInfo, currentRoot) {
		return fmt.Errorf("source directory changed during scan; deletion skipped")
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("source unavailable; deletion skipped: %w", err)
	}
	docs, err := s.Store.Documents(ctx)
	if err != nil {
		return err
	}
	if err = s.Store.CheckSource(ctx, s.Root); err != nil {
		return err
	}
	if len(seen) == 0 && len(docs) > 0 && !s.AllowEmpty {
		return fmt.Errorf("archive contains no PDFs; deletion skipped; use scan -allow-empty only after verifying the source")
	}
	for _, d := range docs {
		if !seen[d.Path] {
			if err = s.Store.Delete(ctx, d.ID); err != nil {
				return err
			}
			s.update(func(r *ScanReport) { r.Deleted++ })
		}
	}
	return nil
}

func (s *Scanner) discover(ctx context.Context, relative string, retry, force bool) error {
	if err := s.Store.CheckSource(ctx, s.Root); err != nil {
		return err
	}
	path, err := sourcePath(s.Root, relative)
	if err != nil {
		return err
	}
	before, err := os.Stat(path)
	if err != nil {
		return err
	}
	hash, err := fingerprint(path)
	if err != nil {
		return err
	}
	after, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("source changed while fingerprinting")
	}
	if err := s.Store.CheckSource(ctx, s.Root); err != nil {
		return err
	}
	_, err = s.Store.Enqueue(ctx, Document{Path: relative, Size: after.Size(), Modified: after.ModTime().UTC().Format(time.RFC3339Nano), Fingerprint: hash}, retry, force)
	return err
}

func (s *Scanner) Retry(ctx context.Context, id int64, force bool) error {
	d, err := s.Store.Document(ctx, id)
	if err != nil {
		return err
	}
	return s.discover(ctx, d.Path, true, force)
}
