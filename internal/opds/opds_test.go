package opds

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/library"
)

// fixedTime keeps rendered feeds deterministic.
var fixedTime = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

func newCatalog(t *testing.T, books ...*library.Book) *Catalog {
	t.Helper()

	db, err := library.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, b := range books {
		if err := db.Upsert(b); err != nil {
			t.Fatalf("upsert %q: %v", b.Title, err)
		}
	}

	c := NewCatalog(db, "Test Library")
	c.now = func() time.Time { return fixedTime }
	return c
}

func book(n int) *library.Book {
	return &library.Book{
		SHA256:    fmt.Sprintf("%064x", n),
		Path:      fmt.Sprintf("/lib/book-%03d.epub", n),
		Size:      1024,
		MTimeUnix: fixedTime.Unix(),
		AddedUnix: fixedTime.Unix(),
		Format:    library.FormatEPUB,
		Title:     fmt.Sprintf("Book %03d", n),
		AuthorSort: fmt.Sprintf("Author, %c",
			rune('A'+n%3)),
		Authors: []string{fmt.Sprintf("%c Author", rune('A'+n%3))},
	}
}

func books(n int) []*library.Book {
	out := make([]*library.Book, 0, n)
	for i := range n {
		out = append(out, book(i))
	}
	return out
}

func render(t *testing.T, f *Feed) []byte {
	t.Helper()
	body, err := f.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return body
}

// The single most destructive way to get this wrong: a page larger than the
// firmware's storage means books past the limit vanish with no error anywhere.
func TestPageSizeNeverExceedsWhatTheFirmwareReads(t *testing.T) {
	c := newCatalog(t, books(200)...)
	c.PageSize = 500 // a caller asking for more than the device can hold

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}
	if len(feed.Entries) > MaxEntriesPerFeed {
		t.Fatalf("feed carries %d entries, firmware reads at most %d",
			len(feed.Entries), MaxEntriesPerFeed)
	}

	res, err := fwParse(render(t, feed))
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	if res.Truncated {
		t.Error("firmware would truncate this feed")
	}
	if len(res.books()) != len(feed.Entries) {
		t.Errorf("firmware saw %d books, feed had %d", len(res.books()), len(feed.Entries))
	}
}

func TestFirmwareSeesEveryBookAcrossPages(t *testing.T) {
	const total = 120
	c := newCatalog(t, books(total)...)

	seen := map[string]bool{}
	next := allPath
	for page := 0; next != "" && page < 20; page++ {
		feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath, Page: page})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}

		res, err := fwParse(render(t, feed))
		if err != nil {
			t.Fatalf("page %d parse: %v", page, err)
		}
		for _, b := range res.books() {
			if seen[b.Href] {
				t.Errorf("page %d repeated %s", page, b.Href)
			}
			seen[b.Href] = true
		}
		next = res.NextPage
	}

	if len(seen) != total {
		t.Errorf("firmware reached %d of %d books by following next links", len(seen), total)
	}
	if next != "" {
		t.Error("paging did not terminate")
	}
}

// The firmware drops any entry missing a title or an href, and does it
// silently. A book with no metadata at all still has to survive.
func TestBookWithNoMetadataStillReachesTheDevice(t *testing.T) {
	bare := &library.Book{
		SHA256: strings.Repeat("a", 64),
		Path:   "/lib/untitled.epub",
		Format: library.FormatEPUB,
	}
	c := newCatalog(t, bare)

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	res, err := fwParse(render(t, feed))
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	got := res.books()
	if len(got) != 1 {
		t.Fatalf("firmware saw %d books, want 1 (title or href was empty)", len(got))
	}
	if got[0].Title == "" {
		t.Error("entry has no title; the firmware would have dropped it")
	}
}

func TestAcquisitionTypeIsExact(t *testing.T) {
	// strcmp, not a prefix match: a parameterised type is not recognised.
	if got := mimeFor(library.FormatEPUB); got != "application/epub+zip" {
		t.Errorf("epub acquisition type is %q", got)
	}

	c := newCatalog(t, book(1))
	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	var found bool
	for _, l := range feed.Entries[0].Links {
		if l.Rel == RelAcquisition {
			found = true
			if l.Type != AcquisitionType {
				t.Errorf("acquisition link type %q, want %q", l.Type, AcquisitionType)
			}
			if !prefersEpubHref(l.Href) {
				t.Errorf("href %q does not satisfy the firmware's .epub preference", l.Href)
			}
		}
	}
	if !found {
		t.Error("no acquisition link")
	}
}

