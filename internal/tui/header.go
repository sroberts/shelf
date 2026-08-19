package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sroberts/shelf/internal/library"
)

// The header is two lines: identity and tabs, then a summary of what the
// library holds. It is always visible, on every screen, because the two
// questions it answers — where am I, and how big is this — are the ones you ask
// on arrival and then keep needing.
//
// Everything it shows comes from state the root model already has. It issues no
// queries of its own: it redraws on every keystroke, and a header that costs a
// round trip per frame is a header that makes scrolling feel slow.

// statsMsg carries a recomputed library summary.
type statsMsg struct{ stats library.Stats }

// loadStats recomputes the header summary off the UI thread.
func (m Model) loadStats() tea.Cmd {
	db := m.app.DB
	return func() tea.Msg {
		s, err := db.Stats()
		if err != nil {
			// A header that cannot count is not worth an error banner over the
			// whole screen; the summary line simply stays empty.
			return statsMsg{}
		}
		return statsMsg{s}
	}
}

// header renders both lines plus the rule beneath them.
func (m Model) header() string {
	width := m.width - 2 // App padding
	if width < 20 {
		width = 20
	}

	top := m.headerTabs(width)
	summary := m.headerSummary(width)

	rule := m.styles.Rule.Render(strings.Repeat("─", width))
	return lipgloss.JoinVertical(lipgloss.Left, top, summary, rule)
}

// headerTabs is the identity and navigation line.
//
// The active-screen badge is what tells you where you are; the right side is
// reserved for state that matters regardless of screen, which today is whether
// a sync is running.
func (m Model) headerTabs(width int) string {
	var tabs []string
	for i, s := range screens {
		label := fmt.Sprintf("%d %s", i+1, s.String())
		if s == m.screen {
			tabs = append(tabs, m.styles.TabActive.Render(label))
		} else {
			tabs = append(tabs, m.styles.TabIdle.Render(label))
		}
	}

	left := lipgloss.JoinHorizontal(lipgloss.Top,
		m.styles.Title.Render(" shelf "),
		" ",
		lipgloss.JoinHorizontal(lipgloss.Top, tabs...),
	)

	right := m.headerActivity()

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// No room for both. The tabs win: knowing which screen you are on
		// matters more than a spinner.
		return truncateWidth(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// headerActivity is the right-hand indicator.
func (m Model) headerActivity() string {
	if m.sync.running() {
		return m.styles.Accent.Render(m.spinner.View() + " syncing")
	}
	if m.library.loading {
		return m.styles.Subtle.Render(m.spinner.View() + " loading")
	}
	if n := len(m.app.Config.Devices); n > 0 {
		return m.styles.Subtle.Render(m.app.Config.Devices[0].Nickname + " ")
	}
	return m.styles.Subtle.Render("no device ")
}

// headerSummary is the library-wide line.
//
// Ordered by how often it is the thing you wanted: how many books, then how
// they break down, then how much disk. Sync counts come last because they are
// only meaningful once a device is configured, and are dropped entirely when
// one is not.
func (m Model) headerSummary(width int) string {
	s := m.stats
	if s.Books == 0 {
		return m.styles.Subtle.Render(" library is empty — run 'shelf scan'")
	}

	sep := m.styles.Subtle.Render(" · ")
	var parts []string

	add := func(n int, one, many string) {
		if n == 0 {
			return
		}
		parts = append(parts, m.styles.StatValue.Render(formatCount(n))+
			m.styles.StatLabel.Render(" "+pluralWord(n, one, many)))
	}

	add(s.Books, "book", "books")
	add(s.Authors, "author", "authors")
	add(s.Series, "series", "series")
	add(s.Tags, "tag", "tags")
	parts = append(parts, m.styles.StatValue.Render(humanSize(s.Bytes)))

	left := " " + strings.Join(parts, sep)
	right := m.headerSyncCounts()

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncateWidth(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// headerSyncCounts summarises the library against the active device.
//
// Uses the same glyphs and colours as the table's state column, so the count
// and the rows it refers to read as one thing rather than two vocabularies.
func (m Model) headerSyncCounts() string {
	counts := m.library.stateCounts()
	if counts == nil {
		return ""
	}

	var parts []string
	for _, g := range []string{GlyphSynced, GlyphPending, GlyphAbsent} {
		if n := counts[g]; n > 0 {
			parts = append(parts,
				m.styles.GlyphStyle(g).Render(g)+m.styles.StatLabel.Render(formatCount(n)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " "
}

// stateCounts tallies the sync-state column.
//
// Returns nil rather than an empty map when no device is configured, so the
// header can tell "nothing to report" from "everything is at zero".
func (m libraryModel) stateCounts() map[string]int {
	if len(m.syncState) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, b := range m.books {
		if g := m.syncState[b.Path]; g != "" {
			out[g]++
		}
	}
	return out
}

// formatCount renders a count with thousands separators. A five-figure library
// is common enough that "12480" reads worse than "12,480".
func formatCount(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}

	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
