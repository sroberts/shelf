package sync

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/device"
)

// Helpers for building planner inputs concisely.

func book(local, rel, sha string, size int64) LocalBook {
	return LocalBook{Path: local, RelPath: rel, SHA256: sha, Size: size}
}

func entry(local, devPath, sha string, size int64) Entry {
	return Entry{LocalPath: local, DevicePath: devPath, SHA256: sha, DeviceSize: size}
}

func devFiles(pairs ...any) map[device.Path]device.FileEntry {
	out := map[device.Path]device.FileEntry{}
	for i := 0; i+1 < len(pairs); i += 2 {
		p := device.NewPath(pairs[i].(string))
		size := int64(pairs[i+1].(int))
		out[p] = device.FileEntry{Name: p.Base(), Size: size}
	}
	return out
}

func withDirs(files map[device.Path]device.FileEntry, dirs ...string) map[device.Path]device.FileEntry {
	for _, d := range dirs {
		p := device.NewPath(d)
		files[p] = device.FileEntry{Name: p.Base(), IsDirectory: true}
	}
	return files
}

func manifest(entries ...Entry) *Manifest {
	m := NewManifest("x4", "/Books")
	m.Entries = entries
	return m
}

// opsOfKind extracts the device paths of operations of one kind.
func opsOfKind(p Plan, kind OpKind) []string {
	var out []string
	for _, op := range p.Ops {
		if op.Kind == kind {
			out = append(out, op.DevicePath.String())
		}
	}
	return out
}

