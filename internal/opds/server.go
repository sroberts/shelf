package opds

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/library"
)

// PasswordChecker verifies a plaintext password.
//
// An interface rather than a concrete store so this package does not depend on
// kosync; *kosync.Store satisfies it, which is what makes one account work for
// both the progress server and the catalog.
type PasswordChecker interface {
	CheckPassword(username, password string) error
}

// Server serves a library as an OPDS 1.2 catalog.
type Server struct {
	catalog *Catalog
	log     *slog.Logger

	// Auth gates every endpoint when set. Left nil the catalog is open, which
	// is a deliberate choice a caller has to make rather than a default.
	Auth PasswordChecker

	// Realm names the catalog in the browser's credential prompt.
	Realm string
}

// NewServer builds a server over a catalog.
func NewServer(catalog *Catalog, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{catalog: catalog, log: log, Realm: catalog.Title}
}

// Handler returns the OPDS routes.
//
// Mounted alongside kosync on one listener so a reader needs a single address
// for both progress sync and browsing.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /opds", s.feed(s.handleRoot))
	mux.HandleFunc("GET /opds/", s.feed(s.handleRoot))
	mux.HandleFunc("GET /opds/all", s.feed(s.handleAll))
	mux.HandleFunc("GET /opds/recent", s.feed(s.handleRecent))
	mux.HandleFunc("GET /opds/search", s.feed(s.handleSearch))

	mux.HandleFunc("GET /opds/authors", s.feed(s.handleAuthors))
	mux.HandleFunc("GET /opds/authors/{value}", s.feed(s.handleAuthor))
	mux.HandleFunc("GET /opds/series", s.feed(s.handleSeries))
	mux.HandleFunc("GET /opds/series/{value}", s.feed(s.handleOneSeries))
	mux.HandleFunc("GET /opds/tags", s.feed(s.handleTags))
	mux.HandleFunc("GET /opds/tags/{value}", s.feed(s.handleTag))
	mux.HandleFunc("GET /opds/shelves", s.feed(s.handleShelves))
	mux.HandleFunc("GET /opds/shelves/{value}", s.feed(s.handleShelf))

	mux.HandleFunc("GET /opds/opensearch.xml", s.authed(s.handleOpenSearch))
	mux.HandleFunc("GET /opds/book/{name}", s.authed(s.handleAcquire))
	mux.HandleFunc("GET /opds/cover/{sum}", s.authed(s.handleCover))
	mux.HandleFunc("GET /opds/thumb/{sum}", s.authed(s.handleCover))

	return mux
}

// feedFunc builds a feed, or fails.
type feedFunc func(*http.Request) (*Feed, error)

// feed wraps a feed builder with authentication, rendering, and error mapping.
func (s *Server) feed(fn feedFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		f, err := fn(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}

		body, err := f.Render()
		if err != nil {
			s.fail(w, r, err)
			return
		}

		// Content type distinguishes navigation from acquisition, which is how
		// standard clients decide whether to render a list of links or a
		// bookshelf. The firmware ignores the header and looks at the entries.
		kind := NavigationType
		if hasBooks(f) {
			kind = AcquisitionFeedType
		}
		w.Header().Set("Content-Type", kind)
		w.Write(body)
	})
}

// authed requires HTTP Basic credentials when Auth is set.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Auth == nil {
			next(w, r)
			return
		}

		username, password, ok := r.BasicAuth()
		if !ok {
			s.challenge(w)
			return
		}
		if err := s.Auth.CheckPassword(username, password); err != nil {
			// Same log-versus-response split as kosync: the operator needs the
			// attempted username, the client gets nothing to enumerate with.
			s.log.Info("opds authentication failed",
				"username", username, "remote", r.RemoteAddr, "err", err)
			s.challenge(w)
			return
		}
		next(w, r)
	}
}

