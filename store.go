package main

import (
	"crypto/sha256"
	"encoding/hex"
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
	ErrAmbiguousEmail       = errors.New("email matches more than one user")
	ErrInvalidWindow        = errors.New("invalid access time window")
	ErrStaleRevision        = errors.New("snapshot revision is older than the current revision")
	ErrRevisionConflict     = errors.New("snapshot revision was already used with different data")
	ErrOIDCIdentityConflict = errors.New("OIDC identity is already linked to another account")
)

type Store struct {
	db *gorm.DB
}

const (
	legacyCardUIDCanonicalColumn = "card_uid_canonical"
	cardUIDHashMigrationName     = "card_uid_sha256"
	cardUIDHashMigrationVersion  = 1
)

type schemaMigration struct {
	Name    string `gorm:"primaryKey"`
	Version int    `gorm:"not null"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

func OpenStore(dsn string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.AutoMigrate(&User{}, &Session{}, &Card{}, &AccessGrant{}, &AccessGrantSync{}, &CardRead{}, &Entry{}, &schemaMigration{}); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := migrateCardUID(db); err != nil {
		return nil, fmt.Errorf("migrate card UID data: %w", err)
	}

	return &Store{db: db}, nil
}

// migrateCardUID hashes existing plaintext UIDs and records the migration so
// the operation is not repeated on later startups. The migration includes
// card reads and entries because all persisted UID-bearing values must use the
// same representation as cards.
func migrateCardUID(db *gorm.DB) error {
	var migration schemaMigration
	err := db.Where("name = ?", cardUIDHashMigrationName).First(&migration).Error
	if err == nil && migration.Version >= cardUIDHashMigrationVersion {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"cards", "card_reads", "entries"} {
			if err := migrateUIDTable(tx, table); err != nil {
				return fmt.Errorf("hash UIDs in %s: %w", table, err)
			}
		}

		if tx.Migrator().HasTable(&Card{}) && tx.Migrator().HasColumn(&Card{}, legacyCardUIDCanonicalColumn) {
			if err := dropLegacyCardUIDColumn(tx); err != nil {
				return fmt.Errorf("drop %s: %w", legacyCardUIDCanonicalColumn, err)
			}
		}

		if err := tx.Where("name = ?", cardUIDHashMigrationName).Delete(&schemaMigration{}).Error; err != nil {
			return fmt.Errorf("replace card UID migration marker: %w", err)
		}
		if err := tx.Create(&schemaMigration{
			Name:    cardUIDHashMigrationName,
			Version: cardUIDHashMigrationVersion,
		}).Error; err != nil {
			return fmt.Errorf("write card UID migration marker: %w", err)
		}
		return nil
	})
}

// migrateUIDTable hashes every non-null card_uid and derives its safe display
// prefix. The prefix is deliberately kept in a separate column because it is
// impossible to recover it from a one-way hash.
func migrateUIDTable(tx *gorm.DB, table string) error {
	var rows []struct {
		ID      uint    `gorm:"column:id"`
		CardUID *string `gorm:"column:card_uid"`
	}
	if err := tx.Table(table).Select("id, card_uid").Find(&rows).Error; err != nil {
		return err
	}

	for _, row := range rows {
		if row.CardUID == nil {
			continue
		}
		hash, prefix := cardUIDHashAndPrefix(*row.CardUID)
		if err := tx.Table(table).Where("id = ?", row.ID).Updates(map[string]any{
			"card_uid":        hash,
			"card_uid_prefix": prefix,
		}).Error; err != nil {
			return fmt.Errorf("update row %d: %w", row.ID, err)
		}
	}
	return nil
}

func dropLegacyCardUIDColumn(tx *gorm.DB) error {
	// The old column had a unique index in some database versions. SQLite
	// refuses to drop an indexed column, so remove any legacy index that refers
	// to it before dropping the column itself.
	var indexes []struct {
		Name string
	}
	if err := tx.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql LIKE ?",
		"cards", "%card_uid_canonical%",
	).Scan(&indexes).Error; err != nil {
		return err
	}
	for _, index := range indexes {
		quotedName := `"` + strings.ReplaceAll(index.Name, `"`, `""`) + `"`
		if err := tx.Exec("DROP INDEX " + quotedName).Error; err != nil {
			return err
		}
	}
	return tx.Exec("ALTER TABLE cards DROP COLUMN card_uid_canonical").Error
}

func normalizeCardUID(uid string) string {
	return strings.ToUpper(uid)
}

func cardUIDHashAndPrefix(uid string) (string, string) {
	normalized := normalizeCardUID(uid)
	digest := sha256.Sum256([]byte(normalized))
	prefix := normalized
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return hex.EncodeToString(digest[:]), prefix
}

