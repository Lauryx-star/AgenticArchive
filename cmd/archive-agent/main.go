package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

func main() {
	config, err := modelConfig()
	if err != nil {
		log.Fatal(err)
	}
	serviceKey, err := agent.Secret("AGENT_SERVICE_KEY")
	if err != nil || len(serviceKey) < 32 {
		log.Fatal("Agent-Dienstschlüssel fehlt oder ist zu kurz.")
	}
	mcp := os.Getenv("ARCHIVE_MCP_URL")
	if mcp == "" {
		mcp = "http://agenticarchive:8080/mcp"
	}
	u, err := url.Parse(mcp)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		log.Fatal("Ungültige Archiv-Adresse.")
	}
	client := &http.Client{Timeout: agent.ProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	runner := &agent.Runner{Client: client, Model: config.model, APIKey: config.key, APIURL: config.endpoint, MCPURL: mcp, Provider: config.provider}
	slots := make(chan struct{}, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(status int, message string) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": message})
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(raw), []byte(serviceKey)) != 1 {
			fail(401, "Agent-Dienstzugang ungültig.")
			return
		}
		readToken := r.Header.Get("X-Archive-Token")
		if len(readToken) != 67 {
			fail(401, "Archiv-Token erforderlich.")
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			fail(415, "application/json erforderlich.")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			fail(429, "Agent beschäftigt. Bitte kurz warten.")
			return
		}
		var request agent.Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			fail(400, "Ungültige Chat-Anfrage.")
			return
		}
		if err := agent.ValidateRequest(request); err != nil {
			fail(400, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), agent.ResearchTimeout)
		defer cancel()
		answer, err := runner.Run(ctx, readToken, request)
		if err != nil {
			fail(502, err.Error())
			return
		}
		json.NewEncoder(w).Encode(answer)
	})
	listen := os.Getenv("AGENT_LISTEN_ADDR")
	if listen == "" {
		listen = "0.0.0.0:8081"
	}
	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: agent.ResearchTimeout + 10*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	fmt.Printf("Archiv-Agent bereit auf %s (Modell %s)\n", listen, config.model)
	log.Fatal(server.ListenAndServe())
}

type modelSettings struct{ provider, model, endpoint, key string }

func modelConfig() (modelSettings, error) {
	c := modelSettings{provider: os.Getenv("LLM_PROVIDER"), model: os.Getenv("OPENAI_MODEL"), endpoint: os.Getenv("OPENAI_API_URL")}
	if c.provider == "" {
		c.provider = "openai"
	}
	switch c.provider {
	case "openai":
		if c.model == "" {
			c.model = "gpt-5.4-mini"
		}
		if c.endpoint == "" {
			c.endpoint = "https://api.openai.com/v1/responses"
		}
		key, err := agent.Secret("OPENAI_API_KEY")
		if err != nil || key == "" {
			return c, fmt.Errorf("OpenAI-API-Schlüssel fehlt oder ist nicht lesbar.")
		}
		c.key = key
	case "ollama":
		if c.model == "" {
			c.model = "qwen3:8b"
		}
		if c.endpoint == "" {
			c.endpoint = "http://host.docker.internal:11434/v1/responses"
		}
		// Never read or forward an existing OpenAI key to a local model server.
	default:
		return c, fmt.Errorf("LLM_PROVIDER muss openai oder ollama sein.")
	}
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("Ungültige Modell-Adresse.")
	}
	if c.provider == "openai" && u.Scheme != "https" {
		return c, fmt.Errorf("Modell-Adresse mit API-Schlüssel muss HTTPS verwenden.")
	}
	return c, nil
}
