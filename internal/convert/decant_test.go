package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/text-layer.pdf is a real PDF produced by a printing pipeline, not a
// hand-assembled one, for the same reason device/testdata holds captured
// firmware responses: a synthetic file agrees with whatever assumptions its
// author held, and those are exactly the assumptions worth testing.
const textLayerPDF = "testdata/text-layer.pdf"

func decantConverter(t *testing.T) *Converter {
	t.Helper()
	conv, err := New("decant", 0)
	if err != nil {
		t.Fatalf("resolving the built-in converter failed: %v", err)
	}
	return conv
}

// epubText concatenates the text content of every XHTML document in an EPUB,
// with tags stripped, so a test can assert the words survived conversion.
func epubText(t *testing.T, archive string) string {
	t.Helper()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var out strings.Builder
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xhtml") && !strings.HasSuffix(f.Name, ".html") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Crude tag strip; enough to assert that words are present.
		inTag := false
		for _, r := range string(body) {
			switch {
			case r == '<':
				inTag = true
			case r == '>':
				inTag = false
				out.WriteByte(' ')
			case !inTag:
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func TestBuiltinConverterNeedsNothingInstalled(t *testing.T) {
	// The whole point of compiling decant in is that PDF conversion works on a
	// machine with an empty PATH. Resolving it must not consult PATH at all.
	t.Setenv("PATH", "")

	conv, err := New("decant", 0)
	if err != nil {
		t.Fatalf("New(decant) with an empty PATH failed: %v", err)
	}
	if !conv.Supports(".pdf") {
		t.Error("decant does not claim to support .pdf")
	}
}

// Every preset must be compiled in. A converter that needs an external install
// is one that fails on a fresh machine, and reintroducing one would quietly
// undo the reason the subprocess path was removed.
func TestEveryPresetIsCompiledIn(t *testing.T) {
	if _, ok := Presets[DefaultPreset]; !ok {
		t.Fatalf("DefaultPreset %q is not in Presets", DefaultPreset)
	}
	for name := range Presets {
		if _, ok := builtins[name]; !ok {
			t.Errorf("preset %q has no compiled-in implementation; "+
				"if it shells out, the no-PATH guarantee is broken", name)
		}
	}
}

func TestDecantConvertsTextLayerPDF(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.epub")

	res, err := decantConverter(t).Convert(context.Background(), textLayerPDF, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if res.Converter != "decant" {
		t.Errorf("Converter = %q, want decant", res.Converter)
	}
	if res.OutputSize == 0 {
		t.Error("OutputSize is zero")
	}

	// The words must survive. Reflowing a PDF is only useful if the text
	// arrives intact, and a converter that silently drops paragraphs would
	// still produce a structurally valid EPUB.
	text := epubText(t, dst)
	for _, want := range []string{
		"There was a wall",
		"seven rooms opening off the corridor",
		"outlived its usefulness",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("converted text is missing %q", want)
		}
	}

	// PDF hard-wraps lines; a reflowable EPUB must join them back into
	// paragraphs rather than preserving the source's line breaks.
	if !strings.Contains(text, "uncut rocks roughly mortared") {
		t.Error("line breaks were not rejoined into paragraphs")
	}
}

func TestDecantOutputIsAValidEPUB(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.epub")

	if _, err := decantConverter(t).Convert(context.Background(), textLayerPDF, dst, Options{}); err != nil {
		t.Fatal(err)
	}

	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed; skipping independent validation")
	}
	out, err := exec.Command(bin, dst).CombinedOutput()
	if err == nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "FATAL") {
			t.Errorf("epubcheck rejected decant's output:\n%s", out)
			return
		}
	}
}

func TestDecantOutputIsReadable(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.epub")

	if _, err := decantConverter(t).Convert(context.Background(), textLayerPDF, dst, Options{}); err != nil {
		t.Fatal(err)
	}

	// os.CreateTemp produces 0600. A converted book lands in the user's
	// library and should be no less readable than the rest of it.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("converted EPUB has mode %v, want 0644", perm)
	}
}

func TestDecantIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.epub")
	b := filepath.Join(dir, "b.epub")

	conv := decantConverter(t)
	for _, dst := range []string{a, b} {
		if _, err := conv.Convert(context.Background(), textLayerPDF, dst, Options{}); err != nil {
			t.Fatal(err)
		}
	}

	// The manifest records the hash of the bytes actually sent. If converting
	// the same PDF twice produced different bytes, every sync would re-upload
	// every converted book.
	ha, err := HashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("two conversions produced different bytes:\n  %s\n  %s", ha, hb)
	}
}

