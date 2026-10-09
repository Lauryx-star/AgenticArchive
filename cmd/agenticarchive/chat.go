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
	savedChatRoutes(mux, auth, store)
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
		var incoming struct {
			agent.Request
			ChatID  int64 `json:"chat_id"`
			Version int64 `json:"version"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&incoming) != nil || decoder.Decode(new(any)) != io.EOF {
			apiError(w, 400, fmt.Errorf("Ungültige Chat-Anfrage."))
			return
		}
		input := incoming.Request
		if len(input.Messages) == 0 {
			apiError(w, 400, fmt.Errorf("Bitte eine Frage eingeben."))
			return
		}
		question := input.Messages[len(input.Messages)-1]
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
		// The browser cannot inject source receipts or replace the signed past.
		for _, message := range input.Messages {
			if len(message.Sources) != 0 {
				apiError(w, 400, fmt.Errorf("Quellen müssen aus dem Recherchekontext stammen."))
				return
			}
		}
		var saved access.Chat
		if incoming.ChatID != 0 {
			if incoming.ChatID < 1 || incoming.Version < 1 || input.Context != "" || len(input.Messages) != 1 || question.Role != "user" {
				apiError(w, 400, fmt.Errorf("Ungültige Chat-Fortsetzung."))
				return
			}
			var err error
			saved, err = auth.store.ChatState(r.Context(), user.ID, incoming.ChatID)
			if err != nil {
				chatError(w, err)
				return
			}
			if saved.Version != incoming.Version {
				chatError(w, access.ErrChatConflict)
				return
			}
			input.Messages = append(append([]agent.Message{}, saved.Working...), question)
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
		if saved.Notes != "" {
			input.Messages[len(input.Messages)-1].Content += "\n\nEigene offene Recherchefragen und Hinweise (Benutzernotizen):\n" + saved.Notes
		}
		for _, source := range chatHistorySources(input.Messages) {
			doc, err := store.Document(r.Context(), source.DocumentID)
			if err != nil || source.Page < 1 || source.Page > doc.Pages || (source.Revision != "" && source.Revision != doc.Fingerprint) {
				if input.Mode == "summary" {
					input.Messages[len(input.Messages)-1].Content += fmt.Sprintf("\nHinweis zum historischen Ergebnis: [Dokument %d, Seite %d] fehlt oder wurde verändert. Verwende nur die früher festgehaltenen Ergebnisse und kennzeichne diesen Bezugsstand.", source.DocumentID, source.Page)
					continue
				}
				apiError(w, 400, fmt.Errorf("Eine bisherige Quelle fehlt oder wurde verändert. Deine gespeicherten Ergebnisse bleiben erhalten. Für historische Ergebnisse wähle Zusammenführen; für neue Belege beginne einen neuen Chat."))
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
		if saved.ID == 0 {
			title := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(question.Content), "\n", " "), "\r", " ")
			chars := []rune(title)
			if len(chars) > 120 {
				title = string(chars[:120])
			}
			var err error
			saved, err = auth.store.CreateChat(r.Context(), user.ID, title, input.Messages[:len(input.Messages)-1], nil, time.Now())
			if err != nil {
				chatError(w, err)
				return
			}
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
		previousSources := chatHistorySources(input.Messages)
		for i, source := range answer.Sources {
			doc, err := store.Document(r.Context(), source.DocumentID)
			unavailable := err != nil || source.Page < 1 || source.Page > doc.Pages || (source.Revision != "" && source.Revision != doc.Fingerprint)
			if unavailable {
				known := false
				for _, old := range previousSources {
					if old.DocumentID == source.DocumentID && old.Page == source.Page && old.Revision == source.Revision {
						answer.Sources[i] = old
						answer.Sources[i].Unavailable = true
						known = true
						break
					}
				}
				if input.Mode != "summary" || !known {
					apiError(w, 502, fmt.Errorf("Quellenverweis ist nicht mehr aktuell oder verfügbar."))
					return
				}
			} else {
				answer.Sources[i].Path = doc.Path
				answer.Sources[i].Revision = doc.Fingerprint
				answer.Sources[i].Unavailable = false
			}
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
		version, err := auth.store.SaveChatTurn(r.Context(), user.ID, saved.ID, saved.Version, question, answer, history, time.Now())
		if err != nil {
			chatError(w, err)
			return
		}
		writeJSON(w, map[string]any{"chat_id": saved.ID, "version": version, "text": answer.Text, "sources": answer.Sources, "model": answer.Model, "context": signChatMemory(key, user.ID, history, time.Now()), "compacted": answer.Compacted})
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
