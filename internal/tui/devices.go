package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/device"
	"github.com/sroberts/shelf/internal/sdcard"
	"github.com/sroberts/shelf/internal/target"
)

// devicesModel shows configured and discovered devices with live status.
type devicesModel struct {
	app    *App
	keys   KeyMap
	styles Styles

	entries []deviceEntry
	cursor  int

	probing       bool
	discovering   bool
	width, height int

	// form is the add-device form while it is open. Nil the rest of the time,
	// which is also what tells the root model to stop treating keys as global
	// commands: typing a nickname with a "q" in it must not quit.
	form *deviceForm
}

// deviceEntry is one row: a configured device, or one found by discovery.
type deviceEntry struct {
	Nickname   string
	Host       string
	Root       string
	Transport  string
	Discovered bool

	Status *device.Status
	Err    error

	// Local marks a filesystem-backed target. It gets its own rendering
	// because a card answers none of the questions the status line asks: no
	// firmware, no signal, no heap, no uptime.
	Local bool

	// Compat carries the firmware gate result, so an unsupported major is
	// visible before a sync is attempted rather than after.
	Compat  error
	Warning string
}

func newDevicesModel(app *App, keys KeyMap, styles Styles) devicesModel {
	return devicesModel{app: app, keys: keys, styles: styles, probing: true}
}

type (
	devicesProbedMsg struct{ entries []deviceEntry }
	discoveredMsg    struct{ found []device.Discovered }

	// deviceSavedMsg carries the device list as config.toml now loads, or the
	// reason the device was not saved.
	deviceSavedMsg struct {
		nickname string
		file     string
		devices  []config.Device
		err      error
	}
)

// saveDevice appends the device to config.toml off the UI thread.
func saveDevice(file string, paths config.Paths, d config.Device) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.AppendDevice(file, paths, d)
		if err != nil {
			return deviceSavedMsg{nickname: d.Nickname, err: err}
		}
		return deviceSavedMsg{nickname: d.Nickname, file: file, devices: cfg.Devices}
	}
}

// probe polls every configured device concurrently.
//
// Concurrency is safe here in a way it is not for uploads: these are small
// independent GETs to different hosts, and the one-transfer-at-a-time rule
// applies per device to uploads, not to status polls.
func (m devicesModel) probe(ctx context.Context) tea.Cmd {
	devices := m.app.Config.Devices

	return func() tea.Msg {
		entries := make([]deviceEntry, len(devices))

		type result struct {
			i int
			e deviceEntry
		}
		ch := make(chan result, len(devices))

		for i, d := range devices {
			go func(i int, d config.Device) {
				ch <- result{i, probeOne(ctx, d)}
			}(i, d)
		}
		for range devices {
			r := <-ch
			entries[r.i] = r.e
		}
		return devicesProbedMsg{entries}
	}
}

func probeOne(ctx context.Context, d config.Device) deviceEntry {
	e := deviceEntry{
		Nickname:  d.Nickname,
		Host:      d.Host,
		Root:      d.Root,
		Transport: string(d.Transport),
	}

	if d.Transport == config.TransportSD {
		// A card has no status endpoint, so "reachable" means mounted and
		// plausibly the right volume. Saying so beats echoing the configured
		// mount path back, which reveals nothing about whether the card is
		// actually in the reader.
		e.Host = target.Label(d)
		if d.Mount == "" {
			e.Err = errors.New("no mount configured")
			return e
		}
		vol, err := sdcard.Open(d.Mount)
		if err != nil {
			e.Err = err
			return e
		}
		if err := vol.Verify(""); err != nil {
			e.Err = err
			return e
		}
		e.Local = true
		return e
	}

	host := d.Host
	if host == "" {
		e.Err = errors.New("no host configured; press r to discover")
		return e
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	status, err := device.New(host).Status(ctx)
	if err != nil {
		e.Err = err
		return e
	}

	e.Status = status
	e.Compat = device.CheckCompat(status)
	e.Warning = device.CompatWarning(status)
	return e
}

// discover broadcasts on the local network.
func (m devicesModel) discover(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		found, err := device.Discover(ctx, 4*time.Second)
		if err != nil {
			return errMsg{err}
		}
		return discoveredMsg{found}
	}
}

func (m *devicesModel) setSize(w, h int) { m.width, m.height = w, h }

func (m *devicesModel) setStyles(styles Styles) {
	m.styles = styles
	if m.form != nil {
		m.form.setStyles(styles)
	}
}

func (m devicesModel) capturing() bool { return m.form != nil }

