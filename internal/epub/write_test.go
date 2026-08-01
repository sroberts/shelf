package epub

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestMimetypeEntryIsSpecCompliant is the single most important test in this
// package. The OCF spec requires the mimetype entry to be the first entry in
// the archive, stored uncompressed, and carrying no extra field. Go's
// archive/zip will happily add an extended-timestamp extra field if the header
// is built the obvious way, which produces an archive that strict readers
// reject. This asserts all three properties on the raw bytes.
func TestMimetypeEntryIsSpecCompliant(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *builder
	}{
		{"epub3", epub3(t)},
		{"epub2", epub2(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.b.write(t)
			if err := Write(path, Patch{Title: strptr("Rewritten")}); err != nil {
				t.Fatal(err)
			}
			assertMimetypeCompliant(t, path)
		})
	}
}

func assertMimetypeCompliant(t *testing.T, path string) {
	t.Helper()

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("the edited archive does not open as a zip: %v", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		t.Fatal("archive is empty")
	}

	e := zr.File[0]
	if e.Name != mimetypePath {
		t.Errorf("first entry is %q, want %q", e.Name, mimetypePath)
	}
	if e.Method != zip.Store {
		t.Errorf("mimetype method = %d, want Store (%d)", e.Method, zip.Store)
	}
	if len(e.Extra) != 0 {
		t.Errorf("mimetype carries a %d-byte extra field, want none: %x", len(e.Extra), e.Extra)
	}

	rc, err := e.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body := make([]byte, 64)
	n, _ := rc.Read(body)
	if got := string(body[:n]); got != mimetypeValue {
		t.Errorf("mimetype content = %q, want %q", got, mimetypeValue)
	}

	// The spec also requires the mimetype content to begin at a fixed offset,
	// which only holds if the entry has no extra field and no compression.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if idx := strings.Index(string(raw), mimetypeValue); idx != 38 {
		t.Errorf("mimetype content starts at offset %d, want 38", idx)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	path := epub3(t).write(t)

	patch := Patch{
		Title:       strptr("A Wizard of Earthsea (Revised)"),
		Series:      strptr("Earthsea"),
		SeriesIndex: f64ptr(1.5),
		Publisher:   strptr("Houghton Mifflin"),
		Language:    strptr("en-US"),
		Subjects:    &[]string{"Fantasy", "Classics", "Le Guin"},
		Authors: &[]Creator{
			{Name: "Ursula K. Le Guin", FileAs: "Le Guin, Ursula K.", Role: "aut"},
		},
	}
	if err := Write(path, patch); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if err != nil {
		t.Fatalf("the edited file no longer parses: %v", err)
	}
	defer f.Close()

	m := f.Metadata()
	if m.Title != "A Wizard of Earthsea (Revised)" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.Series != "Earthsea" || m.SeriesIndex != 1.5 {
		t.Errorf("series = %q/%v, want Earthsea/1.5", m.Series, m.SeriesIndex)
	}
	if m.Publisher != "Houghton Mifflin" || m.Language != "en-US" {
		t.Errorf("publisher/language = %q/%q", m.Publisher, m.Language)
	}
	if !reflect.DeepEqual(m.Subjects, []string{"Fantasy", "Classics", "Le Guin"}) {
		t.Errorf("Subjects = %v", m.Subjects)
	}
	if got := m.AuthorNames(); !reflect.DeepEqual(got, []string{"Ursula K. Le Guin"}) {
		t.Errorf("AuthorNames = %v", got)
	}
	if m.AuthorSort() != "Le Guin, Ursula K." {
		t.Errorf("AuthorSort = %q", m.AuthorSort())
	}
}

// A partial edit must leave every other field exactly as it was.
func TestWritePreservesUntouchedMetadata(t *testing.T) {
	path := epub3(t).write(t)

	before := readMetadata(t, path)
	if err := Write(path, Patch{Publisher: strptr("New Publisher")}); err != nil {
		t.Fatal(err)
	}
	after := readMetadata(t, path)

	if after.Publisher != "New Publisher" {
		t.Fatalf("Publisher = %q, want the new value", after.Publisher)
	}

	// Everything else must be identical.
	before.Publisher = after.Publisher
	if !reflect.DeepEqual(before, after) {
		t.Errorf("unrelated metadata changed:\n before = %+v\n after  = %+v", before, after)
	}
}

// Content files must survive an edit byte-for-byte. shelf edits metadata, not books.
func TestWritePreservesContentBytes(t *testing.T) {
	path := epub3(t).write(t)
	before := archiveContents(t, path)

	if err := Write(path, Patch{Title: strptr("Changed")}); err != nil {
		t.Fatal(err)
	}
	after := archiveContents(t, path)

	for name, wantBody := range before {
		if name == "OEBPS/content.opf" {
			continue // the only entry allowed to change
		}
		gotBody, ok := after[name]
		if !ok {
			t.Errorf("entry %q disappeared from the archive", name)
			continue
		}
		if string(gotBody) != string(wantBody) {
			t.Errorf("entry %q changed: %d bytes -> %d bytes", name, len(wantBody), len(gotBody))
		}
	}
	if len(after) != len(before) {
		t.Errorf("entry count changed: %d -> %d", len(before), len(after))
	}
}

