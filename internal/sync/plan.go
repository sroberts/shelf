package sync

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sroberts/shelf/internal/device"
)

// Build is a pure function: no I/O, no clock, no randomness. Everything it
// needs arrives in Input, so the decision table below is directly testable and
// a dry run is guaranteed to describe exactly what a real run would do.

// OpKind is the type of a planned operation.
type OpKind string

const (
	OpMkdir  OpKind = "mkdir"
	OpUpload OpKind = "upload"
	OpDelete OpKind = "delete"
	OpMove   OpKind = "move"
)

// Op is one planned operation.
type Op struct {
	Kind       OpKind
	DevicePath device.Path
	// From is the source path for a move.
	From device.Path
	// LocalPath is the file to read for an upload.
	LocalPath string
	Size      int64
	SHA256    string
	// Reason is shown in dry-run output.
	Reason string
}

// String renders an operation for display.
func (o Op) String() string {
	switch o.Kind {
	case OpMkdir:
		return fmt.Sprintf("mkdir  %s", o.DevicePath)
	case OpUpload:
		return fmt.Sprintf("upload %s (%d bytes)", o.DevicePath, o.Size)
	case OpDelete:
		return fmt.Sprintf("delete %s", o.DevicePath)
	case OpMove:
		return fmt.Sprintf("move   %s -> %s", o.From, o.DevicePath)
	default:
		return string(o.Kind)
	}
}

// LocalBook is one candidate for syncing.
type LocalBook struct {
	// Path is the local file.
	Path string
	// SHA256 is the hash of the bytes that will be sent.
	SHA256 string
	Size   int64
	// RelPath is the device-relative destination rendered from the naming
	// template. It is used only for books that are not already pinned.
	RelPath string
}

// Options control planning policy.
type Options struct {
	// Prune deletes device files that shelf placed but the shelf no longer
	// contains, and orphans that shelf did not place.
	Prune bool

	// RespectDeviceDeletes treats a book deleted on the device as intentional
	// and drops it from the manifest rather than re-uploading it.
	RespectDeviceDeletes bool

	// Repath allows moving already-pinned books to the path the current
	// template renders. This destroys reading positions and is never implied.
	Repath bool
}

// Input is everything the planner needs.
type Input struct {
	// Root is the device directory the shelf syncs into.
	Root device.Path
	// Local is the set of books that should be on the device.
	Local []LocalBook
	// Device is the current device listing, keyed by full path.
	Device map[device.Path]device.FileEntry
	// Manifest records what shelf placed previously.
	Manifest *Manifest
	Options  Options
}

// Plan is the result of planning.
type Plan struct {
	Ops []Op

	// Orphans are device files shelf did not place. They are reported but only
	// deleted under --prune.
	Orphans []device.Path

	// Warnings describe situations the user should know about but that do not
	// stop the sync.
	Warnings []string

	// RepathWarnings lists books that would lose their reading position. A
	// caller must show these and get confirmation before executing.
	RepathWarnings []string

	// Drops are manifest entries to forget without touching the device.
	Drops []string

	Unchanged  int
	TotalBytes int64
}

// IsEmpty reports whether the plan would change nothing on the device.
func (p *Plan) IsEmpty() bool { return len(p.Ops) == 0 }

// Counts summarizes the plan by operation kind.
func (p *Plan) Counts() (mkdirs, uploads, deletes, moves int) {
	for _, op := range p.Ops {
		switch op.Kind {
		case OpMkdir:
			mkdirs++
		case OpUpload:
			uploads++
		case OpDelete:
			deletes++
		case OpMove:
			moves++
		}
	}
	return
}