func (m devicesModel) Update(ctx context.Context, msg tea.Msg) (devicesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case devicesProbedMsg:
		m.probing = false
		m.entries = msg.entries
		return m, nil

	case discoveredMsg:
		m.discovering = false
		m.mergeDiscovered(msg.found)
		return m, setStatus("discovery found %d device(s)", len(msg.found))

	case deviceSavedMsg:
		if msg.err != nil {
			if m.form != nil {
				m.form.saving = false
				m.form.err = msg.err.Error()
			}
			return m, nil
		}
		m.form = nil
		// Swap in a copy rather than editing the shared config in place: a
		// command already running holds the old pointer and must keep seeing
		// a consistent value. Only the device list is taken from the reload,
		// so a --library override on this run is not lost.
		next := *m.app.Config
		next.Devices = msg.devices
		m.app.Config = &next
		m.probing = true
		m.cursor = max(0, len(msg.devices)-1)
		return m, tea.Batch(m.probe(ctx),
			setStatus("added %q to %s", msg.nickname, msg.file))

	case tea.KeyPressMsg:
		if m.form != nil {
			return m.updateForm(msg)
		}
		switch {
		case key.Matches(msg, m.keys.Up):
			m.cursor = clamp(m.cursor-1, 0, max(0, len(m.entries)-1))
		case key.Matches(msg, m.keys.Down):
			m.cursor = clamp(m.cursor+1, 0, max(0, len(m.entries)-1))

		case key.Matches(msg, m.keys.Refresh):
			m.probing = true
			m.discovering = true
			return m, tea.Batch(
				m.probe(ctx), m.discover(ctx),
				setStatus("probing devices and broadcasting discovery…"),
			)

		case key.Matches(msg, m.keys.AddDevice):
			var from *deviceEntry
			if m.cursor < len(m.entries) && m.entries[m.cursor].Discovered {
				e := m.entries[m.cursor]
				from = &e
			}
			m.form = newDeviceForm(m.styles, from)
			return m, textinput.Blink
		}

	default:
		// Cursor blinks and other input-internal messages.
		if m.form != nil {
			var cmd tea.Cmd
			m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// updateForm handles keys while the add-device form is open. Nothing here does
// I/O: saving returns a command, per the rule that Update never blocks.
func (m devicesModel) updateForm(msg tea.KeyPressMsg) (devicesModel, tea.Cmd) {
	f := m.form
	if f.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.form = nil
		return m, setStatus("add device cancelled")
	case "tab", "down":
		return m, f.move(1)
	case "shift+tab", "up":
		return m, f.move(-1)
	case "enter":
		d, err := f.device(m.app.Config.Devices)
		if err != nil {
			f.err = err.Error()
			return m, nil
		}
		f.err = ""
		f.saving = true
		return m, saveDevice(m.app.Config.File, m.app.Config.Paths, d)
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	f.err = ""
	return m, cmd
}

// mergeDiscovered adds devices found on the network that are not configured,
// and fills in the host for a configured device that had none.
func (m *devicesModel) mergeDiscovered(found []device.Discovered) {
	for _, f := range found {
		matched := false
		for i := range m.entries {
			e := &m.entries[i]
			if e.Host == f.Addr || e.Host == f.Hostname {
				matched = true
				break
			}
			if e.Host == "" {
				e.Host = f.Addr
				e.Err = nil
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		m.entries = append(m.entries, deviceEntry{
			Nickname:   f.Hostname,
			Host:       f.Addr,
			Discovered: true,
			Transport:  "ws",
		})
	}
}

func (m devicesModel) hint() string {
	switch {
	case m.probing || m.discovering:
		return "probing…"
	case m.form != nil:
		return "tab to move between fields, enter to save, esc to cancel"
	case len(m.entries) == 0:
		return "no devices configured — press n to add one, or r to discover"
	default:
		return "n to add a device, r to refresh and discover"
	}
}

func (m devicesModel) View() string {
	if m.form != nil {
		return m.form.View(m.styles)
	}
	if m.probing && len(m.entries) == 0 {
		return m.styles.Subtle.Render("probing devices…")
	}
	if len(m.entries) == 0 {
		// The status line already explains what to do, so this stays short.
		return m.styles.Subtle.Render("No devices configured or discovered.")
	}

	var b strings.Builder
	for i, e := range m.entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.renderEntry(e, i == m.cursor))
	}
	return b.String()
}

func (m devicesModel) renderEntry(e deviceEntry, focused bool) string {
	var b strings.Builder

	name := e.Nickname
	if name == "" {
		name = e.Host
	}
	title := name
	if e.Discovered {
		title += m.styles.Subtle.Render("  (discovered, not in config)")
	}

	marker := "  "
	if focused {
		marker = m.styles.Accent.Render("▸ ")
	}
	b.WriteString(marker + m.styles.Title.Render(title) + "\n")

	host := e.Host
	if host == "" {
		host = "(discovery)"
	}
	b.WriteString(fmt.Sprintf("    %s  %s  root %s\n",
		m.styles.Subtle.Render(host),
		m.styles.Subtle.Render(e.Transport),
		m.styles.Subtle.Render(e.Root)))

	switch {
	case e.Err != nil:
		// "Asleep or not in transfer mode" is by far the most common state and
		// deserves plain language rather than a raw transport error.
		if errors.Is(e.Err, device.ErrNotInTransfer) {
			b.WriteString("    " + m.styles.Warning.Render(
				"asleep, or not in File Transfer mode") + "\n")
		} else {
			b.WriteString("    " + m.styles.Error.Render(e.Err.Error()) + "\n")
		}

	case e.Local:
		b.WriteString("    " + m.styles.Success.Render("SD card mounted") + "   " +
			m.styles.Subtle.Render("no firmware in play") + "\n")

	case e.Status != nil:
		s := e.Status
		b.WriteString(fmt.Sprintf("    %s %s   %s   rssi %s   heap %s   up %s\n",
			m.styles.Success.Render(s.Device),
			m.styles.Success.Render(s.Version),
			s.Mode,
			signalLabel(s.RSSI),
			humanSize(s.FreeHeap),
			(time.Duration(s.Uptime) * time.Second).String()))

		if e.Compat != nil {
			b.WriteString("    " + m.styles.Error.Render("incompatible: "+e.Compat.Error()) + "\n")
		} else if e.Warning != "" {
			b.WriteString("    " + m.styles.Warning.Render(e.Warning) + "\n")
		}

	default:
		b.WriteString("    " + m.styles.Subtle.Render("not probed") + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// signalLabel renders RSSI with a plain-language qualifier. -90 dBm is a real
// link that will still transfer, just slowly, and saying so beats a bare number.
func signalLabel(rssi int) string {
	if rssi == 0 {
		return "n/a"
	}
	quality := "weak"
	switch {
	case rssi > -60:
		quality = "good"
	case rssi > -75:
		quality = "fair"
	}
	return fmt.Sprintf("%d dBm (%s)", rssi, quality)
}
