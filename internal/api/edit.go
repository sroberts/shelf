package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// ErrConflict is an edit that collides with what exists: a shelf name taken,
// a device already syncing.
var ErrConflict = errors.New("conflict")

// BookPatch is a metadata edit. A nil field is left alone; an empty list
// clears authors or tags.
type BookPatch struct {
	Title       *string   `json:"title"`
	Authors     *[]string `json:"authors"`
	Series      *string   `json:"series"`
	SeriesIndex *float64  `json:"series_index"`
	Language    *string   `json:"language"`
	Publisher   *string   `json:"publisher"`
	PubDate     *string   `json:"pubdate"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
}

func (p BookPatch) empty() bool {
	return p.Title == nil && p.Authors == nil && p.Series == nil && p.SeriesIndex == nil &&
		p.Language == nil && p.Publisher == nil && p.PubDate == nil && p.Description == nil && p.Tags == nil
}

// UpdateBook edits a book's metadata in its file, then re-reads the file into
// the index. The file is written first because it is the authority: the index
// is derived and may be rebuilt from disk at any time.
//
// The file never moves. A book's path decides where it sits on the reader,
// and moving it there would cost its reading position, so a new title or
// author changes what the book says, not where it lives.
func (s *Service) UpdateBook(id int64, p BookPatch, caller string) (*Book, error) {
	if p.empty() {
		return nil, badRequest("nothing to change")
	}
	if p.Title != nil && strings.TrimSpace(*p.Title) == "" {
		return nil, badRequest("title must not be empty")
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	b, err := s.DB.ByID(id)
	if errors.Is(err, library.ErrNotFound) {
		return nil, fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if b.Format != library.FormatEPUB {
		return nil, badRequest("only EPUB metadata can be edited; this book is %s", b.Format)
	}

	patch := epub.Patch{
		Title: trimmed(p.Title), Series: trimmed(p.Series), SeriesIndex: p.SeriesIndex,
		Language: trimmed(p.Language), Publisher: trimmed(p.Publisher), Date: trimmed(p.PubDate),
		Description: p.Description,
	}
	if p.Authors != nil {
		authors := []epub.Creator{}
		for _, a := range cleanList(*p.Authors) {
			authors = append(authors, epub.Creator{Name: a})
		}
		patch.Authors = &authors
	}
	if p.Tags != nil {
		tags := cleanList(*p.Tags)
		patch.Subjects = &tags
	}

	if err := epub.Write(b.Path, patch); err != nil {
		return nil, fmt.Errorf("write %s: %w", s.relPath(b.Path), err)
	}
	if _, err := s.DB.Scan(context.Background(), filepath.Dir(b.Path), library.ScanOptions{}); err != nil {
		return nil, fmt.Errorf("re-index: %w", err)
	}
	updated, err := s.DB.ByPath(b.Path)
	if err != nil {
		return nil, err
	}
	if s.Genres != nil && updated.SHA256 != b.SHA256 {
		// Not fatal: the worst case is that the classifier asks again.
		_ = s.Genres.Carry(b.SHA256, updated.SHA256)
	}
	_, byDoc := s.progressFor("", caller)
	out := s.convertOne(updated, byDoc)
	return &out, nil
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Upload is one file a client sent, already written to a staging path whose
// base name is the client's file name.
type Upload struct {
	Name string
	Path string
}

// ImportResult reports an upload.
type ImportResult struct {
	Imported []Book         `json:"imported"`
	Failed   []ImportFailed `json:"failed"`
}

// ImportFailed is a file that did not join the library.
type ImportFailed struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ImportFiles moves uploaded files into the library under the naming
// template, the same way `shelf import --move` does, and indexes them.
//
// A file whose bytes are already in the library is refused rather than added
// beside the original, which would be a duplicate that syncs twice.
func (s *Service) ImportFiles(ctx context.Context, uploads []Upload, caller string) (*ImportResult, error) {
	if s.LibraryRoot == "" || s.ImportTemplate == "" {
		return nil, errors.New("import is not configured")
	}
	out := &ImportResult{Imported: []Book{}, Failed: []ImportFailed{}}
	fail := func(name string, err error) {
		out.Failed = append(out.Failed, ImportFailed{Name: name, Error: err.Error()})
	}

	// One import at a time: destinations are chosen by looking at what is on
	// disk, and two requests choosing at once could pick the same name.
	s.editMu.Lock()
	defer s.editMu.Unlock()

	var sources []string
	names := map[string]string{}
	for _, u := range uploads {
		if _, ok := library.FormatOf(u.Path); !ok {
			fail(u.Name, fmt.Errorf("unsupported format %q: shelf takes epub, pdf, txt and xtc", filepath.Ext(u.Name)))
			continue
		}
		sum, err := library.HashFile(u.Path)
		if err != nil {
			fail(u.Name, err)
			continue
		}
		if dup, err := s.DB.BySHA256(sum); err == nil && len(dup) > 0 {
			fail(u.Name, fmt.Errorf("already in the library as %s", s.relPath(dup[0].Path)))
			continue
		}
		sources = append(sources, u.Path)
		names[u.Path] = u.Name
	}
	if len(sources) == 0 {
		return out, nil
	}

	results, err := s.DB.Import(ctx, s.LibraryRoot, sources, library.ImportOptions{
		Mode: library.ImportMove, Template: s.ImportTemplate,
	})
	if err != nil {
		return nil, err
	}
	_, byDoc := s.progressFor("", caller)
	for _, r := range results {
		name := names[r.Source]
		if r.Err != nil {
			fail(name, r.Err)
			continue
		}
		// Import places the file but does not index it. Scanning only its
		// folder is cheap and cannot remove anything outside it.
		if _, err := s.DB.Scan(ctx, filepath.Dir(r.Dest), library.ScanOptions{}); err != nil {
			fail(name, fmt.Errorf("imported to %s but could not index it: %w", s.relPath(r.Dest), err))
			continue
		}
		b, err := s.DB.ByPath(r.Dest)
		if err != nil {
			fail(name, fmt.Errorf("imported to %s but could not index it: %w", s.relPath(r.Dest), err))
			continue
		}
		out.Imported = append(out.Imported, s.convertOne(b, byDoc))
	}
	if len(out.Imported) > 0 && s.OnImport != nil {
		s.OnImport()
	}
	return out, nil
}

// Cover returns a book's cover image, as extracted and downscaled at scan time.
func (s *Service) Cover(id int64) ([]byte, int64, error) {
	b, err := s.DB.ByID(id)
	if errors.Is(err, library.ErrNotFound) {
		return nil, 0, fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, 0, err
	}
	if len(b.Cover) == 0 {
		return nil, 0, fmt.Errorf("%w: book %d has no cover", ErrNotFound, id)
	}
	return b.Cover, b.MTimeUnix, nil
}

// CreateShelf adds a shelf: a saved query when query is set, otherwise a
// manual set of books.
func (s *Service) CreateShelf(name, query string) (*Shelf, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, badRequest("a shelf needs a name")
	}
	if strings.ContainsAny(name, "/\\") {
		// A shelf name is a path segment in /v1/shelves/{name} and in the
		// web UI's addresses.
		return nil, badRequest("a shelf name cannot contain a slash")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.DB.GetShelf(name); err == nil {
		return nil, fmt.Errorf("%w: a shelf named %q already exists", ErrConflict, name)
	}
	kind := library.KindManual
	query = strings.TrimSpace(query)
	if query != "" {
		kind = library.KindQuery
	}
	if err := s.DB.CreateShelf(library.Shelf{Name: name, Kind: kind, Query: query}); err != nil {
		if kind == library.KindQuery {
			// The only other refusal is a query that does not parse.
			return nil, badRequest("%v", err)
		}
		return nil, err
	}
	if err := s.saveShelves(); err != nil {
		return nil, err
	}
	sh, err := s.DB.GetShelf(name)
	if err != nil {
		return nil, err
	}
	books, err := s.DB.ShelfBooks(name)
	if err != nil {
		return nil, err
	}
	out := shelfSummary(sh, len(books))
	return &out, nil
}

// DeleteShelf removes a shelf's definition. Its books stay in the library.
func (s *Service) DeleteShelf(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.DB.DeleteShelf(name); errors.Is(err, library.ErrShelfNotFound) {
		return fmt.Errorf("%w: no shelf named %q", ErrNotFound, name)
	} else if err != nil {
		return err
	}
	return s.saveShelves()
}

// saveShelves writes shelves.toml, or puts the index back to what the file
// says if it cannot: the file is the authority, and an index that claimed an
// edit the file never got would have it quietly persisted by the next save.
func (s *Service) saveShelves() error {
	if s.SaveShelves == nil {
		return nil
	}
	if err := s.SaveShelves(); err != nil {
		if s.ReloadShelves != nil {
			_ = s.ReloadShelves()
		}
		return fmt.Errorf("save shelves: %w", err)
	}
	return nil
}

// ClearProgress forgets shelf's stored reading position for a book.
func (s *Service) ClearProgress(id int64, user, caller string) error {
	if s.Progress == nil {
		return badRequest("reading progress is not available")
	}
	b, err := s.DB.ByID(id)
	if errors.Is(err, library.ErrNotFound) {
		return fmt.Errorf("%w: no book with id %d", ErrNotFound, id)
	}
	if err != nil {
		return err
	}
	who, _ := s.progressFor(user, caller)
	if who == "" {
		return badRequest("no user: pass user, or set [kosync] user in the config")
	}
	if b.DocID == "" {
		return fmt.Errorf("%w: book %d has no reading position", ErrNotFound, id)
	}
	if err := s.Progress.DeleteProgress(who, b.DocID); errors.Is(err, kosync.ErrNoProgress) {
		return fmt.Errorf("%w: %s has no reading position in book %d", ErrNotFound, who, id)
	} else if err != nil {
		return err
	}
	return nil
}

// removeStaged deletes what is left of an upload: Import moves the files it
// takes, so this only ever removes the ones it refused.
func removeStaged(uploads []Upload) {
	for _, u := range uploads {
		os.Remove(u.Path)
		os.Remove(filepath.Dir(u.Path))
	}
}
