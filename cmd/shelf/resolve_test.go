package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/library"
)

func openResolveDB(t *testing.T) *library.DB {
	t.Helper()
	db, err := library.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Index ids are never printed and change whenever index.db is rebuilt, so a
// bare number that matches no title must not fall back to selecting a book by
// id: under `shelf rm -y 1999` that deleted an unrelated book from disk. An id
// is only honoured when spelled out as `id:N`.
func TestResolveBookRequiresExplicitIDPrefix(t *testing.T) {
	db := openResolveDB(t)

	// Enough books that every small bare number is also a live id.
	var dune *library.Book
	for i, title := range []string{"Kindred", "Dune", "A Wizard of Earthsea"} {
		b := &library.Book{
			SHA256: fmt.Sprintf("hash%d", i), Path: fmt.Sprintf("/lib/%d.epub", i),
			Size: 10, Format: library.FormatEPUB, Title: title,
		}
		if err := db.Upsert(b); err != nil {
			t.Fatal(err)
		}
		if title == "Dune" {
			dune = b
		}
	}
	idArg := fmt.Sprintf("id:%d", dune.ID)

	got, err := resolveBook(db, idArg)
	if err != nil {
		t.Fatalf("resolveBook(%q): %v", idArg, err)
	}
	if got.Path != dune.Path {
		t.Errorf("resolveBook(%q) = %s, want %s", idArg, got.Path, dune.Path)
	}

	tests := []struct {
		ref     string
		wantErr string
	}{
		{ref: fmt.Sprint(dune.ID), wantErr: "no book matches"},
		{ref: "1999", wantErr: "no book matches"},
		{ref: "id:abc", wantErr: "invalid book id"},
		{ref: "id:", wantErr: "invalid book id"},
		{ref: "id:99999", wantErr: "no book with id 99999"},
	}
	for _, tt := range tests {
		b, err := resolveBook(db, tt.ref)
		if err == nil {
			t.Errorf("resolveBook(%q) = %s, want error containing %q", tt.ref, b.Path, tt.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("resolveBook(%q) error = %q, want it to contain %q", tt.ref, err, tt.wantErr)
		}
	}
}
