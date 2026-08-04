package sync

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/convert"
)

// Preparing books for the planner.
//
// This is the step that turns "what is in the library" into "what bytes go to
// the device": PDFs are converted, EPUBs are optimized for the target panel,
// and each result is hashed. All of it is I/O, and none of it belongs in
// plan.go — Build stays a pure function so the decision table is directly
// testable and --dry-run describes exactly what a real run does.
//
// Two rules govern what comes out:
//
//  1. Identity is the library source. A book's manifest key is the file on
//     disk in the library, never the cache artifact. Artifact paths move when
//     the converter version or the optimizer profile changes, and a moving key
//     would make the planner see a new book and orphan a pinned one.
//  2. The hash describes the bytes actually sent. That is what makes an
//     unchanged profile produce no churn, and what makes a changed one
//     re-upload exactly the books it affected.

// Candidate is a library book offered for syncing.
//
// RelPath carries no extension: the destination's extension depends on what
// conversion produces, which is not known until it runs.
type Candidate struct {
	// Path is the file in the library.
	Path string
	// Format is the lowercase source format, without a dot ("epub", "pdf").
	Format string
	// SHA256 and Size describe the source file, as recorded in the index.
	SHA256 string
	Size   int64
	// RelPath is the device-relative destination without an extension.
	RelPath string
}

// Preparer turns candidates into planner input.
type Preparer struct {
	// Convert supplies converted EPUBs. Nil disables conversion, which makes
	// any format the device cannot render a skip.
	Convert *convert.Cache
	// Resolve picks a converter for a source file. Nil disables conversion.
	Resolve func(src string) (*convert.Converter, error)

	// Optimize supplies device-targeted EPUBs. Nil disables optimization.
	Optimize *convert.OptimizeCache
	// Profile is the optimization target. Ignored when Optimize is nil.
	Profile convert.Profile

	// Syncable reports whether the device can render a format directly. A
	// format that is neither syncable nor convertible is skipped.
	Syncable func(format string) bool
}

// PreparerConfig is the settings NewPreparer needs.
//
// It exists so the CLI and the TUI build an identical pipeline rather than
// each assembling one and drifting apart.
type PreparerConfig struct {
	// ConvertCacheDir and OptimizedDir are the two derived-artifact caches.
	ConvertCacheDir string
	OptimizedDir    string

	// ConverterName is the configured converter, used for the formats it
	// accepts and fallen back from for the ones it does not.
	ConverterName string
	Timeout       time.Duration

	// Optimize enables device-targeted optimization, and Profile is its
	// target.
	Optimize bool
	Profile  convert.Profile

	// Syncable reports whether the device renders a format directly. Supplied
	// by the caller so this package does not need to know the library's
	// format vocabulary.
	Syncable func(format string) bool
}

// NewPreparer assembles the standard conversion and optimization pipeline.
func NewPreparer(c PreparerConfig) *Preparer {
	p := &Preparer{
		Convert: convert.NewCache(c.ConvertCacheDir),
		Resolve: func(src string) (*convert.Converter, error) {
			return convert.NewForFile(c.ConverterName, src, c.Timeout)
		},
		Syncable: c.Syncable,
	}
	if c.Optimize {
		p.Optimize = convert.NewOptimizeCache(c.OptimizedDir)
		p.Profile = c.Profile
	}
	return p
}

// Prepared is the result of preparing a shelf.
type Prepared struct {
	Books []LocalBook
	// Notes describe what happened to individual books: conversions, skips,
	// and failures. A failure here is not fatal to the run — one unconvertible
	// PDF should not stop a hundred good books from syncing — so the caller
	// shows these and continues.
	Notes []string
	// Failed counts books that could not be prepared.
	Failed int
}

