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
		"email":    {"new@example.com"},
		"password": {"secret"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "new@example.com") {
		t.Errorf("response fragment should list the new user")
	}

	// New user can log in.
	u, err := store.UserByEmail("new@example.com")
	if err != nil {
		t.Fatalf("new user not persisted: %v", err)
	}
	if u.Admin {
		t.Errorf("new user should not be admin by default")
	}
}

func TestAdminCreateUserDuplicateEmail(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	seedUser(t, store, "taken@example.com", "pw", false)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":    {"taken@example.com"},
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
}

func TestAdminCreateAdminUser(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.PostForm(srv.URL+"/htmx/admin/users", url.Values{
		"email":    {"admin2@example.com"},
		"password": {"secret"},
		"admin":    {"on"},
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
