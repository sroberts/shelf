package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SimpleBook is a single-chapter, text-only book: what shelf builds from a
// mail message so a newsletter can be read on the device. Every string field
// is untrusted — it came from whoever sent the mail.
type SimpleBook struct {
	Identifier  string
	Title       string
	Authors     []string
	Language    string // default "en"
	Date        time.Time
	Description string
	Subjects    []string
	Series      string
	Paragraphs  []string
}

// WriteSimple writes b as an EPUB 3.
//
// The output is deterministic: the same SimpleBook always produces the same
// bytes, because zip entry times come from b.Date rather than the clock and
// nothing else varies. Callers dedupe by the file's hash, so a message built
// twice (a crash between import and delete) must hash the same both times.
func WriteSimple(w io.Writer, b SimpleBook) error {
	if strings.TrimSpace(clean(b.Title)) == "" {
		return errors.New("epub: a book needs a title")
	}
	if b.Language == "" {
		b.Language = "en"
	}
	if b.Identifier == "" {
		b.Identifier = "urn:shelf:" + clean(b.Title)
	}
	stamp := b.Date.UTC()
	if stamp.IsZero() || stamp.Year() < 1980 {
		// The zip format cannot represent dates before 1980.
		stamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	zw := zip.NewWriter(w)
	add := func(name string, method uint16, body []byte) error {
		h := &zip.FileHeader{Name: name, Method: method, Modified: stamp}
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}

	// The OCF spec requires mimetype first and stored, so a reader can
	// identify the file by its leading bytes.
	if err := add("mimetype", zip.Store, []byte("application/epub+zip")); err != nil {
		return err
	}
	if err := add("META-INF/container.xml", zip.Deflate, []byte(simpleContainerXML)); err != nil {
		return err
	}
	if err := add("OEBPS/content.opf", zip.Deflate, buildOPF(b, stamp)); err != nil {
		return err
	}
	if err := add("OEBPS/nav.xhtml", zip.Deflate, buildNav(b)); err != nil {
		return err
	}
	if err := add("OEBPS/text.xhtml", zip.Deflate, buildText(b)); err != nil {
		return err
	}
	return zw.Close()
}

const simpleContainerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

func buildOPF(b SimpleBook, stamp time.Time) []byte {
	var s bytes.Buffer
	s.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="` + esc(b.Language) + `">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
`)
	fmt.Fprintf(&s, "    <dc:identifier id=\"bookid\">%s</dc:identifier>\n", esc(b.Identifier))
	fmt.Fprintf(&s, "    <dc:title>%s</dc:title>\n", esc(b.Title))
	for i, a := range b.Authors {
		if a = clean(a); strings.TrimSpace(a) == "" {
			continue
		}
		fmt.Fprintf(&s, "    <dc:creator id=\"creator%d\">%s</dc:creator>\n", i, esc(a))
	}
	fmt.Fprintf(&s, "    <dc:language>%s</dc:language>\n", esc(b.Language))
	if !b.Date.IsZero() {
		fmt.Fprintf(&s, "    <dc:date>%s</dc:date>\n", b.Date.UTC().Format("2006-01-02"))
	}
	if d := strings.TrimSpace(clean(b.Description)); d != "" {
		fmt.Fprintf(&s, "    <dc:description>%s</dc:description>\n", esc(d))
	}
	for _, sub := range b.Subjects {
		if sub = strings.TrimSpace(clean(sub)); sub != "" {
			fmt.Fprintf(&s, "    <dc:subject>%s</dc:subject>\n", esc(sub))
		}
	}
	fmt.Fprintf(&s, "    <meta property=\"dcterms:modified\">%s</meta>\n", stamp.Format("2006-01-02T15:04:05Z"))
	if series := strings.TrimSpace(clean(b.Series)); series != "" {
		// EPUB 3 collection, which applySeries prefers, plus the calibre pair
		// that older readers and tools look for.
		fmt.Fprintf(&s, "    <meta property=\"belongs-to-collection\" id=\"series\">%s</meta>\n", esc(series))
		s.WriteString("    <meta refines=\"#series\" property=\"collection-type\">series</meta>\n")
		fmt.Fprintf(&s, "    <meta name=\"calibre:series\" content=\"%s\"/>\n", esc(series))
	}
	s.WriteString(`  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="text"/>
  </spine>
</package>
`)
	return s.Bytes()
}

func xhtmlHead(lang, title string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="` + esc(lang) + `" lang="` + esc(lang) + `">
<head>
  <meta charset="UTF-8"/>
  <title>` + esc(title) + `</title>
</head>
`
}

func buildNav(b SimpleBook) []byte {
	return []byte(xhtmlHead(b.Language, b.Title) + `<body>
  <nav epub:type="toc" id="toc">
    <ol>
      <li><a href="text.xhtml">` + esc(b.Title) + `</a></li>
    </ol>
  </nav>
</body>
</html>
`)
}

func buildText(b SimpleBook) []byte {
	var s strings.Builder
	s.WriteString(xhtmlHead(b.Language, b.Title))
	s.WriteString("<body>\n  <h1>" + esc(b.Title) + "</h1>\n")
	for _, p := range b.Paragraphs {
		if p = strings.TrimSpace(clean(p)); p != "" {
			s.WriteString("  <p>" + esc(p) + "</p>\n")
		}
	}
	s.WriteString("</body>\n</html>\n")
	return []byte(s.String())
}

// clean makes s safe to put in XML: invalid UTF-8 is dropped, and so is every
// character XML 1.0 forbids (most C0 controls), which escaping cannot fix.
func clean(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r >= 0xD800 && r <= 0xDFFF:
			return -1
		case unicode.Is(unicode.Cc, r) && r >= 0x7f && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}

func simpleEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(clean(s)))
	return b.String()
}
