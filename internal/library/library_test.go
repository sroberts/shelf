package library

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUpsertAndRead(t *testing.T) {
	db := openTestDB(t)

	b := &Book{
		SHA256: "abc123", Path: "/lib/Le Guin/Earthsea.epub", Size: 1024,
		MTimeUnix: 1700000000, Format: FormatEPUB,
		Title: "A Wizard of Earthsea", AuthorSort: "Le Guin, Ursula K.",
		Authors: []string{"Ursula K. Le Guin"},
		Series:  "Earthsea", SeriesIndex: 1,
		Identifiers: map[string]string{"isbn": "9780553383041"},
		Tags:        []string{"fantasy", "classics"},
	}
	if err := db.Upsert(b); err != nil {
		t.Fatal(err)
	}
	if b.ID == 0 {
		t.Fatal("Upsert did not assign an id")
	}

	got, err := db.ByPath("/lib/Le Guin/Earthsea.epub")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != b.Title || got.Series != "Earthsea" || got.SeriesIndex != 1 {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if !reflect.DeepEqual(got.Authors, []string{"Ursula K. Le Guin"}) {
		t.Errorf("Authors = %v", got.Authors)
	}
	if got.Identifiers["isbn"] != "9780553383041" {
		t.Errorf("Identifiers = %v", got.Identifiers)
	}
	// Tags come back sorted and deduplicated.
	if !reflect.DeepEqual(got.Tags, []string{"classics", "fantasy"}) {
		t.Errorf("Tags = %v, want sorted", got.Tags)
	}

	// Upserting the same path updates rather than duplicating.
	b.Title = "Changed"
	b.Tags = []string{"fantasy"}
	if err := db.Upsert(b); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.Count(); n != 1 {
		t.Errorf("Count = %d after re-upsert, want 1", n)
	}
	got, _ = db.ByPath("/lib/Le Guin/Earthsea.epub")
	if got.Title != "Changed" {
		t.Errorf("Title = %q", got.Title)
	}
	if !reflect.DeepEqual(got.Tags, []string{"fantasy"}) {
		t.Errorf("Tags = %v, want the replacement set", got.Tags)
	}
}

// The same file may legitimately live at two paths. The index must allow it and
// be able to report it.
func TestDuplicateHashesAllowed(t *testing.T) {
	db := openTestDB(t)

	for _, p := range []string{"/lib/a/Book.epub", "/lib/b/Book.epub"} {
		if err := db.Upsert(&Book{
			SHA256: "samehash", Path: p, Size: 10, Format: FormatEPUB, Title: "Book",
		}); err != nil {
			t.Fatalf("the index must permit duplicate content: %v", err)
		}
	}

	dupes, err := db.Duplicates()
	if err != nil {
		t.Fatal(err)
	}
	if len(dupes) != 1 || len(dupes[0]) != 2 {
		t.Errorf("Duplicates = %v, want one group of two", dupes)
	}
}

func TestSearch(t *testing.T) {
	db := openTestDB(t)

	books := []*Book{
		{SHA256: "1", Path: "/lib/Le Guin/Earthsea.epub", Format: FormatEPUB,
			Title: "A Wizard of Earthsea", AuthorSort: "Le Guin, Ursula K.",
			Authors: []string{"Ursula K. Le Guin"}, Series: "Earthsea", SeriesIndex: 1,
			Language: "en", Tags: []string{"fantasy", "queue"}},
		{SHA256: "2", Path: "/lib/Le Guin/Dispossessed.epub", Format: FormatEPUB,
			Title: "The Dispossessed", AuthorSort: "Le Guin, Ursula K.",
			Authors: []string{"Ursula K. Le Guin"}, Series: "Hainish", SeriesIndex: 5,
			Language: "en", Tags: []string{"scifi", "done"}},
		{SHA256: "3", Path: "/lib/Melville/Moby-Dick.epub", Format: FormatEPUB,
			Title: "Moby-Dick", AuthorSort: "Melville, Herman",
			Authors: []string{"Herman Melville"}, Language: "en", Tags: []string{"classics"}},
		{SHA256: "4", Path: "/lib/Papers/thesis.pdf", Format: FormatPDF,
			Title: "A Thesis", AuthorSort: "Student, A."},
	}
	for _, b := range books {
		if err := db.Upsert(b); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"A Wizard of Earthsea", "The Dispossessed", "Moby-Dick", "A Thesis"}},
		{"earthsea", []string{"A Wizard of Earthsea"}},
		{"earth", []string{"A Wizard of Earthsea"}}, // prefix matching
		{"moby", []string{"Moby-Dick"}},
		{"tag:queue", []string{"A Wizard of Earthsea"}},
		{"tag:queue and not tag:done", []string{"A Wizard of Earthsea"}},
		{"tag:fantasy or tag:classics", []string{"A Wizard of Earthsea", "Moby-Dick"}},
		{"not tag:queue", []string{"The Dispossessed", "Moby-Dick", "A Thesis"}},
		{`author:"le guin"`, []string{"A Wizard of Earthsea", "The Dispossessed"}},
		{"series:Earthsea", []string{"A Wizard of Earthsea"}},
		{"format:pdf", []string{"A Thesis"}},
		{"format:epub and tag:scifi", []string{"The Dispossessed"}},
		{"lang:en and tag:classics", []string{"Moby-Dick"}},
		{"(tag:queue or tag:done) and author:guin",
			[]string{"A Wizard of Earthsea", "The Dispossessed"}},
		{"path:Melville", []string{"Moby-Dick"}},
		{"nonexistentterm", nil},
		{"tag:nosuchtag", nil},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, err := db.Search(tt.query, SearchOptions{})
			if err != nil {
				t.Fatalf("Search(%q): %v", tt.query, err)
			}
			var titles []string
			for _, b := range got {
				titles = append(titles, b.Title)
			}
			if !sameSet(titles, tt.want) {
				t.Errorf("Search(%q) = %v, want %v", tt.query, titles, tt.want)
			}
		})
	}
}

