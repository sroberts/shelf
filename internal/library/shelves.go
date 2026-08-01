package library

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// errNoRows aliases the driver-independent "no rows" sentinel.
var errNoRows = sql.ErrNoRows

// ErrShelfNotFound is returned when a shelf name does not exist.
var ErrShelfNotFound = errors.New("library: shelf not found")

// ShelfKind distinguishes the two ways a shelf decides what it contains.
type ShelfKind string

const (
	// KindQuery is a saved search, re-evaluated every time it is used.
	KindQuery ShelfKind = "query"
	// KindManual is an explicit set of books.
	KindManual ShelfKind = "manual"
)

// Shelf is a named set of books, and the unit that syncs to a device.
type Shelf struct {
	ID    int64     `toml:"-"`
	Name  string    `toml:"name"`
	Kind  ShelfKind `toml:"kind"`
	Query string    `toml:"query,omitempty"`
	// Members holds book paths for manual shelves. Paths, not index ids,
	// because the index is a cache that can be rebuilt from scratch.
	Members []string `toml:"members,omitempty"`
}

// shelvesFile is the on-disk TOML form.
type shelvesFile struct {
	Shelves []Shelf `toml:"shelf"`
}

// Shelves are the one piece of library state that cannot be reconstructed by
// rescanning the filesystem, so they are written to TOML as the authoritative
// copy and mirrored into SQLite for querying. Deleting index.db loses nothing.

// LoadShelves reads the TOML file and replaces the index's shelf tables with
// its contents. Call after opening an index that may be stale or freshly built.
func (db *DB) LoadShelves(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no shelves yet is a normal state
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var file shelvesFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM shelves`); err != nil {
		return fmt.Errorf("clear shelves: %w", err)
	}

	for _, s := range file.Shelves {
		if s.Name == "" {
			continue
		}
		if s.Kind == "" {
			s.Kind = KindQuery
			if len(s.Members) > 0 {
				s.Kind = KindManual
			}
		}

		var id int64
		err := tx.QueryRow(
			`INSERT INTO shelves (name, query, kind) VALUES (?,?,?) RETURNING id`,
			s.Name, s.Query, string(s.Kind)).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert shelf %q: %w", s.Name, err)
		}

		for _, member := range s.Members {
			// A member whose file is not in the index is skipped rather than
			// failing the load; it may be a book that has not been scanned yet.
			var bookID int64
			err := tx.QueryRow(`SELECT id FROM books WHERE path = ?`, NormalizePath(member)).Scan(&bookID)
			if err != nil {
				continue
			}
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO shelf_members (shelf_id, book_id) VALUES (?,?)`,
				id, bookID); err != nil {
				return fmt.Errorf("add %q to shelf %q: %w", member, s.Name, err)
			}
		}
	}
	return tx.Commit()
}

// SaveShelves writes every shelf to TOML atomically.
func (db *DB) SaveShelves(path string) error {
	shelves, err := db.ListShelves()
	if err != nil {
		return err
	}

	// Resolve manual members back to paths, since that is what the file stores.
	for _, s := range shelves {
		if s.Kind != KindManual {
			continue
		}
		books, err := db.ShelfBooks(s.Name)
		if err != nil {
			return err
		}
		s.Members = nil
		for _, b := range books {
			s.Members = append(s.Members, b.Path)
		}
	}

	file := shelvesFile{Shelves: make([]Shelf, 0, len(shelves))}
	for _, s := range shelves {
		file.Shelves = append(file.Shelves, *s)
	}

	var sb strings.Builder
	sb.WriteString("# shelf definitions\n")
	sb.WriteString("# This file is authoritative. The SQLite index is a cache and\n")
	sb.WriteString("# may be deleted at any time; this file is what survives.\n\n")
	if err := toml.NewEncoder(&sb).Encode(file); err != nil {
		return fmt.Errorf("encode shelves: %w", err)
	}

	return writeFileAtomic(path, []byte(sb.String()), 0o644)
}

