package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

type adminUserView struct {
	ID              uint
	Email           string
	Admin           bool
	Active          bool
	PermanentAccess bool
	CardsExpanded   bool
	Cards           []adminCardView
}

type adminCardView struct {
	ID            uint
	CardUIDPrefix string
	Label         string
}

func adminUserViewFor(user User) adminUserView {
	view := adminUserView{
		ID:              user.ID,
		Email:           user.Email,
		Admin:           user.Admin,
		Active:          user.Active,
		PermanentAccess: user.PermanentAccess,
		Cards:           make([]adminCardView, 0, len(user.Cards)),
	}
	for _, card := range user.Cards {
		view.Cards = append(view.Cards, adminCardView{
			ID:            card.ID,
			CardUIDPrefix: card.CardUIDPrefix,
			Label:         card.Label,
		})
	}
	return view
}

func (s *server) handleAdminUserList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.renderUserList(w, "")
}

func (s *server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	admin := r.FormValue("admin") != ""
	permanentAccess := r.FormValue("permanent_access") == "on"

	if email == "" || password == "" {
		s.renderUserCreateResult(w, "", "Email and password are required.")
		return
	}

	hash, err := HashPassword(password)
	if err != nil {
		s.logger.Error("hash password", "err", err)
		s.renderUserCreateResult(w, "", "Could not prepare the user's password.")
		return
	}

	err = s.store.CreateUserWithAccess(&User{
		Email:        email,
		PasswordHash: hash,
		Admin:        admin,
		Active:       true,
	}, permanentAccess)
	if errors.Is(err, ErrDuplicate) {
		s.renderUserCreateResult(w, "", "A user with that email already exists.")
		return
	}
	if err != nil {
		s.logger.Error("create user", "err", err)
		s.renderUserCreateResult(w, "", "Could not create the user.")
		return
	}

	w.Header().Set("HX-Trigger", "user-created")
	s.renderUserCreateResult(w, "User registered: "+email, "")
}

func (s *server) renderUserCreateResult(w http.ResponseWriter, success, errorMessage string) {
	if err := s.tmpl.Fragment(w, "user-create-result", map[string]any{
		"Success": success,
		"Error":   errorMessage,
	}); err != nil {
		s.logger.Error("render user creation result", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminAccessGrantUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	users, err := s.store.ListUsers()
	if err != nil {
		s.logger.Error("list people for access-grant selector", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.tmpl.Fragment(w, "admin-access-grant-users", map[string]any{"Users": users}); err != nil {
		s.logger.Error("render access-grant user selector", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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

func (s *server) adminUserView(userID uint) (adminUserView, error) {
	user, err := s.store.UserByID(userID)
	if err != nil {
		return adminUserView{}, err
	}
	cards, err := s.store.CardsByUser(userID)
	if err != nil {
		return adminUserView{}, err
	}
	user.Cards = cards
	return adminUserViewFor(*user), nil
}

func (s *server) renderAdminUserCards(w http.ResponseWriter, userID uint) {
	view, err := s.adminUserView(userID)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("load admin user cards", "user", userID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	view.CardsExpanded = true
	if err := s.tmpl.Fragment(w, "admin-user-cards", view); err != nil {
		s.logger.Error("render admin user cards", "user", userID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminUserCards(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	userID, err := strconv.ParseUint(chi.URLParam(r, "userID"), 10, 64)
	if err != nil || userID == 0 {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	s.renderAdminUserCards(w, uint(userID))
}

func (s *server) adminCardRouteIDs(w http.ResponseWriter, r *http.Request) (uint, uint, bool) {
	userID, err := strconv.ParseUint(chi.URLParam(r, "userID"), 10, 64)
	if err != nil || userID == 0 {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return 0, 0, false
	}
	cardID, err := strconv.ParseUint(chi.URLParam(r, "cardID"), 10, 64)
	if err != nil || cardID == 0 {
		http.Error(w, "bad card id", http.StatusBadRequest)
		return 0, 0, false
	}
	return uint(userID), uint(cardID), true
}

func (s *server) handleAdminCardRelabel(w http.ResponseWriter, r *http.Request) {
	userID, cardID, ok := s.adminCardRouteIDs(w, r)
	if !ok {
		return
	}
	if err := s.store.RelabelUserCard(userID, cardID, strings.TrimSpace(r.FormValue("label"))); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "card not found", http.StatusNotFound)
			return
		}
		s.logger.Error("admin relabel card", "user", userID, "card", cardID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderAdminUserCards(w, userID)
}

func (s *server) handleAdminCardDelete(w http.ResponseWriter, r *http.Request) {
	userID, cardID, ok := s.adminCardRouteIDs(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUserCard(userID, cardID); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "card not found", http.StatusNotFound)
			return
		}
		s.logger.Error("admin unpair card", "user", userID, "card", cardID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderAdminUserCards(w, userID)
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

	w.Header().Set("HX-Trigger", "user-updated")
	s.renderAdminUserRow(w, uint(id))
}

func (s *server) renderAdminUserRow(w http.ResponseWriter, userID uint) {
	view, err := s.adminUserView(userID)
	if err != nil {
		s.logger.Error("reload user row", "user", userID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.tmpl.Fragment(w, "user-row", view); err != nil {
		s.logger.Error("render user-row", "user", userID, "err", err)
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
	users, err := s.store.ListUsersWithCards()
	if err != nil {
		s.logger.Error("list users", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]adminUserView, 0, len(users))
	for _, user := range users {
		views = append(views, adminUserViewFor(user))
	}
	data := map[string]any{"Users": views}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if err := s.tmpl.Fragment(w, "user-list", data); err != nil {
		s.logger.Error("render user-list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
