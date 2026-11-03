package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *server) handleCardList(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	s.renderCardList(w, u.ID, "")
}

func (s *server) handleCardDelete(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad card id", http.StatusBadRequest)
		return
	}

	err = s.store.DeleteUserCard(u.ID, uint(id))
	if errors.Is(err, ErrNotFound) {
		// Either it never existed or it belongs to someone else. Treat both as
		// not-found so we don't leak whether another user's card exists.
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("delete card", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderCardList(w, u.ID, "")
}

func (s *server) handleCardRelabel(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad card id", http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(r.FormValue("label"))
	err = s.store.RelabelUserCard(u.ID, uint(id), label)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("relabel card", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderCardList(w, u.ID, "")
}

type cardListView struct {
	ID            uint
	CardUIDPrefix string
	Label         string
}

func (s *server) renderCardList(w http.ResponseWriter, userID uint, errMsg string) {
	cards, err := s.store.CardsByUser(userID)
	if err != nil {
		s.logger.Error("list cards", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]cardListView, 0, len(cards))
	for _, card := range cards {
		views = append(views, cardListView{
			ID:            card.ID,
			CardUIDPrefix: card.CardUIDPrefix,
			Label:         card.Label,
		})
	}
	data := map[string]any{"Cards": views}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if err := s.tmpl.Fragment(w, "card-list", data); err != nil {
		s.logger.Error("render card-list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleCardPairStart puts the reader into pairing mode for the current user.
// The next card read will be assigned to them rather than opening the door.
func (s *server) handleCardPairStart(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	s.access.StartPairing(u.ID)
	s.renderPairStatus(w, u.ID)
}

// handleCardPairStatus is polled by the browser while pairing is in progress.
// It returns the current status fragment; once resolved, the fragment stops
// polling and reloads the card list.
func (s *server) handleCardPairStatus(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	s.renderPairStatus(w, u.ID)
}

func (s *server) renderPairStatus(w http.ResponseWriter, userID uint) {
	st := s.access.PairingStatusFor(userID)
	if err := s.tmpl.Fragment(w, "pair-status", map[string]any{
		"State":   string(st.State),
		"Message": st.Message,
	}); err != nil {
		s.logger.Error("render pair-status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