// Prepare converts, optimizes, and hashes each candidate.
//
// Ordering is deliberate: convert first, because the device cannot render the
// source at all, then optimize, because optimization only understands EPUB.
func (p *Preparer) Prepare(ctx context.Context, candidates []Candidate) (*Prepared, error) {
	out := &Prepared{Books: make([]LocalBook, 0, len(candidates))}

	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		book, note, err := p.prepareOne(ctx, c)
		if note != "" {
			out.Notes = append(out.Notes, note)
		}
		if err != nil {
			out.Failed++
			out.Notes = append(out.Notes, fmt.Sprintf("%s: %v", c.Path, err))
			continue
		}
		if book == nil {
			continue // skipped, already noted
		}
		out.Books = append(out.Books, *book)
	}
	return out, nil
}

// prepareOne resolves a single candidate. A nil book with a nil error means the
// book was deliberately skipped.
func (p *Preparer) prepareOne(ctx context.Context, c Candidate) (*LocalBook, string, error) {
	format := strings.ToLower(strings.TrimPrefix(c.Format, "."))

	book := LocalBook{
		Path:   c.Path,
		SHA256: c.SHA256,
		Size:   c.Size,
	}
	var note string

	// Conversion. The source format decides, not the file's contents: the
	// index already classified it.
	if !p.syncable(format) {
		if p.Convert == nil || p.Resolve == nil {
			return nil, fmt.Sprintf("skipping %s: the device cannot render .%s and conversion is disabled",
				c.Path, format), nil
		}

		conv, err := p.Resolve(c.Path)
		if err != nil {
			return nil, "", err
		}
		artifact, entry, cached, err := p.Convert.Convert(ctx, conv, c.Path, convert.Options{})
		if err != nil {
			return nil, "", err
		}

		book.UploadPath = artifact
		note = fmt.Sprintf("converted %s with %s%s", c.Path, conv.Preset.Name, cachedSuffix(cached))

		// A conversion shelf is not confident in should be visible before it
		// reaches a device, not discovered while reading.
		if entry != nil && entry.Assessment.Quality != "" &&
			entry.Assessment.Quality != convert.QualityGood {
			note += fmt.Sprintf("\n  %s: %s",
				strings.ToUpper(string(entry.Assessment.Quality)),
				strings.Join(entry.Assessment.Reasons, "; "))
		}
	}

	// Optimization rewrites images inside an EPUB archive, so it applies only
	// where the bytes actually are one: anything converted is an EPUB by
	// definition, and anything else only if its source format says so. Without
	// this check a TXT — which the firmware renders natively — fails to open
	// as a zip and silently never reaches the device.
	if p.Optimize != nil && (book.UploadPath != "" || format == "epub") {
		src := book.UploadPath
		if src == "" {
			src = book.Path
		}

		artifact, entry, cached, err := p.Optimize.Optimize(src, p.Profile)
		if err != nil {
			return nil, "", err
		}
		book.UploadPath = artifact
		book.Optimized = true
		book.OptimizeProfile = p.Profile.Name

		if entry != nil && entry.OutputSize < entry.SourceSize {
			note = appendNote(note, fmt.Sprintf("optimized %s for %s: %d -> %d bytes%s",
				c.Path, p.Profile.Name, entry.SourceSize, entry.OutputSize, cachedSuffix(cached)))
		}
	}

	// The hash and size must describe the bytes actually sent. Recomputing
	// them from the artifact rather than trusting the index is the whole point
	// of this step.
	if book.UploadPath != "" {
		sum, err := convert.HashFile(book.UploadPath)
		if err != nil {
			return nil, "", err
		}
		size, err := fileSize(book.UploadPath)
		if err != nil {
			return nil, "", err
		}
		book.SHA256, book.Size = sum, size
	}

	book.RelPath = c.RelPath + destExt(format, book.UploadPath != "")
	return &book, note, nil
}

// syncable reports whether the device renders a format directly.
func (p *Preparer) syncable(format string) bool {
	if p.Syncable == nil {
		return false
	}
	return p.Syncable(format)
}

// destExt returns the destination's extension. Anything converted lands as an
// EPUB regardless of what it started as, so the device path must say so.
func destExt(format string, converted bool) string {
	if converted {
		return ".epub"
	}
	return "." + format
}

func cachedSuffix(cached bool) string {
	if cached {
		return " (cached)"
	}
	return ""
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "\n  " + add
}

func fileSize(name string) (int64, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