// TestDecisionTable covers the matrix in spec section 6.2 directly. The spec
// calls this the highest-value test surface in the project, and it is: every
// row here is a decision that, if wrong, either loses a book or destroys a
// reading position.
func TestDecisionTable(t *testing.T) {
	const (
		root  = "/Books"
		local = "/home/scott/Books/Melville/Moby-Dick.epub"
		dev   = "/Books/Melville/Moby-Dick.epub"
		rel   = "Melville/Moby-Dick.epub"
	)

	tests := []struct {
		name string

		local    []LocalBook
		device   map[device.Path]device.FileEntry
		manifest *Manifest
		opts     Options

		wantUploads   []string
		wantDeletes   []string
		wantUnchanged int
		wantOrphans   []string
		wantWarning   string
		wantDrops     []string
	}{
		{
			name:          "manifest present, device present, size matches: no-op",
			local:         []LocalBook{book(local, rel, "aaa", 100)},
			device:        withDirs(devFiles(dev, 100), root, "/Books/Melville"),
			manifest:      manifest(entry(local, dev, "aaa", 100)),
			wantUnchanged: 1,
		},
		{
			name:        "manifest present, device present, size differs: re-upload in place",
			local:       []LocalBook{book(local, rel, "aaa", 100)},
			device:      withDirs(devFiles(dev, 57), root, "/Books/Melville"),
			manifest:    manifest(entry(local, dev, "aaa", 100)),
			wantUploads: []string{dev},
		},
		{
			name:        "manifest present, device absent: re-upload",
			local:       []LocalBook{book(local, rel, "aaa", 100)},
			device:      withDirs(devFiles(), root, "/Books/Melville"),
			manifest:    manifest(entry(local, dev, "aaa", 100)),
			wantUploads: []string{dev},
		},
		{
			name:      "manifest present, device absent, --respect-device-deletes: drop it",
			local:     []LocalBook{book(local, rel, "aaa", 100)},
			device:    withDirs(devFiles(), root, "/Books/Melville"),
			manifest:  manifest(entry(local, dev, "aaa", 100)),
			opts:      Options{RespectDeviceDeletes: true},
			wantDrops: []string{local},
		},
		{
			name:        "manifest absent, device present: orphan, reported not deleted",
			local:       nil,
			device:      withDirs(devFiles("/Books/Stranger.epub", 42), root),
			manifest:    manifest(),
			wantOrphans: []string{"/Books/Stranger.epub"},
		},
		{
			name:        "manifest absent, device present, --prune: deleted",
			local:       nil,
			device:      withDirs(devFiles("/Books/Stranger.epub", 42), root),
			manifest:    manifest(),
			opts:        Options{Prune: true},
			wantOrphans: []string{"/Books/Stranger.epub"},
			wantDeletes: []string{"/Books/Stranger.epub"},
		},
		{
			name:        "manifest present, local file gone: warn only",
			local:       nil,
			device:      withDirs(devFiles(dev, 100), root, "/Books/Melville"),
			manifest:    manifest(entry(local, dev, "aaa", 100)),
			wantWarning: "no longer in the shelf",
		},
		{
			name:        "manifest present, local file gone, --prune: delete",
			local:       nil,
			device:      withDirs(devFiles(dev, 100), root, "/Books/Melville"),
			manifest:    manifest(entry(local, dev, "aaa", 100)),
			opts:        Options{Prune: true},
			wantDeletes: []string{dev},
		},
		{
			name:        "local changed (new hash), device size unchanged: re-upload",
			local:       []LocalBook{book(local, rel, "bbb", 100)},
			device:      withDirs(devFiles(dev, 100), root, "/Books/Melville"),
			manifest:    manifest(entry(local, dev, "aaa", 100)),
			wantUploads: []string{dev},
		},
		{
			name:        "brand new book: upload",
			local:       []LocalBook{book(local, rel, "aaa", 100)},
			device:      withDirs(devFiles(), root),
			manifest:    manifest(),
			wantUploads: []string{dev},
		},
		{
			name:     "manifest present, gone from both sides: drop silently",
			local:    nil,
			device:   withDirs(devFiles(), root),
			manifest: manifest(entry(local, dev, "aaa", 100)),
			// No device op needed; just forget the entry.
			wantDrops: []string{local},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Build(Input{
				Root:     device.NewPath(root),
				Local:    tt.local,
				Device:   tt.device,
				Manifest: tt.manifest,
				Options:  tt.opts,
			})

			if got := opsOfKind(plan, OpUpload); !equalStrings(got, tt.wantUploads) {
				t.Errorf("uploads = %v, want %v", got, tt.wantUploads)
			}
			if got := opsOfKind(plan, OpDelete); !equalStrings(got, tt.wantDeletes) {
				t.Errorf("deletes = %v, want %v", got, tt.wantDeletes)
			}
			if plan.Unchanged != tt.wantUnchanged {
				t.Errorf("unchanged = %d, want %d", plan.Unchanged, tt.wantUnchanged)
			}

			var orphans []string
			for _, o := range plan.Orphans {
				orphans = append(orphans, o.String())
			}
			if !equalStrings(orphans, tt.wantOrphans) {
				t.Errorf("orphans = %v, want %v", orphans, tt.wantOrphans)
			}

			if !equalStrings(plan.Drops, tt.wantDrops) {
				t.Errorf("drops = %v, want %v", plan.Drops, tt.wantDrops)
			}

			if tt.wantWarning != "" {
				if !containsSubstring(plan.Warnings, tt.wantWarning) {
					t.Errorf("warnings = %v, want one containing %q", plan.Warnings, tt.wantWarning)
				}
			}
		})
	}
}

