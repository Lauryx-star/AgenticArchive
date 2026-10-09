package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmptySearchContinuesToVariantsAndReadEvidence(t *testing.T) {
	for _, premature := range []string{"Ich werde eine andere Strategie anwenden. Ich starte mit der Suche nach Dateinamen.", "Keine passenden Dokumente gefunden."} {
		t.Run(premature, func(t *testing.T) {
			modelCalls, readCalls := 0, 0
			client := researchClient(t, func(body string) string {
				modelCalls++
				switch modelCalls {
				case 1:
					return modelTool("search_archive", `{"query":"EON Stromrechnung letzte","page":1}`)
				case 2:
					return modelText(premature)
				case 3:
					if !strings.Contains(body, "noch kein Rechercheergebnis") || strings.Contains(body, premature) {
						t.Fatal("missing correction or premature answer retained")
					}
					return modelTool("search_archive", `{"query":"EON","page":1}`)
				case 4:
					return modelTool("read_page", `{"document_id":7,"page":1,"offset":0}`)
				case 5:
					return modelText("Rechnungsdatum: 12.09.2026 [Dokument 7, Seite 1]. Ein Erhaltsdatum ist nicht belegt.")
				default:
					t.Fatal("unbounded model loop")
					return ""
				}
			}, &readCalls)
			runner := Runner{Client: client, Model: "test", Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
			answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Wann habe ich die letzte Stromrechnung erhalten? Mein Anbieter ist EON"}}})
			if err != nil || modelCalls != 5 || readCalls != 1 || len(answer.Sources) != 1 || !strings.Contains(answer.Text, "12.09.2026") {
				t.Fatalf("research failed: %+v %v (%d calls)", answer, err, modelCalls)
			}
		})
	}
}

func TestResearchCorrectionsAreBounded(t *testing.T) {
	modelCalls, readCalls := 0, 0
	client := researchClient(t, func(body string) string {
		modelCalls++
		if modelCalls == 1 {
			return modelTool("search_archive", `{"query":"EON Stromrechnung letzte","page":1}`)
		}
		return modelText("Ich werde nun die Dokumente lesen.")
	}, &readCalls)
	runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Suche meine Rechnungen"}}})
	if err == nil || modelCalls != 4 || answer.Text != "" || readCalls != 0 {
		t.Fatalf("unfinished research accepted or unbounded: %+v %v %d", answer, err, modelCalls)
	}
}

func TestToolFailuresAndUnreadCandidatesCannotFinishResearch(t *testing.T) {
	for _, failure := range []bool{true, false} {
		modelCalls, readCalls := 0, 0
		client := researchClient(t, func(body string) string {
			modelCalls++
			switch modelCalls {
			case 1:
				if failure {
					return modelTool("list_documents", `{"path":"EON"}`)
				}
				return modelTool("search_archive", `{"query":"EON","page":1}`)
			case 2:
				return modelText("Die Anfrage lieferte noch kein belastbares Ergebnis.")
			case 3:
				if !strings.Contains(body, "noch kein Rechercheergebnis") {
					t.Fatal("failed tool or unread candidate accepted as final answer")
				}
				if failure {
					return modelTool("search_archive", `{"query":"EON","page":1}`)
				}
				return modelTool("read_page", `{"document_id":7,"page":1,"offset":0}`)
			case 4:
				if failure {
					return modelTool("read_page", `{"document_id":7,"page":1,"offset":0}`)
				}
				return modelText("Rechnungsdatum 12.09.2026 [Dokument 7, Seite 1]")
			case 5:
				return modelText("Rechnungsdatum 12.09.2026 [Dokument 7, Seite 1]")
			default:
				t.Fatal("unbounded recovery")
				return ""
			}
		}, &readCalls)
		runner := Runner{Client: client, Provider: "ollama", APIURL: "http://model.invalid/responses", MCPURL: "http://archive.invalid/mcp"}
		answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Wann kam meine Rechnung?"}}})
		if err != nil || readCalls != 1 || len(answer.Sources) != 1 {
			t.Fatalf("recovery failed: %+v %v", answer, err)
		}
	}
}

func TestReportedContinuationIsNotAFinalAnswer(t *testing.T) {
	text := `Die Suche nach Dokumenten mit dem Pfadfilter "EON" hat leider zu einem Fehler geführt. Probieren wir stattdessen, nach Dokumenten mit dem Begriff "Stromrechnung" im Dateinamen zu suchen, um mögliche Rechnungen von EON zu finden.`
	if !announcesResearch(text) {
		t.Fatal("reported unfinished response accepted")
	}
}

func modelTool(name, arguments string) string {
	data, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "name": name, "call_id": "call-" + name, "arguments": arguments}}})
	return string(data)
}

func modelText(text string) string {
	data, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}})
	return string(data)
}

func researchClient(t *testing.T, model func(string) string, readCalls *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		status, result := 200, ""
		if r.URL.Host == "model.invalid" {
			result = model(string(body))
		} else {
			var request struct {
				Method string `json:"method"`
				Params struct {
					Name      string `json:"name"`
					Arguments struct {
						Query      string `json:"query"`
						DocumentID int64  `json:"document_id"`
						Offset     int    `json:"offset"`
					} `json:"arguments"`
				} `json:"params"`
			}
			json.Unmarshal(body, &request)
			switch request.Method {
			case "initialize":
				result = `{"result":{}}`
			case "notifications/initialized":
				status = 202
			case "tools/list":
				result = `{"result":{"tools":[{"name":"search_archive","inputSchema":{}},{"name":"list_documents","inputSchema":{}},{"name":"read_page","inputSchema":{}}]}}`
			case "tools/call":
				text := `{"total":0,"hits":[]}`
				if request.Params.Name == "read_page" {
					*readCalls++
					data, _ := json.Marshal(map[string]any{"document_id": request.Params.Arguments.DocumentID, "page": 1, "path": "Stromrechnung.pdf", "text": "EON Rechnungsdatum 12.09.2026", "offset": request.Params.Arguments.Offset})
					text = string(data)
				} else if request.Params.Arguments.Query == "EON" {
					text = `{"total":1,"hits":[{"document_id":7,"page":1,"path":"Stromrechnung.pdf"}]}`
				}
				isError := request.Params.Name == "list_documents"
				if isError {
					text = "Ungültige Dokumentparameter."
				}
				data, _ := json.Marshal(map[string]any{"result": map[string]any{"isError": isError, "content": []any{map[string]any{"type": "text", "text": text}}}})
				result = string(data)
			default:
				t.Fatal("unexpected method", request.Method)
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
	})}
}
