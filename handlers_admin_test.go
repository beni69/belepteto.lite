package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdminCreateUser(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":            {"new@example.com"},
		"password":         {"secret"},
		"permanent_access": {"on"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "User registered: new@example.com") {
		t.Errorf("response fragment should confirm the new user, got: %s", body)
	}
	if got := resp.Header.Get("HX-Trigger"); got != "user-created" {
		t.Errorf("HX-Trigger = %q, want user-created", got)
	}

	// New user can log in.
	u, err := store.UserByEmail("new@example.com")
	if err != nil {
		t.Fatalf("new user not persisted: %v", err)
	}
	if u.Admin {
		t.Errorf("new user should not be admin by default")
	}
	if !u.PermanentAccess {
		t.Errorf("checked access switch should create an always-allowed user")
	}
	resp, err = admin.Get(srv.URL + "/htmx/admin/users")
	if err != nil {
		t.Fatalf("refresh user list: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "new@example.com") {
		t.Fatalf("refreshed user list omitted created user: %s", body)
	}
}

func TestAdminCreateUserDuplicateEmail(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	seedUser(t, store, "taken@example.com", "pw", false)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":    {"TAKEN@example.com"},
		"password": {"secret"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "already exists") {
		t.Errorf("expected duplicate error, got: %s", body)
	}
	if got := resp.Header.Get("HX-Trigger"); got != "" {
		t.Errorf("duplicate create should not trigger refresh/reset, HX-Trigger = %q", got)
	}
}

func TestAdminCreateScheduledOnlyUser(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":    {"scheduled@example.com"},
		"password": {"secret"},
	})
	if err != nil {
		t.Fatalf("create scheduled-only user: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	created, err := store.UserByEmail("scheduled@example.com")
	if err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if created.PermanentAccess {
		t.Fatal("unchecked access switch should create a scheduled-only user")
	}

	resp, err = admin.Get(srv.URL + "/htmx/admin/access-grant-users")
	if err != nil {
		t.Fatalf("refresh access-grant selector: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "scheduled@example.com — scheduled times only") {
		t.Errorf("selector did not include new scheduled-only user: %s", body)
	}
}

func TestAdminAccessSwitchDefaultsToAlwaysAllowed(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatalf("get admin page: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `name="permanent_access" role="switch" checked`) {
		t.Fatalf("always-allowed switch should be checked by default: %s", body)
	}
}

func TestAdminCreateAdminUser(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":            {"admin2@example.com"},
		"password":         {"secret"},
		"admin":            {"on"},
		"permanent_access": {"on"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	resp.Body.Close()

	u, err := store.UserByEmail("admin2@example.com")
	if err != nil {
		t.Fatalf("not persisted: %v", err)
	}
	if !u.Admin {
		t.Errorf("user should be admin")
	}
}

func TestAdminEndpointsForbiddenForNonAdmin(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "user@example.com", "pw", false)
	user := login(t, srv, "user@example.com", "pw")

	resp, err := user.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":    {"x@example.com"},
		"password": {"secret"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin create: status = %d, want 403", resp.StatusCode)
	}
}

func TestAdminDeactivateRemovesSessions(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	target := seedUser(t, store, "target@example.com", "pw", false)

	// Target logs in and has a working session.
	targetClient := login(t, srv, "target@example.com", "pw")
	resp, _ := targetClient.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("target should have access before deactivation")
	}

	// Admin deactivates the target.
	admin := login(t, srv, "admin@example.com", "pw")
	resp, err := admin.Post(
		fmt.Sprintf("%s/htmx/admin/users/%d/deactivate", srv.URL, target.ID), "", nil)
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Inactive") {
		t.Errorf("row should show Inactive, got: %s", body)
	}

	// Target's session is now invalid.
	noFollow := &http.Client{Jar: targetClient.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = noFollow.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("deactivated target session: status = %d, want 303", resp.StatusCode)
	}
}

func TestAdminReactivate(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	target := seedUser(t, store, "target@example.com", "pw", false)
	if err := store.SetUserActive(target.ID, false); err != nil {
		t.Fatalf("pre-deactivate: %v", err)
	}

	admin := login(t, srv, "admin@example.com", "pw")
	resp, err := admin.Post(
		fmt.Sprintf("%s/htmx/admin/users/%d/activate", srv.URL, target.ID), "", nil)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Active") {
		t.Errorf("row should show Active, got: %s", body)
	}
}

func TestAdminCannotDeactivateSelf(t *testing.T) {
	srv, store := authTestServer(t)
	me := seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.Post(
		fmt.Sprintf("%s/htmx/admin/users/%d/deactivate", srv.URL, me.ID), "", nil)
	if err != nil {
		t.Fatalf("deactivate self: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("self-deactivate: status = %d, want 400", resp.StatusCode)
	}
}

func TestAdminCanRenameAndUnpairUserCard(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	card := seedCard(t, store, owner.ID, "aa:bb:cc:01", "front door")
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.Get(srv.URL + "/htmx/admin/users")
	if err != nil {
		t.Fatalf("load admin user list: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "front door") || !strings.Contains(string(body), "AA") {
		t.Fatalf("admin card list missing label or safe UID prefix: %s", body)
	}
	if strings.Contains(string(body), card.CardUID) {
		t.Fatalf("admin HTML must not expose the stored card hash")
	}

	resp, err = admin.PostForm(fmt.Sprintf("%s/htmx/admin/users/%d/cards/%d/relabel", srv.URL, owner.ID, card.ID), url.Values{
		"label": {"back door"},
	})
	if err != nil {
		t.Fatalf("relabel card: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "back door") {
		t.Fatalf("relabel response = %d/%s", resp.StatusCode, body)
	}
	updated, err := store.CardsByUser(owner.ID)
	if err != nil || len(updated) != 1 || updated[0].Label != "back door" {
		t.Fatalf("stored card after relabel = %+v, err = %v", updated, err)
	}

	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/htmx/admin/users/%d/cards/%d", srv.URL, owner.ID, card.ID), nil)
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatalf("unpair card: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "No cards paired yet") {
		t.Fatalf("unpair response = %d/%s", resp.StatusCode, body)
	}
	if cards, _ := store.CardsByUser(owner.ID); len(cards) != 0 {
		t.Fatalf("unpaired card remains in store: %+v", cards)
	}
}

func TestAdminCardManagementIsAdminOnlyAndOwnerScoped(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	other := seedUser(t, store, "other@example.com", "pw", false)
	card := seedCard(t, store, owner.ID, "aa:bb:cc:02", "owned")

	user := login(t, srv, "owner@example.com", "pw")
	resp, err := user.PostForm(fmt.Sprintf("%s/htmx/admin/users/%d/cards/%d/relabel", srv.URL, owner.ID, card.ID), url.Values{
		"label": {"unauthorized"},
	})
	if err != nil {
		t.Fatalf("non-admin relabel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin relabel status = %d, want 403", resp.StatusCode)
	}

	admin := login(t, srv, "admin@example.com", "pw")
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/htmx/admin/users/%d/cards/%d", srv.URL, other.ID, card.ID), nil)
	resp, err = admin.Do(req)
	if err != nil {
		t.Fatalf("cross-owner unpair: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-owner unpair status = %d, want 404", resp.StatusCode)
	}
	if cards, _ := store.CardsByUser(owner.ID); len(cards) != 1 {
		t.Fatalf("owner card should remain after cross-owner request")
	}
}
