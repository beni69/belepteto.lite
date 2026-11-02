package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func externalAPITestServer(t *testing.T, token string) (*httptest.Server, *Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	store := newTestStore(t)
	access := newTestAccess(t, store)
	srv := httptest.NewServer(newRouter(logger, tmpl, store, access, token))
	t.Cleanup(srv.Close)
	return srv, store
}

func externalAPIRequest(t *testing.T, method, target, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, target, err)
	}
	return resp
}

func TestLoadConfigReadsExternalAPIAccessToken(t *testing.T) {
	t.Setenv("EXTERNAL_API_ACCESS_TOKEN", "configured-secret")
	if got := loadConfig().externalAPIAccessToken; got != "configured-secret" {
		t.Fatalf("external API token = %q, want configured value", got)
	}
}

func TestExternalAPIRequiresConfiguredBearerToken(t *testing.T) {
	srv, _ := externalAPITestServer(t, "correct-secret")
	for _, token := range []string{"", "wrong-secret"} {
		resp := externalAPIRequest(t, http.MethodGet, srv.URL+"/api/v1/access-grant-sets?source=x&scope_ref=y", token, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", token, resp.StatusCode)
		}
	}

	disabled, _ := externalAPITestServer(t, "")
	resp := externalAPIRequest(t, http.MethodGet, disabled.URL+"/api/v1/access-grant-sets?source=x&scope_ref=y", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("disabled API status = %d, want 503", resp.StatusCode)
	}
}

