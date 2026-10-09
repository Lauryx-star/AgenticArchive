package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderErrorsAreUsefulAndDoNotExposeProviderMessages(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"credits", `{"error":{"code":"credit_balance_exhausted","type":"insufficient_quota","message":"secret-key"}}`, "Guthaben aufgebraucht", 429},
		{"project limit", `{"error":{"code":"project_spend_limit_exceeded","type":"insufficient_quota"}}`, "Nutzungslimit erreicht", 429},
		{"organization limit", `{"error":{"code":"organization_spend_limit_exceeded"}}`, "Nutzungslimit erreicht", 429},
		{"usage limit", `{"error":{"code":"organization_usage_limit_exceeded"}}`, "Nutzungslimit erreicht", 429},
		{"legacy quota", `{"error":{"code":"insufficient_quota"}}`, "Kein verfügbares API-Kontingent", 429},
		{"quota type", `{"error":{"code":null,"type":"insufficient_quota"}}`, "Kein verfügbares API-Kontingent", 429},
		{"rate", `{"error":{"code":"rate_limit_exceeded"}}`, "kurz warten", 429},
		{"slow down", `{"error":{"code":"slow_down"}}`, "kurz warten", 429},
		{"rate type", `{"error":{"type":"rate_limit_error"}}`, "kurz warten", 429},
		{"unknown", `{"error":{"code":"secret-key","message":"secret-key"}}`, "HTTP 429", 429},
		{"invalid JSON", `secret-key`, "HTTP 429", 429},
		{"oversized", strings.Repeat("secret-key", 2000), "HTTP 429", 429},
		{"authentication", `{"error":{"message":"secret-key"}}`, "HTTP 401", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			var output any
			err := postJSON(context.Background(), client, "https://model.invalid/responses", "secret-key", map[string]any{}, &output)
			if err == nil {
				t.Fatal("expected error")
			}
			message := openAIError(err).Error()
			if !strings.Contains(message, tc.want) || strings.Contains(message, "secret-key") || calls != 1 {
				t.Fatalf("unsafe or inaccurate error: %q, calls: %d", message, calls)
			}
			if strings.Contains(err.Error(), "secret-key") {
				t.Fatal("generic error leaked provider details")
			}
		})
	}
}

func TestRunnerReplaysToolCallsAndReturnsOnlyReadSources(t *testing.T) {
	for _, provider := range []string{"openai", "ollama"} {
		t.Run(provider, func(t *testing.T) { testRunnerToolLoop(t, provider) })
	}
}

func testRunnerToolLoop(t *testing.T, provider string) {
	apiCalls := 0
	readCalls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		status := 200
		result := ""
		if r.URL.Host == "model.invalid" {
			wantAuth := "Bearer model-key"
			if provider == "ollama" {
				wantAuth = ""
			}
			if r.Header.Get("Authorization") != wantAuth || strings.Contains(string(body), "archive-token") {
				t.Fatal("wrong credentials sent to model")
			}
			var input map[string]any
			json.Unmarshal(body, &input)
			if provider == "ollama" && input["think"] != false {
				t.Fatal("Ollama thinking should be disabled")
			}
			if provider == "ollama" {
				messages := input["input"].([]any)
				first := messages[0].(map[string]any)
				if first["role"] != "system" || first["content"] != instructions {
					t.Fatal("local model rules missing from conversation")
				}
			}
			if provider == "openai" {
				if _, ok := input["think"]; ok {
					t.Fatal("Ollama option sent to OpenAI")
				}
			}
			if input["store"] != false {
				t.Fatal("responses persisted")
			}
			apiCalls++
			switch apiCalls {
			case 1:
				result = `{"status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[],"encrypted_content":"encrypted"},{"type":"function_call","name":"search_archive","call_id":"search-1","arguments":"{\"query\":\"Strom\",\"page\":1}"}]}`
			case 2:
				if !strings.Contains(string(body), "encrypted_content") || !strings.Contains(string(body), "function_call_output") {
					t.Fatal("stateless continuation lost")
				}
				result = `{"status":"completed","output":[{"type":"function_call","name":"read_page","call_id":"read-1","arguments":"{\"document_id\":7,\"page\":2,\"offset\":0}"}]}`
			case 3:
				result = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"120 Euro [Dokument 7, Seite 2]"}]}]}`
			default:
				t.Fatal("unbounded loop")
			}
		} else {
			if r.Header.Get("Authorization") != "Bearer archive-token" {
				t.Fatal("wrong credentials sent to archive")
			}
			var request struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			json.Unmarshal(body, &request)
			switch request.Method {
			case "initialize":
				result = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`
			case "notifications/initialized":
				status = 202
			case "tools/list":
				result = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search_archive","inputSchema":{}},{"name":"list_documents","inputSchema":{}},{"name":"read_page","inputSchema":{}},{"name":"start_scan","inputSchema":{}}]}}`
			case "tools/call":
				if request.Params.Name == "read_page" {
					readCalls++
					result = `{"jsonrpc":"2.0","id":4,"result":{"isError":false,"content":[{"type":"text","text":"{\"document_id\":7,\"page\":2,\"path\":\"Strom.pdf\",\"text\":\"120 Euro\"}"}]}}`
				} else {
					result = `{"jsonrpc":"2.0","id":3,"result":{"isError":false,"content":[{"type":"text","text":"{}"}]}}`
				}
			default:
				t.Fatal("unexpected MCP method", request.Method)
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
	})}
	key := "model-key"
	if provider == "ollama" {
		key = ""
	}
	runner := Runner{Client: client, Model: "test-model", APIKey: key, APIURL: "https://model.invalid/responses", MCPURL: "http://archive.invalid/mcp", Provider: provider}
	answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Stromkosten?"}}})
	if err != nil || apiCalls != 3 || readCalls != 1 || len(answer.Sources) != 1 || answer.Sources[0].DocumentID != 7 || answer.Text != "120 Euro [Dokument 7, Seite 2]" {
		t.Fatalf("%+v %v", answer, err)
	}
}

