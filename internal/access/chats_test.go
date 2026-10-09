package access

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

func TestChatsSurviveReopenAndKeepFullResultsBeyondWorkingMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.SetupAdmin(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	user, err := s.Authenticate(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.CreateChat(ctx, user.ID, "Rechnungen", nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	question := agent.Message{Role: "user", Content: "2025?"}
	answer := agent.Answer{Text: "Teilergebnis: 100 EUR [Dokument 7, Seite 1]\n\nRecherche-Lücken:\n- Weitere Jahre sind offen.", Sources: []agent.Source{{DocumentID: 7, Page: 1, Path: "rechnung.pdf"}}}
	working := []agent.Message{question, {Role: "assistant", Content: answer.Text, Sources: answer.Sources}}
	version, err := s.SaveChatTurn(ctx, user.ID, chat.ID, chat.Version, question, answer, working, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateChat(ctx, user.ID, chat.ID, version, "Energie", "2026 noch prüfen", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.Chat(ctx, user.ID, chat.ID)
	if err != nil || len(stored.Messages) != 2 || len(stored.Results) != 1 || stored.Notes != "2026 noch prüfen" || !stored.Results[0].Partial || stored.Results[0].OpenQuestions == "" || len(stored.Working[1].Sources) != 1 {
		t.Fatalf("persistence failed: %+v %v", stored, err)
	}
	short := []agent.Message{{Role: "user", Content: "Gedächtnis"}, {Role: "assistant", Content: "Verdichteter Kontext"}}
	_, err = s.SaveChatTurn(ctx, user.ID, chat.ID, stored.Version, agent.Message{Role: "user", Content: "Weiter"}, agent.Answer{Text: "Noch ein Ergebnis"}, short, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stored, err = s.Chat(ctx, user.ID, chat.ID)
	if err != nil || len(stored.Messages) != 4 || len(stored.Results) != 2 || stored.Results[0].Content != answer.Text || len(stored.Working) != 2 {
		t.Fatal("working memory replaced full results", stored, err)
	}
}

func TestChatsEnforceOwnershipAndAtomicVersionedUpdates(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	other, err := s.CreateUser(ctx, admin.ID, "reader", testPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.CreateChat(ctx, admin.ID, "Privat", nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Chat(ctx, other.ID, chat.ID); !errors.Is(err, ErrChatNotFound) {
		t.Fatal("foreign chat visible", err)
	}
	chats, err := s.Chats(ctx, other.ID, 0)
	if err != nil || len(chats) != 0 {
		t.Fatal("foreign list leaked")
	}
	if err = s.DeleteChat(ctx, other.ID, chat.ID); !errors.Is(err, ErrChatNotFound) {
		t.Fatal("foreign deletion succeeded", err)
	}
	if _, err = s.UpdateChat(ctx, other.ID, chat.ID, chat.Version, "Attack", "", time.Now()); !errors.Is(err, ErrChatConflict) {
		t.Fatal("foreign update succeeded", err)
	}
	question := agent.Message{Role: "user", Content: "Frage"}
	answer := agent.Answer{Text: "Antwort"}
	working := []agent.Message{question, {Role: "assistant", Content: "Antwort"}}
	if _, err = s.SaveChatTurn(ctx, admin.ID, chat.ID, chat.Version, question, answer, working, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveChatTurn(ctx, admin.ID, chat.ID, chat.Version, question, answer, working, time.Now()); !errors.Is(err, ErrChatConflict) {
		t.Fatal("stale turn overwrote context", err)
	}
	stored, err := s.Chat(ctx, admin.ID, chat.ID)
	if err != nil || len(stored.Messages) != 2 || len(stored.Results) != 1 {
		t.Fatal("failed write committed partial data", stored, err)
	}
	if err = s.DeleteChat(ctx, admin.ID, chat.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_id=?`, chat.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("message orphan after deletion")
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM chat_results WHERE chat_id=?`, chat.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("result orphan after deletion")
	}
}