// TestPathPinning is the acceptance test for the whole project.
//
// Changing the naming template, the series numbering, or any other metadata
// must produce ZERO moves and ZERO re-uploads for books already on the device.
// Every path change clears the firmware's .crosspoint cache for that book,
// which destroys the reading position. This is precisely the Calibre behaviour
// shelf exists to avoid.
func TestPathPinningIgnoresTemplateChanges(t *testing.T) {
	const local = "/home/scott/Books/Le Guin/Earthsea 01 - A Wizard of Earthsea.epub"
	const pinned = "/Books/Le Guin/Earthsea 01 - A Wizard of Earthsea.epub"

	m := manifest(entry(local, pinned, "aaa", 1000))
	devices := withDirs(devFiles(pinned, 1000), "/Books", "/Books/Le Guin")

	// The template now renders a completely different path: different author
	// form, different series numbering, different title.
	renamed := book(local, "Ursula K. Le Guin/A Wizard of Earthsea (Earthsea #1).epub", "aaa", 1000)

	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{renamed},
		Device:   devices,
		Manifest: m,
	})

	if len(plan.Ops) != 0 {
		t.Errorf("a template change produced %d operations, want none:\n%s",
			len(plan.Ops), formatOps(plan.Ops))
	}
	if plan.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", plan.Unchanged)
	}
	if len(plan.RepathWarnings) != 0 {
		t.Errorf("unexpected repath warnings without --repath: %v", plan.RepathWarnings)
	}
	if len(plan.Orphans) != 0 {
		t.Errorf("the pinned file was reported as an orphan: %v", plan.Orphans)
	}
}

// Repathing is possible, but only when explicitly requested, and it must warn
// about every book that will lose its position.
func TestRepathOnlyUnderExplicitFlag(t *testing.T) {
	const local = "/home/scott/Books/old.epub"
	const pinned = "/Books/Old Name.epub"
	const wantNew = "/Books/New Name.epub"

	m := manifest(entry(local, pinned, "aaa", 1000))
	in := Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book(local, "New Name.epub", "aaa", 1000)},
		Device:   withDirs(devFiles(pinned, 1000), "/Books"),
		Manifest: m,
		Options:  Options{Repath: true},
	}

	plan := Build(in)

	moves := opsOfKind(plan, OpMove)
	if !equalStrings(moves, []string{wantNew}) {
		t.Fatalf("moves = %v, want [%s]", moves, wantNew)
	}
	for _, op := range plan.Ops {
		if op.Kind == OpMove && op.From.String() != pinned {
			t.Errorf("move source = %s, want %s", op.From, pinned)
		}
	}

	if len(plan.RepathWarnings) != 1 {
		t.Fatalf("repath warnings = %v, want exactly one", plan.RepathWarnings)
	}
	if !strings.Contains(plan.RepathWarnings[0], "loses reading position") {
		t.Errorf("warning does not mention the consequence: %q", plan.RepathWarnings[0])
	}

	// The same input without the flag must be a complete no-op.
	in.Options.Repath = false
	if plan := Build(in); len(plan.Ops) != 0 {
		t.Errorf("without --repath, got %d operations:\n%s", len(plan.Ops), formatOps(plan.Ops))
	}
}

// Directories must be created parents-first. The firmware's mkdir does not
// create intermediate directories, so the reverse order simply fails.
func TestMkdirOrderingIsShallowestFirst(t *testing.T) {
	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Local: []LocalBook{
			book("/l/a.epub", "Le Guin/Earthsea/Wizard.epub", "a", 10),
			book("/l/b.epub", "Melville/Moby.epub", "b", 20),
		},
		Device:   map[device.Path]device.FileEntry{},
		Manifest: manifest(),
	})

	var dirs []string
	for _, op := range plan.Ops {
		if op.Kind == OpMkdir {
			dirs = append(dirs, op.DevicePath.String())
		}
	}

	// Every directory must appear after its parent.
	seen := map[string]bool{}
	for _, d := range dirs {
		parent := device.NewPath(d).Dir().String()
		if parent != "/" && strings.HasPrefix(parent, "/Books") && parent != "/Books" && !seen[parent] {
			t.Errorf("%s was created before its parent %s (order: %v)", d, parent, dirs)
		}
		seen[d] = true
	}

	// The depths must be non-decreasing.
	for i := 1; i < len(dirs); i++ {
		if device.NewPath(dirs[i]).Depth() < device.NewPath(dirs[i-1]).Depth() {
			t.Errorf("directory order is not shallowest-first: %v", dirs)
			break
		}
	}

	// mkdir operations must all precede uploads.
	lastMkdir, firstUpload := -1, len(plan.Ops)
	for i, op := range plan.Ops {
		if op.Kind == OpMkdir {
			lastMkdir = i
		}
		if op.Kind == OpUpload && i < firstUpload {
			firstUpload = i
		}
	}
	if lastMkdir > firstUpload {
		t.Errorf("an mkdir was ordered after an upload:\n%s", formatOps(plan.Ops))
	}
}

