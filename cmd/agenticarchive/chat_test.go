package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

func TestChatGatewayDelegatesCurrentUserAndRevokesTemporaryToken(t *testing.T) {
	var f *authFixture
	var delegated string
	serviceKey := strings.Repeat("s", 64)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat" || r.Header.Get("Authorization") != "Bearer "+serviceKey {
			t.Error("wrong service request")
		}
		delegated = r.Header.Get("X-Archive-Token")
		principal, err := f.a.store.AgentToken(r.Context(), delegated, time.Now())
		if err != nil || principal.User.Username != "admin" {
			t.Error("wrong delegated identity", err)
		}
		json.NewEncoder(w).Encode(agent.Answer{Text: "Belegte Antwort [Dokument 1, Seite 1]", Sources: []agent.Source{{DocumentID: 1, Page: 1, Path: "untrusted.pdf"}}, Model: "fixture"})
	}))
	defer backend.Close()
	t.Setenv("AGENT_URL", backend.URL)
	t.Setenv("AGENT_SERVICE_KEY", serviceKey)
	t.Setenv("AGENT_SERVICE_KEY_FILE", "")
	f = newAuthFixture(t)
	if err := f.a.store.SetupAdmin(context.Background(), "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := f.login(t)
	body := `{"messages":[{"role":"user","content":"Was steht im Dokument?"}]}`
	if w := f.request("POST", "/api/chat", body, cookies, ""); w.Code != 403 {
		t.Fatal("chat missing CSRF", w.Code)
	}
	w := f.request("POST", "/api/chat", body, cookies, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "test.pdf") || strings.Contains(w.Body.String(), "untrusted.pdf") {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := f.a.store.AgentToken(context.Background(), delegated, time.Now()); err == nil {
		t.Fatal("temporary token survived response")
	}
	user, _ := f.a.store.Authenticate(context.Background(), "admin", authPassword)
	tokens, err := f.a.store.AgentTokens(context.Background(), user.ID, time.Now())
	if err != nil || len(tokens) != 0 {
		t.Fatal("webchat token leaked into personal list")
	}
}

func TestChatWithoutAgentHasAnExplicitSetupState(t *testing.T) {
	t.Setenv("AGENT_URL", "")
	t.Setenv("AGENT_SERVICE_KEY", "")
	t.Setenv("AGENT_SERVICE_KEY_FILE", "")
	f := newAuthFixture(t)
	f.a.store.SetupAdmin(context.Background(), "admin", authPassword)
	cookies, csrf := f.login(t)
	if w := f.request("GET", "/api/chat/state", "", cookies, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.request("POST", "/api/chat", `{"messages":[]}`, cookies, csrf); w.Code != 503 {
		t.Fatal("unconfigured chat accepted", w.Code)
	}
}
