package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrNotFound             = errors.New("not found")
	ErrDuplicate            = errors.New("already exists")
	ErrOIDCIdentityConflict = errors.New("OIDC identity is already linked to another account")
	ErrAmbiguousEmail       = errors.New("email matches multiple local accounts")
)

type Store struct {
	db *gorm.DB
}

const legacyCardUIDCanonicalColumn = "card_uid_canonical"

func OpenStore(dsn string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := migrateCardUID(db); err != nil {
		return nil, fmt.Errorf("migrate card UID schema: %w", err)
	}
	if err := db.AutoMigrate(&User{}, &Session{}, &Card{}, &CardRead{}, &Entry{}); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &Store{db: db}, nil
}

// migrateCardUID removes the old derived UID column from databases created by
// earlier versions. The old CardUID value is retained and upper-cased only;
// separators and whitespace are intentionally left unchanged.
func migrateCardUID(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(&Card{}) || !migrator.HasColumn(&Card{}, legacyCardUIDCanonicalColumn) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Card{}).
			Where("card_uid IS NOT NULL").
			UpdateColumn("card_uid", gorm.Expr("UPPER(card_uid)")).Error; err != nil {
			return fmt.Errorf("uppercase existing card UIDs: %w", err)
		}
		if err := tx.Migrator().DropColumn(&Card{}, legacyCardUIDCanonicalColumn); err != nil {
			return fmt.Errorf("drop %s: %w", legacyCardUIDCanonicalColumn, err)
		}
		return nil
	})
}

func (s *Store) CreateUser(u *User) error {
	err := s.db.Create(u).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

// SetUserActive toggles a user's active flag. When deactivating, the caller is
// responsible for clearing the user's sessions.
func (s *Store) SetUserActive(id uint, active bool) error {
	res := s.db.Model(&User{}).Where("id = ?", id).Update("active", active)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
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

// UserByOIDCIdentity finds a user by the issuer-scoped OIDC subject.
func (s *Store) UserByOIDCIdentity(issuer, subject string) (*User, error) {
	var u User
	err := s.db.Where("oidc_issuer = ? AND oidc_subject = ?", issuer, subject).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// LinkOIDCIdentity attaches a verified OIDC identity to an existing active
// local user. It never creates local accounts and will not overwrite an
// identity that has already been linked.
func (s *Store) LinkOIDCIdentity(issuer, subject, email string) (*User, error) {
	email = strings.TrimSpace(email)
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(subject) == "" || email == "" {
		return nil, ErrNotFound
	}

	var linked User
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// A concurrent callback may have linked this identity since the caller's
		// initial lookup. Resolve that identity before attempting an email link.
		var existing User
		err := tx.Where("oidc_issuer = ? AND oidc_subject = ?", issuer, subject).First(&existing).Error
		if err == nil {
			linked = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var matches []User
		if err := tx.Where("LOWER(email) = LOWER(?)", email).Limit(2).Find(&matches).Error; err != nil {
			return err
		}
		if len(matches) == 0 {
			return ErrNotFound
		}
		if len(matches) > 1 {
			return ErrAmbiguousEmail
		}

		user := matches[0]
		if !user.Active {
			return ErrNotFound
		}
		if user.OIDCIssuer != nil || user.OIDCSubject != nil {
			return ErrOIDCIdentityConflict
		}

		res := tx.Model(&User{}).
			Where("id = ? AND oidc_issuer IS NULL AND oidc_subject IS NULL", user.ID).
			Updates(map[string]any{"oidc_issuer": issuer, "oidc_subject": subject})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrOIDCIdentityConflict
		}
		user.OIDCIssuer = &issuer
		user.OIDCSubject = &subject
		linked = user
		return nil
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		// A simultaneous login can win the unique issuer/subject index. Accept
		// that result only if it resolved to an active user.
		user, lookupErr := s.UserByOIDCIdentity(issuer, subject)
		if lookupErr == nil && user.Active {
			return user, nil
		}
		return nil, ErrOIDCIdentityConflict
	}
	if err != nil {
		return nil, err
	}
	if !linked.Active {
		return nil, ErrNotFound
	}
	return &linked, nil
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

// CreateCard inserts a card. Card UIDs are stored in uppercase while retaining
// the reader-provided separators and formatting.
func (s *Store) CreateCard(c *Card) error {
	c.CardUID = strings.ToUpper(c.CardUID)
	err := s.db.Create(c).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

// CardExistsByUID reports whether any card has the given UID after uppercasing.
// No separators or other formatting are removed.
func (s *Store) CardExistsByUID(uid string) (bool, error) {
	var n int64
	err := s.db.Model(&Card{}).
		Where("card_uid = ?", strings.ToUpper(uid)).
		Count(&n).Error
	return n > 0, err
}

// ActiveCardOwner returns the active user who owns an active card matching the
// given UID after uppercasing. Returns ErrNotFound if there is no match, the
// card is inactive, or the owner is inactive.
func (s *Store) ActiveCardOwner(uid string) (*Card, *User, error) {
	var c Card
	err := s.db.Where("card_uid = ? AND active = ?", strings.ToUpper(uid), true).
		First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	u, err := s.UserByID(c.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !u.Active {
		return nil, nil, ErrNotFound
	}
	return &c, u, nil
}

// DeleteUserCard removes a card only if it belongs to the given user. Returns
// ErrNotFound if the card does not exist or is owned by someone else.
func (s *Store) DeleteUserCard(userID, cardID uint) error {
	res := s.db.Where("id = ? AND user_id = ?", cardID, userID).Delete(&Card{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RelabelUserCard updates a card's label only if it belongs to the given user.
func (s *Store) RelabelUserCard(userID, cardID uint, label string) error {
	res := s.db.Model(&Card{}).
		Where("id = ? AND user_id = ?", cardID, userID).
		Update("label", label)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CountUsers() (int64, error) {
	var n int64
	err := s.db.Model(&User{}).Count(&n).Error
	return n, err
}

// CardReadView is a cardRead log row joined with the resolved user's email.
type CardReadView struct {
	CardUID   string
	Email     string // empty if the card was unknown
	Accepted  bool
	CreatedAt time.Time
}

// EntryView is an entry log row joined with the opener's email.
type EntryView struct {
	Email     string
	CardUID   *string // null => remote open (future)
	CreatedAt time.Time
}

// ListCardReads returns cardRead log rows newest-first, paginated. When
// acceptedOnly is true, only accepted reads are returned. It also returns the
// total count for the active filter so the caller can paginate.
func (s *Store) ListCardReads(acceptedOnly bool, limit, offset int) ([]CardReadView, int64, error) {
	q := s.db.Model(&CardRead{})
	if acceptedOnly {
		q = q.Where("card_reads.accepted = ?", true)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var views []CardReadView
	err := q.
		Select("card_reads.card_uid AS card_uid, card_reads.accepted AS accepted, card_reads.created_at AS created_at, users.email AS email").
		Joins("LEFT JOIN users ON users.id = card_reads.user_id").
		Order("card_reads.created_at DESC").
		Limit(limit).Offset(offset).
		Scan(&views).Error
	return views, total, err
}

// ListEntries returns entry log rows newest-first, paginated, with the total.
func (s *Store) ListEntries(limit, offset int) ([]EntryView, int64, error) {
	q := s.db.Model(&Entry{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var views []EntryView
	err := q.
		Select("entries.card_uid AS card_uid, entries.created_at AS created_at, users.email AS email").
		Joins("LEFT JOIN users ON users.id = entries.user_id").
		Order("entries.created_at DESC").
		Limit(limit).Offset(offset).
		Scan(&views).Error
	return views, total, err
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
