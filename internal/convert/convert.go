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
}

// Presets are the converters from the spec.
var Presets = map[string]Preset{
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
const DefaultPreset = "ebook-convert"

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

	if _, err := exec.LookPath(preset.Bin); err != nil {
		return nil, fmt.Errorf("%w: %s is not on PATH (%s)",
			ErrConverterMissing, preset.Bin, installHint(preset.Bin))
	}
	return &Converter{Preset: preset, Timeout: timeout}, nil
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

	args := c.buildArgs(src, tmpName)

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("running %s on %s", c.Preset.Bin, filepath.Base(src)))
	}

	start := time.Now()
	stderr, runErr := runCommand(ctx, c.Preset.Bin, args)
	elapsed := time.Since(start)

	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w after %s: %s", ErrTimeout, c.Timeout, filepath.Base(src))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
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
