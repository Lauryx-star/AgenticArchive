package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryCombinesPreviousPartialAnswersWithoutArchiveCalls(t *testing.T) {
	reads := 0
	calls := 0
	client := researchClient(t, func(body string) string {
		calls++
		var payload map[string]any
		json.Unmarshal([]byte(body), &payload)
		if _, ok := payload["tools"]; ok {
			t.Fatal("summary requested research tools")
		}
		if !strings.Contains(body, "100 EUR") || !strings.Contains(body, "120 EUR") || !strings.Contains(body, "Teilergebnis") {
			t.Fatal("prior findings or caveat lost")
		}
		return modelText("Du hast bisher 220 EUR belegt, ohne Anspruch auf Vollständigkeit. [Dokument 7, Seite 1] [Dokument 8, Seite 2]")
	}, &reads)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	messages := []Message{
		{Role: "user", Content: "Recherchiere 2024"},
		{Role: "assistant", Content: "Teilergebnis: 100 EUR [Dokument 7, Seite 1]", Sources: []Source{{DocumentID: 7, Page: 1}}},
		{Role: "user", Content: "Und 2025?"},
		{Role: "assistant", Content: "120 EUR [Dokument 8, Seite 2]", Sources: []Source{{DocumentID: 8, Page: 2}}},
		{Role: "user", Content: "Führe beide Ergebnisse zusammen"},
	}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Mode: "summary", Messages: messages})
	if err != nil || calls != 1 || reads != 0 || len(answer.Sources) != 2 || len(answer.History) != 6 {
		t.Fatalf("summary lost context: %+v %v", answer, err)
	}
}

func TestLongResearchContinuesBeyondTwelveQuestionsWithSources(t *testing.T) {
	reads := 0
	compressions := 0
	client := researchClient(t, func(body string) string {
		if strings.Contains(body, "Erzeuge kurze interne Notizen") {
			if !strings.Contains(body, "100 EUR") {
				t.Fatal("findings missing in compression")
			}
			return modelText("Teilergebnis: 100 EUR [Dokument 7, Seite 1]; weitere Jahre offen.")
		}
		return modelText("Du hast bisher 100 EUR belegt [Dokument 7, Seite 1]; weitere Jahre offen.")
	}, &reads)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses"}
	history := []Message{{Role: "user", Content: "Recherchiere"}, {Role: "assistant", Content: "Teilergebnis: 100 EUR [Dokument 7, Seite 1]", Sources: []Source{{DocumentID: 7, Page: 1}}}}
	for i := 0; i < 30; i++ {
		answer, err := runner.Run(context.Background(), "archive-token", Request{Mode: "summary", Messages: append(history, Message{Role: "user", Content: "Fasse zusammen"})})
		if err != nil || len(answer.Sources) != 1 || !strings.Contains(answer.Text, "100 EUR") {
			t.Fatalf("turn %d lost findings: %+v %v", i, answer, err)
		}
		if answer.Compacted {
			compressions++
		}
		history = answer.History
	}
	if compressions < 2 || reads != 0 {
		t.Fatalf("unbounded history: %d compressions %d reads", compressions, reads)
	}
}

func TestCompressionPreservesCitedFindingsWhenModelDropsThem(t *testing.T) {
	reads := 0
	client := researchClient(t, func(body string) string {
		if strings.Contains(body, "Erzeuge kurze interne Notizen") {
			return modelText("Die Recherche ist unvollständig.")
		}
		if !strings.Contains(body, "2025: 100 EUR [Dokument 7, Seite 1]") || !strings.Contains(body, "2026: 120 EUR [Dokument 8, Seite 1]") {
			t.Fatal("original cited findings lost during model compression")
		}
		return modelText("220 EUR [Dokument 7, Seite 1] [Dokument 8, Seite 1]")
	}, &reads)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses"}
	var padding strings.Builder
	for i := 0; i < 200; i++ {
		padding.WriteString(strings.Repeat("Recherchekontext ", 5))
		padding.WriteRune(rune('一' + i))
		padding.WriteString("\n")
	}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Mode: "summary", Messages: []Message{
		{Role: "user", Content: "2025?"}, {Role: "assistant", Content: "2025: 100 EUR [Dokument 7, Seite 1]\n\n" + padding.String(), Sources: []Source{{DocumentID: 7, Page: 1}}},
		{Role: "user", Content: "2026?"}, {Role: "assistant", Content: "2026: 120 EUR [Dokument 8, Seite 1]", Sources: []Source{{DocumentID: 8, Page: 1}}},
		{Role: "user", Content: "Zusammenführen"},
	}})
	if err != nil || !answer.Compacted || len(answer.Sources) != 2 || reads != 0 {
		t.Fatalf("lost findings: %+v %v", answer, err)
	}
}