// Existing directories need no mkdir.
func TestMkdirSkipsExistingDirectories(t *testing.T) {
	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book("/l/a.epub", "Melville/Moby.epub", "a", 10)},
		Device:   withDirs(map[device.Path]device.FileEntry{}, "/Books", "/Books/Melville"),
		Manifest: manifest(),
	})

	if dirs := opsOfKind(plan, OpMkdir); len(dirs) != 0 {
		t.Errorf("mkdir ops = %v, want none; both directories already exist", dirs)
	}
}

// A recursive device listing reports files, not directories. The planner must
// infer which directories exist from the files inside them, or it emits a
// redundant mkdir for every directory on every single sync -- harmless to
// execute, but it makes --dry-run output untrustworthy.
func TestMkdirInfersExistingDirectoriesFromFiles(t *testing.T) {
	// The listing contains only files, as ListRecursive returns.
	listing := devFiles(
		"/Books/Melville/Moby-Dick.epub", 100,
		"/Books/Le Guin/Earthsea/Wizard.epub", 200,
	)

	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Local: []LocalBook{
			// A changed book in a directory that already holds a file.
			book("/l/moby.epub", "Melville/Moby-Dick.epub", "new", 150),
		},
		Device: listing,
		Manifest: manifest(
			entry("/l/moby.epub", "/Books/Melville/Moby-Dick.epub", "old", 100)),
	})

	if dirs := opsOfKind(plan, OpMkdir); len(dirs) != 0 {
		t.Errorf("mkdir ops = %v, want none; those directories already hold files", dirs)
	}
	if uploads := opsOfKind(plan, OpUpload); len(uploads) != 1 {
		t.Errorf("uploads = %v, want the changed book", uploads)
	}

	// A genuinely new directory must still be created.
	plan = Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book("/l/new.epub", "Brand New Author/x.epub", "n", 10)},
		Device:   listing,
		Manifest: manifest(),
	})
	if dirs := opsOfKind(plan, OpMkdir); !equalStrings(dirs, []string{"/Books/Brand New Author"}) {
		t.Errorf("mkdir ops = %v, want the new directory only", dirs)
	}
}

// Uploads go smallest-first so a full card fails fast rather than after the
// largest transfer.
func TestUploadsAreSmallestFirst(t *testing.T) {
	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Local: []LocalBook{
			book("/l/big.epub", "big.epub", "a", 5_000_000),
			book("/l/small.epub", "small.epub", "b", 1_000),
			book("/l/mid.epub", "mid.epub", "c", 500_000),
		},
		Device:   withDirs(map[device.Path]device.FileEntry{}, "/Books"),
		Manifest: manifest(),
	})

	var sizes []int64
	for _, op := range plan.Ops {
		if op.Kind == OpUpload {
			sizes = append(sizes, op.Size)
		}
	}
	want := []int64{1_000, 500_000, 5_000_000}
	if !reflect.DeepEqual(sizes, want) {
		t.Errorf("upload sizes = %v, want %v", sizes, want)
	}

	if plan.TotalBytes != 5_501_000 {
		t.Errorf("TotalBytes = %d, want 5501000", plan.TotalBytes)
	}
}