// writeFileAtomic writes via a temp file and rename, so an interrupted write
// cannot truncate the existing file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// CreateShelf adds a shelf.
func (db *DB) CreateShelf(s Shelf) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("shelf name must not be empty")
	}
	if s.Kind == "" {
		s.Kind = KindQuery
	}
	if s.Kind == KindQuery {
		// Reject an unparseable query at creation time rather than at sync
		// time, when it would interrupt a transfer.
		if _, _, err := CompileQuery(s.Query); err != nil {
			return err
		}
	}

	_, err := db.sql.Exec(
		`INSERT INTO shelves (name, query, kind) VALUES (?,?,?)`,
		s.Name, s.Query, string(s.Kind))
	if err != nil {
		return fmt.Errorf("create shelf %q: %w", s.Name, err)
	}
	return nil
}

// DeleteShelf removes a shelf by name.
func (db *DB) DeleteShelf(name string) error {
	res, err := db.sql.Exec(`DELETE FROM shelves WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete shelf %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrShelfNotFound, name)
	}
	return nil
}

// ListShelves returns every shelf, sorted by name.
func (db *DB) ListShelves() ([]*Shelf, error) {
	rows, err := db.sql.Query(
		`SELECT id, name, COALESCE(query, ''), kind FROM shelves ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list shelves: %w", err)
	}
	defer rows.Close()

	var out []*Shelf
	for rows.Next() {
		var s Shelf
		var kind string
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &kind); err != nil {
			return nil, err
		}
		s.Kind = ShelfKind(kind)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// GetShelf looks up one shelf by name.
func (db *DB) GetShelf(name string) (*Shelf, error) {
	var s Shelf
	var kind string
	err := db.sql.QueryRow(
		`SELECT id, name, COALESCE(query, ''), kind FROM shelves WHERE name = ?`, name).
		Scan(&s.ID, &s.Name, &s.Query, &kind)
	if errors.Is(err, errNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrShelfNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get shelf %q: %w", name, err)
	}
	s.Kind = ShelfKind(kind)
	return &s, nil
}

// ShelfBooks resolves a shelf to its current book set.
//
// Query shelves are re-evaluated on every call, so a saved search reflects the
// library as it is now rather than as it was when the shelf was created.
func (db *DB) ShelfBooks(name string) ([]*Book, error) {
	s, err := db.GetShelf(name)
	if err != nil {
		return nil, err
	}

	if s.Kind == KindQuery {
		return db.Search(s.Query, SearchOptions{})
	}
	return db.queryBooks(
		`SELECT `+bookColumns+` FROM books
		 JOIN shelf_members ON shelf_members.book_id = books.id
		 WHERE shelf_members.shelf_id = ?
		 ORDER BY `+defaultOrder, s.ID)
}

// AddToShelf adds books to a manual shelf by path.
func (db *DB) AddToShelf(name string, paths []string) error {
	s, err := db.GetShelf(name)
	if err != nil {
		return err
	}
	if s.Kind != KindManual {
		return fmt.Errorf("shelf %q is a saved query; edit its query instead", name)
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, p := range paths {
		var bookID int64
		err := tx.QueryRow(`SELECT id FROM books WHERE path = ?`, NormalizePath(p)).Scan(&bookID)
		if errors.Is(err, errNoRows) {
			return fmt.Errorf("%w: %s (run `shelf scan` first)", ErrNotFound, p)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO shelf_members (shelf_id, book_id) VALUES (?,?)`,
			s.ID, bookID); err != nil {
			return fmt.Errorf("add %s to %q: %w", p, name, err)
		}
	}
	return tx.Commit()
}

// RemoveFromShelf removes books from a manual shelf by path.
func (db *DB) RemoveFromShelf(name string, paths []string) error {
	s, err := db.GetShelf(name)
	if err != nil {
		return err
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, p := range paths {
		if _, err := tx.Exec(
			`DELETE FROM shelf_members WHERE shelf_id = ?
			 AND book_id IN (SELECT id FROM books WHERE path = ?)`,
			s.ID, NormalizePath(p)); err != nil {
			return fmt.Errorf("remove %s from %q: %w", p, name, err)
		}
	}
	return tx.Commit()
}

// ShelfNames lists shelf names, sorted.
func (db *DB) ShelfNames() ([]string, error) {
	shelves, err := db.ListShelves()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(shelves))
	for i, s := range shelves {
		names[i] = s.Name
	}
	sort.Strings(names)
	return names, nil
}
