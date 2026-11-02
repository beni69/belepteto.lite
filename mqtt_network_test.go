package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// freePort asks the OS for an available TCP port and returns it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestBrokerAcceptsNetworkClient proves the embedded broker accepts a real MQTT
// client over TCP and routes its publish to an inline subscription — the exact
// path the cardreader will use.
func TestBrokerAcceptsNetworkClient(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b, err := NewBroker(logger, addr)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if err := b.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Close()

	got := make(chan []byte, 1)
	if err := b.Subscribe("controller/1619/cardRead", func(_ string, p []byte) {
		got <- p
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	opts := paho.NewClientOptions().
		AddBroker("tcp://" + addr).
		SetClientID("test-cardreader").
		SetConnectTimeout(3 * time.Second)
	client := paho.NewClient(opts)
	if tok := client.Connect(); tok.WaitTimeout(3*time.Second) && tok.Error() != nil {
		t.Fatalf("connect: %v", tok.Error())
	}
	defer client.Disconnect(250)

	payload := `{"cards":[{"cardID":"aa:bb:cc:dd:00:00:00:00"}],"id":42,"timestamp":0}`
	tok := client.Publish("controller/1619/cardRead", QoSExactlyOnce, false, payload)
	tok.WaitTimeout(3 * time.Second)
	if tok.Error() != nil {
		t.Fatalf("publish: %v", tok.Error())
	}

	select {
	case p := <-got:
		if string(p) != payload {
			t.Fatalf("payload mismatch: got %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inline subscription did not receive the network publish")
	}
}
