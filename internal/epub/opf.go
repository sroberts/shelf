package epub

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Metadata is the subset of OPF metadata shelf indexes and edits.
type Metadata struct {
	Title       string
	TitleSort   string
	Authors     []Creator
	Series      string
	SeriesIndex float64
	Language    string
	Publisher   string
	Date        string
	Description string
	Subjects    []string          // dc:subject, shelf's tags
	Identifiers map[string]string // scheme (lowercased) -> value
	Rights      string

	// HasSeriesIndex distinguishes "index 0" from "no index recorded".
	HasSeriesIndex bool
}

// Creator is a dc:creator with its sort form and MARC relator role.
type Creator struct {
	Name   string
	FileAs string
	Role   string // MARC relator, e.g. "aut"
}

// AuthorNames returns creator display names in document order.
func (m *Metadata) AuthorNames() []string {
	names := make([]string, 0, len(m.Authors))
	for _, a := range m.Authors {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return names
}

// AuthorSort returns the best available sortable author string. It prefers an
// explicit file-as, falls back to a "Last, First" guess, and returns "" when
// there is no creator at all.
func (m *Metadata) AuthorSort() string {
	for _, a := range m.Authors {
		if a.FileAs != "" {
			return a.FileAs
		}
	}
	for _, a := range m.Authors {
		if a.Name != "" {
			return guessFileAs(a.Name)
		}
	}
	return ""
}

// guessFileAs converts "Ursula K. Le Guin" to "Le Guin, Ursula K." well enough
// for sorting. It is a heuristic and only used when the OPF supplies no
// file-as; shelf never writes the guess back to the file.
func guessFileAs(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ",") {
		return name
	}
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return name
	}

	// Treat trailing generational suffixes as part of the surname.
	last := len(fields) - 1
	switch strings.ToLower(strings.Trim(fields[last], ".")) {
	case "jr", "sr", "ii", "iii", "iv":
		if last > 0 {
			last--
		}
	}
	// Absorb common surname particles: "Le Guin", "van Gogh", "de la Cruz".
	for last > 1 {
		switch strings.ToLower(fields[last-1]) {
		case "le", "la", "van", "von", "de", "del", "della", "der", "den", "di", "du", "da", "st.", "st":
			last--
		default:
			goto done
		}
	}
done:
	surname := strings.Join(fields[last:], " ")
	rest := strings.Join(fields[:last], " ")
	if rest == "" {
		return surname
	}
	return surname + ", " + rest
}

// pkgDoc is a parsed package document plus the byte offsets needed to rewrite
// it without disturbing anything shelf was not asked to change.
type pkgDoc struct {
	Version          string
	UniqueIdentifier string

	Manifest []ManifestItem
	CoverID  string // from the EPUB 2 <meta name="cover" content="..."> hint

	// Byte range of the <metadata> element's children, i.e. the span between
	// the end of the <metadata> start tag and the start of </metadata>.
	childrenStart int
	childrenEnd   int

	children []metaChild
}

// ManifestItem is one <item> in the OPF manifest.
type ManifestItem struct {
	ID         string
	Href       string
	MediaType  string
	Properties string
}

// hasProperty reports whether the item declares the given space-separated
// property token, e.g. "cover-image".
func (it ManifestItem) hasProperty(want string) bool {
	for _, p := range strings.Fields(it.Properties) {
		if p == want {
			return true
		}
	}
	return false
}

// metaChild is one direct child of <metadata>, retained with its source byte
// range so unmodified entries can be copied through verbatim on write.
type metaChild struct {
	name  xml.Name
	attrs []xml.Attr
	text  string
	start int
	end   int
}

func (c metaChild) attr(space, local string) string {
	for _, a := range c.attrs {
		if a.Name.Local == local && (space == "" || a.Name.Space == space || a.Name.Space == "") {
			return a.Value
		}
	}
	return ""
}

// isDC reports whether this child is the named Dublin Core element.
func (c metaChild) isDC(local string) bool {
	return c.name.Local == local && (c.name.Space == nsDC || c.name.Space == "")
}

