package main

import (
	"errors"
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

type accountView struct {
	Email     string
	SSOIssuer string
}

func accountViewForUser(u *User) accountView {
	view := accountView{Email: u.Email}
	if u.OIDCIssuer != nil {
		view.SSOIssuer = *u.OIDCIssuer
	}
	return view
}

func (s *server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if currentPassword == "" || newPassword == "" || confirmPassword == "" {
		s.renderPasswordResult(w, "All password fields are required.", false)
		return
	}
	if !CheckPassword(u.PasswordHash, currentPassword) {
		s.renderPasswordResult(w, "Current password is incorrect.", false)
		return
	}
	if newPassword != confirmPassword {
		s.renderPasswordResult(w, "The new passwords do not match.", false)
		return
	}

	hash, err := HashPassword(newPassword)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		s.renderPasswordResult(w, "The new password is too long.", false)
		return
	}
	if err != nil {
		s.logger.Error("hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.store.SetUserPassword(u.ID, hash); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		s.logger.Error("change password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPasswordResult(w, "Password changed successfully.", true)
}

func (s *server) renderPasswordResult(w http.ResponseWriter, message string, success bool) {
	data := map[string]any{}
	if success {
		data["Success"] = message
	} else {
		data["Error"] = message
	}
	if err := s.tmpl.Fragment(w, "password-result", data); err != nil {
		s.logger.Error("render password result", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
