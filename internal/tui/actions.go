package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// Book actions on the library screen: everything the CLI can do to a book
// that is not a sync. Each one opens an overlay that takes every key until it
// is saved or cancelled, and each does its work in a tea.Cmd: Update only
// edits the overlay's fields.

type actionKind int

const (
	actEdit actionKind = iota
	actAddShelf
	actRemoveShelf
	actClearProgress
	actDelete
	actImport
)

// bookAction is the open overlay.
type bookAction struct {
	kind  actionKind
	books []*library.Book

	// Text fields: the metadata form, the import path, or the shelf name
	// box at the top of a shelf picker.
	inputs []textinput.Model
	labels []string
	focus  int

	// shelves are the candidates a shelf picker offers, filtered by
	// inputs[0]; cursor indexes the filtered list.
	shelves []string
	cursor  int

	err  string
	busy bool
}

// Messages for actions.
type (
	// shelvesForMsg opens a shelf picker once the candidate shelves are read.
	shelvesForMsg struct {
		kind    actionKind
		books   []*library.Book
		shelves []string
		err     error
	}
	// actionDoneMsg reports a finished action. On error a form stays open
	// with the message, so nothing typed is lost.
	actionDoneMsg struct {
		status string
		err    error
	}
	// libraryChangedMsg asks the root to reload the table and the header.
	libraryChangedMsg struct{ status string }
)

// editFields are the metadata the form edits: the ones worth typing in a
// terminal. Everything else is `shelf meta --set`.
var editFields = []string{"title", "authors", "series", "series #", "tags"}

func newInput(placeholder, value string, styles Styles) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.CharLimit = 500
	ti.SetWidth(50)
	ti.SetValue(value)
	ts := textinput.DefaultStyles(styles.Dark)
	ts.Focused.Text = styles.StatValue
	ti.SetStyles(ts)
	return ti
}

func newEditAction(b *library.Book, styles Styles) *bookAction {
	index := ""
	if b.SeriesIndex != 0 {
		index = strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64)
	}
	values := []string{b.Title, strings.Join(b.Authors, ", "), b.Series, index, strings.Join(b.Tags, ", ")}
	placeholders := []string{"title", "comma-separated", "series name", "1", "comma-separated"}
	a := &bookAction{kind: actEdit, books: []*library.Book{b}, labels: editFields}
	for i := range editFields {
		a.inputs = append(a.inputs, newInput(placeholders[i], values[i], styles))
	}
	a.inputs[0].Focus()
	return a
}

func newImportAction(styles Styles) *bookAction {
	a := &bookAction{
		kind:   actImport,
		labels: []string{"file"},
		inputs: []textinput.Model{newInput("~/Downloads/book.epub", "", styles)},
	}
	a.inputs[0].Focus()
	return a
}

func newShelfPicker(kind actionKind, books []*library.Book, shelves []string, styles Styles) *bookAction {
	a := &bookAction{
		kind: kind, books: books, shelves: shelves,
		labels: []string{"shelf"},
		inputs: []textinput.Model{newInput("type to filter, or a new name", "", styles)},
	}
	if kind == actRemoveShelf {
		a.inputs[0].Placeholder = "type to filter"
	}
	a.inputs[0].Focus()
	return a
}

// matches is the picker's filtered shelf list.
func (a *bookAction) matches() []string {
	q := strings.ToLower(strings.TrimSpace(a.inputs[0].Value()))
	var out []string
	for _, s := range a.shelves {
		if q == "" || strings.Contains(strings.ToLower(s), q) {
			out = append(out, s)
		}
	}
	return out
}

// loadShelvesFor reads the manual shelves an add or remove can use: all of
// them to add to, and those holding one of the books to remove from. Saved
// query shelves are never offered, because their contents follow the query.
func (m libraryModel) loadShelvesFor(kind actionKind, books []*library.Book) tea.Cmd {
	db := m.app.DB
	return func() tea.Msg {
		all, err := db.ListShelves()
		if err != nil {
			return shelvesForMsg{err: err}
		}
		want := map[string]bool{}
		for _, b := range books {
			want[b.Path] = true
		}
		var names []string
		for _, s := range all {
			if s.Kind != library.KindManual {
				continue
			}
			if kind == actRemoveShelf {
				members, err := db.ShelfBooks(s.Name)
				if err != nil {
					return shelvesForMsg{err: err}
				}
				holds := false
				for _, mb := range members {
					if want[mb.Path] {
						holds = true
						break
					}
				}
				if !holds {
					continue
				}
			}
			names = append(names, s.Name)
		}
		return shelvesForMsg{kind: kind, books: books, shelves: names}
	}
}

// targets is what a book action applies to: the selection, or else the book
// under the cursor.
func (m libraryModel) targets() []*library.Book {
	if books := m.selectedBooks(); len(books) > 0 {
		return books
	}
	if b := m.current(); b != nil {
		return []*library.Book{b}
	}
	return nil
}

