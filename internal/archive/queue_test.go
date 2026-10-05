package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type checkpointExtractor struct {
	block     func(context.Context) error
	processed []int
	force     []bool
}

func (e *checkpointExtractor) Extract(context.Context, string) ([]Page, error) {
	return nil, fmt.Errorf("resumable extraction required")
}
func (e *checkpointExtractor) ExtractResumable(ctx context.Context, _ string, completed map[int]bool, force bool, save func(Page, int) error) (int, error) {
	for n := 1; n <= 3; n++ {
		if completed[n] {
			continue
		}
		e.processed = append(e.processed, n)
		e.force = append(e.force, force)
		if err := save(Page{Number: n, Text: fmt.Sprintf("checkpoint page %d", n), OCR: force}, 3); err != nil {
			return 3, err
		}
		if e.block != nil {
			block := e.block
			e.block = nil
			if err := block(ctx); err != nil {
				return 3, err
			}
		}
	}
	return 3, nil
}

func TestHundredDocumentsStayUniqueDuringSlowProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newStore(t)
	root := t.TempDir()
	for n := 0; n < 100; n++ {
		put(t, filepath.Join(root, fmt.Sprintf("%03d.pdf", n)), fmt.Sprintf("document %d", n))
	}
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	resume := make(chan struct{})
	extractor := &checkpointExtractor{block: func(ctx context.Context) error {
		close(started)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	worker := Worker{Store: s, Root: root, Extractor: extractor}
	done := make(chan error, 1)
	go func() { done <- worker.Drain(ctx) }()
	<-started
	var wg sync.WaitGroup
	scanErrors := make(chan error, 10)
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); scan := Scanner{Store: s, Root: root}; scanErrors <- scan.Scan(ctx, false) }()
	}
	wg.Wait()
	close(scanErrors)
	for err := range scanErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	q, err := s.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.Pending != 99 || q.Running != 1 || q.Active.CompletedPages != 1 {
		t.Fatalf("duplicate or reset work: %+v", q)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 100 {
		t.Fatalf("expected 100 jobs, got %d: %v", count, err)
	}
	close(resume)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	q, err = s.Queue(ctx)
	if err != nil || q.Completed != 100 || q.Pending != 0 {
		t.Fatalf("queue not drained: %+v %v", q, err)
	}
}

func TestCrashRecoveryRetainsCompletedPages(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	put(t, filepath.Join(root, "doc.pdf"), "source document")
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Store: s, Root: root}
	if err = scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	j, err := s.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.savePage(ctx, j, Page{Number: 1, Text: "retained checkpoint"}, 3); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	extractor := &checkpointExtractor{}
	if err = (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(extractor.processed) != "[2 3]" {
		t.Fatalf("completed page reprocessed: %v", extractor.processed)
	}
	d, err := s.ByPath(ctx, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Page(ctx, d.ID, 1)
	if err != nil || p.Text != "retained checkpoint" {
		t.Fatalf("lost checkpoint: %+v %v", p, err)
	}
}

func TestCancellationRequeuesWithoutDiscardingPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStore(t)
	root := t.TempDir()
	put(t, filepath.Join(root, "doc.pdf"), "source document")
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{block: func(context.Context) error { cancel(); return context.Canceled }}
	if err := (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	q, err := s.Queue(context.Background())
	if err != nil || q.Pending != 1 || q.Failed != 0 {
		t.Fatalf("not requeued: %+v %v", q, err)
	}
	extractor = &checkpointExtractor{}
	if err := (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(extractor.processed) != "[2 3]" {
		t.Fatalf("lost resume position: %v", extractor.processed)
	}
}

func TestVersionChangeCannotPublishStaleContent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	put(t, path, "old source")
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{block: func(context.Context) error { put(t, path, "new source"); return scanner.Scan(ctx, false) }}
	worker := Worker{Store: s, Root: root, Extractor: extractor}
	// processNext is used here to inspect the boundary before the newer job runs.
	if _, err := worker.processNext(ctx); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "checkpoint").Total != 0 {
		t.Fatal("obsolete pages published")
	}
	q, err := s.Queue(ctx)
	if err != nil || q.Pending != 1 || q.Running != 0 {
		t.Fatalf("missing new version: %+v %v", q, err)
	}
	worker.Extractor = &fakeExtractor{}
	if err = worker.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "new").Total != 1 || hits(t, s, "old").Total != 0 {
		t.Fatal("wrong source version indexed")
	}
}

func TestDeletionDuringProcessingCannotResurrectDocument(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	put(t, path, "source document")
	scanner := Scanner{Store: s, Root: root, AllowEmpty: true}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{block: func(context.Context) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return scanner.Scan(ctx, false)
	}}
	if err := (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByPath(ctx, "doc.pdf"); err != sql.ErrNoRows {
		t.Fatalf("deleted document resurrected: %v", err)
	}
}

func TestFailedJobsRequireExplicitRetryAndCanForceOCR(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	put(t, filepath.Join(root, "doc.pdf"), "source document")
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	failing := &fakeExtractor{fail: true}
	if err := (&Worker{Store: s, Root: root, Extractor: failing}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		if err := scanner.Scan(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	q, err := s.Queue(ctx)
	if err != nil || q.Failed != 1 || q.Pending != 0 {
		t.Fatalf("automatic retry loop: %+v %v", q, err)
	}
	d, err := s.ByPath(ctx, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err = scanner.Retry(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{}
	if err = (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	for _, force := range extractor.force {
		if !force {
			t.Fatal("force OCR flag lost")
		}
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM jobs").Scan(&count)
	if count != 1 {
		t.Fatal("retry created duplicate job")
	}
}

func TestOnlyOneWorkerCanOwnAnIndex(t *testing.T) {
	s := newStore(t)
	release, err := acquireWorkerLock(s.path + ".worker.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = (&Worker{Store: s, Root: t.TempDir(), Extractor: &fakeExtractor{}}).Drain(context.Background()); !errors.Is(err, ErrWorkerBusy) {
		t.Fatalf("second worker acquired lock: %v", err)
	}
}
