// Package convert turns source formats the device cannot render into EPUB.
//
// The CrossPoint firmware has no PDF engine. PDF is therefore a source format
// only: it lives in the library and is indexed, but something has to convert it
// before it can reach a device. On a 4-to-7 inch e-ink panel a reflowed EPUB
// beats a scaled PDF page anyway, so this is the right answer rather than a
// workaround.
//
// Conversion runs in-process. shelf shells out to nothing and requires nothing
// on PATH: a PDF converts on a machine with no Calibre, no Java, and no
// mupdf-tools, which is what makes the single static binary actually single.
//
// This is narrower than the spec's "pluggable external converters" and
// deliberately so. shelf indexes four formats — epub, txt, xtc, pdf — and the
// firmware renders three of them directly, so PDF is the only thing that ever
// needs converting. The external converters were carrying formats shelf cannot
// even index, in exchange for a PATH dependency, a subprocess lifetime to
// manage, and a shell-injection surface to defend. Deleting them removed all
// three and cost nothing shelf's scope actually used.
package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Errors callers distinguish.
var (
	// ErrTimeout means the converter exceeded its budget.
	ErrTimeout = errors.New("convert: converter timed out")

	// ErrConversionFailed means the converter ran and reported failure.
	ErrConversionFailed = errors.New("convert: conversion failed")

	// ErrUnsupported means no converter is configured for this input format.
	ErrUnsupported = errors.New("convert: unsupported input format")

	// ErrEmptyOutput means the converter claimed success but produced nothing
	// usable. Treated as failure: a zero-byte EPUB on the device is worse than
	// an error here.
	ErrEmptyOutput = errors.New("convert: converter produced no usable output")

	// ErrEncryptedPDF means the source carries an /Encrypt dictionary.
	ErrEncryptedPDF = errors.New("convert: PDF is encrypted")

	// ErrNoTextLayer means the source is a scanned page image with no
	// extractable text. Converting it anyway produces garbage, so shelf fails
	// and points at OCR instead.
	ErrNoTextLayer = errors.New("convert: PDF has no text layer (scanned)")

	// ErrMalformedPDF means the source could not be parsed as a PDF.
	ErrMalformedPDF = errors.New("convert: PDF is malformed")
)

// DefaultTimeout bounds a single conversion. A large PDF is genuinely slow to
// reconstruct, so this is generous.
const DefaultTimeout = 10 * time.Minute

// Preset is a named converter. Every preset is compiled in; nothing here
// executes an external program.
type Preset struct {
	Name string
	// Formats the preset accepts, lowercase without the dot.
	Formats []string
	// Notes is shown when the preset is chosen, so the user knows what to
	// expect before waiting on a large book.
	Notes string

	// Args describes the settings the converter runs with. It is identity
	// only — nothing executes it — but it feeds the cache key, so changing a
	// setting produces a new artifact rather than serving output the old
	// settings produced.
	Args []string

	// Version identifies the converter's implementation: the library's module
	// version, folded into the cache key so upgrading re-converts. This
	// matters more for a compiled-in converter than it did for a subprocess,
	// because there is no binary on PATH whose absence would otherwise hint
	// that anything moved.
	Version string
}

// Presets are the compiled-in converters.
var Presets = map[string]Preset{
	// decant is compiled in, so a PDF converts on a machine with nothing else
	// installed. It reconstructs semantic, reflowable EPUB 3 from a
	// text-layer PDF and ships a "crosspoint" profile whose numbers come from
	// reading the CrossPoint firmware, which is a better target than anything
	// shelf could ask a general-purpose converter for.
	//
	// This answers the dependency question in spec.md 14.6: the best PDF path
	// is no longer a Calibre component, and is no longer a subprocess at all.
	"decant": {
		Name:    "decant",
		Args:    []string{"--profile=crosspoint"},
		Formats: []string{"pdf"},
		Notes:   "Reflowable EPUB 3 from a text-layer PDF, targeted at CrossPoint.",
		Version: decantVersion(),
	},
}

// DefaultPreset is used when config names none. It is also the only preset:
// the name is kept so config files stay valid and a second compiled-in
// converter can be added without changing the shape of anything.
const DefaultPreset = "decant"

// Converter is a resolved, runnable converter.
type Converter struct {
	Preset  Preset
	Timeout time.Duration
}

// Options configures a conversion run.
type Options struct {
	// Preset names the converter; empty uses DefaultPreset.
	Preset string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
	// Progress, when set, receives human-readable status lines.
	Progress func(string)
}

// Result describes a completed conversion.
type Result struct {
	Source     string
	Output     string
	Converter  string
	Duration   time.Duration
	OutputSize int64
	// Assessment reports whether the output is likely readable.
	Assessment Assessment
	// Stderr holds the converter's diagnostic output, retained for failures.
	// Truncated, since a converter can be verbose on a large book.
	Stderr string
}

