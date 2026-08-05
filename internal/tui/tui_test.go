package tui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/library"
)

func testApp(t *testing.T, books ...*library.Book) *App {
	t.Helper()

	dir := t.TempDir()
	db, err := library.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, b := range books {
		if err := db.Upsert(b); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.LibraryRoot = dir
	cfg.Paths = config.Paths{Config: dir, Data: dir, State: dir, Cache: dir}

	return &App{
		Config: &cfg,
		DB:     db,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func book(path, title, author string) *library.Book {
	return &library.Book{
		SHA256: path, Path: path, Format: library.FormatEPUB,
		Title: title, AuthorSort: author, Authors: []string{author}, Size: 1024,
	}
}

// The library screen must render the books it was given.
func TestLibraryRendersBooks(t *testing.T) {
	app := testApp(t,
		book("/lib/a.epub", "A Wizard of Earthsea", "Le Guin, Ursula K."),
		book("/lib/b.epub", "Moby-Dick", "Melville, Herman"),
	)

	tm := teatest.NewTestModel(t, New(context.Background(), app),
		teatest.WithInitialTermSize(120, 30))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("Earthsea")) && bytes.Contains(out, []byte("Moby-Dick"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// Tab must cycle screens, and the tab bar must reflect the focus.
func TestScreenCycling(t *testing.T) {
	app := testApp(t, book("/lib/a.epub", "A Book", "An Author"))

	tm := teatest.NewTestModel(t, New(context.Background(), app),
		teatest.WithInitialTermSize(120, 30))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("A Book"))
	}, teatest.WithDuration(5*time.Second))

	// Jump straight to Devices with its number key.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("No devices")) ||
			bytes.Contains(out, []byte("probing"))
	}, teatest.WithDuration(5*time.Second))

	// And to Sync.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("No sync in progress"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// Typing into the filter must not be interpreted as global commands. Without
// key capture, typing a word containing "q" would quit the program.
func TestFilterCapturesKeys(t *testing.T) {
	app := testApp(t,
		book("/lib/a.epub", "Quicksilver", "Stephenson, Neal"),
		book("/lib/b.epub", "Moby-Dick", "Melville, Herman"),
	)

	tm := teatest.NewTestModel(t, New(context.Background(), app),
		teatest.WithInitialTermSize(120, 30))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("Quicksilver"))
	}, teatest.WithDuration(5*time.Second))

	// Open the filter and type a word containing "q" and "s" (quit and sync).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "quicksilver" {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	// The program must still be alive and showing the typed text.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("quicksilver"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("Quicksilver"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestHelpToggle(t *testing.T) {
	app := testApp(t, book("/lib/a.epub", "A Book", "An Author"))

	tm := teatest.NewTestModel(t, New(context.Background(), app),
		teatest.WithInitialTermSize(120, 40))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("A Book"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		// The expanded help exposes bindings the short strip omits.
		return bytes.Contains(out, []byte("visual select")) ||
			bytes.Contains(out, []byte("select all"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// An empty library must say something useful rather than rendering a blank box.
func TestEmptyLibraryIsExplained(t *testing.T) {
	app := testApp(t)

	tm := teatest.NewTestModel(t, New(context.Background(), app),
		teatest.WithInitialTermSize(120, 30))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("shelf import")) ||
			bytes.Contains(out, []byte("library is empty"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// --- unit tests that need no terminal ---

func TestSelectionTracksPathsNotIndices(t *testing.T) {
	app := testApp(t)
	m := newLibraryModel(app, DefaultKeyMap(), DefaultStyles())
	m.books = []*library.Book{
		book("/lib/a.epub", "A", "Author A"),
		book("/lib/b.epub", "B", "Author B"),
		book("/lib/c.epub", "C", "Author C"),
	}

	m.selected["/lib/b.epub"] = true

	// Filtering reorders the slice; the selection must follow the book, not
	// the row it happened to occupy.
	m.books = []*library.Book{
		book("/lib/c.epub", "C", "Author C"),
		book("/lib/b.epub", "B", "Author B"),
	}

	got := m.selectedBooks()
	if len(got) != 1 || got[0].Path != "/lib/b.epub" {
		t.Errorf("selectedBooks() = %v, want the book at /lib/b.epub", got)
	}
}

func TestVisualSelectionCoversRange(t *testing.T) {
	app := testApp(t)
	m := newLibraryModel(app, DefaultKeyMap(), DefaultStyles())
	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		m.books = append(m.books, book(p, p, "Author"))
	}

	m.cursor = 1
	m.visual = true
	m.visualStart = 1
	m.extendVisual()
	m.cursor = 3
	m.extendVisual()

	if n := len(m.selectedBooks()); n != 3 {
		t.Errorf("selected %d books, want 3 (indices 1..3)", n)
	}
	if m.selected["/a"] {
		t.Error("the range should not extend above its start")
	}
}

func TestTruncateWidthHandlesWideRunes(t *testing.T) {
	tests := []struct {
		in    string
		width int
	}{
		{"short", 20},
		{"a much longer title than fits", 10},
		{"海辺のカフカという長い題名", 8},
		{"", 5},
	}
	for _, tt := range tests {
		got := truncateWidth(tt.in, tt.width)
		if w := displayWidth(got); w > tt.width {
			t.Errorf("truncateWidth(%q, %d) = %q (width %d, over budget)",
				tt.in, tt.width, got, w)
		}
	}
}

func TestPadAlignsWideRunes(t *testing.T) {
	// CJK titles must not break column alignment; padding counts display
	// cells, not bytes or runes.
	for _, s := range []string{"ascii", "海辺のカフカ", "mixed 混合", ""} {
		got := pad(s, 20)
		if w := displayWidth(got); w != 20 {
			t.Errorf("pad(%q, 20) has width %d, want 20", s, w)
		}
	}
}

func TestDetectGraphicsHonorsExplicitPreference(t *testing.T) {
	for _, tt := range []struct {
		pref string
		want GraphicsMode
	}{
		{"none", GraphicsNone},
		{"ascii", GraphicsBlocks},
		{"sixel", GraphicsSixel},
		{"kitty", GraphicsKitty},
		{"KITTY", GraphicsKitty},
		{"  none  ", GraphicsNone},
	} {
		if got := DetectGraphics(tt.pref); got != tt.want {
			t.Errorf("DetectGraphics(%q) = %q, want %q", tt.pref, got, tt.want)
		}
	}
}

func TestDetectGraphicsAuto(t *testing.T) {
	clear := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{"TMUX", "TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID"} {
			t.Setenv(k, "")
		}
	}

	t.Run("kitty by window id", func(t *testing.T) {
		clear(t)
		t.Setenv("KITTY_WINDOW_ID", "1")
		if got := DetectGraphics("auto"); got != GraphicsKitty {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ghostty", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM_PROGRAM", "ghostty")
		if got := DetectGraphics("auto"); got != GraphicsKitty {
			t.Errorf("got %q", got)
		}
	})

	t.Run("foot speaks sixel", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "foot")
		if got := DetectGraphics("auto"); got != GraphicsSixel {
			t.Errorf("got %q", got)
		}
	})

	// Multiplexers need explicit passthrough and get it wrong more often than
	// right, so graphics degrade rather than spraying escapes across panes.
	t.Run("tmux degrades to blocks", func(t *testing.T) {
		clear(t)
		t.Setenv("KITTY_WINDOW_ID", "1")
		t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
		if got := DetectGraphics("auto"); got != GraphicsBlocks {
			t.Errorf("got %q, want blocks inside tmux", got)
		}
	})

	t.Run("screen degrades to blocks", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "screen-256color")
		if got := DetectGraphics("auto"); got != GraphicsBlocks {
			t.Errorf("got %q", got)
		}
	})

	t.Run("unknown terminal falls back to blocks", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "xterm-256color")
		if got := DetectGraphics("auto"); got != GraphicsBlocks {
			t.Errorf("got %q", got)
		}
	})
}