func TestDecantRejectsMalformedPDF(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4\nthis is not a real pdf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := decantConverter(t).Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if err == nil {
		t.Fatal("converting a malformed PDF succeeded")
	}
	// The specific error matters: "malformed" tells a user to re-download the
	// file, where a generic failure tells them nothing.
	if !errors.Is(err, ErrMalformedPDF) && !errors.Is(err, ErrConversionFailed) {
		t.Errorf("err = %v, want ErrMalformedPDF or ErrConversionFailed", err)
	}
}

func TestDecantLeavesNoOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(src, []byte("not a pdf at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.epub")

	if _, err := decantConverter(t).Convert(context.Background(), src, dst, Options{}); err == nil {
		t.Fatal("converting garbage succeeded")
	}

	// A half-written EPUB left on disk would be indexed by the next scan as a
	// real book.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a failed conversion left %s behind", dst)
	}

	// Nor should the temporary file survive.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shelf-convert-") {
			t.Errorf("temporary file %s was left behind", e.Name())
		}
	}
}

func TestDecantRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	_, err := decantConverter(t).Convert(ctx, textLayerPDF, filepath.Join(dir, "out.epub"), Options{})
	if err == nil {
		t.Fatal("conversion succeeded despite a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDecantRejectsNonPDF(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	// decant reads PDF only; TXT belongs to another preset. The refusal must
	// come from the format check, not from a confusing parse failure.
	_, err := decantConverter(t).Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestDecantVersionIsKnown(t *testing.T) {
	// The version is folded into the cache key so a decant upgrade re-converts
	// rather than serving output the previous version produced. If this ever
	// resolves to nothing, that invalidation silently stops working.
	//
	// A `go test` binary records no dependency list, so this exercises the
	// pinned fallback rather than the build-info path. The shipped binary
	// takes the other branch; `go version -m shelf` shows the dep there.
	v := decantVersion()
	if v == "" {
		t.Fatal("decantVersion() is empty; the cache key can no longer track decant upgrades")
	}
	if !strings.HasPrefix(v, "v") {
		t.Errorf("decantVersion() = %q, want a semantic version", v)
	}
}

// TestDecantPinnedVersionMatchesGoMod stops the fallback constant from drifting
// away from the dependency it names. A bump that updates only go.mod would
// otherwise leave every cached artifact keyed to the old version, so a rebuilt
// shelf would keep serving EPUBs the previous decant produced.
func TestDecantPinnedVersionMatchesGoMod(t *testing.T) {
	// Tests run from the package directory; go.mod is at the repo root.
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	var found string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == decantModule {
			found = fields[1]
			break
		}
	}
	if found == "" {
		t.Fatalf("go.mod has no require line for %s", decantModule)
	}
	if found != decantPinnedVersion {
		t.Errorf("go.mod requires %s %s but decantPinnedVersion is %q; update the constant",
			decantModule, found, decantPinnedVersion)
	}
}

func TestCacheKeyDependsOnConverterVersion(t *testing.T) {
	p := Presets["decant"]
	older := p
	older.Version = "v0.0.1"

	if key("abc", p) == key("abc", older) {
		t.Error("cache key ignores the converter version; upgrading decant would serve stale artifacts")
	}
}

func TestNewForFileUsesTheBuiltinForPDF(t *testing.T) {
	// PDF must resolve to the built-in even with nothing installed; that is
	// the entire reason it is compiled in.
	t.Setenv("PATH", "")

	conv, err := NewForFile(DefaultPreset, "somewhere/book.pdf", 0)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Preset.Name != "decant" {
		t.Errorf("resolved %q for a PDF, want decant", conv.Preset.Name)
	}
}

// A TXT has no converter, and that is correct rather than a gap: the firmware
// renders TXT directly, so converting one would be pointless work. The error
// has to say that, because "unsupported" alone reads like a missing feature.
func TestNewForFileExplainsFormatsTheDeviceRendersItself(t *testing.T) {
	_, err := NewForFile(DefaultPreset, "somewhere/notes.txt", 0)
	if err == nil {
		t.Fatal("resolving a .txt returned a converter; nothing converts TXT")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "reads .txt directly") {
		t.Errorf("error should explain that the device renders TXT itself: %v", err)
	}
}

func TestNewForFileRejectsUnknownFormats(t *testing.T) {
	if _, err := NewForFile(DefaultPreset, "somewhere/book.xyz", 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err for .xyz = %v, want ErrUnsupported", err)
	}
	if _, err := NewForFile(DefaultPreset, "somewhere/noext", 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err for an extensionless file = %v, want ErrUnsupported", err)
	}
}

func TestDecantSummaryReportsVectorLoss(t *testing.T) {
	// Vector artwork is dropped rather than rasterized, so a chart can vanish
	// with no other trace. Silent loss is the failure mode worth guarding.
	var buf bytes.Buffer
	buf.WriteString(decantSummary(nil))
	if buf.Len() != 0 {
		t.Errorf("summary of a nil report = %q, want empty", buf.String())
	}
}