// Relative hrefs resolve differently under the firmware's UrlUtils::buildUrl
// than under RFC 3986, so the catalog would mean two things at once.
func TestEveryHrefIsAnAbsolutePath(t *testing.T) {
	c := newCatalog(t, books(3)...)

	feeds := map[string]*Feed{}
	root, err := c.Root()
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	feeds["root"] = root

	all, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath, Page: 1})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	feeds["all"] = all

	authors, err := c.db.Authors()
	if err != nil {
		t.Fatalf("authors: %v", err)
	}
	feeds["authors"] = c.Groups("urn:test", "By Author", authorsPath, authors,
		authorsPath+"/", "book", "books", 0)

	for name, f := range feeds {
		check := func(where, href string) {
			if href == "" {
				t.Errorf("%s: %s has an empty href", name, where)
				return
			}
			// The search template is the one href carrying a placeholder; it
			// is still an absolute path.
			if !strings.HasPrefix(href, "/") {
				t.Errorf("%s: %s href %q is not an absolute path", name, where, href)
			}
		}
		for _, l := range f.Links {
			check("feed link "+l.Rel, l.Href)
		}
		for _, e := range f.Entries {
			for _, l := range e.Links {
				check("entry "+e.Title+" link "+l.Rel, l.Href)
			}
		}
	}
}

func TestPaginationUsesPreviousNotPrev(t *testing.T) {
	c := newCatalog(t, books(120)...)

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath, Page: 1})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	var rels []string
	for _, l := range feed.Links {
		rels = append(rels, l.Rel)
	}
	joined := strings.Join(rels, ",")
	if !strings.Contains(joined, "previous") {
		t.Errorf("no previous link on page 1; rels were %s", joined)
	}
	for _, r := range rels {
		if r == "prev" {
			t.Error(`rel is "prev"; the firmware compares against "previous"`)
		}
	}

	res, err := fwParse(render(t, feed))
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	if res.PrevPage == "" {
		t.Error("firmware found no previous page link")
	}
	if res.NextPage == "" {
		t.Error("firmware found no next page link")
	}
}

// The firmware reads the search template out of the link href. A catalog that
// only offers an OpenSearch description document is unsearchable on the device.
func TestSearchTemplateIsInlineForTheFirmware(t *testing.T) {
	c := newCatalog(t, book(1))
	root, err := c.Root()
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	res, err := fwParse(render(t, root))
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	if res.SearchTemplate == "" {
		t.Fatal("firmware found no search template")
	}
	if !strings.Contains(res.SearchTemplate, "{searchTerms}") {
		t.Errorf("template %q has no placeholder", res.SearchTemplate)
	}

	// The OpenSearch description link must still be there for other clients,
	// and must not be the one the firmware picked up.
	var hasDescription bool
	for _, l := range root.Links {
		if l.Type == "application/opensearchdescription+xml" {
			hasDescription = true
			if l.Href == res.SearchTemplate {
				t.Error("firmware picked up the description document as its template")
			}
		}
	}
	if !hasDescription {
		t.Error("no OpenSearch description link for standard clients")
	}
}

func TestTruncateUTF8IsRuneSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"ascii under limit", "Dune", 160},
		{"ascii over limit", strings.Repeat("a", 400), 160},
		{"multibyte over limit", strings.Repeat("日", 200), MaxTitleBytes},
		{"emoji over limit", strings.Repeat("📚", 100), MaxTitleBytes},
		{"cut lands mid-rune", strings.Repeat("é", 100), 61},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8(tc.in, tc.max)
			if len(got) > tc.max {
				t.Errorf("got %d bytes, limit was %d", len(got), tc.max)
			}
			if !utf8Valid(got) {
				t.Errorf("truncation produced invalid UTF-8: %q", got)
			}
		})
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestLongTitleSurvivesTheFirmwareByteCap(t *testing.T) {
	long := &library.Book{
		SHA256: strings.Repeat("b", 64),
		Path:   "/lib/long.epub",
		Format: library.FormatEPUB,
		// Multi-byte throughout, so a naive byte cut lands mid-rune.
		Title: strings.Repeat("日本語のタイトル", 40),
	}
	c := newCatalog(t, long)

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	title := feed.Entries[0].Title
	if len(title) > MaxTitleBytes {
		t.Errorf("title is %d bytes; the firmware caps at %d and would cut a rune in half",
			len(title), MaxTitleBytes)
	}
	if !utf8Valid(title) {
		t.Errorf("title is not valid UTF-8: %q", title)
	}
}

