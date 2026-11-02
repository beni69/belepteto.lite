package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *gorm.DB
}

func OpenStore(dsn string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.AutoMigrate(&User{}, &Session{}, &Card{}, &CardRead{}, &Entry{}); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) CreateUser(u *User) error {
	return s.db.Create(u).Error
}

func (s *Store) UserByEmail(email string) (*User, error) {
	var u User
	err := s.db.Where("email = ?", email).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByID(id uint) (*User, error) {
	var u User
	err := s.db.First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	var users []User
	err := s.db.Order("created_at asc").Find(&users).Error
	return users, err
}

func (s *Store) CardsByUser(userID uint) ([]Card, error) {
	var cards []Card
	err := s.db.Where("user_id = ?", userID).Order("created_at asc").Find(&cards).Error
	return cards, err
}

func (s *Store) CardByUID(uid string) (*Card, error) {
	var c Card
	err := s.db.Where("card_uid = ?", uid).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CountUsers() (int64, error) {
	var n int64
	err := s.db.Model(&User{}).Count(&n).Error
	return n, err
}

// SeedAdmin creates an initial admin user if the users table is empty. It
// returns true if a user was created. The password is hashed with bcrypt.
func (s *Store) SeedAdmin(email, password string) (bool, error) {
	n, err := s.CountUsers()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	admin := &User{Email: email, PasswordHash: hash, Admin: true, Active: true}
	if err := s.CreateUser(admin); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CreateSession(sess *Session) error {
	return s.db.Create(sess).Error
}

// SessionUser returns the user for a valid, unexpired session whose owner is
// still active. Any failure (missing, expired, inactive) returns ErrNotFound.
func (s *Store) SessionUser(sessionID string) (*User, error) {
	var sess Session
	err := s.db.First(&sess, "id = ?", sessionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, ErrNotFound
	}

	u, err := s.UserByID(sess.UserID)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Store) DeleteSession(sessionID string) error {
	return s.db.Delete(&Session{}, "id = ?", sessionID).Error
}

func (s *Store) DeleteUserSessions(userID uint) error {
	return s.db.Delete(&Session{}, "user_id = ?", userID).Error
}

func (s *Store) CreateCardRead(r *CardRead) error {
	return s.db.Create(r).Error
}

func (s *Store) CreateEntry(e *Entry) error {
	return s.db.Create(e).Error
}
