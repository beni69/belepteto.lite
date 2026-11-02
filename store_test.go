package main

import (
	"errors"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store
}

func TestCreateAndReadUserWithCard(t *testing.T) {
	store := newTestStore(t)

	u := &User{
		Email:        "admin@example.com",
		PasswordHash: "hash",
		Admin:        true,
		Active:       true,
		Cards: []Card{
			{CardUID: "DEADBEEF", Label: "keychain", Active: true},
		},
	}
	if err := store.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("expected user ID to be populated after create")
	}

	got, err := store.UserByEmail("admin@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if !got.Admin || !got.Active {
		t.Fatalf("unexpected user: %+v", got)
	}

	cards, err := store.CardsByUser(u.ID)
	if err != nil {
		t.Fatalf("CardsByUser: %v", err)
	}
	if len(cards) != 1 || cards[0].CardUID != "DEADBEEF" {
		t.Fatalf("unexpected cards: %+v", cards)
	}
}

func TestUserByEmailNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.UserByEmail("nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCardUIDUnique(t *testing.T) {
	store := newTestStore(t)

	u1 := &User{Email: "a@example.com", PasswordHash: "h", Active: true}
	u2 := &User{Email: "b@example.com", PasswordHash: "h", Active: true}
	if err := store.CreateUser(u1); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := store.CreateUser(u2); err != nil {
		t.Fatalf("create u2: %v", err)
	}

	if err := store.db.Create(&Card{UserID: u1.ID, CardUID: "SAME", Active: true}).Error; err != nil {
		t.Fatalf("first card: %v", err)
	}
	if err := store.db.Create(&Card{UserID: u2.ID, CardUID: "SAME", Active: true}).Error; err == nil {
		t.Fatal("expected duplicate card_uid to be rejected")
	}
}
