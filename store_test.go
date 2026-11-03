package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

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
			{CardUID: "DE:AD:BE:EF", Label: "keychain", Active: true},
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
	wantHash := hashCardUID("DE:AD:BE:EF")
	if len(cards) != 1 || cards[0].CardUID != wantHash || cards[0].CardUIDPrefix != "DE" {
		t.Fatalf("unexpected cards: %+v", cards)
	}
}

func TestCreateUserWithAccessChoice(t *testing.T) {
	store := newTestStore(t)

	scheduled := &User{Email: "scheduled@example.com", PasswordHash: "hash", Active: true}
	if err := store.CreateUserWithAccess(scheduled, false); err != nil {
		t.Fatalf("create scheduled-only user: %v", err)
	}
	if scheduled.PermanentAccess {
		t.Fatal("created user should be scheduled-only")
	}
	reloaded, err := store.UserByID(scheduled.ID)
	if err != nil {
		t.Fatalf("reload scheduled-only user: %v", err)
	}
	if reloaded.PermanentAccess {
		t.Fatal("scheduled-only setting was not persisted")
	}

	legacyDefault := &User{Email: "default@example.com", PasswordHash: "hash", Active: true}
	if err := store.CreateUser(legacyDefault); err != nil {
		t.Fatalf("create with legacy default: %v", err)
	}
	if !legacyDefault.PermanentAccess {
		t.Fatal("CreateUser should retain the always-allowed default")
	}
}

func TestUserByEmailNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.UserByEmail("nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUserByEmailIgnoresCaseAndWhitespace(t *testing.T) {
	store := newTestStore(t)
	user := &User{Email: "person@example.com", PasswordHash: "h", Active: true}
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	got, err := store.UserByEmail("  PERSON@Example.com ")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("user id = %d, want %d", got.ID, user.ID)
	}
}

func TestScheduledAccessGrantUsesHalfOpenWindowAndRevocation(t *testing.T) {
	store := newTestStore(t)
	user := &User{Email: "scheduled@example.com", PasswordHash: "h", Active: true}
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.SetUserPermanentAccess(user.ID, false); err != nil {
		t.Fatalf("set scheduled access: %v", err)
	}
	from := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	until := from.Add(time.Hour)
	if err := store.CreateManualAccessGrant(user.ID, from, until); err != nil {
		t.Fatalf("create grant: %v", err)
	}

	checks := []struct {
		at   time.Time
		want bool
	}{
		{at: from.Add(-time.Second), want: false},
		{at: from, want: true},
		{at: until.Add(-time.Second), want: true},
		{at: until, want: false},
	}
	for _, check := range checks {
		got, err := store.HasActiveAccessGrant(user.ID, check.at)
		if err != nil {
			t.Fatalf("HasActiveAccessGrant(%s): %v", check.at, err)
		}
		if got != check.want {
			t.Errorf("HasActiveAccessGrant(%s) = %v, want %v", check.at, got, check.want)
		}
	}

	grants, err := store.ListVisibleAccessGrants(from)
	if err != nil || len(grants) != 1 {
		t.Fatalf("visible grants = %d, err = %v; want 1 grant", len(grants), err)
	}
	if err := store.RevokeAccessGrant(grants[0].ID, from); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if got, err := store.HasActiveAccessGrant(user.ID, from.Add(time.Minute)); err != nil || got {
		t.Fatalf("revoked grant active = %v, err = %v; want false", got, err)
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
	// The same UID with different casing must be rejected after uppercasing.
	if err := store.CreateCard(&Card{UserID: u2.ID, CardUID: "AA:BB:CC:DD", Active: true}); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate for the same UID, got %v", err)
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
		// Legacy databases can contain case variants even though new accounts
		// normalize email addresses before insertion.
		for _, email := range []string{"Person@example.com", "person@example.com"} {
			if err := store.db.Create(&User{Email: email, PasswordHash: "hash", Active: true}).Error; err != nil {
				t.Fatalf("create %q: %v", email, err)
			}
		}
		if _, err := store.LinkOIDCIdentity("issuer", "subject", "PERSON@example.com"); !errors.Is(err, ErrAmbiguousEmail) {
			t.Fatalf("link error = %v, want ErrAmbiguousEmail", err)
		}
	})
}
