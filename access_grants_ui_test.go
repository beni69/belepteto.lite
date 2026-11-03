package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdminCanCreateAndManageScheduledEntryTimes(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "admin@example.com", "pw", true)
	person := seedUser(t, store, "person@example.com", "pw", false)
	admin := login(t, srv, "admin@example.com", "pw")

	resp, err := admin.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatalf("get admin page: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin page status = %d, want 200", resp.StatusCode)
	}
	for _, text := range []string{"Temporary entry times", "Choose a person", "Starts", "Ends", "always allowed"} {
		if !strings.Contains(string(page), text) {
			t.Errorf("admin page missing %q", text)
		}
	}

	from := time.Now().In(time.Local).Add(time.Hour).Truncate(time.Minute)
	until := from.Add(2 * time.Hour)
	resp, err = admin.PostForm(srv.URL+"/htmx/admin/access-grants", url.Values{
		"user_id":     {stringID(person.ID)},
		"valid_from":  {from.Format("2006-01-02T15:04")},
		"valid_until": {until.Format("2006-01-02T15:04")},
	})
	if err != nil {
		t.Fatalf("create entry time: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), person.Email) || !strings.Contains(string(body), "Starts later") {
		t.Fatalf("create response status/body = %d/%s", resp.StatusCode, body)
	}

	// Creating a time window does not silently remove the user's existing
	// always-allowed setting.
	reloaded, err := store.UserByID(person.ID)
	if err != nil {
		t.Fatalf("reload user after creating window: %v", err)
	}
	if !reloaded.PermanentAccess {
		t.Fatal("creating an entry time must not remove permanent access")
	}
	var grantCount int64
	if err := store.db.Model(&AccessGrant{}).Where("user_id = ?", person.ID).Count(&grantCount).Error; err != nil || grantCount != 1 {
		t.Fatalf("grant count = %d, err = %v; want 1", grantCount, err)
	}

	// The admin can explicitly make the user time-limited without deleting the
	// saved window.
	resp, err = admin.Post(srv.URL+"/htmx/admin/users/"+stringID(person.ID)+"/scheduled-access", "", nil)
	if err != nil {
		t.Fatalf("switch to scheduled access: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Only during listed times") {
		t.Fatalf("scheduled setting response status/body = %d/%s", resp.StatusCode, body)
	}

	resp, err = admin.Post(srv.URL+"/htmx/admin/users/"+stringID(person.ID)+"/permanent-access", "", nil)
	if err != nil {
		t.Fatalf("restore always-allowed access: %v", err)
	}
	resp.Body.Close()
	reloaded, err = store.UserByID(person.ID)
	if err != nil {
		t.Fatalf("reload user after restoring access: %v", err)
	}
	if !reloaded.PermanentAccess {
		t.Fatal("restoring anytime access did not persist")
	}

	var grant AccessGrant
	if err := store.db.Where("user_id = ?", person.ID).First(&grant).Error; err != nil {
		t.Fatalf("find saved entry time: %v", err)
	}
	resp, err = admin.Post(srv.URL+"/htmx/admin/access-grants/"+stringID(grant.ID)+"/revoke", "", nil)
	if err != nil {
		t.Fatalf("remove entry time: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "No current or upcoming entry times") {
		t.Fatalf("remove response status/body = %d/%s", resp.StatusCode, body)
	}
}

func TestNonAdminCannotManageScheduledEntryTimes(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "person@example.com", "pw", false)
	user := login(t, srv, "person@example.com", "pw")
	resp, err := user.PostForm(srv.URL+"/htmx/admin/access-grants", url.Values{
		"user_id": {"1"},
	})
	if err != nil {
		t.Fatalf("post access grant: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin access grant status = %d, want 403", resp.StatusCode)
	}
}

func stringID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
