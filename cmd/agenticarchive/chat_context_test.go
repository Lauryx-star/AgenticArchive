package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

func TestChatContextRejectsForgeryOtherUserAndExpiry(t *testing.T) {
	now := time.Now()
	key := strings.Repeat("s", 64)
	messages := []agent.Message{{Role: "user", Content: "Frage"}, {Role: "assistant", Content: "Teilergebnis", Sources: []agent.Source{{DocumentID: 1, Page: 1}}}}
	token := signChatMemory(key, 7, messages, now)
	history, err := readChatMemory(key, token, 7, now)
	if err != nil || len(history) != 2 || len(history[1].Sources) != 1 {
		t.Fatal("legitimate context lost", err)
	}
	for _, check := range []struct {
		token string
		user  int64
		now   time.Time
	}{
		{"A" + token[1:], 7, now}, {token, 8, now}, {token, 7, now.Add(12 * time.Hour)},
	} {
		if _, err := readChatMemory(key, check.token, check.user, check.now); err == nil {
			t.Fatal("forged, foreign or expired context accepted")
		}
	}
}