func TestFeedIsWellFormedXML(t *testing.T) {
	c := newCatalog(t, books(5)...)
	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	var parsed struct {
		XMLName xml.Name
		Entries []struct {
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(render(t, feed), &parsed); err != nil {
		t.Fatalf("feed is not well-formed: %v", err)
	}
	if parsed.XMLName.Space != nsAtom {
		t.Errorf("root namespace is %q, want %q", parsed.XMLName.Space, nsAtom)
	}
	if len(parsed.Entries) != 5 {
		t.Errorf("parsed %d entries, want 5", len(parsed.Entries))
	}
}

// Titles and author names go into XML unescaped otherwise.
func TestHostileMetadataDoesNotBreakTheFeed(t *testing.T) {
	nasty := &library.Book{
		SHA256: strings.Repeat("c", 64),
		Path:   "/lib/nasty.epub",
		Format: library.FormatEPUB,
		Title:  `Bell & Sons <script>"'`,
		// An ampersand and a quote in an author name is the case that would
		// otherwise produce a feed the device's Expat parser rejects outright,
		// taking every book on the page with it.
		Authors: []string{`O'Brien & Co "Ltd"`},
		Tags:    []string{`<tag>`},
	}
	c := newCatalog(t, nasty)

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}

	res, err := fwParse(render(t, feed))
	if err != nil {
		t.Fatalf("firmware could not parse the feed: %v", err)
	}
	got := res.books()
	if len(got) != 1 {
		t.Fatalf("firmware saw %d books, want 1", len(got))
	}
	if got[0].Title != nasty.Title {
		t.Errorf("title round-tripped as %q, want %q", got[0].Title, nasty.Title)
	}
	if got[0].Author != nasty.Authors[0] {
		t.Errorf("author round-tripped as %q, want %q", got[0].Author, nasty.Authors[0])
	}
}

// Browsing by an author whose name contains quotes is exactly what the query
// lexer cannot express, which is why the filter binds a parameter instead.
func TestBrowseByAuthorWithQuotesInTheName(t *testing.T) {
	const name = `O'Brien & Sons "Bob"`
	b := &library.Book{
		SHA256: strings.Repeat("d", 64), Path: "/lib/q.epub", Format: library.FormatEPUB,
		Title: "Quoted", AuthorSort: name, Authors: []string{name},
	}
	other := book(7)
	c := newCatalog(t, b, other)

	feed, err := c.Books(BookFeed{
		ID: "urn:test", Title: name, Self: authorsPath + "/x",
		Filter: &library.Filter{Field: library.FilterAuthor, Value: name},
	})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("got %d entries, want exactly the one book by %s", len(feed.Entries), name)
	}
	if feed.Entries[0].Title != "Quoted" {
		t.Errorf("matched the wrong book: %q", feed.Entries[0].Title)
	}
}

func TestRootOmitsEmptySections(t *testing.T) {
	// No tags, no series, no shelves — only the book-list sections apply.
	c := newCatalog(t, book(1))
	root, err := c.Root()
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	for _, e := range root.Entries {
		switch e.Title {
		case "By Tag", "By Series", "Shelves":
			t.Errorf("root offers %q with nothing behind it", e.Title)
		}
	}
	if len(root.Entries) == 0 {
		t.Error("root feed is empty")
	}
}

// --- server ---

type staticAuth map[string]string

func (a staticAuth) CheckPassword(username, password string) error {
	if want, ok := a[username]; ok && want == password {
		return nil
	}
	return fmt.Errorf("bad credentials")
}

func newServer(t *testing.T, c *Catalog, auth PasswordChecker) *httptest.Server {
	t.Helper()
	s := NewServer(c, nil)
	s.Auth = auth
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestCatalogRequiresCredentials(t *testing.T) {
	c := newCatalog(t, book(1))
	srv := newServer(t, c, staticAuth{"reader": "pw"})

	resp, err := http.Get(srv.URL + "/opds")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", resp.StatusCode)
	}
	// Without a challenge a browser never prompts, which is the quickest way
	// to check the catalog is reachable before configuring the reader.
	if h := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(h, "Basic ") {
		t.Errorf("WWW-Authenticate is %q", h)
	}
}

func TestCatalogAcceptsBasicAuth(t *testing.T) {
	c := newCatalog(t, book(1))
	srv := newServer(t, c, staticAuth{"reader": "pw"})

	req, _ := http.NewRequest("GET", srv.URL+"/opds", nil)
	req.SetBasicAuth("reader", "pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "atom+xml") {
		t.Errorf("content type is %q", ct)
	}
}

func TestAnonymousCatalogWhenAuthIsUnset(t *testing.T) {
	c := newCatalog(t, book(1))
	srv := newServer(t, c, nil)

	resp, err := http.Get(srv.URL + "/opds")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200", resp.StatusCode)
	}
}

