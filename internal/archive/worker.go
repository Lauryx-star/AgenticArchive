package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ResumableExtractor interface {
	ExtractResumable(context.Context, string, map[int]bool, bool, func(Page, int) error) (int, error)
}
type Worker struct {
	Store     *Store
	Root      string
	Extractor Extractor
}

func sourcePath(root, relative string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("source is not a regular file")
	}
	path, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("source is outside the archive")
	}
	return path, nil
}

// Each extraction uses an immutable on-disk snapshot of the fingerprinted version.
func snapshot(ctx context.Context, source, destination string, expected string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer output.Close()
	hash := sha256.New()
	writer := io.MultiWriter(output, hash)
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err = writer.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return ErrObsolete
	}
	return output.Close()
}

// Drain is used by the CLI; Run keeps a worker available for future scan intervals.
func (w *Worker) Drain(ctx context.Context) error { return w.work(ctx, false) }
func (w *Worker) Run(ctx context.Context) error   { return w.work(ctx, true) }
func (w *Worker) work(ctx context.Context, continuous bool) error {
	lock, err := acquireWorkerLock(w.Store.path + ".worker.lock")
	for continuous && errors.Is(err, ErrWorkerBusy) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		lock, err = acquireWorkerLock(w.Store.path + ".worker.lock")
	}
	if err != nil {
		return err
	}
	defer lock()
	// Scratch files belong to this index and can be cleaned after a process crash.
	workRoot := w.Store.path + ".work"
	if err = os.RemoveAll(workRoot); err != nil {
		return err
	}
	if err = os.MkdirAll(workRoot, 0700); err != nil {
		return err
	}
	if err = w.Store.recoverJobs(ctx); err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = w.Store.enrollSource(ctx, w.Root)
		if err != nil {
			if !continuous || !errors.Is(err, ErrSourceUnavailable) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		worked, err := w.processNext(ctx)
		if err != nil {
			if continuous && errors.Is(err, ErrSourceUnavailable) {
				continue
			}
			return err
		}
		if !worked {
			if !continuous {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

func (w *Worker) processNext(ctx context.Context) (bool, error) {
	j, err := w.Store.claim(ctx)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = w.process(ctx, j)
	if err != nil {
		if sourceErr := w.Store.CheckSource(ctx, w.Root); errors.Is(sourceErr, ErrSourceUnavailable) {
			err = sourceErr
		}
	}
	if err == nil || errors.Is(err, ErrObsolete) {
		return true, nil
	}
	if ctx.Err() != nil || errors.Is(err, ErrSourceUnavailable) {
		releaseErr := w.Store.releaseJob(j)
		if releaseErr != nil && !errors.Is(releaseErr, ErrObsolete) {
			return true, releaseErr
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, err
	}
	if failureErr := w.Store.failJob(ctx, j, err); failureErr != nil && !errors.Is(failureErr, ErrObsolete) {
		return true, failureErr
	}
	return true, nil
}

func (w *Worker) refresh(ctx context.Context, j Job) error {
	scanner := Scanner{Store: w.Store, Root: w.Root}
	// Rediscovery also handles a version that changed without an interval scan yet.
	if err := scanner.discover(ctx, j.Path, false, false); err != nil {
		return err
	}
	if err := w.Store.releaseJob(j); err != nil && !errors.Is(err, ErrObsolete) {
		return err
	}
	return ErrObsolete
}

func (w *Worker) process(ctx context.Context, j Job) error {
	if err := w.Store.CheckSource(ctx, w.Root); err != nil {
		return err
	}
	source, err := sourcePath(w.Root, j.Path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(w.Store.path+".work", 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(w.Store.path+".work", "job-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "source.pdf")
	if err = snapshot(ctx, source, path, j.Fingerprint); errors.Is(err, ErrObsolete) {
		return w.refresh(ctx, j)
	} else if err != nil {
		return err
	}
	saved, err := w.Store.savedPages(ctx, j)
	if err != nil {
		return err
	}
	completed := map[int]bool{}
	for _, p := range saved {
		completed[p.Number] = true
	}
	save := func(p Page, total int) error { return w.Store.savePage(ctx, j, p, total) }
	var total int
	extractor := w.Extractor
	if pdf, ok := extractor.(PDFExtractor); ok {
		pdf.TempRoot = tmp
		extractor = pdf
	}
	if resumable, ok := extractor.(ResumableExtractor); ok {
		total, err = resumable.ExtractResumable(ctx, path, completed, j.ForceOCR, save)
	} else {
		var pages []Page
		pages, err = extractor.Extract(ctx, path)
		total = len(pages)
		if err == nil {
			for _, p := range pages {
				if !completed[p.Number] {
					if err = save(p, total); err != nil {
						break
					}
				}
			}
		}
	}
	if err != nil {
		return err
	}
	if err := w.Store.CheckSource(ctx, w.Root); err != nil {
		return err
	}
	// Publication checks both disk content and the current database version.
	hash, err := fingerprint(source)
	if err != nil {
		return err
	}
	if hash != j.Fingerprint {
		return w.refresh(ctx, j)
	}
	pages, err := w.Store.savedPages(ctx, j)
	if err != nil {
		return err
	}
	if len(pages) != total {
		return fmt.Errorf("incomplete page checkpoints")
	}
	for i, p := range pages {
		if p.Number != i+1 {
			return fmt.Errorf("invalid page checkpoint sequence")
		}
	}
	return w.Store.finish(ctx, j, pages)
}
