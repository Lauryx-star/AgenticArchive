package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResearchCanReadMoreThanEightRounds(t *testing.T) {
	modelCalls, readCalls := 0, 0
	client := researchClient(t, func(body string) string {
		modelCalls++
		if modelCalls <= 10 {
			return modelTool("read_page", fmt.Sprintf(`{"document_id":%d,"page":1,"offset":0}`, modelCalls))
		}
		return modelText("Hier findest du deine chronologische Auflistung.")
	}, &readCalls)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Liste meine Rechnungen chronologisch auf"}}})
	if err != nil || modelCalls != 11 || readCalls != 10 || len(answer.Sources) != 10 || strings.Contains(answer.Text, "Teilergebnis") {
		t.Fatalf("larger research failed: %+v %v, %d rounds", answer, err, modelCalls)
	}
}

func TestModelBudgetProducesGroundedPartialAnswer(t *testing.T) {
	modelCalls, readCalls := 0, 0
	client := researchClient(t, func(body string) string {
		modelCalls++
		var payload map[string]any
		json.Unmarshal([]byte(body), &payload)
		if modelCalls == MaxModelCalls {
			if _, ok := payload["tools"]; ok {
				t.Fatal("final summary must not request more tools")
			}
			if !strings.Contains(body, "EON Rechnungsdatum") || !strings.Contains(body, "duze") {
				t.Fatal("read evidence or informal address missing")
			}
			return modelText("| Datum | Betrag | Quelle |\n| 12.09.2026 | unbekannt | [Dokument 7, Seite 1] |")
		}
		return modelTool("read_page", fmt.Sprintf(`{"document_id":7,"page":1,"offset":%d}`, modelCalls))
	}, &readCalls)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Liste alle Rechnungsbeträge"}}})
	if err != nil || modelCalls != MaxModelCalls || !strings.Contains(answer.Text, "Teilergebnis") || !strings.Contains(answer.Text, "12.09.2026") || len(answer.Sources) != 1 {
		t.Fatalf("findings discarded: %+v %v (%d calls)", answer, err, modelCalls)
	}
}

func TestRepeatedReadsStopAndPreserveEvidence(t *testing.T) {
	modelCalls, readCalls := 0, 0
	client := researchClient(t, func(body string) string {
		modelCalls++
		if modelCalls == 4 {
			return modelText("Dein Beleg nennt das Datum 12.09.2026 [Dokument 7, Seite 1].")
		}
		return modelTool("read_page", `{"document_id":7,"page":1,"offset":0}`)
	}, &readCalls)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Liste meine Rechnungen"}}})
	if err != nil || modelCalls != 4 || readCalls != 2 || !strings.Contains(answer.Text, "Teilergebnis") || !strings.Contains(answer.Text, "12.09.2026") {
		t.Fatalf("repeat loop failed: %+v %v", answer, err)
	}
}

func TestPartialFinalizationKeepsPreviousResearchContext(t *testing.T) {
	modelCalls, readCalls := 0, 0
	client := researchClient(t, func(body string) string {
		modelCalls++
		if modelCalls == 4 {
			if !strings.Contains(body, "2025: 100 EUR") || !strings.Contains(body, "EON Rechnungsdatum") {
				t.Fatal("prior answer or current read evidence lost at limit")
			}
			return modelText("2025: 100 EUR [Dokument 1, Seite 1]; neuer Beleg [Dokument 7, Seite 1].")
		}
		return modelTool("read_page", `{"document_id":7,"page":1,"offset":0}`)
	}, &readCalls)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{
		{Role: "user", Content: "Recherchiere 2025"},
		{Role: "assistant", Content: "2025: 100 EUR [Dokument 1, Seite 1]", Sources: []Source{{DocumentID: 1, Page: 1}}},
		{Role: "user", Content: "Und jetzt 2026"},
	}})
	if err != nil || !strings.Contains(answer.Text, "Teilergebnis") || len(answer.Sources) != 2 {
		t.Fatalf("previous research disappeared: %+v %v", answer, err)
	}
}
