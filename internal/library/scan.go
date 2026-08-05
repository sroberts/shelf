package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/kosync"
)

// ScanOptions controls a library scan.
type ScanOptions struct {
	// Deep rehashes and re-reads metadata for every file, ignoring the
	// (size, mtime) fast path. Use after an external tool has edited books.
	Deep bool

	// Covers extracts cover thumbnails. Scanning without covers is
	// substantially faster on a large library.
	Covers bool

	// Progress, when set, is called as each file is processed.
	Progress func(ScanProgress)
}

// ScanProgress reports a single file's outcome during a scan.
type ScanProgress struct {
	Path   string
	Action ScanAction
	Err    error
}

// ScanAction is what a scan did with one file.
type ScanAction string

const (
	ActionAdded     ScanAction = "added"
	ActionUpdated   ScanAction = "updated"
	ActionUnchanged ScanAction = "unchanged"
	ActionRemoved   ScanAction = "removed"
	ActionSkipped   ScanAction = "skipped"
	ActionFailed    ScanAction = "failed"
)

// ScanResult summarizes a completed scan.
type ScanResult struct {
	Added     int
	Updated   int
	Unchanged int
	Removed   int
	Skipped   int
	Errors    []ScanError
}

// ScanError records a file that could not be indexed. One bad book must not
// abort a scan of ten thousand.
type ScanError struct {
	Path string
	Err  error
}

func (e ScanError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e ScanError) Unwrap() error { return e.Err }

// Scan walks root and brings the index into agreement with what is on disk.
//
// Change detection is by (size, mtime); files whose stat data is unchanged are
// not opened at all. This is what makes a rescan of a large library cheap.
func (db *DB) Scan(ctx context.Context, root string, opts ScanOptions) (*ScanResult, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve library root: %w", err)
	}
	if fi, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("library root %s: %w", root, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("library root %s is not a directory", root)
	}

	known, err := db.Snapshot()
	if err != nil {
		return nil, err
	}

	res := &ScanResult{}
	seen := make(map[string]bool, len(known))

	report := func(path string, action ScanAction, err error) {
		if opts.Progress != nil {
			opts.Progress(ScanProgress{Path: path, Action: action, Err: err})
		}
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// An unreadable directory is reported but does not stop the walk.
			res.Errors = append(res.Errors, ScanError{Path: path, Err: err})
			report(path, ActionFailed, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if skipDir(d.Name()) && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		format, ok := FormatOf(path)
		if !ok {
			res.Skipped++
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			res.Skipped++
			return nil
		}

		canonical := NormalizePath(path)
		seen[canonical] = true

		info, err := d.Info()
		if err != nil {
			res.Errors = append(res.Errors, ScanError{Path: path, Err: err})
			report(path, ActionFailed, err)
			return nil
		}

		prev, existing := known[canonical]
		if existing && !opts.Deep &&
			prev.Size == info.Size() && prev.MTimeUnix == info.ModTime().Unix() {
			res.Unchanged++
			report(path, ActionUnchanged, nil)
			return nil
		}

		// Pass the walk's own path to the indexer, not the normalized form:
		// on macOS the normalized name may not be what the filesystem accepts.
		book, err := indexFile(path, canonical, info.Size(), info.ModTime().Unix(), format, opts.Covers)
		if err != nil {
			res.Errors = append(res.Errors, ScanError{Path: path, Err: err})
			report(path, ActionFailed, err)
			return nil
		}
		if existing {
			book.AddedUnix = 0 // preserved by the upsert
		}

		if err := db.Upsert(book); err != nil {
			res.Errors = append(res.Errors, ScanError{Path: path, Err: err})
			report(path, ActionFailed, err)
			return nil
		}

		if existing {
			res.Updated++
			report(path, ActionUpdated, nil)
		} else {
			res.Added++
			report(path, ActionAdded, nil)
		}
		return nil
	})
	if walkErr != nil {
		return res, walkErr
	}

	// Anything indexed but no longer on disk under root is dropped. Books
	// outside root are left alone so that a scan of one directory does not
	// evict a library rooted elsewhere.
	for path := range known {
		if seen[path] {
			continue
		}
		if !withinRoot(root, path) {
			continue
		}
		if err := db.Delete(path); err != nil {
			res.Errors = append(res.Errors, ScanError{Path: path, Err: err})
			continue
		}
		res.Removed++
		report(path, ActionRemoved, nil)
	}

	if opts.Deep {
		if err := db.RebuildFTS(); err != nil {
			return res, err
		}
	}
	return res, nil
}

// withinRoot reports whether path lies under root.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(NormalizePath(root), path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// skipDir reports whether a directory should be excluded from scanning.
func skipDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", ".shelf", "System Volume Information":
		return true
	}
	// Hidden directories are not part of a library.
	return strings.HasPrefix(name, ".")
}

// indexFile hashes a file and extracts whatever metadata its format offers.
func indexFile(osPath, canonical string, size, mtime int64, format Format, covers bool) (*Book, error) {
	sum, err := hashFile(osPath)
	if err != nil {
		return nil, err
	}

	// The KOReader document id is what reading progress is keyed by. Computing
	// it here, once per changed file, is what lets a listing show percentages
	// without rehashing the library.
	docID, err := kosync.DocumentID(osPath)
	if err != nil {
		// Not fatal: a book that cannot be hashed is still a book, it just
		// cannot be matched to progress.
		docID = ""
	}

	b := &Book{
		SHA256:    sum,
		DocID:     docID,
		Path:      canonical,
		Size:      size,
		MTimeUnix: mtime,
		Format:    format,
	}

	if format == FormatEPUB {
		if err := applyEPUBMetadata(b, osPath, covers); err != nil {
			// A book whose metadata will not parse is still a book. Index it
			// by filename rather than dropping it from the library.
			b.Title = titleFromFilename(osPath)
		}
	} else {
		b.Title = titleFromFilename(osPath)
	}

	if b.Title == "" {
		b.Title = titleFromFilename(osPath)
	}
	return b, nil
}

func applyEPUBMetadata(b *Book, osPath string, covers bool) error {
	f, err := epub.Open(osPath)
	if err != nil {
		return err
	}
	defer f.Close()

	m := f.Metadata()
	b.Title = NormalizeString(m.Title)
	b.AuthorSort = NormalizeString(m.AuthorSort())
	b.Series = NormalizeString(m.Series)
	b.SeriesIndex = m.SeriesIndex
	b.Language = m.Language
	b.Publisher = NormalizeString(m.Publisher)
	b.PubDate = m.Date
	b.Identifiers = m.Identifiers
	b.Tags = m.Subjects

	for _, name := range m.AuthorNames() {
		b.Authors = append(b.Authors, NormalizeString(name))
	}

	// meta_json retains the parsed metadata so a future feature can round-trip
	// fields shelf does not model as columns.
	if data, err := json.Marshal(m); err == nil {
		b.MetaJSON = string(data)
	}

	if covers {
		// A missing cover is normal and must not fail the scan.
		if thumb, err := f.CoverThumbnailPNG(epub.DefaultThumbnailWidth); err == nil {
			b.Cover = thumb
		}
	}
	return nil
}

// titleFromFilename derives a display title for books with no usable metadata.
func titleFromFilename(path string) string {
	base := filepath.Base(path)
	return NormalizeString(strings.TrimSuffix(base, filepath.Ext(base)))
}

// hashFile computes the SHA-256 of a file's full contents.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile is the exported form, used by the sync engine to hash the bytes it
// is about to send.
func HashFile(path string) (string, error) { return hashFile(path) }
