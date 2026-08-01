package epub

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadEPUB3(t *testing.T) {
	f, err := Open(epub3(t).write(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Version() != "3.0" {
		t.Errorf("Version = %q, want 3.0", f.Version())
	}
	if f.OPFPath() != "OEBPS/content.opf" {
		t.Errorf("OPFPath = %q", f.OPFPath())
	}

	m := f.Metadata()
	if m.Title != "A Wizard of Earthsea" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.TitleSort != "Wizard of Earthsea, A" {
		t.Errorf("TitleSort = %q", m.TitleSort)
	}

	// The illustrator has role "ill" and must not be treated as an author.
	if got := m.AuthorNames(); !reflect.DeepEqual(got, []string{"Ursula K. Le Guin"}) {
		t.Errorf("AuthorNames = %v, want only the author", got)
	}
	if m.AuthorSort() != "Le Guin, Ursula K." {
		t.Errorf("AuthorSort = %q", m.AuthorSort())
	}

	if m.Series != "Earthsea Cycle" {
		t.Errorf("Series = %q", m.Series)
	}
	if !m.HasSeriesIndex || m.SeriesIndex != 1 {
		t.Errorf("SeriesIndex = %v (has=%v), want 1", m.SeriesIndex, m.HasSeriesIndex)
	}
	if m.Language != "en" || m.Publisher != "Parnassus Press" || m.Date != "1968-11-01" {
		t.Errorf("unexpected core fields: %+v", m)
	}
	if !reflect.DeepEqual(m.Subjects, []string{"Fantasy", "Coming of Age"}) {
		t.Errorf("Subjects = %v", m.Subjects)
	}
	if m.Identifiers["isbn"] != "9780553383041" {
		t.Errorf("isbn = %q", m.Identifiers["isbn"])
	}
	if m.Identifiers["uuid"] != "1c5c9f4e-0d4a-4d0b-9c6f-1a2b3c4d5e6f" {
		t.Errorf("uuid = %q", m.Identifiers["uuid"])
	}
}

func TestReadEPUB2(t *testing.T) {
	f, err := Open(epub2(t).write(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m := f.Metadata()
	if m.Title != "The Dispossessed" {
		t.Errorf("Title = %q", m.Title)
	}
	// EPUB 2 carries file-as and role as opf: attributes rather than refinements.
	if m.AuthorSort() != "Le Guin, Ursula K." {
		t.Errorf("AuthorSort = %q", m.AuthorSort())
	}
	if m.Series != "Hainish Cycle" || m.SeriesIndex != 5 {
		t.Errorf("series = %q/%v", m.Series, m.SeriesIndex)
	}
	// Schemes are lowercased so lookups do not depend on the file's casing.
	if m.Identifiers["isbn"] != "9780060512750" {
		t.Errorf("isbn = %q", m.Identifiers["isbn"])
	}
	if m.Identifiers["uuid"] != "6f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Errorf("uuid = %q", m.Identifiers["uuid"])
	}
}

func TestSeriesPrefersEPUB3OverCalibre(t *testing.T) {
	// A document carrying both forms with different values must resolve to the
	// standard one.
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>T</dc:title>
    <meta name="calibre:series" content="Stale Legacy Value"/>
    <meta name="calibre:series_index" content="9"/>
    <meta property="belongs-to-collection" id="c1">Authoritative</meta>
    <meta refines="#c1" property="collection-type">series</meta>
    <meta refines="#c1" property="group-position">2</meta>
  </metadata>
  <manifest/><spine/>
</package>`

	f := openBytes(t, newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", opf))
	defer f.Close()

	m := f.Metadata()
	if m.Series != "Authoritative" {
		t.Errorf("Series = %q, want the EPUB 3 value", m.Series)
	}
	if m.SeriesIndex != 2 {
		t.Errorf("SeriesIndex = %v, want 2", m.SeriesIndex)
	}
}

// A "set" collection is not a series and must be ignored.
func TestSeriesIgnoresNonSeriesCollection(t *testing.T) {
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>T</dc:title>
    <meta property="belongs-to-collection" id="c1">Some Boxed Set</meta>
    <meta refines="#c1" property="collection-type">set</meta>
  </metadata>
  <manifest/><spine/>
</package>`

	f := openBytes(t, newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", opf))
	defer f.Close()

	if s := f.Metadata().Series; s != "" {
		t.Errorf("Series = %q, want empty for a non-series collection", s)
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("not a zip", func(t *testing.T) {
		p := filepath.Join(dir, "notzip.epub")
		os.WriteFile(p, []byte("this is plain text, not an archive"), 0o644)
		_, err := Open(p)
		if !errors.Is(err, ErrNotEPUB) {
			t.Errorf("err = %v, want ErrNotEPUB", err)
		}
	})

	t.Run("zip without container", func(t *testing.T) {
		p := filepath.Join(dir, "nocontainer.epub")
		os.WriteFile(p, newBuilder().addString("random.txt", "hello").bytes(t), 0o644)
		_, err := Open(p)
		if !errors.Is(err, ErrNoContainer) {
			t.Errorf("err = %v, want ErrNoContainer", err)
		}
	})

	t.Run("container pointing at a missing OPF", func(t *testing.T) {
		p := filepath.Join(dir, "noopf.epub")
		os.WriteFile(p, newBuilder().addString(containerPath, containerXML).bytes(t), 0o644)
		_, err := Open(p)
		if !errors.Is(err, ErrNoOPF) {
			t.Errorf("err = %v, want ErrNoOPF", err)
		}
	})

	t.Run("malformed OPF", func(t *testing.T) {
		p := filepath.Join(dir, "badopf.epub")
		os.WriteFile(p, newBuilder().
			addString(containerPath, containerXML).
			addString("OEBPS/content.opf", "<package><metadata><dc:title>unclosed").bytes(t), 0o644)
		if _, err := Open(p); err == nil {
			t.Error("expected an error for a malformed package document")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Open(filepath.Join(dir, "nope.epub")); err == nil {
			t.Error("expected an error for a missing file")
		}
	})
}

// Real libraries contain EPUBs with wrong charset declarations. Reading them is
// more useful than rejecting them.
func TestLenientCharsetDeclaration(t *testing.T) {
	opf := `<?xml version="1.0" encoding="windows-1252"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Naïve Café</dc:title>
  </metadata>
  <manifest/><spine/>
</package>`

	f := openBytes(t, newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", opf))
	defer f.Close()

	if got := f.Metadata().Title; got != "Naïve Café" {
		t.Errorf("Title = %q", got)
	}
}

func TestCover(t *testing.T) {
	t.Run("epub3 cover-image property", func(t *testing.T) {
		f, err := Open(epub3(t).write(t))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		if p, err := f.CoverPath(); err != nil || p != "OEBPS/images/cover.png" {
			t.Errorf("CoverPath = %q, %v", p, err)
		}
		img, err := f.CoverImage()
		if err != nil {
			t.Fatal(err)
		}
		if got := img.Bounds().Dx(); got != 600 {
			t.Errorf("cover width = %d, want 600", got)
		}
	})

	t.Run("epub2 meta name=cover pointer", func(t *testing.T) {
		f, err := Open(epub2(t).write(t))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		if p, err := f.CoverPath(); err != nil || p != "OEBPS/cover.jpeg" {
			t.Errorf("CoverPath = %q, %v", p, err)
		}
	})

	t.Run("thumbnail downscales and preserves aspect", func(t *testing.T) {
		f, err := Open(epub3(t).write(t))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		data, err := f.CoverThumbnailPNG(DefaultThumbnailWidth)
		if err != nil {
			t.Fatal(err)
		}
		img := decodePNG(t, data)
		if got := img.Bounds().Dx(); got != 256 {
			t.Errorf("thumbnail width = %d, want 256", got)
		}
		// 600x900 scaled to width 256 gives height 384.
		if got := img.Bounds().Dy(); got != 384 {
			t.Errorf("thumbnail height = %d, want 384", got)
		}
	})

	t.Run("never upscales", func(t *testing.T) {
		small := newBuilder().
			addString(containerPath, containerXML).
			addString("OEBPS/content.opf", epub3OPF).
			add("OEBPS/images/cover.png", pngBytes(t, 80, 120))

		f := openBytes(t, small)
		defer f.Close()

		data, err := f.CoverThumbnailPNG(256)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodePNG(t, data).Bounds().Dx(); got != 80 {
			t.Errorf("width = %d, want the original 80 (no upscaling)", got)
		}
	})

	t.Run("declared but missing cover reads as no cover", func(t *testing.T) {
		// Manifest points at images/cover.png, which is not in the archive.
		f := openBytes(t, newBuilder().
			addString(containerPath, containerXML).
			addString("OEBPS/content.opf", epub3OPF))
		defer f.Close()

		if _, err := f.CoverImage(); !errors.Is(err, ErrNoCover) {
			t.Errorf("err = %v, want ErrNoCover", err)
		}
	})
}

func TestGuessFileAs(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Ursula K. Le Guin", "Le Guin, Ursula K."},
		{"Herman Melville", "Melville, Herman"},
		{"Vincent van Gogh", "van Gogh, Vincent"},
		{"Martin Luther King Jr.", "King Jr., Martin Luther"},
		{"Plato", "Plato"},
		{"Le Guin, Ursula K.", "Le Guin, Ursula K."}, // already sorted
		{"", ""},
	}
	for _, tt := range tests {
		if got := guessFileAs(tt.in); got != tt.want {
			t.Errorf("guessFileAs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsEPUB(t *testing.T) {
	dir := t.TempDir()

	valid := epub3(t).write(t)
	if !IsEPUB(valid) {
		t.Error("IsEPUB = false for a valid EPUB")
	}

	text := filepath.Join(dir, "a.txt")
	os.WriteFile(text, []byte("not an epub"), 0o644)
	if IsEPUB(text) {
		t.Error("IsEPUB = true for a text file")
	}

	if IsEPUB(filepath.Join(dir, "missing.epub")) {
		t.Error("IsEPUB = true for a missing file")
	}
	if IsEPUB(dir) {
		t.Error("IsEPUB = true for a directory")
	}
}

// openBytes builds an archive in memory and opens it.
func openBytes(t *testing.T, b *builder) *File {
	t.Helper()
	data := b.bytes(t)
	f, err := OpenReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	return img
}
