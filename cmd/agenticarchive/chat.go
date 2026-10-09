package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
	"github.com/Lauryx-star/AgenticArchive/internal/agent"
	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

func chatRoutes(mux *http.ServeMux, auth *authService, store *archive.Store) {
	endpoint := strings.TrimRight(os.Getenv("AGENT_URL"), "/")
	key, keyErr := agent.Secret("AGENT_SERVICE_KEY")
	u, err := url.Parse(endpoint)
	configured := keyErr == nil && len(key) >= 32 && err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
	client := &http.Client{Timeout: agent.ResearchTimeout + 5*time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	slots := make(chan struct{}, 2)
	mux.HandleFunc("GET /api/chat/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"configured": configured}) })
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		if !configured {
			apiError(w, 503, fmt.Errorf("Der Archiv-Agent ist noch nicht eingerichtet."))
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			apiError(w, 415, fmt.Errorf("application/json erforderlich."))
			return
		}
		var input agent.Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			apiError(w, 400, fmt.Errorf("Ungültige Chat-Anfrage."))
			return
		}
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
		// The browser cannot inject source receipts or replace the signed past.
		for _, message := range input.Messages {
			if len(message.Sources) != 0 {
				apiError(w, 400, fmt.Errorf("Quellen müssen aus dem Recherchekontext stammen."))
				return
			}
		}
		if input.Context != "" {
			if len(input.Messages) != 1 || input.Messages[0].Role != "user" {
				apiError(w, 400, fmt.Errorf("Ungültige Fortsetzung."))
				return
			}
			past, err := readChatMemory(key, input.Context, user.ID, time.Now())
			if err != nil {
				apiError(w, 400, err)
				return
			}
			input.Messages = append(past, input.Messages...)
			input.Context = ""
		}
		for _, source := range chatHistorySources(input.Messages) {
			doc, err := store.Document(r.Context(), source.DocumentID)
			if err != nil || source.Page < 1 || source.Page > doc.Pages {
				apiError(w, 400, fmt.Errorf("Eine bisherige Quelle ist nicht mehr verfügbar. Bitte beginne einen neuen Chat."))
				return
			}
		}
		if err := agent.ValidateRequest(input); err != nil {
			apiError(w, 400, err)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			apiError(w, 429, fmt.Errorf("Agent beschäftigt. Bitte kurz warten."))
			return
		}
		token, raw, err := auth.store.CreateAgentToken(r.Context(), user, "Webchat", 5*time.Minute, true, time.Now())
		if err != nil {
			accountError(w, err)
			return
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			auth.store.RevokeAgentToken(ctx, user.ID, token.ID)
		}()
		body, _ := json.Marshal(input)
		request, err := http.NewRequestWithContext(r.Context(), "POST", endpoint+"/chat", bytes.NewReader(body))
		if err != nil {
			apiError(w, 503, fmt.Errorf("Agent-Adresse ungültig."))
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("X-Archive-Token", raw)
		response, err := client.Do(request)
		if err != nil {
			apiError(w, 503, fmt.Errorf("Agent nicht erreichbar. Bitte später erneut versuchen."))
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
		if err != nil || len(data) > 512<<10 {
			apiError(w, 502, fmt.Errorf("Agent-Antwort ungültig."))
			return
		}
		if response.StatusCode != 200 {
			var failure struct {
				Error string `json:"error"`
			}
			json.Unmarshal(data, &failure)
			if failure.Error == "" || len(failure.Error) > 500 {
				failure.Error = "Agent-Anfrage fehlgeschlagen."
			}
			status := 502
			if response.StatusCode == 429 {
				status = 429
			}
			apiError(w, status, fmt.Errorf("%s", failure.Error))
			return
		}
		// A revoked session/password also suppresses a response already in flight.
		cookie, _ := r.Cookie(sessionCookie)
		if _, err := auth.store.Session(r.Context(), cookie.Value, time.Now()); err != nil {
			apiError(w, 401, fmt.Errorf("Bitte erneut anmelden."))
			return
		}
		var answer agent.Answer
		if json.Unmarshal(data, &answer) != nil || strings.TrimSpace(answer.Text) == "" || len(answer.Text) > 32000 || len(answer.Sources) > 512 {
			apiError(w, 502, fmt.Errorf("Agent-Antwort ungültig."))
			return
		}
		for i, source := range answer.Sources {
			doc, err := store.Document(r.Context(), source.DocumentID)
			if err != nil || source.Page < 1 || source.Page > doc.Pages {
				apiError(w, 502, fmt.Errorf("Quellenverweis ist nicht mehr verfügbar."))
				return
			}
			answer.Sources[i].Path = doc.Path
		}
		// Rebuild the last answer with server-resolved paths before sealing context.
		history := answer.History
		if len(history) == 0 {
			history = append(input.Messages, agent.Message{Role: "assistant", Content: answer.Text})
		}
		history[len(history)-1] = agent.Message{Role: "assistant", Content: answer.Text, Sources: answer.Sources}
		if err := agent.ValidateRequest(agent.Request{Messages: append(append([]agent.Message{}, history...), agent.Message{Role: "user", Content: "Fortsetzung"})}); err != nil {
			apiError(w, 502, fmt.Errorf("Recherchekontext zu groß. Bitte beginne einen neuen Chat."))
			return
		}
		writeJSON(w, map[string]any{"text": answer.Text, "sources": answer.Sources, "model": answer.Model, "context": signChatMemory(key, user.ID, history, time.Now()), "compacted": answer.Compacted})
	})
}

func chatHistorySources(messages []agent.Message) []agent.Source {
	sources := []agent.Source{}
	seen := map[[2]int64]bool{}
	for _, message := range messages {
		for _, source := range message.Sources {
			key := [2]int64{source.DocumentID, int64(source.Page)}
			if !seen[key] {
				sources = append(sources, source)
				seen[key] = true
			}
		}
	}
	return sources
}
