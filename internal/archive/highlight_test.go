package archive

import (
	"context"
	"strings"
	"testing"
)

func TestSearchHighlightsExactFTSSpans(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	text := `Die Kosten für das Nießbrauchsrecht: <script>alert("test")</script>. Eine wichtige Wortgruppe ist private Haftpflichtversicherung.`
	if err := s.Replace(ctx, Document{Path: "Nachweis.pdf", Status: "ready"}, []Page{{Number: 1, Text: text}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"KOSTEN Nießbrauchsrecht", []string{"Kosten", "Nießbrauchsrecht"}},
		{`"private Haftpflichtversicherung"`, []string{"private Haftpflichtversicherung"}},
		{"fur", []string{"für"}},
	} {
		r, err := s.Search(ctx, SearchOptions{Query: test.query, Page: 1, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) != 1 {
			t.Fatalf("missing hit for %s", test.query)
		}
		h := r.Hits[0]
		var plain strings.Builder
		var matches []string
		for _, part := range h.SnippetParts {
			plain.WriteString(part.Text)
			if part.Match {
				matches = append(matches, part.Text)
			}
		}
		if plain.String() != h.Snippet || h.Snippet != text {
			t.Fatal("highlighting changed original snippet text")
		}
		if strings.Join(matches, "|") != strings.Join(test.want, "|") {
			t.Fatalf("wrong highlights for %s: %+v", test.query, matches)
		}
	}
	r, err := s.Search(ctx, SearchOptions{Query: "Nachweis", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 1 {
		t.Fatal("path-only match lost")
	}
	for _, part := range r.Hits[0].SnippetParts {
		if part.Match {
			t.Fatal("path-only match highlighted unrelated text")
		}
	}
}
