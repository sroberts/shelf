// Package sdcard implements the sync engine's Transport over a mounted volume.
//
// The reader's USB-C port is not a data path -- the ESP32-C3 exposes only a
// USB Serial/JTAG controller, so there is no mass-storage mode to mount. The
// offline route is to take the microSD card out and put it in a card reader,
// which is what this package drives.
//
// The consequence is worth stating plainly: with no firmware in between, shelf
// is directly responsible for a FAT32 volume that holds every reading position
// the device has recorded. Every guard the HTTP client gets from the firmware
// has to be enforced here instead.
package sdcard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"

	"github.com/sroberts/shelf/internal/device"
)

// Volume is a mounted CrossPoint SD card.
//
// It satisfies sync.Transport, so the planner, the executor, the manifest, and
// path pinning all work unchanged: the card is just another target that
// happens to be reachable without Wi-Fi.
type Volume struct {
	mount string
}

// ErrOutsideMount means a device path translated to somewhere outside the
// mount point. It should be impossible to reach through the planner, which is
// exactly why it is checked.
var ErrOutsideMount = errors.New("sdcard: path escapes the mount point")

// Open prepares a mounted volume for use.
//
// It confirms the mount exists and is a directory, but deliberately does not
// require it to look like a CrossPoint card -- a brand new card is empty, and
// refusing to write to one would make the first sync impossible. Verify is the
// check that catches the wrong volume.
func Open(mount string) (*Volume, error) {
	if strings.TrimSpace(mount) == "" {
		return nil, errors.New("sdcard: no mount point given")
	}

	abs, err := filepath.Abs(mount)
	if err != nil {
		return nil, fmt.Errorf("sdcard: resolve %s: %w", mount, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"sdcard: %s is not mounted (insert the card, or check the mount path)", abs)
		}
		return nil, fmt.Errorf("sdcard: %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sdcard: %s is not a directory", abs)
	}

	return &Volume{mount: abs}, nil
}

// Mount returns the volume's root on the local filesystem.
func (v *Volume) Mount() string { return v.mount }

// Verify checks that this volume is the card the manifest describes.
//
// Writing a library onto the wrong volume is the destructive failure mode of
// this transport: a mistyped mount path is one keystroke away from a home
// directory. Two things are checked, in order of confidence:
//
//   - If the card already carries shelf's marker, its UUID must match. A
//     mismatch means a different card, and continuing would orphan every
//     pinned path in the manifest.
//   - Otherwise the volume must at least look like a reader's card, or be
//     empty. A populated volume with no CrossPoint markings is far more likely
//     to be the wrong disk than a card shelf has simply never seen.
//
// wantUUID may be empty, which skips the first check for a first-ever sync.
func (v *Volume) Verify(wantUUID string) error {
	marker, err := v.readMarker()
	switch {
	case err != nil:
		return err

	case marker != "":
		if wantUUID != "" && marker != wantUUID {
			return fmt.Errorf(
				"sdcard: %s carries device %s, but the manifest is for %s; "+
					"this is a different card, and syncing would orphan every pinned path",
				v.mount, marker, wantUUID)
		}
		return nil
	}

	// No marker. Accept a card the firmware has clearly touched, or an empty
	// volume, and refuse anything else.
	if _, err := os.Stat(filepath.Join(v.mount, ".crosspoint")); err == nil {
		return nil
	}

	entries, err := os.ReadDir(v.mount)
	if err != nil {
		return fmt.Errorf("sdcard: read %s: %w", v.mount, err)
	}
	if visibleCount(entries) == 0 {
		return nil
	}

	return fmt.Errorf(
		"sdcard: %s has no .crosspoint directory and no shelf marker, but is not empty; "+
			"refusing to write in case this is the wrong volume", v.mount)
}

// visibleCount counts entries a user would see, ignoring the bookkeeping that
// macOS and Windows sprinkle onto removable media. A card holding nothing but
// .Spotlight-V100 is empty for our purposes.
func visibleCount(entries []os.DirEntry) int {
	n := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "System Volume Information" {
			continue
		}
		n++
	}
	return n
}

