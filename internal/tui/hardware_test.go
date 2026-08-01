//go:build hardware

package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sroberts/shelf/internal/config"
)

// Hardware-backed rendering checks.
//
// Guarded by a build tag so the normal suite never depends on a device being
// awake. Run with:
//
//	SHELF_TEST_DEVICE=crosspoint.local go test -tags hardware ./internal/tui/
//
// The point is not to re-test the device client, which internal/device already
// covers, but to confirm the Devices screen renders a real status without
// mangling it.
func TestDevicesScreenAgainstHardware(t *testing.T) {
	host := os.Getenv("SHELF_TEST_DEVICE")
	if host == "" {
		t.Skip("set SHELF_TEST_DEVICE to a reachable device")
	}

	app := testApp(t, book("/lib/a.epub", "A Book", "An Author"))
	app.Config.Devices = []config.Device{{
		Nickname:  "x4",
		Host:      host,
		Root:      "/Books",
		Transport: config.TransportWS,
		ChunkSize: config.DefaultChunkSize,
	}}

	frame := stripANSI(captureFrameWith(t, app, []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")},
	}))

	t.Logf("devices screen:\n%s", frame)

	for _, want := range []string{"x4", host} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame does not mention %q:\n%s", want, frame)
		}
	}

	// Either the device answered, or it is asleep. Both are legitimate; a raw
	// transport error leaking into the UI is not.
	answered := strings.Contains(frame, "X4") && strings.Contains(frame, "dBm")
	asleep := strings.Contains(frame, "not in File Transfer mode")

	if !answered && !asleep {
		t.Errorf("device state is rendered as neither reachable nor asleep:\n%s", frame)
	}
	if strings.Contains(frame, "context deadline exceeded") ||
		strings.Contains(frame, "dial tcp") {
		t.Errorf("a raw transport error leaked into the UI:\n%s", frame)
	}
}

// TestPushWorkflowReachesPlanPreview is M3's definition of done: selecting a
// book and pressing s must produce a real plan without touching the CLI.
//
// It stops at the confirmation step and never presses enter, so nothing is
// written to the device. Executing the plan is covered by the CLI's hardware
// verification; what is under test here is that the TUI wires the same planner
// to the same device and shows the result.
func TestPushWorkflowReachesPlanPreview(t *testing.T) {
	host := os.Getenv("SHELF_TEST_DEVICE")
	if host == "" {
		t.Skip("set SHELF_TEST_DEVICE to a reachable device")
	}

	app := testApp(t, goldenBooks()...)
	app.Config.Devices = []config.Device{{
		Nickname:  "x4",
		Host:      host,
		Root:      "/tui-plan-preview-only",
		Transport: config.TransportWS,
		ChunkSize: config.DefaultChunkSize,
	}}

	frame := stripANSI(captureFrameWith(t, app, []tea.Msg{
		tea.KeyMsg{Type: tea.KeySpace}, // select the first book
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")},
	}))

	t.Logf("sync screen:\n%s", frame)

	if !strings.Contains(frame, "Plan for x4") {
		t.Fatalf("the push workflow did not reach a plan preview:\n%s", frame)
	}
	if !strings.Contains(frame, "uploads") {
		t.Errorf("the plan does not summarize uploads:\n%s", frame)
	}
	// The plan must be awaiting confirmation, not already running.
	if !strings.Contains(frame, "enter to start") {
		t.Errorf("the plan is not waiting for confirmation:\n%s", frame)
	}
}
