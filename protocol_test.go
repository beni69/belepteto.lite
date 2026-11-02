package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestControllerTopics(t *testing.T) {
	c := NewControllerTopics("F19C")
	cases := map[string]string{
		c.CardRead():    "controller/F19C/cardRead",
		c.DoorControl(): "controller/F19C/doorControl",
		c.Command():     "controller/F19C/command",
		c.DBControl():   "controller/F19C/dbControl",
		c.Entries():     "controller/F19C/entries",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestDoorControlWithID(t *testing.T) {
	if got := DoorControlWithID(DoorOpen, 118514413); got != "open 118514413" {
		t.Errorf("got %q, want %q", got, "open 118514413")
	}
	if got := DoorControlWithID(DoorDeny, 42); got != "deny 42" {
		t.Errorf("got %q, want %q", got, "deny 42")
	}
}

func TestDBCommands(t *testing.T) {
	if got := DBAdd("aa:bb:cc:dd:00:00:00:00"); got != "add aa:bb:cc:dd:00:00:00:00" {
		t.Errorf("DBAdd got %q", got)
	}
	if got := DBDelete("aa:bb:cc:dd:00:00:00:00"); got != "delete aa:bb:cc:dd:00:00:00:00" {
		t.Errorf("DBDelete got %q", got)
	}
}

func TestEventTime(t *testing.T) {
	received := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	// Zero timestamp falls back to the receive time.
	if got := EventTime(0, received); !got.Equal(received) {
		t.Errorf("zero ts: got %v, want %v", got, received)
	}

	// Fractional seconds are preserved.
	got := EventTime(1755193918.547, received)
	want := time.Unix(1755193918, 547000000).UTC()
	// Allow sub-millisecond rounding from float parsing.
	if diff := got.Sub(want); diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("got %v, want ~%v (diff %v)", got, want, diff)
	}
}

func TestCardReadUnmarshal(t *testing.T) {
	raw := `{
		"cards": [
			{"cardID": "aa:bb:cc:dd:00:00:00:00", "atqa": 4, "sak": 8, "reader": [1,2]},
			{"cardID": "aa:bb:cc:dd:ff:ff:ff:ff", "atqa": 68, "sak": 32, "reader": [1]}
		],
		"id": 118514413,
		"timestamp": 1755193918.547
	}`
	var msg CardReadMsg
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.ID != 118514413 {
		t.Errorf("id = %d", msg.ID)
	}
	if len(msg.Cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(msg.Cards))
	}
	if msg.Cards[0].CardID != "aa:bb:cc:dd:00:00:00:00" {
		t.Errorf("card0 id = %q", msg.Cards[0].CardID)
	}
	if len(msg.Cards[0].Reader) != 2 {
		t.Errorf("card0 readers = %v", msg.Cards[0].Reader)
	}
}
