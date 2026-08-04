package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests register a built-in converter with controllable behaviour rather than
// using the real one, so Convert's contract can be exercised without a real PDF
// and without depending on anything outside this binary. The real converter is
// covered in decant_test.go.

// fakeConverter registers a built-in with the given behaviour and returns a
// Converter bound to it.
func fakeConverter(t *testing.T, name string,
	run func(ctx context.Context, src, dst string) (string, error), formats ...string) *Converter {
	t.Helper()
	if len(formats) == 0 {
		formats = []string{"pdf", "txt"}
	}
	registerBuiltin(t, name, run)
	return &Converter{
		Preset:  Preset{Name: name, Formats: formats},
		Timeout: 30 * time.Second,
	}
}

// writesEPUB returns a converter body that emits a fixed, readable EPUB.
func writesEPUB(t *testing.T, dir string) func(context.Context, string, string) (string, error) {
	t.Helper()
	built := filepath.Join(dir, "built.epub")
	minimalEPUB(t, built, []string{strings.Repeat("Call me Ishmael. ", 100)}, 0, 0)

	return func(_ context.Context, _, dst string) (string, error) {
		data, err := os.ReadFile(built)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(dst, data, 0o644)
	}
}

func testConverter(p Preset, timeout time.Duration) *Converter {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Converter{Preset: p, Timeout: timeout}
}

