package epub

import (
	"archive/zip"
	"bytes"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// builder assembles synthetic EPUBs for tests. It writes a spec-correct
// archive: mimetype first, stored, no extra field.
type builder struct {
	entries []entry
}

type entry struct {
	name  string
	body  []byte
	store bool
}

func newBuilder() *builder { return &builder{} }

func (b *builder) add(name string, body []byte) *builder {
	b.entries = append(b.entries, entry{name: name, body: body})
	return b
}

func (b *builder) addString(name, body string) *builder {
	return b.add(name, []byte(body))
}

// bytes renders the archive.
func (b *builder) bytes(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	body := []byte(mimetypeValue)
	fh := &zip.FileHeader{
		Name:               mimetypePath,
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(body),
		CompressedSize64:   uint64(len(body)),
		UncompressedSize64: uint64(len(body)),
	}
	w, err := zw.CreateRaw(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}

	for _, e := range b.entries {
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
	return buf.Bytes()
}

// write renders the archive to a file inside t.TempDir and returns its path.
func (b *builder) write(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := os.WriteFile(path, b.bytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

// epub3OPF is a realistic EPUB 3 package document, including refinements and a
// collection, so parsing is exercised against the standard forms.
const epub3OPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title id="t1">A Wizard of Earthsea</dc:title>
    <meta refines="#t1" property="file-as">Wizard of Earthsea, A</meta>
    <dc:creator id="creator01">Ursula K. Le Guin</dc:creator>
    <meta refines="#creator01" property="file-as">Le Guin, Ursula K.</meta>
    <meta refines="#creator01" property="role" scheme="marc:relators">aut</meta>
    <dc:creator id="illus">Ruth Robbins</dc:creator>
    <meta refines="#illus" property="role" scheme="marc:relators">ill</meta>
    <dc:identifier id="pub-id">urn:isbn:9780553383041</dc:identifier>
    <dc:identifier>urn:uuid:1c5c9f4e-0d4a-4d0b-9c6f-1a2b3c4d5e6f</dc:identifier>
    <dc:language>en</dc:language>
    <dc:publisher>Parnassus Press</dc:publisher>
    <dc:date>1968-11-01</dc:date>
    <dc:subject>Fantasy</dc:subject>
    <dc:subject>Coming of Age</dc:subject>
    <meta property="belongs-to-collection" id="c1">Earthsea Cycle</meta>
    <meta refines="#c1" property="collection-type">series</meta>
    <meta refines="#c1" property="group-position">1</meta>
    <meta property="dcterms:modified">2024-01-01T00:00:00Z</meta>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png" properties="cover-image"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
  </spine>
</package>`

// epub2OPF uses the legacy forms: opf: attributes and calibre: meta keys.
const epub2OPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uuid_id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>The Dispossessed</dc:title>
    <dc:creator opf:file-as="Le Guin, Ursula K." opf:role="aut">Ursula K. Le Guin</dc:creator>
    <dc:identifier id="uuid_id" opf:scheme="uuid">6f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8</dc:identifier>
    <dc:identifier opf:scheme="ISBN">9780060512750</dc:identifier>
    <dc:language>en</dc:language>
    <dc:subject>Science Fiction</dc:subject>
    <meta name="calibre:series" content="Hainish Cycle"/>
    <meta name="calibre:series_index" content="5"/>
    <meta name="cover" content="cover-img"/>
  </metadata>
  <manifest>
    <item id="cover-img" href="cover.jpeg" media-type="image/png"/>
    <item id="ch1" href="ch1.html" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="ch1"/>
  </spine>
</package>`

// pngBytes renders a solid-color PNG of the given size.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// epub3 builds a complete, valid EPUB 3 test book.
func epub3(t *testing.T) *builder {
	t.Helper()
	return newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", epub3OPF).
		add("OEBPS/images/cover.png", pngBytes(t, 600, 900)).
		addString("OEBPS/nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><nav/></body></html>`).
		addString("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>There was a boy.</p></body></html>`)
}

// epub2 builds a complete, valid EPUB 2 test book.
func epub2(t *testing.T) *builder {
	t.Helper()
	return newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", epub2OPF).
		add("OEBPS/cover.jpeg", pngBytes(t, 400, 600)).
		addString("OEBPS/ch1.html", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Anarres.</p></body></html>`)
}

func strptr(s string) *string   { return &s }
func f64ptr(v float64) *float64 { return &v }
