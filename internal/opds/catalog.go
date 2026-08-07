package opds

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/library"
)

// Catalog paths.
//
// Every one is an absolute path, and every href in a feed is built from these.
// See compat.go for why relative hrefs are not an option.
const (
	rootPath       = "/opds"
	allPath        = "/opds/all"
	recentPath     = "/opds/recent"
	authorsPath    = "/opds/authors"
	seriesPath     = "/opds/series"
	tagsPath       = "/opds/tags"
	shelvesPath    = "/opds/shelves"
	searchPath     = "/opds/search"
	openSearchPath = "/opds/opensearch.xml"

	bookPrefix  = "/opds/book/"
	coverPrefix = "/opds/cover/"
	thumbPrefix = "/opds/thumb/"
)

// Catalog builds feeds from a library index.
//
// It holds no state beyond the database and the page size, so a handler can
// call it concurrently. Every method returns a fully-built feed; nothing is
// written to the wire here, which keeps the feed shape testable without an
// HTTP server.
type Catalog struct {
	db *library.DB

	// PageSize is how many entries a feed carries. Clamped to the firmware's
	// limit in NewCatalog, because a larger page silently loses books.
	PageSize int

	// Title names the catalog in every feed and in the reader's server list.
	Title string

	// now is injected so feed timestamps are deterministic under test.
	now func() time.Time
}

// NewCatalog builds a catalog over a library.
func NewCatalog(db *library.DB, title string) *Catalog {
	if title == "" {
		title = "shelf"
	}
	return &Catalog{db: db, PageSize: DefaultPageSize, Title: title, now: time.Now}
}

// pageSize returns the effective page size, never above what the firmware will
// read. A caller that sets PageSize too high gets a smaller page rather than a
// truncated one: dropping books silently is the failure worth preventing.
func (c *Catalog) pageSize() int {
	if c.PageSize <= 0 {
		return DefaultPageSize
	}
	if c.PageSize > MaxEntriesPerFeed {
		return MaxEntriesPerFeed
	}
	return c.PageSize
}

// Root is the navigation feed the reader lands on.
func (c *Catalog) Root() (*Feed, error) {
	total, err := c.db.Count()
	if err != nil {
		return nil, err
	}
	tags, err := c.db.Tags()
	if err != nil {
		return nil, err
	}
	authors, err := c.db.Authors()
	if err != nil {
		return nil, err
	}
	series, err := c.db.Series()
	if err != nil {
		return nil, err
	}
	shelves, err := c.db.ShelfNames()
	if err != nil {
		return nil, err
	}

	feed := newFeed("urn:shelf:catalog", c.Title, c.now(), rootPath)
	feed.Subtitle = plural(total, "book", "books")

	sections := []struct {
		title string
		href  string
		count int
		unit  [2]string
		kind  string
	}{
		{"All Books", allPath, total, [2]string{"book", "books"}, AcquisitionFeedType},
		{"Recently Added", recentPath, min(total, c.pageSize()), [2]string{"book", "books"}, AcquisitionFeedType},
		{"By Author", authorsPath, len(authors), [2]string{"author", "authors"}, NavigationType},
		{"By Series", seriesPath, len(series), [2]string{"series", "series"}, NavigationType},
		{"By Tag", tagsPath, len(tags), [2]string{"tag", "tags"}, NavigationType},
		{"Shelves", shelvesPath, len(shelves), [2]string{"shelf", "shelves"}, NavigationType},
	}

	for _, s := range sections {
		// An empty section is left out rather than shown as a dead end. On a
		// device where every navigation step is a page redraw over Wi-Fi, a
		// row that leads to "no entries" is worse than no row.
		if s.count == 0 {
			continue
		}
		feed.Entries = append(feed.Entries, Entry{
			Title:   s.title,
			ID:      "urn:shelf:nav:" + slugID(s.title),
			Updated: Time{c.now()},
			Content: &Content{Type: "text", Body: plural(s.count, s.unit[0], s.unit[1])},
			Links:   []Link{{Rel: "subsection", Type: s.kind, Href: s.href}},
		})
	}

	return feed, nil
}

// BookFeed describes an acquisition feed to build.
type BookFeed struct {
	ID    string
	Title string
	Self  string

	// Query is shelf's own search syntax, and Filter is an exact-match
	// narrowing. Browse dimensions use Filter rather than folding the value
	// into Query, because an author name can contain quotes that the query
	// lexer cannot escape.
	Query  string
	Filter *library.Filter

	OrderBy string
	Page    int
}