// startAction handles the keys that open an overlay. ok is false when the key
// is not an action key.
func (m libraryModel) startAction(msg tea.KeyPressMsg) (libraryModel, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Edit):
		b := m.current()
		switch {
		case b == nil:
			return m, setStatus("no book under the cursor"), true
		case b.Format != library.FormatEPUB:
			return m, setStatus("metadata editing is only supported for EPUB, not %s", b.Format), true
		}
		m.action = newEditAction(b, m.styles)
		return m, textinput.Blink, true

	case key.Matches(msg, m.keys.AddToShelf), key.Matches(msg, m.keys.RemoveShelf):
		books := m.targets()
		if len(books) == 0 {
			return m, setStatus("nothing selected"), true
		}
		kind := actAddShelf
		if key.Matches(msg, m.keys.RemoveShelf) {
			kind = actRemoveShelf
		}
		return m, m.loadShelvesFor(kind, books), true

	case key.Matches(msg, m.keys.ClearProgress):
		books := m.targets()
		if len(books) == 0 {
			return m, setStatus("nothing selected"), true
		}
		if m.app.Config.Kosync.User == "" {
			return m, setStatus("no reading progress: set [kosync] user in config.toml"), true
		}
		m.action = &bookAction{kind: actClearProgress, books: books}
		return m, nil, true

	case key.Matches(msg, m.keys.DeleteBook):
		books := m.targets()
		if len(books) == 0 {
			return m, setStatus("nothing selected"), true
		}
		m.action = &bookAction{kind: actDelete, books: books}
		return m, nil, true

	case key.Matches(msg, m.keys.Import):
		m.action = newImportAction(m.styles)
		return m, textinput.Blink, true
	}
	return m, nil, false
}

// updateAction routes a key to the open overlay. No I/O: confirming returns
// the command that does the work.
func (m libraryModel) updateAction(msg tea.KeyPressMsg) (libraryModel, tea.Cmd) {
	a := m.action
	if a.busy {
		return m, nil
	}

	// Confirmations take y/enter or n/esc and nothing else.
	if a.kind == actClearProgress || a.kind == actDelete {
		switch msg.String() {
		case "y", "enter":
			a.busy = true
			if a.kind == actDelete {
				return m, m.deleteBooks(a.books)
			}
			return m, m.clearProgress(a.books)
		case "n", "esc":
			m.action = nil
			return m, setStatus("cancelled")
		}
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.action = nil
		return m, setStatus("cancelled")

	case "tab", "shift+tab":
		if len(a.inputs) > 1 {
			delta := 1
			if msg.String() == "shift+tab" {
				delta = -1
			}
			a.inputs[a.focus].Blur()
			a.focus = (a.focus + delta + len(a.inputs)) % len(a.inputs)
			return m, a.inputs[a.focus].Focus()
		}
		return m, nil

	case "up", "down":
		if a.kind == actAddShelf || a.kind == actRemoveShelf {
			n := len(a.matches())
			if n > 0 {
				d := 1
				if msg.String() == "up" {
					d = -1
				}
				a.cursor = (a.cursor + d + n) % n
			}
			return m, nil
		}
		if len(a.inputs) > 1 {
			d := 1
			if msg.String() == "up" {
				d = -1
			}
			a.inputs[a.focus].Blur()
			a.focus = (a.focus + d + len(a.inputs)) % len(a.inputs)
			return m, a.inputs[a.focus].Focus()
		}
		return m, nil

	case "enter":
		return m.submitAction()
	}

	var cmd tea.Cmd
	a.inputs[a.focus], cmd = a.inputs[a.focus].Update(msg)
	a.err = ""
	if a.kind == actAddShelf || a.kind == actRemoveShelf {
		a.cursor = 0 // the list just changed under the cursor
	}
	return m, cmd
}

func (m libraryModel) submitAction() (libraryModel, tea.Cmd) {
	a := m.action
	switch a.kind {
	case actEdit:
		patch, err := editPatch(a.books[0], a.inputs)
		if err != nil {
			a.err = err.Error()
			return m, nil
		}
		if patch.IsEmpty() {
			m.action = nil
			return m, setStatus("nothing changed")
		}
		a.busy = true
		return m, m.writeMetadata(a.books[0], patch)

	case actImport:
		path := strings.TrimSpace(a.inputs[0].Value())
		if path == "" {
			a.err = "a file to import is required"
			return m, nil
		}
		a.busy = true
		return m, m.importFile(path)

	case actAddShelf, actRemoveShelf:
		name := strings.TrimSpace(a.inputs[0].Value())
		if list := a.matches(); len(list) > 0 && a.cursor < len(list) {
			name = list[a.cursor]
		}
		if name == "" {
			a.err = "choose a shelf"
			return m, nil
		}
		if a.kind == actRemoveShelf && !contains(a.shelves, name) {
			a.err = fmt.Sprintf("%q does not hold these books", name)
			return m, nil
		}
		a.busy = true
		return m, m.editShelf(a.kind, name, !contains(a.shelves, name), a.books)
	}
	return m, nil
}

