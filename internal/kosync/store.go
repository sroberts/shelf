package kosync

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Errors the store returns.
var (
	ErrUserExists   = errors.New("kosync: user already exists")
	ErrNoSuchUser   = errors.New("kosync: no such user")
	ErrBadPassword  = errors.New("kosync: incorrect password")
	ErrNoProgress   = errors.New("kosync: no progress for that document")
	ErrEmptyRequest = errors.New("kosync: username and password are required")
)

const storeSchema = `
CREATE TABLE users (
  username   TEXT PRIMARY KEY,
  salt       TEXT NOT NULL,
  key_hash   TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE progress (
  username    TEXT NOT NULL,
  document    TEXT NOT NULL,
  progress    TEXT,
  percentage  REAL,
  device      TEXT,
  device_id   TEXT,
  -- The CrossPoint position extension, stored verbatim as JSON. See the note
  -- on Position for why it is usually absent.
  position    TEXT,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (username, document)
);
CREATE INDEX progress_document ON progress(document);
`

const storeSchemaVersion = 1

// Store persists users and reading progress.
//
// Unlike the library index, this is NOT disposable: it is the only copy of
// where you are in each book. Deleting it loses reading positions that exist
// nowhere else, because the device pushes progress here and does not keep a
// synchronised copy of its own.
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore opens or creates the progress database.
func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("kosync: create %s: %w", dir, err)
		}
	}

	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("kosync: open store: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	if _, err := s.db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = err // unavailable on some filesystems; not fatal
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("kosync: read schema version: %w", err)
	}
	switch {
	case version == storeSchemaVersion:
		return nil
	case version != 0:
		// Refuse rather than guess. This database holds the only copy of your
		// reading positions, so a wrong migration is not recoverable by
		// rescanning anything.
		return fmt.Errorf("kosync: store schema version %d, expected %d", version, storeSchemaVersion)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(storeSchema); err != nil {
		return fmt.Errorf("kosync: create schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, storeSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the store's location on disk.
func (s *Store) Path() string { return s.path }

// Credential storage.
//
// The protocol transmits MD5(password) as the auth key, so that value is the
// credential: anything holding it can authenticate. shelf therefore does not
// store it. What is stored is SHA-256(salt || key) with a per-user random salt,
// so a stolen database does not hand over something directly replayable.
//
// This is deliberately not sold as password hashing. SHA-256 is fast and MD5 of
// a password is weak to begin with; the protocol's design is the ceiling here
// and shelf does not get to raise it. What the salting buys is narrow and real:
// the database file stops being a list of working credentials.

// CreateUser registers a user. key is the MD5 the client sends.
func (s *Store) CreateUser(username, key string) error {
	username = NormalizeUsername(username)
	if username == "" || key == "" {
		return ErrEmptyRequest
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("kosync: generate salt: %w", err)
	}
	saltHex := hex.EncodeToString(salt)

	_, err := s.db.Exec(
		`INSERT INTO users (username, salt, key_hash, created_at) VALUES (?,?,?,?)`,
		username, saltHex, hashKey(saltHex, key), time.Now().Unix())
	if err != nil {
		// The protocol has a specific status for an existing user, so this
		// needs to be distinguishable rather than a generic failure.
		if isUniqueViolation(err) {
			return ErrUserExists
		}
		return fmt.Errorf("kosync: create user: %w", err)
	}
	return nil
}

// Authenticate verifies a username and auth key.
func (s *Store) Authenticate(username, key string) error {
	username = NormalizeUsername(username)
	if username == "" || key == "" {
		return ErrEmptyRequest
	}

	var salt, stored string
	err := s.db.QueryRow(`SELECT salt, key_hash FROM users WHERE username = ?`, username).
		Scan(&salt, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSuchUser
	}
	if err != nil {
		return fmt.Errorf("kosync: look up user: %w", err)
	}

	// Constant time, so a wrong key cannot be narrowed down by timing.
	if subtle.ConstantTimeCompare([]byte(hashKey(salt, key)), []byte(stored)) != 1 {
		return ErrBadPassword
	}
	return nil
}

// hashKey derives the stored verifier from a salt and the client's auth key.
func hashKey(saltHex, key string) string {
	h := sha256.New()
	h.Write([]byte(saltHex))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))
}

// isUniqueViolation reports whether an insert failed on a primary key.
//
// modernc's driver does not expose a typed constraint error, so this matches on
// the message. Narrow enough not to swallow unrelated failures.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Progress is one book's reading position.
type Progress struct {
	Document   string  `json:"document"`
	Progress   string  `json:"progress"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device,omitempty"`
	DeviceID   string  `json:"device_id,omitempty"`

	// Position is CrossPoint's extension: a structured location (spine index,
	// page, xpath) that is richer than the standard progress string.
	//
	// It will almost always be empty here. The firmware only sends it when the
	// configured server URL exactly equals CrossPoint's own sync server, so a
	// self-hosted shelf never receives it. Stored verbatim when it does arrive,
	// which costs nothing and means shelf is ready if that check ever loosens.
	Position json.RawMessage `json:"position,omitempty"`

	// Timestamp is when the server recorded this, in Unix seconds.
	Timestamp int64 `json:"timestamp"`
}

// PutProgress records a reading position, replacing any previous one.
func (s *Store) PutProgress(username string, p Progress) (int64, error) {
	username = NormalizeUsername(username)
	if p.Document == "" {
		return 0, fmt.Errorf("kosync: progress requires a document id")
	}

	now := time.Now().Unix()
	var position any
	if len(p.Position) > 0 {
		position = string(p.Position)
	}

	_, err := s.db.Exec(`
INSERT INTO progress (username, document, progress, percentage, device, device_id, position, updated_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(username, document) DO UPDATE SET
  progress=excluded.progress, percentage=excluded.percentage,
  device=excluded.device, device_id=excluded.device_id,
  position=excluded.position, updated_at=excluded.updated_at`,
		username, p.Document, p.Progress, p.Percentage, p.Device, p.DeviceID, position, now)
	if err != nil {
		return 0, fmt.Errorf("kosync: save progress: %w", err)
	}
	return now, nil
}

// GetProgress returns a stored position.
func (s *Store) GetProgress(username, document string) (*Progress, error) {
	username = NormalizeUsername(username)

	var (
		p        Progress
		progress sql.NullString
		device   sql.NullString
		deviceID sql.NullString
		position sql.NullString
		pct      sql.NullFloat64
	)
	err := s.db.QueryRow(`
SELECT progress, percentage, device, device_id, position, updated_at
FROM progress WHERE username = ? AND document = ?`, username, document).
		Scan(&progress, &pct, &device, &deviceID, &position, &p.Timestamp)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoProgress
	}
	if err != nil {
		return nil, fmt.Errorf("kosync: read progress: %w", err)
	}

	p.Document = document
	p.Progress = progress.String
	p.Percentage = pct.Float64
	p.Device = device.String
	p.DeviceID = deviceID.String
	if position.Valid && position.String != "" {
		p.Position = json.RawMessage(position.String)
	}
	return &p, nil
}

// AllProgress returns every recorded position for a user, keyed by document id.
//
// This is what the library table uses: one query, then an in-memory join, since
// hashing every book on every listing would be far slower than the lookup.
func (s *Store) AllProgress(username string) (map[string]Progress, error) {
	username = NormalizeUsername(username)

	rows, err := s.db.Query(`
SELECT document, progress, percentage, device, device_id, updated_at
FROM progress WHERE username = ?`, username)
	if err != nil {
		return nil, fmt.Errorf("kosync: list progress: %w", err)
	}
	defer rows.Close()

	out := map[string]Progress{}
	for rows.Next() {
		var (
			p        Progress
			progress sql.NullString
			device   sql.NullString
			deviceID sql.NullString
			pct      sql.NullFloat64
		)
		if err := rows.Scan(&p.Document, &progress, &pct, &device, &deviceID, &p.Timestamp); err != nil {
			return nil, err
		}
		p.Progress = progress.String
		p.Percentage = pct.Float64
		p.Device = device.String
		p.DeviceID = deviceID.String
		out[p.Document] = p
	}
	return out, rows.Err()
}

// Users lists registered usernames.
func (s *Store) Users() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
