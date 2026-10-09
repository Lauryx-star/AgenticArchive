package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentTokensAreOwnedHashedExpiringAndRevokedByPasswordReset(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	now := time.Now()
	reader, err := s.CreateUser(ctx, admin.ID, "reader", testPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	reader, err = s.Authenticate(ctx, "reader", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, raw, err := s.CreateAgentToken(ctx, reader, "My agent", time.Hour, false, now)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AgentToken(ctx, raw, now)
	if err != nil || principal.User.ID != reader.ID || principal.TokenID != token.ID {
		t.Fatalf("principal: %+v %v", principal, err)
	}
	var hash []byte
	if err := s.db.QueryRow("SELECT token_hash FROM agent_tokens WHERE id=?", token.ID).Scan(&hash); err != nil || string(hash) == raw || len(hash) != 32 {
		t.Fatal("raw token persisted")
	}
	listed, err := s.AgentTokens(ctx, reader.ID, now)
	if err != nil || len(listed) != 1 || listed[0].Name != "My agent" {
		t.Fatal(listed, err)
	}
	if err := s.RevokeAgentToken(ctx, admin.ID, token.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("other owner revoked token", err)
	}
	if _, err := s.AgentToken(ctx, raw, now.Add(time.Hour)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired token accepted", err)
	}
	if err := s.ResetUserPassword(ctx, admin.ID, reader.ID, "a completely different password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentToken(ctx, raw, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("password reset retained token", err)
	}
	if _, _, err := s.CreateAgentToken(ctx, reader, "stale login", time.Hour, false, now); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale credentials issued token", err)
	}
}

func TestAgentTokenBoundsAndTemporaryVisibility(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, name := range []string{"", " leading", "line\nname", strings.Repeat("x", 81)} {
		if _, _, err := s.CreateAgentToken(ctx, admin, name, time.Hour, false, now); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	if _, _, err := s.CreateAgentToken(ctx, admin, "temporary", time.Hour, true, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("temporary lifetime unbounded")
	}
	temporary, raw, err := s.CreateAgentToken(ctx, admin, "Webchat", 5*time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.AgentTokens(ctx, admin.ID, now)
	if err != nil || len(listed) != 0 {
		t.Fatal("temporary token visible", listed, err)
	}
	if err := s.RevokeAgentToken(ctx, admin.ID, temporary.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentToken(ctx, raw, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revocation ineffective")
	}
	for i := 0; i < 20; i++ {
		if _, _, err := s.CreateAgentToken(ctx, admin, "bounded", time.Hour, false, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateAgentToken(ctx, admin, "overflow", time.Hour, false, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded storage")
	}
}