// isMeta reports whether this child is an OPF <meta> element.
func (c metaChild) isMeta() bool {
	return c.name.Local == "meta" && (c.name.Space == nsOPF || c.name.Space == "")
}

// parsePackage decodes the OPF, recording byte ranges as it goes.
func parsePackage(data []byte) (*pkgDoc, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	pkg := &pkgDoc{childrenStart: -1}

	var (
		inMetadata bool
		inManifest bool
		prevOffset int64
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptOPF, err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "package":
				pkg.Version = attrValue(t.Attr, "version")
				pkg.UniqueIdentifier = attrValue(t.Attr, "unique-identifier")

			case t.Name.Local == "metadata" && !inManifest:
				inMetadata = true
				pkg.childrenStart = int(dec.InputOffset())

			case t.Name.Local == "manifest":
				inManifest = true

			case inManifest && t.Name.Local == "item":
				pkg.Manifest = append(pkg.Manifest, ManifestItem{
					ID:         attrValue(t.Attr, "id"),
					Href:       attrValue(t.Attr, "href"),
					MediaType:  attrValue(t.Attr, "media-type"),
					Properties: attrValue(t.Attr, "properties"),
				})

			case inMetadata:
				// A direct child of <metadata>. Capture it whole, including any
				// nested content, along with its exact source bytes.
				start := indexTagStart(data, int(prevOffset))
				text, err := collectElement(dec)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrCorruptOPF, err)
				}
				pkg.children = append(pkg.children, metaChild{
					name:  t.Name,
					attrs: t.Attr,
					text:  text,
					start: start,
					end:   int(dec.InputOffset()),
				})
			}

		case xml.EndElement:
			switch t.Name.Local {
			case "metadata":
				if inMetadata {
					inMetadata = false
					pkg.childrenEnd = indexTagStart(data, int(prevOffset))
				}
			case "manifest":
				inManifest = false
			}
		}
		prevOffset = dec.InputOffset()
	}

	if pkg.childrenStart < 0 {
		return nil, fmt.Errorf("%w: no <metadata> element", ErrCorruptOPF)
	}
	if pkg.childrenEnd < pkg.childrenStart {
		return nil, fmt.Errorf("%w: unterminated <metadata> element", ErrCorruptOPF)
	}

	for _, c := range pkg.children {
		if c.isMeta() && c.attr("", "name") == "cover" {
			pkg.CoverID = c.attr("", "content")
		}
	}
	return pkg, nil
}

