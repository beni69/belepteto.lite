package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const maxExternalSnapshotBytes = 2 << 20
const maxExternalSnapshotGrants = 2000

func (s *server) handleAdminUserScheduledAccess(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserPermanentAccess(w, r, false)
}

func (s *server) handleAdminUserPermanentAccess(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserPermanentAccess(w, r, true)
}

func (s *server) setAdminUserPermanentAccess(w http.ResponseWriter, r *http.Request, permanent bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	if err := s.store.SetUserPermanentAccess(uint(id), permanent); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		s.logger.Error("set user permanent access", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	user, err := s.store.UserByID(uint(id))
	if err != nil {
		s.logger.Error("reload user after access setting", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.tmpl.Fragment(w, "user-row", user); err != nil {
		s.logger.Error("render user row after access setting", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminAccessGrantList(w http.ResponseWriter, r *http.Request) {
	s.renderAccessGrantList(w, "")
}

func (s *server) handleAdminAccessGrantCreate(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseUint(r.FormValue("user_id"), 10, 64)
	if err != nil || userID == 0 {
		s.renderAccessGrantList(w, "Choose a person first.")
		return
	}

	from, err := parseLocalDateTime(r.FormValue("valid_from"))
	if err != nil {
		s.renderAccessGrantList(w, "Enter a valid start time.")
		return
	}
	until, err := parseLocalDateTime(r.FormValue("valid_until"))
	if err != nil || !until.After(from) {
		s.renderAccessGrantList(w, "The end time must be later than the start time.")
		return
	}

	if err := s.store.CreateManualAccessGrant(uint(userID), from, until); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			s.renderAccessGrantList(w, "That person could not be found.")
		case errors.Is(err, ErrInactiveUser):
			s.renderAccessGrantList(w, "Activate this person before adding an entry time.")
		case errors.Is(err, ErrInvalidWindow):
			s.renderAccessGrantList(w, "The end time must be later than the start time.")
		default:
			s.logger.Error("create manual access grant", "err", err)
			s.renderAccessGrantList(w, "Could not save the entry time.")
		}
		return
	}
	s.renderAccessGrantList(w, "")
}

func parseLocalDateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, ErrInvalidWindow
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04", value, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func (s *server) handleAdminAccessGrantRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "bad access entry id", http.StatusBadRequest)
		return
	}
	if err := s.store.RevokeAccessGrant(uint(id), time.Now().UTC()); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.renderAccessGrantList(w, "That entry could not be found or was already removed.")
			return
		}
		s.logger.Error("revoke access grant", "err", err)
		s.renderAccessGrantList(w, "Could not remove the entry time.")
		return
	}
	s.renderAccessGrantList(w, "")
}

