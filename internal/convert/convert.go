// Package convert turns source formats the device cannot render into EPUB.
//
// The CrossPoint firmware has no PDF engine. PDF is therefore a source format
// only: it lives in the library and is indexed, but something has to convert it
// before it can reach a device. On a 4-to-7 inch e-ink panel a reflowed EPUB
// beats a scaled PDF page anyway, so this is the right answer rather than a
// workaround.
//
// Conversion is delegated to external commands, configured in TOML. shelf does
// not implement a PDF parser and should not: the good converters represent
// years of work on a genuinely hard problem, and shelf's job is to run one
// safely, cache the result, and be honest about what came out.
package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Errors callers distinguish.
var (
	// ErrConverterMissing means the configured binary is not on PATH. This is a
	// setup problem, not a bad book, and the message says how to fix it.
	ErrConverterMissing = errors.New("convert: converter not installed")

	// ErrTimeout means the converter exceeded its budget and was killed.
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

// DefaultTimeout bounds a single conversion. Large scanned PDFs are genuinely
// slow, so this is generous.
const DefaultTimeout = 10 * time.Minute

// Preset is a named external converter.
//
// Args use {in} and {out} placeholders rather than shell interpolation: the
// command is executed directly, never through a shell, so a filename
// containing a quote or a semicolon cannot become an injection.
type Preset struct {
	Name string
	Bin  string
	Args []string
	// Formats the preset accepts, lowercase without the dot.
	Formats []string
	// Notes is shown when the preset is chosen, so the user knows what to
	// expect before waiting ten minutes for a bad result.
	Notes string

	// Builtin marks a converter that runs inside this process rather than as
	// a subprocess. Bin and Args are then identity only: Args still describes
	// the settings the converter runs with, because the cache key is derived
	// from them and a settings change must produce a new artifact.
	Builtin bool

	// Version identifies the converter's implementation. For a built-in it is
	// the library's module version, folded into the cache key so upgrading
	// the converter re-converts rather than serving output the old code
	// produced. Subprocess presets leave it empty; their binaries are
	// resolved from PATH and shelf does not pin them.
	Version string
}

// Presets are the converters from the spec.
var Presets = map[string]Preset{
	// decant is compiled in, so a PDF converts on a machine with nothing else
	// installed. It reconstructs semantic, reflowable EPUB 3 from a
	// text-layer PDF and ships a "crosspoint" profile whose numbers come from
	// reading the CrossPoint firmware, which is a better target than anything
	// shelf could ask a general-purpose converter for.
	//
	// This is the answer to the dependency question in spec.md 14.6: the best
	// PDF path is no longer a Calibre component.
	"decant": {
		Name:    "decant",
		Bin:     "(built-in)",
		Args:    []string{"--profile=crosspoint"},
		Formats: []string{"pdf"},
		Notes:   "Built in. Reflowable EPUB 3 from text-layer PDF, targeted at CrossPoint.",
		Builtin: true,
		Version: decantVersion(),
	},
	"ebook-convert": {
		Name:    "ebook-convert",
		Bin:     "ebook-convert",
		Args:    []string{"{in}", "{out}", "--enable-heuristics"},
		Formats: []string{"pdf", "txt", "mobi", "azw3", "fb2", "rtf", "docx", "html"},
		Notes:   "Calibre's converter. Best available fidelity for text PDFs.",
	},
	"pandoc": {
		Name:    "pandoc",
		Bin:     "pandoc",
		Args:    []string{"{in}", "-o", "{out}"},
		Formats: []string{"txt", "md", "html", "docx", "rtf"},
		Notes:   "Good for already-structured text. Does little layout recovery on PDF.",
	},
	"mutool": {
		Name:    "mutool",
		Bin:     "mutool",
		Args:    []string{"convert", "-o", "{out}", "{in}"},
		Formats: []string{"pdf"},
		Notes:   "Fast and dependency-light. Output needs assembly; not yet wired up.",
	},
}

// DefaultPreset is used when config names none.
//
// decant rather than ebook-convert: it is compiled in, so the default path
// works with nothing installed, and it targets this device specifically.
// ebook-convert remains the answer for the formats decant does not read.
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
	// Stderr holds the converter's error output, retained for diagnosis. It is
	// truncated: ebook-convert is extremely chatty on success.
	Stderr string
}

