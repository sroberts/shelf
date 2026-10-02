package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sroberts/shelf/internal/config"
)

// press feeds one message to the root model and returns its command, so a test
// can choose which commands to run instead of letting probes hit the network.
func press(t *testing.T, m tea.Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return model, cmd
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// The add-device form takes keys before the global bindings, so a nickname
// with a "q" in it is typed rather than quitting, and enter saves the device
// to config.toml and to the running config without a restart.
func TestAddDeviceFromTheDevicesScreen(t *testing.T) {
	app := testApp(t)
	app.Config.File = filepath.Join(t.TempDir(), "config.toml")

	m, _ := press(t, New(context.Background(), app), tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.devices.form == nil {
		t.Fatal("n did not open the add-device form")
	}

	m = typeText(t, m, "quinn")
	if m.quitting {
		t.Fatal("typing q into the form quit the program")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	// 127.0.0.1:1 refuses at once, so the probe after saving cannot stall.
	m = typeText(t, m, "127.0.0.1:1")

	m, save := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if save == nil {
		t.Fatalf("enter did not save; form error %q", m.devices.form.err)
	}
	m, _ = press(t, m, save())

	if m.devices.form != nil {
		t.Errorf("form still open after saving; error %q", m.devices.form.err)
	}
	if len(app.Config.Devices) != 1 || app.Config.Devices[0].Nickname != "quinn" {
		t.Errorf("running config devices = %+v, want quinn", app.Config.Devices)
	}
	data, err := os.ReadFile(app.Config.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `nickname = "quinn"`) ||
		!strings.Contains(string(data), `host = "127.0.0.1:1"`) {
		t.Errorf("config.toml does not hold the device:\n%s", data)
	}
}

// A nickname that is already configured is refused in the form, before
// anything is written.
func TestAddDeviceRefusesADuplicateNickname(t *testing.T) {
	app := testApp(t)
	app.Config.File = filepath.Join(t.TempDir(), "config.toml")
	app.Config.Devices = []config.Device{{Nickname: "quinn", Host: "10.0.0.2"}}

	m, _ := press(t, New(context.Background(), app), tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeText(t, m, "quinn")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = typeText(t, m, "reader.local")

	m, save := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if save != nil {
		t.Fatal("a duplicate nickname produced a save command")
	}
	if m.devices.form == nil || !strings.Contains(m.devices.form.err, "already configured") {
		t.Fatalf("want an already-configured error in the form, got %+v", m.devices.form)
	}
	if _, err := os.Stat(app.Config.File); !os.IsNotExist(err) {
		t.Errorf("config.toml was written for a refused device: %v", err)
	}
}
