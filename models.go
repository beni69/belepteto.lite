package main

import "time"

type User struct {
	ID           uint   `gorm:"primaryKey"`
	Email        string `gorm:"uniqueIndex;not null"`
	PasswordHash string `gorm:"not null"`
	Admin        bool   `gorm:"not null;default:false"`
	Active       bool   `gorm:"not null;default:true"`
	CreatedAt    time.Time
	Cards        []Card    `gorm:"constraint:OnDelete:CASCADE"`
	Sessions     []Session `gorm:"constraint:OnDelete:CASCADE"`
}

type Session struct {
	ID        string `gorm:"primaryKey"`
	UserID    uint   `gorm:"index;not null"`
	ExpiresAt time.Time
	CreatedAt time.Time
}

type Card struct {
	ID uint `gorm:"primaryKey"`
	// CardUID stores the reader's original colon-hex form, e.g.
	// "aa:bb:cc:dd:00:00:00:00", so it can round-trip to the device later.
	CardUID string `gorm:"not null"`
	// CardUIDCanonical is the lenient match key (colons/whitespace stripped,
	// upper-cased). Uniqueness and reader lookups use this column.
	CardUIDCanonical string `gorm:"uniqueIndex;not null"`
	UserID           uint   `gorm:"index;not null"`
	Label            string
	Active           bool `gorm:"not null;default:true"`
	CreatedAt        time.Time
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
