package tui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// detailModel is the right-hand panel: everything known about the book under
// the cursor.
//
// It exists because the table can only afford five columns, and the fields it
// has to drop — tags, publisher, language, the actual path on disk — are the
// ones you go looking for when a book is not where you expect it to be.
//
// Held in a viewport so a book with thirty tags scrolls rather than pushing the
// path off the bottom.
type detailModel struct {
	styles Styles

	vp  viewport.Model
	bar progress.Model

	graphics GraphicsMode

	book      *library.Book
	syncGlyph string
	readPct   float64
	hasRead   bool

	width, height int
	visible       bool

	// covers caches rendered cover art by content hash and cell size.
	//
	// Rendering means decoding a PNG and rescaling it, which is cheap enough
	// once and wasteful sixty times a second while a key is held down. The key
	// includes the size because a resize invalidates the pixels, not just the
	// layout.
	covers map[string]string
}

// newDetailModel builds the panel with cover art off.
//
// Graphics are opted into by the root model after terminal detection rather
// than detected here. That keeps the zero value safe for the golden tests,
// which compare ANSI-stripped frames and would otherwise pick up whatever
// escape sequences the CI terminal happened to support.
func newDetailModel(styles Styles, graphics GraphicsMode) detailModel {
	bar := progress.New(
		progress.WithDefaultGradient(),
		progress.WithoutPercentage(),
	)

	return detailModel{
		styles:   styles,
		vp:       viewport.New(0, 0),
		bar:      bar,
		graphics: graphics,
		visible:  true,
		covers:   map[string]string{},
	}
}

// minWidthForDetail is the narrowest terminal that gets a side panel.
//
// Below this the table loses more than the panel adds: a title column under
// about thirty cells stops being readable, and a squeezed panel cannot hold a
// wrapped path either. Both would be bad rather than one being good.
const minWidthForDetail = 90

// detailWidth is how wide the panel should be for a given terminal width.
func detailWidth(total int) int {
	if total < minWidthForDetail {
		return 0
	}
	w := total * 32 / 100
	return clamp(w, 30, 46)
}

func (m *detailModel) setSize(w, h int) {
	m.width, m.height = w, h
	// The viewport renders inside the panel border and padding.
	m.vp.Width = max(1, w-4)
	m.vp.Height = max(1, h-2)
	m.bar.Width = max(6, m.vp.Width-6)
	m.refresh()
}

// setBook points the panel at a different book.
func (m *detailModel) setBook(b *library.Book, glyph string, pct float64, hasRead bool) {
	same := m.book != nil && b != nil && m.book.Path == b.Path
	m.book, m.syncGlyph, m.readPct, m.hasRead = b, glyph, pct, hasRead
	if !same {
		m.vp.GotoTop()
	}
	m.refresh()
}

func (m *detailModel) refresh() {
	if m.width <= 0 {
		return
	}
	m.vp.SetContent(m.content())
}

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// View renders the panel, or nothing when it is hidden or there is no room.
func (m detailModel) View() string {
	if !m.visible || m.width <= 0 {
		return ""
	}
	if m.book == nil {
		return m.styles.Panel.Width(m.width - 2).Height(m.height - 2).
			Render(m.styles.Subtle.Render("no book selected"))
	}

	body := m.vp.View()
	if m.vp.TotalLineCount() > m.vp.Height {
		// Say the panel has more in it. Without this the cut is invisible and
		// reads as missing metadata rather than as scrollable content.
		body += "\n" + m.styles.Subtle.Render(
			fmt.Sprintf("  ⇧↑/⇧↓  %d%%", int(m.vp.ScrollPercent()*100)))
	}

	return m.styles.Panel.Width(m.width - 2).Height(m.height - 2).Render(body)
}

// content builds the panel body.
func (m detailModel) content() string {
	b := m.book
	if b == nil {
		return ""
	}
	w := m.vp.Width

	var sections []string

	if cover := m.cover(); cover != "" {
		sections = append(sections, cover)
	}

	// Title and author, wrapped rather than truncated: this is the one place
	// in the UI that shows a long title in full.
	head := m.styles.DetailTitle.Width(w).Render(b.DisplayTitle())
	if a := b.DisplayAuthor(); a != "" {
		head += "\n" + m.styles.DetailAuthor.Width(w).Render(a)
	}
	if b.Series != "" {
		series := b.Series
		if b.SeriesIndex != 0 {
			series += " #" + strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64)
		}
		head += "\n" + m.styles.Accent.Width(w).Render(series)
	}
	sections = append(sections, head)

	if m.hasRead {
		sections = append(sections, m.readingProgress())
	}
	if s := m.syncLine(); s != "" {
		sections = append(sections, s)
	}
	if len(b.Tags) > 0 {
		sections = append(sections, m.tagChips(w))
	}

	sections = append(sections, m.fields())

	// The path last and dimmest. It is the answer to "which file is this",
	// which is asked rarely but is the only thing that resolves an ambiguity
	// between two books with the same title.
	sections = append(sections, m.styles.Subtle.Width(w).Render(b.Path))

	return strings.Join(sections, "\n\n")
}