// Metadata elements shelf does not model, such as dcterms:modified, must be
// carried through unchanged rather than silently dropped.
func TestWritePreservesUnknownMetadataElements(t *testing.T) {
	path := epub3(t).write(t)

	if err := Write(path, Patch{Title: strptr("Changed")}); err != nil {
		t.Fatal(err)
	}

	opf := string(readOPF(t, path))
	for _, want := range []string{
		`property="dcterms:modified"`,
		`urn:isbn:9780553383041`,
		`urn:uuid:1c5c9f4e-0d4a-4d0b-9c6f-1a2b3c4d5e6f`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("edited OPF lost %q:\n%s", want, opf)
		}
	}
	// The manifest and spine must be untouched.
	if !strings.Contains(opf, `properties="cover-image"`) || !strings.Contains(opf, `<itemref idref="ch1"/>`) {
		t.Errorf("edited OPF damaged the manifest or spine:\n%s", opf)
	}
}

// An EPUB 2 document must not acquire EPUB 3 syntax, and vice versa.
func TestWriteUsesVersionAppropriateSeriesForm(t *testing.T) {
	t.Run("epub3", func(t *testing.T) {
		path := epub3(t).write(t)
		if err := Write(path, Patch{Series: strptr("Earthsea"), SeriesIndex: f64ptr(3)}); err != nil {
			t.Fatal(err)
		}
		opf := string(readOPF(t, path))

		if !strings.Contains(opf, `property="belongs-to-collection"`) {
			t.Error("EPUB 3 output is missing belongs-to-collection")
		}
		if !strings.Contains(opf, `property="group-position">3<`) {
			t.Errorf("EPUB 3 output is missing group-position:\n%s", opf)
		}
		// The calibre keys are written too, since that is what the wider
		// ecosystem reads.
		if !strings.Contains(opf, `name="calibre:series"`) {
			t.Error("EPUB 3 output is missing the calibre compatibility keys")
		}
		// There must be exactly one of each, not duplicates from the original.
		if n := strings.Count(opf, `property="belongs-to-collection"`); n != 1 {
			t.Errorf("belongs-to-collection appears %d times, want 1", n)
		}
		if n := strings.Count(opf, `name="calibre:series"`); n != 1 {
			t.Errorf("calibre:series appears %d times, want 1", n)
		}
	})

	t.Run("epub2", func(t *testing.T) {
		path := epub2(t).write(t)
		if err := Write(path, Patch{Series: strptr("Hainish"), SeriesIndex: f64ptr(2)}); err != nil {
			t.Fatal(err)
		}
		opf := string(readOPF(t, path))

		if strings.Contains(opf, "belongs-to-collection") {
			t.Errorf("EPUB 2 output should not use EPUB 3 collection syntax:\n%s", opf)
		}
		if !strings.Contains(opf, `<meta name="calibre:series" content="Hainish"/>`) {
			t.Errorf("EPUB 2 output is missing calibre:series:\n%s", opf)
		}
		if !strings.Contains(opf, `<meta name="calibre:series_index" content="2"/>`) {
			t.Errorf("EPUB 2 output is missing calibre:series_index:\n%s", opf)
		}
	})
}

// A whole-numbered index must render as "1", not "1.0".
func TestSeriesIndexFormatting(t *testing.T) {
	path := epub2(t).write(t)
	if err := Write(path, Patch{SeriesIndex: f64ptr(7)}); err != nil {
		t.Fatal(err)
	}
	opf := string(readOPF(t, path))
	if !strings.Contains(opf, `content="7"`) {
		t.Errorf("index rendered with a decimal suffix:\n%s", opf)
	}
	// Setting only the index must not clear the series name.
	if m := readMetadata(t, path); m.Series != "Hainish Cycle" {
		t.Errorf("Series = %q, want it preserved when only the index is set", m.Series)
	}
}

func TestClearSeries(t *testing.T) {
	path := epub3(t).write(t)
	if err := Write(path, Patch{Series: strptr("")}); err != nil {
		t.Fatal(err)
	}

	opf := string(readOPF(t, path))
	for _, gone := range []string{"belongs-to-collection", "calibre:series", "group-position"} {
		if strings.Contains(opf, gone) {
			t.Errorf("clearing the series left %q behind:\n%s", gone, opf)
		}
	}
	if m := readMetadata(t, path); m.Series != "" {
		t.Errorf("Series = %q, want empty", m.Series)
	}
}

