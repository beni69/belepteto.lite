package main

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// seedCard creates a card for a user directly via the store.
func seedCard(t *testing.T, store *Store, userID uint, uid, label string) *Card {
	t.Helper()
	c := &Card{UserID: userID, CardUID: uid, Label: label, Active: true}
	if err := store.CreateCard(c); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	return c
}

func TestCardDeleteOwn(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")
	card := seedCard(t, store, u.ID, "aa:bb:cc:01", "")

	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/htmx/cards/%d", srv.URL, card.ID), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	if cards, _ := store.CardsByUser(u.ID); len(cards) != 0 {
		t.Errorf("card should be deleted, %d remain", len(cards))
	}
}

func TestCardCannotDeleteOthers(t *testing.T) {
	srv, store := authTestServer(t)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	seedUser(t, store, "attacker@example.com", "pw", false)
	victim := seedCard(t, store, owner.ID, "aa:bb:cc:02", "")

	attacker := login(t, srv, "attacker@example.com", "pw")
	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/htmx/cards/%d", srv.URL, victim.ID), nil)
	resp, err := attacker.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user delete: status = %d, want 404", resp.StatusCode)
	}
	if cards, _ := store.CardsByUser(owner.ID); len(cards) != 1 {
		t.Errorf("owner's card should be untouched")
	}
}

func TestCardRelabel(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")
	card := seedCard(t, store, u.ID, "aa:bb:cc:03", "old")

	resp, err := client.PostForm(
		fmt.Sprintf("%s/htmx/cards/%d/relabel", srv.URL, card.ID),
		url.Values{"label": {"new label"}})
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	resp.Body.Close()
	cards, _ := store.CardsByUser(u.ID)
	if cards[0].Label != "new label" {
		t.Errorf("label = %q, want %q", cards[0].Label, "new label")
	}
}

func TestCardRelabelCrossUserForbidden(t *testing.T) {
	srv, store := authTestServer(t)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	seedUser(t, store, "attacker@example.com", "pw", false)
	card := seedCard(t, store, owner.ID, "aa:bb:cc:04", "orig")

	attacker := login(t, srv, "attacker@example.com", "pw")
	resp, err := attacker.PostForm(
		fmt.Sprintf("%s/htmx/cards/%d/relabel", srv.URL, card.ID),
		url.Values{"label": {"hacked"}})
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user relabel: status = %d, want 404", resp.StatusCode)
	}
	reloaded, _ := store.CardsByUser(owner.ID)
	if reloaded[0].Label != "orig" {
		t.Errorf("label should be unchanged, got %q", reloaded[0].Label)
	}
}

func TestCardRoutesRequireAuth(t *testing.T) {
	srv, _ := authTestServer(t)
	client := noRedirectClient()
	resp, err := client.Get(srv.URL + "/htmx/cards")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("unauthenticated /htmx/cards: status = %d, want 303", resp.StatusCode)
	}
}