// Search terms must never be able to break out into SQL or FTS5 syntax.
func TestSearchHandlesHostileInput(t *testing.T) {
	db := openTestDB(t)
	if err := db.Upsert(&Book{
		SHA256: "1", Path: "/lib/a.epub", Format: FormatEPUB, Title: "100% Pure",
	}); err != nil {
		t.Fatal(err)
	}

	// Rejecting a malformed query is a safe outcome; the property under test is
	// that nothing reaches SQL or FTS5 as syntax. A panic or a mutated database
	// is the failure mode, not an error return.
	for _, q := range []string{
		`'; DROP TABLE books; --`,
		`"unbalanced`,
		`%`,
		`_`,
		`title:%`,
		`OR 1=1`,
		`NEAR("a" "b")`,
		`*`,
		`title:'; DELETE FROM books; --`,
		`tag:x') OR ('1'='1`,
	} {
		if _, err := db.Search(q, SearchOptions{}); err != nil {
			t.Logf("Search(%q) rejected: %v", q, err)
		}
	}

	// The table must still exist.
	if n, err := db.Count(); err != nil || n != 1 {
		t.Errorf("Count = %d, %v; the books table should be intact", n, err)
	}

	// A literal % must not act as a wildcard.
	got, err := db.Search("title:%", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "100% Pure" {
		t.Errorf("literal %% search = %v, want the one book containing a percent sign", got)
	}
}

func TestSearchOrdering(t *testing.T) {
	db := openTestDB(t)
	for i, b := range []*Book{
		{SHA256: "1", Path: "/z.epub", Format: FormatEPUB, Title: "Zebra", AuthorSort: "Zulu", Size: 300},
		{SHA256: "2", Path: "/a.epub", Format: FormatEPUB, Title: "Apple", AuthorSort: "Alpha", Size: 100},
		{SHA256: "3", Path: "/m.epub", Format: FormatEPUB, Title: "Mango", AuthorSort: "Mike", Size: 200},
	} {
		b.AddedUnix = int64(1700000000 + i)
		if err := db.Upsert(b); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		order string
		want  []string
	}{
		{"title", []string{"Apple", "Mango", "Zebra"}},
		{"title-desc", []string{"Zebra", "Mango", "Apple"}},
		{"size", []string{"Apple", "Mango", "Zebra"}},
		{"size-desc", []string{"Zebra", "Mango", "Apple"}},
		{"", []string{"Apple", "Mango", "Zebra"}}, // default: by author
	}
	for _, tt := range tests {
		got, err := db.Search("", SearchOptions{OrderBy: tt.order})
		if err != nil {
			t.Fatalf("order %q: %v", tt.order, err)
		}
		var titles []string
		for _, b := range got {
			titles = append(titles, b.Title)
		}
		if !reflect.DeepEqual(titles, tt.want) {
			t.Errorf("order %q = %v, want %v", tt.order, titles, tt.want)
		}
	}

	if _, err := db.Search("", SearchOptions{OrderBy: "path; DROP TABLE books"}); err == nil {
		t.Error("an unknown sort key must be rejected, not interpolated")
	}
}

func TestSearchLimitOffset(t *testing.T) {
	db := openTestDB(t)
	for i := 0; i < 10; i++ {
		if err := db.Upsert(&Book{
			SHA256: fmt.Sprint(i), Path: fmt.Sprintf("/b%02d.epub", i),
			Format: FormatEPUB, Title: fmt.Sprintf("Book %02d", i), AuthorSort: fmt.Sprintf("A%02d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.Search("", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Title != "Book 00" {
		t.Errorf("limit: got %d books starting at %q", len(got), got[0].Title)
	}

	got, err = db.Search("", SearchOptions{Limit: 3, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Title != "Book 03" {
		t.Errorf("offset: got %d books starting at %q", len(got), got[0].Title)
	}
}

func TestDeleteRemovesTagsAndFTS(t *testing.T) {
	db := openTestDB(t)
	if err := db.Upsert(&Book{
		SHA256: "1", Path: "/lib/a.epub", Format: FormatEPUB,
		Title: "Findable", Tags: []string{"gone"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Delete("/lib/a.epub"); err != nil {
		t.Fatal(err)
	}

	// The full-text index and the tags table must not retain the row.
	if got, _ := db.Search("findable", SearchOptions{}); len(got) != 0 {
		t.Errorf("deleted book still in the full-text index: %v", got)
	}
	if got, _ := db.Search("tag:gone", SearchOptions{}); len(got) != 0 {
		t.Errorf("deleted book still has tags: %v", got)
	}
	if tags, _ := db.Tags(); len(tags) != 0 {
		t.Errorf("orphaned tags remain: %v", tags)
	}

	if err := db.Delete("/lib/a.epub"); err == nil {
		t.Error("deleting a missing book should error")
	}
}

// Updating a book must keep the full-text index in step with the content table.
func TestFTSStaysInSyncOnUpdate(t *testing.T) {
	db := openTestDB(t)
	b := &Book{SHA256: "1", Path: "/lib/a.epub", Format: FormatEPUB, Title: "Original Title"}
	if err := db.Upsert(b); err != nil {
		t.Fatal(err)
	}

	b.Title = "Replacement Title"
	if err := db.Upsert(b); err != nil {
		t.Fatal(err)
	}

	if got, _ := db.Search("original", SearchOptions{}); len(got) != 0 {
		t.Errorf("stale term still matches: %v", got)
	}
	if got, _ := db.Search("replacement", SearchOptions{}); len(got) != 1 {
		t.Errorf("new term does not match: %v", got)
	}
}

func TestOpenRebuildsCorruptIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The index is derived data, so a corrupt file is recreated rather than
	// reported as a fatal error.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open should rebuild a corrupt index, got: %v", err)
	}
	defer db.Close()

	if n, err := db.Count(); err != nil || n != 0 {
		t.Errorf("Count = %d, %v; want an empty, usable index", n, err)
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "index.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("index file was not created: %v", err)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	remaining := make(map[string]int, len(want))
	for _, w := range want {
		remaining[w]++
	}
	for _, g := range got {
		remaining[g]--
		if remaining[g] < 0 {
			return false
		}
	}
	return true
}

// --- fixtures ---

// writeEPUB creates a minimal valid EPUB with the given metadata.
func writeEPUB(t *testing.T, path, title, author, series string, seriesIndex int) {
	t.Helper()

	seriesMeta := ""
	if series != "" {
		seriesMeta = fmt.Sprintf(
			"\n    <meta name=\"calibre:series\" content=%q/>\n    <meta name=\"calibre:series_index\" content=\"%d\"/>",
			series, seriesIndex)
	}
	opf := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>%s</dc:title>
    <dc:creator opf:role="aut">%s</dc:creator>
    <dc:identifier id="id">urn:uuid:%s</dc:identifier>
    <dc:language>en</dc:language>%s
  </metadata>
  <manifest><item id="c1" href="c1.html" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="c1"/></spine>
</package>`, title, author, title, seriesMeta)

	container := `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mt := []byte("application/epub+zip")
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "mimetype", Method: zip.Store, CRC32: crc32.ChecksumIEEE(mt),
		CompressedSize64: uint64(len(mt)), UncompressedSize64: uint64(len(mt)),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(mt)

	for name, body := range map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf":      opf,
		"OEBPS/c1.html":          "<html><body><p>text</p></body></html>",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "Le Guin", "Earthsea.epub"), "A Wizard of Earthsea", "Ursula K. Le Guin", "Earthsea", 1)
	writeEPUB(t, filepath.Join(root, "Melville", "Moby-Dick.epub"), "Moby-Dick", "Herman Melville", "", 0)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("plain text book"), 0o644)
	os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("not a book"), 0o644)

	db := openTestDB(t)
	res, err := db.Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if res.Added != 3 { // two epubs plus the txt
		t.Errorf("Added = %d, want 3 (errors: %v)", res.Added, res.Errors)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the jpg)", res.Skipped)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors: %v", res.Errors)
	}

	got, err := db.Search("earthsea", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("search after scan returned %d books", len(got))
	}
	if got[0].AuthorSort != "Le Guin, Ursula K." {
		t.Errorf("AuthorSort = %q; the sort form should be derived", got[0].AuthorSort)
	}
	if got[0].Series != "Earthsea" || got[0].SeriesIndex != 1 {
		t.Errorf("series = %q/%v", got[0].Series, got[0].SeriesIndex)
	}
	if got[0].SHA256 == "" {
		t.Error("SHA256 was not computed")
	}
}

// A rescan with nothing changed must not re-read any file.
func TestScanIsIncremental(t *testing.T) {
	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "a.epub"), "Book A", "Author A", "", 0)
	writeEPUB(t, filepath.Join(root, "b.epub"), "Book B", "Author B", "", 0)

	db := openTestDB(t)
	if _, err := db.Scan(context.Background(), root, ScanOptions{}); err != nil {
		t.Fatal(err)
	}

	res, err := db.Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Unchanged != 2 || res.Added != 0 || res.Updated != 0 {
		t.Errorf("second scan: added=%d updated=%d unchanged=%d, want 0/0/2",
			res.Added, res.Updated, res.Unchanged)
	}
}

