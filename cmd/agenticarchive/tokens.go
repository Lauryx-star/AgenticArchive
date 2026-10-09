package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
)

func (a *authService) tokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
		tokens, err := a.store.AgentTokens(r.Context(), user.ID, time.Now())
		if err != nil {
			accountError(w, err)
			return
		}
		writeJSON(w, map[string]any{"tokens": tokens})
	})
	mux.HandleFunc("POST /api/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
			Days int    `json:"days"`
		}
		if !decodeAccountJSON(w, r, &input) {
			return
		}
		if input.Days < 1 || input.Days > 365 {
			apiError(w, 400, fmt.Errorf("Laufzeit muss 1–365 Tage sein."))
			return
		}
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
		token, raw, err := a.store.CreateAgentToken(r.Context(), user, input.Name, time.Duration(input.Days)*24*time.Hour, false, time.Now())
		if err != nil {
			accountError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(201)
		writeJSON(w, map[string]any{"token": token, "secret": raw})
	})
	mux.HandleFunc("DELETE /api/auth/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := targetUserID(w, r)
		if !ok {
			return
		}
		user := r.Context().Value(sessionContextKey{}).(access.Session).User
		if err := a.store.RevokeAgentToken(r.Context(), user.ID, id); err != nil {
			accountError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"revoked": true})
	})
}
