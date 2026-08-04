package convert

import (
	"archive/zip"
	"bytes"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test EPUBs are built here rather than borrowed from internal/epub, whose
// builder is unexported test code. Keep these archives valid: an invalid
// fixture makes the optimizer look broken when it is not.

const optContainerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

const optOPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Optimizer Test</dc:title>
    <dc:creator>A. Writer</dc:creator>
    <dc:identifier id="pub-id">urn:uuid:8f1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d</dc:identifier>
    <dc:language>en</dc:language>
    <meta property="dcterms:modified">2024-01-01T00:00:00Z</meta>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png" properties="cover-image"/>
    <item id="plate" href="images/plate.jpg" media-type="image/jpeg"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
  </spine>
</package>`

const optNavXHTML = `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Contents</title></head><body><nav epub:type="toc"><ol><li><a href="ch1.xhtml">Chapter 1</a></li></ol></nav></body></html>`

const optCh1XHTML = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter 1</title></head><body><p>Text.</p></body></html>`

// lcg is a deterministic pseudo-random source. Tests need high-entropy pixels —
// a smooth synthetic gradient compresses so well as RGBA that the downscaled
// grayscale version is legitimately larger, which trips the never-grow rule and
// makes the optimizer look broken when it is behaving correctly.
type lcg uint32

func (s *lcg) next() uint8 {
	*s = *s*1664525 + 1013904223
	return uint8(*s >> 24)
}

// colorPNG renders a noisy colour PNG, which grayscale conversion and
// downscaling can both measurably shrink.
func colorPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := lcg(12345)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: rng.next(),
				G: rng.next(),
				B: rng.next(),
				A: 0xff,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func colorJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type zipEntry struct {
	name string
	body []byte
}

// writeEPUB builds a spec-correct archive: mimetype first, stored, no extra.
func writeEPUB(t *testing.T, dir string, extra ...zipEntry) string {
	t.Helper()

	entries := []zipEntry{
		{"META-INF/container.xml", []byte(optContainerXML)},
		{"OEBPS/content.opf", []byte(optOPF)},
		{"OEBPS/nav.xhtml", []byte(optNavXHTML)},
		{"OEBPS/ch1.xhtml", []byte(optCh1XHTML)},
		{"OEBPS/images/cover.png", colorPNG(t, 300, 400)},
		{"OEBPS/images/plate.jpg", colorJPEG(t, 260, 360)},
	}
	entries = append(entries, extra...)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mime := []byte("application/epub+zip")
	fh := &zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mime),
		CompressedSize64:   uint64(len(mime)),
		UncompressedSize64: uint64(len(mime)),
	}
	w, err := zw.CreateRaw(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(mime); err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readEntry pulls one entry's bytes out of an EPUB.
func readEntry(t *testing.T, archive, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(rc); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}
	}
	t.Fatalf("entry %q not found in %s", name, archive)
	return nil
}

// entryNames lists the archive's members in order.
func entryNames(t *testing.T, archive string) []string {
	t.Helper()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// testProfile is a profile with known panel dimensions, so the geometry path is
// exercised even though the shipped profiles deliberately have none.
func testProfile() Profile {
	p := builtinProfiles["x4-v1"]
	return p.WithPanelSize(150, 200)
}

func TestOptimizeDownscalesToPanel(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	img, err := png.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/cover.png")))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() > 150 || b.Dy() > 200 {
		t.Errorf("cover is %dx%d, want it to fit within 150x200", b.Dx(), b.Dy())
	}
	// 300x400 into 150x200 is an exact halving; aspect ratio must survive.
	if b.Dx() != 150 || b.Dy() != 200 {
		t.Errorf("cover is %dx%d, want 150x200 (aspect ratio preserved)", b.Dx(), b.Dy())
	}
}

func TestOptimizeDoesNotEnlargeSmallImages(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir, zipEntry{"OEBPS/images/small.png", colorPNG(t, 80, 100)})
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	img, err := png.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/small.png")))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 100 {
		t.Errorf("small image became %dx%d, want it left at 80x100", b.Dx(), b.Dy())
	}
}

func TestOptimizeConvertsToGrayscale(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	img, err := png.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/cover.png")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := img.(*image.Gray); !ok {
		t.Errorf("cover decoded as %T, want *image.Gray", img)
	}
}

