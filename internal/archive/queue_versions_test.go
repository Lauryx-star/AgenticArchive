package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRevertedVersionReusesExistingJob(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	for _, text := range []string{"alpha source", "bravo source", "alpha source"} {
		put(t, path, text)
		if err := scanAndDrain(ctx, &scanner, true); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 2 {
		t.Fatalf("reverted version duplicated a job: %d %v", count, err)
	}
	if hits(t, s, "alpha").Total != 1 || hits(t, s, "bravo").Total != 0 {
		t.Fatal("reverted content was not published")
	}
}

func TestQueuedReplacementDoesNotExposeOldPageText(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	put(t, path, "old indexed content")
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	d, err := s.ByPath(ctx, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, "new source content")
	if err = scanner.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(ctx, d.ID, 1); err != sql.ErrNoRows {
		t.Fatalf("stale page exposed during replacement: %v", err)
	}
	if err = (&Worker{Store: s, Root: root, Extractor: scanner.Extractor}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.Page(ctx, d.ID, 1)
	if err != nil || p.Text != "new source content" {
		t.Fatalf("replacement page missing: %+v %v", p, err)
	}
}
