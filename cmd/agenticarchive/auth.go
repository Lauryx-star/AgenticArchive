package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
)

const sessionCookie = "archive_session"
const csrfCookie = "archive_csrf"

type sessionContextKey struct{}
type authService struct {
	store     *access.Store
	secure    bool
	setupCode string
	setupPath string
	loginMu   sync.Mutex
	budgetMu  sync.Mutex
	window    time.Time
	attempts  int
}

func newAuth(store *access.Store, data string, secure bool) (*authService, error) {
	a := &authService{store: store, secure: secure, setupPath: filepath.Join(data, "setup-code.txt")}
	configured, err := store.Configured(context.Background())
	if err != nil {
		return nil, err
	}
	if configured {
		os.Remove(a.setupPath)
		return a, nil
	}
	code, err := access.RandomToken()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(a.setupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = io.WriteString(f, code+"\n")
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	} else if errors.Is(err, os.ErrExist) {
		var b []byte
		b, err = os.ReadFile(a.setupPath)
		code = strings.TrimSpace(string(b))
	}
	if err != nil {
		return nil, err
	}
	if len(code) != 64 {
		return nil, fmt.Errorf("invalid setup code file: %s", a.setupPath)
	}
	a.setupCode = code
	return a, nil
}

func (a *authService) routes(mux *http.ServeMux) {
	a.userRoutes(mux)
	a.tokenRoutes(mux)
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("ui/login.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/auth/state", func(w http.ResponseWriter, r *http.Request) {
		configured, err := a.store.Configured(r.Context())
		if err != nil {
			apiError(w, 500, fmt.Errorf("Kontostatus nicht verfügbar."))
			return
		}
		writeJSON(w, map[string]bool{"setup_required": !configured})
	})
	mux.HandleFunc("POST /api/auth/setup", a.setup)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(sessionContextKey{}).(access.Session)
		cookie, err := r.Cookie(csrfCookie)
		if err != nil || !session.ValidCSRF(cookie.Value) {
			apiError(w, 401, fmt.Errorf("Bitte erneut anmelden."))
			return
		}
		writeJSON(w, map[string]any{"user": session.User, "csrf_token": cookie.Value, "permissions": map[string]bool{
			"read_archive": session.User.Can(access.ReadArchive), "manage_settings": session.User.Can(access.ManageSettings), "manage_imports": session.User.Can(access.ManageImports), "manage_users": session.User.Can(access.ManageUsers),
		}})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(sessionCookie)
		if err := a.store.Logout(r.Context(), cookie.Value); err != nil {
			apiError(w, 500, fmt.Errorf("Abmelden fehlgeschlagen."))
			return
		}
		a.cookies(w, "", "", time.Unix(1, 0), -1)
		writeJSON(w, map[string]bool{"logged_out": true})
	})
}

func (a *authService) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		scheme := "http"
		if r.TLS != nil || a.secure {
			scheme = "https"
		}
		return origin == scheme+"://"+r.Host
	}
	return true
}

