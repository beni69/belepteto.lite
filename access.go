package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// PairingState is the lifecycle of the single global pairing session.
type PairingState string

const (
	PairingIdle      PairingState = "idle"
	PairingWaiting   PairingState = "waiting"
	PairingPaired    PairingState = "paired"
	PairingTimeout   PairingState = "timeout"
	PairingDuplicate PairingState = "duplicate"
	PairingBusy      PairingState = "busy"
	PairingError     PairingState = "error"
)

// PairingStatus is what the frontend poll endpoint reads.
type PairingStatus struct {
	State   PairingState
	Message string
}

// AccessService is the stateless decision core for card reads plus the single
// global card-pairing session. It subscribes to the controller's cardRead topic
// and publishes doorControl / command responses.
type AccessService struct {
	store   *Store
	broker  *Broker
	topics  ControllerTopics
	logger  *slog.Logger
	timeout time.Duration

	mu      sync.Mutex
	pairing *pairingSession
	// lastResult holds the outcome of the most recent pairing attempt per user,
	// so the poll endpoint can report paired/timeout/duplicate once the session
	// itself is gone.
	lastResult map[uint]PairingStatus
	timer      *time.Timer
}

type pairingSession struct {
	userID uint
}

func NewAccessService(store *Store, broker *Broker, topics ControllerTopics, logger *slog.Logger) *AccessService {
	return &AccessService{
		store:      store,
		broker:     broker,
		topics:     topics,
		logger:     logger.With("component", "access"),
		timeout:    10 * time.Second,
		lastResult: make(map[uint]PairingStatus),
	}
}

// Start subscribes to the controller's cardRead topic.
func (a *AccessService) Start() error {
	return a.broker.Subscribe(a.topics.CardRead(), a.onCardRead)
}

// Close stops any running pairing timer.
func (a *AccessService) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}

// StartPairing begins a pairing session for userID. If a session is already
// active (for anyone), the request is rejected as busy.
func (a *AccessService) StartPairing(userID uint) {
	a.mu.Lock()
	if a.pairing != nil {
		a.mu.Unlock()
		a.setResult(userID, PairingStatus{State: PairingBusy, Message: "The reader is busy with another pairing. Try again shortly."})
		return
	}
	a.pairing = &pairingSession{userID: userID}
	a.lastResult[userID] = PairingStatus{State: PairingWaiting, Message: "Waiting for a card swipe…"}
	a.timer = time.AfterFunc(a.timeout, func() { a.onTimeout(userID) })
	a.mu.Unlock()

	if err := a.broker.PublishString(a.topics.Command(), CmdAlternativeColorOn, QoSAtLeastOnce); err != nil {
		a.logger.Error("publish alternativeColorOn", "err", err)
	}
	a.logger.Info("pairing started", "user", userID)
}

func (a *AccessService) onTimeout(userID uint) {
	a.mu.Lock()
	// Only act if this user's session is still the active one.
	if a.pairing == nil || a.pairing.userID != userID {
		a.mu.Unlock()
		return
	}
	a.pairing = nil
	a.timer = nil
	a.lastResult[userID] = PairingStatus{State: PairingTimeout, Message: "No card detected. Pairing timed out."}
	a.mu.Unlock()

	a.publishColorOff()
	a.logger.Info("pairing timed out", "user", userID)
}

// PairingStatusFor returns the current pairing status for a user.
func (a *AccessService) PairingStatusFor(userID uint) PairingStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.lastResult[userID]; ok {
		return st
	}
	return PairingStatus{State: PairingIdle}
}

func (a *AccessService) setResult(userID uint, st PairingStatus) {
	a.mu.Lock()
	a.lastResult[userID] = st
	a.mu.Unlock()
}

func (a *AccessService) publishColorOff() {
	if err := a.broker.PublishString(a.topics.Command(), CmdAlternativeColorOff, QoSAtLeastOnce); err != nil {
		a.logger.Error("publish alternativeColorOff", "err", err)
	}
}

