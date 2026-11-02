package main

import "time"

type User struct {
	ID              uint   `gorm:"primaryKey"`
	Email           string `gorm:"uniqueIndex;not null"`
	PasswordHash    string `gorm:"not null"`
	Admin           bool   `gorm:"not null;default:false"`
	Active          bool   `gorm:"not null;default:true"`
	PermanentAccess bool   `gorm:"not null;default:true"`
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
	// stores the reader's colon-hex form in uppercase
	// "AA:BB:CC:DD:00:00:00:00"
	CardUID   string `gorm:"uniqueIndex;not null"`
	UserID    uint   `gorm:"index;not null"`
	Label     string
	Active    bool `gorm:"not null;default:true"`
	CreatedAt time.Time
}

// AccessGrant allows a user to enter during one half-open time interval:
// ValidFrom <= now < ValidUntil. ExternalSource/ScopeRef/ExternalRef are nil
// for grants created by an administrator in the web UI.
type AccessGrant struct {
	ID uint `gorm:"primaryKey"`

	UserID     uint      `gorm:"not null;index:idx_access_grant_window,priority:1"`
	ValidFrom  time.Time `gorm:"not null;index:idx_access_grant_window,priority:2"`
	ValidUntil time.Time `gorm:"not null"`
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
	ID        uint   `gorm:"primaryKey"`
	CardUID   string `gorm:"index;not null"`
	UserID    *uint  `gorm:"index"`
	Accepted  bool   `gorm:"not null"`
	CreatedAt time.Time
}

type Entry struct {
	ID        uint    `gorm:"primaryKey"`
	UserID    uint    `gorm:"index;not null"`
	CardUID   *string `gorm:"index"`
	CreatedAt time.Time
}