func TestScanDetectsChangesAndDeletions(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "a.epub")
	pathB := filepath.Join(root, "b.epub")
	writeEPUB(t, pathA, "Book A", "Author A", "", 0)
	writeEPUB(t, pathB, "Book B", "Author B", "", 0)

	db := openTestDB(t)
	if _, err := db.Scan(context.Background(), root, ScanOptions{}); err != nil {
		t.Fatal(err)
	}

	// Rewrite one book with different metadata and remove the other.
	writeEPUB(t, pathA, "Book A Revised", "Author A", "", 0)
	if err := os.Remove(pathB); err != nil {
		t.Fatal(err)
	}

	res, err := db.Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Errorf("Updated = %d, want 1", res.Updated)
	}
	if res.Removed != 1 {
		t.Errorf("Removed = %d, want 1", res.Removed)
	}

	if got, _ := db.Search("revised", SearchOptions{}); len(got) != 1 {
		t.Error("the updated title is not searchable")
	}
	if _, err := db.ByPath(pathB); err == nil {
		t.Error("the deleted book is still indexed")
	}
}

// A corrupt book must be indexed under its filename, not skipped entirely.
func TestScanToleratesUnreadableBooks(t *testing.T) {
	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "good.epub"), "Good Book", "Author", "", 0)
	os.WriteFile(filepath.Join(root, "broken.epub"), []byte("not a zip file"), 0o644)

	db := openTestDB(t)
	res, err := db.Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Errorf("Added = %d, want 2; a corrupt book should still be indexed", res.Added)
	}

	b, err := db.ByPath(filepath.Join(root, "broken.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "broken" {
		t.Errorf("Title = %q, want the filename fallback", b.Title)
	}
}

