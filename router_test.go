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
	srv := httptest.NewServer(newRouter(logger, tmpl, store))
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

func TestPagesRender(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		path string
		want string
	}{
		{"/", "My cards"},
		{"/login", "Log in to continue"},
		{"/admin", "Manage users"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", tc.path, resp.StatusCode)
		}
		if !strings.Contains(string(body), tc.want) {
			t.Errorf("GET %s: body missing %q", tc.path, tc.want)
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