// collectElement consumes tokens through the end of the element that was just
// opened, returning its top-level character data.
func collectElement(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 1 {
				sb.Write(t)
			}
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// indexTagStart finds the first '<' at or after from. The XML decoder reports
// the offset just past the previous token, which for an element start is the
// whitespace preceding the tag.
func indexTagStart(data []byte, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(data); i++ {
		if data[i] == '<' {
			return i
		}
	}
	return len(data)
}

func attrValue(attrs []xml.Attr, local string) string {
	for _, a := range attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// Metadata builds the metadata model from the parsed package document.
func (f *File) Metadata() *Metadata {
	m := &Metadata{Identifiers: map[string]string{}}
	pkg := f.pkg

	// EPUB 3 refinements target another element by id; collect them first so
	// they can be applied regardless of document order.
	refines := map[string][]metaChild{}
	for _, c := range pkg.children {
		if c.isMeta() {
			if id := strings.TrimPrefix(c.attr("", "refines"), "#"); id != "" {
				refines[id] = append(refines[id], c)
			}
		}
	}
	refinement := func(id, property string) string {
		for _, r := range refines[id] {
			if r.attr("", "property") == property {
				return r.text
			}
		}
		return ""
	}

	for _, c := range pkg.children {
		switch {
		case c.isDC("title"):
			if m.Title == "" {
				m.Title = c.text
				if id := c.attr("", "id"); id != "" {
					m.TitleSort = refinement(id, "file-as")
				}
			}

		case c.isDC("creator"):
			cr := Creator{Name: c.text}
			// EPUB 2 carries these as opf: attributes, EPUB 3 as refinements.
			cr.FileAs = c.attr(nsOPF, "file-as")
			cr.Role = c.attr(nsOPF, "role")
			if id := c.attr("", "id"); id != "" {
				if v := refinement(id, "file-as"); v != "" {
					cr.FileAs = v
				}
				if v := refinement(id, "role"); v != "" {
					cr.Role = v
				}
			}
			// Skip non-author creators when a role is declared; illustrators
			// and editors should not become the book's author.
			if cr.Role != "" && cr.Role != "aut" {
				continue
			}
			if cr.Name != "" {
				m.Authors = append(m.Authors, cr)
			}

		case c.isDC("language"):
			if m.Language == "" {
				m.Language = c.text
			}
		case c.isDC("publisher"):
			if m.Publisher == "" {
				m.Publisher = c.text
			}
		case c.isDC("date"):
			if m.Date == "" {
				m.Date = c.text
			}
		case c.isDC("description"):
			if m.Description == "" {
				m.Description = c.text
			}
		case c.isDC("rights"):
			if m.Rights == "" {
				m.Rights = c.text
			}
		case c.isDC("subject"):
			if s := strings.TrimSpace(c.text); s != "" {
				m.Subjects = append(m.Subjects, s)
			}
		case c.isDC("identifier"):
			scheme, value := parseIdentifier(c)
			if value != "" {
				if _, exists := m.Identifiers[scheme]; !exists {
					m.Identifiers[scheme] = value
				}
			}
		}
	}

	applySeries(m, pkg, refines)
	return m
}

// applySeries resolves series from EPUB 3 belongs-to-collection when present,
// falling back to the legacy calibre:series pair. EPUB 3 wins because it is the
// standard form; the calibre keys exist in most real libraries, so both are read.
func applySeries(m *Metadata, pkg *pkgDoc, refines map[string][]metaChild) {
	for _, c := range pkg.children {
		if !c.isMeta() || c.attr("", "property") != "belongs-to-collection" {
			continue
		}
		id := c.attr("", "id")

		// collection-type may be absent; absent means series by convention.
		ctype := ""
		pos := ""
		for _, r := range refines[id] {
			switch r.attr("", "property") {
			case "collection-type":
				ctype = r.text
			case "group-position":
				pos = r.text
			}
		}
		if ctype != "" && ctype != "series" {
			continue
		}

		m.Series = strings.TrimSpace(c.text)
		if v, err := strconv.ParseFloat(strings.TrimSpace(pos), 64); err == nil {
			m.SeriesIndex, m.HasSeriesIndex = v, true
		}
		if m.Series != "" {
			return
		}
	}

	for _, c := range pkg.children {
		if !c.isMeta() {
			continue
		}
		switch c.attr("", "name") {
		case "calibre:series":
			if m.Series == "" {
				m.Series = strings.TrimSpace(c.attr("", "content"))
			}
		case "calibre:series_index":
			if !m.HasSeriesIndex {
				if v, err := strconv.ParseFloat(strings.TrimSpace(c.attr("", "content")), 64); err == nil {
					m.SeriesIndex, m.HasSeriesIndex = v, true
				}
			}
		}
	}
}

// parseIdentifier extracts a (scheme, value) pair from a dc:identifier, which
// may declare its scheme via opf:scheme or embed it in a urn: value.
func parseIdentifier(c metaChild) (scheme, value string) {
	value = strings.TrimSpace(c.text)
	scheme = strings.ToLower(strings.TrimSpace(c.attr(nsOPF, "scheme")))

	if lower := strings.ToLower(value); strings.HasPrefix(lower, "urn:") {
		rest := value[len("urn:"):]
		if i := strings.Index(rest, ":"); i > 0 {
			if scheme == "" {
				scheme = strings.ToLower(rest[:i])
			}
			value = rest[i+1:]
		}
	}
	if scheme == "" {
		scheme = "unknown"
	}
	return scheme, value
}
