package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/sroberts/shelf/internal/config"
)

// deviceForm collects a network device to add to config.toml.
//
// Network devices only: an SD-card target needs a mount path that is easier to
// get right in the file than in a three-field form, and the empty-state hint
// already points there.
type deviceForm struct {
	inputs [3]textinput.Model
	focus  int
	err    string
	saving bool
}

const (
	fieldNickname = iota
	fieldHost
	fieldRoot
)

var deviceFormLabels = [...]string{"nickname", "host", "root"}

// newDeviceForm opens the form, prefilled from a discovered device when there
// is one under the cursor.
func newDeviceForm(styles Styles, from *deviceEntry) *deviceForm {
	f := &deviceForm{}
	placeholders := [...]string{"reader", "192.168.1.42 or crosspoint.local", "/Books"}
	for i := range f.inputs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholders[i]
		ti.CharLimit = 200
		ti.SetWidth(40)
		f.inputs[i] = ti
	}
	f.inputs[fieldRoot].SetValue("/Books")
	if from != nil {
		f.inputs[fieldNickname].SetValue(from.Nickname)
		f.inputs[fieldHost].SetValue(from.Host)
	}
	f.setStyles(styles)
	f.inputs[f.focus].Focus()
	return f
}

func (f *deviceForm) setStyles(styles Styles) {
	ts := textinput.DefaultStyles(styles.Dark)
	ts.Focused.Text = styles.StatValue
	for i := range f.inputs {
		f.inputs[i].SetStyles(ts)
	}
}

// move shifts focus by delta, wrapping, so tab and shift+tab cycle the fields.
func (f *deviceForm) move(delta int) tea.Cmd {
	f.inputs[f.focus].Blur()
	f.focus = (f.focus + delta + len(f.inputs)) % len(f.inputs)
	return f.inputs[f.focus].Focus()
}

// device builds the entry the form describes, checking what can be checked
// without I/O. Duplicate nicknames are caught here for a clear message; the
// config loader checks them again when the file is written.
func (f *deviceForm) device(existing []config.Device) (config.Device, error) {
	d := config.Device{
		Nickname: strings.TrimSpace(f.inputs[fieldNickname].Value()),
		Host:     strings.TrimSpace(f.inputs[fieldHost].Value()),
		Root:     strings.TrimSpace(f.inputs[fieldRoot].Value()),
	}
	switch {
	case d.Nickname == "":
		return d, errors.New("nickname is required")
	case d.Host == "":
		// The loader accepts an empty host and leaves it to discovery, but
		// discovery here only ever offers a host to fill in, so a device saved
		// without one could never be synced from the TUI.
		return d, errors.New("host is required")
	case strings.ContainsAny(d.Host, "/ "):
		return d, errors.New("host is a name or address, not a URL")
	}
	for _, e := range existing {
		if e.Nickname == d.Nickname {
			return d, fmt.Errorf("a device named %q is already configured", d.Nickname)
		}
	}
	if d.Root != "" && !strings.HasPrefix(d.Root, "/") {
		d.Root = "/" + d.Root
	}
	return d, nil
}

func (f *deviceForm) View(styles Styles) string {
	var b strings.Builder
	b.WriteString(styles.Title.Render("Add a device") + "\n\n")
	for i, in := range f.inputs {
		marker := "  "
		if i == f.focus {
			marker = styles.Accent.Render("▸ ")
		}
		b.WriteString(fmt.Sprintf("%s%s  %s\n", marker,
			styles.StatLabel.Render(fmt.Sprintf("%-8s", deviceFormLabels[i])), in.View()))
	}
	b.WriteString("\n")
	switch {
	case f.saving:
		b.WriteString(styles.Subtle.Render("saving…"))
	case f.err != "":
		b.WriteString(styles.Error.Render(f.err))
	default:
		b.WriteString(styles.Subtle.Render("Saved as a [[device]] block at the end of config.toml."))
	}
	return b.String()
}
