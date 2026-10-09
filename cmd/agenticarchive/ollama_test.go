package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

// Opt-in: only synthetic invoices are exposed to a locally running model.
func TestLiveOllamaResearchReadsLatestSyntheticInvoice(t *testing.T) {
	testLiveOllamaResearch(t, false)
}

func TestLiveOllamaChronologicalAmounts(t *testing.T) { testLiveOllamaResearch(t, true) }

func testLiveOllamaResearch(t *testing.T, chronological bool) {
	endpoint := os.Getenv("OLLAMA_TEST_URL")
	if endpoint == "" {
		t.Skip("set OLLAMA_TEST_URL to run the local model integration")
	}
	f := newAuthFixture(t)
	db, err := sql.Open("sqlite3", filepath.Join(f.dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE documents SET path='2025-Stromrechnung.pdf' WHERE id=1;
UPDATE pages SET text='EON Stromrechnung. Rechnungsdatum 10.05.2025. Bruttorechnungsbetrag 100,00 EUR. Der tatsächliche Erhalt ist nicht dokumentiert.' WHERE document_id=1;
INSERT INTO documents(id,path,size,modified,fingerprint,status,pages) VALUES(2,'2026-Stromrechnung.pdf',19,'2026-10-07T00:00:00Z','invoice-2','ready',1);
INSERT INTO pages(document_id,number,text,ocr) VALUES(2,1,'EON Stromrechnung. Rechnungsdatum 12.09.2026. Bruttorechnungsbetrag 120,00 EUR. Der tatsächliche Erhalt ist nicht dokumentiert.',0);
INSERT INTO page_search(rowid,text,path) SELECT p.id,p.text,d.path FROM pages p JOIN documents d ON d.id=p.document_id;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agent.ResearchTimeout)
	defer cancel()
	if err := f.a.store.SetupAdmin(ctx, "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	user, err := f.a.store.Authenticate(ctx, "admin", authPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.a.store.CreateAgentToken(ctx, user, "synthetic live test", 5*time.Minute, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.h)
	defer server.Close()
	client := &http.Client{Timeout: agent.ProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: liveOllamaTransport(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(data))
		if r.URL.Path == "/v1/responses" {
			var decoded struct {
				Output json.RawMessage `json:"output"`
			}
			json.Unmarshal(data, &decoded)
			t.Logf("Synthetic model output: %s", decoded.Output)
		} else {
			t.Logf("Synthetic MCP output: %s", data)
		}
		return response, err
	})}
	model := os.Getenv("OLLAMA_TEST_MODEL")
	if model == "" {
		model = "qwen3:8b"
	}
	question := "Wann habe ich die letzte Stromrechnung erhalten? Mein Anbieter ist EON"
	if chronological {
		question = "Gib mir bitte eine chronologische Auflistung aller Rechnungsbeträge meiner Stromrechnungen von EON"
	}
	runner := agent.Runner{Client: client, Provider: "ollama", Model: model, APIURL: endpoint, MCPURL: server.URL + "/mcp"}
	answer, err := runner.Run(ctx, token, agent.Request{Messages: []agent.Message{{Role: "user", Content: question}}})
	if err != nil {
		t.Fatal(err)
	}
	readLatest := false
	for _, source := range answer.Sources {
		if source.DocumentID == 2 && source.Page == 1 {
			readLatest = true
		}
	}
	if !readLatest || len(answer.Sources) != 2 || !(strings.Contains(answer.Text, "12.09.2026") || strings.Contains(answer.Text, "12. September 2026") || strings.Contains(answer.Text, "2026-09-12")) {
		t.Fatalf("latest invoice not researched: %+v", answer)
	}
	if chronological {
		older := strings.Index(answer.Text, "10.05.2025")
		newer := strings.Index(answer.Text, "12.09.2026")
		if older < 0 || newer < 0 || older >= newer || !strings.Contains(answer.Text, "100") || !strings.Contains(answer.Text, "120") || strings.Contains(answer.Text, " Sie ") || strings.Contains(answer.Text, " Ihnen") {
			t.Fatalf("amounts, chronology or informal address missing: %s", answer.Text)
		}
	}
	t.Logf("Synthetic research succeeded with %d read source pages", len(answer.Sources))
}

