package main

import (
	"errors"
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

// renderUserList renders the whole user list fragment, optionally with an error
// message shown above the table.
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