// editPatch turns the form into a patch of the fields that changed. Only
// changed fields are written, so an untouched author keeps any role or
// file-as attribute the form cannot show.
func editPatch(b *library.Book, inputs []textinput.Model) (epub.Patch, error) {
	var p epub.Patch
	val := func(i int) string { return strings.TrimSpace(inputs[i].Value()) }

	if v := val(0); v != b.Title {
		if v == "" {
			return p, errors.New("title cannot be empty")
		}
		p.Title = &v
	}
	if v := splitComma(val(1)); strings.Join(v, ", ") != strings.Join(b.Authors, ", ") {
		creators := make([]epub.Creator, 0, len(v))
		for _, name := range v {
			creators = append(creators, epub.Creator{Name: name})
		}
		p.Authors = &creators
	}
	if v := val(2); v != b.Series {
		p.Series = &v
	}
	if v := val(3); v != "" || b.SeriesIndex != 0 {
		f := 0.0
		if v != "" {
			var err error
			if f, err = strconv.ParseFloat(v, 64); err != nil {
				return p, fmt.Errorf("series # %q is not a number", v)
			}
		}
		if f != b.SeriesIndex {
			p.SeriesIndex = &f
		}
	}
	if v := splitComma(val(4)); strings.Join(v, ",") != strings.Join(b.Tags, ",") {
		p.Subjects = &v
	}
	return p, nil
}

func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// writeMetadata rewrites the EPUB, then refreshes the index from the file:
// the index is derived, so it is never updated in parallel with the file.
func (m libraryModel) writeMetadata(b *library.Book, patch epub.Patch) tea.Cmd {
	db := m.app.DB
	path := b.Path
	return func() tea.Msg {
		if err := epub.Write(path, patch); err != nil {
			return actionDoneMsg{err: err}
		}
		if _, err := db.Scan(context.Background(), filepath.Dir(path), library.ScanOptions{}); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "saved " + filepath.Base(path)}
	}
}

// editShelf adds books to, or removes them from, a manual shelf, creating it
// first when asked. shelves.toml is the authority: when it cannot be written
// the index is reloaded from it, so the table never shows an edit that will
// not survive a restart.
func (m libraryModel) editShelf(kind actionKind, name string, create bool, books []*library.Book) tea.Cmd {
	db := m.app.DB
	file := m.app.Config.Paths.ShelvesFile()
	paths := make([]string, 0, len(books))
	for _, b := range books {
		paths = append(paths, b.Path)
	}
	return func() tea.Msg {
		var err error
		if create {
			err = db.CreateShelf(library.Shelf{Name: name, Kind: library.KindManual})
		}
		if err == nil {
			if kind == actAddShelf {
				err = db.AddToShelf(name, paths)
			} else {
				err = db.RemoveFromShelf(name, paths)
			}
		}
		if err == nil {
			err = db.SaveShelves(file)
		}
		if err != nil {
			_ = db.LoadShelves(file)
			return actionDoneMsg{err: err}
		}
		verb := "added to"
		if kind == actRemoveShelf {
			verb = "removed from"
		}
		return actionDoneMsg{status: fmt.Sprintf("%s %s %q", countBooks(len(paths)), verb, name)}
	}
}

// clearProgress forgets shelf's copy of the reading positions. The reader
// keeps its own, which the confirmation says before anything happens.
func (m libraryModel) clearProgress(books []*library.Book) tea.Cmd {
	cfg := m.app.Config
	return func() tea.Msg {
		store, err := kosync.OpenStore(cfg.Paths.ProgressFile())
		if err != nil {
			return actionDoneMsg{err: err}
		}
		defer store.Close()
		cleared := 0
		for _, b := range books {
			if b.DocID == "" {
				continue
			}
			switch err := store.DeleteProgress(cfg.Kosync.User, b.DocID); {
			case errors.Is(err, kosync.ErrNoProgress):
			case err != nil:
				return actionDoneMsg{err: err}
			default:
				cleared++
			}
		}
		if cleared == 0 {
			return actionDoneMsg{status: "no reading position to clear"}
		}
		return actionDoneMsg{status: fmt.Sprintf("cleared the reading position of %s", countBooks(cleared))}
	}
}

