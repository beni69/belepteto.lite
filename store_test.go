package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	// Transactions may use a different pooled SQLite connection. A uniquely
	// named shared in-memory database keeps the schema visible on every one.
	store, err := OpenStore(fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString()))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	db, err := store.db.DB()
	if err != nil {
		t.Fatalf("get sql DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
			{CardUID: "DEADBEEF", CardUIDCanonical: "DEADBEEF", Label: "keychain", Active: true},
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

	if err := store.CreateCard(&Card{UserID: u1.ID, CardUID: "aa:bb:cc:dd", Active: true}); err != nil {
		t.Fatalf("first card: %v", err)
	}
	// Same card in different formatting/casing must be rejected (lenient match).
	if err := store.CreateCard(&Card{UserID: u2.ID, CardUID: "AABBCCDD", Active: true}); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate for lenient-equal UID, got %v", err)
	}
}

func TestCreateUserDuplicateEmail(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateUser(&User{Email: "dup@example.com", PasswordHash: "h", Active: true}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := store.CreateUser(&User{Email: "dup@example.com", PasswordHash: "h", Active: true})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestSetUserActiveNotFound(t *testing.T) {
	store := newTestStore(t)
	if err := store.SetUserActive(9999, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLinkOIDCIdentityToExistingActiveUser(t *testing.T) {
	store := newTestStore(t)
	u := &User{Email: "Person@Example.com", PasswordHash: "hash", Active: true}
	if err := store.CreateUser(u); err != nil {
		t.Fatalf("create local user: %v", err)
	}
	linked, err := store.LinkOIDCIdentity("https://issuer.example", "subject-1", "person@example.com")
	if err != nil {
		t.Fatalf("link identity: %v", err)
	}
	if linked.ID != u.ID || linked.OIDCIssuer == nil || *linked.OIDCIssuer != "https://issuer.example" ||
		linked.OIDCSubject == nil || *linked.OIDCSubject != "subject-1" {
		t.Fatalf("unexpected linked user: %+v", linked)
	}

	got, err := store.UserByOIDCIdentity("https://issuer.example", "subject-1")
	if err != nil || got.ID != u.ID {
		t.Fatalf("lookup linked identity = (%+v, %v), want user %d", got, err, u.ID)
	}

	// A different identity cannot take over the account after the first link.
	if _, err := store.LinkOIDCIdentity("https://issuer.example", "subject-2", "person@example.com"); !errors.Is(err, ErrOIDCIdentityConflict) {
		t.Fatalf("second subject link error = %v, want ErrOIDCIdentityConflict", err)
	}
}

func TestOIDCLinkRejectsMissingInactiveAndAmbiguousLocalUsers(t *testing.T) {
	t.Run("unknown email", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.LinkOIDCIdentity("issuer", "subject", "missing@example.com"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("link error = %v, want ErrNotFound", err)
		}
	})

	t.Run("inactive user", func(t *testing.T) {
		store := newTestStore(t)
		u := &User{Email: "inactive@example.com", PasswordHash: "hash", Active: false}
		if err := store.CreateUser(u); err != nil {
			t.Fatalf("create local user: %v", err)
		}
		if err := store.SetUserActive(u.ID, false); err != nil {
			t.Fatalf("deactivate local user: %v", err)
		}
		if _, err := store.LinkOIDCIdentity("issuer", "subject", u.Email); !errors.Is(err, ErrNotFound) {
			t.Fatalf("link error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ambiguous case-insensitive email", func(t *testing.T) {
		store := newTestStore(t)
		for _, email := range []string{"Person@example.com", "person@example.com"} {
			if err := store.CreateUser(&User{Email: email, PasswordHash: "hash", Active: true}); err != nil {
				t.Fatalf("create %q: %v", email, err)
			}
		}
		if _, err := store.LinkOIDCIdentity("issuer", "subject", "PERSON@example.com"); !errors.Is(err, ErrAmbiguousEmail) {
			t.Fatalf("link error = %v, want ErrAmbiguousEmail", err)
		}
	})
}
