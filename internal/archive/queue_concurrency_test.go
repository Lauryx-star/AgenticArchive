package archive

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentDiscoveryAcrossDatabaseConnections(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	put(t, filepath.Join(root, "doc.pdf"), "same source version")
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for n := 0; n < 8; n++ {
		store := a
		if n%2 == 1 {
			store = b
		}
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			scanner := Scanner{Store: s, Root: root}
			results <- scanner.Scan(ctx, true)
		}(store)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = a.db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate job across connections: %d %v", count, err)
	}
}

func TestUnobservedChangeDuringExtractionQueuesLatestVersion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	put(t, path, "old source")
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{block: func(context.Context) error { put(t, path, "new source"); return nil }}
	worker := Worker{Store: s, Root: root, Extractor: extractor}
	if _, err := worker.processNext(ctx); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "checkpoint").Total != 0 {
		t.Fatal("changed source published before rediscovery")
	}
	q, err := s.Queue(ctx)
	if err != nil || q.Pending != 1 || q.Running != 0 {
		t.Fatalf("changed source not queued: %+v %v", q, err)
	}
	worker.Extractor = &fakeExtractor{}
	if err = worker.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "new").Total != 1 {
		t.Fatal("latest source missing")
	}
}

func TestIdenticalContentsKeepSeparateDocumentPaths(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	for n := 0; n < 2; n++ {
		put(t, filepath.Join(root, fmt.Sprintf("%d.pdf", n)), "same contents")
	}
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	q, err := s.Queue(ctx)
	if err != nil || q.Pending != 2 {
		t.Fatalf("different paths merged: %+v %v", q, err)
	}
	if err = (&Worker{Store: s, Root: root, Extractor: &fakeExtractor{}}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "contents").Total != 2 {
		t.Fatal("document path lost")
	}
}
