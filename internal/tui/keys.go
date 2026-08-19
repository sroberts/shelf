package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap is the global keymap. Vim-first, discoverable through bubbles/help.
//
// Bindings live in one place rather than scattered across screens so the help
// view cannot drift out of sync with what the keys actually do.
type KeyMap struct {
	// Navigation
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	PageUp   key.Binding
	PageDown key.Binding

	// Screens
	NextScreen key.Binding
	PrevScreen key.Binding
	Library    key.Binding
	Devices    key.Binding
	SyncScreen key.Binding

	// Selection
	Select    key.Binding
	VisualSel key.Binding
	SelectAll key.Binding
	ClearSel  key.Binding

	// Detail panel
	Detail     key.Binding
	DetailUp   key.Binding
	DetailDown key.Binding

	// Actions
	Filter  key.Binding
	Sync    key.Binding
	SyncAll key.Binding
	Refresh key.Binding
	Confirm key.Binding
	Cancel  key.Binding

	// Meta
	Help key.Binding
	Quit key.Binding
}

// DefaultKeyMap returns the standard bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k/↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j/↓", "down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "bottom"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u", "pgup"),
			key.WithHelp("ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d", "pgdown"),
			key.WithHelp("ctrl+d", "page down"),
		),

		NextScreen: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next screen"),
		),
		PrevScreen: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("⇧tab", "prev screen"),
		),
		Library: key.NewBinding(
			key.WithKeys("1"),
			key.WithHelp("1", "library"),
		),
		Devices: key.NewBinding(
			key.WithKeys("2"),
			key.WithHelp("2", "devices"),
		),
		SyncScreen: key.NewBinding(
			key.WithKeys("3"),
			key.WithHelp("3", "sync"),
		),

		Select: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle selection"),
		),
		VisualSel: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "visual select"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "select all"),
		),
		ClearSel: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "clear selection"),
		),

		Detail: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "toggle details"),
		),
		DetailUp: key.NewBinding(
			key.WithKeys("shift+up"),
			key.WithHelp("⇧↑", "scroll details"),
		),
		DetailDown: key.NewBinding(
			key.WithKeys("shift+down"),
			key.WithHelp("⇧↓", "scroll details"),
		),

		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		Sync: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "sync selection"),
		),
		SyncAll: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "sync everything"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("enter", "y"),
			key.WithHelp("enter", "confirm"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),

		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp is the one-line help strip.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Filter, k.Select, k.Detail, k.Sync, k.Help, k.Quit}
}

// FullHelp is the expanded help, grouped by column.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.PageUp, k.PageDown},
		{k.NextScreen, k.PrevScreen, k.Library, k.Devices, k.SyncScreen},
		{k.Select, k.VisualSel, k.SelectAll, k.ClearSel, k.Filter},
		{k.Detail, k.DetailUp, k.DetailDown, k.Refresh},
		{k.Sync, k.SyncAll, k.Confirm, k.Cancel, k.Help, k.Quit},
	}
}
