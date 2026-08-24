package sync

import (
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/device"
)

// The device's card is FAT32, so two destinations differing only in case are
// one file there. Planning both is how a book's bytes end up at another book's
// pinned path, which clears the firmware cache holding its reading position.

func TestCaseCollisionPlansOneUploadNotTwo(t *testing.T) {
	a := book("/lib/a.epub", "Moby-Dick.epub", "aaa", 100)
	b := book("/lib/b.epub", "moby-dick.epub", "bbb", 200)

	plan := Build(Input{
		Root:   device.NewPath("/Books"),
		Local:  []LocalBook{a, b},
		Device: withDirs(devFiles(), "/Books"),
	})

	uploads := opsOfKind(plan, OpUpload)
	if len(uploads) != 1 {
		t.Fatalf("planned %d uploads for two books that fold together, want 1:\n%s",
			len(uploads), formatOps(plan.Ops))
	}
	if len(plan.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one collision warning", plan.Warnings)
	}
	// The warning has to name both books, or it is not actionable: the user
	// has to know which two files to go and rename.
	w := plan.Warnings[0]
	for _, want := range []string{"/lib/a.epub", "/lib/b.epub"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning does not name %s: %s", want, w)
		}
	}
}

// Two books that merely share a directory prefix are not a collision. Without
// this the check would be worthless -- it would fire on every second book.
func TestDistinctNamesAreNotCollisions(t *testing.T) {
	a := book("/lib/a.epub", "Le Guin/Wizard.epub", "aaa", 100)
	b := book("/lib/b.epub", "Le Guin/Tombs.epub", "bbb", 200)

	plan := Build(Input{
		Root:   device.NewPath("/Books"),
		Local:  []LocalBook{a, b},
		Device: withDirs(devFiles(), "/Books"),
	})

	if got := len(opsOfKind(plan, OpUpload)); got != 2 {
		t.Errorf("uploads = %d, want 2:\n%s", got, formatOps(plan.Ops))
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", plan.Warnings)
	}
}

// The tie-break that matters. A pinned book holds a reading position; a new
// book must never take its path, regardless of iteration order.
func TestPinnedBookWinsACaseCollision(t *testing.T) {
	const pinnedLocal = "/lib/zzz-sorts-last.epub"
	const pinnedDev = "/Books/Moby-Dick.epub"

	// The newcomer sorts FIRST by local path, so a naive single pass in sorted
	// order would let it claim the path and evict the pinned book.
	newcomer := book("/lib/aaa-sorts-first.epub", "moby-dick.epub", "bbb", 200)
	pinned := book(pinnedLocal, "Moby-Dick.epub", "aaa", 100)

	m := manifest(entry(pinnedLocal, pinnedDev, "aaa", 100))

	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{newcomer, pinned},
		Device:   withDirs(devFiles(pinnedDev, 100), "/Books"),
		Manifest: m,
	})

	for _, op := range plan.Ops {
		if op.SourcePath == newcomer.Path {
			t.Fatalf("the newcomer was planned onto the pinned book's path:\n%s",
				formatOps(plan.Ops))
		}
	}
	if plan.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1 (the pinned book still matches)", plan.Unchanged)
	}
}

// A skipped pinned book must stay "matched", or the deletes pass reads it as a
// book that vanished locally and --prune removes it from the device.
func TestASkippedPinnedBookIsNotTreatedAsDeleted(t *testing.T) {
	const firstLocal = "/lib/a.epub"
	const secondLocal = "/lib/b.epub"

	// Two manifest entries that fold together: a library already in this state
	// before the check existed.
	m := manifest(
		entry(firstLocal, "/Books/Dune.epub", "aaa", 100),
		entry(secondLocal, "/Books/dune.epub", "bbb", 200),
	)

	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Local: []LocalBook{
			book(firstLocal, "Dune.epub", "aaa", 100),
			book(secondLocal, "dune.epub", "bbb", 200),
		},
		Device:   withDirs(devFiles("/Books/Dune.epub", 100, "/Books/dune.epub", 200), "/Books"),
		Manifest: m,
		Options:  Options{Prune: true},
	})

	for _, op := range plan.Ops {
		if op.Kind == OpDelete {
			t.Errorf("planned a delete for a book that is still in the library: %s", op)
		}
	}
	if len(plan.Drops) != 0 {
		t.Errorf("dropped a book that is still in the library: %v", plan.Drops)
	}
}

// Repath computes a fresh destination from the template, so it needs the same
// guard -- otherwise --repath is a way to route around the check.
func TestRepathDestinationsAlsoCollide(t *testing.T) {
	const aLocal = "/lib/a.epub"
	const bLocal = "/lib/b.epub"

	m := manifest(
		entry(aLocal, "/Books/old-a.epub", "aaa", 100),
		entry(bLocal, "/Books/old-b.epub", "bbb", 200),
	)

	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Local: []LocalBook{
			book(aLocal, "Ubik.epub", "aaa", 100),
			book(bLocal, "ubik.epub", "bbb", 200),
		},
		Device:   withDirs(devFiles("/Books/old-a.epub", 100, "/Books/old-b.epub", 200), "/Books"),
		Manifest: m,
		Options:  Options{Repath: true},
	})

	if got := len(opsOfKind(plan, OpMove)); got != 1 {
		t.Errorf("moves = %d, want 1 (the second folds onto the first):\n%s",
			got, formatOps(plan.Ops))
	}
}

// Fold has to normalize as well as lowercase. APFS returns NFD, so the same
// name typed once can arrive spelled two ways.
func TestFoldMatchesAcrossUnicodeNormalization(t *testing.T) {
	nfc := device.NewPath("/Books/Brontë.epub")   // ë as one rune
	nfd := device.NewPath("/Books/Brontë.epub")  // e + combining diaeresis
	upper := device.NewPath("/Books/BRONTË.epub") // NFC, uppercase

	if nfc.Fold() != nfd.Fold() {
		t.Errorf("NFC and NFD spellings folded apart:\n  %q\n  %q", nfc.Fold(), nfd.Fold())
	}
	if nfc.Fold() != upper.Fold() {
		t.Errorf("case was not folded:\n  %q\n  %q", nfc.Fold(), upper.Fold())
	}
	if device.NewPath("/Books/Dune.epub").Fold() == device.NewPath("/Books/Ubik.epub").Fold() {
		t.Error("Fold collapsed two genuinely different names")
	}
}
