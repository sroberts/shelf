package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Styles holds every lipgloss style the TUI uses.
//
// Colors are given as light/dark pairs so the same build reads correctly on
// either kind of terminal. Which half applies is decided by DefaultStyles from
// the background the terminal reports once the program is running. Lip Gloss
// v1 asked at package init instead, which made every CLI command, `shelf
// version` included, query the terminal and wait up to five seconds for a
// reply that some terminals never send.
type Styles struct {
	// Dark records which background these styles were built for, so the
	// Bubbles components can be given their matching defaults.
	Dark bool

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

	// Header
	Rule      lipgloss.Style
	StatValue lipgloss.Style
	StatLabel lipgloss.Style

	// Detail panel
	Panel        lipgloss.Style
	DetailTitle  lipgloss.Style
	DetailAuthor lipgloss.Style
	Chip         lipgloss.Style

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

// DefaultStyles builds the standard theme for a dark or light background.
func DefaultStyles(dark bool) Styles {
	pick := lipgloss.LightDark(dark)
	pair := func(light, dark string) color.Color {
		return pick(lipgloss.Color(light), lipgloss.Color(dark))
	}
	var (
		accent    = pair("#7D56F4", "#B39DFF")
		subtle    = pair("#6C6C6C", "#8A8A8A")
		success   = pair("#1F7A3D", "#5BD97E")
		warning   = pair("#8A6100", "#F2C14E")
		errorCol  = pair("#B3261E", "#FF7B72")
		selBg     = pair("#E6E0FF", "#2E2A45")
		cursorBg  = pair("#D4C9FF", "#4A3F7A")
		headerCol = pair("#2B2B2B", "#E4E4E4")
		border    = pair("#C9C9C9", "#4A4A4A")
	)

	return Styles{
		Dark: dark,

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

		// The rule under the header is drawn rather than bordered, so it spans
		// the full width regardless of what the two lines above it contain.
		Rule:      lipgloss.NewStyle().Foreground(border),
		StatValue: lipgloss.NewStyle().Bold(true).Foreground(headerCol),
		StatLabel: lipgloss.NewStyle().Foreground(subtle),

		Panel: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).BorderForeground(border).
			Padding(0, 1),
		DetailTitle:  lipgloss.NewStyle().Bold(true).Foreground(headerCol),
		DetailAuthor: lipgloss.NewStyle().Foreground(subtle),
		Chip: lipgloss.NewStyle().
			Foreground(accent).Background(selBg).Padding(0, 1),

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
