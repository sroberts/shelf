package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/sroberts/decant"
)

// The built-in PDF converter.
//
// decant reconstructs semantic, reflowable EPUB 3 from a text-layer PDF. It is
// pure Go with no cgo, so compiling it in costs shelf nothing it cares about:
// the binary stays static and still cross-compiles, and a PDF now converts on
// a machine with no Calibre, no Java, and nothing on PATH.
//
// It is imported as a library rather than shelled out to because it publishes
// one, and because the library reports structured failures — encrypted,
// scanned, malformed — that a subprocess could only express as an exit code.
// A scanned PDF converts to garbage rather than failing, so being told which
// of those happened is the difference between a useful error and a bad book.

// decantModule is the module path used to read the compiled-in version.
const decantModule = "github.com/sroberts/decant"

// decantPinnedVersion is the version go.mod requires.
//
// It exists because a `go test` binary's build info records only the main
// module and no dependency list, so the runtime lookup below finds nothing
// there. Without a fallback the cache key would silently lose its version
// component in exactly the builds that are supposed to be checking it.
//
// TestDecantPinnedVersionMatchesGoMod keeps this honest; a bump to go.mod that
// forgets this constant fails the build rather than quietly serving artifacts
// the previous version produced.
const decantPinnedVersion = "v1.1.1"

// decantVersion reports the decant version this binary was built against.
//
// It is folded into the cache key: a decant upgrade changes the output for the
// same input, and serving a cached artifact the old code produced would be
// wrong. Build info is preferred over the constant because it stays correct
// under a `replace` directive or a `go get -u` that outruns the constant.
func decantVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return decantPinnedVersion
	}
	for _, dep := range info.Deps {
		if dep.Path == decantModule {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			if dep.Version != "" {
				return dep.Version
			}
		}
	}
	return decantPinnedVersion
}

// runBuiltin dispatches to the compiled-in converter named by the preset.
//
// It mirrors runCommand's contract so Convert's flow is identical either way:
// return a diagnostic string and an error, and leave the output at dst.
func runBuiltin(ctx context.Context, p Preset, src, dst string) (string, error) {
	switch p.Name {
	case "decant":
		return runDecant(ctx, src, dst)
	}
	return "", fmt.Errorf("%w: no built-in converter named %q", ErrUnsupported, p.Name)
}

// runDecant converts a PDF to EPUB in process.
func runDecant(ctx context.Context, src, dst string) (string, error) {
	opts := decant.DefaultOptions()
	// The crosspoint profile's numbers come from reading the CrossPoint
	// firmware: a 480x800 panel on an ESP32-C3 with roughly 380 KB of usable
	// RAM. That is the device shelf syncs to, so it is the right target even
	// when the book is destined for the library rather than straight to a
	// device — the optimizer can only remove detail, never restore it.
	opts.Profile = decant.ProfileCrossPoint
	opts.ApplyProfileDefaults()

	conv, err := decant.New(opts)
	if err != nil {
		return "", fmt.Errorf("convert: configure decant: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("convert: open %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return "", fmt.Errorf("convert: stat %s: %w", src, err)
	}

	// Convert writes to the temporary path Convert already created, which is
	// renamed into place only on success.
	out, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("convert: create %s: %w", dst, err)
	}

	rep, convErr := conv.Convert(ctx, in, info.Size(), out)
	if cerr := out.Close(); cerr != nil && convErr == nil {
		return "", fmt.Errorf("convert: close %s: %w", dst, cerr)
	}
	if convErr != nil {
		return "", decantError(src, convErr)
	}

	return decantSummary(rep), nil
}

// decantError maps decant's typed failures onto shelf's.
//
// Matching on types rather than message text is the same rule device/errors.go
// follows: a library is free to reword an error, and a call site that greps for
// a phrase breaks silently when it does.
func decantError(src string, err error) error {
	base := filepath.Base(src)

	var enc *decant.EncryptedError
	if errors.As(err, &enc) {
		return fmt.Errorf("%w: %s uses the %s security handler (revision %d); "+
			"decrypt it first", ErrEncryptedPDF, base, enc.Handler, enc.Revision)
	}

	var scanned *decant.NoTextLayerError
	if errors.As(err, &scanned) {
		return fmt.Errorf("%w: %s averages %.0f glyphs across %d sampled pages; "+
			"run OCR (for example ocrmypdf) and convert the result",
			ErrNoTextLayer, base, scanned.MedianGlyphs, scanned.SampledPages)
	}

	var malformed *decant.MalformedError
	if errors.As(err, &malformed) {
		return fmt.Errorf("%w: %s: %v", ErrMalformedPDF, base, err)
	}

	// A usage error means shelf built an option set decant rejected, which is
	// a shelf bug rather than a bad book. Say so instead of blaming the file.
	var usage *decant.UsageError
	if errors.As(err, &usage) {
		return fmt.Errorf("convert: shelf configured decant incorrectly: %w", err)
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrConversionFailed, base, err)
}

// decantSummary renders the parts of a Report worth showing a user.
//
// It goes where a subprocess's stderr would, so the reporting path is the same
// for both kinds of converter. Only losses and surprises are listed: a clean
// conversion should be quiet.
func decantSummary(r *decant.Report) string {
	if r == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d page(s) -> %d chapter(s)", r.PagesConverted, r.Chapters)
	if r.ImagesPlaced > 0 {
		fmt.Fprintf(&b, ", %d image(s)", r.ImagesPlaced)
	}
	if r.MultiColumnPages > 0 {
		fmt.Fprintf(&b, ", %d multi-column page(s)", r.MultiColumnPages)
	}
	// Vector artwork is dropped rather than rasterized, so a chart can vanish
	// with no other trace. Report it: silent loss is the worst outcome.
	if r.VectorPagesDropped > 0 {
		fmt.Fprintf(&b, "\nwarning: dropped vector artwork on %d page(s) (%d painted path(s)); "+
			"charts and diagrams drawn as paths will be missing",
			r.VectorPagesDropped, r.VectorPaintsDropped)
	}
	if r.RTLLetterRatio > 0.1 {
		fmt.Fprintf(&b, "\nwarning: %.0f%% of letters are right-to-left; "+
			"reading order may be wrong", r.RTLLetterRatio*100)
	}
	if r.VerticalTextPages > 0 {
		fmt.Fprintf(&b, "\nwarning: %d page(s) use vertical writing mode", r.VerticalTextPages)
	}
	return b.String()
}