// challenge asks for credentials.
//
// The firmware sends Basic preemptively and never sees this, but a browser
// pointed at the catalog needs the header to prompt, and that is the fastest
// way to check a catalog is reachable before setting the reader up.
func (s *Server) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+strings.ReplaceAll(s.Realm, `"`, "")+`", charset="UTF-8"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *Server) handleRoot(*http.Request) (*Feed, error) { return s.catalog.Root() }

func (s *Server) handleAll(r *http.Request) (*Feed, error) {
	return s.catalog.Books(BookFeed{
		ID: "urn:shelf:all", Title: "All Books", Self: allPath, Page: page(r),
	})
}

func (s *Server) handleRecent(r *http.Request) (*Feed, error) {
	return s.catalog.Books(BookFeed{
		ID: "urn:shelf:recent", Title: "Recently Added", Self: recentPath,
		OrderBy: "added-desc", Page: page(r),
	})
}

func (s *Server) handleSearch(r *http.Request) (*Feed, error) {
	// Both spellings: OpenSearch templates conventionally use q, and some
	// clients send the kosync-era "query" instead. Costs one line.
	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("query")
	}

	title := "Search"
	if q != "" {
		title = "Search: " + q
	}
	return s.catalog.Books(BookFeed{
		ID:    "urn:shelf:search",
		Title: title,
		// The query is echoed back into the self link so paging through
		// results keeps the search applied.
		Self:  searchPath + "?q=" + url.QueryEscape(q),
		Query: q,
		Page:  page(r),
	})
}

func (s *Server) handleAuthors(r *http.Request) (*Feed, error) {
	groups, err := s.catalog.db.Authors()
	if err != nil {
		return nil, err
	}
	return s.catalog.Groups("urn:shelf:authors", "By Author", authorsPath,
		groups, authorsPath+"/", "book", "books", page(r)), nil
}

func (s *Server) handleSeries(r *http.Request) (*Feed, error) {
	groups, err := s.catalog.db.Series()
	if err != nil {
		return nil, err
	}
	return s.catalog.Groups("urn:shelf:series", "By Series", seriesPath,
		groups, seriesPath+"/", "book", "books", page(r)), nil
}

func (s *Server) handleTags(r *http.Request) (*Feed, error) {
	tags, err := s.catalog.db.Tags()
	if err != nil {
		return nil, err
	}
	groups := make([]library.GroupCount, 0, len(tags))
	for _, t := range tags {
		groups = append(groups, library.GroupCount{Value: t.Tag, Count: t.Count})
	}
	return s.catalog.Groups("urn:shelf:tags", "By Tag", tagsPath,
		groups, tagsPath+"/", "book", "books", page(r)), nil
}

func (s *Server) handleAuthor(r *http.Request) (*Feed, error) {
	return s.filtered(r, library.FilterAuthor, authorsPath, "series")
}

func (s *Server) handleOneSeries(r *http.Request) (*Feed, error) {
	return s.filtered(r, library.FilterSeries, seriesPath, "series")
}

func (s *Server) handleTag(r *http.Request) (*Feed, error) {
	return s.filtered(r, library.FilterTag, tagsPath, "")
}

// filtered builds the acquisition feed for one value of a browse dimension.
func (s *Server) filtered(r *http.Request, field library.FilterField, base, orderBy string) (*Feed, error) {
	value := pathValue(r.PathValue("value"))
	if value == "" {
		return nil, errNotFound
	}

	self := base + "/" + url.PathEscape(value)
	return s.catalog.Books(BookFeed{
		ID:      "urn:shelf:" + string(field) + ":" + slugID(value),
		Title:   value,
		Self:    self,
		Filter:  &library.Filter{Field: field, Value: value},
		OrderBy: orderBy,
		Page:    page(r),
	})
}

func (s *Server) handleShelves(*http.Request) (*Feed, error) { return s.catalog.Shelves() }

func (s *Server) handleShelf(r *http.Request) (*Feed, error) {
	name := pathValue(r.PathValue("value"))
	if name == "" {
		return nil, errNotFound
	}
	return s.catalog.ShelfBooks(name, page(r))
}

func (s *Server) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	doc := OpenSearchDescription{
		XMLNS:       nsOpenSearch,
		ShortName:   s.catalog.Title,
		Description: "Search the " + s.catalog.Title + " library",
		InputEnc:    "UTF-8",
	}
	doc.URLs = append(doc.URLs, struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
	}{Type: AcquisitionFeedType, Template: searchPath + "?q={searchTerms}"})

	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/opensearchdescription+xml")
	w.Write([]byte(xml.Header))
	w.Write(body)
}

// handleAcquire streams a book's bytes.
//
// The name in the path is the content hash plus the format's extension. The
// hash is what identifies the book — the extension is decoration for clients
// that save by URL, and for the firmware's preference for an href containing
// ".epub". The file path comes from the index and never from the request, so
// there is nothing here to traverse with.
func (s *Server) handleAcquire(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}

	book, err := s.lookup(name)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	f, err := os.Open(book.Path)
	if err != nil {
		// The index is a cache of what is on disk and can be out of date. A
		// book that has been moved or deleted since the last scan is a 404,
		// not a server error — the catalog is stale, nothing is broken.
		s.log.Warn("opds: book missing from disk", "path", book.Path, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Clear the server's write deadline for this response only.
	//
	// The listener runs a bounded WriteTimeout so a stalled feed request cannot
	// hold a connection forever, but a book is not a feed: a 40 MB EPUB to a
	// device on 2.4 GHz Wi-Fi takes minutes, and the timeout would cut it off
	// partway with no error the client can distinguish from a network drop.
	// Feeds keep the deadline; only the download escapes it.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		s.log.Debug("opds: could not clear write deadline", "err", err)
	}

	w.Header().Set("Content-Type", mimeFor(book.Format))
	w.Header().Set("Content-Disposition", contentDisposition(book))
	// ServeContent handles range requests and conditional gets, which is what
	// lets a download resume. The firmware does not resume, but every other
	// client does.
	http.ServeContent(w, r, book.Path, info.ModTime(), f)
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	book, err := s.lookup(r.PathValue("sum"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(book.Cover) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Covers are extracted at scan time and stored in the index already
	// downscaled, so cover and thumbnail are the same bytes. Splitting them
	// would mean a second decode per request to save a few kilobytes on a LAN.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "cover.png", time.Unix(book.MTimeUnix, 0),
		bytes.NewReader(book.Cover))
}

// lookup resolves a content hash to a book.
func (s *Server) lookup(sum string) (*library.Book, error) {
	if sum == "" {
		return nil, errNotFound
	}
	books, err := s.catalog.db.BySHA256(sum)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, errNotFound
	}
	// Duplicate files share a hash and are the same bytes by definition, so
	// any of them serves.
	return books[0], nil
}

var (
	errNotFound   = errors.New("opds: not found")
	errBadRequest = errors.New("opds: bad request")
)

// fail maps an error to a status, logging only the ones that are shelf's fault.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errBadRequest):
		http.Error(w, "bad request", http.StatusBadRequest)
	default:
		s.log.Error("opds request failed", "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// hasBooks reports whether a feed lists books rather than sub-catalogs.
func hasBooks(f *Feed) bool {
	for _, e := range f.Entries {
		for _, l := range e.Links {
			if strings.Contains(l.Rel, "opds-spec.org/acquisition") {
				return true
			}
		}
	}
	return false
}

// page reads the page number, treating anything unparseable as the first page.
// A bad page number in a URL is not worth an error screen on an e-ink panel.
func page(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// contentDisposition suggests a filename in Author - Title form.
//
// This matches what the firmware would name the file itself, so a book fetched
// over OPDS and the same book fetched by another client land on the same name.
func contentDisposition(b *library.Book) string {
	name := b.DisplayTitle()
	if a := b.DisplayAuthor(); a != "" {
		name = a + " - " + name
	}
	name = library.SanitizeComponent(name) + extFor(b.Format)
	return `attachment; filename*=UTF-8''` + url.PathEscape(name)
}
