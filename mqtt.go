package main

import (
	"fmt"
	"log/slog"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Broker wraps an embedded Mochi-MQTT server and exposes a small
// Publish/Subscribe API backed by the inline client. TLS and real
// authentication (client certs, ACLs) plug into the listener config and auth
// hook later; for now it accepts all connections.
type Broker struct {
	server *mqtt.Server
	addr   string
	subID  int
}

// MessageHandler receives the topic and raw payload of an inbound message.
type MessageHandler func(topic string, payload []byte)

func NewBroker(logger *slog.Logger, addr string) (*Broker, error) {
	server := mqtt.New(&mqtt.Options{
		InlineClient: true,
		Logger:       logger.With("component", "mqtt"),
	})

	// Allow-all auth for now. Replaced by cert-based auth + ACL hooks later.
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		return nil, fmt.Errorf("add auth hook: %w", err)
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:      "tcp",
		Address: addr,
	})
	if err := server.AddListener(tcp); err != nil {
		return nil, fmt.Errorf("add tcp listener: %w", err)
	}

	return &Broker{server: server, addr: addr}, nil
}

// Start begins serving in a background goroutine. Serve() itself returns once
// listeners are established, so this does not block.
func (b *Broker) Start() error {
	return b.server.Serve()
}

func (b *Broker) Close() error {
	return b.server.Close()
}

// Publish sends a message to a topic via the inline client.
func (b *Broker) Publish(topic string, payload []byte, qos byte) error {
	return b.server.Publish(topic, payload, false, qos)
}

// PublishString is a convenience for the plaintext commands the controller
// expects (doorControl, command, dbControl).
func (b *Broker) PublishString(topic, payload string, qos byte) error {
	return b.Publish(topic, []byte(payload), qos)
}

// Subscribe registers an inline subscription. Each call gets a unique
// subscription id so multiple subscriptions coexist.
func (b *Broker) Subscribe(filter string, handler MessageHandler) error {
	b.subID++
	return b.server.Subscribe(filter, b.subID, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		handler(pk.TopicName, pk.Payload)
	})
}
