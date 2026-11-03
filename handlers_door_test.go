package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDoorOpenEndpointRechecksAccessAndLogsRequest(t *testing.T) {
	srv, store := authTestServer(t)
	user := seedUser(t, store, "opener@example.com", "pw", false)
	client := login(t, srv, user.Email, "pw")

	resp, err := client.Post(srv.URL+"/htmx/door/open", "", nil)
	if err != nil {
		t.Fatalf("open door: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode open response %q: %v", body, err)
	}
	if resp.StatusCode != http.StatusOK || result["ok"] != true {
		t.Fatalf("open response = %d/%s", resp.StatusCode, body)
	}
	var entries int64
	if err := store.db.Model(&Entry{}).Where("user_id = ?", user.ID).Count(&entries).Error; err != nil || entries != 1 {
		t.Fatalf("entry count = %d, err = %v; want 1", entries, err)
	}

	if err := store.SetUserPermanentAccess(user.ID, false); err != nil {
		t.Fatalf("make user scheduled-only: %v", err)
	}
	resp, err = client.Post(srv.URL+"/htmx/door/open", "", nil)
	if err != nil {
		t.Fatalf("open without active grant: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !json.Valid(body) {
		t.Fatalf("denied open response = %d/%s", resp.StatusCode, body)
	}
	if err := store.db.Model(&Entry{}).Where("user_id = ?", user.ID).Count(&entries).Error; err != nil || entries != 1 {
		t.Fatalf("denied request changed entry count to %d, err = %v", entries, err)
	}
}

func TestDoorOpenRequiresAuthentication(t *testing.T) {
	srv := newTestServer(t)
	client := noRedirectClient()
	resp, err := client.Post(srv.URL+"/htmx/door/open", "", nil)
	if err != nil {
		t.Fatalf("unauthenticated door open: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated open status = %d, want 303", resp.StatusCode)
	}
}

func TestDoorOpenControlOnlyShowsForCurrentAccess(t *testing.T) {
	srv, store := authTestServer(t)
	user := seedUser(t, store, "scheduled@example.com", "pw", false)
	if err := store.SetUserPermanentAccess(user.ID, false); err != nil {
		t.Fatalf("make user scheduled-only: %v", err)
	}
	client := login(t, srv, user.Email, "pw")

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get home without grant: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), `id="door-open-button"`) {
		t.Fatal("FAB should be hidden without a current grant")
	}

	now := time.Now().UTC()
	if err := store.CreateManualAccessGrant(user.ID, now.Add(-time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("create active grant: %v", err)
	}
	resp, err = client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get home with grant: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `id="door-open-button"`) {
		t.Fatal("FAB should be visible with a current grant")
	}
}
