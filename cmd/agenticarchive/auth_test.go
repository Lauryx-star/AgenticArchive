package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

const authPassword = "correct horse battery staple"

type authFixture struct {
	a     *authService
	h     http.Handler
	dir   string
	scans atomic.Int32
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	f := &authFixture{dir: t.TempDir()}
	accounts, err := access.Open(filepath.Join(f.dir, "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.Close() })
	f.a, err = newAuth(accounts, f.dir, false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(filepath.Join(f.dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test.pdf"), []byte("%PDF-private source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptSource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(f.dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO documents(id,path,size,modified,fingerprint,status,pages) VALUES(1,'test.pdf',19,'2026-10-07T00:00:00Z','test','ready',1);
INSERT INTO pages(document_id,number,text,ocr) VALUES(1,1,'PRIVATE PAGE CONTENT',0);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	scanner := &archive.Scanner{Store: store, Root: root}
	f.h = archiveHandler(store, scanner, newScanSchedule(0), root, f.dir, f.a, func(bool) { f.scans.Add(1) })
	return f
}
func (f *authFixture) request(method, path, body string, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}
func (f *authFixture) login(t *testing.T) ([]*http.Cookie, string) {
	t.Helper()
	w := f.request("POST", "/api/auth/login", `{"username":"admin","password":"`+authPassword+`"}`, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies(), result.CSRF
}

func TestActualRoutesRequireAuthenticationAndSetupCode(t *testing.T) {
	f := newAuthFixture(t)
	for _, path := range []string{"/api/status", "/api/settings", "/api/search?q=private", "/api/documents", "/api/documents/1", "/api/documents/1/pages/1", "/api/documents/1/pdf", "/api/auth/me", "/api/../api/documents/1/pdf"} {
		for _, method := range []string{"GET", "HEAD"} {
			w := f.request(method, path, "", nil, "")
			if w.Code != 401 || strings.Contains(w.Body.String(), "PRIVATE") {
				t.Fatalf("exposed %s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/scan", "/api/documents/1/retry", "/api/auth/logout"} {
		if w := f.request("POST", path, "", nil, ""); w.Code != 401 {
			t.Fatalf("anonymous mutation: %d", w.Code)
		}
	}
	for _, path := range []string{"/", "/index.html", "/app.js", "/login.html", "//api/documents/1/pdf"} {
		if w := f.request("GET", path, "", nil, ""); w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Fatalf("UI not locked: %s %d", path, w.Code)
		}
	}
	if w := f.request("GET", "/login", "", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "login-form") {
		t.Fatal("login not public")
	}
	if w := f.request("POST", "/api/auth/setup", `{"username":"admin","password":"`+authPassword+`","setup_code":"wrong"}`, nil, ""); w.Code != 403 {
		t.Fatal("setup without code accepted")
	}
	w := f.request("POST", "/api/auth/setup", `{"username":"admin","password":"`+authPassword+`","setup_code":"`+f.a.setupCode+`"}`, nil, "")
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(f.a.setupPath); !os.IsNotExist(err) {
		t.Fatal("setup code not removed")
	}
	if w := f.request("POST", "/api/auth/setup", `{"username":"other","password":"`+authPassword+`","setup_code":"wrong"}`, nil, ""); w.Code != 409 {
		t.Fatal("setup reused")
	}
	if w := f.request("GET", "/api/documents/1/pdf", "", nil, ""); w.Code != 401 {
		t.Fatal("setup unlocked anonymous access")
	}
}

func TestLoginReadAccessCSRFPermissionsAndLogout(t *testing.T) {
	f := newAuthFixture(t)
	if err := f.a.store.SetupAdmin(context.Background(), "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := f.login(t)
	for _, cookie := range cookies {
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 43200 || cookie.Path != "/" {
			t.Fatal("unsafe cookie")
		}
	}
	for _, path := range []string{"/", "/api/status", "/api/settings", "/api/documents", "/api/documents/1/pages/1", "/api/documents/1/pdf", "/api/auth/me"} {
		if w := f.request("GET", path, "", cookies, ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("read %s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, token := range []string{"", strings.Repeat("0", 64)} {
		if w := f.request("POST", "/api/scan", "", cookies, token); w.Code != 403 {
			t.Fatal("invalid CSRF accepted")
		}
	}
	if f.scans.Load() != 0 {
		t.Fatal("forbidden scan started")
	}
	if w := f.request("POST", "/api/scan", "", cookies, csrf); w.Code != 202 || f.scans.Load() != 1 {
		t.Fatal("admin scan rejected")
	}
	if w := f.request("PUT", "/api/settings", `{"scan_interval_seconds":300}`, cookies, csrf); w.Code != 200 {
		t.Fatalf("admin settings rejected: %s", w.Body)
	}
	// A future reader account is represented in the real identity database. The session resolves live roles.
	db, err := sql.Open("sqlite3", filepath.Join(f.dir, "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE users SET role='reader' WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, action := range []struct{ method, path, body string }{{"GET", "/api/settings", ""}, {"PUT", "/api/settings", `{"scan_interval_seconds":0}`}, {"POST", "/api/scan", ""}, {"POST", "/api/documents/1/retry?ocr=true", ""}, {"POST", "/api/../api/scan", ""}} {
		w := f.request(action.method, action.path, action.body, cookies, csrf)
		if w.Code == http.StatusTemporaryRedirect || w.Code == http.StatusMovedPermanently {
			w = f.request(action.method, w.Header().Get("Location"), action.body, cookies, csrf)
		}
		if w.Code != 403 {
			t.Fatalf("reader mutation allowed: %s %d", action.path, w.Code)
		}
	}
	if f.scans.Load() != 1 {
		t.Fatal("reader started scan")
	}
	if w := f.request("GET", "/api/documents/1/pages/1", "", cookies, ""); w.Code != 200 {
		t.Fatal("reader lost read access")
	}
	if w := f.request("POST", "/api/auth/logout", "", cookies, csrf); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := f.request("GET", "/api/documents/1/pdf", "", cookies, ""); w.Code != 401 {
		t.Fatal("logged-out cookie accepted")
	}
}

func TestCrossOriginMalformedLoginThrottleAndSecureCookies(t *testing.T) {
	f := newAuthFixture(t)
	for _, origin := range []string{"https://evil.example", "null", "http://example.com.evil.example"} {
		r := httptest.NewRequest("POST", "http://example.com/api/auth/login", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("foreign origin accepted")
		}
	}
	for _, body := range []string{`{}`, `{"username":"admin","unknown":true}`, `{} {}`, strings.Repeat("x", 5000)} {
		w := f.request("POST", "/api/auth/login", body, nil, "")
		if w.Code != 400 && w.Code != 401 {
			t.Fatalf("bad login accepted: %d", w.Code)
		}
	}
	f.a.window = time.Now()
	f.a.attempts = 10
	if w := f.request("POST", "/api/auth/login", `{}`, nil, ""); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("throttle absent")
	}
	f.a.window = time.Now().Add(-time.Minute)
	if !f.a.allowAttempt(time.Now()) {
		t.Fatal("throttle never resets")
	}
	f.a.secure = true
	w := httptest.NewRecorder()
	f.a.cookies(w, "token", "csrf", time.Now().Add(time.Hour), 3600)
	for _, cookie := range w.Result().Cookies() {
		if !cookie.Secure {
			t.Fatal("HTTPS cookie insecure")
		}
	}
	r := httptest.NewRequest("POST", "http://example.com/api/auth/login", nil)
	r.Header.Set("Origin", "https://example.com")
	if !f.a.sameOrigin(r) {
		t.Fatal("configured HTTPS proxy origin rejected")
	}
}

func TestSetupCodeSurvivesRestartAndStaysPrivate(t *testing.T) {
	f := newAuthFixture(t)
	a, err := newAuth(f.a.store, f.dir, false)
	if err != nil || a.setupCode != f.a.setupCode {
		t.Fatal("setup code changed on restart")
	}
	info, err := os.Stat(a.setupPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("setup code not private")
	}
}
