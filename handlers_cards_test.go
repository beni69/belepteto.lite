package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func addCard(t *testing.T, client *http.Client, srv string, uid, label string) *http.Response {
	t.Helper()
	resp, err := client.PostForm(srv+"/htmx/cards", url.Values{
		"card_uid": {uid},
		"label":    {label},
	})
	if err != nil {
		t.Fatalf("add card: %v", err)
	}
	return resp
}

func TestCardAddAndList(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")

	resp := addCard(t, client, srv.URL, "deadbeef", "keychain")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// UID is normalized to upper-case.
	if !strings.Contains(string(body), "DEADBEEF") {
		t.Errorf("card list should contain normalized UID, got: %s", body)
	}
	if !strings.Contains(string(body), "keychain") {
		t.Errorf("card list should contain label")
	}
}

func TestCardDuplicateUID(t *testing.T) {
	srv, store := authTestServer(t)
	seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")

	addCard(t, client, srv.URL, "ABC123", "first").Body.Close()
	// Same UID, different casing — must still be rejected after normalization.
	resp := addCard(t, client, srv.URL, "abc123", "second")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "already registered") {
		t.Errorf("expected duplicate error, got: %s", body)
	}
}

func TestCardDeleteOwn(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")
	addCard(t, client, srv.URL, "CARD1", "").Body.Close()

	cards, _ := store.CardsByUser(u.ID)
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}

	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/htmx/cards/%d", srv.URL, cards[0].ID), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}

	cards, _ = store.CardsByUser(u.ID)
	if len(cards) != 0 {
		t.Errorf("card should be deleted, %d remain", len(cards))
	}
}

func TestCardCannotDeleteOthers(t *testing.T) {
	srv, store := authTestServer(t)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	seedUser(t, store, "attacker@example.com", "pw", false)

	// Owner adds a card directly via the store.
	if err := store.CreateCard(&Card{UserID: owner.ID, CardUID: "SECRET", Active: true}); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	cards, _ := store.CardsByUser(owner.ID)
	victimID := cards[0].ID

	attacker := login(t, srv, "attacker@example.com", "pw")
	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/htmx/cards/%d", srv.URL, victimID), nil)
	resp, err := attacker.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user delete: status = %d, want 404", resp.StatusCode)
	}

	// Card must still exist.
	if cards, _ := store.CardsByUser(owner.ID); len(cards) != 1 {
		t.Errorf("owner's card should be untouched")
	}
}

func TestCardRelabel(t *testing.T) {
	srv, store := authTestServer(t)
	u := seedUser(t, store, "u@example.com", "pw", false)
	client := login(t, srv, "u@example.com", "pw")
	addCard(t, client, srv.URL, "CARD1", "old").Body.Close()

	cards, _ := store.CardsByUser(u.ID)
	resp, err := client.PostForm(
		fmt.Sprintf("%s/htmx/cards/%d/relabel", srv.URL, cards[0].ID),
		url.Values{"label": {"new label"}})
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "new label") {
		t.Errorf("relabel not reflected, got: %s", body)
	}
}

func TestCardRelabelCrossUserForbidden(t *testing.T) {
	srv, store := authTestServer(t)
	owner := seedUser(t, store, "owner@example.com", "pw", false)
	seedUser(t, store, "attacker@example.com", "pw", false)
	if err := store.CreateCard(&Card{UserID: owner.ID, CardUID: "SECRET", Label: "orig", Active: true}); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	cards, _ := store.CardsByUser(owner.ID)

	attacker := login(t, srv, "attacker@example.com", "pw")
	resp, err := attacker.PostForm(
		fmt.Sprintf("%s/htmx/cards/%d/relabel", srv.URL, cards[0].ID),
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
