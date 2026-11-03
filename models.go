package main

import "time"

type User struct {
	ID              uint    `gorm:"primaryKey"`
	Email           string  `gorm:"uniqueIndex;not null"`
	PasswordHash    string  `gorm:"not null"`
	OIDCIssuer      *string `gorm:"column:oidc_issuer;uniqueIndex:idx_users_oidc_identity,priority:1"`
	OIDCSubject     *string `gorm:"column:oidc_subject;uniqueIndex:idx_users_oidc_identity,priority:2"`
	Admin           bool    `gorm:"not null;default:false"`
	Active          bool    `gorm:"not null;default:true"`
	PermanentAccess bool    `gorm:"not null;default:true"`
	CreatedAt       time.Time
	Cards           []Card        `gorm:"constraint:OnDelete:CASCADE"`
	Sessions        []Session     `gorm:"constraint:OnDelete:CASCADE"`
	AccessGrants    []AccessGrant `gorm:"constraint:OnDelete:CASCADE"`
}

type Session struct {
	ID        string `gorm:"primaryKey"`
	UserID    uint   `gorm:"index;not null"`
	ExpiresAt time.Time
	CreatedAt time.Time
}

type Card struct {
	ID uint `gorm:"primaryKey"`
	// CardUID stores the SHA-256 hash of the normalized reader UID. The raw UID
	// is never persisted in this field.
	CardUID string `gorm:"uniqueIndex;not null"`
	// CardUIDPrefix contains the first two characters of the normalized UID for
	// display in the frontend. It is intentionally stored separately because a
	// one-way hash cannot recover a display prefix.
	CardUIDPrefix string `gorm:"column:card_uid_prefix"`
	UserID        uint   `gorm:"index;not null"`
	Label         string
	Active        bool `gorm:"not null;default:true"`
	CreatedAt     time.Time
}

// AccessGrant allows a user to enter during one half-open time interval:
// ValidFrom <= now < ValidUntil. ExternalSource/ScopeRef/ExternalRef are nil
// for grants created by an administrator in the web UI.
type AccessGrant struct {
	ID uint `gorm:"primaryKey"`

	UserID     uint       `gorm:"not null;index:idx_access_grant_window,priority:1"`
	ValidFrom  time.Time  `gorm:"not null;index:idx_access_grant_window,priority:2"`
	ValidUntil time.Time  `gorm:"not null"`
	RevokedAt  *time.Time `gorm:"index"`

	ExternalSource *string `gorm:"uniqueIndex:idx_access_grant_external,priority:1;index:idx_access_grant_scope,priority:1"`
	ScopeRef       *string `gorm:"uniqueIndex:idx_access_grant_external,priority:2;index:idx_access_grant_scope,priority:2"`
	ExternalRef    *string `gorm:"uniqueIndex:idx_access_grant_external,priority:3"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AccessGrantSync stores the last accepted version of an external source's
// desired state for one opaque scope. It makes retries idempotent and prevents
// an older snapshot from overwriting a newer one.
type AccessGrantSync struct {
	ID          uint   `gorm:"primaryKey"`
	Source      string `gorm:"not null;uniqueIndex:idx_access_grant_sync,priority:1"`
	ScopeRef    string `gorm:"not null;uniqueIndex:idx_access_grant_sync,priority:2"`
	Revision    uint64 `gorm:"not null"`
	PayloadHash string `gorm:"not null"`
	UpdatedAt   time.Time
}

type CardRead struct {
	ID uint `gorm:"primaryKey"`
	// CardUID stores the SHA-256 hash of the normalized reader UID.
	CardUID string `gorm:"index;not null"`
	// CardUIDPrefix contains the first two characters of the normalized UID for
	// the admin log, without exposing the hash or raw UID.
	CardUIDPrefix string `gorm:"column:card_uid_prefix"`
	UserID        *uint  `gorm:"index"`
	Accepted      bool   `gorm:"not null"`
	CreatedAt     time.Time
}

type Entry struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"index;not null"`
	// CardUID is nil for entries opened without a card. When present, it stores
	// the SHA-256 hash of the normalized reader UID.
	CardUID *string `gorm:"index"`
	// CardUIDPrefix is nil when CardUID is nil and otherwise contains the first
	// two characters of the normalized UID for the admin log.
	CardUIDPrefix *string `gorm:"column:card_uid_prefix"`
	CreatedAt     time.Time
}
