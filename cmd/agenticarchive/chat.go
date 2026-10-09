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
	client := &http.Client{Timeout: 115 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			apiError(w, 400, fmt.Errorf("Ungültige Chat-Anfrage."))
			return
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
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
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
		data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
		if err != nil || len(data) > 256<<10 {
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
		if json.Unmarshal(data, &answer) != nil || strings.TrimSpace(answer.Text) == "" || len(answer.Text) > 32000 || len(answer.Sources) > 16 {
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
		writeJSON(w, answer)
	})
}
