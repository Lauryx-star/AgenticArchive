package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceIdentitySurvivesRestartAndRestoration(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "doc.pdf"), "insurance original")
	data := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scanner.Store = s
	saved := root + "-original"
	if err := os.Rename(root, saved); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, false); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("missing root: %v", err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Replacement includes a same-named file: it must not overwrite the indexed version.
	put(t, filepath.Join(root, "doc.pdf"), "wrong replacement")
	if err := scanner.Scan(ctx, true); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("replacement accepted: %v", err)
	}
	if hits(t, s, "original").Total != 1 {
		t.Fatal("index changed")
	}
	d, err := s.ByPath(ctx, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.Retry(ctx, d.ID, true); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("retry on wrong source: %v", err)
	}
	if err := (&Worker{Store: s, Root: root, Extractor: &fakeExtractor{}}).Drain(ctx); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("worker on wrong source: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, root); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if scanner.Status().Unchanged != 1 {
		t.Fatal("restored archive not recognized")
	}
}

func TestEmptyArchiveRequiresExplicitScan(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	put(t, filepath.Join(root, "one.pdf"), "first document")
	put(t, filepath.Join(root, "two.pdf"), "second document")
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "one.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if scanner.Status().Deleted != 1 {
		t.Fatal("ordinary deletion blocked")
	}
	if err := os.Remove(filepath.Join(root, "two.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, false); err == nil {
		t.Fatal("empty archive pruned automatically")
	}
	if hits(t, s, "second").Total != 1 {
		t.Fatal("index lost on empty source")
	}
	scanner.AllowEmpty = true
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "second").Total != 0 || scanner.Status().Deleted != 1 {
		t.Fatal("explicit empty scan did not prune")
	}
}

func TestLegacyIndexEnrollmentRequiresMatchingContent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	put(t, filepath.Join(root, "doc.pdf"), "original")
	hash, err := fingerprint(filepath.Join(root, "doc.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, Document{Path: "doc.pdf", Fingerprint: hash, Status: "ready"}, []Page{{Number: 1, Text: "original"}}); err != nil {
		t.Fatal(err)
	}
	wrong := t.TempDir()
	put(t, filepath.Join(wrong, "doc.pdf"), "replacement")
	scanner := Scanner{Store: s, Root: wrong}
	if err := scanner.Scan(ctx, false); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("unsafe enrollment: %v", err)
	}
	scanner.Root = root
	if err := scanner.Scan(ctx, true); err != nil {
		t.Fatal(err)
	}
	scanner.Root = wrong
	if err := s.AcceptSource(ctx, wrong); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "original").Total != 1 {
		t.Fatal("accept-source changed index")
	}
	if err := scanner.Scan(ctx, true); err != nil {
		t.Fatal(err)
	}
	if scanner.Status().Queued != 1 {
		t.Fatal("accepted replacement not queued")
	}
}

func TestSourceReplacementDuringExtractionPreservesCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "doc.pdf"), "original")
	scanner := Scanner{Store: s, Root: root}
	if err := scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	extractor := &checkpointExtractor{block: func(context.Context) error {
		if err := os.Rename(root, root+"-original"); err != nil {
			return err
		}
		return os.Mkdir(root, 0700)
	}}
	if err := (&Worker{Store: s, Root: root, Extractor: extractor}).Drain(ctx); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("changed root: %v", err)
	}
	q, err := s.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.Pending != 1 || q.Failed != 0 {
		t.Fatalf("job not paused: %+v", q)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM job_pages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("checkpoint discarded")
	}
}