// deleteBooks removes the files and their index rows, as `shelf rm` does.
func (m libraryModel) deleteBooks(books []*library.Book) tea.Cmd {
	db := m.app.DB
	root := m.app.Config.LibraryRoot
	file := m.app.Config.Paths.ShelvesFile()
	return func() tea.Msg {
		for i, b := range books {
			if err := db.DeleteBook(b.Path); err != nil {
				// Books already gone have left their shelves; persist that
				// before reporting, as the CLI does.
				return actionDoneMsg{err: errors.Join(
					fmt.Errorf("deleted %d of %d: %w", i, len(books), err), db.SaveShelves(file))}
			}
			library.PruneEmptyDirs(root, b.Path)
		}
		if err := db.SaveShelves(file); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "deleted " + countBooks(len(books))}
	}
}

// importFile copies a file into the library by the naming template and indexes
// it, as `shelf import` does.
func (m libraryModel) importFile(path string) tea.Cmd {
	db := m.app.DB
	cfg := m.app.Config
	return func() tea.Msg {
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, rest)
			}
		}
		results, err := db.Import(context.Background(), cfg.LibraryRoot, []string{path},
			library.ImportOptions{Mode: library.ImportCopy, Template: cfg.NamingTemplate})
		if err != nil {
			return actionDoneMsg{err: err}
		}
		if len(results) != 1 {
			return actionDoneMsg{err: errors.New("import produced no result")}
		}
		if results[0].Err != nil {
			return actionDoneMsg{err: results[0].Err}
		}
		if _, err := db.Scan(context.Background(), filepath.Dir(results[0].Dest), library.ScanOptions{}); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "imported " + results[0].Dest}
	}
}

func countBooks(n int) string {
	if n == 1 {
		return "1 book"
	}
	return fmt.Sprintf("%d books", n)
}

// actionView draws the overlay in place of the table.
func (m libraryModel) actionView() string {
	a := m.action
	s := m.styles
	var b strings.Builder

	title := map[actionKind]string{
		actEdit:          "Edit metadata",
		actAddShelf:      "Add to a shelf",
		actRemoveShelf:   "Remove from a shelf",
		actClearProgress: "Clear reading position",
		actDelete:        "Delete",
		actImport:        "Import a file",
	}[a.kind]
	b.WriteString(s.Title.Render(title) + "\n\n")

	if len(a.books) > 0 {
		for i, bk := range a.books {
			if i == 5 {
				b.WriteString(s.Subtle.Render(fmt.Sprintf("  …and %d more", len(a.books)-5)) + "\n")
				break
			}
			b.WriteString("  " + s.StatValue.Render(bk.DisplayTitle()) + "\n")
		}
		b.WriteString("\n")
	}

	switch a.kind {
	case actClearProgress:
		b.WriteString(s.Subtle.Render("Forgets shelf's copy only. The reader keeps its own position and\npushes it again the next time the book is open.") + "\n\n")
		b.WriteString(s.Accent.Render("y") + " clear  " + s.Accent.Render("n") + " cancel")
	case actDelete:
		b.WriteString(s.Warning.Render("The files are removed from disk.") + "\n\n")
		b.WriteString(s.Accent.Render("y") + " delete  " + s.Accent.Render("n") + " cancel")
	default:
		for i, in := range a.inputs {
			marker := "  "
			if i == a.focus {
				marker = s.Accent.Render("▸ ")
			}
			b.WriteString(fmt.Sprintf("%s%s  %s\n", marker, s.StatLabel.Render(fmt.Sprintf("%-8s", a.labels[i])), in.View()))
		}
		if a.kind == actAddShelf || a.kind == actRemoveShelf {
			b.WriteString("\n")
			list := a.matches()
			if len(list) == 0 {
				switch {
				case a.kind == actRemoveShelf:
					b.WriteString(s.Subtle.Render("no manual shelf holds these books"))
				case strings.TrimSpace(a.inputs[0].Value()) != "":
					b.WriteString(s.Subtle.Render("enter creates a new manual shelf with this name"))
				default:
					b.WriteString(s.Subtle.Render("no manual shelves yet — type a name to create one"))
				}
				b.WriteString("\n")
			}
			for i, name := range list {
				line := "  " + name
				if i == a.cursor {
					line = s.Accent.Render("▸ " + name)
				}
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	switch {
	case a.busy:
		b.WriteString(s.Subtle.Render("working…"))
	case a.err != "":
		b.WriteString(s.Error.Render(a.err))
	}
	return b.String()
}

// actionHint is the status line while an overlay is open.
func (a *bookAction) hint() string {
	switch a.kind {
	case actClearProgress, actDelete:
		return "y to confirm, n or esc to cancel"
	case actEdit:
		return "tab to move between fields, enter to save, esc to cancel"
	case actAddShelf, actRemoveShelf:
		return "type to filter, ↑/↓ to choose, enter to apply, esc to cancel"
	default:
		return "enter to import, esc to cancel"
	}
}