// readMarker returns the device UUID recorded on the card, or "" if absent.
func (v *Volume) readMarker() (string, error) {
	local, err := v.local(device.NewPath("/shelf/device.json"))
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(local)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("sdcard: read marker: %w", err)
	}

	// Decoded by hand rather than through sync.DeviceMarker, because importing
	// sync here would be a cycle: sync is what consumes this package.
	var m struct {
		DeviceUUID string `json:"device_uuid"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		// A corrupt marker is not fatal. Treating it as absent falls through
		// to the shape check, which is the more conservative answer anyway.
		return "", nil
	}
	return m.DeviceUUID, nil
}

// local translates a device path to a path on the mounted volume.
//
// Every mutating method goes through this, and it is the only place that knows
// how a device.Path becomes a filesystem path. That is deliberate: it is the
// SD-card counterpart of the guard in device/path.go, and it exists at the
// lowest layer so callers cannot route around it.
func (v *Volume) local(p device.Path) (string, error) {
	if device.IsProtected(p) {
		return "", fmt.Errorf("%w: %s is reserved by the firmware",
			device.ErrProtectedPath, p)
	}

	rel := strings.TrimPrefix(p.String(), "/")
	joined := filepath.Join(v.mount, filepath.FromSlash(rel))

	// Belt and braces against a traversal that survived NewPath's cleaning.
	// filepath.Join already cleans, so this should be unreachable; an
	// unreachable check on the path that can destroy a home directory is
	// cheap.
	if joined != v.mount && !strings.HasPrefix(joined, v.mount+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrOutsideMount, p)
	}
	return joined, nil
}

// Mkdir creates a directory on the card.
//
// MkdirAll rather than Mkdir, and nil rather than an error when it already
// exists. The executor treats "already exists" as success, and the planner
// emits parents before children, so this is more permissive than it needs to
// be in both directions -- which costs nothing and removes a class of ordering
// bug the firmware transport has to care about.
func (v *Volume) Mkdir(ctx context.Context, p device.Path) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	local, err := v.local(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return mapError(fmt.Errorf("sdcard: mkdir %s: %w", p, err))
	}
	return nil
}

// Upload writes a file to the card.
//
// Written to a temporary file in the destination directory and renamed into
// place. The firmware deletes partial uploads when a connection drops; a raw
// mount has no such courtesy, and a truncated file whose size happens to match
// the manifest would be indistinguishable from a good one on the next run.
// This is the same write-then-rename discipline epub/write.go uses.
func (v *Volume) Upload(ctx context.Context, dest device.Path, r io.Reader, size int64, opts device.UploadOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	local, err := v.local(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return mapError(fmt.Errorf("sdcard: create parent of %s: %w", dest, err))
	}

	tmp, err := os.CreateTemp(filepath.Dir(local), ".shelf-*.tmp")
	if err != nil {
		return mapError(fmt.Errorf("sdcard: create temp for %s: %w", dest, err))
	}
	tmpName := tmp.Name()

	// Remove the temp file on every failure path. A no-op once the rename has
	// succeeded, because the name no longer exists.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	written, err := copyWithProgress(ctx, tmp, r, dest, size, opts.Progress)
	if err != nil {
		return mapError(err)
	}
	// fsync before the rename: on removable media the rename can otherwise
	// land before the bytes, leaving a correctly named empty file if the card
	// is pulled at the wrong moment.
	if err := tmp.Sync(); err != nil {
		return mapError(fmt.Errorf("sdcard: flush %s: %w", dest, err))
	}
	if err := tmp.Close(); err != nil {
		return mapError(fmt.Errorf("sdcard: close %s: %w", dest, err))
	}

	if size > 0 && written != size {
		return fmt.Errorf("sdcard: %s: wrote %d bytes, expected %d", dest, written, size)
	}

	if err := os.Rename(tmpName, local); err != nil {
		return mapError(fmt.Errorf("sdcard: place %s: %w", dest, err))
	}
	return nil
}

// copyWithProgress streams r into w, reporting progress and honouring ctx.
func copyWithProgress(ctx context.Context, w io.Writer, r io.Reader, dest device.Path,
	size int64, progress func(device.Progress)) (int64, error) {

	buf := make([]byte, 64*1024)
	var total int64

	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}

		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return total, fmt.Errorf("sdcard: write %s: %w", dest, err)
			}
			total += int64(n)
			if progress != nil {
				progress(device.Progress{Path: dest, Sent: total, Total: size})
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, fmt.Errorf("sdcard: read source for %s: %w", dest, readErr)
		}
	}
}

// Delete removes a file from the card.
//
// Files only. shelf never plans a directory removal -- treating a directory as
// an orphan once nearly deleted a sync root -- so a directory arriving here
// means something upstream is wrong, and refusing is better than recursing.
func (v *Volume) Delete(ctx context.Context, p device.Path) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	local, err := v.local(p)
	if err != nil {
		return err
	}

	info, err := os.Lstat(local)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", device.ErrNotFound, p)
		}
		return mapError(err)
	}
	if info.IsDir() {
		return fmt.Errorf("sdcard: %s is a directory; shelf does not delete directories", p)
	}
	if err := os.Remove(local); err != nil {
		return mapError(fmt.Errorf("sdcard: delete %s: %w", p, err))
	}
	return nil
}

// Move renames a file on the card, creating the destination's parent.
func (v *Volume) Move(ctx context.Context, from, to device.Path) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	src, err := v.local(from)
	if err != nil {
		return err
	}
	dst, err := v.local(to)
	if err != nil {
		return err
	}

	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", device.ErrNotFound, from)
		}
		return mapError(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return mapError(fmt.Errorf("sdcard: create parent of %s: %w", to, err))
	}
	if err := os.Rename(src, dst); err != nil {
		return mapError(fmt.Errorf("sdcard: move %s to %s: %w", from, to, err))
	}
	return nil
}

// Status describes the volume.
//
// A card reader cannot report firmware, heap, or signal -- those come from a
// running device over HTTP, and there is no device here. Mode is set to
// device.ModeLocal so the firmware compatibility gate knows to stand down
// rather than reject an empty version string.
func (v *Volume) Status(ctx context.Context) (*device.Status, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &device.Status{Mode: device.ModeLocal}, nil
}

// ListRecursive walks the card under root, producing the same map the planner
// consumes from the HTTP client.
//
// Names are normalized to NFC. APFS returns NFD, and without this every book
// with an accent in its name would look like a book the manifest has never
// seen -- a full re-upload of the library, every time, on a Mac.
func (v *Volume) ListRecursive(ctx context.Context, root device.Path) (map[device.Path]device.FileEntry, error) {
	out := map[device.Path]device.FileEntry{}

	localRoot, err := v.local(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(localRoot); err != nil {
		if os.IsNotExist(err) {
			return out, nil // a missing directory is an empty one
		}
		return nil, mapError(err)
	}

	queue := []device.Path{root}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		dir := queue[0]
		queue = queue[1:]

		localDir, err := v.local(dir)
		if err != nil {
			return out, err
		}
		entries, err := os.ReadDir(localDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return out, mapError(err)
		}

		for _, e := range entries {
			name := norm.NFC.String(e.Name())
			full := dir.Join(name)

			// Never descend into the firmware's cache directories: /.crosspoint
			// holds the reading positions this whole project exists to protect.
			if device.IsProtected(full) {
				continue
			}
			if e.IsDir() {
				queue = append(queue, full)
				out[full] = device.FileEntry{Name: name, IsDirectory: true}
				continue
			}

			info, err := e.Info()
			if err != nil {
				// A file that vanished mid-walk is not an error worth aborting
				// a sync over; the next run will see it, or not.
				continue
			}
			out[full] = device.FileEntry{
				Name:   name,
				Size:   info.Size(),
				IsEpub: strings.EqualFold(filepath.Ext(name), ".epub"),
			}
		}
	}
	return out, nil
}

// mapError translates filesystem errors into the vocabulary the executor
// already understands, so its abort logic keeps working.
//
// Disk-full is the one that matters: the executor aborts a whole run on
// device.ErrDiskFull rather than retrying, and without this mapping an ENOSPC
// would look like an ordinary failure and be retried three times per book for
// the rest of the library.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w (is the card mounted read-only?)", err)
	case isNoSpace(err):
		return fmt.Errorf("%w: %v", device.ErrDiskFull, err)
	}
	return err
}

// isNoSpace reports whether an error is the filesystem running out of room.
//
// Matched on the errno rather than the message: the text is localized on some
// systems, and this decides whether the executor aborts the run or retries
// every remaining book three times against a full card.
func isNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC)
}

// MkdirAll creates a directory and every missing parent.
//
// Mkdir already does this, so the two are the same call. Both names exist
// because the HTTP client must distinguish them -- the firmware's mkdir does
// not create intermediate parents -- and callers should not have to know which
// transport they hold.
func (v *Volume) MkdirAll(ctx context.Context, p device.Path) error {
	return v.Mkdir(ctx, p)
}

// Download opens a file on the card for reading.
//
// The caller closes it. Protected paths are refused here too: /.crosspoint
// holds reading positions, and while reading one is harmless, allowing it
// would make the guard something callers reason about rather than something
// they cannot reach.
func (v *Volume) Download(ctx context.Context, p device.Path) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	local, err := v.local(p)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(local)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", device.ErrNotFound, p)
		}
		return nil, mapError(err)
	}
	return f, nil
}