// Books is a paginated acquisition feed.
//
// Everything that lists books goes through here, so the OPDS catalog cannot
// drift from what `shelf ls` returns for the same filter.
func (c *Catalog) Books(f BookFeed) (*Feed, error) {
	size := c.pageSize()
	page := f.Page
	if page < 0 {
		page = 0
	}

	// One extra row is fetched to learn whether a next page exists, which is
	// cheaper than a second COUNT over the same predicate.
	books, err := c.db.Search(f.Query, library.SearchOptions{
		Limit:   size + 1,
		Offset:  page * size,
		OrderBy: f.OrderBy,
		Filter:  f.Filter,
	})
	if err != nil {
		return nil, err
	}
	id, title, self := f.ID, f.Title, f.Self

	hasNext := len(books) > size
	if hasNext {
		books = books[:size]
	}

	feed := newFeed(id, title, c.now(), pageHref(self, page))
	feed.ItemsPerPage = size
	feed.StartIndex = page*size + 1

	for _, b := range books {
		feed.Entries = append(feed.Entries, c.bookEntry(b))
		if u := time.Unix(b.MTimeUnix, 0); u.After(feed.Updated.Time) {
			feed.Updated = Time{u}
		}
	}

	if hasNext {
		feed.Links = append(feed.Links, Link{
			Rel: "next", Type: AcquisitionFeedType, Href: pageHref(self, page+1),
		})
	}
	if page > 0 {
		feed.Links = append(feed.Links, Link{
			Rel: RelPrevious, Type: AcquisitionFeedType, Href: pageHref(self, page-1),
		})
	}

	return feed, nil
}

// Shelves is the navigation feed listing every shelf.
func (c *Catalog) Shelves() (*Feed, error) {
	shelves, err := c.db.ListShelves()
	if err != nil {
		return nil, err
	}

	groups := make([]library.GroupCount, 0, len(shelves))
	for _, s := range shelves {
		// A query shelf is re-evaluated on every use, so its size is only
		// knowable by running it. Shelves are few and this feed is one
		// navigation step, which makes the extra queries affordable — but it
		// is the reason this does not scale the way the other dimensions do.
		books, err := c.db.ShelfBooks(s.Name)
		if err != nil {
			return nil, err
		}
		groups = append(groups, library.GroupCount{Value: s.Name, Count: len(books)})
	}

	return c.Groups("urn:shelf:shelves", "Shelves", shelvesPath, groups,
		shelvesPath+"/", "book", "books", 0), nil
}

// ShelfBooks is a paginated acquisition feed for one shelf.
//
// Paginated in memory rather than in SQL: a shelf is either a saved query,
// whose membership is only known once it has been run, or an explicit list.
// Neither composes with the LIMIT/OFFSET path without re-running the query per
// page, and shelves are small enough that it does not matter.
func (c *Catalog) ShelfBooks(name string, page int) (*Feed, error) {
	books, err := c.db.ShelfBooks(name)
	if err != nil {
		return nil, err
	}

	size := c.pageSize()
	if page < 0 {
		page = 0
	}
	self := shelvesPath + "/" + url.PathEscape(name)

	start := min(page*size, len(books))
	end := min(start+size, len(books))

	feed := newFeed("urn:shelf:shelf:"+slugID(name), name, c.now(), pageHref(self, page))
	feed.TotalResults = len(books)
	feed.ItemsPerPage = size
	feed.StartIndex = start + 1

	for _, b := range books[start:end] {
		feed.Entries = append(feed.Entries, c.bookEntry(b))
	}

	if end < len(books) {
		feed.Links = append(feed.Links, Link{
			Rel: "next", Type: AcquisitionFeedType, Href: pageHref(self, page+1),
		})
	}
	if page > 0 {
		feed.Links = append(feed.Links, Link{
			Rel: RelPrevious, Type: AcquisitionFeedType, Href: pageHref(self, page-1),
		})
	}

	return feed, nil
}

// Groups is a paginated navigation feed over a browse dimension.
//
// Author and tag lists outgrow one page as readily as a book list does, and the
// firmware drops what it cannot hold, so these paginate on exactly the same
// terms.
func (c *Catalog) Groups(id, title, self string, groups []library.GroupCount, childPrefix, unit, units string, page int) *Feed {
	size := c.pageSize()
	if page < 0 {
		page = 0
	}

	start := min(page*size, len(groups))
	end := min(start+size, len(groups))
	window := groups[start:end]

	feed := newFeed(id, title, c.now(), pageHref(self, page))
	feed.TotalResults = len(groups)
	feed.ItemsPerPage = size
	feed.StartIndex = start + 1

	for _, g := range window {
		feed.Entries = append(feed.Entries, Entry{
			Title:   g.Value,
			ID:      "urn:shelf:group:" + slugID(childPrefix+g.Value),
			Updated: Time{c.now()},
			Content: &Content{Type: "text", Body: plural(g.Count, unit, units)},
			Links: []Link{{
				Rel:  "subsection",
				Type: AcquisitionFeedType,
				Href: childPrefix + url.PathEscape(g.Value),
			}},
		})
	}

	if end < len(groups) {
		feed.Links = append(feed.Links, Link{
			Rel: "next", Type: NavigationType, Href: pageHref(self, page+1),
		})
	}
	if page > 0 {
		feed.Links = append(feed.Links, Link{
			Rel: RelPrevious, Type: NavigationType, Href: pageHref(self, page-1),
		})
	}

	return feed
}

