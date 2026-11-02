package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

var ErrInactiveUser = errors.New("user is inactive")

// ExternalAccessGrant is one time window in a versioned, externally-managed
// desired-state snapshot. ExternalRef is stable within its source and scope.
type ExternalAccessGrant struct {
	ExternalRef string    `json:"external_ref"`
	Email       string    `json:"email"`
	ValidFrom   time.Time `json:"valid_from"`
	ValidUntil  time.Time `json:"valid_until"`
}

type ExternalAccessGrantSetRequest struct {
	Source   string                `json:"source"`
	ScopeRef string                `json:"scope_ref"`
	Revision uint64                `json:"revision"`
	Grants   []ExternalAccessGrant `json:"grants"`
}

type ExternalAccessGrantSetResult struct {
	Source                  string   `json:"source"`
	ScopeRef                string   `json:"scope_ref"`
	Revision                uint64   `json:"revision"`
	Applied                 bool     `json:"applied"`
	RevisionAdvanced        bool     `json:"revision_advanced"`
	GrantCount              int      `json:"grant_count"`
	UsersWithAlwaysAccess   []string `json:"users_with_always_access,omitempty"`
}

type ExternalAccessGrantState struct {
	ExternalRef     string    `json:"external_ref"`
	Email           string    `json:"email"`
	ValidFrom       time.Time `json:"valid_from"`
	ValidUntil      time.Time `json:"valid_until"`
	PermanentAccess bool      `json:"permanent_access"`
}

type ExternalAccessGrantSetState struct {
	Source   string                      `json:"source"`
	ScopeRef string                      `json:"scope_ref"`
	Revision uint64                      `json:"revision"`
	Grants   []ExternalAccessGrantState `json:"grants"`
}

type AccessGrantView struct {
	ID           uint
	Email        string
	FromDisplay  string
	UntilDisplay string
	Status       string
	SourceLabel  string
	External     bool
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validAccessWindow(from, until time.Time) bool {
	return !from.IsZero() && !until.IsZero() && until.After(from)
}

// CreateManualAccessGrant adds a single time window from the admin interface.
func (s *Store) CreateManualAccessGrant(userID uint, from, until time.Time) error {
	if !validAccessWindow(from, until) {
		return ErrInvalidWindow
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.First(&user, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if !user.Active {
			return ErrInactiveUser
		}

		return tx.Create(&AccessGrant{
			UserID:     user.ID,
			ValidFrom:  from.UTC(),
			ValidUntil: until.UTC(),
		}).Error
	})
}

func (s *Store) SetUserPermanentAccess(userID uint, permanent bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.First(&user, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		return tx.Model(&user).Update("permanent_access", permanent).Error
	})
}

func (s *Store) HasActiveAccessGrant(userID uint, at time.Time) (bool, error) {
	var count int64
	at = at.UTC()
	err := s.db.Model(&AccessGrant{}).
		Where("user_id = ? AND revoked_at IS NULL AND valid_from <= ? AND valid_until > ?", userID, at, at).
		Count(&count).Error
	return count > 0, err
}

