package library

import (
	"fmt"
	"strings"
)

// SearchOptions tunes a search.
type SearchOptions struct {
	Limit  int
	Offset int
	// OrderBy overrides the default author/series/title ordering.
	OrderBy string

	// Filter narrows to an exact value of one field.
	//
	// Separate from the query text on purpose. Browsing by author or tag means
	// filtering on a value that came out of book metadata, and the query lexer
	// ends a quoted string at the first matching quote with no escape — an
	// author like O'Brien "Bob" cannot be expressed in query syntax at all.
	// Binding the value as a parameter sidesteps the question.
	Filter *Filter
}

// Filter is an exact-match predicate on one field.
type Filter struct {
	Field FilterField
	Value string
}

// FilterField names the fields an exact filter can apply to. A closed set,
// because each one compiles to a hand-written SQL predicate.
type FilterField string

const (
	FilterAuthor FilterField = "author"
	FilterSeries FilterField = "series"
	FilterTag    FilterField = "tag"
)

// compile renders the filter as a SQL condition and its argument.
func (f Filter) compile() (string, any, error) {
	switch f.Field {
	case FilterAuthor:
		return "author_sort = ? COLLATE NOCASE", f.Value, nil
	case FilterSeries:
		return "series = ? COLLATE NOCASE", f.Value, nil
	case FilterTag:
		return "EXISTS (SELECT 1 FROM tags WHERE tags.book_id = books.id AND tags.tag = ? COLLATE NOCASE)",
			f.Value, nil
	default:
		return "", nil, fmt.Errorf("cannot filter on %q", f.Field)
	}
}

// Search returns books matching a query string.
//
// An empty query returns the whole library, which is what `shelf ls` with no
// arguments should do.
func (db *DB) Search(query string, opts SearchOptions) ([]*Book, error) {
	where, args, err := CompileQuery(query)
	if err != nil {
		return nil, err
	}

	if opts.Filter != nil {
		cond, arg, err := opts.Filter.compile()
		if err != nil {
			return nil, err
		}
		where = "(" + where + ") AND " + cond
		args = append(args, arg)
	}

	order := defaultOrder
	if opts.OrderBy != "" {
		col, ok := orderColumn(opts.OrderBy)
		if !ok {
			return nil, fmt.Errorf("cannot sort by %q", opts.OrderBy)
		}
		order = col
	}

	sql := `SELECT ` + bookColumns + ` FROM books WHERE ` + where + ` ORDER BY ` + order
	if opts.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", opts.Limit)
		if opts.Offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", opts.Offset)
		}
	}
	return db.queryBooks(sql, args...)
}

// CompileQuery turns a query string into a SQL condition and its arguments.
// Exported so the sync planner can resolve a shelf's saved query without
// duplicating the parser.
func CompileQuery(query string) (string, []any, error) {
	if strings.TrimSpace(query) == "" {
		return "1=1", nil, nil
	}

	root, err := parseQuery(query)
	if err != nil {
		return "", nil, fmt.Errorf("invalid query %q: %w", query, err)
	}

	var c condBuilder
	root.compile(&c)
	return c.String(), c.args, nil
}

// orderColumn maps a user-facing sort name to a safe SQL fragment. Sort keys
// are interpolated into SQL, so they come from this allowlist and never from
// user input directly.
func orderColumn(name string) (string, bool) {
	desc := strings.HasSuffix(name, "-desc")
	name = strings.TrimSuffix(name, "-desc")

	var col string
	switch strings.ToLower(name) {
	case "title":
		col = "title COLLATE NOCASE"
	case "author":
		col = "author_sort COLLATE NOCASE"
	case "series":
		col = "series COLLATE NOCASE, series_index"
	case "size":
		col = "size"
	case "added":
		col = "added_unix"
	case "modified", "mtime":
		col = "mtime_unix"
	case "path":
		col = "path COLLATE NOCASE"
	case "format":
		col = "format"
	default:
		return "", false
	}

	if desc {
		col += " DESC"
	}
	return col, true
}

