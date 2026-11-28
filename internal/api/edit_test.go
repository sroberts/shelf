package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
	"github.com/sroberts/shelf/internal/syncrun"
)

// realFixture is a library of real EPUBs, scanned the way `shelf scan` does,
// with uploads, metadata edits and a mounted card to sync to. The other
// fixture's books are placeholder bytes, which is enough for reading but not
// for anything that opens the file.
type realFixture struct {
	*fixture
	card string
	cfg  *config.Config
}

func writeEPUB(t *testing.T, path, title, author string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := epub.WriteSimple(f, epub.SimpleBook{
		Title: title, Authors: []string{author}, Date: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
		Description: "<p>A <b>fine</b> book.</p>", Paragraphs: []string{"Text."},
	}); err != nil {
		t.Fatal(err)
	}
}

func epubBytes(t *testing.T, title, author string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.epub")
	writeEPUB(t, path, title, author)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newRealFixture(t *testing.T) *realFixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	writeEPUB(t, filepath.Join(root, "Le Guin", "Earthsea.epub"), "A Wizard of Earthsea", "Ursula K. Le Guin")
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("plain text"), 0o644)

	db, err := library.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Scan(context.Background(), root, library.ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateShelf(library.Shelf{Name: "to-read", Kind: library.KindManual}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Paths = config.StoragePaths(filepath.Join(dir, "storage"))
	cfg.LibraryRoot = root
	cfg.NamingTemplate = "{author}/{title}"
	cfg.Sync.InterOpDelay = config.Duration{}
	card := filepath.Join(dir, "card")
	os.MkdirAll(card, 0o755)
	cfg.Devices = []config.Device{{Nickname: "card", Root: "/Books", Transport: config.TransportSD, Mount: card}}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cfg.Paths.ConfigFile(), []byte("library_root = "+fmt.Sprintf("%q", root)+"\n"), 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	syncSvc := NewSyncService(ctx, &cfg, cfg.Paths.ConfigFile())
	t.Cleanup(func() { cancel(); syncSvc.Wait() })

	shelves := filepath.Join(dir, "shelves.toml")
	svc := &Service{
		DB: db, LibraryRoot: root, Version: "test",
		Progress:       fakeProgress{"reader": {}},
		Genres:         fakeGenres{},
		SaveShelves:    func() error { return db.SaveShelves(shelves) },
		ReloadShelves:  func() error { return db.LoadShelves(shelves) },
		ImportTemplate: cfg.NamingTemplate,
		StageDir:       filepath.Join(dir, "stage"),
		Sync:           syncSvc,
	}
	auth := func(r *http.Request) (string, bool) {
		u, p, ok := r.BasicAuth()
		return u, ok && u == "reader" && p == "pw"
	}
	mux := http.NewServeMux()
	srv := NewServer(svc, auth, nil)
	srv.Info = Info{Auth: "basic", Features: Features{OPDS: true}}
	srv.Mount(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return &realFixture{fixture: &fixture{svc: svc, db: db, root: root, srv: hs, shelfs: shelves}, card: card, cfg: &cfg}
}

func (f *realFixture) book(t *testing.T, title string) *library.Book {
	t.Helper()
	books, err := f.db.Search(fmt.Sprintf("title:%q", title), library.SearchOptions{})
	if err != nil || len(books) != 1 {
		t.Fatalf("find %q: %d books, %v", title, len(books), err)
	}
	return books[0]
}

func TestMeDescribesTheServer(t *testing.T) {
	f := newRealFixture(t)
	var me Me
	f.getJSON(t, "/v1/me", &me)
	if me.User != "reader" || me.Auth != "basic" || me.ProgressUser != "reader" {
		t.Errorf("me = %+v", me)
	}
	want := Features{AI: true, OPDS: true, Sync: true, Upload: true, Progress: true}
	if me.Features != want {
		t.Errorf("features = %+v, want %+v", me.Features, want)
	}
}

func TestPatchBookRewritesTheFileInPlace(t *testing.T) {
	f := newRealFixture(t)
	before := f.book(t, "A Wizard of Earthsea")
	f.svc.Genres.(fakeGenres)[before.SHA256] = "fantasy"

	resp, body := f.do(t, "PATCH", fmt.Sprintf("/v1/books/%d", before.ID), "application/json",
		`{"title":"The Wizard of Earthsea","authors":["Ursula Le Guin"," "],"series":"Earthsea","series_index":1,"tags":["fantasy","classic"]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	var got Book
	json.Unmarshal(body, &got)
	if got.Title != "The Wizard of Earthsea" || got.Series != "Earthsea" || len(got.Authors) != 1 || len(got.Tags) != 2 {
		t.Errorf("patched book = %+v", got)
	}
	// The file did not move: its path is where the reader has it.
	if got.Path != "Le Guin/Earthsea.epub" {
		t.Errorf("path = %q; a metadata edit must not move the file", got.Path)
	}
	// What changed is in the file, not only the index.
	ef, err := epub.Open(before.Path)
	if err != nil {
		t.Fatal(err)
	}
	if title := ef.Metadata().Title; title != "The Wizard of Earthsea" {
		t.Errorf("file title = %q", title)
	}
	ef.Close()
	// The genre followed the book to its new hash rather than being paid for again.
	if got.SHA256 == before.SHA256 || got.Genre != "fantasy" {
		t.Errorf("sha changed %v, genre %q", got.SHA256 != before.SHA256, got.Genre)
	}
	if got.Description != "A fine book." {
		t.Errorf("description = %q, want markup removed", got.Description)
	}

	txt := f.book(t, "notes")
	for name, tc := range map[string]struct{ path, body string }{
		"non-epub":      {fmt.Sprintf("/v1/books/%d", txt.ID), `{"title":"x"}`},
		"empty patch":   {fmt.Sprintf("/v1/books/%d", got.ID), `{}`},
		"unknown field": {fmt.Sprintf("/v1/books/%d", got.ID), `{"colour":"red"}`},
		"blank title":   {fmt.Sprintf("/v1/books/%d", got.ID), `{"title":"  "}`},
	} {
		if resp, body := f.do(t, "PATCH", tc.path, "application/json", tc.body); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s, want 400", name, resp.StatusCode, body)
		}
	}
}

func upload(t *testing.T, f *realFixture, files map[string][]byte) (*http.Response, ImportResult) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", "ignored")
	for name, data := range files {
		w, _ := mw.CreateFormFile("file", name)
		w.Write(data)
	}
	mw.Close()
	resp, body := f.do(t, "POST", "/v1/books", mw.FormDataContentType(), buf.String())
	var out ImportResult
	json.Unmarshal(body, &out)
	return resp, out
}

func TestUploadPlacesFilesByTheTemplate(t *testing.T) {
	f := newRealFixture(t)
	existing, _ := os.ReadFile(f.book(t, "A Wizard of Earthsea").Path)

	resp, res := upload(t, f, map[string][]byte{
		"../../dune.epub": epubBytes(t, "Dune", "Frank Herbert"),
		"copy.epub":       existing,
		"virus.exe":       []byte("MZ"),
	})
	if resp.StatusCode != 200 {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	if len(res.Imported) != 1 || res.Imported[0].Title != "Dune" {
		t.Fatalf("imported = %+v", res.Imported)
	}
	// The client's "../.." is dropped; the template decides where it goes.
	if res.Imported[0].Path != "Frank Herbert/Dune.epub" {
		t.Errorf("placed at %q", res.Imported[0].Path)
	}
	if _, err := os.Stat(filepath.Join(f.root, "Frank Herbert", "Dune.epub")); err != nil {
		t.Errorf("not on disk: %v", err)
	}
	if len(res.Failed) != 2 {
		t.Fatalf("failed = %+v, want the duplicate and the .exe", res.Failed)
	}
	for _, fl := range res.Failed {
		if fl.Name == "copy.epub" && !strings.Contains(fl.Error, "already in the library") {
			t.Errorf("duplicate refused with %q", fl.Error)
		}
	}
	// Nothing is left in staging.
	if entries, _ := os.ReadDir(f.svc.StageDir); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}

	old := maxUpload
	maxUpload = 1 << 10
	t.Cleanup(func() { maxUpload = old })
	if resp, _ := upload(t, f, map[string][]byte{"big.epub": bytes.Repeat([]byte("x"), 4<<10)}); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: %d, want 413", resp.StatusCode)
	}
	if resp, _ := f.do(t, "POST", "/v1/books", "application/json", `{}`); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("JSON upload: %d, want 415", resp.StatusCode)
	}
}

func TestShelfCreateAndDelete(t *testing.T) {
	f := newRealFixture(t)
	resp, body := f.do(t, "POST", "/v1/shelves", "application/json", `{"name":"fantasy","query":"author:\"le guin\""}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var sh Shelf
	json.Unmarshal(body, &sh)
	if sh.Kind != "query" || sh.Books != 1 {
		t.Errorf("shelf = %+v", sh)
	}
	if data, _ := os.ReadFile(f.shelfs); !strings.Contains(string(data), "fantasy") {
		t.Errorf("shelves.toml does not have the new shelf:\n%s", data)
	}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"taken":     {`{"name":"to-read"}`, 409},
		"bad query": {`{"name":"x","query":"(tag:a"}`, 400},
		"slash":     {`{"name":"a/b"}`, 400},
		"no name":   {`{"query":"tag:x"}`, 400},
	} {
		if resp, body := f.do(t, "POST", "/v1/shelves", "application/json", tc.body); resp.StatusCode != tc.code {
			t.Errorf("%s: %d %s, want %d", name, resp.StatusCode, body, tc.code)
		}
	}

	if resp, _ := f.do(t, "DELETE", "/v1/shelves/fantasy", "", ""); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if data, _ := os.ReadFile(f.shelfs); strings.Contains(string(data), "fantasy") {
		t.Errorf("shelves.toml still has the deleted shelf:\n%s", data)
	}
	if resp, _ := f.do(t, "DELETE", "/v1/shelves/fantasy", "", ""); resp.StatusCode != 404 {
		t.Errorf("second delete: %d, want 404", resp.StatusCode)
	}
}

func TestClearProgress(t *testing.T) {
	f := newRealFixture(t)
	b := f.book(t, "A Wizard of Earthsea")
	f.svc.Progress.(fakeProgress)["reader"][b.DocID] = kosync.Progress{Document: b.DocID, Percentage: 0.5, Device: "X4", Timestamp: 1}

	var got Book
	f.getJSON(t, fmt.Sprintf("/v1/books/%d", b.ID), &got)
	if got.Reading == nil || got.Reading.Percent != 50 || got.Reading.Device != "X4" {
		t.Fatalf("reading = %+v", got.Reading)
	}

	path := fmt.Sprintf("/v1/progress/%d", b.ID)
	if resp, body := f.do(t, "DELETE", path, "", ""); resp.StatusCode != 204 {
		t.Fatalf("clear: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.do(t, "DELETE", path, "", ""); resp.StatusCode != 404 {
		t.Errorf("clear again: %d, want 404", resp.StatusCode)
	}
}

func TestCoverIsNotFoundWithoutOne(t *testing.T) {
	f := newRealFixture(t)
	b := f.book(t, "notes")
	if resp, _ := f.do(t, "GET", fmt.Sprintf("/v1/books/%d/cover", b.ID), "", ""); resp.StatusCode != 404 {
		t.Errorf("cover: %d, want 404", resp.StatusCode)
	}
}

func waitJob(t *testing.T, f *realFixture, id string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var j Job
		f.getJSON(t, "/v1/jobs/"+id, &j)
		if j.State != JobPlanning && j.State != JobRunning {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", j.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startJob(t *testing.T, f *realFixture, body string) Job {
	t.Helper()
	resp, b := f.do(t, "POST", "/v1/devices/card/sync", "application/json", body)
	if resp.StatusCode != 202 {
		t.Fatalf("start: %d %s", resp.StatusCode, b)
	}
	var j Job
	json.Unmarshal(b, &j)
	return j
}

func TestSyncJobPreviewsThenRuns(t *testing.T) {
	f := newRealFixture(t)

	var devs struct{ Devices []Device }
	f.getJSON(t, "/v1/devices", &devs)
	if len(devs.Devices) != 1 || devs.Devices[0].Name != "card" || !devs.Devices[0].Default || devs.Devices[0].LastSync != 0 {
		t.Fatalf("devices = %+v", devs.Devices)
	}

	preview := waitJob(t, f, startJob(t, f, `{"dry_run":true}`).ID)
	if preview.State != JobDone || preview.Plan == nil || preview.Plan.Uploads != 2 {
		t.Fatalf("preview = %+v plan %+v", preview, preview.Plan)
	}
	if entries, _ := os.ReadDir(f.card); len(entries) != 0 {
		t.Errorf("a preview wrote to the card: %v", entries)
	}

	run := waitJob(t, f, startJob(t, f, `{}`).ID)
	if run.State != JobDone || run.Result == nil || run.Result.Failed != 0 {
		t.Fatalf("run = %+v result %+v", run, run.Result)
	}
	if _, err := os.Stat(filepath.Join(f.card, "Books", "Ursula K. Le Guin", "A Wizard of Earthsea.epub")); err != nil {
		t.Errorf("book not on the card: %v", err)
	}

	f.getJSON(t, "/v1/devices", &devs)
	if devs.Devices[0].LastSync == 0 || devs.Devices[0].Books != 2 {
		t.Errorf("after sync: %+v", devs.Devices[0])
	}
	var jobs struct{ Jobs []Job }
	f.getJSON(t, "/v1/jobs", &jobs)
	if len(jobs.Jobs) != 2 || jobs.Jobs[0].ID != run.ID {
		t.Errorf("jobs = %d, newest %v", len(jobs.Jobs), jobs.Jobs)
	}

	for name, tc := range map[string]struct {
		path, body string
		code       int
	}{
		"unknown device": {"/v1/devices/nope/sync", `{}`, 404},
		"unknown shelf":  {"/v1/devices/card/sync", `{"shelf":"nope"}`, 404},
		"repath refused": {"/v1/devices/card/sync", `{"repath":true}`, 400},
	} {
		if resp, b := f.do(t, "POST", tc.path, "application/json", tc.body); resp.StatusCode != tc.code {
			t.Errorf("%s: %d %s, want %d", name, resp.StatusCode, b, tc.code)
		}
	}
}

// blockingSession holds Execute until released or cancelled, so a test can
// see a job while it runs.
type blockingSession struct{ release chan struct{} }

func (blockingSession) PlanOf() syncpkg.Plan {
	return syncpkg.Plan{Ops: []syncpkg.Op{{Kind: syncpkg.OpUpload}}}
}
func (blockingSession) NotesOf() []string { return nil }
func (b blockingSession) Execute(ctx context.Context, events func(syncpkg.Event)) (*syncpkg.Result, error) {
	events(syncpkg.Event{Kind: syncpkg.EventStart, Index: 1, Total: 1})
	select {
	case <-b.release:
		return &syncpkg.Result{Completed: 1}, nil
	case <-ctx.Done():
		return &syncpkg.Result{}, ctx.Err()
	}
}

func TestOneSyncPerDeviceAndCancel(t *testing.T) {
	f := newRealFixture(t)
	release := make(chan struct{})
	f.svc.Sync.Prepare = func(context.Context, *config.Config, config.Device, []*library.Book, syncrun.Options) (Session, error) {
		return blockingSession{release}, nil
	}

	first := startJob(t, f, `{}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var j Job
		f.getJSON(t, "/v1/jobs/"+first.ID, &j)
		if j.State == JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ran: %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, body := f.do(t, "POST", "/v1/devices/card/sync", "application/json", `{}`)
	var busy map[string]string
	json.Unmarshal(body, &busy)
	if resp.StatusCode != 409 || busy["job"] != first.ID {
		t.Errorf("second sync: %d %s, want 409 naming %s", resp.StatusCode, body, first.ID)
	}

	if resp, _ := f.do(t, "DELETE", "/v1/jobs/"+first.ID, "", ""); resp.StatusCode != 202 {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	if j := waitJob(t, f, first.ID); j.State != JobCancelled || j.Error != "" {
		t.Errorf("after cancel: %s %q", j.State, j.Error)
	}
	// The device is free again.
	next := startJob(t, f, `{}`)
	close(release)
	if j := waitJob(t, f, next.ID); j.State != JobDone {
		t.Errorf("next job: %s %s", j.State, j.Error)
	}
}

func TestAddDeviceAppendsToTheConfig(t *testing.T) {
	f := newRealFixture(t)
	resp, body := f.do(t, "POST", "/v1/devices", "application/json", `{"name":"x4","host":"192.168.1.50"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	data, _ := os.ReadFile(f.cfg.Paths.ConfigFile())
	if !strings.Contains(string(data), `nickname = "x4"`) || !strings.Contains(string(data), "library_root") {
		t.Errorf("config.toml:\n%s", data)
	}
	if resp, _ := f.do(t, "POST", "/v1/devices", "application/json", `{"name":"x4","host":"10.0.0.1"}`); resp.StatusCode != 409 {
		t.Errorf("duplicate: %d, want 409", resp.StatusCode)
	}
	if resp, _ := f.do(t, "POST", "/v1/devices", "application/json", `{"name":"y"}`); resp.StatusCode != 400 {
		t.Errorf("no host: %d, want 400", resp.StatusCode)
	}
	// The name becomes a file name in the state directory, and the host is
	// pasted into a URL: neither may carry anything that changes where.
	for _, bad := range []string{
		`{"name":"../../escape","host":"10.0.0.2"}`,
		`{"name":"z","host":"10.0.0.2@evil.example"}`,
		`{"name":"z","host":"10.0.0.2?x"}`,
	} {
		if resp, body := f.do(t, "POST", "/v1/devices", "application/json", bad); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s, want 400", bad, resp.StatusCode, body)
		}
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Paths.State, "..", "escape.json")); err == nil {
		t.Error("a manifest was written outside the state directory")
	}
}

func TestMCPSyncDefaultsToAPreview(t *testing.T) {
	f := newRealFixture(t)
	r := callTool(t, f.fixture, "sync_to_device", `{}`)
	if r["isError"] != false {
		t.Fatalf("sync_to_device: %v", r)
	}
	id := r["structuredContent"].(map[string]any)["id"].(string)
	if j := waitJob(t, f, id); !j.DryRun || j.State != JobDone {
		t.Errorf("job = %+v; an agent's first call must only plan", j)
	}
	if entries, _ := os.ReadDir(f.card); len(entries) != 0 {
		t.Errorf("preview wrote to the card: %v", entries)
	}

	b := f.book(t, "A Wizard of Earthsea")
	r = callTool(t, f.fixture, "update_book_metadata", fmt.Sprintf(`{"id":%d,"tags":["mcp"]}`, b.ID))
	if r["isError"] != false || !strings.Contains(fmt.Sprint(r["structuredContent"]), "mcp") {
		t.Errorf("update_book_metadata: %v", r)
	}
	r = callTool(t, f.fixture, "create_shelf", `{"name":"to-read"}`)
	if r["isError"] != true || !strings.Contains(fmt.Sprint(r["content"]), "already exists") {
		t.Errorf("create_shelf on a taken name: %v", r)
	}
}