func hashCardUID(uid string) string {
	hash, _ := cardUIDHashAndPrefix(uid)
	return hash
}

func cardUIDPrefix(uid string) string {
	_, prefix := cardUIDHashAndPrefix(uid)
	return prefix
}

func (s *Store) CreateUser(u *User) error {
	return s.createUser(u, true)
}

// CreateUserWithAccess creates a user with an explicit door access policy.
// CreateUser remains always-allowed by default for existing callers.
func (s *Store) CreateUserWithAccess(u *User, permanentAccess bool) error {
	return s.createUser(u, permanentAccess)
}

func (s *Store) createUser(u *User, permanentAccess bool) error {
	u.Email = normalizeEmail(u.Email)
	if u.Email == "" {
		return errors.New("email is required")
	}
	for i := range u.Cards {
		prepareCardUID(&u.Cards[i])
	}

	// GORM applies the bool column default to a zero value during Create. Insert
	// with the legacy true value, then explicitly update to false in the same
	// transaction when scheduled-only access was requested.
	u.PermanentAccess = true
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&User{}).Where("LOWER(email) = ?", u.Email).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return ErrDuplicate
		}

		if err := tx.Create(u).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrDuplicate
			}
			return err
		}
		if !permanentAccess {
			return tx.Model(&User{}).Where("id = ?", u.ID).UpdateColumn("permanent_access", false).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	u.PermanentAccess = permanentAccess
	return nil
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

// SetUserPassword updates only the password hash for the identified user.
func (s *Store) SetUserPassword(id uint, passwordHash string) error {
	res := s.db.Model(&User{}).Where("id = ?", id).Update("password_hash", passwordHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserByEmail(email string) (*User, error) {
	var users []User
	err := s.db.Where("LOWER(email) = ?", normalizeEmail(email)).Limit(2).Find(&users).Error
	if err != nil {
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

func (s *Store) ListUsersWithCards() ([]User, error) {
	var users []User
	err := s.db.Preload("Cards").Order("created_at asc").Find(&users).Error
	return users, err
}

func (s *Store) CardsByUser(userID uint) ([]Card, error) {
	var cards []Card
	err := s.db.Where("user_id = ?", userID).Order("created_at asc").Find(&cards).Error
	return cards, err
}

// CreateCard inserts a card. The raw UID is normalized, hashed, and replaced
// with its safe display prefix before it reaches the database.
func (s *Store) CreateCard(c *Card) error {
	prepareCardUID(c)
	err := s.db.Create(c).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

func prepareCardUID(c *Card) {
	hash, prefix := cardUIDHashAndPrefix(c.CardUID)
	c.CardUID = hash
	c.CardUIDPrefix = prefix
}

// CardExistsByUID reports whether any card has the given UID after
// normalization. No separators or other formatting are removed.
func (s *Store) CardExistsByUID(uid string) (bool, error) {
	var n int64
	err := s.db.Model(&Card{}).
		Where("card_uid = ?", hashCardUID(uid)).
		Count(&n).Error
	return n > 0, err
}

// ActiveCardOwner returns the active user who owns an active card matching the
// given UID after normalization. Returns ErrNotFound if there is no match, the
// card is inactive, or the owner is inactive.
func (s *Store) ActiveCardOwner(uid string) (*Card, *User, error) {
	var c Card
	err := s.db.Where("card_uid = ? AND active = ?", hashCardUID(uid), true).
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
// It intentionally contains only the safe UID prefix, never the stored hash.
type CardReadView struct {
	CardUIDPrefix string
	Email         string // empty if the card was unknown
	Accepted      bool
	CreatedAt     time.Time
}

// EntryView is an entry log row joined with the opener's email.
// CardUIDPrefix is nil for remote opens.
type EntryView struct {
	Email         string
	CardUIDPrefix *string
	CreatedAt     time.Time
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
		Select("card_reads.card_uid_prefix AS card_uid_prefix, card_reads.accepted AS accepted, card_reads.created_at AS created_at, users.email AS email").
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
		Select("entries.card_uid_prefix AS card_uid_prefix, entries.created_at AS created_at, users.email AS email").
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
	hash, prefix := cardUIDHashAndPrefix(r.CardUID)
	r.CardUID = hash
	r.CardUIDPrefix = prefix
	return s.db.Create(r).Error
}

func (s *Store) CreateEntry(e *Entry) error {
	if e.CardUID == nil {
		e.CardUIDPrefix = nil
		return s.db.Create(e).Error
	}

	hash, prefix := cardUIDHashAndPrefix(*e.CardUID)
	e.CardUID = &hash
	e.CardUIDPrefix = &prefix
	return s.db.Create(e).Error
}