// Deletes come last, after uploads, so a failure partway through never leaves
// the device with fewer books than it started with.
func TestDeletesComeLast(t *testing.T) {
	plan := Build(Input{
		Root:  device.NewPath("/Books"),
		Local: []LocalBook{book("/l/new.epub", "new.epub", "n", 100)},
		Device: withDirs(devFiles(
			"/Books/orphan.epub", 50,
			"/Books/gone.epub", 60,
		), "/Books"),
		Manifest: manifest(entry("/l/gone.epub", "/Books/gone.epub", "g", 60)),
		Options:  Options{Prune: true},
	})

	lastUpload, firstDelete := -1, len(plan.Ops)
	for i, op := range plan.Ops {
		if op.Kind == OpUpload {
			lastUpload = i
		}
		if op.Kind == OpDelete && i < firstDelete {
			firstDelete = i
		}
	}
	if lastUpload > firstDelete {
		t.Errorf("a delete was ordered before an upload:\n%s", formatOps(plan.Ops))
	}
}

// Directories must never be planned for deletion. Treating a directory as an
// orphan would make --prune attempt to delete the sync root itself.
func TestDirectoriesAreNeverOrphansOrDeleted(t *testing.T) {
	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Device: withDirs(devFiles("/Books/Melville/Moby-Dick.epub", 100),
			"/Books", "/Books/Melville", "/Books/Empty Folder"),
		Manifest: manifest(),
		Options:  Options{Prune: true},
	})

	for _, o := range plan.Orphans {
		if o == "/Books" || o == "/Books/Melville" || o == "/Books/Empty Folder" {
			t.Errorf("directory %s reported as an orphan", o)
		}
	}
	for _, op := range plan.Ops {
		if op.Kind != OpDelete {
			continue
		}
		if op.DevicePath == "/Books" || op.DevicePath == "/Books/Melville" ||
			op.DevicePath == "/Books/Empty Folder" {
			t.Errorf("plan would delete the directory %s", op.DevicePath)
		}
	}

	// The actual file is still correctly identified as an orphan.
	if !equalStrings(opsOfKind(plan, OpDelete), []string{"/Books/Melville/Moby-Dick.epub"}) {
		t.Errorf("deletes = %v, want only the orphaned file", opsOfKind(plan, OpDelete))
	}
}

// shelf's own state files on the device are not orphans.
func TestShelfStateIsNotAnOrphan(t *testing.T) {
	plan := Build(Input{
		Root: device.NewPath("/Books"),
		Device: withDirs(devFiles(
			"/shelf/device.json", 120,
			"/shelf/manifest.json", 4000,
		), "/Books", "/shelf"),
		Manifest: manifest(),
		Options:  Options{Prune: true},
	})

	if len(plan.Orphans) != 0 {
		t.Errorf("shelf's own state was reported as orphaned: %v", plan.Orphans)
	}
	if dels := opsOfKind(plan, OpDelete); len(dels) != 0 {
		t.Errorf("--prune tried to delete shelf's own state: %v", dels)
	}
}

// The firmware's cache directories must never appear in a plan, even under
// --prune. Deleting them destroys every reading position on the device.
func TestProtectedPathsAreNeverPlanned(t *testing.T) {
	plan := Build(Input{
		Root: device.NewPath("/"),
		Device: devFiles(
			"/.crosspoint/settings.json", 100,
			"/.crosspoint/epub_abc/progress.bin", 200,
			"/XTCache/thumb.bin", 300,
			"/System Volume Information/x", 400,
		),
		Manifest: manifest(),
		Options:  Options{Prune: true},
	})

	for _, op := range plan.Ops {
		if device.IsProtected(op.DevicePath) {
			t.Errorf("plan touches a protected path: %s", op)
		}
	}
	for _, o := range plan.Orphans {
		if device.IsProtected(o) {
			t.Errorf("protected path reported as an orphan: %s", o)
		}
	}
	if len(plan.Ops) != 0 {
		t.Errorf("expected an empty plan, got:\n%s", formatOps(plan.Ops))
	}
}

