// Package tui is the terminal frontend.
//
// It is a frontend on the same internal API the CLI uses, not a place where
// logic lives: every behaviour here is reachable headless. Two rules keep it
// honest, both from the spec:
//
//   - No blocking I/O in Update. Every device call and every disk scan returns
//     a tea.Cmd, so the display never stalls behind a slow Wi-Fi link.
//   - Nothing writes to stdout or stderr. Those belong to the renderer; logs go
//     to a file under XDG_STATE_HOME.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/library"
)

// Screen identifies a top-level view.
type Screen int

const (
	ScreenLibrary Screen = iota
	ScreenDevices
	ScreenSync
)

// screens is the tab order.
var screens = []Screen{ScreenLibrary, ScreenDevices, ScreenSync}

func (s Screen) String() string {
	switch s {
	case ScreenLibrary:
		return "Library"
	case ScreenDevices:
		return "Devices"
	case ScreenSync:
		return "Sync"
	}
	return "?"
}

// App is the shared state the screens read.
type App struct {
	Config *config.Config
	DB     *library.DB
	Log    *slog.Logger
}

// Model is the root tea.Model.
type Model struct {
	app    *App
	keys   KeyMap
	styles Styles
	help   help.Model

	screen  Screen
	width   int
	height  int
	ready   bool
	showAll bool // expanded help

	library libraryModel
	devices devicesModel
	sync    syncModel

	// ctx is cancelled on quit. Long-running work watches it, which is what
	// makes "quit waits for the current upload" true rather than aspirational.
	ctx    context.Context
	cancel context.CancelFunc

	status   string
	err      error
	quitting bool
}

// New builds the root model.
func New(ctx context.Context, app *App) Model {
	ctx, cancel := context.WithCancel(ctx)

	keys := DefaultKeyMap()
	styles := DefaultStyles()

	h := help.New()
	h.ShowAll = false

	return Model{
		app:     app,
		keys:    keys,
		styles:  styles,
		help:    h,
		screen:  ScreenLibrary,
		ctx:     ctx,
		cancel:  cancel,
		library: newLibraryModel(app, keys, styles),
		devices: newDevicesModel(app, keys, styles),
		sync:    newSyncModel(app, keys, styles),
	}
}

// Init starts the initial loads.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.library.load(m.ctx),
		m.devices.probe(m.ctx),
	)
}

// Messages shared across screens.
type (
	// statusMsg sets the status line.
	statusMsg string
	// errMsg reports a failure without tearing the UI down.
	errMsg struct{ err error }
	// switchScreenMsg moves focus, used when a screen hands off to another.
	switchScreenMsg Screen
)

func reportErr(err error) tea.Cmd {
	if err == nil {
		return nil
	}
	return func() tea.Msg { return errMsg{err} }
}

func setStatus(format string, args ...any) tea.Cmd {
	return func() tea.Msg { return statusMsg(fmt.Sprintf(format, args...)) }
}

// Update routes messages. It performs no I/O itself.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.help.Width = msg.Width
		m.propagateSize()
		return m, nil

	case statusMsg:
		m.status = string(msg)
		m.err = nil
		return m, nil

	case errMsg:
		m.err = msg.err
		m.app.Log.Error("ui error", "err", msg.err)
		return m, nil

	case switchScreenMsg:
		m.screen = Screen(msg)
		return m, nil

	case tea.KeyMsg:
		// A screen that is capturing text input (the filter box, a
		// confirmation prompt) gets first refusal on every key, or typing "q"
		// into a search would quit the program.
		if m.captureKeys() {
			return m.routeToScreen(msg)
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m.beginQuit()

		case key.Matches(msg, m.keys.Help):
			m.showAll = !m.showAll
			m.help.ShowAll = m.showAll
			m.propagateSize()
			return m, nil

		case key.Matches(msg, m.keys.NextScreen):
			m.screen = screens[(m.indexOfScreen()+1)%len(screens)]
			return m, nil

		case key.Matches(msg, m.keys.PrevScreen):
			i := m.indexOfScreen() - 1
			if i < 0 {
				i = len(screens) - 1
			}
			m.screen = screens[i]
			return m, nil

		case key.Matches(msg, m.keys.Library):
			m.screen = ScreenLibrary
			return m, nil
		case key.Matches(msg, m.keys.Devices):
			m.screen = ScreenDevices
			return m, nil
		case key.Matches(msg, m.keys.SyncScreen):
			m.screen = ScreenSync
			return m, nil
		}
	}

	updated, cmd := m.routeToScreen(msg)
	cmds = append(cmds, cmd)
	return updated, tea.Batch(cmds...)
}

