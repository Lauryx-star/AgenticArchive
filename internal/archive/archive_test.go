package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fakeExtractor struct {
	calls int
	fail  bool
}

func (f *fakeExtractor) Extract(_ context.Context, path string) ([]Page, error) {
	f.calls++
	if f.fail {
		return nil, fmt.Errorf("unreadable PDF")
	}
	b, err := os.ReadFile(path)
	return []Page{{Number: 1, Text: string(b)}}, err
}
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func hits(t *testing.T, s *Store, query string) SearchResult {
	t.Helper()
	r, err := s.Search(context.Background(), SearchOptions{Query: query, Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScanLifecycleAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "archive.db")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(root, "nested", "Insurance.PDF")
	put(t, pdf, "Current liability insurance certificate")
	put(t, filepath.Join(root, "ignored.txt"), "not a PDF")
	store, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	extractor := &fakeExtractor{}
	scanner := Scanner{Store: store, Root: root, Extractor: extractor}
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, store, `"liability insurance"`).Total != 1 {
		t.Fatal("phrase not found")
	}
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 1 {
		t.Fatal("unchanged PDF extracted again")
	}
	put(t, pdf, "Updated household insurance certificate")
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, store, "liability").Total != 0 || hits(t, store, "household").Total != 1 {
		t.Fatal("stale text after update")
	}
	store.Close()
	store, err = Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner.Store = store
	if hits(t, store, "household").Total != 1 {
		t.Fatal("index not persisted")
	}
	scanner.AllowEmpty = true // Explicitly confirm intentional removal of the final PDF.
	if err = os.Remove(pdf); err != nil {
		t.Fatal(err)
	}
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, store, "household").Total != 0 || scanner.Status().Deleted != 1 {
		t.Fatal("deleted PDF still searchable")
	}
}

func TestUnavailableSourceDoesNotPrune(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "doc.pdf"), "Important policy reference")
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := scanAndDrain(ctx, &scanner, false); err == nil {
		t.Fatal("missing root accepted")
	}
	if hits(t, s, "policy").Total != 1 {
		t.Fatal("index pruned after unavailable source")
	}
}

func TestFullVerificationDetectsPreservedMetadata(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "doc.pdf")
	put(t, path, "alpha policy")
	extractor := &fakeExtractor{}
	scanner := Scanner{Store: s, Root: root, Extractor: extractor}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, "bravo policy")
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 1 {
		t.Fatal("fast scan did not use metadata")
	}
	if err = scanAndDrain(ctx, &scanner, true); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "bravo").Total != 1 || hits(t, s, "alpha").Total != 0 {
		t.Fatal("full verification failed")
	}
}

func TestFailureIsolationAndRetry(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := t.TempDir()
	put(t, filepath.Join(root, "bad.pdf"), "Insurance certificate")
	extractor := &fakeExtractor{fail: true}
	scanner := Scanner{Store: s, Root: root, Extractor: extractor}
	if err := scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	d, err := s.ByPath(ctx, "bad.pdf")
	if err != nil || d.Status != "error" {
		t.Fatal("missing failure status")
	}
	if hits(t, s, "Insurance").Total != 0 {
		t.Fatal("failed document searchable")
	}
	extractor.fail = false
	if err = scanner.Retry(ctx, d.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = scanAndDrain(ctx, &scanner, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "Insurance").Total != 1 {
		t.Fatal("failed document was not retried")
	}
}

func TestSearchGroupingFiltersAndLiteralSyntax(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := 0; i < 3; i++ {
		d := Document{Path: fmt.Sprintf("policy-%d.pdf", i), Status: "ready", Modified: fmt.Sprintf("2026-10-0%dT12:00:00Z", i+1)}
		if err := s.Replace(ctx, d, []Page{{Number: 1, Text: "Private liability insurance"}, {Number: 2, Text: "Additional liability insurance"}}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Search(ctx, SearchOptions{Query: `"liability insurance"`, Page: 2, Limit: 1, Sort: "modified"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 3 || len(r.Hits) != 1 || r.Hits[0].Path != "policy-1.pdf" {
		t.Fatalf("grouping or paging: %+v", r)
	}
	r, err = s.Search(ctx, SearchOptions{Query: "LIABILITY", After: "2026-10-02", Before: "2026-10-02", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Fatal("date filters failed")
	}
	if hits(t, s, "private OR insurance").Total != 0 {
		t.Fatal("OR interpreted as query operator")
	}
	if _, err = SearchExpression(`"unclosed`); err == nil {
		t.Fatal("unclosed phrase accepted")
	}
}

func TestSymlinksAreNotIndexed(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.pdf")
	put(t, outside, "Outside archive")
	if err := os.Symlink(outside, filepath.Join(root, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	scanner := Scanner{Store: s, Root: root, Extractor: &fakeExtractor{}}
	if err := scanAndDrain(context.Background(), &scanner, false); err != nil {
		t.Fatal(err)
	}
	if hits(t, s, "Outside").Total != 0 {
		t.Fatal("symlink escaped root")
	}
}

func scanAndDrain(ctx context.Context, s *Scanner, full bool) error {
	if err := s.Scan(ctx, full); err != nil {
		return err
	}
	return (&Worker{Store: s.Store, Root: s.Root, Extractor: s.Extractor}).Drain(ctx)
}