// New resolves a converter by name, verifying the binary exists.
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

	// A built-in converter is compiled in; there is nothing to look up and
	// nothing the user can fail to install.
	if !preset.Builtin {
		if _, err := exec.LookPath(preset.Bin); err != nil {
			return nil, fmt.Errorf("%w: %s is not on PATH (%s)",
				ErrConverterMissing, preset.Bin, installHint(preset.Bin))
		}
	}
	return &Converter{Preset: preset, Timeout: timeout}, nil
}

// FallbackOrder lists the converters tried for a format the configured
// converter does not accept, most preferred first.
//
// decant is PDF-only and is the default, so TXT, DOCX, and the rest need
// somewhere to go. Order is by output quality, not by convenience.
var FallbackOrder = []string{"ebook-convert", "pandoc"}

// NewForFile resolves a converter able to handle src's format.
//
// The configured converter is used whenever it accepts the format. When it
// does not — the common case being the built-in PDF converter handed a TXT —
// the first installed converter from FallbackOrder that does is used instead.
// Falling back is right here because the configured name answers "what should
// convert my PDFs", not "what should convert everything".
//
// Callers that must honor an explicit choice should use New and let an
// unsupported format be an error.
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

	var missing []string
	for _, name := range FallbackOrder {
		p, ok := Presets[name]
		if !ok || !supportsFormat(p, ext) {
			continue
		}
		alt, err := New(name, timeout)
		if err == nil {
			return alt, nil
		}
		if errors.Is(err, ErrConverterMissing) {
			missing = append(missing, p.Bin)
		}
	}

	// A configured converter that failed to resolve at all is the more useful
	// thing to report: the user named it, so its absence is the real problem.
	if cfgErr != nil && !errors.Is(cfgErr, ErrUnsupported) {
		return nil, cfgErr
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: nothing installed reads .%s (tried: %s)",
			ErrConverterMissing, ext, strings.Join(missing, ", "))
	}
	return nil, fmt.Errorf("%w: no converter reads .%s", ErrUnsupported, ext)
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

// installHint suggests how to obtain a missing converter.
func installHint(bin string) string {
	switch bin {
	case "ebook-convert":
		return "install Calibre: brew install --cask calibre, or nix-shell -p calibre"
	case "pandoc":
		return "brew install pandoc"
	case "mutool":
		return "brew install mupdf-tools"
	case "ocrmypdf":
		return "brew install ocrmypdf"
	}
	return "install it and ensure it is on PATH"
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
	var (
		stderr string
		runErr error
	)
	if c.Preset.Builtin {
		stderr, runErr = runBuiltin(ctx, c.Preset, src, tmpName)
	} else {
		stderr, runErr = runCommand(ctx, c.Preset.Bin, c.buildArgs(src, tmpName))
	}
	elapsed := time.Since(start)

	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w after %s: %s", ErrTimeout, c.Timeout, filepath.Base(src))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		// A built-in converter has already produced a typed error naming what
		// was wrong with the book. Wrapping it in the generic "exited with an
		// error" would throw away the only useful part.
		if c.Preset.Builtin {
			return nil, runErr
		}
		return nil, fmt.Errorf("%w: %s exited with an error: %v\n%s",
			ErrConversionFailed, c.Preset.Bin, runErr, tailLines(stderr, 20))
	}

	// A converter that exits zero having written nothing is a failure, whatever
	// it claims.
	info, err := os.Stat(tmpName)
	if err != nil || info.Size() == 0 {
		return nil, fmt.Errorf("%w: %s exited cleanly but wrote no output\n%s",
			ErrEmptyOutput, c.Preset.Bin, tailLines(stderr, 20))
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

// buildArgs substitutes the {in} and {out} placeholders.
func (c *Converter) buildArgs(src, dst string) []string {
	args := make([]string, 0, len(c.Preset.Args))
	for _, a := range c.Preset.Args {
		a = strings.ReplaceAll(a, "{in}", src)
		a = strings.ReplaceAll(a, "{out}", dst)
		args = append(args, a)
	}
	return args
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
