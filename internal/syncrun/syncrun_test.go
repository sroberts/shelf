package syncrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
)

// fixture is a library of two books and a mounted "card" to sync them to.
// The SD transport needs no hardware, so this runs the real pipeline end to
// end: open, list, prepare, plan, execute, save the manifest.
func fixture(t *testing.T) (*config.Config, config.Device, []*library.Book, string) {
	t.Helper()
	dir := t.TempDir()
	libRoot := filepath.Join(dir, "library")
	card := filepath.Join(dir, "card")
	for _, d := range []string{libRoot, card} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i, title := range []string{"First Light", "Second Wind"} {
		f, err := os.Create(filepath.Join(libRoot, title+".epub"))
		if err != nil {
			t.Fatal(err)
		}
		err = epub.WriteSimple(f, epub.SimpleBook{
			Title: title, Authors: []string{"Test Author"},
			Date:       time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC),
			Paragraphs: []string{"Text."},
		})
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Paths = config.StoragePaths(filepath.Join(dir, "storage"))
	cfg.LibraryRoot = libRoot
	cfg.NamingTemplate = "{author}/{title}"
	cfg.Sync.InterOpDelay = config.Duration{}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	db, err := library.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Scan(context.Background(), libRoot, library.ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	books, err := db.All()
	if err != nil || len(books) != 2 {
		t.Fatalf("books = %d, %v", len(books), err)
	}

	dev := config.Device{Nickname: "card", Root: "/Books", Transport: config.TransportSD, Mount: card}
	return &cfg, dev, books, card
}

func TestPrepareThenExecuteSendsTheBooksOnce(t *testing.T) {
	cfg, dev, books, card := fixture(t)
	ctx := context.Background()

	s, err := Prepare(ctx, cfg, dev, books, Options{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, uploads, _, _ := s.Plan.Counts(); uploads != 2 {
		t.Fatalf("uploads = %d, want 2; ops %v", uploads, s.Plan.Ops)
	}

	var events int
	res, err := s.Execute(ctx, func(syncpkg.Event) { events++ })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Completed == 0 || res.Failed != 0 || events == 0 {
		t.Errorf("result %+v, %d events", res, events)
	}
	for _, title := range []string{"First Light", "Second Wind"} {
		if _, err := os.Stat(filepath.Join(card, "Books", "Test Author", title+".epub")); err != nil {
			t.Errorf("%s not on the card: %v", title, err)
		}
	}

	// A session is one run: its plan describes the device before it ran.
	if _, err := s.Execute(ctx, nil); !errors.Is(err, ErrExecuted) {
		t.Errorf("second Execute = %v, want ErrExecuted", err)
	}

	// The manifest was saved, so planning again finds nothing to do.
	again, err := Prepare(ctx, cfg, dev, books, Options{})
	if err != nil {
		t.Fatalf("prepare again: %v", err)
	}
	if !again.Plan.IsEmpty() || again.Plan.Unchanged != 2 {
		t.Errorf("second plan: %d ops, %d unchanged; want 0 and 2", len(again.Plan.Ops), again.Plan.Unchanged)
	}
}

func TestPrepareChangesNothingOnTheDevice(t *testing.T) {
	cfg, dev, books, card := fixture(t)
	if _, err := Prepare(context.Background(), cfg, dev, books, Options{Prune: true}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	entries, err := os.ReadDir(card)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a preview wrote to the card: %v", entries)
	}
	if _, err := os.Stat(cfg.Paths.DeviceStateFile(dev.Nickname)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a preview saved a manifest: %v", err)
	}
}
