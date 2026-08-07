package opds

import "strings"

// The CrossPoint OPDS compatibility contract.
//
// Everything in this file is read off the firmware's own parser
// (lib/OpdsParser/OpdsParser.cpp) rather than the OPDS specification. Where the
// two disagree the firmware wins, because it is the client that has to render
// the feed. Each constant below names the code that forces it, so a future
// firmware bump can be checked against the source rather than guessed at.
//
// The parser is Expat-based and matches element names by suffix
// (`strstr(name, ":entry")`), so a namespace prefix is optional throughout. It
// reads exactly five things per entry: title, author name, id, and the href of
// an acquisition or navigation link. Everything else in the feed — summary,
// content, categories, cover images, publication dates — is parsed and
// discarded. They are still emitted here, because other OPDS clients do use
// them and they cost nothing on the wire that matters.

// MaxEntriesPerFeed is where the firmware stops collecting.
//
// OpdsParser.cpp reserves ENTRY_STORAGE_CAPACITY = 64 and sets
// MAX_ENTRIES = ENTRY_STORAGE_CAPACITY - 2. Past that, `collectCurrentEntry`
// goes false and every further entry is silently dropped — the feed is marked
// truncated, but the user just sees a short list. A page larger than this does
// not fail, it lies.
const MaxEntriesPerFeed = 62

// DefaultPageSize is what shelf actually paginates at.
//
// Deliberately below MaxEntriesPerFeed rather than equal to it. The browser
// activity prepends its own synthetic "previous page" navigation entry to the
// list it renders, and leaving headroom means a future firmware that prepends
// one more does not start dropping books.
const DefaultPageSize = 48

// Field limits, from the MAX_*_CHARS constants in OpdsParser.cpp.
//
// These are byte counts, not rune counts: the parser appends into a std::string
// with a byte bound and will happily cut a multi-byte character in half. shelf
// truncates on a rune boundary before that can happen, which is why these are
// used with truncateUTF8 rather than a plain slice.
const (
	MaxTitleBytes  = 160
	MaxAuthorBytes = 120
	MaxIDBytes     = 128
	MaxHrefBytes   = 768
)

// AcquisitionType is the only MIME type the firmware will download.
//
// The check is `strcmp(type, "application/epub+zip") == 0` — exact, not a
// prefix. A parameterised variant such as "application/epub+zip; charset=utf-8"
// does not match and the entry is not treated as a book. Other formats may
// still be advertised for other clients; CrossPoint ignores them.
const AcquisitionType = "application/epub+zip"

// NavigationType marks a link to another feed. The firmware tests with
// `strstr(type, "application/atom+xml")`, so the profile and kind parameters
// that standard clients want are safe to include.
const NavigationType = "application/atom+xml;profile=opds-catalog;kind=navigation"

// AcquisitionFeedType is the same for feeds that list books.
const AcquisitionFeedType = "application/atom+xml;profile=opds-catalog;kind=acquisition"

// RelPrevious is "previous", not "prev".
//
// The firmware compares with `strcmp(rel, "previous")`. RFC 5005 uses
// "previous" too, but "prev" is common enough in the wild that it is worth
// naming the constant rather than typing the string at each call site.
const RelPrevious = "previous"

// RelAcquisition is matched with `strstr(rel, "opds-spec.org/acquisition")`, so
// the sub-relations ("/open-access", "/buy") would match too. The plain
// relation is what a free local library wants.
const RelAcquisition = "http://opds-spec.org/acquisition"

// The firmware ignores these entirely; they are here for Panels, KOReader,
// Thorium, and anything else pointed at the same catalog.
const (
	RelImage     = "http://opds-spec.org/image"
	RelThumbnail = "http://opds-spec.org/image/thumbnail"
)

// prefersEpubHref reports whether an acquisition href will win the firmware's
// preference test when an entry carries more than one.
//
// OpdsParser.cpp prefers an href containing ".epub" or "/epub/" over one that
// does not, so that a catalog offering both a plain EPUB and some derived
// format lands on the plain one. shelf's acquisition paths end in ".epub"
// precisely to satisfy this; the helper exists so a test can assert it rather
// than the property being an accident of the URL layout.
func prefersEpubHref(href string) bool {
	return strings.Contains(href, ".epub") || strings.Contains(href, "/epub/")
}

// Why every href shelf emits is an absolute path.
//
// The firmware resolves links with UrlUtils::buildUrl, which is not RFC 3986
// relative resolution. Given a base of "http://host/opds/authors" and a
// relative href of "tolkien", it returns "http://host/opds/authors/tolkien" —
// it appends to the whole base rather than replacing the last segment, so a
// standard client resolving the same feed would fetch a different URL. The one
// form the firmware and RFC 3986 agree on is a path starting with "/", which
// buildUrl handles by joining it to the extracted host. Emitting anything else
// makes the catalog mean two different things depending on who reads it.
//
// Enforced by TestEveryHrefIsAnAbsolutePath.
