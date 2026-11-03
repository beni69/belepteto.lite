package main

import (
	"net/http"
	"testing"
	"time"
)

func TestListCardReadsFilterAndJoin(t *testing.T) {
	store := newTestStore(t)
	u := &User{Email: "u@example.com", PasswordHash: "h", Active: true}
	if err := store.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	now := time.Now().UTC()
	// Accepted read, resolved to user.
	if err := store.CreateCardRead(&CardRead{CardUID: "aa:bb", UserID: &u.ID, Accepted: true, CreatedAt: now}); err != nil {
		t.Fatalf("read1: %v", err)
	}
	// Denied read, unknown card (no user).
	if err := store.CreateCardRead(&CardRead{CardUID: "ff:ff", Accepted: false, CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("read2: %v", err)
	}

	// All reads.
	rows, total, err := store.ListCardReads(false, 20, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("all: total=%d rows=%d, want 2/2", total, len(rows))
	}
	// Newest first: the denied read is newer.
	if rows[0].CardUIDPrefix != "FF" || rows[0].Accepted {
		t.Errorf("expected newest (denied) first, got %+v", rows[0])
	}
	if rows[0].Email != "" {
		t.Errorf("unknown card should have empty email, got %q", rows[0].Email)
	}
	if rows[1].Email != "u@example.com" {
		t.Errorf("accepted read should join user email, got %q", rows[1].Email)
	}

	// Accepted only.
	rows, total, err = store.ListCardReads(true, 20, 0)
	if err != nil {
		t.Fatalf("list accepted: %v", err)
	}
	if total != 1 || len(rows) != 1 || !rows[0].Accepted {
		t.Fatalf("accepted filter: total=%d rows=%d", total, len(rows))
	}
}

func TestListCardReadsPagination(t *testing.T) {
	store := newTestStore(t)
	base := time.Now().UTC()
	for i := 0; i < 25; i++ {
		if err := store.CreateCardRead(&CardRead{
			CardUID:   "c",
			Accepted:  false,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	rows, total, err := store.ListCardReads(false, 20, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if total != 25 || len(rows) != 20 {
		t.Fatalf("page1: total=%d rows=%d, want 25/20", total, len(rows))
	}
	rows, _, err = store.ListCardReads(false, 20, 20)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("page2 rows=%d, want 5", len(rows))
	}
}

func TestListEntriesJoin(t *testing.T) {
	store := newTestStore(t)
	u := &User{Email: "opener@example.com", PasswordHash: "h", Active: true}
	if err := store.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	uid := "aa:bb:cc"
	if err := store.CreateEntry(&Entry{UserID: u.ID, CardUID: &uid, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("entry: %v", err)
	}
	rows, total, err := store.ListEntries(20, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d rows=%d", total, len(rows))
	}
	if rows[0].Email != "opener@example.com" {
		t.Errorf("email = %q", rows[0].Email)
	}
	if rows[0].CardUIDPrefix == nil || *rows[0].CardUIDPrefix != cardUIDPrefix(uid) {
		t.Errorf("card uid prefix = %v", rows[0].CardUIDPrefix)
	}
}

func TestAdminLogViewerForbiddenForNonAdmin(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "user@example.com", "pw", false)
	user := login(t, srv, "user@example.com", "pw")

	for _, path := range []string{"/htmx/admin/logs/entries", "/htmx/admin/logs/cardreads"} {
		resp, err := user.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestAdminLogViewerRenders(t *testing.T) {
	srv, store := authTestServer(t)
	admin := seedUser(t, store, "admin@example.com", "pw", true)
	uid := "aa:bb:cc"
	_ = store.CreateEntry(&Entry{UserID: admin.ID, CardUID: &uid, CreatedAt: time.Now().UTC()})
	_ = store.CreateCardRead(&CardRead{CardUID: "aa:bb:cc", UserID: &admin.ID, Accepted: true, CreatedAt: time.Now().UTC()})

	client := login(t, srv, "admin@example.com", "pw")
	for _, path := range []string{"/htmx/admin/logs/entries", "/htmx/admin/logs/cardreads"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}
