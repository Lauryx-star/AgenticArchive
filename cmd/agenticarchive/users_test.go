package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
)

func (f *authFixture) sessionFor(t *testing.T, username, password string) ([]*http.Cookie, string) {
	t.Helper()
	user, err := f.a.store.Authenticate(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf, err := f.a.store.NewSession(context.Background(), user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return []*http.Cookie{{Name: sessionCookie, Value: token}, {Name: csrfCookie, Value: csrf}}, csrf
}

func TestUserManagementHTTPAndReaderRestrictions(t *testing.T) {
	f := newAuthFixture(t)
	if err := f.a.store.SetupAdmin(context.Background(), "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	admin, csrf := f.login(t)
	if w := f.request("GET", "/api/users", "", nil, ""); w.Code != 401 {
		t.Fatal("anonymous user list")
	}
	body := `{"username":"reader","password":"` + authPassword + `","role":"reader"}`
	if w := f.request("POST", "/api/users", body, admin, ""); w.Code != 403 {
		t.Fatal("creation without CSRF accepted")
	}
	w := f.request("POST", "/api/users", body, admin, csrf)
	if w.Code != 201 {
		t.Fatalf("creation: %d %s", w.Code, w.Body)
	}
	var user access.User
	if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if w := f.request("POST", "/api/users", body, admin, csrf); w.Code != 409 {
		t.Fatal("duplicate accepted")
	}
	w = f.request("GET", "/api/users", "", admin, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "salt") || strings.Contains(w.Body.String(), "csrf") {
		t.Fatal("user list leaked credential data")
	}
	reader, readerCSRF := f.sessionFor(t, "reader", authPassword)
	for _, action := range []struct{ method, path, body string }{{"GET", "/api/users", ""}, {"POST", "/api/users", body}, {"PUT", fmt.Sprintf("/api/users/%d/role", user.ID), `{"role":"admin"}`}, {"PUT", "/api/users/1/password", `{"password":"new administrator password"}`}} {
		if w := f.request(action.method, action.path, action.body, reader, readerCSRF); w.Code != 403 {
			t.Fatalf("reader gained access: %s %d", action.path, w.Code)
		}
	}
	if w := f.request("PUT", "/api/users/1/role", `{"role":"reader"}`, admin, csrf); w.Code != 409 {
		t.Fatal("last admin demoted")
	}
	if w := f.request("PUT", "/api/users/0/role", `{"role":"admin"}`, admin, csrf); w.Code != 400 {
		t.Fatal("invalid ID accepted")
	}
	if w := f.request("PUT", "/api/users/999/role", `{"role":"admin"}`, admin, csrf); w.Code != 404 {
		t.Fatal("missing user accepted")
	}
	if w := f.request("PUT", fmt.Sprintf("/api/users/%d/role", user.ID), `{"role":"admin","username":"other"}`, admin, csrf); w.Code != 400 {
		t.Fatal("unknown fields accepted")
	}
	if w := f.request("PUT", fmt.Sprintf("/api/users/%d/role", user.ID), `{"role":"admin"}`, admin, csrf); w.Code != 200 {
		t.Fatalf("promotion: %s", w.Body)
	}
	if w := f.request("GET", "/api/users", "", reader, ""); w.Code != 200 {
		t.Fatal("existing session ignored new role")
	}
}

func TestSelfPasswordChangeHTTPForReaderAndAdministrator(t *testing.T) {
	for _, role := range []string{"reader", "admin"} {
		t.Run(role, func(t *testing.T) {
			f := newAuthFixture(t)
			ctx := context.Background()
			if err := f.a.store.SetupAdmin(ctx, "admin", authPassword); err != nil {
				t.Fatal(err)
			}
			root, _ := f.a.store.Authenticate(ctx, "admin", authPassword)
			if _, err := f.a.store.CreateUser(ctx, root.ID, "person", authPassword, role); err != nil {
				t.Fatal(err)
			}
			cookies, csrf := f.sessionFor(t, "person", authPassword)
			second, _ := f.sessionFor(t, "person", authPassword)
			for _, body := range []string{`{"current_password":"wrong","new_password":"new personal long password"}`, `{"current_password":"` + authPassword + `","new_password":"short"}`, `{"current_password":"` + authPassword + `","new_password":"new personal long password","user_id":1}`} {
				if w := f.request("PUT", "/api/auth/password", body, cookies, csrf); w.Code != 400 {
					t.Fatalf("bad password change: %d %s", w.Code, w.Body)
				}
			}
			if w := f.request("GET", "/api/auth/me", "", cookies, ""); w.Code != 200 {
				t.Fatal("failed change revoked session")
			}
			w := f.request("PUT", "/api/auth/password", `{"current_password":"`+authPassword+`","new_password":"new personal long password"}`, cookies, csrf)
			if w.Code != 200 {
				t.Fatalf("self change: %d %s", w.Code, w.Body)
			}
			for _, cookie := range w.Result().Cookies() {
				if cookie.MaxAge != -1 {
					t.Fatal("browser cookies not cleared")
				}
			}
			for _, old := range [][]*http.Cookie{cookies, second} {
				if w := f.request("GET", "/api/auth/me", "", old, ""); w.Code != 401 {
					t.Fatal("old session survived")
				}
			}
			if _, err := f.a.store.Authenticate(ctx, "person", authPassword); err == nil {
				t.Fatal("old password survived")
			}
			if _, err := f.a.store.Authenticate(ctx, "person", "new personal long password"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdministratorResetHTTPInvalidatesTargetAndPreservesActor(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if err := f.a.store.SetupAdmin(ctx, "admin", authPassword); err != nil {
		t.Fatal(err)
	}
	admin, csrf := f.login(t)
	root, _ := f.a.store.Authenticate(ctx, "admin", authPassword)
	user, err := f.a.store.CreateUser(ctx, root.ID, "person", authPassword, "reader")
	if err != nil {
		t.Fatal(err)
	}
	cookies, _ := f.sessionFor(t, "person", authPassword)
	if w := f.request("PUT", "/api/users/1/password", `{"password":"new administrator password"}`, admin, csrf); w.Code != 400 {
		t.Fatal("admin bypassed personal password validation")
	}
	if w := f.request("PUT", fmt.Sprintf("/api/users/%d/password", user.ID), `{"password":"administrator assigned password"}`, admin, csrf); w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if w := f.request("GET", "/api/auth/me", "", cookies, ""); w.Code != 401 {
		t.Fatal("target session retained")
	}
	if w := f.request("GET", "/api/auth/me", "", admin, ""); w.Code != 200 {
		t.Fatal("actor logged out")
	}
	if _, err := f.a.store.Authenticate(ctx, "person", "administrator assigned password"); err != nil {
		t.Fatal(err)
	}
}
