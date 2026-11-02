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

func (s *server) handleCardCreate(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())

	uid := normalizeCardUID(r.FormValue("card_uid"))
	label := strings.TrimSpace(r.FormValue("label"))

	if uid == "" {
		s.renderCardList(w, u.ID, "Card UID is required.")
		return
	}

	err := s.store.CreateCard(&Card{UserID: u.ID, CardUID: uid, Label: label, Active: true})
	if errors.Is(err, ErrDuplicate) {
		s.renderCardList(w, u.ID, "That card is already registered.")
		return
	}
	if err != nil {
		s.logger.Error("create card", "err", err)
		s.renderCardList(w, u.ID, "Could not add the card.")
		return
	}

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

func (s *server) renderCardList(w http.ResponseWriter, userID uint, errMsg string) {
	cards, err := s.store.CardsByUser(userID)
	if err != nil {
		s.logger.Error("list cards", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Cards": cards}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if err := s.tmpl.Fragment(w, "card-list", data); err != nil {
		s.logger.Error("render card-list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// normalizeCardUID trims whitespace and upper-cases the UID so the same physical
// card can't be registered twice under different casing.
func normalizeCardUID(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}
