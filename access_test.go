package main

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// accessFixture wires a store + broker + access service and captures messages
// published to the controller's doorControl and command topics.
type accessFixture struct {
	store  *Store
	broker *Broker
	access *AccessService
	topics ControllerTopics
	door   chan string
	cmd    chan string
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := newTestStore(t)
	broker := newTestBroker(t)
	topics := NewControllerTopics("TEST")
	access := NewAccessService(store, broker, topics, logger)
	if err := access.Start(); err != nil {
		t.Fatalf("access.Start: %v", err)
	}
	t.Cleanup(access.Close)

	f := &accessFixture{
		store:  store,
		broker: broker,
		access: access,
		topics: topics,
		door:   make(chan string, 8),
		cmd:    make(chan string, 8),
	}
	if err := broker.Subscribe(topics.DoorControl(), func(_ string, p []byte) { f.door <- string(p) }); err != nil {
		t.Fatalf("subscribe door: %v", err)
	}
	if err := broker.Subscribe(topics.Command(), func(_ string, p []byte) { f.cmd <- string(p) }); err != nil {
		t.Fatalf("subscribe cmd: %v", err)
	}
	return f
}

func (f *accessFixture) sendCardRead(t *testing.T, json string) {
	t.Helper()
	if err := f.broker.PublishString(f.topics.CardRead(), json, QoSExactlyOnce); err != nil {
		t.Fatalf("publish cardRead: %v", err)
	}
}

func waitFor(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published message")
		return ""
	}
}

func TestAccessGrantedForActiveCard(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	seedCard(t, f.store, u.ID, "aa:bb:cc:dd:00:00:00:00", "mine")

	f.sendCardRead(t, `{"cards":[{"cardID":"aa:bb:cc:dd:00:00:00:00"}],"id":777,"timestamp":0}`)

	if got := waitFor(t, f.door); got != "open 777" {
		t.Fatalf("doorControl = %q, want %q", got, "open 777")
	}

	// A CardRead (accepted) and an Entry should be logged.
	waitForCondition(t, func() bool {
		var reads, entries int64
		f.store.db.Model(&CardRead{}).Where("accepted = ?", true).Count(&reads)
		f.store.db.Model(&Entry{}).Count(&entries)
		return reads == 1 && entries == 1
	})
}

func TestAccessDeniedForUnknownCard(t *testing.T) {
	f := newAccessFixture(t)

	f.sendCardRead(t, `{"cards":[{"cardID":"ff:ff:ff:ff"}],"id":5,"timestamp":0}`)

	if got := waitFor(t, f.door); got != "deny 5" {
		t.Fatalf("doorControl = %q, want %q", got, "deny 5")
	}
	waitForCondition(t, func() bool {
		var reads, entries int64
		f.store.db.Model(&CardRead{}).Where("accepted = ?", false).Count(&reads)
		f.store.db.Model(&Entry{}).Count(&entries)
		return reads == 1 && entries == 0
	})
}

