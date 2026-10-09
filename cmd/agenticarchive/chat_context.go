package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

type chatMemory struct {
	UserID   int64           `json:"user_id"`
	Expires  int64           `json:"expires"`
	Messages []agent.Message `json:"messages"`
}

// Authenticated, user-bound context carries only previous successful answers. It is
// not a credential: every continuation still requires a current session and CSRF.
func signChatMemory(key string, userID int64, messages []agent.Message, now time.Time) string {
	data, _ := json.Marshal(chatMemory{UserID: userID, Expires: now.Add(12 * time.Hour).Unix(), Messages: messages})
	encoded := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("archive-chat-context-v1:" + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func readChatMemory(key, token string, userID int64, now time.Time) ([]agent.Message, error) {
	invalid := fmt.Errorf("Recherchekontext ungültig oder abgelaufen. Bitte beginne einen neuen Chat.")
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok || len(token) > 512<<10 {
		return nil, invalid
	}
	decodedSignature, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return nil, invalid
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("archive-chat-context-v1:" + encoded))
	if !hmac.Equal(mac.Sum(nil), decodedSignature) {
		return nil, invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, invalid
	}
	var memory chatMemory
	if json.Unmarshal(data, &memory) != nil || memory.UserID != userID || memory.Expires <= now.Unix() || len(memory.Messages) == 0 || memory.Messages[len(memory.Messages)-1].Role != "assistant" {
		return nil, invalid
	}
	return memory.Messages, nil
}