func TestExternalAPIReconcilesVersionedAccessSnapshot(t *testing.T) {
	const token = "test-token"
	srv, store := externalAPITestServer(t, token)
	user := seedUser(t, store, "person@example.com", "pw", false)

	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	end := start.Add(time.Hour)
	req := ExternalAccessGrantSetRequest{
		Source:   "booking-system",
		ScopeRef: "opaque-scope-123",
		Revision: 1,
		Grants: []ExternalAccessGrant{{
			ExternalRef: "registration-1",
			Email:       " PERSON@Example.com ",
			ValidFrom:   start,
			ValidUntil:  end,
		}},
	}
	postSnapshot := func(request ExternalAccessGrantSetRequest) (*http.Response, ExternalAccessGrantSetResult) {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		resp := externalAPIRequest(t, http.MethodPut, srv.URL+"/api/v1/access-grant-sets", token, strings.NewReader(string(body)))
		var result ExternalAccessGrantSetResult
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				t.Fatalf("decode response: %v", err)
			}
		}
		return resp, result
	}

	resp, result := postSnapshot(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first snapshot status = %d, want 200", resp.StatusCode)
	}
	if !result.Applied || !result.RevisionAdvanced || result.GrantCount != 1 || len(result.UsersWithAlwaysAccess) != 1 || result.UsersWithAlwaysAccess[0] != user.Email {
		t.Fatalf("unexpected first result: %+v", result)
	}
	reloaded, err := store.UserByID(user.ID)
	if err != nil {
		t.Fatalf("reload user after external sync: %v", err)
	}
	if !reloaded.PermanentAccess {
		t.Fatal("external API must not change permanent access")
	}
	if exists, err := store.HasActiveAccessGrant(user.ID, time.Now().UTC()); err != nil || !exists {
		t.Fatalf("active grant = %v, err = %v; want true", exists, err)
	}

	// Replaying an identical revision is safe and reconciles without advancing
	// the revision.
	resp, result = postSnapshot(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !result.Applied || result.RevisionAdvanced {
		t.Fatalf("retry status/result = %d/%+v, want 200 and no revision advance", resp.StatusCode, result)
	}
	var storedGrant AccessGrant
	if err := store.db.Where("external_ref = ?", "registration-1").First(&storedGrant).Error; err != nil {
		t.Fatalf("load grant before drift test: %v", err)
	}
	if err := store.RevokeAccessGrant(storedGrant.ID, time.Now().UTC()); err != nil {
		t.Fatalf("simulate local drift: %v", err)
	}
	resp, result = postSnapshot(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !result.Applied || result.RevisionAdvanced {
		t.Fatalf("same-revision reconciliation status/result = %d/%+v", resp.StatusCode, result)
	}
	if exists, err := store.HasActiveAccessGrant(user.ID, time.Now().UTC()); err != nil || !exists {
		t.Fatalf("same-revision reconciliation restored grant = %v, err = %v", exists, err)
	}

	// A full, newer snapshot with no entries revokes the prior external grant.
	request := req
	request.Revision = 2
	request.Grants = []ExternalAccessGrant{}
	resp, result = postSnapshot(request)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !result.Applied || !result.RevisionAdvanced || result.GrantCount != 0 {
		t.Fatalf("empty snapshot status/result = %d/%+v", resp.StatusCode, result)
	}
	if exists, err := store.HasActiveAccessGrant(user.ID, time.Now().UTC()); err != nil || exists {
		t.Fatalf("grant after reconciliation = %v, err = %v; want false", exists, err)
	}

	// A delayed earlier update cannot restore the revoked entry.
	resp, _ = postSnapshot(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "stale_revision") {
		t.Fatalf("stale snapshot status/body = %d/%s", resp.StatusCode, body)
	}

	query := url.Values{"source": {req.Source}, "scope_ref": {req.ScopeRef}}
	resp = externalAPIRequest(t, http.MethodGet, srv.URL+"/api/v1/access-grant-sets?"+query.Encode(), token, nil)
	defer resp.Body.Close()
	var state ExternalAccessGrantSetState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if resp.StatusCode != http.StatusOK || state.Revision != 2 || len(state.Grants) != 0 {
		t.Fatalf("unexpected state response: status=%d state=%+v", resp.StatusCode, state)
	}
}

func TestExternalAPIDoesNotCreateUnknownUsersOrPartiallyApply(t *testing.T) {
	const token = "test-token"
	srv, store := externalAPITestServer(t, token)
	known := seedUser(t, store, "known@example.com", "pw", false)
	before, err := store.CountUsers()
	if err != nil {
		t.Fatalf("count users: %v", err)
	}

	req := ExternalAccessGrantSetRequest{
		Source:   "booking-system",
		ScopeRef: "scope-with-unknown-user",
		Revision: 1,
		Grants: []ExternalAccessGrant{
			{ExternalRef: "known", Email: known.Email, ValidFrom: time.Now().UTC(), ValidUntil: time.Now().UTC().Add(time.Hour)},
			{ExternalRef: "unknown", Email: "new@example.com", ValidFrom: time.Now().UTC(), ValidUntil: time.Now().UTC().Add(time.Hour)},
		},
	}
	body, _ := json.Marshal(req)
	resp := externalAPIRequest(t, http.MethodPut, srv.URL+"/api/v1/access-grant-sets", token, strings.NewReader(string(body)))
	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(responseBody), "unknown_user") {
		t.Fatalf("status/body = %d/%s, want 422 unknown_user", resp.StatusCode, responseBody)
	}
	after, err := store.CountUsers()
	if err != nil || after != before {
		t.Fatalf("user count after snapshot = %d, err = %v; want %d", after, err, before)
	}
	if exists, err := store.HasActiveAccessGrant(known.ID, time.Now().UTC()); err != nil || exists {
		t.Fatalf("known user's partial grant = %v, err = %v; want false", exists, err)
	}
}

func TestExternalAPIRejectsInvalidJSONAndDuplicateReferences(t *testing.T) {
	srv, _ := externalAPITestServer(t, "test-token")
	for _, body := range []string{
		`{"source":"x","scope_ref":"y","revision":1,"grants":[],"unexpected":true}`,
		`{"source":"x","scope_ref":"y","revision":1,"grants":[{"external_ref":"same","email":"a@example.com","valid_from":"2026-01-01T00:00:00Z","valid_until":"2026-01-01T01:00:00Z"},{"external_ref":"same","email":"a@example.com","valid_from":"2026-01-01T00:00:00Z","valid_until":"2026-01-01T01:00:00Z"}]}`,
	} {
		resp := externalAPIRequest(t, http.MethodPut, srv.URL+"/api/v1/access-grant-sets", "test-token", strings.NewReader(body))
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
}