// Replacing the author list must also drop the refinements that pointed at the
// old creators, or the file accumulates orphaned metadata on every edit.
func TestReplacingAuthorsDropsStaleRefinements(t *testing.T) {
	path := epub3(t).write(t)

	if err := Write(path, Patch{Authors: &[]Creator{{Name: "Someone Else"}}}); err != nil {
		t.Fatal(err)
	}
	opf := string(readOPF(t, path))

	// The old creators and the refinements describing them must be gone.
	for _, gone := range []string{"Ruth Robbins", "#illus", "Le Guin, Ursula K."} {
		if strings.Contains(opf, gone) {
			t.Errorf("the old creator list survived (%q):\n%s", gone, opf)
		}
	}
	if !strings.Contains(opf, "Someone Else") {
		t.Errorf("the new author is missing:\n%s", opf)
	}

	// The invariant that actually matters: no refinement may dangle. Creator
	// ids are regenerated on write, so a surviving refinement to a removed
	// creator would point at nothing.
	assertNoDanglingRefinements(t, opf)
}

// assertNoDanglingRefinements checks that every refines="#id" resolves to an
// element that declares that id.
func assertNoDanglingRefinements(t *testing.T, opf string) {
	t.Helper()

	ids := map[string]bool{}
	for _, m := range regexpID.FindAllStringSubmatch(opf, -1) {
		ids[m[1]] = true
	}
	for _, m := range regexpRefines.FindAllStringSubmatch(opf, -1) {
		if !ids[m[1]] {
			t.Errorf("refinement points at missing id %q:\n%s", m[1], opf)
		}
	}
}

var (
	regexpID      = regexp.MustCompile(`\bid="([^"]+)"`)
	regexpRefines = regexp.MustCompile(`\brefines="#([^"]+)"`)
)

// Repeated edits must converge rather than accumulating cruft.
func TestRepeatedEditsAreStable(t *testing.T) {
	path := epub3(t).write(t)

	for i := 0; i < 3; i++ {
		if err := Write(path, Patch{Series: strptr("Earthsea"), SeriesIndex: f64ptr(1)}); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}

	opf := string(readOPF(t, path))
	if n := strings.Count(opf, `name="calibre:series"`); n != 1 {
		t.Errorf("calibre:series appears %d times after repeated edits, want 1", n)
	}
	if n := strings.Count(opf, "belongs-to-collection"); n != 1 {
		t.Errorf("belongs-to-collection appears %d times, want 1", n)
	}
	assertMimetypeCompliant(t, path)
}

func TestWriteAddsMissingFields(t *testing.T) {
	// A minimal OPF with no publisher, series, or subjects.
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Bare</dc:title>
  </metadata>
  <manifest/><spine/>
</package>`

	dir := t.TempDir()
	path := filepath.Join(dir, "bare.epub")
	data := newBuilder().
		addString(containerPath, containerXML).
		addString("OEBPS/content.opf", opf).
		bytes(t)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	patch := Patch{
		Publisher: strptr("Small Press"),
		Series:    strptr("Standalone"),
		Subjects:  &[]string{"Essays"},
		Authors:   &[]Creator{{Name: "A. Writer", FileAs: "Writer, A."}},
	}
	if err := Write(path, patch); err != nil {
		t.Fatal(err)
	}

	m := readMetadata(t, path)
	if m.Publisher != "Small Press" || m.Series != "Standalone" {
		t.Errorf("added fields missing: %+v", m)
	}
	if !reflect.DeepEqual(m.Subjects, []string{"Essays"}) {
		t.Errorf("Subjects = %v", m.Subjects)
	}
	if m.AuthorSort() != "Writer, A." {
		t.Errorf("AuthorSort = %q", m.AuthorSort())
	}
}

// XML metacharacters in user input must not be able to corrupt the document.
func TestWriteEscapesXML(t *testing.T) {
	path := epub3(t).write(t)

	nasty := `Tom & Jerry <script>alert("x")</script>`
	if err := Write(path, Patch{Title: strptr(nasty), Series: strptr(`A & B "quoted"`)}); err != nil {
		t.Fatal(err)
	}

	m := readMetadata(t, path)
	if m.Title != nasty {
		t.Errorf("Title = %q, want %q", m.Title, nasty)
	}
	if m.Series != `A & B "quoted"` {
		t.Errorf("Series = %q", m.Series)
	}
	assertMimetypeCompliant(t, path)
}

func TestWriteIsAtomicOnFailure(t *testing.T) {
	path := epub3(t).write(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// An empty patch is a no-op and must not rewrite the file at all.
	if err := Write(path, Patch{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Error("an empty patch rewrote the file")
	}

	// No temp files may be left behind in the book's directory.
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	path := epub3(t).write(t)
	if err := Write(path, Patch{Title: strptr("Changed")}); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestWritePreservesFileMode(t *testing.T) {
	path := epub3(t).write(t)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, Patch{Title: strptr("Changed")}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shelf-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func readMetadata(t *testing.T, path string) *Metadata {
	t.Helper()
	f, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	return f.Metadata()
}

func readOPF(t *testing.T, path string) []byte {
	t.Helper()
	f, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	return f.opfData
}

// archiveContents reads every entry into a map for comparison.
func archiveContents(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	out := map[string][]byte{}
	for _, e := range zr.File {
		rc, err := e.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, e.UncompressedSize64)
		if _, err := readFull(rc, buf); err != nil {
			t.Fatalf("read %s: %v", e.Name, err)
		}
		rc.Close()
		out[e.Name] = buf
	}
	return out
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}