// New resolves a converter by name.
func New(name string, timeout time.Duration) (*Converter, error) {
	if name == "" {
		name = DefaultPreset
	}
	preset, ok := Presets[name]
	if !ok {
		return nil, fmt.Errorf("%w: unknown converter %q (have: %s)",
			ErrUnsupported, name, strings.Join(PresetNames(), ", "))
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// Every converter is compiled in: there is nothing to look up on PATH and
	// nothing the user can fail to install.
	return &Converter{Preset: preset, Timeout: timeout}, nil
}

// NewForFile resolves a converter able to handle src's format.
//
// There is no fallback chain any more, because there is nothing to fall back
// to: one converter is compiled in and it reads PDF. The value this still adds
// is the error for everything else, which has to distinguish two very different
// situations — a format the device already renders, where converting would be
// pointless, and a format shelf simply does not handle.
func NewForFile(configured, src string, timeout time.Duration) (*Converter, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(src), "."))
	if ext == "" {
		return nil, fmt.Errorf("%w: %s has no extension", ErrUnsupported, filepath.Base(src))
	}

	// The configured converter first, but only if it reads this format.
	conv, cfgErr := New(configured, timeout)
	if cfgErr == nil && conv.Supports(ext) {
		return conv, nil
	}
	// Any other compiled-in converter that does.
	for _, name := range PresetNames() {
		if supportsFormat(Presets[name], ext) {
			return New(name, timeout)
		}
	}

	// A named converter that does not exist is the user's mistake and the more
	// useful thing to report.
	if cfgErr != nil && !errors.Is(cfgErr, ErrUnsupported) {
		return nil, cfgErr
	}
	if renderedNatively(ext) {
		return nil, fmt.Errorf("%w: the device reads .%s directly, so there is nothing to convert",
			ErrUnsupported, ext)
	}
	return nil, fmt.Errorf("%w: shelf converts PDF only; .%s is not supported",
		ErrUnsupported, ext)
}

// renderedNatively reports whether the firmware displays a format as-is.
//
// Duplicated from the library's format vocabulary rather than imported, to keep
// this package free of that dependency. It exists only to turn "unsupported"
// into a useful sentence.
func renderedNatively(ext string) bool {
	switch ext {
	case "epub", "txt", "xtc", "xtch", "bmp":
		return true
	}
	return false
}

// supportsFormat reports whether a preset accepts an extension, without
// needing a resolved Converter.
func supportsFormat(p Preset, ext string) bool {
	for _, f := range p.Formats {
		if f == ext {
			return true
		}
	}
	return false
}

// PresetNames lists available converter names, sorted for stable output.
func PresetNames() []string {
	names := make([]string, 0, len(Presets))
	for n := range Presets {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

// Supports reports whether this converter accepts the given file extension.
func (c *Converter) Supports(ext string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	for _, f := range c.Preset.Formats {
		if f == ext {
			return true
		}
	}
	return false
}

// Convert produces an EPUB at dst from the source file at src.
//
// The output is written to a temporary file and renamed into place only on
// success, so an interrupted or failed conversion never leaves a half-written
// EPUB that a later scan would index as a real book.
func (c *Converter) Convert(ctx context.Context, src, dst string, opts Options) (*Result, error) {
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("convert: source %s: %w", src, err)
	}
	ext := filepath.Ext(src)
	if !c.Supports(ext) {
		return nil, fmt.Errorf("%w: %s does not accept %s",
			ErrUnsupported, c.Preset.Name, ext)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("convert: create output directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".shelf-convert-*.epub")
	if err != nil {
		return nil, fmt.Errorf("convert: create temp output: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	// The converter writes this path itself; remove it on every failure path.
	defer os.Remove(tmpName)

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("running %s on %s", c.Preset.Name, filepath.Base(src)))
	}

	start := time.Now()
	stderr, runErr := runBuiltin(ctx, c.Preset, src, tmpName)
	elapsed := time.Since(start)

	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w after %s: %s", ErrTimeout, c.Timeout, filepath.Base(src))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		// The converter has already produced a typed error naming what was
		// wrong with the book — encrypted, scanned, malformed. Wrapping it in
		// something generic would throw away the only useful part.
		return nil, runErr
	}

	// A converter that returns success having written nothing is a failure,
	// whatever it claims.
	info, err := os.Stat(tmpName)
	if err != nil || info.Size() == 0 {
		return nil, fmt.Errorf("%w: %s returned success but wrote no output\n%s",
			ErrEmptyOutput, c.Preset.Name, tailLines(stderr, 20))
	}

	assessment := Assess(tmpName)

	// os.CreateTemp made this 0600. A converted book is a book: it belongs in
	// the library at the same permissions as everything else there, not
	// readable only by the user who happened to run the conversion.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return nil, fmt.Errorf("convert: set permissions on %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, dst); err != nil {
		return nil, fmt.Errorf("convert: place output at %s: %w", dst, err)
	}

	return &Result{
		Source:     src,
		Output:     dst,
		Converter:  c.Preset.Name,
		Duration:   elapsed,
		OutputSize: info.Size(),
		Assessment: assessment,
		Stderr:     tailLines(stderr, 40),
	}, nil
}

// tailLines keeps the last n lines, which is where converters put the reason
// for a failure. Leading output is progress noise.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}

// sortStrings is a tiny helper to avoid importing sort for one call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
