package opds

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Atom and OPDS document types.
//
// These marshal to OPDS 1.2, which is the Atom-based generation. The firmware
// parses XML with Expat and has no JSON path at all, so OPDS 2.0 is not an
// option here — see compat.go.

const (
	nsAtom    = "http://www.w3.org/2005/Atom"
	nsOPDS    = "http://opds-spec.org/2010/catalog"
	nsDCTerms = "http://purl.org/dc/terms/"

	nsOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"
)

// Feed is an OPDS catalog feed.
//
// Only the Atom namespace is declared on the root. Fields in another namespace
// carry it on the element itself, which is what encoding/xml emits for a
// namespace-qualified tag: verbose, but correct without hand-managing prefixes.
type Feed struct {
	XMLName xml.Name `xml:"feed"`
	XMLNS   string   `xml:"xmlns,attr"`

	ID       string  `xml:"id"`
	Title    string  `xml:"title"`
	Updated  Time    `xml:"updated"`
	Author   *Person `xml:"author,omitempty"`
	Subtitle string  `xml:"subtitle,omitempty"`

	// Feed-level paging counters, read by standard clients. The firmware
	// ignores them and follows the next/previous links instead.
	TotalResults int `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults,omitempty"`
	ItemsPerPage int `xml:"http://a9.com/-/spec/opensearch/1.1/ itemsPerPage,omitempty"`
	StartIndex   int `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex,omitempty"`

	Links   []Link  `xml:"link"`
	Entries []Entry `xml:"entry"`
}

// Entry is one row in a feed: either a link to another feed or a book.
type Entry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated Time     `xml:"updated"`
	Authors []Person `xml:"author,omitempty"`

	// Content is what standard clients show under the title. The firmware
	// discards it.
	Content *Content `xml:"content,omitempty"`

	// Language, Issued, and Publisher are dcterms rather than plain Dublin
	// Core on purpose. The firmware matches element names by suffix, and
	// `strstr(name, ":id")` matches "dc:identifier" — emitting an identifier
	// element inside an entry would silently overwrite the entry's real <id>.
	// Nothing here ends up colliding, and no identifier element is emitted.
	Language  string `xml:"http://purl.org/dc/terms/ language,omitempty"`
	Issued    string `xml:"http://purl.org/dc/terms/ issued,omitempty"`
	Publisher string `xml:"http://purl.org/dc/terms/ publisher,omitempty"`

	Categories []Category `xml:"category,omitempty"`

	// Links are ordered acquisition-first for book entries. The firmware only
	// overwrites a navigation href with an acquisition one and not the reverse,
	// so the order is not load-bearing, but it keeps the intent obvious.
	Links []Link `xml:"link"`
}

// Link is an Atom link. Href is always an absolute path — see compat.go.
type Link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Type  string `xml:"type,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Title string `xml:"title,attr,omitempty"`
}

// Person is an Atom author.
type Person struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

// Content is an Atom content element.
type Content struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

// Category is an Atom category, used for tags.
type Category struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr,omitempty"`
}

// Time marshals as RFC 3339, which is what Atom requires.
type Time struct{ time.Time }

func (t Time) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(t.UTC().Format(time.RFC3339), start)
}

// newFeed builds a feed with the namespaces and the links every feed carries.
func newFeed(id, title string, updated time.Time, self string) *Feed {
	return &Feed{
		XMLNS:   nsAtom,
		ID:      truncateUTF8(id, MaxIDBytes),
		Title:   truncateUTF8(title, MaxTitleBytes),
		Updated: Time{updated},
		Author:  &Person{Name: "shelf"},
		Links: []Link{
			{Rel: "self", Type: NavigationType, Href: self},
			{Rel: "start", Type: NavigationType, Href: rootPath},
			// Two search links, and both are needed.
			//
			// Standard OPDS points rel="search" at an OpenSearch description
			// document and puts the template inside it. The firmware does not
			// fetch that document — it takes the href of any rel="search" link
			// that itself contains "{searchTerms}" and uses it as the template
			// directly. The two coexist safely because the firmware only
			// assigns when it sees the placeholder, so the description link
			// cannot clobber the template one.
			{Rel: "search", Type: "application/opensearchdescription+xml", Href: openSearchPath},
			{Rel: "search", Type: AcquisitionFeedType, Href: searchPath + "?q={searchTerms}"},
		},
	}
}

// Render writes a feed as a complete XML document.
func (f *Feed) Render() ([]byte, error) {
	body, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("opds: marshal feed: %w", err)
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// OpenSearchDescription is the document standard clients fetch to learn the
// search template. The firmware never asks for it.
type OpenSearchDescription struct {
	XMLName     xml.Name `xml:"OpenSearchDescription"`
	XMLNS       string   `xml:"xmlns,attr"`
	ShortName   string   `xml:"ShortName"`
	Description string   `xml:"Description"`
	InputEnc    string   `xml:"InputEncoding"`
	URLs        []struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
	} `xml:"Url"`
}

// truncateUTF8 cuts a string to at most n bytes without splitting a rune.
//
// The firmware bounds these fields by byte count and will cut a multi-byte
// character in half, which puts invalid UTF-8 into its string and renders as
// mojibake on the panel. Truncating here on a rune boundary means the device
// never has to. Anything cut gets an ellipsis so it reads as deliberate rather
// than as a corrupted title.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}

	const ellipsis = "…"
	limit := n - len(ellipsis)
	if limit <= 0 {
		return ""
	}

	// Back off to a rune boundary.
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return strings.TrimRight(s[:limit], " ") + ellipsis
}
