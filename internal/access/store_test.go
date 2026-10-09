package access

import (
	"context"
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse battery staple"

func TestAccountsSessionsPersistenceAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "access.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err := s.SetupAdmin(ctx, "admin", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.SetupAdmin(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.SetupAdmin(ctx, "other", testPassword); err == nil {
		t.Fatal("setup reused")
	}
	user, err := s.Authenticate(ctx, "admin", testPassword)
	if err != nil || !user.Can(ManageSettings) {
		t.Fatalf("admin rejected: %v", err)
	}
	for _, name := range []string{"admin", "missing"} {
		if _, err := s.Authenticate(ctx, name, "wrong"); !errors.Is(err, ErrCredentials) {
			t.Fatalf("wrong credentials accepted: %v", err)
		}
	}
	now := time.Now().Truncate(time.Second)
	token, csrf, err := s.NewSession(ctx, user, now)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Session(ctx, token, now.Add(time.Hour))
	if err != nil || session.User != user || !session.ValidCSRF(csrf) || session.ValidCSRF(strings.Repeat("0", 64)) {
		t.Fatalf("session invalid after reopen: %v", err)
	}
	if _, err := s.Session(ctx, token, now.Add(SessionLifetime)); err == nil {
		t.Fatal("expired session accepted")
	}
	var storedToken []byte
	if err := s.db.QueryRow("SELECT token_hash FROM sessions").Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if subtle.ConstantTimeCompare(storedToken, []byte(token)) == 1 {
		t.Fatal("raw session token persisted")
	}
	if err := s.ResetAdminPassword(ctx, "admin", "a different long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, token, now); err == nil {
		t.Fatal("password reset retained sessions")
	}
	if _, err := s.Authenticate(ctx, "admin", testPassword); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password retained")
	}
	if _, err := s.Authenticate(ctx, "admin", "a different long password"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("account database not private")
	}
}

func TestLiveRolesSessionLimitAndLogout(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SetupAdmin(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	user, _ := s.Authenticate(ctx, "admin", testPassword)
	now := time.Now().Truncate(time.Second)
	var token string
	for i := 0; i < 25; i++ {
		token, _, err = s.NewSession(ctx, user, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count)
	if count != 20 {
		t.Fatalf("session limit: %d", count)
	}
	if _, err := s.db.Exec("UPDATE users SET role='reader' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	session, err := s.Session(ctx, token, now)
	if err != nil || !session.User.Can(ReadArchive) || session.User.Can(ManageImports) || session.User.Can(ManageSettings) || session.User.Can(Permission("unknown")) {
		t.Fatal("role change not applied to existing session")
	}
	if (User{Role: "unknown"}).Can(ReadArchive) {
		t.Fatal("unknown role accepted")
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, token, now); err == nil {
		t.Fatal("logout retained session")
	}
}
