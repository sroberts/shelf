package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned when a lookup matches no book.
var ErrNotFound = errors.New("library: book not found")

// Format identifies a book's container format.
type Format string

const (
	FormatEPUB Format = "epub"
	FormatTXT  Format = "txt"
	FormatXTC  Format = "xtc" // Xteink native; opaque passthrough
	FormatPDF  Format = "pdf" // source format only; converted before sync
)

// FormatOf classifies a file by extension.
func FormatOf(path string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		return FormatEPUB, true
	case ".txt":
		return FormatTXT, true
	case ".xtc", ".xtch":
		return FormatXTC, true
	case ".pdf":
		return FormatPDF, true
	}
	return "", false
}

// SyncableToDevice reports whether the firmware can render this format
// directly. PDF is indexed but must be converted before it reaches a device.
func (f Format) SyncableToDevice() bool {
	switch f {
	case FormatEPUB, FormatTXT, FormatXTC:
		return true
	}
	return false
}

// Book is one indexed file.
type Book struct {
	ID          int64
	SHA256      string
	Path        string // absolute, canonical, NFC-normalized
	Size        int64
	MTimeUnix   int64
	Format      Format
	Title       string
	AuthorSort  string
	Authors     []string
	Series      string
	SeriesIndex float64
	Language    string
	Publisher   string
	PubDate     string
	Identifiers map[string]string
	AddedUnix   int64
	Tags        []string
	Cover       []byte
	MetaJSON    string
}

