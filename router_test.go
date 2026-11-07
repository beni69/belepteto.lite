package main

import (
	"encoding/json"
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
	srv := httptest.NewServer(newRouter(logger, tmpl, store, access, "", nil))
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

	for _, path := range []string{
		"/static/pico.min.css",
		"/static/htmx.min.js",
		"/static/app.css",
		"/static/door-open.js",
		"/static/assets/logo/Logo.gif",
		"/static/assets/logo/Logo.png",
		"/static/assets/logo/favicon-32.png",
		"/static/assets/logo/apple-touch-icon.png",
		"/static/assets/logo/pwa-192.png",
		"/static/assets/logo/pwa-512.png",
	} {
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

func TestPwaManifest(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatalf("GET /manifest.webmanifest: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", got)
	}

	var manifest struct {
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
		Display  string `json:"display"`
		Icons    []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode PWA manifest: %v", err)
	}
	if manifest.StartURL != "/" || manifest.Scope != "/" || manifest.Display != "standalone" {
		t.Errorf("manifest launch config = start_url %q, scope %q, display %q", manifest.StartURL, manifest.Scope, manifest.Display)
	}

	wantSizes := map[string]bool{"192x192": false, "512x512": false}
	hasMaskableIcon := false
	for _, icon := range manifest.Icons {
		if _, ok := wantSizes[icon.Sizes]; ok && icon.Type == "image/png" {
			wantSizes[icon.Sizes] = true
		}
		if icon.Sizes == "512x512" && strings.Contains(icon.Purpose, "maskable") {
			hasMaskableIcon = true
		}
	}
	for size, found := range wantSizes {
		if !found {
			t.Errorf("manifest is missing its %s PNG icon", size)
		}
	}
	if !hasMaskableIcon {
		t.Error("manifest is missing a maskable 512x512 PNG icon")
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
	if !strings.Contains(string(body), `href="/manifest.webmanifest"`) || !strings.Contains(string(body), `src="/static/assets/logo/Logo.gif"`) {
		t.Errorf("login page missing PWA or animated logo metadata")
	}
	if !strings.Contains(string(body), `class="build-version"`) || !strings.Contains(string(body), ">dev</small>") {
		t.Errorf("login page missing development build version")
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
	data := map[string]any{"Cards": []cardListView{{ID: 1, CardUIDPrefix: "AB", Label: "keys"}}}
	if err := tmpl.Fragment(rec, "card-list", data); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "AB") {
		t.Fatalf("fragment missing card UID prefix, got: %s", rec.Body.String())
	}
}
