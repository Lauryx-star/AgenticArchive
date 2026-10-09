package main

import "testing"

func TestModelConfig(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, endpoint, keyFile string
		invalid                                  bool
	}{
		{"OpenAI defaults", "", "", "", "", false},
		{"Ollama never reads key file", "ollama", "", "", "/nonexistent/private-key", false},
		{"custom model", "ollama", "qwen3:4b", "http://localhost:11434/v1/responses", "", false},
		{"unsupported", "unknown", "", "", "", true},
		{"embedded credentials", "ollama", "", "http://user:password@localhost/v1/responses", "", true},
		{"bad scheme", "ollama", "", "file:///tmp/model", "", true},
		{"key over HTTP", "openai", "", "http://model.invalid/v1/responses", "", true},
		{"query", "ollama", "", "http://localhost/v1/responses?key=secret", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LLM_PROVIDER", tc.provider)
			t.Setenv("OPENAI_MODEL", tc.model)
			t.Setenv("OPENAI_API_URL", tc.endpoint)
			t.Setenv("OPENAI_API_KEY", "test-key")
			t.Setenv("OPENAI_API_KEY_FILE", tc.keyFile)
			c, err := modelConfig()
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected config error: %v", err)
			}
			if tc.invalid {
				return
			}
			if tc.provider == "ollama" {
				if c.key != "" {
					t.Fatal("OpenAI key passed to Ollama")
				}
				if tc.model == "" && c.model != "qwen3:8b" {
					t.Fatal("wrong Ollama default")
				}
			} else if c.key != "test-key" || c.model != "gpt-5.4-mini" || c.endpoint != "https://api.openai.com/v1/responses" {
				t.Fatal("OpenAI defaults changed")
			}
			if tc.model != "" && c.model != tc.model {
				t.Fatal("custom model ignored")
			}
		})
	}
}
