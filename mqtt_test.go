package main

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func newTestBroker(t *testing.T) *Broker {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// :0 lets the OS pick a free port so parallel test runs don't collide.
	b, err := NewBroker(logger, ":0")
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if err := b.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestBrokerInlinePublishSubscribe(t *testing.T) {
	b := newTestBroker(t)

	got := make(chan string, 1)
	if err := b.Subscribe("controller/1619/cardRead", func(topic string, payload []byte) {
		got <- topic + "|" + string(payload)
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.PublishString("controller/1619/cardRead", `{"id":1}`, QoSExactlyOnce); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case msg := <-got:
		want := `controller/1619/cardRead|{"id":1}`
		if msg != want {
			t.Fatalf("got %q, want %q", msg, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not receive the message")
	}
}

func TestBrokerMultipleSubscriptions(t *testing.T) {
	b := newTestBroker(t)

	a := make(chan []byte, 1)
	c := make(chan []byte, 1)
	if err := b.Subscribe("controller/1619/heartbeat", func(_ string, p []byte) { a <- p }); err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	if err := b.Subscribe("controller/1619/syslog", func(_ string, p []byte) { c <- p }); err != nil {
		t.Fatalf("subscribe c: %v", err)
	}

	_ = b.PublishString("controller/1619/heartbeat", "hb", QoSAtMostOnce)
	_ = b.PublishString("controller/1619/syslog", "log", QoSExactlyOnce)

	for i := 0; i < 2; i++ {
		select {
		case p := <-a:
			if string(p) != "hb" {
				t.Errorf("heartbeat payload = %q", p)
			}
		case p := <-c:
			if string(p) != "log" {
				t.Errorf("syslog payload = %q", p)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for messages")
		}
	}
}
