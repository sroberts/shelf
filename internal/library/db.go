// Package library indexes a directory tree of books and searches it.
//
// Files on disk are the source of truth. This index is a derived cache: it can
// be deleted at any time and rebuilt by rescanning, and nothing in it is
// authoritative except the shelves, which are mirrored to TOML for exactly that
// reason.
package library

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schema is applied to a fresh database.
//
// Two deliberate departures from the draft spec:
//
//  1. books.sha256 is indexed but NOT unique. Real libraries contain the same
//     file in two places, and a UNIQUE constraint would make a scan fail on a
//     legitimate library rather than reporting the duplicate. Identity is the
//     path; the hash is for change detection and sync bookkeeping.
//
//  2. books carries a denormalized tags_flat column. FTS5 external-content
//     tables read every indexed column from the content table, so indexing the
//     separate tags table directly fails at rebuild time.
const schema = `
CREATE TABLE books (
  id            INTEGER PRIMARY KEY,
  sha256        TEXT NOT NULL,
  -- KOReader partial-MD5, used to join reading progress. Cached here because
  -- hashing every book on every listing would make shelf ls unusable.
  doc_id        TEXT,
  path          TEXT NOT NULL UNIQUE,
  size          INTEGER NOT NULL,
  mtime_unix    INTEGER NOT NULL,
  format        TEXT NOT NULL,
  title         TEXT,
  author_sort   TEXT,
  authors       TEXT,
  series        TEXT,
  series_index  REAL,
  language      TEXT,
  publisher     TEXT,
  pubdate       TEXT,
  identifiers   TEXT,
  added_unix    INTEGER NOT NULL,
  cover_blob    BLOB,
  meta_json     TEXT,
  tags_flat     TEXT
);
CREATE INDEX books_sha256 ON books(sha256);
CREATE INDEX books_doc_id ON books(doc_id);
CREATE INDEX books_author_sort ON books(author_sort);
CREATE INDEX books_series ON books(series, series_index);

CREATE TABLE tags (
  book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  tag     TEXT NOT NULL,
  PRIMARY KEY (book_id, tag)
);
CREATE INDEX tags_tag ON tags(tag);

CREATE TABLE shelves (
  id    INTEGER PRIMARY KEY,
  name  TEXT NOT NULL UNIQUE,
  query TEXT,
  kind  TEXT NOT NULL
);
CREATE TABLE shelf_members (
  shelf_id INTEGER NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
  book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  PRIMARY KEY (shelf_id, book_id)
);

CREATE VIRTUAL TABLE books_fts USING fts5(
  title, author_sort, series, tags_flat,
  content='books', content_rowid='id'
);

-- External-content FTS5 does not track its content table on its own.
CREATE TRIGGER books_fts_ai AFTER INSERT ON books BEGIN
  INSERT INTO books_fts(rowid, title, author_sort, series, tags_flat)
  VALUES (new.id, new.title, new.author_sort, new.series, new.tags_flat);
END;
CREATE TRIGGER books_fts_ad AFTER DELETE ON books BEGIN
  INSERT INTO books_fts(books_fts, rowid, title, author_sort, series, tags_flat)
  VALUES ('delete', old.id, old.title, old.author_sort, old.series, old.tags_flat);
END;
CREATE TRIGGER books_fts_au AFTER UPDATE ON books BEGIN
  INSERT INTO books_fts(books_fts, rowid, title, author_sort, series, tags_flat)
  VALUES ('delete', old.id, old.title, old.author_sort, old.series, old.tags_flat);
  INSERT INTO books_fts(rowid, title, author_sort, series, tags_flat)
  VALUES (new.id, new.title, new.author_sort, new.series, new.tags_flat);
END;
`

// schemaVersion is bumped whenever schema changes. Because the index is a pure
// cache, an unrecognized version is handled by rebuilding from scratch rather
// than by writing migration code.
const schemaVersion = 2

// DB is an open library index.
type DB struct {
	sql  *sql.DB
	path string
}

// Open opens the index at path, creating or rebuilding it as needed.
//
// A schema mismatch or a corrupt file is not an error: the index is derived
// data, so it is discarded and recreated. The caller is expected to rescan.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create index directory: %w", err)
		}
	}

	db, err := openAt(path)
	if err == nil {
		return db, nil
	}

	// Recreate on any structural problem. Memory databases cannot be removed
	// and re-opened, so only do this for real files.
	if path == ":memory:" {
		return nil, err
	}
	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return nil, fmt.Errorf("index at %s is unusable (%v) and could not be removed: %w", path, err, rmErr)
	}
	return openAt(path)
}

func openAt(path string) (*DB, error) {
	dsn := path
	if path != ":memory:" {
		// Foreign keys are off by default in SQLite and the cascade deletes in
		// this schema depend on them.
		dsn = path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}

	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}

	// A single writer avoids "database is locked" under concurrent scans; the
	// workload is one process doing bulk writes, so this costs nothing.
	sqldb.SetMaxOpenConns(1)

	db := &DB{sql: sqldb, path: path}
	if err := db.init(); err != nil {
		sqldb.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) init() error {
	if _, err := db.sql.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		// WAL is unavailable on some filesystems and in memory; not fatal.
		_ = err
	}
	if _, err := db.sql.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	var version int
	if err := db.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	switch {
	case version == schemaVersion:
		// Confirm the schema really is present; a truncated file reports 0 rows
		// but a matching user_version is impossible, so this is cheap insurance.
		var n int
		if err := db.sql.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='books'`).Scan(&n); err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("index reports schema version %d but has no books table", version)
		}
		return nil

	case version == 0:
		return db.create()

	default:
		return fmt.Errorf("index schema version %d is not version %d", version, schemaVersion)
	}
}

func (db *DB) create() error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return tx.Commit()
}

// Close closes the index.
func (db *DB) Close() error { return db.sql.Close() }

// Path is the index file location.
func (db *DB) Path() string { return db.path }

// RebuildFTS regenerates the full-text index from the books table. Used by
// `shelf scan --deep` and as a repair for an index that predates a schema fix.
func (db *DB) RebuildFTS() error {
	_, err := db.sql.Exec(`INSERT INTO books_fts(books_fts) VALUES ('rebuild')`)
	if err != nil {
		return fmt.Errorf("rebuild full-text index: %w", err)
	}
	return nil
}

// Count returns the number of indexed books.
func (db *DB) Count() (int, error) {
	var n int
	err := db.sql.QueryRow(`SELECT count(*) FROM books`).Scan(&n)
	return n, err
}
