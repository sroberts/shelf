package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Patch describes a metadata edit. A nil pointer field means "leave unchanged",
// which is what lets `shelf meta --set series=X` touch exactly one field and
// leave the rest of the OPF byte-identical.
type Patch struct {
	Title       *string
	TitleSort   *string
	Language    *string
	Publisher   *string
	Date        *string
	Description *string
	Series      *string
	SeriesIndex *float64

	// Authors replaces the full creator list when non-nil. A non-nil empty
	// slice removes all creators.
	Authors *[]Creator
	// Subjects replaces all dc:subject entries (shelf's tags) when non-nil.
	Subjects *[]string
}

// IsEmpty reports whether the patch would change nothing.
func (p Patch) IsEmpty() bool {
	return p.Title == nil && p.TitleSort == nil && p.Language == nil &&
		p.Publisher == nil && p.Date == nil && p.Description == nil &&
		p.Series == nil && p.SeriesIndex == nil &&
		p.Authors == nil && p.Subjects == nil
}

// Write applies a metadata patch to the EPUB at name, in place.
//
// The write is atomic: the new archive is built in a temporary file in the same
// directory, flushed, and renamed over the original. A crash mid-write leaves
// the original book untouched.
func Write(name string, p Patch) error {
	if p.IsEmpty() {
		return nil
	}

	f, err := Open(name)
	if err != nil {
		return err
	}
	newOPF, err := f.patchOPF(p)
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", name, err)
	}

	// Capture the original mode so the rename does not change permissions.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(name); err == nil {
		mode = fi.Mode().Perm()
	}

	dir := filepath.Dir(name)
	tmp, err := os.CreateTemp(dir, ".shelf-*.epub")
	if err != nil {
		f.Close()
		return fmt.Errorf("create temp file next to %s: %w", name, err)
	}
	tmpName := tmp.Name()

	// On any failure past this point, remove the temp file and leave the
	// original in place.
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
		f.Close()
	}

	if err := repack(tmp, f, newOPF); err != nil {
		cleanup()
		return fmt.Errorf("%s: %w", name, err)
	}
	// Durability: the rename below is only safe if the bytes are on disk.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		f.Close()
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	// Release the original before renaming; Windows and some network
	// filesystems refuse to replace an open file.
	f.Close()

	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, name); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", name, err)
	}
	return nil
}

// repack writes a new EPUB containing every entry of src, with the package
// document replaced by newOPF.
func repack(w io.Writer, src *File, newOPF []byte) error {
	zw := zip.NewWriter(w)

	// The OCF spec requires the mimetype entry to be first, stored
	// uncompressed, and free of extra fields. Getting this wrong produces an
	// archive that many readers reject, so it is written explicitly rather
	// than copied through the generic path below.
	if err := writeMimetype(zw); err != nil {
		return err
	}

	opfName := src.opfPath
	for _, e := range src.zr.File {
		name := strings.TrimPrefix(e.Name, "./")
		if name == mimetypePath {
			continue // already written first
		}

		if name == opfName || e.Name == opfName {
			if err := writeDeflated(zw, e, newOPF); err != nil {
				return err
			}
			continue
		}
		if err := copyRaw(zw, e); err != nil {
			return fmt.Errorf("copy %s: %w", e.Name, err)
		}
	}
	return zw.Close()
}

// writeMimetype emits the required first entry.
func writeMimetype(zw *zip.Writer) error {
	body := []byte(mimetypeValue)
	fh := &zip.FileHeader{
		Name:               mimetypePath,
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(body),
		CompressedSize64:   uint64(len(body)),
		UncompressedSize64: uint64(len(body)),
		// Modified is deliberately left as the zero time. A non-zero Modified
		// makes archive/zip append an extended-timestamp extra field, and the
		// mimetype entry must carry no extra field at all.
	}

	w, err := zw.CreateRaw(fh)
	if err != nil {
		return fmt.Errorf("create mimetype entry: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write mimetype entry: %w", err)
	}
	return nil
}

