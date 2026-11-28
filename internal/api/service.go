// Package api is shelf's machine interface: a JSON REST API under /v1, its
// OpenAPI description at /v1/openapi.json, and an MCP server at /mcp.
//
// Both front ends call the one Service below, so a tool an MCP client calls and
// the REST route an OpenAPI-generated skill calls cannot drift apart. The Outpost
// service spec (§9) asks for exactly this shape: REST under /v1/*, MCP at /mcp
// over Streamable HTTP, and the OpenAPI document at GET /v1/openapi.json.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// Errors the front ends map to a status or a tool error.
var (
	ErrNotFound   = errors.New("not found")
	ErrBadRequest = errors.New("bad request")
)

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, args...))
}

// ProgressSource is the reading-progress store; *kosync.Store satisfies it.
type ProgressSource interface {
	AllProgress(username string) (map[string]kosync.Progress, error)
	Users() ([]string, error)
	DeleteProgress(username, document string) error
}

// GenreSource is the AI classification store, or nil when AI is off.
type GenreSource interface {
	Genre(sha256 string) (string, bool)
	Genres() (map[string]string, error)
	// Carry moves an answer to a book's new hash after an edit changed its
	// bytes, so the operator does not pay to classify the same book twice.
	Carry(fromSHA, toSHA string) error
}

// Service answers every API question.
type Service struct {
	DB          *library.DB
	LibraryRoot string
	Progress    ProgressSource // nil: no progress
	Genres      GenreSource    // nil: AI off
	// DefaultUser is [kosync] user, whose progress is shown when the caller
	// is not itself a kosync account.
	DefaultUser string
	// SaveShelves writes shelf definitions to shelves.toml, and
	// ReloadShelves restores the index from it. A shelf edit that cannot be
	// written to the file is rolled back in the index: the file is the
	// authority, and the index is a cache that must not claim more than it.
	SaveShelves   func() error
	ReloadShelves func() error

	Version string

	// ImportTemplate is the naming template uploads are placed by, and
	// StageDir is scratch space they land in before they are placed.
	ImportTemplate string
	StageDir       string
	// OnImport, when set, is called after books join the library, so the
	// caller can enrich them (classify genres) without polling.
	OnImport func()

	// Sync, when set, lets clients list devices and sync to them.
	Sync *SyncService

	// AccountDB holds reader accounts, the ones the e-reader signs in with.
	AccountDB AccountStore

	// mu serialises shelf edits: each is a read-modify-write of shelves.toml.
	mu sync.Mutex
	// editMu serialises writes to the library's files: metadata edits and
	// imports, which both choose or rewrite files on disk.
	editMu sync.Mutex
}

// Book is the API's stable shape for a book. Defined here rather than
// marshalling library.Book, so an internal field rename cannot change what
// clients receive.
type Book struct {
	ID          int64             `json:"id"`
	Path        string            `json:"path"`
	Title       string            `json:"title"`
	Authors     []string          `json:"authors"`
	AuthorSort  string            `json:"author_sort,omitempty"`
	Series      string            `json:"series,omitempty"`
	SeriesIndex float64           `json:"series_index,omitempty"`
	Format      string            `json:"format"`
	Size        int64             `json:"size"`
	Language    string            `json:"language,omitempty"`
	Publisher   string            `json:"publisher,omitempty"`
	PubDate     string            `json:"pubdate,omitempty"`
	Tags        []string          `json:"tags"`
	Identifiers map[string]string `json:"identifiers,omitempty"`
	SHA256      string            `json:"sha256"`
	AddedUnix   int64             `json:"added_unix"`
	Description string            `json:"description,omitempty"`
	HasCover    bool              `json:"has_cover"`
	Genre       string            `json:"genre,omitempty"`
	Percent     *float64          `json:"percent_read,omitempty"`
	Reading     *Reading          `json:"reading,omitempty"`
}

// Reading is where a reader last was in a book.
type Reading struct {
	Percent float64 `json:"percent"`
	Device  string  `json:"device,omitempty"`
	Updated int64   `json:"updated_unix"`
}

