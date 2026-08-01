package library

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShelfCRUD(t *testing.T) {
	db := openTestDB(t)

	if err := db.CreateShelf(Shelf{Name: "queue", Kind: KindQuery, Query: "tag:queue and not tag:done"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateShelf(Shelf{Name: "favorites", Kind: KindManual}); err != nil {
		t.Fatal(err)
	}

	names, err := db.ShelfNames()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"favorites", "queue"}) {
		t.Errorf("ShelfNames = %v", names)
	}

	s, err := db.GetShelf("queue")
	if err != nil {
		t.Fatal(err)
	}
	if s.Query != "tag:queue and not tag:done" || s.Kind != KindQuery {
		t.Errorf("shelf = %+v", s)
	}

	if err := db.DeleteShelf("queue"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetShelf("queue"); err == nil {
		t.Error("expected the shelf to be gone")
	}
	if err := db.DeleteShelf("nosuchshelf"); err == nil {
		t.Error("expected an error deleting a missing shelf")
	}
}

// An unparseable query must be rejected when the shelf is created, not later
// during a sync where it would interrupt a transfer.
func TestCreateShelfRejectsBadQuery(t *testing.T) {
	db := openTestDB(t)

	err := db.CreateShelf(Shelf{Name: "broken", Kind: KindQuery, Query: "tag:"})
	if err == nil {
		t.Fatal("expected an error for an invalid query")
	}
	if err := db.CreateShelf(Shelf{Name: "", Kind: KindQuery}); err == nil {
		t.Error("expected an error for an empty shelf name")
	}
}

func TestQueryShelfIsLive(t *testing.T) {
	db := openTestDB(t)

	if err := db.CreateShelf(Shelf{Name: "queue", Kind: KindQuery, Query: "tag:queue"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert(&Book{
		SHA256: "1", Path: "/lib/a.epub", Format: FormatEPUB, Title: "A", Tags: []string{"queue"},
	}); err != nil {
		t.Fatal(err)
	}

	books, err := db.ShelfBooks("queue")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}

	// A saved query reflects the library as it is now, so adding a matching
	// book later must change the shelf without touching the shelf itself.
	if err := db.Upsert(&Book{
		SHA256: "2", Path: "/lib/b.epub", Format: FormatEPUB, Title: "B", Tags: []string{"queue"},
	}); err != nil {
		t.Fatal(err)
	}
	books, _ = db.ShelfBooks("queue")
	if len(books) != 2 {
		t.Errorf("got %d books, want 2 after adding a matching book", len(books))
	}
}

func TestManualShelfMembership(t *testing.T) {
	db := openTestDB(t)

	if err := db.CreateShelf(Shelf{Name: "favorites", Kind: KindManual}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/lib/a.epub", "/lib/b.epub"} {
		if err := db.Upsert(&Book{SHA256: p, Path: p, Format: FormatEPUB, Title: p}); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.AddToShelf("favorites", []string{"/lib/a.epub"}); err != nil {
		t.Fatal(err)
	}
	books, _ := db.ShelfBooks("favorites")
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}

	// Adding twice must not duplicate.
	if err := db.AddToShelf("favorites", []string{"/lib/a.epub"}); err != nil {
		t.Fatal(err)
	}
	if books, _ := db.ShelfBooks("favorites"); len(books) != 1 {
		t.Errorf("got %d books after a duplicate add, want 1", len(books))
	}

	if err := db.RemoveFromShelf("favorites", []string{"/lib/a.epub"}); err != nil {
		t.Fatal(err)
	}
	if books, _ := db.ShelfBooks("favorites"); len(books) != 0 {
		t.Errorf("got %d books after removal, want 0", len(books))
	}

	// A book that is not indexed cannot be added.
	if err := db.AddToShelf("favorites", []string{"/lib/missing.epub"}); err == nil {
		t.Error("expected an error adding an unindexed book")
	}
	// A query shelf has no explicit membership to edit.
	db.CreateShelf(Shelf{Name: "q", Kind: KindQuery, Query: "tag:x"})
	if err := db.AddToShelf("q", []string{"/lib/a.epub"}); err == nil {
		t.Error("expected an error adding to a query shelf")
	}
}

// Deleting a book must not leave it behind in a manual shelf.
func TestDeletingBookRemovesShelfMembership(t *testing.T) {
	db := openTestDB(t)

	db.CreateShelf(Shelf{Name: "favorites", Kind: KindManual})
	db.Upsert(&Book{SHA256: "1", Path: "/lib/a.epub", Format: FormatEPUB, Title: "A"})
	if err := db.AddToShelf("favorites", []string{"/lib/a.epub"}); err != nil {
		t.Fatal(err)
	}

	if err := db.Delete("/lib/a.epub"); err != nil {
		t.Fatal(err)
	}
	books, err := db.ShelfBooks("favorites")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Errorf("got %d books, want 0; the cascade delete did not fire", len(books))
	}
}

// This is the guarantee that makes the SQLite index disposable: shelves live in
// TOML, so deleting index.db and rescanning loses nothing.
func TestShelvesSurviveIndexDeletion(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.db")
	shelvesPath := filepath.Join(dir, "shelves.toml")

	root := t.TempDir()
	writeEPUB(t, filepath.Join(root, "a.epub"), "Book A", "Author A", "", 0)
	writeEPUB(t, filepath.Join(root, "b.epub"), "Book B", "Author B", "", 0)

	db, err := Open(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Scan(context.Background(), root, ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateShelf(Shelf{Name: "queue", Kind: KindQuery, Query: "tag:queue"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateShelf(Shelf{Name: "favorites", Kind: KindManual}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddToShelf("favorites", []string{filepath.Join(root, "a.epub")}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveShelves(shelvesPath); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Nuke the index entirely, as the spec says a user may do at any time.
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	if _, err := db2.Scan(context.Background(), root, ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db2.LoadShelves(shelvesPath); err != nil {
		t.Fatal(err)
	}

	names, err := db2.ShelfNames()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"favorites", "queue"}) {
		t.Errorf("ShelfNames = %v, want both shelves restored", names)
	}

	s, err := db2.GetShelf("queue")
	if err != nil || s.Query != "tag:queue" {
		t.Errorf("query shelf not restored: %+v, %v", s, err)
	}

	// Manual membership is stored by path, so it survives new index ids.
	books, err := db2.ShelfBooks("favorites")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || filepath.Base(books[0].Path) != "a.epub" {
		t.Errorf("manual shelf membership not restored: %v", books)
	}
}

func TestLoadShelvesMissingFileIsFine(t *testing.T) {
	db := openTestDB(t)
	if err := db.LoadShelves(filepath.Join(t.TempDir(), "nope.toml")); err != nil {
		t.Errorf("a missing shelves file should not be an error: %v", err)
	}
}

func TestSaveShelvesIsAtomic(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shelves.toml")

	db.CreateShelf(Shelf{Name: "queue", Kind: KindQuery, Query: "tag:queue"})
	if err := db.SaveShelves(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "queue") {
		t.Errorf("shelves file missing content:\n%s", data)
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shelves") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestImport(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()

	writeEPUB(t, filepath.Join(src, "downloaded.epub"), "A Wizard of Earthsea", "Ursula K. Le Guin", "Earthsea", 1)
	writeEPUB(t, filepath.Join(src, "other.epub"), "Moby-Dick", "Herman Melville", "", 0)

	db := openTestDB(t)
	results, err := db.Import(context.Background(), root, []string{
		filepath.Join(src, "downloaded.epub"),
		filepath.Join(src, "other.epub"),
	}, ImportOptions{
		Mode:     ImportCopy,
		Template: "{author}/{series} {series_index:02d} - {title}",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Source, r.Err)
		}
	}

	wantA := filepath.Join(root, "Ursula K. Le Guin", "Earthsea 01 - A Wizard of Earthsea.epub")
	wantB := filepath.Join(root, "Herman Melville", "Moby-Dick.epub")
	for _, want := range []string{wantA, wantB} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected %s to exist: %v", want, err)
		}
	}

	// Copy mode leaves the source in place.
	if _, err := os.Stat(filepath.Join(src, "downloaded.epub")); err != nil {
		t.Errorf("copy mode removed the source: %v", err)
	}
}

func TestImportMove(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	source := filepath.Join(src, "book.epub")
	writeEPUB(t, source, "Moby-Dick", "Herman Melville", "", 0)

	db := openTestDB(t)
	if _, err := db.Import(context.Background(), root, []string{source}, ImportOptions{
		Mode: ImportMove, Template: "{author}/{title}",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Error("move mode should remove the source")
	}
	if _, err := os.Stat(filepath.Join(root, "Herman Melville", "Moby-Dick.epub")); err != nil {
		t.Errorf("destination missing: %v", err)
	}
}

func TestImportDryRunTouchesNothing(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	source := filepath.Join(src, "book.epub")
	writeEPUB(t, source, "Moby-Dick", "Herman Melville", "", 0)

	db := openTestDB(t)
	results, err := db.Import(context.Background(), root, []string{source}, ImportOptions{
		Mode: ImportMove, Template: "{author}/{title}", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "would-import" {
		t.Errorf("Action = %q", results[0].Action)
	}
	if _, err := os.Stat(source); err != nil {
		t.Error("dry run removed the source")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("dry run created %d entries in the library", len(entries))
	}
}

// Two different books that render to the same name must not overwrite each other.
func TestImportResolvesCollisions(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()

	// Same title and author, different content.
	writeEPUB(t, filepath.Join(src, "one.epub"), "Same Title", "Same Author", "", 0)
	writeEPUB(t, filepath.Join(src, "two.epub"), "Same Title", "Same Author", "", 0)
	// Make the second file's bytes differ.
	os.WriteFile(filepath.Join(src, "two.epub"), append(readFile(t, filepath.Join(src, "two.epub")), 0x00), 0o644)

	db := openTestDB(t)
	results, err := db.Import(context.Background(), root, []string{
		filepath.Join(src, "one.epub"),
		filepath.Join(src, "two.epub"),
	}, ImportOptions{Mode: ImportCopy, Template: "{author}/{title}"})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Source, r.Err)
		}
	}
	if results[0].Dest == results[1].Dest {
		t.Fatalf("both books resolved to the same destination: %s", results[0].Dest)
	}

	entries, _ := os.ReadDir(filepath.Join(root, "Same Author"))
	if len(entries) != 2 {
		t.Errorf("got %d files, want 2 distinct destinations", len(entries))
	}
}

func TestImportRejectsUnsupportedFormat(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	bad := filepath.Join(src, "book.mobi")
	os.WriteFile(bad, []byte("mobi"), 0o644)

	db := openTestDB(t)
	results, err := db.Import(context.Background(), root, []string{bad}, ImportOptions{
		Template: "{author}/{title}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err == nil {
		t.Error("expected an error for an unsupported format")
	}
	if results[0].Action != "skipped" {
		t.Errorf("Action = %q, want skipped", results[0].Action)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