// writeDeflated writes replacement content for an entry, compressing it.
func writeDeflated(zw *zip.Writer, orig *zip.File, body []byte) error {
	fh := &zip.FileHeader{
		Name:     orig.Name,
		Method:   zip.Deflate,
		Modified: orig.Modified,
	}
	fh.SetMode(orig.Mode())

	w, err := zw.CreateHeader(fh)
	if err != nil {
		return fmt.Errorf("create %s: %w", orig.Name, err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", orig.Name, err)
	}
	return nil
}

// copyRaw copies an entry without recompressing it. Besides being much faster,
// this guarantees that content shelf did not touch comes out byte-identical.
func copyRaw(zw *zip.Writer, e *zip.File) error {
	fh := e.FileHeader
	// Drop the zip64 extended-information field. archive/zip regenerates it
	// when it is actually needed, and a stale copy confuses strict readers.
	fh.Extra = stripExtra(fh.Extra, 0x0001)

	w, err := zw.CreateRaw(&fh)
	if err != nil {
		return err
	}
	rc, err := e.OpenRaw()
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

// stripExtra removes all extra-field records with the given header ID.
func stripExtra(extra []byte, dropID uint16) []byte {
	var out []byte
	for len(extra) >= 4 {
		id := uint16(extra[0]) | uint16(extra[1])<<8
		size := int(uint16(extra[2]) | uint16(extra[3])<<8)
		if len(extra) < 4+size {
			break // malformed; keep what remains rather than guessing
		}
		record := extra[:4+size]
		if id != dropID {
			out = append(out, record...)
		}
		extra = extra[4+size:]
	}
	return out
}

// patchOPF returns the package document with the patch applied.
//
// Only the children of <metadata> are regenerated. Everything else in the
// document, including the manifest, spine, and the exact bytes of metadata
// entries the patch does not touch, is preserved verbatim.
func (f *File) patchOPF(p Patch) ([]byte, error) {
	pkg := f.pkg
	indent := detectIndent(f.opfData, pkg)
	epub3 := strings.HasPrefix(pkg.Version, "3")

	var b strings.Builder
	b.Grow(len(f.opfData))

	// Track what the patch still needs to emit; anything left over at the end
	// had no existing element to replace and is appended.
	pending := map[string]bool{}
	mark := func(k string, set bool) {
		if set {
			pending[k] = true
		}
	}
	mark("title", p.Title != nil)
	mark("language", p.Language != nil)
	mark("publisher", p.Publisher != nil)
	mark("date", p.Date != nil)
	mark("description", p.Description != nil)
	mark("creator", p.Authors != nil)
	mark("subject", p.Subjects != nil)
	mark("series", p.Series != nil || p.SeriesIndex != nil)

	// The series edit needs the merged value, since --set series_index alone
	// must not erase the existing series name.
	cur := f.Metadata()
	series := cur.Series
	if p.Series != nil {
		series = *p.Series
	}
	seriesIndex, hasIndex := cur.SeriesIndex, cur.HasSeriesIndex
	if p.SeriesIndex != nil {
		seriesIndex, hasIndex = *p.SeriesIndex, true
	}
	seriesTouched := p.Series != nil || p.SeriesIndex != nil

	// Creator refinement ids that must be dropped alongside their creators.
	staleRefines := map[string]bool{}
	if p.Authors != nil {
		for _, c := range pkg.children {
			if c.isDC("creator") {
				if id := c.attr("", "id"); id != "" {
					staleRefines[id] = true
				}
			}
		}
	}

	emit := func(s string) {
		if s == "" {
			return
		}
		b.WriteString(indent)
		b.WriteString(s)
	}

	for _, c := range pkg.children {
		verbatim := string(f.opfData[c.start:c.end])

		// Drop refinements whose target element is going away.
		if c.isMeta() {
			if id := strings.TrimPrefix(c.attr("", "refines"), "#"); id != "" && staleRefines[id] {
				continue
			}
		}

		switch {
		case p.Title != nil && c.isDC("title"):
			if pending["title"] {
				delete(pending, "title")
				emit(titleElement(*p.Title, p.TitleSort, c, epub3, indent))
			}
			continue

		case p.Language != nil && c.isDC("language"):
			if pending["language"] {
				delete(pending, "language")
				emit(dcElement("language", *p.Language))
			}
			continue

		case p.Publisher != nil && c.isDC("publisher"):
			if pending["publisher"] {
				delete(pending, "publisher")
				emit(dcElement("publisher", *p.Publisher))
			}
			continue

		case p.Date != nil && c.isDC("date"):
			if pending["date"] {
				delete(pending, "date")
				emit(dcElement("date", *p.Date))
			}
			continue

		case p.Description != nil && c.isDC("description"):
			if pending["description"] {
				delete(pending, "description")
				emit(dcElement("description", *p.Description))
			}
			continue

		case p.Authors != nil && c.isDC("creator"):
			if pending["creator"] {
				delete(pending, "creator")
				emit(creatorElements(*p.Authors, epub3, indent))
			}
			continue

		case p.Subjects != nil && c.isDC("subject"):
			if pending["subject"] {
				delete(pending, "subject")
				emit(subjectElements(*p.Subjects, indent))
			}
			continue

		case seriesTouched && isSeriesElement(c):
			// All existing series markers are dropped and rewritten together
			// so the EPUB 3 and calibre forms cannot drift apart.
			if pending["series"] {
				delete(pending, "series")
				emit(seriesElements(series, seriesIndex, hasIndex, epub3, indent))
			}
			continue
		}

		b.WriteString(indent)
		b.WriteString(verbatim)
	}

	// Fields with no existing element to replace get appended.
	if pending["title"] {
		emit(titleElement(*p.Title, p.TitleSort, metaChild{}, epub3, indent))
	}
	if pending["language"] {
		emit(dcElement("language", *p.Language))
	}
	if pending["publisher"] {
		emit(dcElement("publisher", *p.Publisher))
	}
	if pending["date"] {
		emit(dcElement("date", *p.Date))
	}
	if pending["description"] {
		emit(dcElement("description", *p.Description))
	}
	if pending["creator"] {
		emit(creatorElements(*p.Authors, epub3, indent))
	}
	if pending["subject"] {
		emit(subjectElements(*p.Subjects, indent))
	}
	if pending["series"] {
		emit(seriesElements(series, seriesIndex, hasIndex, epub3, indent))
	}

	// Reassemble: everything before the metadata children, the rebuilt
	// children, then everything from </metadata> onward.
	var out bytes.Buffer
	out.Grow(len(f.opfData) + b.Len())
	out.Write(f.opfData[:pkg.childrenStart])
	out.WriteString(b.String())
	out.WriteString(closingIndent(indent))
	out.Write(f.opfData[pkg.childrenEnd:])

	// Re-parse the result. A patch that produces an unparseable package
	// document must fail here, before it can reach the user's library.
	if _, err := parsePackage(out.Bytes()); err != nil {
		return nil, fmt.Errorf("metadata edit produced an invalid package document: %w", err)
	}
	return out.Bytes(), nil
}

// isSeriesElement reports whether a metadata child encodes series information
// in either the EPUB 3 or the legacy calibre form.
func isSeriesElement(c metaChild) bool {
	if !c.isMeta() {
		return false
	}
	switch c.attr("", "name") {
	case "calibre:series", "calibre:series_index":
		return true
	}
	if c.attr("", "property") == "belongs-to-collection" {
		return true
	}
	// Refinements of a collection element are dropped with it.
	switch c.attr("", "property") {
	case "collection-type", "group-position":
		return strings.HasPrefix(c.attr("", "refines"), "#")
	}
	return false
}

// detectIndent recovers the whitespace preceding the first metadata child so
// generated elements line up with the surrounding document.
func detectIndent(data []byte, pkg *pkgDoc) string {
	end := pkg.childrenStart
	if len(pkg.children) > 0 {
		end = pkg.children[0].start
	}
	if end > len(data) {
		return "\n    "
	}
	ws := data[pkg.childrenStart:end]
	if i := bytes.LastIndexByte(ws, '\n'); i >= 0 {
		return string(ws[i:])
	}
	return "\n    "
}

// closingIndent derives the indentation for the </metadata> line, one level
// out from the children.
func closingIndent(indent string) string {
	trimmed := strings.TrimSuffix(indent, "  ")
	if trimmed == indent {
		trimmed = strings.TrimSuffix(indent, "\t")
	}
	if trimmed == "" || !strings.HasPrefix(trimmed, "\n") {
		return "\n  "
	}
	return trimmed
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func dcElement(local, value string) string {
	return fmt.Sprintf("<dc:%s>%s</dc:%s>", local, esc(value), local)
}

// titleElement rewrites dc:title, preserving the element id when one exists so
// that unrelated refinements keep pointing at it.
func titleElement(title string, sort *string, existing metaChild, epub3 bool, indent string) string {
	id := existing.attr("", "id")
	if sort != nil && *sort != "" && id == "" {
		id = "title"
	}

	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "<dc:title id=%q>%s</dc:title>", id, esc(title))
	} else {
		b.WriteString(dcElement("title", title))
	}

	if sort != nil && *sort != "" {
		if epub3 {
			fmt.Fprintf(&b, "%s<meta refines=\"#%s\" property=\"file-as\">%s</meta>",
				indent, id, esc(*sort))
		} else {
			fmt.Fprintf(&b, "%s<meta name=\"calibre:title_sort\" content=%q/>", indent, esc(*sort))
		}
	}
	return b.String()
}

// creatorElements renders the full creator list, using EPUB 3 refinements or
// EPUB 2 opf: attributes as appropriate for the document version.
func creatorElements(authors []Creator, epub3 bool, indent string) string {
	var b strings.Builder
	for i, a := range authors {
		if a.Name == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(indent)
		}
		id := fmt.Sprintf("creator%02d", i+1)

		if epub3 {
			fmt.Fprintf(&b, "<dc:creator id=%q>%s</dc:creator>", id, esc(a.Name))
			if a.FileAs != "" {
				fmt.Fprintf(&b, "%s<meta refines=\"#%s\" property=\"file-as\">%s</meta>",
					indent, id, esc(a.FileAs))
			}
			fmt.Fprintf(&b, "%s<meta refines=\"#%s\" property=\"role\" scheme=\"marc:relators\">%s</meta>",
				indent, id, esc(orDefault(a.Role, "aut")))
			continue
		}

		b.WriteString("<dc:creator")
		if a.FileAs != "" {
			fmt.Fprintf(&b, " opf:file-as=%q", esc(a.FileAs))
		}
		fmt.Fprintf(&b, " opf:role=%q>%s</dc:creator>", esc(orDefault(a.Role, "aut")), esc(a.Name))
	}
	return b.String()
}