// BookList is one page of books.
type BookList struct {
	Books  []Book `json:"books"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
	// More is true when another page follows.
	More bool `json:"more"`
	// User is whose reading progress percent_read reflects, if anyone's.
	User string `json:"user,omitempty"`
}

// Count is one value of a browse dimension.
type Count struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Shelf summarises a shelf.
type Shelf struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Query string `json:"query,omitempty"`
	Books int    `json:"books"`
}

// ShelfDetail is a shelf with its books.
type ShelfDetail struct {
	Shelf
	Contents []Book `json:"contents"`
	User     string `json:"user,omitempty"`
}

// Stats summarises the library.
type Stats struct {
	library.Stats
	Genres  map[string]int `json:"genres,omitempty"`
	Version string         `json:"version,omitempty"`
}

// ProgressEntry is one book a user has opened on the reader.
type ProgressEntry struct {
	Book       Book    `json:"book"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device,omitempty"`
	Updated    int64   `json:"updated_unix"`
}

// ProgressList is a user's reading positions, most recent first.
type ProgressList struct {
	User  string          `json:"user"`
	Books []ProgressEntry `json:"books"`
}

// SearchParams narrows a book listing.
type SearchParams struct {
	Query  string
	Genre  string
	Sort   string
	Limit  int
	Offset int
	User   string
}

const (
	defaultLimit = 50
	maxLimit     = 500
)

// Search lists books matching a query, in the CLI's query syntax.
func (s *Service) Search(p SearchParams, caller string) (*BookList, error) {
	limit := p.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if p.Offset < 0 {
		return nil, badRequest("offset must not be negative")
	}

	opts := library.SearchOptions{OrderBy: p.Sort}
	if p.Genre == "" {
		// One extra row says whether another page follows without a count.
		opts.Limit, opts.Offset = limit+1, p.Offset
	}
	books, err := s.DB.Search(p.Query, opts)
	if err != nil {
		return nil, badRequest("%v", err)
	}

	// Genre lives in the AI store, not the index, so it filters in memory.
	// A personal library is small enough that this is cheaper than keeping
	// a second copy of every genre in a disposable cache.
	if p.Genre != "" {
		books = slices.DeleteFunc(books, func(b *library.Book) bool {
			g, _ := s.genre(b.SHA256)
			return !strings.EqualFold(g, p.Genre)
		})
		if p.Offset >= len(books) {
			books = nil
		} else {
			books = books[p.Offset:]
		}
		if len(books) > limit+1 {
			books = books[:limit+1]
		}
	}

	more := len(books) > limit
	if more {
		books = books[:limit]
	}
	user, byDoc := s.progressFor(p.User, caller)
	return &BookList{
		Books: s.convert(books, byDoc), Offset: p.Offset, Limit: limit, More: more, User: user,
	}, nil
}

// Book returns one book by index id.
func (s *Service) Book(id int64, user, caller string) (*Book, error) {
	b, err := s.DB.ByID(id)
	if errors.Is(err, library.ErrNotFound) {
		return nil, fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	_, byDoc := s.progressFor(user, caller)
	out := s.convertOne(b, byDoc)
	return &out, nil
}

// BookFile returns the path of a book's file, for download.
func (s *Service) BookFile(id int64) (*library.Book, error) {
	b, err := s.DB.ByID(id)
	if errors.Is(err, library.ErrNotFound) {
		return nil, fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
	}
	return b, err
}

// Authors, Series and Tags list browse values with counts.
func (s *Service) Authors() ([]Count, error) { return groups(s.DB.Authors()) }
func (s *Service) Series() ([]Count, error)  { return groups(s.DB.Series()) }

func (s *Service) Tags() ([]Count, error) {
	tags, err := s.DB.Tags()
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(tags))
	for _, t := range tags {
		out = append(out, Count{Value: t.Tag, Count: t.Count})
	}
	return out, nil
}