// writeSource creates a dummy input file.
func writeSource(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("source content"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// minimalEPUB builds a valid EPUB with the given content documents and images.
func minimalEPUB(t *testing.T, path string, docs []string, images int, imageBytes int) {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mt := []byte("application/epub+zip")
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "mimetype", Method: zip.Store, CRC32: crc32.ChecksumIEEE(mt),
		CompressedSize64: uint64(len(mt)), UncompressedSize64: uint64(len(mt)),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(mt)

	add := func(name string, body []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}

	add("META-INF/container.xml", []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
<rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`))
	add("OEBPS/content.opf", []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata/><manifest/><spine/></package>`))

	for i, text := range docs {
		add(fmt.Sprintf("OEBPS/ch%02d.xhtml", i+1),
			[]byte("<html><body><p>"+text+"</p></body></html>"))
	}
	for i := 0; i < images; i++ {
		add(fmt.Sprintf("OEBPS/images/img%02d.jpg", i+1), bytes.Repeat([]byte{0xFF}, imageBytes))
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConvertSuccess(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")
	dst := filepath.Join(dir, "out.epub")

	conv := fakeConverter(t, "ok", writesEPUB(t, dir), "pdf")

	res, err := conv.Convert(context.Background(), src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != dst {
		t.Errorf("Output = %q, want %q", res.Output, dst)
	}
	if res.OutputSize == 0 {
		t.Error("OutputSize is zero")
	}
	if res.Assessment.Quality != QualityGood {
		t.Errorf("Quality = %q (%v), want good", res.Assessment.Quality, res.Assessment.Reasons)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("output not placed: %v", err)
	}
}

func TestConvertEmptyOutputIsFailure(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	conv := fakeConverter(t, "writes-nothing",
		func(context.Context, string, string) (string, error) { return "", nil }, "pdf")

	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrEmptyOutput) {
		t.Errorf("err = %v, want ErrEmptyOutput", err)
	}
}

// The converter's own explanation is the only useful diagnostic a user gets,
// so it must reach the caller intact rather than being flattened into a
// generic "conversion failed".
func TestConverterErrorReachesTheCaller(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	conv := fakeConverter(t, "explains-itself",
		func(context.Context, string, string) (string, error) {
			return "", fmt.Errorf("%w: no text layer on page 42", ErrNoTextLayer)
		}, "pdf")

	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrNoTextLayer) {
		t.Fatalf("err = %v, want it to wrap ErrNoTextLayer", err)
	}
	if !strings.Contains(err.Error(), "page 42") {
		t.Errorf("error lost the converter's detail: %v", err)
	}
}

func TestFailedConversionLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")
	dst := filepath.Join(dir, "out.epub")

	conv := fakeConverter(t, "writes-then-fails",
		func(_ context.Context, _, dst string) (string, error) {
			os.WriteFile(dst, []byte("half a file"), 0o644)
			return "", errors.New("gave up partway")
		}, "pdf")

	if _, err := conv.Convert(context.Background(), src, dst, Options{}); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a partial output survived a failed conversion")
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shelf-convert-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestConvertTimeout(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	conv := fakeConverter(t, "hangs", func(ctx context.Context, _, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, "pdf")
	conv.Timeout = 300 * time.Millisecond

	start := time.Now()
	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v; the timeout did not fire promptly", elapsed)
	}
}

func TestConvertRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	ctx, cancel := context.WithCancel(context.Background())
	conv := fakeConverter(t, "slow", func(ctx context.Context, _, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, "pdf")

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := conv.Convert(ctx, src, filepath.Join(dir, "out.epub"), Options{}); err == nil {
		t.Fatal("expected an error on cancellation")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancellation took %v", time.Since(start))
	}
}

func TestConversionNeedsNothingOnPATH(t *testing.T) {
	// The reason the old shell-injection test is gone: there is no shell, no
	// argv, and no PATH lookup left to attack. Emptying PATH proves it — a
	// conversion that still succeeds cannot have executed anything.
	t.Setenv("PATH", "")

	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")
	dst := filepath.Join(dir, "out.epub")

	conv := fakeConverter(t, "no-path", writesEPUB(t, dir), "pdf")

	if _, err := conv.Convert(context.Background(), src, dst, Options{}); err != nil {
		t.Fatalf("conversion needed something on PATH: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("no output: %v", err)
	}
}

// A filename full of shell metacharacters is just a filename now. Kept as a
// regression guard in case a subprocess ever comes back.
func TestHostileFilenamesAreHarmless(t *testing.T) {
	dir := t.TempDir()

	canary := filepath.Join(dir, "pwned")
	nasty := filepath.Join(dir, `a; touch `+canary+`; echo .pdf`)
	if err := os.WriteFile(nasty, []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejected the hostile filename: %v", err)
	}

	conv := fakeConverter(t, "hostile", writesEPUB(t, dir), "pdf")
	if _, err := conv.Convert(context.Background(), nasty, filepath.Join(dir, "out.epub"), Options{}); err != nil {
		t.Logf("conversion error (acceptable): %v", err)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("a filename was interpreted by a shell")
	}
}

func TestSupports(t *testing.T) {
	conv := testConverter(Presets[DefaultPreset], 0)

	for _, ext := range []string{".pdf", "pdf", ".PDF"} {
		if !conv.Supports(ext) {
			t.Errorf("Supports(%q) = false", ext)
		}
	}
	// Formats the device renders itself are deliberately not supported: there
	// is nothing to convert.
	for _, ext := range []string{".epub", ".txt", ".xyz", ""} {
		if conv.Supports(ext) {
			t.Errorf("Supports(%q) = true", ext)
		}
	}
}

func TestConvertRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.xyz")

	conv := fakeConverter(t, "pdf-only",
		func(context.Context, string, string) (string, error) { return "", nil }, "pdf")
	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestNewRejectsUnknownPreset(t *testing.T) {
	_, err := New("no-such-converter", 0)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
	// The message should list what is available.
	if !strings.Contains(err.Error(), DefaultPreset) {
		t.Errorf("error should list known converters: %v", err)
	}
}

// --- assessment ---

func TestAssessClassification(t *testing.T) {
	prose := strings.Repeat("Call me Ishmael and so on. ", 60) // well over the floor

	tests := []struct {
		name       string
		docs       []string
		images     int
		imageBytes int
		want       Quality
		reason     string
	}{
		{
			name: "text-only book",
			docs: []string{prose, prose},
			want: QualityGood,
		},
		{
			name: "illustrated book with real prose",
			docs: []string{prose, prose}, images: 2, imageBytes: 512,
			want: QualityGood,
		},
		{
			// The signature of a scanned PDF run through a converter: pages of
			// images, essentially no words.
			name: "scanned pages, no text layer",
			docs: []string{"1", "2", "3"}, images: 3, imageBytes: 4096,
			want:   QualityPoor,
			reason: "scanned",
		},
		{
			name: "no content documents at all",
			docs: nil, images: 2, imageBytes: 1024,
			want: QualityPoor,
		},
		{
			name: "text but far too little of it",
			docs: []string{"a few words only"},
			want: QualityPoor,
		},
		{
			name:       "image-heavy but readable",
			docs:       []string{prose},
			images:     3,
			imageBytes: 200_000,
			want:       QualityQuestionable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "out.epub")
			minimalEPUB(t, p, tt.docs, tt.images, tt.imageBytes)

			a := Assess(p)
			if a.Quality != tt.want {
				t.Errorf("Quality = %q, want %q (reasons: %v, chars=%d images=%d)",
					a.Quality, tt.want, a.Reasons, a.TextChars, a.Images)
			}
			if tt.want != QualityGood && len(a.Reasons) == 0 {
				t.Error("a non-good verdict must explain itself")
			}
			if tt.reason != "" && !strings.Contains(strings.Join(a.Reasons, " "), tt.reason) {
				t.Errorf("reasons %v should mention %q", a.Reasons, tt.reason)
			}
		})
	}
}

// Assessment is advisory: a broken file must not turn a successful conversion
// into a failure.
func TestAssessNeverFails(t *testing.T) {
	dir := t.TempDir()

	notZip := filepath.Join(dir, "bad.epub")
	os.WriteFile(notZip, []byte("this is not a zip"), 0o644)

	a := Assess(notZip)
	if a.Quality != QualityUnknown {
		t.Errorf("Quality = %q, want unknown", a.Quality)
	}
	if len(a.Reasons) == 0 {
		t.Error("unknown should say why")
	}

	if a := Assess(filepath.Join(dir, "missing.epub")); a.Quality != QualityUnknown {
		t.Errorf("missing file: Quality = %q", a.Quality)
	}
}

func TestCountVisibleText(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"<p>abc</p>", 3},
		{"<p>a b c</p>", 3}, // whitespace not counted
		{"<script>var x = 12345;</script><p>ab</p>", 2}, // script contents skipped
		{"<style>body{color:red}</style><p>abc</p>", 3}, // style contents skipped
		{"<p></p>", 0},
		{"plain text", 9},
	}
	for _, tt := range tests {
		if got := countVisibleText(tt.in); got != tt.want {
			t.Errorf("countVisibleText(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// The navigation document is structure, not prose; counting it would inflate
// an otherwise empty book past the threshold.
func TestNavigationDocumentsAreNotCountedAsText(t *testing.T) {
	if isContentDocument("oebps/nav.xhtml") {
		t.Error("nav.xhtml should not count as content")
	}
	if isContentDocument("oebps/toc.xhtml") {
		t.Error("toc.xhtml should not count as content")
	}
	if !isContentDocument("oebps/ch01.xhtml") {
		t.Error("a chapter should count as content")
	}
	if isContentDocument("oebps/content.opf") {
		t.Error("the package document is not content")
	}
}
