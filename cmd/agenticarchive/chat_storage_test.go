package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

func TestSavedChatResumesServerContextAndStoresSuccessfulTurnsOnly(t *testing.T) {
	key := strings.Repeat("s", 64)
	calls := 0
	fail := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input agent.Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if calls == 2 && (len(input.Messages) != 3 || !strings.Contains(input.Messages[2].Content, "2026 prüfen") || input.Messages[1].Sources[0].Path != "test.pdf") {
			t.Error("saved context or notes not replayed", input)
		}
		if fail {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": "Testfehler"})
			return
		}
		answer := agent.Answer{Text: "Teilergebnis 100 EUR [Dokument 1, Seite 1]", Sources: []agent.Source{{DocumentID: 1, Page: 1}}}
		answer.History = append(input.Messages, agent.Message{Role: "assistant", Content: answer.Text, Sources: answer.Sources})
		json.NewEncoder(w).Encode(answer)
	}))
	defer backend.Close()
	t.Setenv("AGENT_URL", backend.URL)
	t.Setenv("AGENT_SERVICE_KEY", key)
	t.Setenv("AGENT_SERVICE_KEY_FILE", "")
	f := newAuthFixture(t)
	f.a.store.SetupAdmin(context.Background(), "admin", authPassword)
	cookies, csrf := f.login(t)
	w := f.request("POST", "/api/chats", `{"title":"Recherche"}`, cookies, csrf)
	var chat access.Chat
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &chat) != nil {
		t.Fatal(w.Code, w.Body)
	}
	request := func(version int64) string {
		body, _ := json.Marshal(map[string]any{"chat_id": chat.ID, "version": version, "messages": []agent.Message{{Role: "user", Content: "Frage"}}})
		return string(body)
	}
	if w = f.request("POST", "/api/chat", request(chat.Version), cookies, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	user, _ := f.a.store.Authenticate(context.Background(), "admin", authPassword)
	stored, _ := f.a.store.Chat(context.Background(), user.ID, chat.ID)
	version, err := f.a.store.UpdateChat(context.Background(), user.ID, chat.ID, stored.Version, "Recherche", "2026 prüfen", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w = f.request("POST", "/api/chat", request(version), cookies, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = f.request("POST", "/api/chat", request(version), cookies, csrf); w.Code != 409 || calls != 2 {
		t.Fatal("stale client invoked model", w.Code, calls)
	}
	stored, _ = f.a.store.Chat(context.Background(), user.ID, chat.ID)
	fail = true
	if w = f.request("POST", "/api/chat", request(stored.Version), cookies, csrf); w.Code != 502 {
		t.Fatal("backend failure accepted", w.Code)
	}
	after, _ := f.a.store.Chat(context.Background(), user.ID, chat.ID)
	if after.Version != stored.Version || len(after.Messages) != 4 || len(after.Results) != 2 {
		t.Fatal("failure changed persisted research", after)
	}
}

func TestSavedChatRoutesAndMergeArePrivateAndSnapshotSelectedResults(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.a.store.SetupAdmin(ctx, "admin", authPassword)
	cookies, csrf := f.login(t)
	admin, _ := f.a.store.Authenticate(ctx, "admin", authPassword)
	reader, err := f.a.store.CreateUser(ctx, admin.ID, "reader", authPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	makeChat := func(userID int64, title, text string) access.Chat {
		working := []agent.Message{{Role: "user", Content: title}, {Role: "assistant", Content: text, Sources: []agent.Source{{DocumentID: 1, Page: 1, Path: "test.pdf"}}}}
		chat, err := f.a.store.CreateChat(ctx, userID, title, working, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return chat
	}
	first := makeChat(admin.ID, "2025", "Teilergebnis 100 EUR [Dokument 1, Seite 1]")
	second := makeChat(admin.ID, "2026", "Teilergebnis 120 EUR [Dokument 1, Seite 1]")
	foreign := makeChat(reader.ID, "Geheim", "Privater Reader-Chat")
	route := func(id int64) string { return "/api/chats/" + strconv.FormatInt(id, 10) }
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		body := ""
		if method == "PATCH" {
			body = `{"title":"Attack","notes":"","version":1}`
		}
		if w := f.request(method, route(foreign.ID), body, cookies, csrf); w.Code != 404 {
			t.Fatal("foreign chat route exposed", method, w.Code, w.Body)
		}
	}
	if w := f.request("POST", "/api/chats", `{"title":"No CSRF"}`, cookies, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	payload, _ := json.Marshal(map[string]any{"ids": []int64{first.ID, foreign.ID}})
	if w := f.request("POST", "/api/chats/merge", string(payload), cookies, csrf); w.Code != 404 {
		t.Fatal("foreign merge accepted", w.Code)
	}
	payload, _ = json.Marshal(map[string]any{"ids": []int64{first.ID, second.ID}})
	w := f.request("POST", "/api/chats/merge", string(payload), cookies, csrf)
	var merged access.Chat
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &merged) != nil || len(merged.Imports) != 2 || len(merged.Results) != 2 {
		t.Fatal("merge failed", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "100 EUR") || !strings.Contains(w.Body.String(), "120 EUR") {
		t.Fatal("merge lost selected findings")
	}
	if err = f.a.store.DeleteChat(ctx, admin.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := f.a.store.Chat(ctx, admin.ID, merged.ID)
	if err != nil || len(saved.Messages) != 4 || !strings.Contains(saved.Results[0].Content, "100 EUR") {
		t.Fatal("merged snapshot depended on deleted parent", saved, err)
	}
	if w := f.request("GET", "/api/chats", "", cookies, ""); strings.Contains(w.Body.String(), "Geheim") || w.Code != 200 {
		t.Fatal("foreign chat in list")
	}
}

func TestSavedChatMarksChangedSourcesAndStillSummarizesHistoricalResults(t *testing.T) {
	key := strings.Repeat("s", 64)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input agent.Request
		json.NewDecoder(r.Body).Decode(&input)
		if input.Mode != "summary" || !strings.Contains(input.Messages[len(input.Messages)-1].Content, "historischen Ergebnis") {
			t.Error("historic source status missing")
		}
		source := input.Messages[1].Sources[0]
		json.NewEncoder(w).Encode(agent.Answer{Text: "Historisches Teilergebnis: 100 EUR [Dokument 1, Seite 1]", Sources: []agent.Source{source}})
	}))
	defer backend.Close()
	t.Setenv("AGENT_URL", backend.URL)
	t.Setenv("AGENT_SERVICE_KEY", key)
	t.Setenv("AGENT_SERVICE_KEY_FILE", "")
	f := newAuthFixture(t)
	ctx := context.Background()
	f.a.store.SetupAdmin(ctx, "admin", authPassword)
	cookies, csrf := f.login(t)
	user, _ := f.a.store.Authenticate(ctx, "admin", authPassword)
	working := []agent.Message{{Role: "user", Content: "Rechnung?"}, {Role: "assistant", Content: "100 EUR [Dokument 1, Seite 1]", Sources: []agent.Source{{DocumentID: 1, Page: 1, Path: "old.pdf", Revision: "old-document-version"}}}}
	chat, err := f.a.store.CreateChat(ctx, user.ID, "Historie", working, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/chats/"+strconv.FormatInt(chat.ID, 10), "", cookies, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailable":true`) || !strings.Contains(w.Body.String(), "100 EUR") {
		t.Fatal("changed source silently relinked", w.Code, w.Body)
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": chat.ID, "version": chat.Version, "mode": "research", "messages": []agent.Message{{Role: "user", Content: "Weiter"}}})
	if w = f.request("POST", "/api/chat", string(payload), cookies, csrf); w.Code != 400 {
		t.Fatal("changed source accepted as fresh evidence", w.Code)
	}
	payload, _ = json.Marshal(map[string]any{"chat_id": chat.ID, "version": chat.Version, "mode": "summary", "messages": []agent.Message{{Role: "user", Content: "Fasse bisherige Ergebnisse zusammen"}}})
	if w = f.request("POST", "/api/chat", string(payload), cookies, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailable":true`) {
		t.Fatal("historic summary failed", w.Code, w.Body)
	}
}

func TestOldTranscriptImportRetainsTextWithoutPromotingUnverifiedSources(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.a.store.SetupAdmin(ctx, "admin", authPassword)
	cookies, csrf := f.login(t)
	transcript := "Du: Rechnung?\nArchiv-Agent: 100 EUR [Dokument 1, Seite 1] <script>not executable</script>"
	payload, _ := json.Marshal(map[string]string{"title": "Früherer Chat", "transcript": transcript})
	w := f.request("POST", "/api/chats", string(payload), cookies, csrf)
	var chat access.Chat
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &chat) != nil || len(chat.Messages) != 2 || chat.Messages[1].Content != transcript || len(chat.Results) != 1 || len(chat.Results[0].Sources) != 1 || !chat.Results[0].Sources[0].Imported {
		t.Fatal("import altered history or forged evidence", w.Code, w.Body)
	}
	payload, _ = json.Marshal(map[string]string{"title": "Zu lang", "transcript": strings.Repeat("x", 32001)})
	if w = f.request("POST", "/api/chats", string(payload), cookies, csrf); w.Code != 400 {
		t.Fatal("oversized import accepted", w.Code)
	}
}
