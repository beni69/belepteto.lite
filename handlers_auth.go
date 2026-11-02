package main

import (
	"errors"
	"net/http"
	"strings"
)

func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Already logged in? Skip the form.
	if s.currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := map[string]any{"SSOEnabled": s.sso != nil}
	if s.sso != nil {
		data["SSOButtonText"] = s.sso.buttonText
	}
	if r.URL.Query().Get("error") == "sso" {
		data["Error"] = "SSO sign-in failed. Please try again or use your password."
	}
	s.render(w, "login", data)
}

func (s *server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	u, err := s.store.UserByEmail(email)
	if err != nil || !u.Active || !CheckPassword(u.PasswordHash, password) {
		if err != nil && !errors.Is(err, ErrNotFound) {
			s.logger.Error("login lookup", "err", err)
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{
			"Error":         "Invalid email or password.",
			"SSOEnabled":    s.sso != nil,
			"SSOButtonText": ssoButtonText(s.sso),
		})
		return
	}

	if err := s.startSession(w, r, u.ID); err != nil {
		s.logger.Error("start session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
