package main

import (
	"errors"
	"net/http"
)

func (s *server) handleDoorOpenControl(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := userFromContext(r.Context())
	canOpen, err := s.access.CanOpenDoor(user.ID)
	if err != nil {
		s.logger.Error("check door access for control", "user", user.ID, "err", err)
		canOpen = false
	}
	if err := s.tmpl.Fragment(w, "door-open-control", map[string]any{"CanOpenDoor": canOpen}); err != nil {
		s.logger.Error("render door-open control", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) handleDoorOpen(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if err := s.access.OpenDoorForUser(user.ID); err != nil {
		if errors.Is(err, ErrDoorAccessDenied) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok":      false,
				"code":    "access_denied",
				"message": "Your door access is no longer active. No open request was sent.",
			})
			return
		}
		s.logger.Error("publish remote door open", "user", user.ID, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"code":    "open_failed",
			"message": "Could not send the open request. Please try again.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Open request sent.",
	})
}
