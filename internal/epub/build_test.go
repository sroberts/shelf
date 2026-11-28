package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var simpleDate = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

func simpleBook() SimpleBook {
	return SimpleBook{
		Identifier:  "urn:shelf:mail:abc@sentry.io",
		Title:       "2026-11-08 New issue in api",
		Authors:     []string{"Sentry"},
		Date:        simpleDate,
		Description: "An error spiked.",
		Subjects:    []string{"mail", "k7q2"},
		Series:      "Sentry alerts",
		Paragraphs:  []string{"First paragraph.", "Second paragraph."},
	}
}

func writeSimpleFile(t *testing.T, b SimpleBook) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.epub")
	var buf bytes.Buffer
	if err := WriteSimple(&buf, b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteSimpleRoundTrips(t *testing.T) {
	f, err := Open(writeSimpleFile(t, simpleBook()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m := f.Metadata()
	if m.Title != "2026-11-08 New issue in api" {
		t.Errorf("title %q", m.Title)
	}
	if names := m.AuthorNames(); len(names) != 1 || names[0] != "Sentry" {
		t.Errorf("authors %v", names)
	}
	if m.Series != "Sentry alerts" {
		t.Errorf("series %q", m.Series)
	}
	if m.Description != "An error spiked." {
		t.Errorf("description %q", m.Description)
	}
	if len(m.Subjects) != 2 || m.Subjects[0] != "mail" {
		t.Errorf("subjects %v", m.Subjects)
	}
	if !strings.HasPrefix(m.Date, "2026-11-08") {
		t.Errorf("date %q", m.Date)
	}
}

func TestWriteSimpleMimetypeFirstAndStored(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSimple(&buf, simpleBook()); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if first := zr.File[0]; first.Name != "mimetype" || first.Method != zip.Store {
		t.Errorf("first entry %q method %d", first.Name, first.Method)
	}
}

// The mail importer dedupes by hash, so the same input must give the same
// bytes every time.
func TestWriteSimpleIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	if err := WriteSimple(&a, simpleBook()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a clock-derived timestamp would differ now
	if err := WriteSimple(&b, simpleBook()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("identical input produced different bytes")
	}
}

func TestWriteSimpleNeedsATitle(t *testing.T) {
	b := simpleBook()
	b.Title = "\x01 "
	if err := WriteSimple(io.Discard, b); err == nil {
		t.Error("an untitled book should be refused")
	}
}

// Mail is written by strangers. Markup must come out as text, and characters
// XML cannot carry at all must be dropped rather than break the document.
func TestWriteSimpleHostileText(t *testing.T) {
	b := simpleBook()
	b.Title = `<script>alert(1)</script> & "quotes"`
	b.Authors = []string{"Eve <eve@example.com>\x00"}
	b.Description = "bad\x01bytes\xff\xfe here"
	b.Series = `]]><evil/>`
	b.Paragraphs = []string{"<img src=x onerror=alert(1)>", "tab\tand\x0bvertical", "\xc3\x28 invalid"}

	path := writeSimpleFile(t, b)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xhtml") && !strings.HasSuffix(f.Name, ".opf") && !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()

		dec := xml.NewDecoder(bytes.NewReader(body))
		dec.Strict = true
		dec.Entity = xml.HTMLEntity
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s is not well-formed: %v\n%s", f.Name, err, body)
			}
			if se, ok := tok.(xml.StartElement); ok {
				switch se.Name.Local {
				case "script", "img", "evil":
					t.Errorf("%s contains a live <%s> element", f.Name, se.Name.Local)
				}
			}
		}
	}

	ef, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ef.Close()
	if got := ef.Metadata().Title; got != `<script>alert(1)</script> & "quotes"` {
		t.Errorf("title did not survive as text: %q", got)
	}
}

func TestEpubCheckValidatesSimpleBook(t *testing.T) {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed; skipping independent validation")
	}
	out, err := exec.Command(bin, writeSimpleFile(t, simpleBook())).CombinedOutput()
	if err == nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "FATAL") {
			t.Fatalf("epubcheck rejected the built file:\n%s", out)
		}
	}
}

// The mimetype entry must carry no zip extra field (epubcheck PKG-005). A
// non-zero Modified time makes archive/zip add one, so this is checked here
// directly rather than only when epubcheck happens to be installed.
func TestSimpleBookMimetypeHasNoExtraField(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSimple(&buf, SimpleBook{Title: "T", Date: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	first := zr.File[0]
	if first.Name != "mimetype" || first.Method != zip.Store {
		t.Fatalf("first entry %q method %d; want stored mimetype", first.Name, first.Method)
	}
	if len(first.Extra) != 0 {
		t.Errorf("mimetype has a %d-byte extra field", len(first.Extra))
	}
}