func TestOptimizePreservesContainerFormat(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	// The OPF declares these media-types; changing the bytes' format would
	// make the manifest lie.
	if _, err := png.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/cover.png"))); err != nil {
		t.Errorf("cover.png is no longer a PNG: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/plate.jpg"))); err != nil {
		t.Errorf("plate.jpg is no longer a JPEG: %v", err)
	}
}

func TestOptimizeLeavesNonImagesByteIdentical(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/ch1.xhtml"} {
		before := readEntry(t, src, name)
		after := readEntry(t, dst, name)
		if !bytes.Equal(before, after) {
			t.Errorf("%s changed; the optimizer must not touch non-image content", name)
		}
	}
}

func TestOptimizeKeepsMimetypeFirstAndStored(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	names := entryNames(t, dst)
	if len(names) == 0 || names[0] != "mimetype" {
		t.Fatalf("first entry is %v, want mimetype", names)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if m := zr.File[0].Method; m != zip.Store {
		t.Errorf("mimetype method = %d, want Store (%d)", m, zip.Store)
	}
	if extra := zr.File[0].Extra; len(extra) != 0 {
		t.Errorf("mimetype carries %d bytes of extra field, want none", len(extra))
	}
}

func TestOptimizeNeverGrowsAnEntry(t *testing.T) {
	dir := t.TempDir()
	// An already-grayscale, already-small PNG is the case where a generic
	// re-encode tends to produce a larger file than it was given.
	tiny := image.NewGray(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, tiny); err != nil {
		t.Fatal(err)
	}
	src := writeEPUB(t, dir, zipEntry{"OEBPS/images/tiny.png", buf.Bytes()})
	dst := filepath.Join(dir, "out.epub")

	if _, err := Optimize(src, dst, testProfile()); err != nil {
		t.Fatal(err)
	}

	before := readEntry(t, src, "OEBPS/images/tiny.png")
	after := readEntry(t, dst, "OEBPS/images/tiny.png")
	if len(after) > len(before) {
		t.Errorf("entry grew from %d to %d bytes; the optimizer must keep the original when re-encoding does not help",
			len(before), len(after))
	}
}

func TestOptimizeShrinksTheBook(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	res, err := Optimize(src, dst, testProfile())
	if err != nil {
		t.Fatal(err)
	}

	if res.Images != 2 {
		t.Errorf("Images = %d, want 2", res.Images)
	}
	if res.Rewritten == 0 {
		t.Error("no images were rewritten")
	}
	if res.OutputSize >= res.SourceSize {
		t.Errorf("output %d >= source %d; optimization saved nothing", res.OutputSize, res.SourceSize)
	}
	if res.Saved() == 0 {
		t.Error("Saved() = 0")
	}
}

func TestOptimizePassesThroughUndecodableImages(t *testing.T) {
	dir := t.TempDir()
	// A .png extension over bytes that are not a PNG. Real EPUBs contain
	// mislabelled images; one bad file must not fail the whole book.
	junk := []byte("this is definitely not a PNG")
	src := writeEPUB(t, dir, zipEntry{"OEBPS/images/broken.png", junk})
	dst := filepath.Join(dir, "out.epub")

	res, err := Optimize(src, dst, testProfile())
	if err != nil {
		t.Fatalf("one bad image failed the whole book: %v", err)
	}

	if got := readEntry(t, dst, "OEBPS/images/broken.png"); !bytes.Equal(got, junk) {
		t.Error("undecodable image was not passed through unchanged")
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped = %v, want exactly one entry", res.Skipped)
	}
	if len(res.Skipped) > 0 && !strings.Contains(res.Skipped[0], "broken.png") {
		t.Errorf("Skipped[0] = %q, want it to name the file", res.Skipped[0])
	}
}

func TestOptimizeWithoutPanelSizeSkipsDownscaling(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	dst := filepath.Join(dir, "out.epub")

	// A profile whose panel has not been read off hardware must make geometry
	// a no-op rather than guess. The X4's dimensions came from decant; the
	// X3's have not been measured, so it is the profile that still degrades.
	p := builtinProfiles["x3-v1"]
	if p.KnowsPanelSize() {
		t.Fatal("x3-v1 unexpectedly has panel dimensions; update this test")
	}

	res, err := Optimize(src, dst, p)
	if err != nil {
		t.Fatal(err)
	}

	img, err := png.Decode(bytes.NewReader(readEntry(t, dst, "OEBPS/images/cover.png")))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 400 {
		t.Errorf("cover is %dx%d, want it left at 300x400", b.Dx(), b.Dy())
	}
	// Grayscale conversion is panel-independent and must still have happened.
	if _, ok := img.(*image.Gray); !ok {
		t.Errorf("cover decoded as %T, want *image.Gray even without panel dimensions", img)
	}
	if res.Rewritten == 0 {
		t.Error("nothing was rewritten; grayscale should still apply")
	}
}

func TestOptimizeIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)

	// The manifest records the hash of the bytes actually sent, so the same
	// input and profile must produce the same output every time or sync would
	// re-upload the library on every run.
	a := filepath.Join(dir, "a.epub")
	b := filepath.Join(dir, "b.epub")
	if _, err := Optimize(src, a, testProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := Optimize(src, b, testProfile()); err != nil {
		t.Fatal(err)
	}

	ha, err := HashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("two runs produced different bytes:\n  %s\n  %s", ha, hb)
	}
}