// GenreCounts lists AI-assigned genres with counts; empty when AI is off.
func (s *Service) GenreCounts() ([]Count, error) {
	counts, err := s.genreCounts()
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(counts))
	for g, n := range counts {
		out = append(out, Count{Value: g, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

func groups(gs []library.GroupCount, err error) ([]Count, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(gs))
	for _, g := range gs {
		out = append(out, Count{Value: g.Value, Count: g.Count})
	}
	return out, nil
}

// Shelves lists every shelf.
func (s *Service) Shelves() ([]Shelf, error) {
	shelves, err := s.DB.ListShelves()
	if err != nil {
		return nil, err
	}
	out := make([]Shelf, 0, len(shelves))
	for _, sh := range shelves {
		books, err := s.DB.ShelfBooks(sh.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, shelfSummary(sh, len(books)))
	}
	return out, nil
}

// Shelf returns one shelf with its contents.
func (s *Service) Shelf(name, user, caller string) (*ShelfDetail, error) {
	sh, err := s.DB.GetShelf(name)
	if errors.Is(err, library.ErrShelfNotFound) {
		return nil, fmt.Errorf("%w: no shelf named %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	books, err := s.DB.ShelfBooks(name)
	if err != nil {
		return nil, err
	}
	who, byDoc := s.progressFor(user, caller)
	return &ShelfDetail{
		Shelf: shelfSummary(sh, len(books)), Contents: s.convert(books, byDoc), User: who,
	}, nil
}

func shelfSummary(sh *library.Shelf, n int) Shelf {
	return Shelf{Name: sh.Name, Kind: string(sh.Kind), Query: sh.Query, Books: n}
}

// AddToShelf adds books to a manual shelf.
func (s *Service) AddToShelf(name string, ids []int64) (*Shelf, error) {
	return s.editShelf(name, ids, s.DB.AddToShelf)
}

// RemoveFromShelf takes books off a manual shelf.
func (s *Service) RemoveFromShelf(name string, ids []int64) (*Shelf, error) {
	return s.editShelf(name, ids, s.DB.RemoveFromShelf)
}

func (s *Service) editShelf(name string, ids []int64, edit func(string, []string) error) (*Shelf, error) {
	if len(ids) == 0 {
		return nil, badRequest("no book ids given")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sh, err := s.DB.GetShelf(name)
	if errors.Is(err, library.ErrShelfNotFound) {
		return nil, fmt.Errorf("%w: no shelf named %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if sh.Kind != library.KindManual {
		return nil, badRequest("shelf %q is a saved query (%s); its contents follow the query", name, sh.Query)
	}

	// Members are stored by path, not id: ids are renumbered whenever the
	// index is rebuilt, and shelves.toml must outlive that.
	paths := make([]string, 0, len(ids))
	for _, id := range ids {
		b, err := s.DB.ByID(id)
		if errors.Is(err, library.ErrNotFound) {
			return nil, fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
		}
		if err != nil {
			return nil, err
		}
		paths = append(paths, b.Path)
	}

	if err := edit(name, paths); err != nil {
		return nil, err
	}
	if err := s.saveShelves(); err != nil {
		return nil, err
	}

	books, err := s.DB.ShelfBooks(name)
	if err != nil {
		return nil, err
	}
	out := shelfSummary(sh, len(books))
	return &out, nil
}

// LibraryStats summarises the library.
func (s *Service) LibraryStats() (*Stats, error) {
	st, err := s.DB.Stats()
	if err != nil {
		return nil, err
	}
	genres, err := s.genreCounts()
	if err != nil {
		return nil, err
	}
	return &Stats{Stats: st, Genres: genres, Version: s.Version}, nil
}

// ReadingProgress lists the books a user has progress for.
func (s *Service) ReadingProgress(user, caller string) (*ProgressList, error) {
	who, byDoc := s.progressFor(user, caller)
	if who == "" {
		return nil, badRequest("no user: pass user, or set [kosync] user in the config")
	}
	all, err := s.DB.All()
	if err != nil {
		return nil, err
	}
	out := &ProgressList{User: who, Books: []ProgressEntry{}}
	for _, b := range all {
		p, ok := byDoc[b.DocID]
		if b.DocID == "" || !ok {
			continue
		}
		out.Books = append(out.Books, ProgressEntry{
			Book:       s.convertOne(b, byDoc),
			Percentage: p.Percentage * 100,
			Device:     p.Device,
			Updated:    p.Timestamp,
		})
	}
	sort.SliceStable(out.Books, func(i, j int) bool { return out.Books[i].Updated > out.Books[j].Updated })
	return out, nil
}

// progressFor picks whose progress to show: the user asked for, else the
// caller when the caller is a kosync account (a reader's own login), else the
// configured default. A gateway username that is not a kosync account falls
// through to the default rather than showing nothing.
func (s *Service) progressFor(requested, caller string) (string, map[string]kosync.Progress) {
	if s.Progress == nil {
		return "", nil
	}
	user := kosync.NormalizeUsername(requested)
	if user == "" && caller != "" {
		if users, err := s.Progress.Users(); err == nil && slices.Contains(users, kosync.NormalizeUsername(caller)) {
			user = kosync.NormalizeUsername(caller)
		}
	}
	if user == "" {
		user = s.DefaultUser
	}
	if user == "" {
		return "", nil
	}
	byDoc, err := s.Progress.AllProgress(user)
	if err != nil {
		return user, nil
	}
	return user, byDoc
}

func (s *Service) genre(sha string) (string, bool) {
	if s.Genres == nil {
		return "", false
	}
	return s.Genres.Genre(sha)
}

func (s *Service) genreCounts() (map[string]int, error) {
	if s.Genres == nil {
		return nil, nil
	}
	m, err := s.Genres.Genres()
	if err != nil {
		return nil, err
	}
	// Count only books still in the index; the AI store outlives deletions.
	all, err := s.DB.All()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, b := range all {
		if g := m[b.SHA256]; g != "" {
			counts[g]++
		}
	}
	return counts, nil
}

func (s *Service) convert(books []*library.Book, byDoc map[string]kosync.Progress) []Book {
	out := make([]Book, 0, len(books))
	for _, b := range books {
		out = append(out, s.convertOne(b, byDoc))
	}
	return out
}

func (s *Service) convertOne(b *library.Book, byDoc map[string]kosync.Progress) Book {
	out := Book{
		ID:          b.ID,
		Path:        s.relPath(b.Path),
		Title:       b.DisplayTitle(),
		Authors:     nonNil(b.Authors),
		AuthorSort:  b.AuthorSort,
		Series:      b.Series,
		SeriesIndex: b.SeriesIndex,
		Format:      string(b.Format),
		Size:        b.Size,
		Language:    b.Language,
		Publisher:   b.Publisher,
		PubDate:     b.PubDate,
		Tags:        nonNil(b.Tags),
		Identifiers: b.Identifiers,
		SHA256:      b.SHA256,
		AddedUnix:   b.AddedUnix,
	}
	out.Description = description(b.MetaJSON)
	out.HasCover = len(b.Cover) > 0
	out.Genre, _ = s.genre(b.SHA256)
	if p, ok := byDoc[b.DocID]; ok && b.DocID != "" {
		pct := p.Percentage * 100
		out.Percent = &pct
		out.Reading = &Reading{Percent: pct, Device: p.Device, Updated: p.Timestamp}
	}
	return out
}

// relPath reports a book's path relative to the library root. An absolute
// path would tell every API client the layout of the host's filesystem, and
// under Outpost the storage directory's location is the operator's business.
func (s *Service) relPath(p string) string {
	if s.LibraryRoot == "" {
		return filepath.ToSlash(p)
	}
	rel, err := filepath.Rel(s.LibraryRoot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(p)
	}
	return filepath.ToSlash(rel)
}

// description reads the blurb out of the metadata the scan kept. It is not an
// index column because nothing searches or sorts by it; only a detail view
// shows it.
func description(metaJSON string) string {
	if metaJSON == "" {
		return ""
	}
	var m struct{ Description string }
	if json.Unmarshal([]byte(metaJSON), &m) != nil {
		return ""
	}
	// EPUB descriptions are often HTML. Clients get text: a client that
	// rendered the markup would be rendering whatever a book file carried.
	text := html.UnescapeString(markup.ReplaceAllString(m.Description, " "))
	return strings.Join(strings.Fields(text), " ")
}

var markup = regexp.MustCompile(`<[^>]*>`)

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
