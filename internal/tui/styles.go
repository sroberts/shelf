package tui

import "github.com/charmbracelet/lipgloss"

// Styles holds every lipgloss style the TUI uses.
//
// Colors are given as adaptive pairs so the same build reads correctly on a
// light and a dark terminal; lipgloss picks per background at runtime.
type Styles struct {
	App       lipgloss.Style
	Title     lipgloss.Style
	TabActive lipgloss.Style
	TabIdle   lipgloss.Style
	TabBar    lipgloss.Style

	Header   lipgloss.Style
	Row      lipgloss.Style
	RowAlt   lipgloss.Style
	Selected lipgloss.Style
	Cursor   lipgloss.Style

	Status  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Subtle  lipgloss.Style
	Accent  lipgloss.Style

	Box      lipgloss.Style
	Progress lipgloss.Style
	Help     lipgloss.Style
}

// Sync-state glyphs for the per-device column in the library table.
//
// Single characters chosen to be legible in a monospace column and to survive
// terminals with poor emoji support -- a book list is not the place to discover
// that your font lacks a glyph.
const (
	GlyphSynced  = "●" // on the device and up to date
	GlyphPending = "◐" // needs upload or re-upload
	GlyphAbsent  = "○" // not on the device
	GlyphOrphan  = "✕" // on the device but not placed by shelf
	GlyphUnknown = "·" // no device selected, or status not yet known
)

// DefaultStyles builds the standard theme.
func DefaultStyles() Styles {
	var (
		accent    = lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#B39DFF"}
		subtle    = lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#8A8A8A"}
		success   = lipgloss.AdaptiveColor{Light: "#1F7A3D", Dark: "#5BD97E"}
		warning   = lipgloss.AdaptiveColor{Light: "#8A6100", Dark: "#F2C14E"}
		errorCol  = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#FF7B72"}
		selBg     = lipgloss.AdaptiveColor{Light: "#E6E0FF", Dark: "#2E2A45"}
		cursorBg  = lipgloss.AdaptiveColor{Light: "#D4C9FF", Dark: "#4A3F7A"}
		headerCol = lipgloss.AdaptiveColor{Light: "#2B2B2B", Dark: "#E4E4E4"}
		border    = lipgloss.AdaptiveColor{Light: "#C9C9C9", Dark: "#4A4A4A"}
	)

	return Styles{
		App:   lipgloss.NewStyle().Padding(0, 1),
		Title: lipgloss.NewStyle().Bold(true).Foreground(accent),

		TabActive: lipgloss.NewStyle().
			Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(accent).
			Padding(0, 2),
		TabIdle: lipgloss.NewStyle().Foreground(subtle).Padding(0, 2),
		TabBar:  lipgloss.NewStyle().MarginBottom(1),

		Header: lipgloss.NewStyle().Bold(true).Foreground(headerCol).
			BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(border),
		Row:      lipgloss.NewStyle(),
		RowAlt:   lipgloss.NewStyle(),
		Selected: lipgloss.NewStyle().Background(selBg),
		Cursor:   lipgloss.NewStyle().Background(cursorBg).Bold(true),

		Status:  lipgloss.NewStyle().Foreground(subtle),
		Success: lipgloss.NewStyle().Foreground(success),
		Warning: lipgloss.NewStyle().Foreground(warning),
		Error:   lipgloss.NewStyle().Foreground(errorCol),
		Subtle:  lipgloss.NewStyle().Foreground(subtle),
		Accent:  lipgloss.NewStyle().Foreground(accent),

		Box: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).BorderForeground(border).
			Padding(0, 1),
		Progress: lipgloss.NewStyle().Foreground(accent),
		Help:     lipgloss.NewStyle().Foreground(subtle).MarginTop(1),
	}
}

// GlyphStyle colors a sync-state glyph.
func (s Styles) GlyphStyle(glyph string) lipgloss.Style {
	switch glyph {
	case GlyphSynced:
		return s.Success
	case GlyphPending:
		return s.Warning
	case GlyphOrphan:
		return s.Error
	default:
		return s.Subtle
	}
}