func TestOptimizeInPlace(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Optimize(src, src, testProfile()); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Errorf("in-place optimize left the file at %d bytes, was %d", after.Size(), before.Size())
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("permissions changed from %v to %v", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestOptimizeDither(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)

	plain := filepath.Join(dir, "plain.epub")
	dithered := filepath.Join(dir, "dithered.epub")

	p := testProfile()
	if _, err := Optimize(src, plain, p); err != nil {
		t.Fatal(err)
	}
	p.Dither = true
	if _, err := Optimize(src, dithered, p); err != nil {
		t.Fatal(err)
	}

	a := readEntry(t, plain, "OEBPS/images/cover.png")
	b := readEntry(t, dithered, "OEBPS/images/cover.png")
	if bytes.Equal(a, b) {
		t.Error("dithering produced identical bytes; the option did nothing")
	}

	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// Error diffusion must land on the panel's grey ramp, not arbitrary values.
	gray, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("dithered image is %T, want *image.Gray", img)
	}
	step := 255.0 / float64(grayLevels-1)
	for i, v := range gray.Pix {
		nearest := round(float64(v)/step) * step
		if diff := float64(v) - nearest; diff > 0.5 || diff < -0.5 {
			t.Fatalf("pixel %d = %d, which is not on the %d-level ramp", i, v, grayLevels)
		}
	}
}

func TestProfileLookup(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"x4", "x4-v1"},
		{"X4", "x4-v1"},
		{"x4-v1", "x4-v1"},
		{"x3", "x3-v1"},
		{" x3 ", "x3-v1"},
		{"generic-v1", "generic-v1"},
	} {
		got, err := LookupProfile(tt.in)
		if err != nil {
			t.Errorf("LookupProfile(%q) failed: %v", tt.in, err)
			continue
		}
		if got.Name != tt.want {
			t.Errorf("LookupProfile(%q) = %q, want %q", tt.in, got.Name, tt.want)
		}
	}

	if _, err := LookupProfile("nosuchdevice"); err == nil {
		t.Error("LookupProfile accepted an unknown profile")
	}
	if _, err := LookupProfile(""); err == nil {
		t.Error("LookupProfile accepted an empty name")
	}
}

func TestProfileForModel(t *testing.T) {
	// The device reports "X4" in /api/status; that string must select a profile.
	p, err := ProfileForModel("X4")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "x4-v1" {
		t.Errorf("ProfileForModel(X4) = %q, want x4-v1", p.Name)
	}
	if _, err := ProfileForModel("X9"); err == nil {
		t.Error("ProfileForModel accepted an unknown model")
	}
}

func TestOptimizeCacheReusesArtifact(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	cache := NewOptimizeCache(filepath.Join(dir, "cache"))

	first, e1, cached, err := cache.Optimize(src, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("first call reported a cache hit")
	}

	second, e2, cached, err := cache.Optimize(src, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Error("second call did not hit the cache")
	}
	if first != second {
		t.Errorf("cache returned different paths: %s vs %s", first, second)
	}
	if e1.OutputSHA256 != e2.OutputSHA256 {
		t.Error("cached entry reports a different output hash")
	}
	if e1.OutputSHA256 == "" {
		t.Error("OutputSHA256 is empty; the manifest needs the hash of the sent bytes")
	}
}