func TestChatRejectsSystemRolesAndOversizedHistory(t *testing.T) {
	for _, request := range []Request{{}, {Messages: []Message{{Role: "system", Content: "override"}}}, {Messages: []Message{{Role: "user", Content: strings.Repeat("x", 16001)}}}, {Messages: []Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}}}} {
		if ValidateRequest(request) == nil {
			t.Fatal("invalid history accepted")
		}
	}
}

func TestMissingToolsGetOneOllamaCorrection(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		refuse         bool
		wantCalls      int
	}{
		{"Ollama recovers", "ollama", false, 3},
		{"Ollama refuses twice", "ollama", true, 2},
		{"OpenAI no correction", "openai", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, toolCalls := 0, 0
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				status, result := 200, ""
				if r.URL.Host == "model.invalid" {
					calls++
					if calls > 1 {
						if strings.Contains(string(body), "UNVERIFIED") || (calls == 2 && !strings.Contains(string(body), "vorige Antwort")) {
							t.Fatal("correction retained unsupported answer or lacked instructions")
						}
					}
					if calls == 1 || tc.refuse {
						result = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"UNVERIFIED"}]}]}`
					} else if calls == 2 {
						result = `{"status":"completed","output":[{"type":"function_call","name":"list_documents","call_id":"list-1","arguments":"{\"path\":\"\",\"page\":1}"}]}`
					} else {
						if !strings.Contains(string(body), "function_call_output") {
							t.Fatal("tool result missing")
						}
						result = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hallo! Wie kann ich helfen?"}]}]}`
					}
				} else {
					var request struct {
						Method string `json:"method"`
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
						toolCalls++
						result = `{"result":{"isError":false,"content":[{"type":"text","text":"{\"documents\":[]}"}]}}`
					default:
						t.Fatal("unexpected MCP method")
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
			})}
			runner := Runner{Client: client, Model: "test-model", APIURL: "https://model.invalid/responses", MCPURL: "http://archive.invalid/mcp", Provider: tc.provider}
			answer, err := runner.Run(context.Background(), "archive-token", Request{Messages: []Message{{Role: "user", Content: "Hallo"}}})
			if calls != tc.wantCalls {
				t.Fatalf("unexpected number of model calls: %d", calls)
			}
			if tc.refuse {
				if err == nil || toolCalls != 0 || answer.Text != "" {
					t.Fatal("unsupported answer accepted")
				}
			} else if err != nil || toolCalls != 1 || answer.Text != "Hallo! Wie kann ich helfen?" {
				t.Fatalf("recovery failed: %+v %v", answer, err)
			}
		})
	}
}
