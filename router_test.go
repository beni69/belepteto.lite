package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	store := newTestStore(t)
	access := newTestAccess(t, store)
	srv := httptest.NewServer(newRouter(logger, tmpl, store, access))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestStaticAssets(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/static/pico.min.css", "/static/htmx.min.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestLoginPageRenders(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Log in to continue") {
		t.Errorf("login page missing expected content")
	}
	if strings.Contains(string(body), defaultSSOLoginButtonText) || strings.Contains(string(body), "/sso/login") {
		t.Errorf("SSO button should be hidden when SSO is not configured")
	}
}

// noRedirectClient refuses to follow redirects so tests can assert on the 303
// itself rather than the redirect target.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestGatedPagesRedirectWhenLoggedOut(t *testing.T) {
	srv := newTestServer(t)
	client := noRedirectClient()

	for _, path := range []string{"/", "/admin"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s: status = %d, want 303", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/login" {
			t.Errorf("GET %s: redirect to %q, want /login", path, loc)
		}
	}
}

func TestFragmentRenders(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	rec := httptest.NewRecorder()
	data := map[string]any{"Cards": []Card{{ID: 1, CardUID: "ABC123", Label: "keys"}}}
	if err := tmpl.Fragment(rec, "card-list", data); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "ABC123") {
		t.Fatalf("fragment missing card UID, got: %s", rec.Body.String())
	}
}
