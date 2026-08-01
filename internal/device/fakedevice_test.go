package device

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
)

// fakeDevice is an httptest server that mimics the documented CrossPoint HTTP
// surface, including its quirks: error strings returned with a 200 status, a
// listing that omits mtime and checksums, and rejection of protected paths.
//
// Behaviour here is taken from the firmware's docs/webserver-endpoints.md.
// Where the real hardware disagrees, the fixtures captured from a device in
// Phase 0 are authoritative and this file should be corrected to match.
type fakeDevice struct {
	mu sync.Mutex

	status Status
	// files maps a full device path to its contents. Directories are implied
	// by the paths of the files inside them, plus explicit entries in dirs.
	files map[string][]byte
	dirs  map[string]bool

	// Failure injection.
	failStatus   bool
	failNextPost string // error string to return from the next mutating call

	server *httptest.Server
}

func newFakeDevice(t *testing.T) *fakeDevice {
	t.Helper()

	d := &fakeDevice{
		status: Status{
			Version: TestedVersion, IP: "192.168.1.42", Mode: "STA",
			RSSI: -55, FreeHeap: 142000, Uptime: 3600, Device: ModelX4,
		},
		files: map[string][]byte{},
		dirs:  map[string]bool{"/": true},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", d.handleStatus)
	mux.HandleFunc("/api/files", d.handleFiles)
	mux.HandleFunc("/download", d.handleDownload)
	mux.HandleFunc("/mkdir", d.handleMkdir)
	mux.HandleFunc("/rename", d.handleRename)
	mux.HandleFunc("/move", d.handleMove)
	mux.HandleFunc("/delete", d.handleDelete)

	d.server = httptest.NewServer(mux)
	t.Cleanup(d.server.Close)
	return d
}

// client returns a Client pointed at the fake.
func (d *fakeDevice) client() *Client {
	return New(strings.TrimPrefix(d.server.URL, "http://"))
}

// addFile registers a file, creating its parent directories.
func (d *fakeDevice) addFile(p string, body []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[p] = body
	for dir := path.Dir(p); dir != "/" && dir != "."; dir = path.Dir(dir) {
		d.dirs[dir] = true
	}
}

func (d *fakeDevice) addDir(p string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirs[p] = true
}

func (d *fakeDevice) handleStatus(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	fail := d.failStatus
	s := d.status
	d.mu.Unlock()

	if fail {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func (d *fakeDevice) handleFiles(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/"
	}
	dir = path.Clean(dir)

	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.dirs[dir] {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	seen := map[string]bool{}
	entries := []FileEntry{}

	for p, body := range d.files {
		if path.Dir(p) != dir {
			continue
		}
		name := path.Base(p)
		entries = append(entries, FileEntry{
			Name: name, Size: int64(len(body)),
			IsEpub: strings.HasSuffix(strings.ToLower(name), ".epub"),
		})
		seen[name] = true
	}
	for p := range d.dirs {
		if p == "/" || path.Dir(p) != dir {
			continue
		}
		name := path.Base(p)
		if seen[name] {
			continue
		}
		entries = append(entries, FileEntry{Name: name, IsDirectory: true})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (d *fakeDevice) handleDownload(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(r.URL.Query().Get("path"))

	d.mu.Lock()
	body, ok := d.files[p]
	d.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(p, ".epub") {
		w.Header().Set("Content-Type", "application/epub+zip")
	}
	w.Write(body)
}

// takeInjectedFailure returns and clears a pending injected error.
func (d *fakeDevice) takeInjectedFailure() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	msg := d.failNextPost
	d.failNextPost = ""
	return msg
}

func (d *fakeDevice) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if msg := d.takeInjectedFailure(); msg != "" {
		fmt.Fprintf(w, "ERROR:%s", msg) // 200 with an error body, as the firmware does
		return
	}
	r.ParseForm()
	parent := path.Clean(orDefault(r.FormValue("path"), "/"))
	name := r.FormValue("name")
	if name == "" {
		fmt.Fprint(w, "ERROR:Invalid name")
		return
	}
	full := path.Join(parent, name)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dirs[full] {
		fmt.Fprint(w, "ERROR:Directory already exists")
		return
	}
	d.dirs[full] = true
	fmt.Fprint(w, "Directory created")
}

func (d *fakeDevice) handleRename(w http.ResponseWriter, r *http.Request) {
	if msg := d.takeInjectedFailure(); msg != "" {
		fmt.Fprintf(w, "ERROR:%s", msg)
		return
	}
	r.ParseForm()
	from := path.Clean(r.FormValue("path"))
	name := r.FormValue("name")

	d.mu.Lock()
	defer d.mu.Unlock()
	body, ok := d.files[from]
	if !ok {
		fmt.Fprint(w, "ERROR:File not found")
		return
	}
	delete(d.files, from)
	d.files[path.Join(path.Dir(from), name)] = body
	fmt.Fprint(w, "Renamed")
}

func (d *fakeDevice) handleMove(w http.ResponseWriter, r *http.Request) {
	if msg := d.takeInjectedFailure(); msg != "" {
		fmt.Fprintf(w, "ERROR:%s", msg)
		return
	}
	r.ParseForm()
	from := path.Clean(r.FormValue("path"))
	to := path.Clean(r.FormValue("dest"))

	d.mu.Lock()
	defer d.mu.Unlock()
	body, ok := d.files[from]
	if !ok {
		fmt.Fprint(w, "ERROR:File not found")
		return
	}
	delete(d.files, from)
	d.files[to] = body
	fmt.Fprint(w, "Moved")
}

func (d *fakeDevice) handleDelete(w http.ResponseWriter, r *http.Request) {
	if msg := d.takeInjectedFailure(); msg != "" {
		fmt.Fprintf(w, "ERROR:%s", msg)
		return
	}
	r.ParseForm()

	var targets []string
	if list := r.FormValue("paths"); list != "" {
		if err := json.Unmarshal([]byte(list), &targets); err != nil {
			fmt.Fprint(w, "ERROR:Invalid paths")
			return
		}
	} else if p := r.FormValue("path"); p != "" {
		targets = []string{p}
	} else {
		fmt.Fprint(w, "ERROR:No path given")
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range targets {
		t = path.Clean(t)
		if _, ok := d.files[t]; ok {
			delete(d.files, t)
			continue
		}
		if d.dirs[t] {
			// The firmware refuses to delete a non-empty directory.
			for p := range d.files {
				if strings.HasPrefix(p, t+"/") {
					fmt.Fprint(w, "ERROR:Directory not empty")
					return
				}
			}
			delete(d.dirs, t)
			continue
		}
		fmt.Fprint(w, "ERROR:File not found")
		return
	}
	fmt.Fprint(w, "Deleted")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// requestedPaths records the query path of each request, for assertions about
// what the client actually asked for.
func (d *fakeDevice) url(endpoint string, q url.Values) string {
	return d.server.URL + endpoint + "?" + q.Encode()
}