func TestScanSkipsHiddenAndVCSDirectories(t *testing.T) {
	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "visible.epub"), "Visible", "Author", "", 0)
	writeEPUB(t, filepath.Join(root, ".hidden", "secret.epub"), "Secret", "Author", "", 0)
	writeEPUB(t, filepath.Join(root, ".git", "obj.epub"), "Git Object", "Author", "", 0)

	db := openTestDB(t)
	res, err := db.Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("Added = %d, want 1", res.Added)
	}
}

func TestScanRespectsContextCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		writeEPUB(t, filepath.Join(root, fmt.Sprintf("b%02d.epub", i)), fmt.Sprintf("Book %d", i), "Author", "", 0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db := openTestDB(t)
	if _, err := db.Scan(ctx, root, ScanOptions{}); err == nil {
		t.Error("a cancelled scan should return an error")
	}
}

func TestScanExtractsCovers(t *testing.T) {
	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "a.epub"), "Book A", "Author", "", 0)

	db := openTestDB(t)
	if _, err := db.Scan(context.Background(), root, ScanOptions{Covers: true}); err != nil {
		t.Fatal(err)
	}
	// The fixture has no cover; the scan must succeed anyway.
	b, err := db.ByPath(filepath.Join(root, "a.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Cover != nil {
		t.Errorf("expected no cover for a book without one")
	}
}