// onCardRead is the inline subscription handler for cardRead messages.
func (a *AccessService) onCardRead(_ string, payload []byte) {
	received := time.Now().UTC()

	var msg CardReadMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		a.logger.Error("parse cardRead", "err", err)
		return
	}
	if len(msg.Cards) == 0 {
		a.logger.Warn("cardRead with no cards", "id", msg.ID)
		return
	}
	for i := range msg.Cards {
		// Preserve the reader's separators and only uppercase the UID for the
		// database lookup and log records.
		msg.Cards[i].CardID = strings.ToUpper(msg.Cards[i].CardID)
	}

	// Decide whether this read is a pairing attempt or a normal access attempt.
	a.mu.Lock()
	pairing := a.pairing
	a.mu.Unlock()

	if pairing != nil {
		a.handlePairingRead(pairing.userID, msg)
		return
	}
	a.handleAccessRead(msg, received)
}

// handleAccessRead runs the open/deny decision and logs the read.
func (a *AccessService) handleAccessRead(msg CardReadMsg, received time.Time) {
	anyMatch := false
	var matchedUser *User

	for _, sc := range msg.Cards {
		_, user, err := a.store.ActiveCardOwner(sc.CardID)
		var uid *uint
		accepted := false
		if err == nil {
			uid = &user.ID
			if user.PermanentAccess {
				accepted = true
			} else {
				accepted, err = a.store.HasActiveAccessGrant(user.ID, received)
				if err != nil {
					a.logger.Error("check scheduled access", "user", user.ID, "err", err)
					accepted = false
				}
			}
			if accepted {
				anyMatch = true
				matchedUser = user
			}
		} else if !errors.Is(err, ErrNotFound) {
			a.logger.Error("look up card owner", "err", err)
		}

		if logErr := a.store.CreateCardRead(&CardRead{
			CardUID:   sc.CardID,
			UserID:    uid,
			Accepted:  accepted,
			CreatedAt: received,
		}); logErr != nil {
			a.logger.Error("log cardRead", "err", logErr)
		}
	}

	if anyMatch {
		if err := a.broker.PublishString(a.topics.DoorControl(), DoorControlWithID(DoorOpen, msg.ID), QoSExactlyOnce); err != nil {
			a.logger.Error("publish open", "err", err)
		}
		// Record the entry (the door opening) for the matched user.
		if matchedUser != nil {
			if err := a.store.CreateEntry(&Entry{UserID: matchedUser.ID, CreatedAt: received}); err != nil {
				a.logger.Error("log entry", "err", err)
			}
		}
		a.logger.Info("access granted", "readID", msg.ID, "user", matchedUser.ID)
		return
	}

	if err := a.broker.PublishString(a.topics.DoorControl(), DoorControlWithID(DoorDeny, msg.ID), QoSExactlyOnce); err != nil {
		a.logger.Error("publish deny", "err", err)
	}
	a.logger.Info("access denied", "readID", msg.ID)
}

// handlePairingRead assigns the first scanned card to the pairing user.
func (a *AccessService) handlePairingRead(userID uint, msg CardReadMsg) {
	uid := msg.Cards[0].CardID

	// Tell the controller to do nothing with this read (no open, no deny, no
	// localDB lookup) — the user is assigning a card, not entering.
	defer func() {
		if err := a.broker.PublishString(a.topics.DoorControl(), DoorControlWithID(DoorIgnore, msg.ID), QoSExactlyOnce); err != nil {
			a.logger.Error("publish ignore", "err", err)
		}
		a.endPairing()
	}()

	exists, err := a.store.CardExistsByUID(uid)
	if err != nil {
		a.logger.Error("pairing duplicate check", "err", err)
		a.setResult(userID, PairingStatus{State: PairingError, Message: "Could not complete pairing. Try again."})
		return
	}
	if exists {
		a.setResult(userID, PairingStatus{State: PairingDuplicate, Message: "That card is already registered."})
		return
	}

	if err := a.store.CreateCard(&Card{UserID: userID, CardUID: uid, Active: true}); err != nil {
		a.logger.Error("pairing create card", "err", err)
		a.setResult(userID, PairingStatus{State: PairingError, Message: "Could not save the card. Try again."})
		return
	}
	a.setResult(userID, PairingStatus{State: PairingPaired, Message: "Card paired successfully."})
	a.logger.Info("card paired", "user", userID, "uid_prefix", cardUIDPrefix(uid))
}

// endPairing clears the active session and its timer, then turns the reader
// color back to normal.
func (a *AccessService) endPairing() {
	a.mu.Lock()
	a.pairing = nil
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.mu.Unlock()
	a.publishColorOff()
}