// Planning must be deterministic: the same input always yields the same plan.
// A dry run is only meaningful if the real run does the same thing.
func TestPlanIsDeterministic(t *testing.T) {
	build := func() Plan {
		return Build(Input{
			Root: device.NewPath("/Books"),
			Local: []LocalBook{
				book("/l/c.epub", "c.epub", "c", 300),
				book("/l/a.epub", "a.epub", "a", 100),
				book("/l/b.epub", "b.epub", "b", 200),
			},
			Device:   withDirs(devFiles("/Books/orphan.epub", 9), "/Books"),
			Manifest: manifest(),
		})
	}

	first := build()
	for i := 0; i < 20; i++ {
		got := build()
		if !reflect.DeepEqual(formatOps(first.Ops), formatOps(got.Ops)) {
			t.Fatalf("plan differed between runs:\n%s\nvs\n%s",
				formatOps(first.Ops), formatOps(got.Ops))
		}
	}
}

// A second sync with nothing changed must be a complete no-op. This is the
// core assertion of the whole sync design.
func TestRepeatedSyncIsNoOp(t *testing.T) {
	root := device.NewPath("/Books")
	books := []LocalBook{
		book("/l/a.epub", "Le Guin/a.epub", "aaa", 100),
		book("/l/b.epub", "Melville/b.epub", "bbb", 200),
	}

	// First sync against an empty device.
	first := Build(Input{
		Root: root, Local: books,
		Device:   map[device.Path]device.FileEntry{},
		Manifest: manifest(),
	})
	if uploads := len(opsOfKind(first, OpUpload)); uploads != 2 {
		t.Fatalf("first sync uploads = %d, want 2", uploads)
	}

	// Apply the plan to a simulated device and manifest.
	m := manifest()
	deviceState := map[device.Path]device.FileEntry{}
	for _, op := range first.Ops {
		switch op.Kind {
		case OpMkdir:
			deviceState[op.DevicePath] = device.FileEntry{Name: op.DevicePath.Base(), IsDirectory: true}
		case OpUpload:
			deviceState[op.DevicePath] = device.FileEntry{Name: op.DevicePath.Base(), Size: op.Size}
			m.Put(Entry{
				SHA256: op.SHA256, DevicePath: op.DevicePath.String(),
				DeviceSize: op.Size, LocalPath: op.LocalPath,
			})
		}
	}

	second := Build(Input{Root: root, Local: books, Device: deviceState, Manifest: m})
	if !second.IsEmpty() {
		t.Errorf("the second sync was not a no-op:\n%s", formatOps(second.Ops))
	}
	if second.Unchanged != 2 {
		t.Errorf("unchanged = %d, want 2", second.Unchanged)
	}
	if len(second.Orphans) != 0 {
		t.Errorf("orphans on an unchanged re-sync: %v", second.Orphans)
	}
	if len(second.Warnings) != 0 {
		t.Errorf("warnings on an unchanged re-sync: %v", second.Warnings)
	}
}

func TestNilManifestIsHandled(t *testing.T) {
	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book("/l/a.epub", "a.epub", "a", 10)},
		Device:   map[device.Path]device.FileEntry{},
		Manifest: nil,
	})
	if len(opsOfKind(plan, OpUpload)) != 1 {
		t.Errorf("a nil manifest should plan the book as new: %s", formatOps(plan.Ops))
	}
}

func TestCounts(t *testing.T) {
	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book("/l/a.epub", "Sub/a.epub", "a", 10)},
		Device:   withDirs(devFiles("/Books/orphan.epub", 5), "/Books"),
		Manifest: manifest(),
		Options:  Options{Prune: true},
	})

	mkdirs, uploads, deletes, moves := plan.Counts()
	if mkdirs != 1 || uploads != 1 || deletes != 1 || moves != 0 {
		t.Errorf("counts = %d/%d/%d/%d, want 1/1/1/0", mkdirs, uploads, deletes, moves)
	}
}

// --- helpers ---

func equalStrings(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func formatOps(ops []Op) string {
	if len(ops) == 0 {
		return "  (none)"
	}
	var b strings.Builder
	for _, op := range ops {
		fmt.Fprintf(&b, "  %s  [%s]\n", op, op.Reason)
	}
	return b.String()
}