func TestScanRejectsBadRoot(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.Scan(context.Background(), filepath.Join(t.TempDir(), "nope"), ScanOptions{}); err == nil {
		t.Error("expected an error for a missing root")
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	if _, err := db.Scan(context.Background(), file, ScanOptions{}); err == nil {
		t.Error("expected an error when the root is a file")
	}
}

func TestFormatOf(t *testing.T) {
	tests := []struct {
		path string
		want Format
		ok   bool
	}{
		{"a.epub", FormatEPUB, true},
		{"a.EPUB", FormatEPUB, true},
		{"a.txt", FormatTXT, true},
		{"a.xtc", FormatXTC, true},
		{"a.xtch", FormatXTC, true},
		{"a.pdf", FormatPDF, true},
		{"a.mobi", "", false},
		{"a", "", false},
	}
	for _, tt := range tests {
		got, ok := FormatOf(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("FormatOf(%q) = %q,%v want %q,%v", tt.path, got, ok, tt.want, tt.ok)
		}
	}

	// PDF is a source format only; the firmware has no PDF engine.
	if FormatPDF.SyncableToDevice() {
		t.Error("PDF must not be syncable to a device")
	}
	for _, f := range []Format{FormatEPUB, FormatTXT, FormatXTC} {
		if !f.SyncableToDevice() {
			t.Errorf("%s should be syncable", f)
		}
	}
}

func TestNormalizePathIsIdempotentAcrossForms(t *testing.T) {
	// "é" as a single composed rune versus "e" plus a combining accent. macOS
	// hands back the decomposed form; Linux usually stores the composed one.
	composed := "/lib/Hervé/Book.epub"
	decomposed := "/lib/Hervé/Book.epub"

	if composed == decomposed {
		t.Fatal("test fixture is wrong: the two forms should differ as bytes")
	}
	if NormalizePath(composed) != NormalizePath(decomposed) {
		t.Errorf("NFC normalization failed:\n %q\n %q",
			NormalizePath(composed), NormalizePath(decomposed))
	}
	if NormalizePath(NormalizePath(composed)) != NormalizePath(composed) {
		t.Error("NormalizePath is not idempotent")
	}
}

// The index must not grow a second row for the same book when the same library
// is scanned from a machine that reports filenames in the other Unicode form.
func TestIndexTreatsUnicodeFormsAsOneBook(t *testing.T) {
	db := openTestDB(t)

	composed := "/lib/Hervé/Book.epub"
	decomposed := "/lib/Hervé/Book.epub"

	if err := db.Upsert(&Book{SHA256: "1", Path: composed, Format: FormatEPUB, Title: "Book"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert(&Book{SHA256: "1", Path: decomposed, Format: FormatEPUB, Title: "Book"}); err != nil {
		t.Fatal(err)
	}

	if n, _ := db.Count(); n != 1 {
		t.Errorf("Count = %d, want 1; the two Unicode forms are the same book", n)
	}
	if _, err := db.ByPath(decomposed); err != nil {
		t.Errorf("lookup by the decomposed form failed: %v", err)
	}
}

func TestFoldPathDetectsCaseCollisions(t *testing.T) {
	// The device's SD card is FAT32 and case-insensitive regardless of host.
	if FoldPath("/Books/Moby-Dick.epub") != FoldPath("/books/moby-dick.EPUB") {
		t.Error("FoldPath should treat case variants as colliding")
	}
	if !SameFile("/a/b", "/a/./b") {
		t.Error("SameFile should ignore redundant path elements")
	}
}

func TestRenderTemplate(t *testing.T) {
	tmpl := "{author}/{series} {series_index:02d} - {title}"

	tests := []struct {
		name string
		book *Book
		want string
	}{
		{
			name: "series book",
			book: &Book{Authors: []string{"Ursula K. Le Guin"}, AuthorSort: "Le Guin, Ursula K.",
				Title: "A Wizard of Earthsea", Series: "Earthsea", SeriesIndex: 1},
			want: "Ursula K. Le Guin/Earthsea 01 - A Wizard of Earthsea",
		},
		{
			// With no series, the series segment must collapse rather than
			// leaving " 00 - " behind.
			name: "standalone book",
			book: &Book{Authors: []string{"Herman Melville"}, Title: "Moby-Dick"},
			want: "Herman Melville/Moby-Dick",
		},
		{
			name: "reserved characters are replaced",
			book: &Book{Authors: []string{"A/B: C"}, Title: `What? "Yes"`},
			want: "A_B_ C/What_ _Yes_",
		},
		{
			name: "missing author falls back to sort form",
			book: &Book{AuthorSort: "Anonymous", Title: "A Book"},
			want: "Anonymous/A Book",
		},
		{
			name: "double-digit series index",
			book: &Book{Authors: []string{"A"}, Title: "T", Series: "S", SeriesIndex: 12},
			want: "A/S 12 - T",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTemplate(tmpl, tt.book)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}

	if _, err := RenderTemplate("{nosuchfield}", &Book{Title: "T"}); err == nil {
		t.Error("expected an error for an unknown template field")
	}
	if _, err := RenderTemplate("", &Book{Title: "T"}); err == nil {
		t.Error("expected an error for an empty template")
	}
}

func TestSanitizeComponent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Normal Name", "Normal Name"},
		{`bad<>:"/\|?*chars`, "bad_________chars"},
		{"trailing dots...", "trailing dots"},
		{"trailing spaces   ", "trailing spaces"},
		{"  collapse   spaces  ", "collapse spaces"},
		{"CON", "CON_"},  // reserved DOS device name
		{"nul", "nul_"},  // case-insensitively reserved
		{"", "untitled"}, // never produce an empty component
		{"control\x01char", "controlchar"},
	}
	for _, tt := range tests {
		if got := SanitizeComponent(tt.in); got != tt.want {
			t.Errorf("SanitizeComponent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// Long names are truncated to the FAT32 limit without splitting a rune.
	long := strings.Repeat("é", 300)
	got := SanitizeComponent(long)
	if len(got) > maxComponent {
		t.Errorf("length = %d bytes, want <= %d", len(got), maxComponent)
	}
	if !isValidUTF8(got) {
		t.Error("truncation split a multi-byte rune")
	}
}

func TestValidFATName(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"Moby-Dick.epub", true},
		{"", false},
		{"has:colon.epub", false},
		{"trailing.", false},
		{"trailing ", false},
		{"CON", false},
		{strings.Repeat("a", 256), false},
		{"control\x01.epub", false},
	}
	for _, tt := range tests {
		got, reason := ValidFATName(tt.in)
		if got != tt.want {
			t.Errorf("ValidFATName(%q) = %v (%s), want %v", tt.in, got, reason, tt.want)
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
