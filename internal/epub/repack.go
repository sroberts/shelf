package epub

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Archive rewriting for callers outside this package.
//
// internal/convert needs to replace image entries inside an EPUB, which means
// rebuilding the archive. Rebuilding it correctly is the fiddly part: the
// mimetype entry must come first, stored, with no extra field, or readers
// reject the file. That rule lives here, at the lowest layer, so a caller
// cannot route around it — the same reason the device path guard lives in
// device/path.go.

// ErrDropEntry tells Repack to omit an entry from the output archive.
//
// Dropping a file that the package document still references produces an
// invalid EPUB, so a caller that drops content is responsible for patching the
// OPF manifest in the same pass.
var ErrDropEntry = errors.New("epub: drop entry")

// Entry describes one archive member offered to a RewriteFunc.
type Entry struct {
	// Name is the archive path, with any leading "./" removed.
	Name string
	// Size is the uncompressed size in bytes.
	Size int64

	file *zip.File
}

// Open reads the entry's content.
//
// It is a method rather than a pre-read []byte because most entries in a
// typical rewrite are copied through untouched, and decompressing every one of
// them to offer bytes nobody wants is pure waste on a large book.
func (e Entry) Open() ([]byte, error) {
	rc, err := e.file.Open()
	if err != nil {
		return nil, fmt.Errorf("epub: open %s: %w", e.Name, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("epub: read %s: %w", e.Name, err)
	}
	return body, nil
}

// IsOPF reports whether this entry is the package document.
func (f *File) IsOPF(name string) bool { return name == f.opfPath }

// RewriteFunc decides what happens to a single archive entry.
//
// Returning (nil, nil) copies the entry through byte-for-byte without
// recompressing it. Returning a body replaces the entry. Returning
// ErrDropEntry omits it. Any other error aborts the repack.
type RewriteFunc func(e Entry) ([]byte, error)

// Repack writes a new EPUB at dst holding every entry of the EPUB at src, with
// each entry passed through fn.
//
// The write is atomic: the archive is built in a temporary file beside dst and
// renamed into place, so a crash mid-write cannot leave a truncated book. src
// and dst may be the same path.
func Repack(src, dst string, fn RewriteFunc) error {
	f, err := Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	// Match the source's permissions rather than inventing them, so an
	// optimized copy is no more permissive than the book it came from.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(src); err == nil {
		mode = fi.Mode().Perm()
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("epub: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".shelf-repack-*.epub")
	if err != nil {
		return fmt.Errorf("epub: create temp file next to %s: %w", dst, err)
	}
	tmpName := tmp.Name()

	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}

	if err := rewrite(tmp, f, fn); err != nil {
		return fail(fmt.Errorf("%s: %w", src, err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("epub: sync %s: %w", tmpName, err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("epub: close %s: %w", tmpName, err)
	}

	// Release the source before renaming: on Windows and some network
	// filesystems an open file cannot be replaced, and src may equal dst.
	f.Close()

	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("epub: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("epub: replace %s: %w", dst, err)
	}
	return nil
}

// rewrite emits the new archive.
func rewrite(w io.Writer, src *File, fn RewriteFunc) error {
	zw := zip.NewWriter(w)

	if err := writeMimetype(zw); err != nil {
		return err
	}

	for _, e := range src.zr.File {
		name := strings.TrimPrefix(e.Name, "./")
		if name == mimetypePath {
			continue // already written first
		}
		// Directory members carry no content and nothing references them by
		// name; passing them to fn would only invite a caller to mangle one.
		if e.FileInfo().IsDir() {
			if err := copyRaw(zw, e); err != nil {
				return fmt.Errorf("copy %s: %w", e.Name, err)
			}
			continue
		}

		body, err := fn(Entry{Name: name, Size: int64(e.UncompressedSize64), file: e})
		switch {
		case errors.Is(err, ErrDropEntry):
			continue
		case err != nil:
			return fmt.Errorf("rewrite %s: %w", e.Name, err)
		case body == nil:
			if err := copyRaw(zw, e); err != nil {
				return fmt.Errorf("copy %s: %w", e.Name, err)
			}
		default:
			if err := writeDeflated(zw, e, body); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}