// Tags returns every tag in the library with its book count, most used first.
func (db *DB) Tags() ([]TagCount, error) {
	rows, err := db.sql.Query(
		`SELECT tag, count(*) AS n FROM tags GROUP BY tag ORDER BY n DESC, tag COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// TagCount is a tag and how many books carry it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// GroupCount is one value of a browse dimension and how many books share it.
type GroupCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Authors returns every author_sort in the library with its book count.
//
// Grouped on author_sort rather than the authors list: a book with two authors
// has one sort key, and browsing wants one row per shelf-neighbour, not one per
// contributor. Books with no author at all are left out — an "" heading is a
// dead end to click on.
func (db *DB) Authors() ([]GroupCount, error) {
	return db.groupCounts(`
SELECT author_sort, count(*) AS n FROM books
WHERE author_sort IS NOT NULL AND author_sort != ''
GROUP BY author_sort COLLATE NOCASE
ORDER BY author_sort COLLATE NOCASE`)
}

// Series returns every series in the library with its book count.
func (db *DB) Series() ([]GroupCount, error) {
	return db.groupCounts(`
SELECT series, count(*) AS n FROM books
WHERE series IS NOT NULL AND series != ''
GROUP BY series COLLATE NOCASE
ORDER BY series COLLATE NOCASE`)
}

func (db *DB) groupCounts(q string) ([]GroupCount, error) {
	rows, err := db.sql.Query(q)
	if err != nil {
		return nil, fmt.Errorf("group books: %w", err)
	}
	defer rows.Close()

	var out []GroupCount
	for rows.Next() {
		var g GroupCount
		if err := rows.Scan(&g.Value, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Stats summarises the whole library.
//
// One call rather than five, because the TUI header redraws on every keystroke
// and a header that costs a round of separate queries per frame is a header
// that makes scrolling feel slow.
type Stats struct {
	Books   int            `json:"books"`
	Authors int            `json:"authors"`
	Series  int            `json:"series"`
	Tags    int            `json:"tags"`
	Bytes   int64          `json:"bytes"`
	Formats map[Format]int `json:"formats"`
}

// Stats computes the library summary.
func (db *DB) Stats() (Stats, error) {
	s := Stats{Formats: map[Format]int{}}

	// COUNT(DISTINCT …) over a nullable column ignores NULL, and the empty
	// string is filtered out separately: a book with no series is not a series
	// called "".
	err := db.sql.QueryRow(`
SELECT
  count(*),
  coalesce(sum(size), 0),
  count(DISTINCT CASE WHEN author_sort  != '' THEN author_sort END),
  count(DISTINCT CASE WHEN series != '' THEN series END)
FROM books`).Scan(&s.Books, &s.Bytes, &s.Authors, &s.Series)
	if err != nil {
		return s, fmt.Errorf("library stats: %w", err)
	}

	if err := db.sql.QueryRow(`SELECT count(DISTINCT tag) FROM tags`).Scan(&s.Tags); err != nil {
		return s, fmt.Errorf("library stats: count tags: %w", err)
	}

	rows, err := db.sql.Query(`SELECT format, count(*) FROM books GROUP BY format`)
	if err != nil {
		return s, fmt.Errorf("library stats: count formats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var f Format
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			return s, err
		}
		s.Formats[f] = n
	}
	return s, rows.Err()
}

// Duplicates returns groups of books that share a content hash.
//
// The index deliberately allows the same file to exist at several paths, so
// this reports them rather than the schema forbidding them.
func (db *DB) Duplicates() ([][]*Book, error) {
	rows, err := db.sql.Query(
		`SELECT sha256 FROM books GROUP BY sha256 HAVING count(*) > 1 ORDER BY sha256`)
	if err != nil {
		return nil, fmt.Errorf("find duplicates: %w", err)
	}
	defer rows.Close()

	var sums []string
	for rows.Next() {
		var sum string
		if err := rows.Scan(&sum); err != nil {
			return nil, err
		}
		sums = append(sums, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out [][]*Book
	for _, sum := range sums {
		group, err := db.BySHA256(sum)
		if err != nil {
			return nil, err
		}
		out = append(out, group)
	}
	return out, nil
}