// DisplayTitle falls back to the filename for books with no title metadata,
// since an empty column in a book list is useless.
func (b *Book) DisplayTitle() string {
	if b.Title != "" {
		return b.Title
	}
	base := filepath.Base(b.Path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// DisplayAuthor returns the best available author string.
func (b *Book) DisplayAuthor() string {
	if len(b.Authors) > 0 {
		return strings.Join(b.Authors, ", ")
	}
	return b.AuthorSort
}

// tagsFlat renders tags for the full-text index.
func (b *Book) tagsFlat() string { return strings.Join(b.Tags, " ") }

// Upsert inserts or updates a book by path, replacing its tags.
func (db *DB) Upsert(b *Book) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := upsertTx(tx, b); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertTx(tx *sql.Tx, b *Book) error {
	b.Path = NormalizePath(b.Path)
	if b.AddedUnix == 0 {
		b.AddedUnix = time.Now().Unix()
	}

	identifiers, err := marshalMap(b.Identifiers)
	if err != nil {
		return err
	}
	authors, err := marshalSlice(b.Authors)
	if err != nil {
		return err
	}

	// Sorting tags makes the flattened form stable, which keeps the FTS
	// triggers from rewriting rows that did not actually change.
	tags := dedupeSorted(b.Tags)
	b.Tags = tags

	const q = `
INSERT INTO books (sha256, path, size, mtime_unix, format, title, author_sort, authors,
                   series, series_index, language, publisher, pubdate, identifiers,
                   added_unix, cover_blob, meta_json, tags_flat)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET
  sha256=excluded.sha256, size=excluded.size, mtime_unix=excluded.mtime_unix,
  format=excluded.format, title=excluded.title, author_sort=excluded.author_sort,
  authors=excluded.authors, series=excluded.series, series_index=excluded.series_index,
  language=excluded.language, publisher=excluded.publisher, pubdate=excluded.pubdate,
  identifiers=excluded.identifiers, cover_blob=excluded.cover_blob,
  meta_json=excluded.meta_json, tags_flat=excluded.tags_flat
RETURNING id`

	var id int64
	err = tx.QueryRow(q,
		b.SHA256, b.Path, b.Size, b.MTimeUnix, string(b.Format), b.Title, b.AuthorSort, authors,
		b.Series, b.SeriesIndex, b.Language, b.Publisher, b.PubDate, identifiers,
		b.AddedUnix, b.Cover, b.MetaJSON, strings.Join(tags, " "),
	).Scan(&id)
	if err != nil {
		return fmt.Errorf("upsert %s: %w", b.Path, err)
	}
	b.ID = id

	if _, err := tx.Exec(`DELETE FROM tags WHERE book_id = ?`, id); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, tag := range tags {
		if _, err := tx.Exec(`INSERT INTO tags (book_id, tag) VALUES (?, ?)`, id, tag); err != nil {
			return fmt.Errorf("insert tag %q: %w", tag, err)
		}
	}
	return nil
}

// ByPath looks up a book by filesystem path.
func (db *DB) ByPath(path string) (*Book, error) {
	return db.queryOne(`SELECT `+bookColumns+` FROM books WHERE path = ?`, NormalizePath(path))
}

// ByID looks up a book by index id.
func (db *DB) ByID(id int64) (*Book, error) {
	return db.queryOne(`SELECT `+bookColumns+` FROM books WHERE id = ?`, id)
}

// BySHA256 returns every book whose content hash matches. More than one result
// means the library holds duplicate files, which is allowed.
func (db *DB) BySHA256(sum string) ([]*Book, error) {
	return db.queryBooks(`SELECT `+bookColumns+` FROM books WHERE sha256 = ? ORDER BY path`, sum)
}

// All returns every indexed book, sorted by author then series then title.
func (db *DB) All() ([]*Book, error) {
	return db.queryBooks(`SELECT ` + bookColumns + ` FROM books ORDER BY ` + defaultOrder)
}

// Delete removes a book from the index by path. Removing a row never touches
// the file: the index is a cache, and only explicit user commands delete books.
func (db *DB) Delete(path string) error {
	res, err := db.sql.Exec(`DELETE FROM books WHERE path = ?`, NormalizePath(path))
	if err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	return nil
}

// PathIndex is the (size, mtime) snapshot a scan compares against to decide
// which files need rehashing.
type PathIndex map[string]PathState

// PathState is what the index knows about a file without opening it.
type PathState struct {
	ID        int64
	Size      int64
	MTimeUnix int64
	SHA256    string
}

// Snapshot loads the stat-level state of every indexed book. Scanning compares
// against this in memory rather than issuing a query per file.
func (db *DB) Snapshot() (PathIndex, error) {
	rows, err := db.sql.Query(`SELECT id, path, size, mtime_unix, sha256 FROM books`)
	if err != nil {
		return nil, fmt.Errorf("snapshot index: %w", err)
	}
	defer rows.Close()

	out := PathIndex{}
	for rows.Next() {
		var path string
		var st PathState
		if err := rows.Scan(&st.ID, &path, &st.Size, &st.MTimeUnix, &st.SHA256); err != nil {
			return nil, err
		}
		out[path] = st
	}
	return out, rows.Err()
}

const bookColumns = `id, sha256, path, size, mtime_unix, format, title, author_sort, authors,
 series, series_index, language, publisher, pubdate, identifiers, added_unix, cover_blob,
 meta_json, tags_flat`

// defaultOrder sorts the way a reader browsing a shelf would expect: by author,
// then within an author by series and series position, then by title.
const defaultOrder = `
 CASE WHEN author_sort IS NULL OR author_sort = '' THEN 1 ELSE 0 END, author_sort COLLATE NOCASE,
 CASE WHEN series IS NULL OR series = '' THEN 1 ELSE 0 END, series COLLATE NOCASE,
 series_index, title COLLATE NOCASE`

func (db *DB) queryOne(q string, args ...any) (*Book, error) {
	books, err := db.queryBooks(q, args...)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, ErrNotFound
	}
	return books[0], nil
}

func (db *DB) queryBooks(q string, args ...any) ([]*Book, error) {
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var out []*Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, db.loadTags(out)
}

func scanBook(rows *sql.Rows) (*Book, error) {
	var (
		b                                                   Book
		format                                              string
		title, authorSort, authors, series, language        sql.NullString
		publisher, pubdate, identifiers, metaJSON, tagsFlat sql.NullString
		seriesIndex                                         sql.NullFloat64
		cover                                               []byte
	)

	if err := rows.Scan(
		&b.ID, &b.SHA256, &b.Path, &b.Size, &b.MTimeUnix, &format, &title, &authorSort, &authors,
		&series, &seriesIndex, &language, &publisher, &pubdate, &identifiers, &b.AddedUnix,
		&cover, &metaJSON, &tagsFlat,
	); err != nil {
		return nil, fmt.Errorf("scan book row: %w", err)
	}

	b.Format = Format(format)
	b.Title = title.String
	b.AuthorSort = authorSort.String
	b.Series = series.String
	b.SeriesIndex = seriesIndex.Float64
	b.Language = language.String
	b.Publisher = publisher.String
	b.PubDate = pubdate.String
	b.MetaJSON = metaJSON.String
	b.Cover = cover

	if err := unmarshalSlice(authors.String, &b.Authors); err != nil {
		return nil, err
	}
	if err := unmarshalMap(identifiers.String, &b.Identifiers); err != nil {
		return nil, err
	}
	return &b, nil
}

// loadTags fills in tags for a result set in one query rather than N.
func (db *DB) loadTags(books []*Book) error {
	if len(books) == 0 {
		return nil
	}

	byID := make(map[int64]*Book, len(books))
	ids := make([]any, 0, len(books))
	placeholders := make([]string, 0, len(books))
	for _, b := range books {
		byID[b.ID] = b
		ids = append(ids, b.ID)
		placeholders = append(placeholders, "?")
	}

	q := `SELECT book_id, tag FROM tags WHERE book_id IN (` +
		strings.Join(placeholders, ",") + `) ORDER BY tag`
	rows, err := db.sql.Query(q, ids...)
	if err != nil {
		return fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		if b := byID[id]; b != nil {
			b.Tags = append(b.Tags, tag)
		}
	}
	return rows.Err()
}

func marshalMap(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode identifiers: %w", err)
	}
	return string(data), nil
}

func unmarshalMap(s string, out *map[string]string) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("decode identifiers: %w", err)
	}
	return nil
}

func marshalSlice(v []string) (string, error) {
	if len(v) == 0 {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode authors: %w", err)
	}
	return string(data), nil
}

func unmarshalSlice(s string, out *[]string) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("decode authors: %w", err)
	}
	return nil
}

// dedupeSorted normalizes, trims, deduplicates, and sorts tags.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(NormalizeString(s))
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
