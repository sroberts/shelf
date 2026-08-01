package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
)

// libraryModel is the book table: the default screen and the place a push
// workflow starts.
type libraryModel struct {
	app    *App
	keys   KeyMap
	styles Styles

	books  []*library.Book
	cursor int
	offset int

	// selected holds book paths rather than indices, so a filter change does
	// not silently reassign what the user picked.
	selected map[string]bool

	// visual-mode range selection
	visual      bool
	visualStart int

	filter    textinput.Model
	filtering bool
	query     string

	// syncState maps a book path to a glyph for the active device.
	syncState map[string]string

	width, height int
	loading       bool
}

func newLibraryModel(app *App, keys KeyMap, styles Styles) libraryModel {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "title, author:…, tag:…, and/or/not"
	ti.CharLimit = 200

	return libraryModel{
		app:       app,
		keys:      keys,
		styles:    styles,
		selected:  map[string]bool{},
		syncState: map[string]string{},
		filter:    ti,
		loading:   true,
	}
}

// Messages produced by this screen.
type (
	booksLoadedMsg struct {
		books []*library.Book
		query string
	}
	syncStateMsg struct{ state map[string]string }
)

// load queries the index. Runs as a tea.Cmd so a large library cannot stall
// the display.
func (m libraryModel) load(ctx context.Context) tea.Cmd {
	query := m.query
	return func() tea.Msg {
		books, err := m.app.DB.Search(query, library.SearchOptions{})
		if err != nil {
			return errMsg{err}
		}
		return booksLoadedMsg{books: books, query: query}
	}
}

// computeSyncState derives the per-book glyph from the device manifest.
//
// This reads only local state (the manifest shelf saved after the last sync),
// so it is cheap and works with the device asleep. It reports what shelf
// believes, which is the useful thing to show before a sync confirms it.
func (m libraryModel) computeSyncState() tea.Cmd {
	cfg := m.app.Config
	books := m.books

	return func() tea.Msg {
		state := map[string]string{}
		if len(cfg.Devices) == 0 {
			return syncStateMsg{state}
		}

		dev := cfg.Devices[0]
		manifest, err := syncpkg.LoadManifest(
			cfg.Paths.DeviceStateFile(dev.Nickname), dev.Nickname, dev.Root)
		if err != nil {
			return syncStateMsg{state}
		}

		byLocal := manifest.ByLocalPath()
		for _, b := range books {
			entry, ok := byLocal[b.Path]
			switch {
			case !ok:
				state[b.Path] = GlyphAbsent
			case entry.SHA256 != b.SHA256:
				state[b.Path] = GlyphPending
			default:
				state[b.Path] = GlyphSynced
			}
		}
		return syncStateMsg{state}
	}
}

func (m libraryModel) capturing() bool { return m.filtering }

// setSize takes a pointer receiver deliberately: Bubble Tea child models are
// held by value, so a value receiver here would update a copy and the screen
// would silently keep whatever size it started with (zero).
func (m *libraryModel) setSize(w, h int) {
	m.width, m.height = w, h
	m.filter.Width = max(10, w-4)
}

func (m libraryModel) Update(ctx context.Context, msg tea.Msg) (libraryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.filter.Width = msg.Width - 4
		return m, nil

	case booksLoadedMsg:
		m.books = msg.books
		m.loading = false
		if m.cursor >= len(m.books) {
			m.cursor = max(0, len(m.books)-1)
		}
		return m, m.computeSyncState()

	case syncStateMsg:
		m.syncState = msg.state
		return m, nil

	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(ctx, msg)
		}
		return m.updateNormal(ctx, msg)
	}
	return m, nil
}

func (m libraryModel) updateFilter(ctx context.Context, msg tea.KeyMsg) (libraryModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filtering = false
		m.query = m.filter.Value()
		m.filter.Blur()
		m.cursor, m.offset = 0, 0
		m.loading = true
		return m, m.load(ctx)

	case "esc":
		m.filtering = false
		m.filter.SetValue(m.query)
		m.filter.Blur()
		return m, nil
	}

	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	return m, cmd
}

func (m libraryModel) updateNormal(ctx context.Context, msg tea.KeyMsg) (libraryModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink

	case key.Matches(msg, m.keys.Up):
		m.moveCursor(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveCursor(1)
	case key.Matches(msg, m.keys.Top):
		m.cursor = 0
		m.extendVisual()
	case key.Matches(msg, m.keys.Bottom):
		m.cursor = max(0, len(m.books)-1)
		m.extendVisual()
	case key.Matches(msg, m.keys.PageUp):
		m.moveCursor(-m.visibleRows())
	case key.Matches(msg, m.keys.PageDown):
		m.moveCursor(m.visibleRows())

	case key.Matches(msg, m.keys.Select):
		if b := m.current(); b != nil {
			m.selected[b.Path] = !m.selected[b.Path]
			if !m.selected[b.Path] {
				delete(m.selected, b.Path)
			}
			m.moveCursor(1)
		}

	case key.Matches(msg, m.keys.VisualSel):
		m.visual = !m.visual
		if m.visual {
			m.visualStart = m.cursor
			m.extendVisual()
		}

	case key.Matches(msg, m.keys.SelectAll):
		for _, b := range m.books {
			m.selected[b.Path] = true
		}

	case key.Matches(msg, m.keys.ClearSel):
		m.selected = map[string]bool{}
		m.visual = false

	case key.Matches(msg, m.keys.Refresh):
		m.loading = true
		return m, tea.Batch(m.load(ctx), setStatus("reloading library…"))

	case key.Matches(msg, m.keys.Sync):
		books := m.selectedBooks()
		if len(books) == 0 {
			if b := m.current(); b != nil {
				books = []*library.Book{b}
			}
		}
		if len(books) == 0 {
			return m, setStatus("nothing selected")
		}
		return m, requestSync(books)

	case key.Matches(msg, m.keys.SyncAll):
		if len(m.books) == 0 {
			return m, setStatus("library is empty")
		}
		return m, requestSync(m.books)
	}
	return m, nil
}

