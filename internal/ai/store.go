package ai

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const storeSchema = `
CREATE TABLE genres (
  sha256        TEXT PRIMARY KEY,
  -- Empty when the model was asked but not sure enough, or the request was
  -- refused as malformed. The row still exists so the book is never asked
  -- about again.
  genre         TEXT NOT NULL,
  fiction       REAL,
  model         TEXT NOT NULL,
  classified_at INTEGER NOT NULL
);
`

const storeSchemaVersion = 1

// Store keeps what the models said about each book, keyed by content hash.
//
// Kept apart from index.db, which may be deleted at any time: every row here
// was paid for by the operator, and rebuilding the index must not mean asking
// again. Keyed by SHA-256 rather than path so a renamed or re-imported book
// keeps its answer.
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore opens or creates the store.
func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("ai: create %s: %w", dir, err)
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ai: open store: %w", err)
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
		return fmt.Errorf("ai: read schema version: %w", err)
	}
	switch {
	case version == storeSchemaVersion:
		return nil
	case version != 0:
		return fmt.Errorf("ai: store schema version %d, expected %d", version, storeSchemaVersion)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(storeSchema); err != nil {
		return fmt.Errorf("ai: create schema: %w", err)
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

// Genre returns a book's genre. ok is false when the book was never
// classified; genre is "" when it was, without a confident answer.
func (s *Store) Genre(sha256 string) (string, bool) {
	var g string
	err := s.db.QueryRow(`SELECT genre FROM genres WHERE sha256 = ?`, sha256).Scan(&g)
	if err != nil {
		return "", false
	}
	return g, true
}

// Genres maps every confidently classified book's hash to its genre.
func (s *Store) Genres() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT sha256, genre FROM genres WHERE genre != ''`)
	if err != nil {
		return nil, fmt.Errorf("ai: list genres: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sum, g string
		if err := rows.Scan(&sum, &g); err != nil {
			return nil, err
		}
		out[sum] = g
	}
	return out, rows.Err()
}

// Classified reports whether a book has been asked about.
func (s *Store) Classified(sha256 string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM genres WHERE sha256 = ?`, sha256).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetGenre records an answer. fiction is nil when there was none.
func (s *Store) SetGenre(sha256, genre string, fiction *float64, model string) error {
	_, err := s.db.Exec(`INSERT INTO genres (sha256, genre, fiction, model, classified_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(sha256) DO UPDATE SET genre = excluded.genre, fiction = excluded.fiction,
		  model = excluded.model, classified_at = excluded.classified_at`,
		sha256, genre, fiction, model, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("ai: record genre: %w", err)
	}
	return nil
}

// Carry copies a book's answer to its new hash after its bytes changed under a
// metadata edit. The book is the same book, and asking again would cost the
// operator a model call for an answer already paid for. A book with no answer
// carries nothing; an answer already recorded for the new hash is kept.
func (s *Store) Carry(fromSHA, toSHA string) error {
	if fromSHA == toSHA {
		return nil
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO genres (sha256, genre, fiction, model, classified_at)
		SELECT ?, genre, fiction, model, classified_at FROM genres WHERE sha256 = ?`, toSHA, fromSHA)
	if err != nil {
		return fmt.Errorf("ai: carry genre: %w", err)
	}
	return nil
}
