package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

type fixture struct {
	svc    *Service
	db     *library.DB
	root   string
	srv    *httptest.Server
	shelfs string
}

type fakeProgress map[string]map[string]kosync.Progress

func (f fakeProgress) AllProgress(u string) (map[string]kosync.Progress, error) { return f[u], nil }
func (f fakeProgress) Users() ([]string, error) {
	var out []string
	for u := range f {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, nil
}

type fakeGenres map[string]string

func (f fakeGenres) Genre(sha string) (string, bool) { g, ok := f[sha]; return g, ok }
func (f fakeGenres) Genres() (map[string]string, error) {
	return f, nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	db, err := library.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for i, title := range []string{"A Wizard of Earthsea", "The Dispossessed", "Dune"} {
		path := filepath.Join(root, fmt.Sprintf("book-%d.epub", i))
		os.MkdirAll(root, 0o755)
		os.WriteFile(path, []byte("PK\x03\x04 book "+title), 0o644)
		author := "Ursula K. Le Guin"
		if title == "Dune" {
			author = "Frank Herbert"
		}
		if err := db.Upsert(&library.Book{
			SHA256: fmt.Sprintf("%064x", i+1), DocID: fmt.Sprintf("doc%d", i+1),
			Path: path, Size: 10, Format: library.FormatEPUB, Title: title,
			Authors: []string{author}, AuthorSort: author, AddedUnix: 1700000000,
			Tags: []string{"sf"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateShelf(library.Shelf{Name: "to-read", Kind: library.KindManual}); err != nil {
		t.Fatal(err)
	}
	shelves := filepath.Join(dir, "shelves.toml")

	svc := &Service{
		DB: db, LibraryRoot: root, Version: "test",
		Progress:      fakeProgress{"reader": {"doc3": {Document: "doc3", Percentage: 0.42, Timestamp: 1700000500}}},
		Genres:        fakeGenres{fmt.Sprintf("%064x", 3): "science fiction", fmt.Sprintf("%064x", 1): "fantasy"},
		SaveShelves:   func() error { return db.SaveShelves(shelves) },
		ReloadShelves: func() error { return db.LoadShelves(shelves) },
	}
	auth := func(r *http.Request) (string, bool) {
		u, p, ok := r.BasicAuth()
		return u, ok && u == "reader" && p == "pw"
	}
	mux := http.NewServeMux()
	NewServer(svc, auth, nil).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fixture{svc: svc, db: db, root: root, srv: srv, shelfs: shelves}
}

func (f *fixture) do(t *testing.T, method, path, ctype, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.SetBasicAuth("reader", "pw")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (f *fixture) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	resp, body := f.do(t, "GET", path, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func TestRESTRequiresAuthExceptOpenAPI(t *testing.T) {
	f := newFixture(t)
	for _, rt := range routes {
		path := strings.NewReplacer("{id}", "1", "{name}", "to-read").Replace(rt.pattern)
		req, _ := http.NewRequest(rt.method, f.srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if rt.public != (resp.StatusCode != http.StatusUnauthorized) {
			t.Errorf("%s %s without credentials: %d (public=%v)", rt.method, rt.pattern, resp.StatusCode, rt.public)
		}
	}
	resp, err := http.Post(f.srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/mcp without credentials: %d", resp.StatusCode)
	}
}

func TestSearchBooks(t *testing.T) {
	f := newFixture(t)
	var res BookList
	f.getJSON(t, `/v1/books?q=author:"le+guin"`, &res)
	if len(res.Books) != 2 || res.More {
		t.Fatalf("got %+v", res)
	}
	// Paths are relative: an API client has no business knowing the host's
	// directory layout.
	if strings.HasPrefix(res.Books[0].Path, "/") {
		t.Errorf("path %q is absolute", res.Books[0].Path)
	}

	f.getJSON(t, "/v1/books?limit=2", &res)
	if len(res.Books) != 2 || !res.More {
		t.Errorf("first page of 3 at limit 2: %d books, more=%v", len(res.Books), res.More)
	}

	f.getJSON(t, "/v1/books?genre=fantasy", &res)
	if len(res.Books) != 1 || res.Books[0].Title != "A Wizard of Earthsea" || res.Books[0].Genre != "fantasy" {
		t.Errorf("genre filter: %+v", res.Books)
	}

	// The caller "reader" is a kosync account, so its progress shows.
	f.getJSON(t, "/v1/books?q=dune", &res)
	if res.User != "reader" || res.Books[0].Percent == nil || *res.Books[0].Percent != 42 {
		t.Errorf("progress: user=%q books=%+v", res.User, res.Books)
	}

	resp, _ := f.do(t, "GET", "/v1/books?sort=bogus", "", "")
	if resp.StatusCode != 400 {
		t.Errorf("bad sort: %d, want 400", resp.StatusCode)
	}
}

func TestBookAndFile(t *testing.T) {
	f := newFixture(t)
	var b Book
	f.getJSON(t, "/v1/books/1", &b)
	if b.ID != 1 || b.Title == "" {
		t.Fatalf("got %+v", b)
	}

	resp, body := f.do(t, "GET", "/v1/books/1/file", "", "")
	if resp.StatusCode != 200 || !bytes.HasPrefix(body, []byte("PK")) {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/epub+zip" {
		t.Errorf("content type %q", ct)
	}

	for path, want := range map[string]int{"/v1/books/999": 404, "/v1/books/abc": 400, "/v1/nope": 404} {
		if resp, _ := f.do(t, "GET", path, "", ""); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// A shelf edit reaches shelves.toml, the authority, not only the index.
func TestShelfEditsPersistToTOML(t *testing.T) {
	f := newFixture(t)

	resp, body := f.do(t, "POST", "/v1/shelves/to-read/books", "application/json", `{"ids":[1,3]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	data, err := os.ReadFile(f.shelfs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "book-0.epub") || !strings.Contains(string(data), "book-2.epub") {
		t.Errorf("shelves.toml lacks the added books:\n%s", data)
	}

	var d ShelfDetail
	f.getJSON(t, "/v1/shelves/to-read", &d)
	if d.Books != 2 || len(d.Contents) != 2 {
		t.Errorf("shelf: %+v", d)
	}

	if resp, _ := f.do(t, "DELETE", "/v1/shelves/to-read/books/1", "", ""); resp.StatusCode != 200 {
		t.Errorf("remove: %d", resp.StatusCode)
	}
	f.getJSON(t, "/v1/shelves/to-read", &d)
	if d.Books != 1 {
		t.Errorf("after remove: %d books", d.Books)
	}

	// A form post cannot set a JSON content type; refusing anything else
	// closes form-based CSRF.
	if resp, _ := f.do(t, "POST", "/v1/shelves/to-read/books", "application/x-www-form-urlencoded", `ids=1`); resp.StatusCode != 415 {
		t.Errorf("form post: %d, want 415", resp.StatusCode)
	}
	if resp, _ := f.do(t, "POST", "/v1/shelves/missing/books", "application/json", `{"ids":[1]}`); resp.StatusCode != 404 {
		t.Errorf("missing shelf: %d, want 404", resp.StatusCode)
	}
}

// When shelves.toml cannot be written the index is put back, so the next
// successful save cannot persist an edit the caller was told failed.
func TestShelfEditRollsBackWhenSaveFails(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.SaveShelves(); err != nil {
		t.Fatal(err)
	}
	f.svc.SaveShelves = func() error { return errors.New("disk full") }

	if _, err := f.svc.AddToShelf("to-read", []int64{1}); err == nil {
		t.Fatal("expected the save failure")
	}
	books, _ := f.db.ShelfBooks("to-read")
	if len(books) != 0 {
		t.Errorf("index kept %d books after a failed save", len(books))
	}
}

func TestStatsGenresProgress(t *testing.T) {
	f := newFixture(t)
	var st Stats
	f.getJSON(t, "/v1/stats", &st)
	if st.Books != 3 || st.Genres["fantasy"] != 1 || st.Version != "test" {
		t.Errorf("stats: %+v", st)
	}

	var g struct{ Genres []Count }
	f.getJSON(t, "/v1/genres", &g)
	if len(g.Genres) != 2 {
		t.Errorf("genres: %+v", g)
	}

	var p ProgressList
	f.getJSON(t, "/v1/progress", &p)
	if p.User != "reader" || len(p.Books) != 1 || p.Books[0].Book.Title != "Dune" || p.Books[0].Percentage != 42 {
		t.Errorf("progress: %+v", p)
	}
}

// The OpenAPI document and the route table must describe the same API; this
// is what keeps a generated skill from calling routes that are not there.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPIDoc, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Errorf("openapi version %q", doc.OpenAPI)
	}

	var documented, served []string
	for path, ops := range doc.Paths {
		for method := range ops {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	for _, rt := range routes {
		served = append(served, rt.method+" "+rt.pattern)
	}
	slices.Sort(documented)
	slices.Sort(served)
	if !slices.Equal(documented, served) {
		t.Errorf("documented:\n  %s\nserved:\n  %s", strings.Join(documented, "\n  "), strings.Join(served, "\n  "))
	}

	// Every $ref resolves.
	var all map[string]any
	json.Unmarshal(openAPIDoc, &all)
	for _, ref := range collectRefs(all) {
		parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
		var node any = all
		for _, p := range parts {
			m, ok := node.(map[string]any)
			if !ok {
				node = nil
				break
			}
			node = m[p]
		}
		if node == nil {
			t.Errorf("unresolved $ref %s", ref)
		}
	}
}

func collectRefs(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "$ref" {
				out = append(out, s)
			}
			out = append(out, collectRefs(val)...)
		}
	case []any:
		for _, val := range x {
			out = append(out, collectRefs(val)...)
		}
	}
	return out
}

// --- MCP ---

func (f *fixture) rpc(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	resp, b := f.do(t, "POST", "/mcp", "application/json", body)
	if resp.StatusCode == http.StatusAccepted {
		return resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("response %q: %v", b, err)
	}
	return resp.StatusCode, out
}

func TestMCPLifecycle(t *testing.T) {
	f := newFixture(t)

	_, init := f.rpc(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("negotiated %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability missing")
	}

	_, init = f.rpc(t, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := init["result"].(map[string]any)["protocolVersion"]; v != supportedProtocols[0] {
		t.Errorf("unknown version answered with %v", v)
	}

	if code, _ := f.rpc(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Errorf("notification: %d, want 202", code)
	}

	_, list := f.rpc(t, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	toolsOut := list["result"].(map[string]any)["tools"].([]any)
	if len(toolsOut) != len(tools) {
		t.Errorf("%d tools listed", len(toolsOut))
	}

	_, unknown := f.rpc(t, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)
	if unknown["error"].(map[string]any)["code"].(float64) != rpcMethodNotFound {
		t.Errorf("unknown method: %v", unknown)
	}

	_, batch := f.rpc(t, `[{"jsonrpc":"2.0","id":5,"method":"ping"}]`)
	if batch["error"] == nil {
		t.Error("a batch should be refused")
	}

	resp, _ := f.do(t, "GET", "/mcp", "", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp: %d, want 405", resp.StatusCode)
	}
}

func callTool(t *testing.T, f *fixture, name, args string) map[string]any {
	t.Helper()
	_, out := f.rpc(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args))
	if out["error"] != nil {
		t.Fatalf("%s: protocol error %v", name, out["error"])
	}
	return out["result"].(map[string]any)
}

func TestMCPTools(t *testing.T) {
	f := newFixture(t)

	r := callTool(t, f, "search_books", `{"query":"earthsea"}`)
	books := r["structuredContent"].(map[string]any)["books"].([]any)
	if len(books) != 1 || r["isError"] != false {
		t.Fatalf("search: %v", r)
	}
	if text := r["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "Earthsea") {
		t.Errorf("text content %q", text)
	}

	r = callTool(t, f, "add_to_shelf", `{"name":"to-read","ids":[2]}`)
	if r["structuredContent"].(map[string]any)["books"].(float64) != 1 {
		t.Errorf("add_to_shelf: %v", r)
	}

	r = callTool(t, f, "list_values", `{"dimension":"authors"}`)
	if len(r["structuredContent"].(map[string]any)["authors"].([]any)) != 2 {
		t.Errorf("authors: %v", r)
	}

	// Mistakes the model can fix come back as tool errors it can read.
	for name, args := range map[string]string{
		"get_book":     `{"id":999}`,
		"list_values":  `{"dimension":"planets"}`,
		"search_books": `{"bogus":1}`,
	} {
		if r := callTool(t, f, name, args); r["isError"] != true {
			t.Errorf("%s %s: isError=%v", name, args, r["isError"])
		}
	}

	_, out := f.rpc(t, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"rm_rf","arguments":{}}}`)
	if out["error"] == nil {
		t.Error("an unknown tool is a protocol error")
	}

	// Every tool's schema is an object schema, which is what clients require.
	for _, tl := range tools {
		if tl.schema["type"] != "object" {
			t.Errorf("%s: schema type %v", tl.name, tl.schema["type"])
		}
	}
}
