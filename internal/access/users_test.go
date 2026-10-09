package access

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func userStore(t *testing.T) (*Store, User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetupAdmin(context.Background(), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	admin, err := s.Authenticate(context.Background(), "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return s, admin
}

func TestUserCreationRolesAndLastAdministrator(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	reader, err := s.CreateUser(ctx, admin.ID, "reader", testPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, admin.ID, "reader", testPassword, "admin"); !errors.Is(err, ErrUsernameExists) {
		t.Fatal("duplicate accepted")
	}
	for _, name := range []string{"", " leading", "trailing ", "line\nname", "null\x00name", strings.Repeat("x", 65)} {
		if _, err := s.CreateUser(ctx, admin.ID, name, testPassword, "reader"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid username accepted: %q %v", name, err)
		}
	}
	if _, err := s.CreateUser(ctx, admin.ID, "unsafe", testPassword, "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown role accepted")
	}
	if _, err := s.CreateUser(ctx, reader.ID, "new", testPassword, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("reader created admin")
	}
	if _, err := s.ChangeRole(ctx, reader.ID, reader.ID, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("reader promoted itself")
	}
	if _, err := s.ChangeRole(ctx, admin.ID, admin.ID, "reader"); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("last admin demoted")
	}
	if _, err := s.ChangeRole(ctx, admin.ID, 999, "reader"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing user accepted")
	}
	if _, err := s.ChangeRole(ctx, admin.ID, reader.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeRole(ctx, admin.ID, admin.ID, "reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeRole(ctx, admin.ID, reader.ID, "reader"); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale admin authorization accepted")
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("user list: %v", err)
	}
}

func TestPasswordChangesAndResetsRevokeOnlyAffectedSessions(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	now := time.Now()
	reader, err := s.CreateUser(ctx, admin.ID, "reader", testPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	reader, err = s.Authenticate(ctx, reader.Username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	rootToken, _, err := s.NewSession(ctx, admin, now)
	if err != nil {
		t.Fatal(err)
	}
	readerToken, _, err := s.NewSession(ctx, reader, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, reader.ID, "wrong", "new personal long password"); !errors.Is(err, ErrCurrentPassword) {
		t.Fatal("wrong current password accepted")
	}
	if _, err := s.Session(ctx, readerToken, now); err != nil {
		t.Fatal("failed change revoked session")
	}
	if err := s.ChangePassword(ctx, reader.ID, testPassword, "short"); !errors.Is(err, ErrInvalid) {
		t.Fatal("weak password accepted")
	}
	if err := s.ChangePassword(ctx, reader.ID, testPassword, "new personal long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, readerToken, now); err == nil {
		t.Fatal("personal change retained session")
	}
	if _, _, err := s.NewSession(ctx, reader, now); !errors.Is(err, ErrCredentials) {
		t.Fatal("stale password verification created fresh session")
	}
	reader, err = s.Authenticate(ctx, "reader", "new personal long password")
	if err != nil {
		t.Fatal(err)
	}
	readerToken, _, err = s.NewSession(ctx, reader, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetUserPassword(ctx, reader.ID, admin.ID, "new personal long password"); !errors.Is(err, ErrForbidden) {
		t.Fatal("reader reset admin password")
	}
	if err := s.ResetUserPassword(ctx, admin.ID, admin.ID, "new personal long password"); !errors.Is(err, ErrInvalid) {
		t.Fatal("self-reset bypassed current-password check")
	}
	if err := s.ResetUserPassword(ctx, admin.ID, reader.ID, "administrator assigned password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, readerToken, now); err == nil {
		t.Fatal("admin reset retained target session")
	}
	if _, err := s.Session(ctx, rootToken, now); err != nil {
		t.Fatal("admin reset revoked unrelated session")
	}
	if _, err := s.Authenticate(ctx, "reader", "new personal long password"); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password still works")
	}
	if _, err := s.Authenticate(ctx, "reader", "administrator assigned password"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentDemotionsCannotRemoveEveryAdministrator(t *testing.T) {
	s, admin := userStore(t)
	ctx := context.Background()
	other, err := s.CreateUser(ctx, admin.ID, "second-admin", testPassword, "admin")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []int64{admin.ID, other.ID} {
		go func(id int64) { <-start; _, err := s.ChangeRole(ctx, id, id, "reader"); results <- err }(id)
	}
	close(start)
	a, b := <-results, <-results
	if (a == nil) == (b == nil) || (a != nil && !errors.Is(a, ErrLastAdmin)) || (b != nil && !errors.Is(b, ErrLastAdmin)) {
		t.Fatalf("demotions: %v / %v", a, b)
	}
}