type liveOllamaTransport func(*http.Request) (*http.Response, error)

func (f liveOllamaTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiveOllamaCombinesPartialResearchWithoutTools(t *testing.T) { testLiveOllamaSummary(t, false) }
func TestLiveOllamaCompactsAndCombinesPartialResearch(t *testing.T)  { testLiveOllamaSummary(t, true) }
func testLiveOllamaSummary(t *testing.T, compact bool) {
	endpoint := os.Getenv("OLLAMA_TEST_URL")
	if endpoint == "" {
		t.Skip("set OLLAMA_TEST_URL to run the local model integration")
	}
	model := os.Getenv("OLLAMA_TEST_MODEL")
	if model == "" {
		model = "qwen3:8b"
	}
	runner := agent.Runner{Client: &http.Client{Timeout: agent.ProviderTimeout, Transport: liveOllamaTransport(func(r *http.Request) (*http.Response, error) {
		start := time.Now()
		response, err := http.DefaultTransport.RoundTrip(r)
		t.Logf("Synthetic context model call: %s, error: %v", time.Since(start), err)
		return response, err
	})}, Provider: "ollama", Model: model, APIURL: endpoint, MCPURL: "http://archive.invalid/mcp"}
	ctx, cancel := context.WithTimeout(context.Background(), agent.ResearchTimeout)
	defer cancel()
	request := agent.Request{Mode: "summary", Messages: []agent.Message{
		{Role: "user", Content: "Liste meine Stromrechnungen von 2025 auf"},
		{Role: "assistant", Content: "Teilergebnis: Rechnungsdatum 10.05.2025, Bruttorechnungsbetrag 100,00 EUR [Dokument 1, Seite 1]. Weitere Rechnungen sind nicht ausgeschlossen.", Sources: []agent.Source{{DocumentID: 1, Page: 1}}},
		{Role: "user", Content: "Jetzt die Rechnungen von 2026"},
		{Role: "assistant", Content: "Teilergebnis: Rechnungsdatum 12.09.2026, Bruttorechnungsbetrag 120,00 EUR [Dokument 2, Seite 1]. Die Recherche ist unvollständig.", Sources: []agent.Source{{DocumentID: 2, Page: 1}}},
		{Role: "user", Content: "Führe beide Teilergebnisse chronologisch zusammen und summiere die belegten Bruttorechnungsbeträge. Nenne weiterhin die Quellen und Recherche-Lücken."},
	}}
	if compact {
		request.Messages[1].Content += "\n" + strings.Repeat("Hinweis: Dies ist nur ein Teilergebnis; weitere Rechnungen sind nicht ausgeschlossen.\n", 240)
	}
	answer, err := runner.Run(ctx, "unused-read-token", request)
	if err != nil {
		t.Fatal(err)
	}
	if compact && !answer.Compacted {
		t.Fatal("large research context was not condensed")
	}
	t.Logf("Synthetic combined answer: %s", answer.Text)
	first, second := strings.Index(answer.Text, "10.05.2025"), strings.Index(answer.Text, "12.09.2026")
	if first < 0 || second <= first || !strings.Contains(answer.Text, "220") || !strings.Contains(answer.Text, "[Dokument 1, Seite 1]") || !strings.Contains(answer.Text, "[Dokument 2, Seite 1]") || len(answer.Sources) != 2 {
		t.Fatalf("lost dates, totals or citations: %+v", answer)
	}
	if strings.Contains(answer.Text, "keine weiteren Rechnungen") || strings.Contains(answer.Text, "Netzbetreiber") {
		t.Fatal("invented absence or research gap")
	}
	if strings.Contains(answer.Text, "Sie ") || strings.Contains(answer.Text, "Ihnen") {
		t.Fatal("formal address")
	}
}