// readingProgress draws the position bar.
func (m detailModel) readingProgress() string {
	label := m.styles.StatLabel.Render("READING")
	pct := kosync.FormatPercent(m.readPct)
	return label + "  " + m.styles.StatValue.Render(pct) + "\n" +
		m.bar.ViewAs(clampFloat(m.readPct/100, 0, 1))
}

// syncLine says where this book stands against the device, in words.
//
// The table shows the same thing as a glyph, which is right for a column and
// useless the first time you see it. Here there is room to say what it means.
func (m detailModel) syncLine() string {
	var text string
	switch m.syncGlyph {
	case GlyphSynced:
		text = "on the device, up to date"
	case GlyphPending:
		text = "needs upload"
	case GlyphAbsent:
		text = "not on the device"
	case GlyphOrphan:
		text = "on the device, not placed by shelf"
	default:
		return ""
	}
	return m.styles.GlyphStyle(m.syncGlyph).Render(m.syncGlyph) + " " +
		m.styles.Subtle.Render(text)
}

// tagChips renders tags as padded chips, wrapping at the panel width.
func (m detailModel) tagChips(width int) string {
	var lines []string
	var line string

	for _, t := range m.book.Tags {
		chip := m.styles.Chip.Render(t)
		switch {
		case line == "":
			line = chip
		case lipgloss.Width(line)+1+lipgloss.Width(chip) <= width:
			line += " " + chip
		default:
			lines = append(lines, line)
			line = chip
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// fields is the label/value block.
func (m detailModel) fields() string {
	b := m.book

	rows := [][2]string{
		{"FORMAT", strings.ToUpper(string(b.Format))},
		{"SIZE", humanSize(b.Size)},
	}
	if b.AddedUnix > 0 {
		rows = append(rows, [2]string{"ADDED", time.Unix(b.AddedUnix, 0).Format("2006-01-02")})
	}
	if b.Language != "" {
		rows = append(rows, [2]string{"LANGUAGE", b.Language})
	}
	if b.Publisher != "" {
		rows = append(rows, [2]string{"PUBLISHER", b.Publisher})
	}
	if b.PubDate != "" {
		rows = append(rows, [2]string{"PUBLISHED", b.PubDate})
	}
	for _, scheme := range []string{"isbn", "asin", "doi"} {
		if v := b.Identifiers[scheme]; v != "" {
			rows = append(rows, [2]string{strings.ToUpper(scheme), v})
		}
	}

	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, lipgloss.Width(r[0]))
	}
	valueWidth := max(1, m.vp.Width-labelWidth-2)

	var out []string
	for _, r := range rows {
		out = append(out,
			m.styles.StatLabel.Render(pad(r[0], labelWidth))+"  "+
				m.styles.StatValue.Render(truncateWidth(r[1], valueWidth)))
	}
	return strings.Join(out, "\n")
}

// cover renders the cover art, cached.
//
// Returns empty when the book has no cover or the terminal cannot draw one, and
// the panel simply starts at the title — an empty box where a cover should be
// looks like a failure, whereas no box looks like a book without a cover.
func (m detailModel) cover() string {
	b := m.book
	if len(b.Cover) == 0 || m.graphics == GraphicsNone {
		return ""
	}

	cols := min(m.vp.Width, 20)
	rows := cols / 2
	if cols < 6 || rows < 3 {
		return ""
	}

	key := fmt.Sprintf("%s/%dx%d", b.SHA256, cols, rows)
	if cached, ok := m.covers[key]; ok {
		return cached
	}

	img, _, err := image.Decode(bytes.NewReader(b.Cover))
	if err != nil {
		m.covers[key] = ""
		return ""
	}

	out := RenderCover(img, m.graphics, cols, rows)
	m.covers[key] = out
	return out
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
