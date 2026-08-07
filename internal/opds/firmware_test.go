package opds

import (
	"encoding/xml"
	"io"
	"strings"
)

// A port of the CrossPoint firmware's OPDS parser.
//
// This mirrors lib/OpdsParser/OpdsParser.cpp rule for rule, including the parts
// that are wrong by the OPDS specification. It exists so the tests assert what
// the device will actually do with a feed rather than what the feed means: a
// change that keeps the XML valid but stops the reader seeing books fails here
// instead of on the hardware.
//
// Kept deliberately literal, matching by name suffix and comparing the same
// strings in the same order, so it can be diffed against the C++ when firmware
// moves. Where behaviour looked surprising it is quoted in a comment.

type fwEntryType int

const (
	fwNavigation fwEntryType = iota
	fwBook
)

type fwEntry struct {
	Type   fwEntryType
	Title  string
	Author string
	Href   string
	ID     string
}

type fwParseResult struct {
	Entries        []fwEntry
	SearchTemplate string
	NextPage       string
	PrevPage       string
	Truncated      bool
}

func (r fwParseResult) books() []fwEntry {
	var out []fwEntry
	for _, e := range r.Entries {
		if e.Type == fwBook {
			out = append(out, e)
		}
	}
	return out
}

// fwParse runs the firmware's parsing rules over a feed.
func fwParse(doc []byte) (fwParseResult, error) {
	var res fwParseResult
	dec := xml.NewDecoder(strings.NewReader(string(doc)))

	var (
		inEntry, collect          bool
		inTitle, inAuthor, inName bool
		inID                      bool
		current                   fwEntry
		text                      strings.Builder
	)

	// The firmware creates its Expat parser without a namespace separator, so
	// element names arrive as written and are matched with strcmp on the bare
	// name or strstr on ":name". Go resolves namespaces for us, so the local
	// name is the equivalent comparison.
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local

			switch {
			case name == "entry":
				inEntry = true
				// MAX_ENTRIES = ENTRY_STORAGE_CAPACITY - 2. Past it the entry
				// is parsed and thrown away, and the feed is flagged truncated.
				collect = len(res.Entries) < MaxEntriesPerFeed
				res.Truncated = res.Truncated || !collect
				current = fwEntry{}
				text.Reset()
				inTitle, inAuthor, inName, inID = false, false, false, false
				continue

			case name == "link":
				href := attr(t, "href")
				if href == "" {
					continue
				}
				rel := attr(t, "rel")
				typ := attr(t, "type")

				if rel == "search" {
					// Note: the template is taken from the href directly. The
					// firmware never fetches an OpenSearch description.
					if strings.Contains(href, "{searchTerms}") {
						res.SearchTemplate = href
					}
				} else if rel == "next" && !inEntry {
					res.NextPage = href
				} else if rel == "previous" && !inEntry {
					// "previous", not "prev".
					res.PrevPage = href
				}

				if inEntry && collect {
					if strings.Contains(rel, "opds-spec.org/acquisition") && typ == AcquisitionType {
						isPlain := strings.Contains(href, ".epub") || strings.Contains(href, "/epub/")
						hasPlain := current.Type == fwBook &&
							(strings.Contains(current.Href, ".epub") || strings.Contains(current.Href, "/epub/"))
						if current.Type != fwBook || (isPlain && !hasPlain) {
							current.Type = fwBook
							current.Href = href
						}
					} else if strings.Contains(typ, "application/atom+xml") {
						if current.Type != fwBook {
							current.Type = fwNavigation
							current.Href = href
						}
					}
				}
				continue
			}

			if !inEntry || !collect {
				continue
			}

			switch {
			case name == "title":
				inTitle = true
				text.Reset()
			case name == "author":
				inAuthor = true
			case inAuthor && name == "name":
				inName = true
				text.Reset()
			case name == "id":
				inID = true
				text.Reset()
			}

		case xml.CharData:
			if !collect {
				continue
			}
			if inTitle || inName || inID {
				text.Write(t)
			}

		case xml.EndElement:
			name := t.Name.Local

			if name == "entry" {
				// An entry with no title or no href is dropped silently.
				if collect && current.Title != "" && current.Href != "" {
					res.Entries = append(res.Entries, current)
				}
				inEntry, collect = false, false
				continue
			}
			if !inEntry {
				continue
			}
			switch {
			case name == "title":
				if inTitle {
					current.Title = text.String()
				}
				inTitle = false
			case name == "author":
				inAuthor = false
			case inName && name == "name":
				current.Author = text.String()
				inName = false
			case name == "id":
				if inID {
					current.ID = text.String()
				}
				inID = false
			}
		}
	}

	return res, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
