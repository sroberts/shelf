package library

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFTS5Available guards the single assumption the whole search design rests
// on: that the pure-Go SQLite driver ships with FTS5 compiled in. If this ever
// fails, search has to be redesigned, so fail loudly and early rather than
// discovering it through a confusing runtime error in scan.
func TestFTS5Available(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var opts string
	rows, err := db.Query("PRAGMA compile_options")
	if err != nil {
		t.Fatalf("compile_options: %v", err)
	}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			t.Fatal(err)
		}
		opts += o + " "
	}
	rows.Close()
	t.Logf("compile options: %s", opts)

	// The authoritative check is whether the module actually builds.
	if _, err := db.Exec(`CREATE VIRTUAL TABLE t USING fts5(title, author)`); err != nil {
		t.Fatalf("FTS5 unavailable in modernc.org/sqlite: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('A Wizard of Earthsea', 'Le Guin')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got string
	if err := db.QueryRow(`SELECT title FROM t WHERE t MATCH 'earthsea'`).Scan(&got); err != nil {
		t.Fatalf("MATCH query: %v", err)
	}
	if got != "A Wizard of Earthsea" {
		t.Fatalf("got %q", got)
	}
}

// TestFTS5ExternalContentNeedsRealColumns pins down the spec defect: an
// external-content FTS5 table is happy to be CREATEd with a column the content
// table does not have, and only fails later when it reads through to the
// content table. This is why books carries a denormalized tags_flat column
// instead of trying to index the separate tags table.
func TestFTS5ExternalContentNeedsRealColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, author_sort TEXT)`)
	mustExec(t, db, `INSERT INTO books VALUES (1, 'A Wizard of Earthsea', 'Le Guin, Ursula K.')`)

	// "tags" is not a column on books -- exactly what spec section 4.2 declares.
	// CREATE succeeds, which is what makes this defect easy to ship by accident.
	mustExec(t, db, `CREATE VIRTUAL TABLE bad USING fts5(title, author_sort, tags, content='books')`)

	if _, err := db.Exec(`INSERT INTO bad(bad) VALUES ('rebuild')`); err == nil {
		t.Fatal("expected the spec's schema to fail on rebuild, but it succeeded")
	} else {
		t.Logf("confirmed spec defect at rebuild: %v", err)
	}
}

// TestFTS5TagsFlatWorks proves the fix: denormalize tags onto the content table
// and the external-content index rebuilds and matches correctly.
func TestFTS5TagsFlatWorks(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE books (
		id INTEGER PRIMARY KEY, title TEXT, author_sort TEXT, series TEXT, tags_flat TEXT)`)
	mustExec(t, db, `INSERT INTO books VALUES
		(1, 'A Wizard of Earthsea', 'Le Guin, Ursula K.', 'Earthsea', 'fantasy queue')`)
	mustExec(t, db, `CREATE VIRTUAL TABLE books_fts USING fts5(
		title, author_sort, series, tags_flat, content='books')`)
	mustExec(t, db, `INSERT INTO books_fts(books_fts) VALUES ('rebuild')`)

	for _, q := range []string{"earthsea", "guin", "queue"} {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM books_fts WHERE books_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatalf("MATCH %q: %v", q, err)
		}
		if n != 1 {
			t.Errorf("MATCH %q: got %d rows, want 1", q, n)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
