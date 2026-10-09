package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

func TestArchiveToolsSearchContentAndFilenamesWithSafeDefaults(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	path := filepath.Join(f.dir, "archive.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE documents SET path='Stromrechnung.pdf' WHERE id=1; UPDATE pages SET text='EON Rechnung. Rechnungsdatum 12.09.2026.' WHERE document_id=1;
INSERT INTO page_search(rowid,text,path) SELECT p.id,p.text,d.path FROM pages p JOIN documents d ON d.id=p.document_id;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		query              string
		content, filenames int
	}{{"EON", 1, 0}, {"Stromrechnung", 1, 1}} {
		params, _ := json.Marshal(map[string]any{"query": tc.query}) // no page: first page
		result, err := callArchiveTool(ctx, store, "search_archive", params)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		var got struct {
			Total         int                       `json:"total"`
			FilenameTotal int                       `json:"filename_total"`
			Hits          []archive.Hit             `json:"hits"`
			Filenames     []archive.DocumentSummary `json:"filename_matches"`
		}
		if json.Unmarshal(data, &got) != nil || got.Total != tc.content || got.FilenameTotal != tc.filenames || len(got.Hits) != tc.content || len(got.Filenames) != tc.filenames {
			t.Fatalf("search %s: %s", tc.query, data)
		}
	}
	if _, err := callArchiveTool(ctx, store, "list_documents", json.RawMessage(`{"path":"EON"}`)); err != nil {
		t.Fatalf("missing page should use 1: %v", err)
	}
	if _, err := callArchiveTool(ctx, store, "read_page", json.RawMessage(`{"document_id":1,"page":1}`)); err != nil {
		t.Fatalf("missing offset should use 0: %v", err)
	}
	for _, tc := range []struct{ name, args string }{{"search_archive", `{"query":"EON","page":0}`}, {"list_documents", `{"path":"EON","page":0}`}, {"read_page", `{"document_id":1,"page":1,"offset":-1}`}, {"search_archive", `{"query":"EON","admin":true}`}} {
		if _, err := callArchiveTool(ctx, store, tc.name, json.RawMessage(tc.args)); err == nil {
			t.Fatal("invalid explicit parameters accepted", tc.args)
		}
	}
}

func TestMCPIsBearerOnlyReadOnlyAndRevocable(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.a.store.SetupAdmin(ctx, "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	user, err := f.a.store.Authenticate(ctx, "admin", authPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, raw, err := f.a.store.CreateAgentToken(ctx, user, "external agent", time.Hour, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, bearer, origin, version string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("MCP-Protocol-Version", version)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_page","arguments":{"document_id":1,"page":1,"offset":0}}}`
	if w := call("POST", "/mcp", body, "", "", ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("POST", "/mcp", body, raw, "http://evil.example", ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("GET", "/mcp", "", raw, "http://evil.example", ""); w.Code != 403 {
		t.Fatal("GET origin unchecked")
	}
	if w := call("POST", "/mcp", body, raw, "", "unsupported"); w.Code != 400 {
		t.Fatal("bad version accepted")
	}
	if w := call("POST", "/mcp", body, raw, "", "2025-06-18"); w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE PAGE CONTENT") {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("GET", "/mcp", "", raw, "", ""); w.Code != 405 {
		t.Fatal("SSE not explicitly rejected")
	}
	if w := call("POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, raw, "", ""); w.Code != 202 || w.Body.Len() != 0 {
		t.Fatal("notification not acknowledged")
	}
	if w := call("POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"start_scan","arguments":{}}}`, raw, "", ""); strings.Contains(w.Body.String(), `"isError":true`) == false || f.scans.Load() != 0 {
		t.Fatal("write exposed", w.Body)
	}
	for _, path := range []string{"/api/settings", "/api/auth/tokens", "/api/documents/1/pages/1"} {
		if w := call("GET", path, "", raw, "", ""); w.Code != 401 {
			t.Fatal("bearer accepted outside MCP", path, w.Code)
		}
	}
	if err := f.a.store.RevokeAgentToken(ctx, user.ID, token.ID); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/mcp", body, raw, "", ""); w.Code != 401 {
		t.Fatal("revoked bearer accepted")
	}
}

func TestReaderCanManageOnlyOwnTokensWithCSRF(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.a.store.SetupAdmin(ctx, "admin", authPassword)
	admin, _ := f.a.store.Authenticate(ctx, "admin", authPassword)
	reader, err := f.a.store.CreateUser(ctx, admin.ID, "reader", authPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	reader, _ = f.a.store.Authenticate(ctx, "reader", authPassword)
	session, csrf, err := f.a.store.NewSession(ctx, reader, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cookies := []*http.Cookie{{Name: sessionCookie, Value: session}}
	if w := f.request("POST", "/api/auth/tokens", `{"name":"mine","days":90}`, cookies, ""); w.Code != 403 {
		t.Fatal("CSRF missing", w.Code)
	}
	w := f.request("POST", "/api/auth/tokens", `{"name":"mine","days":90}`, cookies, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var created struct {
		Secret string `json:"secret"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	w = f.request("GET", "/api/auth/tokens", "", cookies, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatal("secret in list", w.Body)
	}
}