func TestRenderCover(t *testing.T) {
	img := testImage(64, 96)

	t.Run("blocks produce output with no stray newlines at the edges", func(t *testing.T) {
		out := RenderCover(img, GraphicsBlocks, 20, 10)
		if out == "" {
			t.Fatal("no output")
		}
		if strings.HasPrefix(out, "\n") || strings.HasSuffix(out, "\n") {
			t.Error("output has leading or trailing newlines")
		}
		if lines := strings.Count(out, "\n") + 1; lines > 10 {
			t.Errorf("rendered %d lines into a 10-row box", lines)
		}
	})

	t.Run("kitty emits a graphics escape", func(t *testing.T) {
		out := RenderCover(img, GraphicsKitty, 20, 10)
		if !strings.HasPrefix(out, "\x1b_G") {
			t.Errorf("output does not start with the kitty introducer: %q", first(out, 20))
		}
		if !strings.HasSuffix(out, "\x1b\\") {
			t.Error("output is not terminated")
		}
	})

	t.Run("sixel emits a sixel envelope", func(t *testing.T) {
		out := RenderCover(img, GraphicsSixel, 20, 10)
		if !strings.HasPrefix(out, "\x1bP") {
			t.Errorf("output does not start with the sixel introducer: %q", first(out, 20))
		}
		if !strings.HasSuffix(out, "\x1b\\") {
			t.Error("output is not terminated")
		}
	})

	t.Run("none and degenerate sizes render nothing", func(t *testing.T) {
		if got := RenderCover(img, GraphicsNone, 20, 10); got != "" {
			t.Error("GraphicsNone should render nothing")
		}
		if got := RenderCover(nil, GraphicsBlocks, 20, 10); got != "" {
			t.Error("a nil image should render nothing")
		}
		if got := RenderCover(img, GraphicsBlocks, 1, 1); got != "" {
			t.Error("a one-cell box should render nothing")
		}
	})

	// Never upscale: a small cover stays small rather than turning to mush.
	t.Run("does not upscale", func(t *testing.T) {
		small := testImage(4, 6)
		out := RenderCover(small, GraphicsBlocks, 80, 40)
		if lines := strings.Count(out, "\n") + 1; lines > 3 {
			t.Errorf("a 4x6 image rendered into %d lines; it should not be upscaled", lines)
		}
	})
}

func TestOpenLogWritesToFileNotStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "shelf.log")

	logger, closer, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hello from the tui", "key", "value")
	closer.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the log file was not created: %v", err)
	}
	if !strings.Contains(string(data), "hello from the tui") {
		t.Errorf("log content = %q", data)
	}
}

// An unwritable log location must not stop the TUI from running.
func TestOpenLogDegradesGracefully(t *testing.T) {
	// A path under a regular file cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("x"), 0o644)

	logger, closer, err := OpenLog(filepath.Join(blocker, "shelf.log"))
	if err == nil {
		t.Skip("this filesystem allowed the path; nothing to assert")
	}
	if logger == nil || closer == nil {
		t.Fatal("OpenLog must return a usable logger even on failure")
	}
	logger.Info("must not panic")
	closer.Close()
}

func TestGlyphStylesAreDistinct(t *testing.T) {
	s := DefaultStyles()
	glyphs := []string{GlyphSynced, GlyphPending, GlyphAbsent, GlyphOrphan, GlyphUnknown}

	seen := map[string]bool{}
	for _, g := range glyphs {
		if seen[g] {
			t.Errorf("duplicate glyph %q", g)
		}
		seen[g] = true
		if displayWidth(g) != 1 {
			t.Errorf("glyph %q is %d cells wide; it must be exactly 1 to keep columns aligned",
				g, displayWidth(g))
		}
		_ = s.GlyphStyle(g)
	}
}

// --- helpers ---

func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 2), B: 0x80, A: 0xff})
		}
	}
	return img
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// The library table grows a READ column only when reading progress exists, so
// a library with no sync configured looks exactly as it did before.
func TestLibraryReadColumnAppearsOnlyWithProgress(t *testing.T) {
	app := testApp(t, book("/lib/a.epub", "A Book", "An Author"))

	m := newLibraryModel(app, DefaultKeyMap(), DefaultStyles())
	m.setSize(100, 20)
	m.loading = false
	m.books = []*library.Book{book("/lib/a.epub", "A Book", "An Author")}

	if strings.Contains(m.View(), "READ") {
		t.Error("the READ column appeared with no progress recorded")
	}

	m.readPct = map[string]float64{"/lib/a.epub": 37.0}
	view := m.View()
	if !strings.Contains(view, "READ") {
		t.Errorf("the READ header is missing once progress exists:\n%s", view)
	}
	if !strings.Contains(view, "37%") {
		t.Errorf("the percentage is missing:\n%s", view)
	}
}

// A finished book reads "done" rather than 100%, since the device reports
// 0.9998 for a book read to its last page.
func TestLibraryShowsDoneForFinishedBooks(t *testing.T) {
	app := testApp(t, book("/lib/a.epub", "A Book", "An Author"))

	m := newLibraryModel(app, DefaultKeyMap(), DefaultStyles())
	m.setSize(100, 20)
	m.loading = false
	m.books = []*library.Book{book("/lib/a.epub", "A Book", "An Author")}
	m.readPct = map[string]float64{"/lib/a.epub": 99.98}

	view := m.View()
	if !strings.Contains(view, "done") {
		t.Errorf("a finished book should read 'done':\n%s", view)
	}
	if strings.Contains(view, "100%") {
		t.Errorf("a finished book should not read '100%%':\n%s", view)
	}
}
