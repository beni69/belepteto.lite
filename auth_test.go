package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// authTestServer returns a running server plus its store so tests can seed users.
func authTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	store := newTestStore(t)
	srv := httptest.NewServer(newRouter(logger, tmpl, store))
	t.Cleanup(srv.Close)
	return srv, store
}

func seedUser(t *testing.T, store *Store, email, password string, admin bool) *User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u := &User{Email: email, PasswordHash: hash, Admin: admin, Active: true}
	if err := store.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// login posts credentials using a cookie-jar client and returns that client,
// which now carries the session cookie.
func login(t *testing.T, srv *httptest.Server, email, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.PostForm(srv.URL+"/login", url.Values{
		"email":    {email},
		"password": {password},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	return client
}

func TestLoginWrongPassword(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "u@example.com", "correct-horse", false)

	client := noRedirectClient()
	resp, err := client.PostForm(srv.URL+"/login", url.Values{
		"email":    {"u@example.com"},
		"password": {"wrong"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Invalid email or password") {
		t.Errorf("expected error message in body")
	}
	if len(resp.Cookies()) > 0 && resp.Cookies()[0].Value != "" {
		t.Errorf("no session cookie should be set on failed login")
	}
}

func TestLoginSuccessGrantsAccess(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "u@example.com", "correct-horse", false)

	client := login(t, srv, "u@example.com", "correct-horse")

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / after login: status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "My cards") {
		t.Errorf("home page not rendered after login")
	}
}

func TestAdminGating(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "user@example.com", "pw", false)
	seedUser(t, store, "admin@example.com", "pw", true)

	// Regular user is forbidden from /admin.
	userClient := login(t, srv, "user@example.com", "pw")
	resp, err := userClient.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatalf("get /admin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin /admin: status = %d, want 403", resp.StatusCode)
	}

	// Admin user gets in.
	adminClient := login(t, srv, "admin@example.com", "pw")
	resp, err = adminClient.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatalf("get /admin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin /admin: status = %d, want 200", resp.StatusCode)
	}
}

func TestDeactivatedUserCannotLogIn(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)
	u.Active = false
	if err := store.db.Save(u).Error; err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	client := noRedirectClient()
	resp, err := client.PostForm(srv.URL+"/login", url.Values{
		"email":    {"u@example.com"},
		"password": {"pw"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("deactivated login: status = %d, want 401", resp.StatusCode)
	}
}

func TestDeactivationInvalidatesSession(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)

	client := login(t, srv, "u@example.com", "pw")

	// Confirm access works first.
	resp, _ := client.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected access before deactivation")
	}

	// Deactivate the user; their existing session must stop working.
	u.Active = false
	if err := store.db.Save(u).Error; err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	noFollow := &http.Client{Jar: client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = noFollow.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("deactivated session: status = %d, want 303 redirect to login", resp.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")

	resp, err := client.Post(srv.URL+"/logout", "", nil)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	resp.Body.Close()

	noFollow := &http.Client{Jar: client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = noFollow.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout: status = %d, want 303", resp.StatusCode)
	}
}