// requestSync hands a book set to the sync screen and focuses it.
func requestSync(books []*library.Book) tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return planRequestMsg{books: books} },
		func() tea.Msg { return switchScreenMsg(ScreenSync) },
	)
}

func (m *libraryModel) moveCursor(delta int) {
	if len(m.books) == 0 {
		return
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.books)-1)
	m.extendVisual()

	// Keep the cursor inside the viewport.
	rows := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// extendVisual keeps the visual range in step with the cursor.
func (m *libraryModel) extendVisual() {
	if !m.visual {
		return
	}
	lo, hi := m.visualStart, m.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	for i := lo; i <= hi && i < len(m.books); i++ {
		m.selected[m.books[i].Path] = true
	}
}

func (m libraryModel) current() *library.Book {
	if m.cursor < 0 || m.cursor >= len(m.books) {
		return nil
	}
	return m.books[m.cursor]
}

// selectedBooks returns the selection in table order.
func (m libraryModel) selectedBooks() []*library.Book {
	var out []*library.Book
	for _, b := range m.books {
		if m.selected[b.Path] {
			out = append(out, b)
		}
	}
	return out
}

func (m libraryModel) visibleRows() int {
	rows := m.height - 3 // header, filter line, padding
	if rows < 1 {
		return 1
	}
	return rows
}

func (m libraryModel) hint() string {
	if m.filtering {
		return "enter to apply the filter, esc to cancel"
	}
	n := len(m.selected)
	switch {
	case m.loading:
		return "loading…"
	case len(m.books) == 0 && m.query != "":
		return fmt.Sprintf("no books match %q — / to change the filter", m.query)
	case len(m.books) == 0:
		return "library is empty — run 'shelf import' or 'shelf scan'"
	case n > 0:
		return fmt.Sprintf("%d selected — s to sync them, A to clear", n)
	default:
		return fmt.Sprintf("%d books — space to select, s to sync, / to filter", len(m.books))
	}
}

func (m libraryModel) View() string {
	var b strings.Builder

	// Filter line.
	if m.filtering {
		b.WriteString(m.filter.View())
	} else if m.query != "" {
		b.WriteString(m.styles.Accent.Render("/" + m.query))
	} else {
		b.WriteString(m.styles.Subtle.Render("/ to filter"))
	}
	b.WriteString("\n")

	if m.loading {
		b.WriteString(m.styles.Subtle.Render("loading…"))
		return b.String()
	}
	if len(m.books) == 0 {
		b.WriteString(m.styles.Subtle.Render("no books"))
		return b.String()
	}

	cols := m.columns()
	b.WriteString(m.styles.Header.Render(m.renderRow(cols, "", "TITLE", "AUTHOR", "SERIES", "SIZE")))
	b.WriteString("\n")

	rows := m.visibleRows()
	end := min(m.offset+rows, len(m.books))

	for i := m.offset; i < end; i++ {
		book := m.books[i]

		glyph := m.syncState[book.Path]
		if glyph == "" {
			glyph = GlyphUnknown
		}

		marker := " "
		if m.selected[book.Path] {
			marker = "✓"
		}

		series := book.Series
		if series != "" && book.SeriesIndex != 0 {
			series = fmt.Sprintf("%s #%s", series,
				strconv.FormatFloat(book.SeriesIndex, 'f', -1, 64))
		}

		line := m.renderRow(cols,
			m.styles.GlyphStyle(glyph).Render(glyph)+marker,
			book.DisplayTitle(), book.DisplayAuthor(), series, humanSize(book.Size))

		switch {
		case i == m.cursor:
			line = m.styles.Cursor.Render(line)
		case m.selected[book.Path]:
			line = m.styles.Selected.Render(line)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// columns computes column widths for the current terminal width.
func (m libraryModel) columns() []int {
	// state(2) title author series size
	avail := m.width - 2 - 4 // glyph+marker, gutters
	if avail < 30 {
		avail = 30
	}

	size := 9
	series := avail * 20 / 100
	author := avail * 25 / 100
	title := avail - size - series - author

	if series < 8 {
		series = 0
	}
	if author < 10 {
		author = 10
	}
	if title < 12 {
		title = 12
	}
	return []int{2, title, author, series, size}
}

func (m libraryModel) renderRow(cols []int, state, title, author, series, size string) string {
	cells := []string{
		pad(state, cols[0]),
		pad(title, cols[1]),
		pad(author, cols[2]),
	}
	if cols[3] > 0 {
		cells = append(cells, pad(series, cols[3]))
	}
	cells = append(cells, padLeft(size, cols[4]))
	return strings.Join(cells, " ")
}

// displayWidth measures a string in terminal cells, so a wide rune counts as
// two. Byte or rune length would misalign every column containing CJK text.
func displayWidth(s string) int { return lipgloss.Width(s) }

// pad truncates or pads a cell to width, counting display cells rather than
// bytes so CJK titles do not break the column alignment.
func pad(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := displayWidth(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return truncateWidth(s, width)
}

func padLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := displayWidth(s)
	if w >= width {
		return truncateWidth(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}

// truncateWidth cuts a string to a display width, appending an ellipsis. It
// measures with lipgloss.Width so wide runes count as two cells.
func truncateWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := displayWidth(string(r))
		if used+rw > width-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// humanSize formats a byte count.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