func (a *authService) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if r.URL.Path == "/mcp" {
			if !a.sameOrigin(r) {
				apiError(w, 403, fmt.Errorf("Anfrage von fremder Herkunft abgelehnt."))
				return
			}
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="archive-mcp"`)
				apiError(w, 401, fmt.Errorf("Agent-Token erforderlich."))
				return
			}
			principal, err := a.store.AgentToken(r.Context(), raw, time.Now())
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="archive-mcp"`)
				apiError(w, 401, fmt.Errorf("Agent-Token ungültig oder abgelaufen."))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenContextKey{}, principal)))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !a.sameOrigin(r) {
			apiError(w, 403, fmt.Errorf("Anfrage von fremder Herkunft abgelehnt."))
			return
		}
		public := r.Method == "GET" || r.Method == "HEAD"
		public = public && (r.URL.Path == "/login" || r.URL.Path == "/login.js" || r.URL.Path == "/style.css" || r.URL.Path == "/api/auth/state")
		public = public || (r.Method == "POST" && (r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/setup"))
		if public {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		var session access.Session
		if err == nil {
			session, err = a.store.Session(r.Context(), cookie.Value, time.Now())
		}
		if err != nil {
			if !errors.Is(err, http.ErrNoCookie) && !errors.Is(err, sql.ErrNoRows) {
				apiError(w, 503, fmt.Errorf("Anmeldung vorübergehend nicht verfügbar."))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				apiError(w, 401, fmt.Errorf("Bitte anmelden."))
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !session.ValidCSRF(r.Header.Get("X-CSRF-Token")) {
			apiError(w, 403, fmt.Errorf("Ungültiger Sicherheitstoken. Bitte erneut anmelden."))
			return
		}
		if !session.User.Can(access.ReadArchive) {
			apiError(w, 403, fmt.Errorf("Keine Leseberechtigung."))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func requirePermission(permission access.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := r.Context().Value(sessionContextKey{}).(access.Session)
		if !ok || !session.User.Can(permission) {
			apiError(w, 403, fmt.Errorf("Für diese Aktion fehlt die Berechtigung."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Limit password work globally, including requests through a reverse proxy. Never trust forwarded IP headers.
func (a *authService) allowAttempt(now time.Time) bool {
	a.budgetMu.Lock()
	defer a.budgetMu.Unlock()
	if a.window.IsZero() || now.Sub(a.window) >= time.Minute {
		a.window = now
		a.attempts = 0
	}
	if a.attempts >= 10 {
		return false
	}
	a.attempts++
	return true
}

func (a *authService) credentials(w http.ResponseWriter, r *http.Request, input any) bool {
	if !a.allowAttempt(time.Now()) {
		w.Header().Set("Retry-After", "60")
		apiError(w, 429, fmt.Errorf("Zu viele Anmeldeversuche. Bitte eine Minute warten."))
		return false
	}
	return decodeAccountJSON(w, r, input)
}

func decodeAccountJSON(w http.ResponseWriter, r *http.Request, input any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		apiError(w, 415, fmt.Errorf("application/json erforderlich."))
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err = d.Decode(input); err != nil {
		apiError(w, 400, fmt.Errorf("Ungültige Anmeldedaten."))
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		apiError(w, 400, fmt.Errorf("Nur ein JSON-Objekt erlaubt."))
		return false
	}
	return true
}

func (a *authService) setup(w http.ResponseWriter, r *http.Request) {
	if !a.loginMu.TryLock() {
		apiError(w, 429, fmt.Errorf("Anmeldung beschäftigt. Bitte kurz warten."))
		return
	}
	defer a.loginMu.Unlock()
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"setup_code"`
	}
	if !a.credentials(w, r, &input) {
		return
	}
	configured, err := a.store.Configured(r.Context())
	if err != nil {
		apiError(w, 500, fmt.Errorf("Kontostatus nicht verfügbar."))
		return
	}
	if configured {
		apiError(w, 409, fmt.Errorf("Konto bereits eingerichtet."))
		return
	}
	if a.setupCode == "" || subtle.ConstantTimeCompare([]byte(input.Code), []byte(a.setupCode)) != 1 {
		apiError(w, 403, fmt.Errorf("Einrichtungscode ist falsch."))
		return
	}
	if err := a.store.SetupAdmin(r.Context(), input.Username, input.Password); err != nil {
		apiError(w, 400, err)
		return
	}
	os.Remove(a.setupPath)
	a.setupCode = ""
	writeJSON(w, map[string]bool{"configured": true})
}

func (a *authService) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginMu.TryLock() {
		apiError(w, 429, fmt.Errorf("Anmeldung beschäftigt. Bitte kurz warten."))
		return
	}
	defer a.loginMu.Unlock()
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !a.credentials(w, r, &input) {
		return
	}
	if len(input.Username) > 64 || len(input.Password) > 1024 {
		apiError(w, 400, fmt.Errorf("Ungültige Anmeldedaten."))
		return
	}
	user, err := a.store.Authenticate(r.Context(), input.Username, input.Password)
	if errors.Is(err, access.ErrCredentials) {
		apiError(w, 401, access.ErrCredentials)
		return
	}
	if err != nil {
		apiError(w, 500, fmt.Errorf("Anmeldung fehlgeschlagen."))
		return
	}
	now := time.Now()
	token, csrf, err := a.store.NewSession(r.Context(), user, now)
	if err != nil {
		if errors.Is(err, access.ErrCredentials) {
			apiError(w, 401, access.ErrCredentials)
			return
		}
		apiError(w, 500, fmt.Errorf("Anmeldung fehlgeschlagen."))
		return
	}
	// Logging in again replaces and invalidates the previous session in this browser.
	if old, err := r.Cookie(sessionCookie); err == nil {
		a.store.Logout(r.Context(), old.Value)
	}
	a.cookies(w, token, csrf, now.Add(access.SessionLifetime), int(access.SessionLifetime/time.Second))
	writeJSON(w, map[string]any{"user": user, "csrf_token": csrf})
}

func (a *authService) cookies(w http.ResponseWriter, token, csrf string, expires time.Time, maxAge int) {
	for name, value := range map[string]string{sessionCookie: token, csrfCookie: csrf} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
	}
}
