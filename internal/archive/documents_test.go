package archive

import (
	"context"
	"fmt"
	"testing"
)

func TestDocumentListPaginationFiltersAndPageCounts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := 0; i < 25; i++ {
		status := "ready"
		if i == 24 {
			status = "error"
		}
		d := Document{Path: fmt.Sprintf("folder/doc-%02d.pdf", i), Status: status, Modified: fmt.Sprintf("2026-10-%02dT00:00:00Z", i+1)}
		if err := s.Replace(ctx, d, []Page{{Number: 1, Text: "document", OCR: true}, {Number: 2, Text: "  "}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListDocuments(ctx, DocumentListOptions{Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ListDocuments(ctx, DocumentListOptions{Page: 2, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 25 || len(first.Documents) != 20 || len(second.Documents) != 5 {
		t.Fatalf("wrong pages: %+v %+v", first, second)
	}
	if first.Documents[0].Path != "folder/doc-00.pdf" || second.Documents[0].Path != "folder/doc-20.pdf" {
		t.Fatal("unstable ordering")
	}
	d := first.Documents[0]
	if d.Pages != 2 || d.OCRPages != 1 || d.TextPages != 1 {
		t.Fatalf("wrong page counts: %+v", d)
	}
	failed, err := s.ListDocuments(ctx, DocumentListOptions{Status: "error", Sort: "modified", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 1 || failed.Documents[0].Path != "folder/doc-24.pdf" {
		t.Fatal("failed document missing")
	}
	newest, err := s.ListDocuments(ctx, DocumentListOptions{Status: "ready", Sort: "modified", Path: "DOC-2", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if newest.Total != 4 || newest.Documents[0].Path != "folder/doc-23.pdf" {
		t.Fatal("path/status/sort mismatch")
	}
	empty, err := s.ListDocuments(ctx, DocumentListOptions{Page: 99, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 25 || len(empty.Documents) != 0 || empty.Documents == nil {
		t.Fatal("invalid empty page result")
	}
	for _, status := range []string{"queued", "processing"} {
		if _, err := s.Enqueue(ctx, Document{Path: status + ".pdf", Fingerprint: status}, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.claim(ctx); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "processing"} {
		result, err := s.ListDocuments(ctx, DocumentListOptions{Status: status, Page: 1, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 1 {
			t.Fatalf("%s document missing: %+v", status, result)
		}
	}
}

func TestDocumentListValidationAndLiteralPathFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Replace(ctx, Document{Path: "100%_cost.pdf", Status: "ready"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, Document{Path: "100xxcost.pdf", Status: "ready"}, nil); err != nil {
		t.Fatal(err)
	}
	r, err := s.ListDocuments(ctx, DocumentListOptions{Path: "%_", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Fatal("filter interpreted as wildcard")
	}
	for _, o := range []DocumentListOptions{{Page: 0, Limit: 20}, {Page: 1, Limit: 101}, {Page: 100001, Limit: 20}, {Page: 1, Limit: 20, Status: "unknown"}, {Page: 1, Limit: 20, Sort: "path; DROP TABLE documents"}} {
		if _, err := s.ListDocuments(ctx, o); err == nil {
			t.Fatalf("invalid options accepted: %+v", o)
		}
	}
}
