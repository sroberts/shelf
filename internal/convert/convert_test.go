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

// Tests build converters directly rather than going through New(), so the
// package can be tested without any external converter installed. The real
// binaries are exercised by the end-to-end CLI run, not here.

// shellPreset builds a preset that runs a shell snippet. {in} and {out} are
// substituted before exec, as with a real converter.
func shellPreset(name, script string, formats ...string) Preset {
	if len(formats) == 0 {
		formats = []string{"pdf", "txt"}
	}
	return Preset{
		Name:    name,
		Bin:     "/bin/sh",
		Args:    []string{"-c", script},
		Formats: formats,
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

	// A converter that produces a real EPUB with plenty of text.
	prose := strings.Repeat("Call me Ishmael. ", 100)
	built := filepath.Join(dir, "built.epub")
	minimalEPUB(t, built, []string{prose, prose}, 0, 0)

	conv := testConverter(shellPreset("fake", "cp '"+built+"' \"$2\"", "pdf"), 0)
	// The shell snippet needs the args positionally.
	conv.Preset.Args = []string{"-c", `cp "` + built + `" "$1"`, "sh", "{out}"}

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

// A converter that exits zero having written nothing is a failure, whatever it
// claims — a zero-byte EPUB on the device is worse than an error here.
func TestConvertEmptyOutputIsFailure(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	conv := testConverter(shellPreset("noop", "exit 0", "pdf"), 0)
	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrEmptyOutput) {
		t.Errorf("err = %v, want ErrEmptyOutput", err)
	}
}

func TestConvertFailureIncludesStderrTail(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	conv := testConverter(shellPreset("failing",
		`echo "something went wrong on page 42" >&2; exit 3`, "pdf"), 0)

	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("err = %v, want ErrConversionFailed", err)
	}
	// The converter's own message is the only useful diagnostic; it must survive.
	if !strings.Contains(err.Error(), "page 42") {
		t.Errorf("error lost the converter's output: %v", err)
	}
}

// A failed conversion must not leave a partial EPUB behind, or a later scan
// would index it as a real book.
func TestFailedConversionLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")
	dst := filepath.Join(dir, "out.epub")

	conv := testConverter(shellPreset("partial",
		`printf 'half a fi' > "$1"; exit 1`, "pdf"), 0)
	conv.Preset.Args = []string{"-c", `printf 'half a file' > "$1"; exit 1`, "sh", "{out}"}

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

// A hung converter must be killed, and must not outlive the call.
func TestConvertTimeoutKillsConverter(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	marker := filepath.Join(dir, "still-running")
	// Sleeps well past the timeout, then would touch a marker file. If the
	// process survives the kill, the marker appears.
	conv := testConverter(shellPreset("hang",
		`sleep 5; touch "`+marker+`"`, "pdf"), 400*time.Millisecond)

	start := time.Now()
	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("took %v; the converter was not killed promptly", elapsed)
	}

	// Give any surviving process time to write the marker.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the converter outlived the timeout; the process group was not killed")
	}
}

func TestConvertRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	conv := testConverter(shellPreset("slow", "sleep 10", "pdf"), 30*time.Second)
	start := time.Now()
	_, err := conv.Convert(ctx, src, filepath.Join(dir, "out.epub"), Options{})

	if err == nil {
		t.Fatal("expected an error on cancellation")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancellation took %v", time.Since(start))
	}
}

// Arguments are passed to exec directly, never through a shell, so a filename
// containing shell metacharacters cannot become an injection.
func TestFilenamesAreNotShellInterpreted(t *testing.T) {
	dir := t.TempDir()

	canary := filepath.Join(dir, "pwned")
	nasty := filepath.Join(dir, `a; touch `+canary+`; echo .pdf`)
	if err := os.WriteFile(nasty, []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejected the hostile filename: %v", err)
	}

	built := filepath.Join(dir, "built.epub")
	minimalEPUB(t, built, []string{strings.Repeat("text ", 200)}, 0, 0)

	conv := testConverter(Preset{
		Name: "fake", Bin: "/bin/sh",
		Args:    []string{"-c", `cp "` + built + `" "$1"`, "sh", "{out}"},
		Formats: []string{"pdf"},
	}, 0)

	// The input path is not even referenced by this converter; what matters is
	// that building the args cannot execute anything.
	_, err := conv.Convert(context.Background(), nasty, filepath.Join(dir, "out.epub"), Options{})
	if err != nil {
		t.Logf("conversion error (acceptable): %v", err)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("a filename was interpreted by a shell; command injection is possible")
	}
}

func TestSupports(t *testing.T) {
	conv := testConverter(Presets["ebook-convert"], 0)

	for _, ext := range []string{".pdf", "pdf", ".PDF", ".txt"} {
		if !conv.Supports(ext) {
			t.Errorf("Supports(%q) = false", ext)
		}
	}
	for _, ext := range []string{".epub", ".xyz", ""} {
		if conv.Supports(ext) {
			t.Errorf("Supports(%q) = true", ext)
		}
	}
}

func TestConvertRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.xyz")

	conv := testConverter(shellPreset("fake", "exit 0", "pdf"), 0)
	_, err := conv.Convert(context.Background(), src, filepath.Join(dir, "out.epub"), Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestNewReportsMissingBinaryWithInstallHint(t *testing.T) {
	Presets["test-absent"] = Preset{
		Name: "test-absent", Bin: "definitely-not-installed-xyzzy", Formats: []string{"pdf"},
	}
	defer delete(Presets, "test-absent")

	_, err := New("test-absent", 0)
	if !errors.Is(err, ErrConverterMissing) {
		t.Fatalf("err = %v, want ErrConverterMissing", err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error should say the binary is not on PATH: %v", err)
	}
}

func TestNewRejectsUnknownPreset(t *testing.T) {
	_, err := New("no-such-converter", 0)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
	// The message should list what is available.
	if !strings.Contains(err.Error(), "ebook-convert") {
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
