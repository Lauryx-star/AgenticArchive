package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
)

func accountError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrInvalid), errors.Is(err, access.ErrCurrentPassword):
		status = http.StatusBadRequest
	case errors.Is(err, access.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, access.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, access.ErrLastAdmin), errors.Is(err, access.ErrUsernameExists):
		status = http.StatusConflict
	default:
		err = fmt.Errorf("Benutzerkonto konnte nicht aktualisiert werden.")
	}
	apiError(w, status, err)
}

func (a *authService) passwordAction(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.loginMu.TryLock() {
			apiError(w, 429, fmt.Errorf("Passwortverarbeitung beschäftigt. Bitte kurz warten."))
			return
		}
		defer a.loginMu.Unlock()
		if !a.allowAttempt(time.Now()) {
			w.Header().Set("Retry-After", "60")
			apiError(w, 429, fmt.Errorf("Zu viele Passwortaktionen. Bitte eine Minute warten."))
			return
		}
		next(w, r)
	}
}

func targetUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		apiError(w, 400, fmt.Errorf("Ungültige Benutzer-ID."))
		return 0, false
	}
	return id, true
}

func (a *authService) userRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/users", requirePermission(access.ManageUsers, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		users, err := a.store.Users(r.Context())
		if err != nil {
			accountError(w, err)
			return
		}
		writeJSON(w, map[string]any{"users": users})
	})))
	mux.Handle("POST /api/users", requirePermission(access.ManageUsers, a.passwordAction(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if !decodeAccountJSON(w, r, &input) {
			return
		}
		actor := r.Context().Value(sessionContextKey{}).(access.Session).User
		user, err := a.store.CreateUser(r.Context(), actor.ID, input.Username, input.Password, input.Role)
		if err != nil {
			accountError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, user)
	})))
	mux.Handle("PUT /api/users/{id}/role", requirePermission(access.ManageUsers, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := targetUserID(w, r)
		if !ok {
			return
		}
		var input struct {
			Role string `json:"role"`
		}
		if !decodeAccountJSON(w, r, &input) {
			return
		}
		actor := r.Context().Value(sessionContextKey{}).(access.Session).User
		user, err := a.store.ChangeRole(r.Context(), actor.ID, id, input.Role)
		if err != nil {
			accountError(w, err)
			return
		}
		writeJSON(w, user)
	})))
	mux.Handle("PUT /api/users/{id}/password", requirePermission(access.ManageUsers, a.passwordAction(func(w http.ResponseWriter, r *http.Request) {
		id, ok := targetUserID(w, r)
		if !ok {
			return
		}
		var input struct {
			Password string `json:"password"`
		}
		if !decodeAccountJSON(w, r, &input) {
			return
		}
		actor := r.Context().Value(sessionContextKey{}).(access.Session).User
		if err := a.store.ResetUserPassword(r.Context(), actor.ID, id, input.Password); err != nil {
			accountError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"password_reset": true})
	})))
	mux.HandleFunc("PUT /api/auth/password", a.passwordAction(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if !decodeAccountJSON(w, r, &input) {
			return
		}
		actor := r.Context().Value(sessionContextKey{}).(access.Session).User
		if err := a.store.ChangePassword(r.Context(), actor.ID, input.CurrentPassword, input.NewPassword); err != nil {
			accountError(w, err)
			return
		}
		a.cookies(w, "", "", time.Unix(1, 0), -1)
		writeJSON(w, map[string]bool{"password_changed": true})
	}))
}