// routeToScreen forwards a message to every child, so background results land
// even when their screen is not focused.
func (m Model) routeToScreen(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Key messages go only to the focused screen; everything else is broadcast,
	// because a device probe that finishes while the library is showing still
	// has to update the devices model.
	if _, isKey := msg.(tea.KeyMsg); isKey {
		switch m.screen {
		case ScreenLibrary:
			var cmd tea.Cmd
			m.library, cmd = m.library.Update(m.ctx, msg)
			cmds = append(cmds, cmd)
		case ScreenDevices:
			var cmd tea.Cmd
			m.devices, cmd = m.devices.Update(m.ctx, msg)
			cmds = append(cmds, cmd)
		case ScreenSync:
			var cmd tea.Cmd
			m.sync, cmd = m.sync.Update(m.ctx, msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	m.library, cmd = m.library.Update(m.ctx, msg)
	cmds = append(cmds, cmd)
	m.devices, cmd = m.devices.Update(m.ctx, msg)
	cmds = append(cmds, cmd)
	m.sync, cmd = m.sync.Update(m.ctx, msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// captureKeys reports whether the focused screen wants raw key input.
func (m Model) captureKeys() bool {
	switch m.screen {
	case ScreenLibrary:
		return m.library.capturing()
	case ScreenSync:
		return m.sync.capturing()
	}
	return false
}

// beginQuit cancels outstanding work and leaves.
//
// A sync in flight finishes its current file rather than being killed
// mid-upload: the device deletes a partial file on disconnect, so tearing the
// connection down would leave the book absent rather than merely stale.
func (m Model) beginQuit() (tea.Model, tea.Cmd) {
	m.quitting = true

	if m.sync.running() {
		m.cancel()
		m.status = "finishing the current upload, then quitting…"
		return m, m.sync.waitForStop()
	}

	m.cancel()
	return m, tea.Quit
}

func (m Model) indexOfScreen() int {
	for i, s := range screens {
		if s == m.screen {
			return i
		}
	}
	return 0
}

// propagateSize gives each child its content area.
func (m *Model) propagateSize() {
	// Chrome: tab bar (2 lines incl. margin), status line, help.
	helpHeight := 1
	if m.showAll {
		helpHeight = 6
	}
	contentHeight := m.height - 2 - 1 - helpHeight - 1
	if contentHeight < 3 {
		contentHeight = 3
	}
	contentWidth := m.width - 2
	if contentWidth < 20 {
		contentWidth = 20
	}

	m.library.setSize(contentWidth, contentHeight)
	m.devices.setSize(contentWidth, contentHeight)
	m.sync.setSize(contentWidth, contentHeight)
}

// View renders the frame.
func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	if m.quitting && !m.sync.running() {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.tabBar())
	b.WriteString("\n")
	b.WriteString(m.body())
	b.WriteString("\n")
	b.WriteString(m.statusLine())
	b.WriteString("\n")
	b.WriteString(m.styles.Help.Render(m.help.View(m.keys)))

	return m.styles.App.Render(b.String())
}

func (m Model) tabBar() string {
	var tabs []string
	for _, s := range screens {
		label := s.String()
		if s == ScreenSync && m.sync.running() {
			label += " ⟳"
		}
		if s == m.screen {
			tabs = append(tabs, m.styles.TabActive.Render(label))
		} else {
			tabs = append(tabs, m.styles.TabIdle.Render(label))
		}
	}

	bar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	title := m.styles.Title.Render("shelf")
	return m.styles.TabBar.Render(lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", bar))
}

func (m Model) body() string {
	switch m.screen {
	case ScreenLibrary:
		return m.library.View()
	case ScreenDevices:
		return m.devices.View()
	case ScreenSync:
		return m.sync.View()
	}
	return ""
}

func (m Model) statusLine() string {
	if m.err != nil {
		return m.styles.Error.Render("error: " + m.err.Error())
	}
	if m.status != "" {
		return m.styles.Status.Render(m.status)
	}
	return m.styles.Status.Render(m.contextHint())
}

// contextHint explains what the focused screen expects next.
func (m Model) contextHint() string {
	switch m.screen {
	case ScreenLibrary:
		return m.library.hint()
	case ScreenDevices:
		return m.devices.hint()
	case ScreenSync:
		return m.sync.hint()
	}
	return ""
}
