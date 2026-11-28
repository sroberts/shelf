package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sroberts/shelf/internal/ai"
	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// diskApp is a testApp whose library holds real EPUBs, indexed by a scan, so
// actions that rewrite or remove files have files to act on.
func diskApp(t *testing.T, titles ...string) *App {
	t.Helper()
	app := testApp(t)
	for _, title := range titles {
		dir := filepath.Join(app.Config.LibraryRoot, "Author")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeEPUB(t, filepath.Join(dir, title+".epub"), title)
	}
	if _, err := app.DB.Scan(context.Background(), app.Config.LibraryRoot, library.ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	return app
}

func writeEPUB(t *testing.T, path, title string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := epub.WriteSimple(f, epub.SimpleBook{
		Title: title, Authors: []string{"An Author"}, Paragraphs: []string{"Text."},
		Date: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
}

// started is the root model after its initial loads, sized.
func started(t *testing.T, app *App) Model {
	t.Helper()
	var model tea.Model = New(context.Background(), app)
	model = runCmd(t, model, model.Init())
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return model.(Model)
}

// sendKey sends one key and runs whatever it starts, as the event loop would.
func sendKey(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	return settle(t, next, cmd, 0).(Model)
}

// settle is runCmd for models with text inputs open. A cursor blink is a
// command that sleeps for half a second and re-arms itself, so commands that
// do not answer promptly are dropped rather than waited for: the work an
// action does (a scan, a file write) finishes well inside the limit.
func settle(t *testing.T, model tea.Model, cmd tea.Cmd, depth int) tea.Model {
	t.Helper()
	if cmd == nil || depth > 8 {
		return model
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(250 * time.Millisecond):
		return model
	}
	switch m := msg.(type) {
	case nil, tea.QuitMsg:
		return model
	case tea.BatchMsg:
		for _, sub := range m {
			model = settle(t, model, sub, depth+1)
		}
		return model
	}
	next, cmd2 := model.Update(msg)
	return settle(t, next, cmd2, depth+1)
}

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

var enter = tea.KeyPressMsg{Code: tea.KeyEnter}

func TestEditMetadataRewritesTheFile(t *testing.T) {
	app := diskApp(t, "Old Title")
	m := started(t, app)

	m = sendKey(t, m, char('e'))
	if m.library.action == nil || m.library.action.kind != actEdit {
		t.Fatal("e did not open the metadata form")
	}
	// Typing "q" into a field must not quit.
	m.library.action.inputs[0].SetValue("New Title")
	m = sendKey(t, m, char('q'))
	if m.quitting {
		t.Fatal("typing q into the form quit the program")
	}
	m.library.action.inputs[0].SetValue("New Title")
	m.library.action.inputs[4].SetValue("fantasy, queue")

	m = sendKey(t, m, enter)
	if m.library.action != nil {
		t.Fatalf("form still open after saving: %q", m.library.action.err)
	}

	path := filepath.Join(app.Config.LibraryRoot, "Author", "Old Title.epub")
	f, err := epub.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := f.Metadata()
	f.Close()
	if meta.Title != "New Title" || strings.Join(meta.Subjects, ",") != "fantasy,queue" {
		t.Errorf("file metadata = %q %v, want New Title [fantasy queue]", meta.Title, meta.Subjects)
	}
	// The table reloads from the index, which was refreshed from the file.
	if b := m.library.current(); b == nil || b.Title != "New Title" {
		t.Errorf("table shows %+v after the edit", b)
	}
	if !strings.Contains(m.status, "saved") {
		t.Errorf("status = %q, want a saved message", m.status)
	}
}

func TestEditRefusesABadSeriesIndex(t *testing.T) {
	m := started(t, diskApp(t, "A Book"))
	m = sendKey(t, m, char('e'))
	m.library.action.inputs[3].SetValue("first")
	m = sendKey(t, m, enter)
	if m.library.action == nil || !strings.Contains(m.library.action.err, "not a number") {
		t.Fatalf("want the form open with a number error, got %+v", m.library.action)
	}
}

func TestAddToANewShelfThenRemove(t *testing.T) {
	app := diskApp(t, "A Book")
	m := started(t, app)

	m = sendKey(t, m, char('+'))
	if m.library.action == nil || m.library.action.kind != actAddShelf {
		t.Fatal("+ did not open the shelf picker")
	}
	for _, r := range "queue" {
		m = sendKey(t, m, char(r))
	}
	m = sendKey(t, m, enter)
	if m.library.action != nil {
		t.Fatalf("picker still open: %q", m.library.action.err)
	}

	data, err := os.ReadFile(app.Config.Paths.ShelvesFile())
	if err != nil {
		t.Fatalf("shelves.toml not written: %v", err)
	}
	if !strings.Contains(string(data), `name = "queue"`) || !strings.Contains(string(data), "A Book.epub") {
		t.Errorf("shelves.toml does not hold the new shelf and book:\n%s", data)
	}

	m = sendKey(t, m, char('-'))
	if m.library.action == nil || len(m.library.action.shelves) != 1 {
		t.Fatalf("- should offer the one shelf holding the book, got %+v", m.library.action)
	}
	m = sendKey(t, m, enter)
	books, err := app.DB.ShelfBooks("queue")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Errorf("queue still holds %d book(s) after removing", len(books))
	}
}

func TestClearReadingPosition(t *testing.T) {
	app := diskApp(t, "A Book")
	app.Config.Kosync.User = "reader"
	b, err := app.DB.All()
	if err != nil || len(b) != 1 || b[0].DocID == "" {
		t.Fatalf("fixture book: %v %+v", err, b)
	}
	store, err := kosync.OpenStore(app.Config.Paths.ProgressFile())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.PutProgress("reader", kosync.Progress{Document: b[0].DocID, Percentage: 0.4}); err != nil {
		t.Fatal(err)
	}

	m := started(t, app)
	m = sendKey(t, m, char('x'))
	if m.library.action == nil || m.library.action.kind != actClearProgress {
		t.Fatal("x did not ask to clear")
	}
	if v := m.View().Content; !strings.Contains(v, "keeps its own position") {
		t.Error("the confirmation does not say the reader keeps its position")
	}
	m = sendKey(t, m, char('y'))
	if _, err := store.GetProgress("reader", b[0].DocID); !errors.Is(err, kosync.ErrNoProgress) {
		t.Errorf("progress survived clearing: %v", err)
	}
	if _, ok := m.library.readPct[b[0].Path]; ok {
		t.Error("the table still shows a read percentage")
	}
}

func TestDeleteBookNeedsConfirmation(t *testing.T) {
	app := diskApp(t, "Doomed")
	path := filepath.Join(app.Config.LibraryRoot, "Author", "Doomed.epub")
	m := started(t, app)

	m = sendKey(t, m, char('d'))
	m = sendKey(t, m, char('n'))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("n deleted the file: %v", err)
	}

	m = sendKey(t, m, char('d'))
	m = sendKey(t, m, char('y'))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still on disk after confirming: %v", err)
	}
	if len(m.library.books) != 0 {
		t.Errorf("table still lists %d book(s)", len(m.library.books))
	}
}

