package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sroberts/shelf/internal/library"
)

// richBook is a book with every optional field populated, so the detail panel
// has something to show in each of its sections.
func richBook() *library.Book {
	return &library.Book{
		SHA256: "r1", Path: "/lib/Le Guin/earthsea-01.epub", Format: library.FormatEPUB,
		Title: "A Wizard of Earthsea", AuthorSort: "Le Guin, Ursula K.",
		Authors: []string{"Ursula K. Le Guin"},
		Series:  "Earthsea", SeriesIndex: 1, Size: 412911,
		Language: "en", Publisher: "Parnassus Press", PubDate: "1968",
		Tags:        []string{"fantasy", "classics"},
		AddedUnix:   goldenAdded,
		Identifiers: map[string]string{"isbn": "9780553383041"},
	}
}

func frameAt(t *testing.T, w, h int, books []*library.Book, msgs ...tea.Msg) string {
	t.Helper()

	var model tea.Model = New(context.Background(), testApp(t, books...))
	model = runCmd(t, model, model.Init())
	model, _ = model.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range msgs {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		model = runCmd(t, model, cmd)
	}
	return stripANSI(model.View())
}

// The header is the answer to "how big is this library", and it has to be right
// without a scan having just run.
func TestHeaderSummarisesTheLibrary(t *testing.T) {
	got := frameAt(t, 120, 24, goldenBooks())

	for _, want := range []string{"4 books", "4 authors", "1 series"} {
		if !strings.Contains(got, want) {
			t.Errorf("header is missing %q\n%s", want, got)
		}
	}
}

func TestHeaderShowsSyncCounts(t *testing.T) {
	books := goldenBooks()
	state := map[string]string{
		books[0].Path: GlyphSynced,
		books[1].Path: GlyphSynced,
		books[2].Path: GlyphPending,
		books[3].Path: GlyphAbsent,
	}

	got := frameAt(t, 120, 24, books, syncStateMsg{state: state})
	header := strings.SplitN(got, "\n", 3)[1]

	for _, want := range []string{GlyphSynced + "2", GlyphPending + "1", GlyphAbsent + "1"} {
		if !strings.Contains(header, want) {
			t.Errorf("header line %q is missing %q", header, want)
		}
	}
}

// With no device configured there is nothing to count against, and a row of
// zeroes would imply the library is entirely un-synced rather than unknown.
func TestHeaderOmitsSyncCountsWithoutADevice(t *testing.T) {
	got := frameAt(t, 120, 24, goldenBooks())
	header := strings.SplitN(got, "\n", 3)[1]

	for _, glyph := range []string{GlyphSynced, GlyphPending, GlyphAbsent} {
		if strings.Contains(header, glyph) {
			t.Errorf("header %q reports sync state with no device configured", header)
		}
	}
}

func TestDetailPanelShowsTheSelectedBook(t *testing.T) {
	got := frameAt(t, 120, 30, []*library.Book{richBook()})

	// Fields the table has no room for are the reason the panel exists.
	for _, want := range []string{
		"Parnassus Press", "9780553383041", "1968", "fantasy", "classics", "en",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail panel is missing %q\n%s", want, got)
		}
	}
}

func TestDetailPanelFollowsTheCursor(t *testing.T) {
	books := goldenBooks()
	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}

	first := frameAt(t, 120, 30, books)
	if !strings.Contains(first, "/lib/earthsea.epub") {
		t.Fatalf("panel does not show the first book's path\n%s", first)
	}

	second := frameAt(t, 120, 30, books, down)
	if !strings.Contains(second, "/lib/moby.epub") {
		t.Errorf("panel did not follow the cursor to the second book\n%s", second)
	}
	if strings.Contains(second, "/lib/earthsea.epub") {
		t.Errorf("panel still shows the first book after moving down\n%s", second)
	}
}

func TestDetailPanelExplainsTheSyncGlyph(t *testing.T) {
	books := goldenBooks()
	got := frameAt(t, 120, 30, books,
		syncStateMsg{state: map[string]string{books[0].Path: GlyphPending}})

	// The glyph alone is unreadable the first time. The panel has room to say
	// what it means, and that is most of why it is worth the width.
	if !strings.Contains(got, "needs upload") {
		t.Errorf("panel does not explain the sync state\n%s", got)
	}
}

// Below the threshold the panel would cost the title column more than it adds.
func TestDetailPanelIsDroppedOnANarrowTerminal(t *testing.T) {
	wide := frameAt(t, 120, 24, []*library.Book{richBook()})
	if !strings.Contains(wide, "Parnassus Press") {
		t.Fatal("panel missing at 120 columns")
	}

	narrow := frameAt(t, minWidthForDetail-1, 24, []*library.Book{richBook()})
	if strings.Contains(narrow, "Parnassus Press") {
		t.Errorf("panel still drawn at %d columns\n%s", minWidthForDetail-1, narrow)
	}
	// The table itself must still be usable, not squeezed to nothing.
	if !strings.Contains(narrow, "A Wizard of Earthsea") {
		t.Errorf("table lost its content on a narrow terminal\n%s", narrow)
	}
}

func TestDetailPanelTogglesWithI(t *testing.T) {
	toggle := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}

	hidden := frameAt(t, 120, 24, []*library.Book{richBook()}, toggle)
	if strings.Contains(hidden, "Parnassus Press") {
		t.Errorf("panel still visible after pressing i\n%s", hidden)
	}

	shown := frameAt(t, 120, 24, []*library.Book{richBook()}, toggle, toggle)
	if !strings.Contains(shown, "Parnassus Press") {
		t.Errorf("panel did not come back on a second i\n%s", shown)
	}
}

// The table has to give back the width the panel was using, or hiding the
// panel would leave a column of dead space.
func TestHidingThePanelWidensTheTable(t *testing.T) {
	long := &library.Book{
		SHA256: "L", Path: "/lib/long.epub", Format: library.FormatEPUB,
		Title: "The Exceedingly Long Title That Will Certainly Need Truncating Somewhere",
	}
	toggle := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}

	withPanel := visibleTitle(t, frameAt(t, 120, 24, []*library.Book{long}))
	without := visibleTitle(t, frameAt(t, 120, 24, []*library.Book{long}, toggle))

	if len(without) <= len(withPanel) {
		t.Errorf("hiding the panel did not widen the title column:\n with panel: %q\n without:    %q",
			withPanel, without)
	}
}

// visibleTitle returns how much of the long title the table actually rendered.
func visibleTitle(t *testing.T, frame string) string {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if i := strings.Index(line, "The Exceedingly"); i >= 0 {
			return strings.TrimSpace(strings.SplitN(line[i:], "  ", 2)[0])
		}
	}
	t.Fatalf("no title row in frame:\n%s", frame)
	return ""
}

func TestFormatCount(t *testing.T) {
	cases := map[int]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		12480: "12,480", 999999: "999,999", 1234567: "1,234,567",
	}
	for in, want := range cases {
		if got := formatCount(in); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDetailWidthScalesAndClamps(t *testing.T) {
	if w := detailWidth(minWidthForDetail - 1); w != 0 {
		t.Errorf("narrow terminal got a panel %d wide", w)
	}
	if w := detailWidth(100); w < 30 || w > 46 {
		t.Errorf("panel at 100 columns is %d wide, outside the clamp", w)
	}
	// A very wide terminal should not hand half the screen to metadata.
	if w := detailWidth(400); w != 46 {
		t.Errorf("panel at 400 columns is %d wide, want the 46 cap", w)
	}
}