// Build computes the operations needed to bring the device into agreement with
// the local shelf.
//
// The decision table, from the spec:
//
//	manifest  device    size    action
//	present   present   match   no-op
//	present   present   differ  re-upload in place
//	present   absent    -       re-upload, or drop under --respect-device-deletes
//	absent    present   -       orphan: report, delete only under --prune
//	present   local gone        delete under --prune, else warn
func Build(in Input) Plan {
	var plan Plan

	if in.Manifest == nil {
		in.Manifest = NewManifest("", in.Root.String())
	}
	byLocal := in.Manifest.ByLocalPath()

	// Device paths this run will occupy, so orphan detection does not flag a
	// file that is about to be written.
	claimed := map[device.Path]bool{}
	// Manifest entries matched to a local book, so the leftovers are the books
	// that vanished locally.
	matched := map[string]bool{}

	var uploads []Op
	neededDirs := map[device.Path]bool{}

	for _, book := range sortedBooks(in.Local) {
		entry, pinned := byLocal[book.Path]

		var dest device.Path
		switch {
		case pinned:
			matched[book.Path] = true
			// Path pinning: the destination comes from the manifest, never
			// from the current template. This is the single most important
			// rule in the project -- recomputing it here is what makes a
			// metadata edit silently destroy a reading position.
			dest = device.NewPath(entry.DevicePath)

			if in.Options.Repath {
				desired := in.Root.Join(book.RelPath)
				if desired != dest {
					plan.RepathWarnings = append(plan.RepathWarnings,
						fmt.Sprintf("%s -> %s (loses reading position)", dest, desired))
					uploads = append(uploads, Op{
						Kind:       OpMove,
						From:       dest,
						DevicePath: desired,
						LocalPath:  book.Path,
						Size:       book.Size,
						SHA256:     book.SHA256,
						Reason:     "repath requested",
					})
					neededDirs[desired.Dir()] = true
					claimed[desired] = true
					claimed[dest] = true
					continue
				}
			}

		default:
			dest = in.Root.Join(book.RelPath)
		}

		claimed[dest] = true

		deviceFile, onDevice := in.Device[dest]

		switch {
		case !pinned:
			// A book shelf has not placed before. If something already sits at
			// the destination, the upload overwrites it, which is the same
			// behaviour as the firmware's own upload endpoint.
			reason := "new book"
			if onDevice {
				reason = "new book (overwrites an existing file at this path)"
			}
			uploads = append(uploads, uploadOp(book, dest, reason))
			neededDirs[dest.Dir()] = true

		case !onDevice:
			if in.Options.RespectDeviceDeletes {
				plan.Drops = append(plan.Drops, book.Path)
				plan.Warnings = append(plan.Warnings,
					fmt.Sprintf("%s was deleted on the device; dropping it from the manifest", dest))
				continue
			}
			uploads = append(uploads, uploadOp(book, dest, "missing from device"))
			neededDirs[dest.Dir()] = true

		case entry.SHA256 != book.SHA256:
			uploads = append(uploads, uploadOp(book, dest, "local file changed"))
			neededDirs[dest.Dir()] = true

		case deviceFile.Size != entry.DeviceSize:
			uploads = append(uploads, uploadOp(book, dest,
				fmt.Sprintf("size differs on device (%d, expected %d)",
					deviceFile.Size, entry.DeviceSize)))
			neededDirs[dest.Dir()] = true

		default:
			plan.Unchanged++
		}
	}

	// Manifest entries whose local file is gone, or that are no longer in the
	// shelf being synced.
	var deletes []Op
	for _, e := range in.Manifest.Entries {
		if matched[e.LocalPath] {
			continue
		}
		dest := device.NewPath(e.DevicePath)
		if _, onDevice := in.Device[dest]; !onDevice {
			// Already gone from both sides; just forget it.
			plan.Drops = append(plan.Drops, e.LocalPath)
			continue
		}

		if in.Options.Prune {
			deletes = append(deletes, Op{
				Kind:       OpDelete,
				DevicePath: dest,
				Reason:     "no longer in the shelf",
			})
			continue
		}
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("%s is on the device but no longer in the shelf; use --prune to remove it", dest))
		claimed[dest] = true
	}

	// Files on the device that shelf never placed.
	for p, e := range in.Device {
		// Directories are never orphans. Treating them as such would make
		// --prune try to delete the sync root itself, and shelf must never
		// plan a directory removal: the firmware refuses non-empty folders,
		// and an empty one costs nothing to leave in place.
		if e.IsDirectory {
			continue
		}
		if claimed[p] || device.IsProtected(p) {
			continue
		}
		if _, known := in.Manifest.ByDevicePath()[p]; known {
			continue
		}
		// shelf's own state files are not orphans.
		if isShelfState(p) {
			continue
		}
		plan.Orphans = append(plan.Orphans, p)

		if in.Options.Prune {
			deletes = append(deletes, Op{
				Kind:       OpDelete,
				DevicePath: p,
				Reason:     "orphan (not placed by shelf)",
			})
		}
	}
	sortPaths(plan.Orphans)

	// Assemble in execution order: directories first (shallowest first, so
	// parents exist before children), then uploads smallest-first so a full
	// card fails fast, then deletes.
	plan.Ops = append(plan.Ops, mkdirOps(neededDirs, in.Root, in.Device)...)

	sort.SliceStable(uploads, func(i, j int) bool {
		if uploads[i].Size != uploads[j].Size {
			return uploads[i].Size < uploads[j].Size
		}
		return uploads[i].DevicePath < uploads[j].DevicePath
	})
	plan.Ops = append(plan.Ops, uploads...)

	sort.SliceStable(deletes, func(i, j int) bool {
		return deletes[i].DevicePath < deletes[j].DevicePath
	})
	plan.Ops = append(plan.Ops, deletes...)

	for _, op := range plan.Ops {
		if op.Kind == OpUpload {
			plan.TotalBytes += op.Size
		}
	}
	sort.Strings(plan.Drops)
	return plan
}

