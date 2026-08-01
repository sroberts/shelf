package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ImportMode selects what happens to the source file.
type ImportMode string

const (
	ImportCopy ImportMode = "copy"
	ImportMove ImportMode = "move"
	ImportLink ImportMode = "link" // hard link, falling back to copy
)

// ImportOptions controls an import.
type ImportOptions struct {
	Mode     ImportMode
	Template string
	// DryRun computes destinations without touching the filesystem.
	DryRun bool
	// Overwrite permits replacing an existing destination file.
	Overwrite bool
}

// ImportResult describes what happened to one file.
type ImportResult struct {
	Source string
	Dest   string
	Action string // imported | would-import | skipped
	Err    error
}

// Import places files into the library according to the naming template.
//
// This is one of only two operations that may decide a book's path; the other
// is `shelf organize`. Metadata edits never move files, because a moved book
// loses its reading position on the device.
func (db *DB) Import(ctx context.Context, root string, sources []string, opts ImportOptions) ([]ImportResult, error) {
	if opts.Mode == "" {
		opts.Mode = ImportCopy
	}
	if opts.Template == "" {
		return nil, errors.New("import: naming template is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	var results []ImportResult
	// Destinations claimed within this run, so two sources cannot resolve to
	// the same path without one of them being renamed.
	claimed := map[string]bool{}

	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return results, err
		}

		res := ImportResult{Source: src}
		dest, err := db.importDest(root, src, opts, claimed)
		if err != nil {
			res.Err = err
			res.Action = "skipped"
			results = append(results, res)
			continue
		}
		res.Dest = dest
		claimed[FoldPath(dest)] = true

		if opts.DryRun {
			res.Action = "would-import"
			results = append(results, res)
			continue
		}

		if err := placeFile(src, dest, opts.Mode); err != nil {
			res.Err = err
			res.Action = "skipped"
			results = append(results, res)
			continue
		}

		res.Action = "imported"
		results = append(results, res)
	}
	return results, nil
}

// importDest computes the destination path for one source file.
func (db *DB) importDest(root, src string, opts ImportOptions, claimed map[string]bool) (string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", src)
	}

	format, ok := FormatOf(src)
	if !ok {
		return "", fmt.Errorf("unsupported format %q", filepath.Ext(src))
	}

	// Read metadata from the source so the template has something to work with.
	book := &Book{
		Path:      NormalizePath(src),
		Size:      info.Size(),
		MTimeUnix: info.ModTime().Unix(),
		Format:    format,
	}
	if format == FormatEPUB {
		// A book whose metadata will not parse still imports, under its filename.
		_ = applyEPUBMetadata(book, src, false)
	}
	if book.Title == "" {
		book.Title = titleFromFilename(src)
	}

	rel, err := RenderTemplate(opts.Template, book)
	if err != nil {
		return "", err
	}

	dest := filepath.Join(root, filepath.FromSlash(rel)+strings.ToLower(filepath.Ext(src)))
	return db.uniqueDest(dest, src, opts, claimed)
}

// uniqueDest resolves a collision by appending a counter, unless the existing
// file is the same file or the caller asked to overwrite.
func (db *DB) uniqueDest(dest, src string, opts ImportOptions, claimed map[string]bool) (string, error) {
	ext := filepath.Ext(dest)
	base := strings.TrimSuffix(dest, ext)

	for i := 0; i < 100; i++ {
		candidate := dest
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i+1, ext)
		}

		// Case-insensitive collision check: the destination may live on a
		// case-insensitive filesystem, and it will certainly end up on a
		// case-insensitive SD card.
		if claimed[FoldPath(candidate)] {
			continue
		}

		existing, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}

		if opts.Overwrite {
			return candidate, nil
		}
		// Importing a file onto itself is a no-op, not a collision.
		if sameInode(existing, src) {
			return "", fmt.Errorf("%s is already in the library", src)
		}
	}
	return "", fmt.Errorf("could not find a free name for %s", src)
}

// sameInode reports whether info describes the file at path.
func sameInode(info os.FileInfo, path string) bool {
	other, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(info, other)
}

// placeFile moves, links, or copies src to dest.
func placeFile(src, dest string, mode ImportMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}

	switch mode {
	case ImportMove:
		if err := os.Rename(src, dest); err == nil {
			return nil
		}
		// Rename fails across filesystems; fall back to copy-then-remove.
		if err := copyFile(src, dest); err != nil {
			return err
		}
		return os.Remove(src)

	case ImportLink:
		if err := os.Link(src, dest); err == nil {
			return nil
		}
		// Hard links fail across filesystems and on some filesystems entirely.
		return copyFile(src, dest)

	default:
		return copyFile(src, dest)
	}
}

// copyFile copies src to dest atomically, via a temp file and rename.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".shelf-import-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dest); err != nil {
		os.Remove(name)
		return fmt.Errorf("place %s: %w", dest, err)
	}
	return nil
}