func subjectElements(subjects []string, indent string) string {
	var b strings.Builder
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(indent)
		}
		b.WriteString(dcElement("subject", s))
	}
	return b.String()
}

// seriesElements renders series metadata.
//
// EPUB 3 documents get the standard belongs-to-collection form. Both versions
// also get the calibre keys, because that is what the rest of the ecosystem
// actually reads, and a book that loses its series on a round trip through
// another tool is worse than a slightly redundant OPF.
func seriesElements(series string, index float64, hasIndex, epub3 bool, indent string) string {
	series = strings.TrimSpace(series)
	if series == "" {
		return "" // clearing the series removes the markers entirely
	}

	var b strings.Builder
	if epub3 {
		fmt.Fprintf(&b, "<meta property=\"belongs-to-collection\" id=\"series\">%s</meta>", esc(series))
		fmt.Fprintf(&b, "%s<meta refines=\"#series\" property=\"collection-type\">series</meta>", indent)
		if hasIndex {
			fmt.Fprintf(&b, "%s<meta refines=\"#series\" property=\"group-position\">%s</meta>",
				indent, formatIndex(index))
		}
		b.WriteString(indent)
	}

	fmt.Fprintf(&b, "<meta name=\"calibre:series\" content=%q/>", esc(series))
	if hasIndex {
		fmt.Fprintf(&b, "%s<meta name=\"calibre:series_index\" content=%q/>",
			indent, formatIndex(index))
	}
	return b.String()
}

// formatIndex renders a series index without a trailing ".0", since "1" is
// what every other tool writes for a whole-numbered entry.
func formatIndex(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
