package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sroberts/shelf/internal/library"
)

//go:embed openapi.json
var openAPIDoc []byte

// Authenticator says who made a request, or that nobody acceptable did.
// The caller composes it: under Outpost's gateway a verified identity token,
// otherwise HTTP Basic against shelf's own accounts.
type Authenticator func(*http.Request) (user string, ok bool)

// Server serves the REST API and the MCP endpoint.
type Server struct {
	svc  *Service
	auth Authenticator
	log  *slog.Logger
	// Realm names the API in a browser's credential prompt.
	Realm string
}

// NewServer builds the HTTP front ends. A nil auth leaves them open, which a
// caller has to choose deliberately: the API can edit shelves.
func NewServer(svc *Service, auth Authenticator, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{svc: svc, auth: auth, log: log, Realm: "shelf"}
}

// route is one REST endpoint. The table is the single list both the mux and
// the OpenAPI consistency test read, so a route cannot be added to one and
// forgotten in the other.
type route struct {
	method, pattern string
	public          bool
	handle          func(s *Server, w http.ResponseWriter, r *http.Request, user string)
}

var routes = []route{
	{"GET", "/v1/openapi.json", true, (*Server).openAPI},
	{"GET", "/v1/books", false, (*Server).listBooks},
	{"GET", "/v1/books/{id}", false, (*Server).getBook},
	{"GET", "/v1/books/{id}/file", false, (*Server).getBookFile},
	{"GET", "/v1/authors", false, (*Server).listAuthors},
	{"GET", "/v1/series", false, (*Server).listSeries},
	{"GET", "/v1/tags", false, (*Server).listTags},
	{"GET", "/v1/genres", false, (*Server).listGenres},
	{"GET", "/v1/shelves", false, (*Server).listShelves},
	{"GET", "/v1/shelves/{name}", false, (*Server).getShelf},
	{"POST", "/v1/shelves/{name}/books", false, (*Server).addToShelf},
	{"DELETE", "/v1/shelves/{name}/books/{id}", false, (*Server).removeFromShelf},
	{"GET", "/v1/stats", false, (*Server).stats},
	{"GET", "/v1/progress", false, (*Server).progress},
}

// Mount registers /v1/* and /mcp on mux.
func (s *Server) Mount(mux *http.ServeMux) {
	for _, rt := range routes {
		mux.HandleFunc(rt.method+" "+rt.pattern, func(w http.ResponseWriter, r *http.Request) {
			user, ok := s.authenticate(w, r, rt.public)
			if !ok {
				return
			}
			rt.handle(s, w, r, user)
		})
	}
	// Anything else under /v1 is a JSON 404, not kosync's plain-text help.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint; see /v1/openapi.json")
	})
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.authenticate(w, r, false)
		if !ok {
			return
		}
		s.serveMCP(w, r, user)
	}))
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, public bool) (string, bool) {
	if public || s.auth == nil {
		return "", true
	}
	if user, ok := s.auth(r); ok {
		return user, true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="`+strings.ReplaceAll(s.Realm, `"`, "")+`", charset="UTF-8"`)
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return "", false
}

func (s *Server) openAPI(w http.ResponseWriter, _ *http.Request, _ string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(openAPIDoc)
}

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	limit, err1 := optInt(q.Get("limit"))
	offset, err2 := optInt(q.Get("offset"))
	if err := errors.Join(err1, err2); err != nil {
		s.fail(w, r, badRequest("limit and offset must be integers"))
		return
	}
	res, err := s.svc.Search(SearchParams{
		Query: q.Get("q"), Genre: q.Get("genre"), Sort: q.Get("sort"),
		Limit: limit, Offset: offset, User: q.Get("user"),
	}, user)
	s.respond(w, r, res, err)
}

func (s *Server) getBook(w http.ResponseWriter, r *http.Request, user string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	res, err := s.svc.Book(id, r.URL.Query().Get("user"), user)
	s.respond(w, r, res, err)
}

func (s *Server) getBookFile(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	b, err := s.svc.BookFile(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := os.Open(b.Path)
	if err != nil {
		// Indexed but gone from disk: the index is a cache and can be stale.
		s.fail(w, r, errors.Join(ErrNotFound, errors.New("the file is no longer on disk; rescan the library")))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Go's mime table has no entry for .epub on most systems, and an OPDS
	// client deciding what it downloaded goes by this header.
	ctype := mime.TypeByExtension(filepath.Ext(b.Path))
	if b.Format == library.FormatEPUB {
		ctype = "application/epub+zip"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": filepath.Base(b.Path)}))
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *Server) listAuthors(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.Authors()
	s.respond(w, r, map[string]any{"authors": res}, err)
}

func (s *Server) listSeries(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.Series()
	s.respond(w, r, map[string]any{"series": res}, err)
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.Tags()
	s.respond(w, r, map[string]any{"tags": res}, err)
}

func (s *Server) listGenres(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.GenreCounts()
	s.respond(w, r, map[string]any{"genres": res}, err)
}

func (s *Server) listShelves(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.Shelves()
	s.respond(w, r, map[string]any{"shelves": res}, err)
}

func (s *Server) getShelf(w http.ResponseWriter, r *http.Request, user string) {
	res, err := s.svc.Shelf(r.PathValue("name"), r.URL.Query().Get("user"), user)
	s.respond(w, r, res, err)
}

func (s *Server) addToShelf(w http.ResponseWriter, r *http.Request, _ string) {
	// A JSON content type cannot be sent by a plain HTML form, so requiring
	// it closes the form-CSRF route on top of the Origin check.
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		s.fail(w, r, badRequest("body must be {\"ids\": [1, 2]}"))
		return
	}
	res, err := s.svc.AddToShelf(r.PathValue("name"), body.IDs)
	s.respond(w, r, res, err)
}

func (s *Server) removeFromShelf(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	res, err := s.svc.RemoveFromShelf(r.PathValue("name"), []int64{id})
	s.respond(w, r, res, err)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.LibraryStats()
	s.respond(w, r, res, err)
}

func (s *Server) progress(w http.ResponseWriter, r *http.Request, user string) {
	res, err := s.svc.ReadingProgress(r.URL.Query().Get("user"), user)
	s.respond(w, r, res, err)
}

func (s *Server) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, r, badRequest("book id must be a positive integer"))
		return 0, false
	}
	return id, true
}

func optInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrBadRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
