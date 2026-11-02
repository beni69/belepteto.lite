package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Controller topic layout. All controller communication lives under
// controller/{id}/..., per the hardware spec. Healthcheck is the one exception
// and uses a fixed backend topic.
type ControllerTopics struct {
	id string
}

func NewControllerTopics(id string) ControllerTopics {
	return ControllerTopics{id: id}
}

func (c ControllerTopics) base() string         { return "controller/" + c.id }
func (c ControllerTopics) CardRead() string      { return c.base() + "/cardRead" }
func (c ControllerTopics) DoorControl() string   { return c.base() + "/doorControl" }
func (c ControllerTopics) Command() string       { return c.base() + "/command" }
func (c ControllerTopics) DBControl() string     { return c.base() + "/dbControl" }
func (c ControllerTopics) DBPush() string        { return c.base() + "/dbPush" }
func (c ControllerTopics) Entries() string       { return c.base() + "/entries" }
func (c ControllerTopics) Heartbeat() string     { return c.base() + "/heartbeat" }
func (c ControllerTopics) Syslog() string        { return c.base() + "/syslog" }
func (c ControllerTopics) DoorState() string     { return c.base() + "/doorState" }

// BackendHealthcheck is a fixed topic, not under controller/{id}.
const BackendHealthcheck = "backend/healthcheck"

// Recommended QoS levels from the spec.
const (
	QoSAtMostOnce  byte = 0
	QoSAtLeastOnce byte = 1
	QoSExactlyOnce byte = 2
)

// --- Inbound payloads (controller -> server) ---

// CardReadMsg is published when the reader scans one or more cards. The ID is a
// random 64-bit number echoed back in the doorControl response.
type CardReadMsg struct {
	Cards     []ScannedCard `json:"cards"`
	ID        uint64        `json:"id"`
	Timestamp float64       `json:"timestamp"`
}

type ScannedCard struct {
	CardID string `json:"cardID"`
	ATQA   int    `json:"atqa"`
	SAK    int    `json:"sak"`
	Reader []int  `json:"reader"`
}

// EntryMsg reports offline/timeout/manual openings the controller handled on its
// own, sent once the server is reachable again.
type EntryMsg struct {
	Type      string  `json:"type"` // offline/timeout/manual
	Card      string  `json:"card"`
	Accepted  bool    `json:"accepted"`
	Timestamp float64 `json:"timestamp"`
}

// SyslogMsg is a log line from a controller subsystem.
type SyslogMsg struct {
	Channel   string  `json:"channel"`
	Level     string  `json:"level"`
	Message   string  `json:"message"`
	Timestamp float64 `json:"timestamp"`
}

// DoorStateMsg reports door open/closed transitions.
type DoorStateMsg struct {
	Open      bool    `json:"open"`
	Expected  bool    `json:"expected"`
	Timestamp float64 `json:"timestamp"`
}

// --- Outbound commands (server -> controller) ---
//
// doorControl and command are PLAINTEXT, not JSON.

// DoorControl commands.
const (
	DoorOpen   = "open"
	DoorDeny   = "deny"
	DoorIgnore = "ignore"
	DoorCustom1 = "custom1"
	DoorCustom2 = "custom2"
)

// DoorControlWithID builds a plaintext doorControl command carrying the cardRead
// ID, e.g. "open 118514413". Using the ID prevents double open/deny on network
// delays. cmd should be one of DoorOpen/DoorDeny/DoorCustom1/DoorCustom2.
func DoorControlWithID(cmd string, id uint64) string {
	return fmt.Sprintf("%s %d", cmd, id)
}

// Controller commands (the command topic).
const (
	CmdAlternativeColorOn  = "alternativeColorOn"
	CmdAlternativeColorOff = "alternativeColorOff"
	CmdReboot              = "reboot"
	CmdMaintenanceModeOn   = "maintenanceModeOn"
	CmdMaintenanceModeOff  = "maintenanceModeOff"
)

// dbControl commands.
func DBAdd(cardID string) string    { return "add " + cardID }
func DBDelete(cardID string) string { return "delete " + cardID }

const DBGet = "get"

// Healthcheck payloads.
const (
	HealthcheckUp   = "true"
	HealthcheckDown = "false"
)

// CanonicalCardUID produces the lenient match key for a card UID: colons and
// whitespace removed, upper-cased. The reader sends colon-hex like
// "aa:bb:cc:dd:00:00:00:00"; users may store the same card in any casing, so
// matching compares canonical forms.
func CanonicalCardUID(uid string) string {
	var b strings.Builder
	for _, r := range uid {
		if r == ':' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// EventTime converts a spec timestamp to a time.Time. Per the spec, a timestamp
// of 0 means "use the time the message was received", so callers pass the
// receive time as the fallback. Timestamps are UTC seconds, possibly fractional.
func EventTime(ts float64, received time.Time) time.Time {
	if ts == 0 {
		return received
	}
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}
