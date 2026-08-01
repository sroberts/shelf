package tui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sroberts/shelf/internal/library"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden frame files")

// Golden-frame regression.
//
// Frames are compared as plain text with ANSI sequences stripped. Colors depend
// on terminal capability detection and on whether the test runs under a tty, so
// asserting on them would produce a suite that fails in CI for reasons that
// have nothing to do with the layout. Layout is the thing worth pinning:
// column alignment, truncation, and what text appears where.

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\x1b[=><]`)

// stripANSI removes escape sequences and normalizes trailing whitespace.
func stripANSI(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.TrimRight(line, " \t"))
	}
	// Collapse the run of blank lines the alt-screen leaves behind.
	joined := strings.Join(out, "\n")
	for strings.Contains(joined, "\n\n\n") {
		joined = strings.ReplaceAll(joined, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(joined)
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	got = stripANSI(got)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s missing; run: go test ./internal/tui -update-golden", path)
	}
	if diff := strings.TrimSpace(string(want)); diff != got {
		t.Errorf("frame does not match %s\n\n--- want ---\n%s\n\n--- got ---\n%s", path, diff, got)
	}
}

// goldenBooks is a fixed library, chosen to exercise the layout: a series with
// an index, a standalone title, a long title that must truncate, and CJK text
// whose runes are two cells wide.
func goldenBooks() []*library.Book {
	return []*library.Book{
		{
			SHA256: "1", Path: "/lib/earthsea.epub", Format: library.FormatEPUB,
			Title: "A Wizard of Earthsea", AuthorSort: "Le Guin, Ursula K.",
			Authors: []string{"Ursula K. Le Guin"},
			Series:  "Earthsea", SeriesIndex: 1, Size: 1291,
		},
		{
			SHA256: "2", Path: "/lib/moby.epub", Format: library.FormatEPUB,
			Title: "Moby-Dick", AuthorSort: "Melville, Herman",
			Authors: []string{"Herman Melville"}, Size: 24835597,
		},
		{
			SHA256: "3", Path: "/lib/long.epub", Format: library.FormatEPUB,
			Title:   "The Exceedingly Long Title That Will Certainly Need Truncating Somewhere",
			Authors: []string{"Verbose, A."}, AuthorSort: "Verbose, A.", Size: 4096,
		},
		{
			SHA256: "4", Path: "/lib/kafka.epub", Format: library.FormatEPUB,
			Title: "海辺のカフカ", AuthorSort: "村上 春樹",
			Authors: []string{"村上 春樹"}, Size: 1130040,
		},
	}
}

// captureFrame drives the model synchronously and returns what it renders.
//
// Deliberately not driven through teatest here. teatest runs a real event loop
// against a pty and emits every intermediate frame into one stream, so a golden
// test built on it has to guess where the last frame begins and races the
// renderer for the final paint. Applying messages directly and calling View()
// asks the model exactly the question a golden test cares about -- given this
// state, what do you draw? -- with no timing in the answer.
func captureFrame(t *testing.T, msgs []tea.Msg) string {
	t.Helper()
	return captureFrameWith(t, testApp(t, goldenBooks()...), msgs)
}

// captureFrameWith is captureFrame against a caller-supplied App, for cases
// that need particular config such as a configured device.
func captureFrameWith(t *testing.T, app *App, msgs []tea.Msg) string {
	t.Helper()

	var model tea.Model = New(context.Background(), app)

	// Resolve the initial loads, then size the window.
	model = runCmd(t, model, model.Init())
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	for _, msg := range msgs {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		model = runCmd(t, model, cmd)
	}
	return model.View()
}

// runCmd executes a command and feeds the resulting messages back in, so the
// model reaches the state the real event loop would have produced.
func runCmd(t *testing.T, model tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	return runCmdDepth(t, model, cmd, 0)
}

func runCmdDepth(t *testing.T, model tea.Model, cmd tea.Cmd, depth int) tea.Model {
	t.Helper()
	// Guard against a command that re-arms itself forever, such as the sync
	// event pump.
	if cmd == nil || depth > 8 {
		return model
	}

	msg := cmd()
	switch m := msg.(type) {
	case nil:
		return model

	case tea.BatchMsg:
		for _, sub := range m {
			model = runCmdDepth(t, model, sub, depth+1)
		}
		return model

	// Frame ticks and quit signals belong to the runtime, not the model.
	case tea.QuitMsg:
		return model
	}

	next, cmd2 := model.Update(msg)
	return runCmdDepth(t, next, cmd2, depth+1)
}

func TestGoldenLibraryFrame(t *testing.T) {
	frame := captureFrame(t, nil)
	assertGolden(t, "library", frame)
}

func TestGoldenLibraryWithSelection(t *testing.T) {
	frame := captureFrame(t,
		[]tea.Msg{
			tea.KeyMsg{Type: tea.KeySpace}, // select row 1, cursor advances
			tea.KeyMsg{Type: tea.KeySpace}, // select row 2
		})
	assertGolden(t, "library_selection", frame)
}

func TestGoldenDevicesEmpty(t *testing.T) {
	frame := captureFrame(t,
		[]tea.Msg{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}})
	assertGolden(t, "devices_empty", frame)
}

func TestGoldenSyncIdle(t *testing.T) {
	frame := captureFrame(t,
		[]tea.Msg{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}})
	assertGolden(t, "sync_idle", frame)
}

func TestGoldenHelpExpanded(t *testing.T) {
	frame := captureFrame(t,
		[]tea.Msg{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}})
	assertGolden(t, "help_expanded", frame)
}

// Column alignment is the property most likely to regress silently, and the
// one users notice immediately. Assert it directly rather than only through a
// golden file, so a failure says what is wrong.
func TestColumnsStayAlignedWithWideRunes(t *testing.T) {
	frame := stripANSI(captureFrame(t, nil))

	var widths []int
	for _, line := range strings.Split(frame, "\n") {
		// Sample the rows that carry book data.
		if strings.Contains(line, "Moby-Dick") ||
			strings.Contains(line, "Earthsea") ||
			strings.Contains(line, "海辺") ||
			strings.Contains(line, "Exceedingly") {
			widths = append(widths, displayWidth(line))
		}
	}

	if len(widths) < 3 {
		t.Fatalf("expected several book rows, found %d in:\n%s", len(widths), frame)
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("row %d has display width %d, but row 0 has %d; "+
				"columns are misaligned:\n%s", i, w, widths[0], frame)
		}
	}
}