func (s *Store) RevokeAccessGrant(id uint, at time.Time) error {
	result := s.db.Model(&AccessGrant{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at.UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListVisibleAccessGrants(now time.Time) ([]AccessGrantView, error) {
	type row struct {
		ID             uint
		Email          string
		Active         bool
		ValidFrom      time.Time
		ValidUntil     time.Time
		ExternalSource *string
	}

	var rows []row
	now = now.UTC()
	err := s.db.Table("access_grants").
		Select("access_grants.id, users.email, users.active, access_grants.valid_from, access_grants.valid_until, access_grants.external_source").
		Joins("JOIN users ON users.id = access_grants.user_id").
		Where("access_grants.revoked_at IS NULL AND access_grants.valid_until > ?", now).
		Order("access_grants.valid_from ASC, users.email ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	views := make([]AccessGrantView, 0, len(rows))
	for _, item := range rows {
		status := "Starts later"
		if !item.Active {
			status = "Person inactive"
		} else if !now.Before(item.ValidFrom) {
			status = "Window active"
		}

		sourceLabel := "Added here"
		external := item.ExternalSource != nil && *item.ExternalSource != ""
		if external {
			sourceLabel = "Managed by another system"
		}
		views = append(views, AccessGrantView{
			ID:           item.ID,
			Email:        item.Email,
			FromDisplay:  item.ValidFrom.In(time.Local).Format("2006-01-02 15:04"),
			UntilDisplay: item.ValidUntil.In(time.Local).Format("2006-01-02 15:04"),
			Status:       status,
			SourceLabel:  sourceLabel,
			External:     external,
		})
	}
	return views, nil
}

func (s *Store) ReplaceExternalAccessGrantSet(req ExternalAccessGrantSetRequest, payloadHash string, now time.Time) (ExternalAccessGrantSetResult, error) {
	result := ExternalAccessGrantSetResult{
		Source:   req.Source,
		ScopeRef: req.ScopeRef,
		Revision: req.Revision,
		Applied:  true,
	}
	if req.Revision == 0 || req.Source == "" || req.ScopeRef == "" || payloadHash == "" {
		return result, ErrInvalidWindow
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current AccessGrantSync
		err := tx.Where("source = ? AND scope_ref = ?", req.Source, req.ScopeRef).First(&current).Error
		hasCurrent := err == nil
		sameRevision := false
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if hasCurrent {
			switch {
			case req.Revision < current.Revision:
				return ErrStaleRevision
			case req.Revision == current.Revision && payloadHash != current.PayloadHash:
				return ErrRevisionConflict
			case req.Revision == current.Revision:
				// Reconcile even on an exact retry so the source can repair local
				// drift while keeping the request idempotent.
				sameRevision = true
			}
		}

		usersByRef := make(map[string]User, len(req.Grants))
		alwaysAllowed := make(map[string]struct{})
		seenRefs := make(map[string]struct{}, len(req.Grants))
		for _, grant := range req.Grants {
			if grant.ExternalRef == "" || !validAccessWindow(grant.ValidFrom, grant.ValidUntil) {
				return ErrInvalidWindow
			}
			if _, exists := seenRefs[grant.ExternalRef]; exists {
				return ErrDuplicate
			}
			seenRefs[grant.ExternalRef] = struct{}{}

			user, err := userByEmail(tx, grant.Email)
			if err != nil {
				return err
			}
			if !user.Active {
				return ErrInactiveUser
			}
			usersByRef[grant.ExternalRef] = *user
			if user.PermanentAccess {
				alwaysAllowed[user.Email] = struct{}{}
			}
		}

		if err := tx.Model(&AccessGrant{}).
			Where("external_source = ? AND scope_ref = ? AND revoked_at IS NULL", req.Source, req.ScopeRef).
			Update("revoked_at", now).Error; err != nil {
			return err
		}

		for _, grant := range req.Grants {
			user := usersByRef[grant.ExternalRef]
			var existing AccessGrant
			err := tx.Where("external_source = ? AND scope_ref = ? AND external_ref = ?", req.Source, req.ScopeRef, grant.ExternalRef).
				First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				source, scope, externalRef := req.Source, req.ScopeRef, grant.ExternalRef
				if err := tx.Create(&AccessGrant{
					UserID:         user.ID,
					ValidFrom:      grant.ValidFrom.UTC(),
					ValidUntil:     grant.ValidUntil.UTC(),
					ExternalSource: &source,
					ScopeRef:       &scope,
					ExternalRef:    &externalRef,
				}).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&existing).Updates(map[string]any{
				"user_id":     user.ID,
				"valid_from":  grant.ValidFrom.UTC(),
				"valid_until": grant.ValidUntil.UTC(),
				"revoked_at":  nil,
				"updated_at":  now,
			}).Error; err != nil {
				return err
			}
		}

		if hasCurrent && !sameRevision {
			if err := tx.Model(&current).Updates(map[string]any{
				"revision":     req.Revision,
				"payload_hash": payloadHash,
				"updated_at":   now,
			}).Error; err != nil {
				return err
			}
		} else if !hasCurrent {
			if err := tx.Create(&AccessGrantSync{
			Source:      req.Source,
			ScopeRef:    req.ScopeRef,
			Revision:    req.Revision,
			PayloadHash: payloadHash,
			UpdatedAt:   now,
			}).Error; err != nil {
				return err
			}
		}

		result.GrantCount = len(req.Grants)
		result.RevisionAdvanced = !sameRevision
		result.UsersWithAlwaysAccess = sortedKeys(alwaysAllowed)
		return nil
	})
	return result, err
}

func (s *Store) ExternalAccessGrantSet(source, scopeRef string) (ExternalAccessGrantSetState, error) {
	var syncState AccessGrantSync
	if err := s.db.Where("source = ? AND scope_ref = ?", source, scopeRef).First(&syncState).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ExternalAccessGrantSetState{}, ErrNotFound
		}
		return ExternalAccessGrantSetState{}, err
	}

	var rows []struct {
		ExternalRef     string
		Email           string
		ValidFrom       time.Time
		ValidUntil      time.Time
		PermanentAccess bool
	}
	if err := s.db.Table("access_grants").
		Select("access_grants.external_ref, users.email, access_grants.valid_from, access_grants.valid_until, users.permanent_access").
		Joins("JOIN users ON users.id = access_grants.user_id").
		Where("access_grants.external_source = ? AND access_grants.scope_ref = ? AND access_grants.revoked_at IS NULL", source, scopeRef).
		Order("access_grants.external_ref ASC").
		Scan(&rows).Error; err != nil {
		return ExternalAccessGrantSetState{}, err
	}

	state := ExternalAccessGrantSetState{
		Source:   source,
		ScopeRef: scopeRef,
		Revision: syncState.Revision,
		Grants:   make([]ExternalAccessGrantState, 0, len(rows)),
	}
	for _, row := range rows {
		state.Grants = append(state.Grants, ExternalAccessGrantState{
			ExternalRef:     row.ExternalRef,
			Email:           row.Email,
			ValidFrom:       row.ValidFrom,
			ValidUntil:      row.ValidUntil,
			PermanentAccess: row.PermanentAccess,
		})
	}
	return state, nil
}

func userByEmail(db *gorm.DB, email string) (*User, error) {
	var users []User
	if err := db.Where("LOWER(email) = ?", normalizeEmail(email)).Limit(2).Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, ErrNotFound
	}
	if len(users) > 1 {
		return nil, ErrAmbiguousEmail
	}
	return &users[0], nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