// bookEntry renders one book.
//
// The acquisition link comes first and is the only one the firmware reads. The
// cover, category, and dcterms elements are for everything else pointed at this
// catalog; CrossPoint parses and discards them.
func (c *Catalog) bookEntry(b *library.Book) Entry {
	e := Entry{
		Title:   truncateUTF8(b.DisplayTitle(), MaxTitleBytes),
		ID:      "urn:shelf:book:" + b.SHA256,
		Updated: Time{time.Unix(b.MTimeUnix, 0)},

		Language:  b.Language,
		Issued:    b.PubDate,
		Publisher: b.Publisher,
	}

	for _, a := range b.Authors {
		e.Authors = append(e.Authors, Person{Name: truncateUTF8(a, MaxAuthorBytes)})
	}
	if len(e.Authors) == 0 && b.AuthorSort != "" {
		e.Authors = []Person{{Name: truncateUTF8(b.AuthorSort, MaxAuthorBytes)}}
	}

	for _, t := range b.Tags {
		e.Categories = append(e.Categories, Category{Term: t, Label: t})
	}

	if summary := bookSummary(b); summary != "" {
		e.Content = &Content{Type: "text", Body: summary}
	}

	e.Links = append(e.Links, Link{
		Rel:  RelAcquisition,
		Type: mimeFor(b.Format),
		Href: acquisitionHref(b),
	})

	if len(b.Cover) > 0 {
		e.Links = append(e.Links,
			Link{Rel: RelImage, Type: "image/png", Href: coverPrefix + b.SHA256},
			Link{Rel: RelThumbnail, Type: "image/png", Href: thumbPrefix + b.SHA256},
		)
	}

	return e
}

// acquisitionHref is where the bytes live.
//
// The extension is the real format's, which matters twice: the firmware prefers
// an href containing ".epub" when an entry offers several, and a client that
// saves by URL gets a file its reader will open.
func acquisitionHref(b *library.Book) string {
	return bookPrefix + b.SHA256 + extFor(b.Format)
}

// mimeFor maps a library format to what the acquisition link advertises.
//
// Only EPUB is downloadable on CrossPoint: the firmware tests the type with an
// exact strcmp against "application/epub+zip", so a TXT or PDF entry is parsed,
// found to have no matching acquisition link, and dropped from the list. The
// other formats are still advertised honestly, because Panels, KOReader, and
// Thorium can all take them.
func mimeFor(f library.Format) string {
	switch f {
	case library.FormatEPUB:
		return AcquisitionType
	case library.FormatPDF:
		return "application/pdf"
	case library.FormatTXT:
		return "text/plain; charset=utf-8"
	default:
		// XTC is Xteink's own container. There is no registered type for it and
		// the firmware would not accept one over OPDS anyway, so it is offered
		// as an opaque download for completeness.
		return "application/octet-stream"
	}
}

func extFor(f library.Format) string {
	switch f {
	case library.FormatEPUB:
		return ".epub"
	case library.FormatPDF:
		return ".pdf"
	case library.FormatTXT:
		return ".txt"
	case library.FormatXTC:
		return ".xtc"
	default:
		return ""
	}
}

// bookSummary is the one line a client shows under the title.
func bookSummary(b *library.Book) string {
	var parts []string
	if b.Series != "" {
		if b.SeriesIndex > 0 {
			parts = append(parts, fmt.Sprintf("%s #%s", b.Series, trimFloat(b.SeriesIndex)))
		} else {
			parts = append(parts, b.Series)
		}
	}
	if b.Format != library.FormatEPUB {
		// Worth saying plainly: on CrossPoint these entries are invisible, and
		// on other clients they download something other than an EPUB.
		parts = append(parts, strings.ToUpper(string(b.Format)))
	}
	return strings.Join(parts, " · ")
}

// pageHref appends a page number only when there is one to append, so the first
// page of a feed has the same URL whether it was reached from the root or from
// a "previous" link. Two URLs for one page would give a client two cache
// entries and the reader two history steps.
func pageHref(base string, page int) string {
	if page <= 0 {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%spage=%d", base, sep, page)
}

// slugID makes a stable, bounded id fragment out of arbitrary text.
//
// Atom ids must be valid URIs, and an author name can hold anything at all, so
// the text is percent-escaped rather than passed through.
func slugID(s string) string {
	return truncateUTF8(url.PathEscape(strings.ToLower(s)), MaxIDBytes/2)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// trimFloat renders a series index without a pointless ".0".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// pathValue recovers a browse value from a URL segment.
//
// Deliberately does no unescaping. http.ServeMux already percent-decodes what
// it hands to Request.PathValue, and decoding a second time turns an author
// literally named "100% Cotton" into an invalid escape sequence and a 400 —
// the segment arrives as "100% Cotton", and "% C" is not a valid escape.
//
// Nor is the result path-cleaned. It is used only as a bound SQL parameter and
// as input to url.PathEscape when building the next href, never as a
// filesystem path, so there is no traversal to defend against and cleaning
// would only corrupt legitimate names.
func pathValue(s string) string {
	return strings.TrimSpace(s)
}