// uploadOp builds an upload operation.
func uploadOp(b LocalBook, dest device.Path, reason string) Op {
	return Op{
		Kind:       OpUpload,
		DevicePath: dest,
		LocalPath:  b.Path,
		Size:       b.Size,
		SHA256:     b.SHA256,
		Reason:     reason,
	}
}

// mkdirOps produces the directory creations needed, shallowest first.
//
// Ordering matters: the firmware's mkdir does not create intermediate parents,
// so a child created before its parent fails.
func mkdirOps(needed map[device.Path]bool, root device.Path, existing map[device.Path]device.FileEntry) []Op {
	present := existingDirs(existing)
	all := map[device.Path]bool{}

	for dir := range needed {
		if dir == "/" || dir == root {
			all[dir] = true
		}
		for _, ancestor := range dir.Ancestors() {
			if withinRoot(root, ancestor) {
				all[ancestor] = true
			}
		}
		if withinRoot(root, dir) {
			all[dir] = true
		}
	}

	var dirs []device.Path
	for dir := range all {
		if dir == "/" || device.IsProtected(dir) {
			continue
		}
		// A directory that already exists on the device needs no mkdir.
		if present[dir] {
			continue
		}
		dirs = append(dirs, dir)
	}

	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].Depth() != dirs[j].Depth() {
			return dirs[i].Depth() < dirs[j].Depth() // shallowest first
		}
		return dirs[i] < dirs[j]
	})

	ops := make([]Op, 0, len(dirs))
	for _, dir := range dirs {
		ops = append(ops, Op{Kind: OpMkdir, DevicePath: dir, Reason: "parent directory"})
	}
	return ops
}

// existingDirs derives the set of directories that already exist on the device.
//
// A directory is known to exist either because the listing names it explicitly
// or because a file sits inside it. The second case matters: a recursive
// listing reports files, so without inferring parents the planner would emit a
// redundant mkdir for every directory on every sync. Those are harmless to
// execute but make dry-run output untrustworthy, which defeats the purpose of
// having one.
func existingDirs(listing map[device.Path]device.FileEntry) map[device.Path]bool {
	out := map[device.Path]bool{}
	for p, e := range listing {
		if e.IsDirectory {
			out[p] = true
		}
		for _, ancestor := range p.Ancestors() {
			out[ancestor] = true
		}
	}
	return out
}

// withinRoot reports whether p is root or lies beneath it.
func withinRoot(root, p device.Path) bool {
	if p == root {
		return true
	}
	prefix := strings.TrimSuffix(root.String(), "/") + "/"
	return strings.HasPrefix(p.String(), prefix)
}

// isShelfState reports whether a device path holds shelf's own bookkeeping.
func isShelfState(p device.Path) bool {
	return p == device.NewPath(ShelfDir) ||
		strings.HasPrefix(p.String(), ShelfDir+"/")
}

// sortedBooks returns books in a deterministic order so plans are reproducible.
func sortedBooks(books []LocalBook) []LocalBook {
	out := make([]LocalBook, len(books))
	copy(out, books)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sortPaths(paths []device.Path) {
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
}