func TestAcquisitionServesTheBook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.epub")
	content := []byte("PK\x03\x04 not really a zip, but the bytes are the point")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	b := &library.Book{
		SHA256: strings.Repeat("e", 64), Path: path, Format: library.FormatEPUB,
		Title: "Real Book", Authors: []string{"A Author"}, Size: int64(len(content)),
	}
	c := newCatalog(t, b)
	srv := newServer(t, c, nil)

	resp, err := http.Get(srv.URL + bookPrefix + b.SHA256 + ".epub")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != AcquisitionType {
		t.Errorf("content type %q, want %q", ct, AcquisitionType)
	}
	got := make([]byte, len(content))
	if _, err := resp.Body.Read(got); err != nil && err.Error() != "EOF" {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("body was %q", got)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("content disposition %q", cd)
	}
}

// The index is a cache of what is on disk and goes stale. A book deleted since
// the last scan is a stale catalog, not a broken server.
func TestAcquisitionOfAMissingFileIsNotFound(t *testing.T) {
	b := &library.Book{
		SHA256: strings.Repeat("f", 64), Path: "/definitely/not/here.epub",
		Format: library.FormatEPUB, Title: "Gone",
	}
	c := newCatalog(t, b)
	srv := newServer(t, c, nil)

	resp, err := http.Get(srv.URL + bookPrefix + b.SHA256 + ".epub")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got %d, want 404", resp.StatusCode)
	}
}

func TestUnknownHashIsNotFound(t *testing.T) {
	c := newCatalog(t, book(1))
	srv := newServer(t, c, nil)

	resp, err := http.Get(srv.URL + bookPrefix + strings.Repeat("9", 64) + ".epub")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got %d, want 404", resp.StatusCode)
	}
}

// http.ServeMux percent-decodes what it hands to PathValue. Unescaping it a
// second time made "100% Cotton" an invalid escape sequence and a 400 — the
// name reaches the handler already decoded, and "% C" is not valid.
func TestBrowseValuesAreNotDecodedTwice(t *testing.T) {
	names := []string{
		`100% Cotton`,
		`A+B`,
		`50%off & more`,
		`Ursula K.`,
		`O'Brien & Sons "Bob"`,
		`日本語`,
	}

	all := make([]*library.Book, 0, len(names))
	for i, n := range names {
		all = append(all, &library.Book{
			SHA256: fmt.Sprintf("%064d", i), Path: fmt.Sprintf("/lib/%d.epub", i),
			Format: library.FormatEPUB, Title: fmt.Sprintf("Book by %s", n),
			AuthorSort: n, Authors: []string{n},
		})
	}

	c := newCatalog(t, all...)
	srv := newServer(t, c, nil)

	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			// Exactly the href the authors feed would have emitted.
			resp, err := http.Get(srv.URL + authorsPath + "/" + urlEscape(n))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("got %d, want 200", resp.StatusCode)
			}

			body := make([]byte, 1<<16)
			read, _ := resp.Body.Read(body)
			res, err := fwParse(body[:read])
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := len(res.books()); got != 1 {
				t.Errorf("author %q matched %d books, want 1", n, got)
			}
		})
	}
}

// urlEscape mirrors what Groups uses to build a child href, so the test walks
// the same path a client would.
func urlEscape(s string) string { return url.PathEscape(s) }

func TestSearchEndpoint(t *testing.T) {
	dune := &library.Book{
		SHA256: strings.Repeat("1", 64), Path: "/lib/dune.epub",
		Format: library.FormatEPUB, Title: "Dune", Authors: []string{"Frank Herbert"},
	}
	c := newCatalog(t, dune, book(2), book(3))
	srv := newServer(t, c, nil)

	resp, err := http.Get(srv.URL + searchPath + "?q=dune")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := make([]byte, 1<<16)
	n, _ := resp.Body.Read(body)
	res, err := fwParse(body[:n])
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	got := res.books()
	if len(got) != 1 || got[0].Title != "Dune" {
		t.Errorf("search returned %d books: %+v", len(got), got)
	}
}

// Non-EPUB formats are advertised honestly. CrossPoint will not show them,
// because its acquisition check is an exact match on the EPUB type — that is a
// firmware limitation, and the feed should not lie about the format to route
// around it.
func TestNonEpubFormatsAreAdvertisedWithTheirRealType(t *testing.T) {
	pdf := &library.Book{
		SHA256: strings.Repeat("2", 64), Path: "/lib/paper.pdf",
		Format: library.FormatPDF, Title: "A Paper",
	}
	c := newCatalog(t, pdf)

	feed, err := c.Books(BookFeed{ID: "urn:test", Title: "All", Self: allPath})
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}
	if got := feed.Entries[0].Links[0].Type; got != "application/pdf" {
		t.Errorf("pdf advertised as %q", got)
	}

	res, err := fwParse(render(t, feed))
	if err != nil {
		t.Fatalf("firmware parse: %v", err)
	}
	if n := len(res.books()); n != 0 {
		t.Errorf("firmware treated a PDF as downloadable (%d books)", n)
	}
}