func TestAccessDeniedForInactiveUser(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	seedCard(t, f.store, u.ID, "aa:bb:cc:01", "")
	if err := f.store.SetUserActive(u.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	f.sendCardRead(t, `{"cards":[{"cardID":"aa:bb:cc:01"}],"id":9,"timestamp":0}`)
	if got := waitFor(t, f.door); got != "deny 9" {
		t.Fatalf("doorControl = %q, want %q", got, "deny 9")
	}
}

func TestAccessUIDFormattingIsSignificant(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	seedCard(t, f.store, u.ID, "aa:bb:cc:dd", "")

	// Uppercasing is allowed, but removing separators is no longer equivalent.
	f.sendCardRead(t, `{"cards":[{"cardID":"AABBCCDD"}],"id":1,"timestamp":0}`)
	if got := waitFor(t, f.door); got != "deny 1" {
		t.Fatalf("doorControl = %q, want %q", got, "deny 1")
	}
}

func TestAccessMultiCardAnyMatchOpens(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	seedCard(t, f.store, u.ID, "aa:bb:cc:02", "")

	// First card unknown, second is valid.
	f.sendCardRead(t, `{"cards":[{"cardID":"de:ad:be:ef"},{"cardID":"aa:bb:cc:02"}],"id":3,"timestamp":0}`)
	if got := waitFor(t, f.door); got != "open 3" {
		t.Fatalf("doorControl = %q, want %q", got, "open 3")
	}
	// Both scanned cards get a CardRead row.
	waitForCondition(t, func() bool {
		var reads int64
		f.store.db.Model(&CardRead{}).Count(&reads)
		return reads == 2
	})
}

func TestPairingHappyPath(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)

	f.access.StartPairing(u.ID)
	if got := waitFor(t, f.cmd); got != CmdAlternativeColorOn {
		t.Fatalf("command = %q, want %q", got, CmdAlternativeColorOn)
	}
	if st := f.access.PairingStatusFor(u.ID); st.State != PairingWaiting {
		t.Fatalf("state = %q, want waiting", st.State)
	}

	// A card read now assigns the card to the user.
	f.sendCardRead(t, `{"cards":[{"cardID":"aa:bb:cc:dd:ee:ff:00:00"}],"id":55,"timestamp":0}`)

	// Controller is told to ignore (not open/deny).
	if got := waitFor(t, f.door); got != "ignore 55" {
		t.Fatalf("doorControl = %q, want %q", got, "ignore 55")
	}
	// Color is turned back off.
	if got := waitFor(t, f.cmd); got != CmdAlternativeColorOff {
		t.Fatalf("command = %q, want %q", got, CmdAlternativeColorOff)
	}

	waitForCondition(t, func() bool {
		return f.access.PairingStatusFor(u.ID).State == PairingPaired
	})
	cards, _ := f.store.CardsByUser(u.ID)
	if len(cards) != 1 || cards[0].CardUID != "AA:BB:CC:DD:EE:FF:00:00" {
		t.Fatalf("card not stored in uppercase colon-hex form: %+v", cards)
	}
	if cards[0].Label != "" {
		t.Errorf("paired card should have empty label, got %q", cards[0].Label)
	}
}

func TestPairingBusyRejectsSecondUser(t *testing.T) {
	f := newAccessFixture(t)
	u1 := seedUser(t, f.store, "a@example.com", "pw", false)
	u2 := seedUser(t, f.store, "b@example.com", "pw", false)

	f.access.StartPairing(u1.ID)
	waitFor(t, f.cmd) // consume alternativeColorOn

	f.access.StartPairing(u2.ID)
	if st := f.access.PairingStatusFor(u2.ID); st.State != PairingBusy {
		t.Fatalf("second user state = %q, want busy", st.State)
	}
	// First user's session is unaffected.
	if st := f.access.PairingStatusFor(u1.ID); st.State != PairingWaiting {
		t.Fatalf("first user state = %q, want waiting", st.State)
	}
}

func TestPairingDuplicateCard(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	seedCard(t, f.store, u.ID, "aa:bb:cc:dd", "existing")

	f.access.StartPairing(u.ID)
	waitFor(t, f.cmd) // alternativeColorOn

	// Swipe the already-registered card with different casing, but the same
	// colon-separated formatting.
	f.sendCardRead(t, `{"cards":[{"cardID":"AA:BB:CC:DD"}],"id":8,"timestamp":0}`)
	if got := waitFor(t, f.door); got != "ignore 8" {
		t.Fatalf("doorControl = %q, want %q", got, "ignore 8")
	}
	waitFor(t, f.cmd) // alternativeColorOff

	waitForCondition(t, func() bool {
		return f.access.PairingStatusFor(u.ID).State == PairingDuplicate
	})
	// No second card created.
	if cards, _ := f.store.CardsByUser(u.ID); len(cards) != 1 {
		t.Errorf("duplicate pairing should not create a card, have %d", len(cards))
	}
}

func TestPairingTimeout(t *testing.T) {
	f := newAccessFixture(t)
	u := seedUser(t, f.store, "u@example.com", "pw", false)
	f.access.timeout = 50 * time.Millisecond

	f.access.StartPairing(u.ID)
	if got := waitFor(t, f.cmd); got != CmdAlternativeColorOn {
		t.Fatalf("command = %q", got)
	}
	// No card read; wait for the timeout to fire alternativeColorOff.
	if got := waitFor(t, f.cmd); got != CmdAlternativeColorOff {
		t.Fatalf("command = %q, want %q", got, CmdAlternativeColorOff)
	}
	waitForCondition(t, func() bool {
		return f.access.PairingStatusFor(u.ID).State == PairingTimeout
	})
}

// waitForCondition polls fn until it returns true or a short deadline passes.
func waitForCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