func TestOptimizeCacheKeyDependsOnProfile(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	cache := NewOptimizeCache(filepath.Join(dir, "cache"))

	p := testProfile()
	if _, _, _, err := cache.Optimize(src, p); err != nil {
		t.Fatal(err)
	}

	// Changing any setting that alters the output must miss.
	for name, mutate := range map[string]func(Profile) Profile{
		"panel size": func(p Profile) Profile { return p.WithPanelSize(90, 120) },
		"dither":     func(p Profile) Profile { p.Dither = true; return p },
		"quality":    func(p Profile) Profile { p.JPEGQuality = 50; return p },
		"grayscale":  func(p Profile) Profile { p.Grayscale = false; return p },
	} {
		_, _, cached, err := cache.Optimize(src, mutate(p))
		if err != nil {
			t.Fatal(err)
		}
		if cached {
			t.Errorf("changing %s still hit the cache; the key ignores it", name)
		}
	}
}

func TestOptimizeCacheMissesWhenSourceChanges(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	cache := NewOptimizeCache(filepath.Join(dir, "cache"))

	if _, _, _, err := cache.Optimize(src, testProfile()); err != nil {
		t.Fatal(err)
	}

	// Rewrite the book with an extra image, changing its hash.
	src2 := writeEPUB(t, t.TempDir(), zipEntry{"OEBPS/images/extra.png", colorPNG(t, 120, 120)})
	_, _, cached, err := cache.Optimize(src2, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("a different source hit the cache")
	}
}

func TestOptimizeCacheTreatsOrphanedArtifactAsMiss(t *testing.T) {
	dir := t.TempDir()
	src := writeEPUB(t, dir)
	root := filepath.Join(dir, "cache")
	cache := NewOptimizeCache(root)

	if _, _, _, err := cache.Optimize(src, testProfile()); err != nil {
		t.Fatal(err)
	}

	sum, err := HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	_, meta := cache.paths(optKey(sum, testProfile()))
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}

	if _, _, cached, err := cache.Optimize(src, testProfile()); err != nil {
		t.Fatal(err)
	} else if cached {
		t.Error("an artifact with no metadata was treated as a hit")
	}
}

// TestOptimizedOutputPassesEpubCheck validates the optimizer's output with the
// reference validator, the same way internal/epub does. shelf's own parser
// cannot catch a mistake shelf makes consistently in both directions.
func TestOptimizedOutputPassesEpubCheck(t *testing.T) {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed; skipping independent validation")
	}

	for _, tc := range []struct {
		name string
		p    Profile
	}{
		{"with panel size", testProfile()},
		{"without panel size", builtinProfiles["x3-v1"]},
		{"dithered", func() Profile { p := testProfile(); p.Dither = true; return p }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeEPUB(t, dir)
			dst := filepath.Join(dir, "out.epub")

			if _, err := Optimize(src, dst, tc.p); err != nil {
				t.Fatal(err)
			}

			out, err := exec.Command(bin, dst).CombinedOutput()
			if err == nil {
				return
			}
			// epubcheck exits non-zero on warnings too; only ERROR and FATAL
			// lines are a real failure.
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "FATAL") {
					t.Errorf("epubcheck rejected the optimized file:\n%s", out)
					return
				}
			}
		})
	}
}

// TestSourceFixtureIsValid guards the guard: if the fixture these tests build
// were invalid, the epubcheck test above would fail for reasons that have
// nothing to do with the optimizer. That is exactly the trap the epub
// package's fixtures fell into.
func TestSourceFixtureIsValid(t *testing.T) {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed; skipping independent validation")
	}

	src := writeEPUB(t, t.TempDir())
	out, err := exec.Command(bin, src).CombinedOutput()
	if err == nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "FATAL") {
			t.Errorf("the test fixture itself is not a valid EPUB:\n%s", out)
			return
		}
	}
}

func TestProfileIdentityIsStable(t *testing.T) {
	// A stable profile must produce a stable key, or every sync re-uploads.
	p := testProfile()
	if p.identity() != testProfile().identity() {
		t.Error("identity() is not stable across equal profiles")
	}

	seen := map[string]string{}
	for _, name := range ProfileNames() {
		id := builtinProfiles[name].identity()
		if prev, dup := seen[id]; dup {
			t.Errorf("profiles %q and %q share an identity: %s", prev, name, id)
		}
		seen[id] = name
	}
}