func TestImportAFile(t *testing.T) {
	app := diskApp(t)
	src := filepath.Join(t.TempDir(), "incoming.epub")
	writeEPUB(t, src, "Imported Book")

	m := started(t, app)
	m = sendKey(t, m, tea.KeyPressMsg{Code: 'I', Text: "I"})
	if m.library.action == nil || m.library.action.kind != actImport {
		t.Fatal("I did not open the import form")
	}
	m.library.action.inputs[0].SetValue(src)
	m = sendKey(t, m, enter)
	if m.library.action != nil {
		t.Fatalf("import form still open: %q", m.library.action.err)
	}
	if len(m.library.books) != 1 || m.library.books[0].Title != "Imported Book" {
		t.Errorf("library after import = %+v", m.library.books)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("import moved the source rather than copying it: %v", err)
	}
}

// The detail panel shows the AI genre and where a reading position came from.
func TestDetailShowsGenreAndReadingSource(t *testing.T) {
	app := diskApp(t, "A Book")
	app.Config.Kosync.User = "reader"
	books, _ := app.DB.All()

	store, err := kosync.OpenStore(app.Config.Paths.ProgressFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProgress("reader", kosync.Progress{
		Document: books[0].DocID, Percentage: 0.25, Device: "X4",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	genres, err := ai.OpenStore(app.Config.Paths.AIFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := genres.SetGenre(books[0].SHA256, "fantasy", nil, "test"); err != nil {
		t.Fatal(err)
	}
	genres.Close()

	view := started(t, app).View().Content
	for _, want := range []string{"GENRE", "fantasy", "25%", "X4", "just now"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail panel lacks %q:\n%s", want, view)
		}
	}
}

// The TUI never creates ai.db: only shelf serve classifies books.
func TestTUIDoesNotCreateTheAIStore(t *testing.T) {
	app := diskApp(t, "A Book")
	started(t, app)
	if _, err := os.Stat(app.Config.Paths.AIFile()); !os.IsNotExist(err) {
		t.Errorf("ai.db exists after opening the TUI: %v", err)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{90 * 24 * time.Hour, "2025-10-12"},
	} {
		if got := relativeTime(now.Add(-c.ago).Unix(), now); got != c.want {
			t.Errorf("%v ago = %q, want %q", c.ago, got, c.want)
		}
	}
	if relativeTime(0, now) != "" {
		t.Error("a zero time should render as nothing")
	}
}
