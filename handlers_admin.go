package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *server) handleAdminUserList(w http.ResponseWriter, r *http.Request) {
	s.renderUserList(w, "")
}

func (s *server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	admin := r.FormValue("admin") != ""

	if email == "" || password == "" {
		s.renderUserList(w, "Email and password are required.")
		return
	}

	hash, err := HashPassword(password)
	if err != nil {
		s.logger.Error("hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	err = s.store.CreateUser(&User{Email: email, PasswordHash: hash, Admin: admin, Active: true})
	if errors.Is(err, ErrDuplicate) {
		s.renderUserList(w, "A user with that email already exists.")
		return
	}
	if err != nil {
		s.logger.Error("create user", "err", err)
		s.renderUserList(w, "Could not create the user.")
		return
	}

	s.renderUserList(w, "")
}

func (s *server) handleAdminUserDeactivate(w http.ResponseWriter, r *http.Request) {
	s.setUserActive(w, r, false)
}

func (s *server) handleAdminUserActivate(w http.ResponseWriter, r *http.Request) {
	s.setUserActive(w, r, true)
}

// handleAdminUserPairStart puts the reader into pairing mode for the selected
// user. The next card read will be assigned to that user's account.
func (s *server) handleAdminUserPairStart(w http.ResponseWriter, r *http.Request) {
	user, ok := s.adminPairingUser(w, r)
	if !ok {
		return
	}

	s.access.StartPairing(user.ID)
	s.renderAdminPairStatus(w, user.ID)
}

// handleAdminUserPairStatus is polled while the selected user's pairing is in
// progress.
func (s *server) handleAdminUserPairStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.adminPairingUser(w, r)
	if !ok {
		return
	}

	s.renderAdminPairStatus(w, user.ID)
}

func (s *server) adminPairingUser(w http.ResponseWriter, r *http.Request) (*User, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return nil, false
	}

	user, err := s.store.UserByID(uint(id))
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "user not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		s.logger.Error("look up user for pairing", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return user, true
}

func (s *server) renderAdminPairStatus(w http.ResponseWriter, userID uint) {
	st := s.access.PairingStatusFor(userID)
	if err := s.tmpl.Fragment(w, "admin-pair-status", map[string]any{
		"UserID":  userID,
		"State":   string(st.State),
		"Message": st.Message,
	}); err != nil {
		s.logger.Error("render admin pair-status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) setUserActive(w http.ResponseWriter, r *http.Request, active bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}

	// Guard: an admin cannot deactivate themselves (would lock out the acting
	// session mid-request and is almost always a mistake).
	if !active {
		if me := userFromContext(r.Context()); me != nil && me.ID == uint(id) {
			http.Error(w, "you cannot deactivate your own account", http.StatusBadRequest)
			return
		}
	}

	if err := s.store.SetUserActive(uint(id), active); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		s.logger.Error("set user active", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Deactivation forces logout by removing the user's sessions.
	if !active {
		if err := s.store.DeleteUserSessions(uint(id)); err != nil {
			s.logger.Error("delete user sessions", "err", err)
		}
	}

	user, err := s.store.UserByID(uint(id))
	if err != nil {
		s.logger.Error("reload user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.tmpl.Fragment(w, "user-row", user); err != nil {
		s.logger.Error("render user-row", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

const logPageSize = 20

// parsePage returns a 1-based page number from the query string, defaulting to 1.
func parsePage(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// pagerData holds the fields the shared "pager" fragment needs.
func pagerData(baseURL, targetID string, page int, total int64, extraQuery string) map[string]any {
	totalPages := int((total + int64(logPageSize) - 1) / int64(logPageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	sep := ""
	if extraQuery != "" {
		sep = extraQuery + "&"
	}
	return map[string]any{
		"BaseURL":    baseURL,
		"TargetID":   targetID,
		"Page":       page,
		"TotalPages": totalPages,
		"HasPrev":    page > 1,
		"HasNext":    page < totalPages,
		"PrevQuery":  fmt.Sprintf("%spage=%d", sep, page-1),
		"NextQuery":  fmt.Sprintf("%spage=%d", sep, page+1),
	}
}

func (s *server) handleAdminEntryLog(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)
	rows, total, err := s.store.ListEntries(logPageSize, (page-1)*logPageSize)
	if err != nil {
		s.logger.Error("list entries", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := pagerData("/htmx/admin/logs/entries", "entry-log", page, total, "")
	data["Rows"] = rows
	if err := s.tmpl.Fragment(w, "entry-log", data); err != nil {
		s.logger.Error("render entry-log", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminCardReadLog(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)
	acceptedOnly := r.URL.Query().Get("accepted") == "1"

	rows, total, err := s.store.ListCardReads(acceptedOnly, logPageSize, (page-1)*logPageSize)
	if err != nil {
		s.logger.Error("list card reads", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	extra := ""
	if acceptedOnly {
		extra = "accepted=1"
	}
	data := pagerData("/htmx/admin/logs/cardreads", "cardread-log", page, total, extra)
	data["Rows"] = rows
	data["AcceptedOnly"] = acceptedOnly
	if err := s.tmpl.Fragment(w, "cardread-log", data); err != nil {
		s.logger.Error("render cardread-log", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// renderUserList renders the whole user list fragment, optionally with an error
// message shown above the list.
func (s *server) renderUserList(w http.ResponseWriter, errMsg string) {
	users, err := s.store.ListUsers()
	if err != nil {
		s.logger.Error("list users", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Users": users}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if err := s.tmpl.Fragment(w, "user-list", data); err != nil {
		s.logger.Error("render user-list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