func (s *server) renderAccessGrantList(w http.ResponseWriter, errMsg string) {
	grants, err := s.store.ListVisibleAccessGrants(time.Now().UTC())
	if err != nil {
		s.logger.Error("list access grants", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Grants": grants}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	if err := s.tmpl.Fragment(w, "access-grant-list", data); err != nil {
		s.logger.Error("render access grant list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// RequireExternalAPIToken protects machine-to-machine endpoints. An empty
// token deliberately disables the API instead of accepting unauthenticated
// requests.
func (s *server) RequireExternalAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.externalAPIToken == "" {
			http.Error(w, "external API is not configured", http.StatusServiceUnavailable)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimSpace(header[len(prefix):])
		if len(provided) != len(s.externalAPIToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.externalAPIToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleExternalAccessGrantSetPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source   string                  `json:"source"`
		ScopeRef string                  `json:"scope_ref"`
		Revision uint64                  `json:"revision"`
		Grants   *[]ExternalAccessGrant `json:"grants"`
	}
	if err := decodeJSONBody(w, r, &body, maxExternalSnapshotBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "snapshot is too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.Grants == nil {
		writeAPIError(w, http.StatusBadRequest, "missing_grants", "grants must be provided; use an empty array to remove all entries in a scope")
		return
	}
	req := ExternalAccessGrantSetRequest{
		Source:   body.Source,
		ScopeRef: body.ScopeRef,
		Revision: body.Revision,
		Grants:   *body.Grants,
	}
	if err := normalizeAndValidateExternalSet(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_snapshot", err.Error())
		return
	}

	hash, err := hashExternalSnapshot(req)
	if err != nil {
		s.logger.Error("hash external grant snapshot", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not process snapshot")
		return
	}
	result, err := s.store.ReplaceExternalAccessGrantSet(req, hash, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeAPIError(w, http.StatusUnprocessableEntity, "unknown_user", "all email addresses must belong to existing users")
		case errors.Is(err, ErrAmbiguousEmail):
			writeAPIError(w, http.StatusConflict, "ambiguous_email", "an email address matches more than one user")
		case errors.Is(err, ErrInactiveUser):
			writeAPIError(w, http.StatusConflict, "inactive_user", "activate all users before syncing this access list")
		case errors.Is(err, ErrDuplicate):
			writeAPIError(w, http.StatusBadRequest, "duplicate_external_ref", "external_ref values must be unique within the snapshot")
		case errors.Is(err, ErrInvalidWindow):
			writeAPIError(w, http.StatusBadRequest, "invalid_time_window", "each end time must be later than its start time")
		case errors.Is(err, ErrStaleRevision):
			writeAPIError(w, http.StatusConflict, "stale_revision", "a newer snapshot has already been applied")
		case errors.Is(err, ErrRevisionConflict):
			writeAPIError(w, http.StatusConflict, "revision_conflict", "this revision was already used for different data")
		default:
			s.logger.Error("replace external access grant set", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not apply snapshot")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) handleExternalAccessGrantSetGet(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	scopeRef := strings.TrimSpace(r.URL.Query().Get("scope_ref"))
	if source == "" || scopeRef == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_scope", "source and scope_ref are required")
		return
	}
	state, err := s.store.ExternalAccessGrantSet(source, scopeRef)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "not_found", "access list not found")
			return
		}
		s.logger.Error("get external access grant set", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not read access list")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func normalizeAndValidateExternalSet(req *ExternalAccessGrantSetRequest) error {
	req.Source = strings.TrimSpace(req.Source)
	req.ScopeRef = strings.TrimSpace(req.ScopeRef)
	if req.Source == "" || len(req.Source) > 128 {
		return errors.New("source is required and must be at most 128 characters")
	}
	if req.ScopeRef == "" || len(req.ScopeRef) > 256 {
		return errors.New("scope_ref is required and must be at most 256 characters")
	}
	if req.Revision == 0 {
		return errors.New("revision must be greater than zero")
	}
	if len(req.Grants) > maxExternalSnapshotGrants {
		return fmt.Errorf("snapshot cannot contain more than %d grants", maxExternalSnapshotGrants)
	}

	seen := make(map[string]struct{}, len(req.Grants))
	for i := range req.Grants {
		grant := &req.Grants[i]
		grant.ExternalRef = strings.TrimSpace(grant.ExternalRef)
		if grant.ExternalRef == "" || len(grant.ExternalRef) > 256 {
			return fmt.Errorf("grants[%d].external_ref is required and must be at most 256 characters", i)
		}
		if _, exists := seen[grant.ExternalRef]; exists {
			return ErrDuplicate
		}
		seen[grant.ExternalRef] = struct{}{}

		grant.Email = normalizeEmail(grant.Email)
		if len(grant.Email) > 254 {
			return fmt.Errorf("grants[%d].email must be at most 254 characters", i)
		}
		parsed, err := mail.ParseAddress(grant.Email)
		if err != nil || parsed.Address != grant.Email || grant.Email == "" {
			return fmt.Errorf("grants[%d].email must be a valid email address", i)
		}
		if !validAccessWindow(grant.ValidFrom, grant.ValidUntil) {
			return fmt.Errorf("grants[%d] must have an end time later than its start time", i)
		}
		grant.ValidFrom = grant.ValidFrom.UTC()
		grant.ValidUntil = grant.ValidUntil.UTC()
	}

	// Stable ordering makes retries with the same revision idempotent even if the
	// sending system serializes the list in a different order.
	sort.Slice(req.Grants, func(i, j int) bool {
		return req.Grants[i].ExternalRef < req.Grants[j].ExternalRef
	})
	return nil
}

func hashExternalSnapshot(req ExternalAccessGrantSetRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
