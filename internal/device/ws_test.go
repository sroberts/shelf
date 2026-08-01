package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsPeer is a fake device WebSocket endpoint that replays the documented
// START/READY/PROGRESS/DONE/ERROR sequence, with hooks to inject each
// documented failure.
type wsPeer struct {
	mu sync.Mutex

	// Injected behaviour.
	errorOnStart  string // reply with ERROR instead of READY
	errorMidway   string // reply with ERROR after this many bytes
	errorAtBytes  int64
	refuseUpgrade bool  // fail the handshake, exercising the HTTP fallback
	silentReady   bool  // never send READY
	progressEvery int64 // bytes between PROGRESS frames

	// Observed.
	startFrame string
	received   []byte
	done       bool

	// Concurrency tracking. The firmware accepts one upload at a time, so
	// maxConcurrent is the direct measure of whether the client serializes.
	active        int
	maxConcurrent int

	server *httptest.Server
}

// enter and leave bracket a connection for concurrency accounting.
func (p *wsPeer) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active++
	if p.active > p.maxConcurrent {
		p.maxConcurrent = p.active
	}
}

func (p *wsPeer) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
}

func (p *wsPeer) peakConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxConcurrent
}

func newWSPeer(t *testing.T) *wsPeer {
	t.Helper()
	p := &wsPeer{progressEvery: 64 * 1024}

	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)
	return p
}

// host returns the host:port the peer listens on.
func (p *wsPeer) host() string { return strings.TrimPrefix(p.server.URL, "http://") }

// client builds a Client whose WebSocket URL points at this peer.
//
// The real device serves HTTP on 80 and WebSocket on 81. httptest gives one
// port, so wsURL is overridden to reuse it.
func (p *wsPeer) client() *Client {
	c := New(p.host())
	c.wsURLOverride = p.server.URL
	return c
}

func (p *wsPeer) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	refuse := p.refuseUpgrade
	p.mu.Unlock()

	if refuse {
		http.Error(w, "no websocket here", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	// Close gracefully, as the firmware does after its final frame, so the
	// client has a chance to read the verdict before the socket goes away.
	defer conn.Close(websocket.StatusNormalClosure, "")

	p.enter()
	defer p.leave()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Expect a START frame first.
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	p.mu.Lock()
	p.startFrame = string(data)
	errStart := p.errorOnStart
	silent := p.silentReady
	p.mu.Unlock()

	if errStart != "" {
		conn.Write(ctx, websocket.MessageText, []byte("ERROR:"+errStart))
		return
	}
	if silent {
		<-ctx.Done()
		return
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte("READY")); err != nil {
		return
	}

	total := parseStartSize(string(data))

	// Per-connection state. Sharing a buffer across connections would make the
	// peer close early under concurrent uploads and produce phantom failures.
	var body []byte
	var received, lastProgress int64

	for {
		typ, chunk, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}

		body = append(body, chunk...)
		received = int64(len(body))

		p.mu.Lock()
		p.received = body // latest connection's bytes, for single-upload assertions
		errMid, at := p.errorMidway, p.errorAtBytes
		every := p.progressEvery
		p.mu.Unlock()

		if errMid != "" && received >= at {
			conn.Write(ctx, websocket.MessageText, []byte("ERROR:"+errMid))
			return
		}

		if every > 0 && received-lastProgress >= every {
			lastProgress = received
			conn.Write(ctx, websocket.MessageText,
				[]byte(fmt.Sprintf("PROGRESS:%d:%d", received, total)))
		}

		if received >= total {
			conn.Write(ctx, websocket.MessageText,
				[]byte(fmt.Sprintf("PROGRESS:%d:%d", received, total)))
			conn.Write(ctx, websocket.MessageText, []byte("DONE"))

			p.mu.Lock()
			p.done = true
			p.mu.Unlock()
			return
		}
	}
}

// parseStartSize extracts the declared size from a START frame.
func parseStartSize(frame string) int64 {
	parts := strings.Split(frame, ":")
	if len(parts) < 3 {
		return 0
	}
	var n int64
	fmt.Sscanf(parts[2], "%d", &n)
	return n
}

func (p *wsPeer) body() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.received...)
}

func (p *wsPeer) start() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startFrame
}

// --- tests ---

func TestWSUploadSuccess(t *testing.T) {
	p := newWSPeer(t)
	body := bytes.Repeat([]byte("shelf"), 20_000) // 100 KB

	var lastProgress Progress
	err := p.client().Upload(context.Background(),
		NewPath("/Books/Le Guin/Earthsea.epub"),
		bytes.NewReader(body), int64(len(body)),
		UploadOptions{ChunkSize: 16 * 1024, Progress: func(pr Progress) { lastProgress = pr }})
	if err != nil {
		t.Fatal(err)
	}

	if got := p.body(); !bytes.Equal(got, body) {
		t.Errorf("device received %d bytes, want %d", len(got), len(body))
	}

	// The START frame carries the base name, the size, and the directory.
	wantStart := fmt.Sprintf("START:Earthsea.epub:%d:/Books/Le Guin", len(body))
	if p.start() != wantStart {
		t.Errorf("START frame = %q, want %q", p.start(), wantStart)
	}

	if lastProgress.Sent != int64(len(body)) || lastProgress.Total != int64(len(body)) {
		t.Errorf("final progress = %+v, want %d/%d", lastProgress, len(body), len(body))
	}
	if lastProgress.Percent() != 100 {
		t.Errorf("Percent = %v, want 100", lastProgress.Percent())
	}
}

func TestWSUploadReportsProgress(t *testing.T) {
	p := newWSPeer(t)
	p.progressEvery = 32 * 1024
	body := bytes.Repeat([]byte("x"), 200*1024)

	var updates []Progress
	var mu sync.Mutex
	err := p.client().Upload(context.Background(), NewPath("/Books/big.epub"),
		bytes.NewReader(body), int64(len(body)),
		UploadOptions{ChunkSize: 16 * 1024, Progress: func(pr Progress) {
			mu.Lock()
			updates = append(updates, pr)
			mu.Unlock()
		}})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) < 2 {
		t.Fatalf("got %d progress updates, want several", len(updates))
	}
	// Progress must be monotonic and end at the full size.
	for i := 1; i < len(updates); i++ {
		if updates[i].Sent < updates[i-1].Sent {
			t.Errorf("progress went backwards: %d then %d", updates[i-1].Sent, updates[i].Sent)
		}
	}
	if last := updates[len(updates)-1]; last.Sent != int64(len(body)) {
		t.Errorf("final progress = %d, want %d", last.Sent, len(body))
	}
}

// Every documented ERROR string must surface as its typed error, and a
// device-level refusal must not silently fall back to HTTP.
func TestWSUploadErrorStrings(t *testing.T) {
	tests := []struct {
		msg  string
		want error
	}{
		{"Upload already in progress", ErrUploadInProgress},
		{"Invalid START format", ErrInvalidStart},
		{"Failed to create file", ErrCreateFailed},
		{"No upload in progress", ErrNoUpload},
		{"Upload overflow", ErrOverflow},
		{"Write failed - disk full?", ErrDiskFull},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			p := newWSPeer(t)
			p.errorOnStart = tt.msg

			err := p.client().Upload(context.Background(), NewPath("/Books/a.epub"),
				bytes.NewReader([]byte("data")), 4, UploadOptions{})

			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			// A device that answered must not trigger the HTTP fallback.
			if errors.Is(err, errWSUnavailable) {
				t.Error("a device-level error must not be treated as transport unavailability")
			}
		})
	}
}

// A failure partway through must be reported, not silently truncated.
func TestWSUploadErrorMidTransfer(t *testing.T) {
	p := newWSPeer(t)
	p.errorMidway = "Write failed - disk full?"
	p.errorAtBytes = 32 * 1024
	p.progressEvery = 8 * 1024

	body := bytes.Repeat([]byte("y"), 512*1024)
	err := p.client().Upload(context.Background(), NewPath("/Books/big.epub"),
		bytes.NewReader(body), int64(len(body)), UploadOptions{ChunkSize: 8 * 1024})

	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("err = %v, want ErrDiskFull", err)
	}
	if !IsFatal(err) {
		t.Error("a full disk must abort the whole sync run")
	}
}

// When the WebSocket handshake fails outright, the client falls back to
// POST /upload rather than failing the transfer.
func TestUploadFallsBackToHTTP(t *testing.T) {
	var (
		mu           sync.Mutex
		gotUpload    bool
		gotBody      []byte
		gotPathParam string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotUpload = true
		gotPathParam = r.URL.Query().Get("path")

		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		buf := new(bytes.Buffer)
		buf.ReadFrom(file)
		gotBody = buf.Bytes()

		fmt.Fprint(w, "File uploaded successfully: test.epub")
	})
	// Anything else fails the WebSocket upgrade.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a websocket endpoint", http.StatusBadRequest)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(strings.TrimPrefix(srv.URL, "http://"))
	c.wsURLOverride = srv.URL

	body := []byte("epub bytes here")
	err := c.Upload(context.Background(), NewPath("/Books/test.epub"),
		bytes.NewReader(body), int64(len(body)), UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotUpload {
		t.Fatal("the HTTP fallback was never used")
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
	if gotPathParam != "/Books" {
		t.Errorf("path parameter = %q, want the destination directory", gotPathParam)
	}
}

// A source that cannot be rewound must not be uploaded twice or truncated.
func TestUploadFallbackNeedsSeekableSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no websocket", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(strings.TrimPrefix(srv.URL, "http://"))
	c.wsURLOverride = srv.URL

	// A bare Reader is not a Seeker.
	nonSeekable := struct{ *bytes.Buffer }{bytes.NewBufferString("data")}
	err := c.Upload(context.Background(), NewPath("/Books/a.epub"), nonSeekable, 4, UploadOptions{})
	if err == nil {
		t.Fatal("expected an error when the source cannot be re-read")
	}
	if !strings.Contains(err.Error(), "cannot be re-read") {
		t.Errorf("err = %v", err)
	}
}

func TestUploadRefusesProtectedPaths(t *testing.T) {
	p := newWSPeer(t)
	for _, dest := range []string{
		"/.crosspoint/settings.json",
		"/XTCache/x.bin",
		"/System Volume Information/a",
	} {
		err := p.client().Upload(context.Background(), NewPath(dest),
			bytes.NewReader([]byte("x")), 1, UploadOptions{})
		if !errors.Is(err, ErrProtectedPath) {
			t.Errorf("Upload(%s) = %v, want ErrProtectedPath", dest, err)
		}
	}
}

// A size that disagrees with the bytes actually read must be caught locally,
// with a clearer message than the device's "Upload overflow".
func TestUploadRejectsSizeMismatch(t *testing.T) {
	p := newWSPeer(t)

	body := []byte("only twelve")
	err := p.client().Upload(context.Background(), NewPath("/Books/a.epub"),
		bytes.NewReader(body), 9999, UploadOptions{})
	if !errors.Is(err, ErrInvalidStart) {
		t.Errorf("err = %v, want ErrInvalidStart", err)
	}
}

func TestUploadRejectsNegativeSize(t *testing.T) {
	p := newWSPeer(t)
	err := p.client().Upload(context.Background(), NewPath("/Books/a.epub"),
		bytes.NewReader(nil), -1, UploadOptions{})
	if !errors.Is(err, ErrInvalidStart) {
		t.Errorf("err = %v, want ErrInvalidStart", err)
	}
}

// The firmware accepts exactly one upload at a time and rejects a second with
// "Upload already in progress". Serialization therefore lives in the client, so
// that no caller -- including a future concurrent sync planner -- can violate
// it. The peer counts overlapping connections, which measures that directly.
func TestUploadsAreSerialized(t *testing.T) {
	p := newWSPeer(t)
	c := p.client()

	const n = 6
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		failures []error
	)

	body := bytes.Repeat([]byte("z"), 64*1024)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			err := c.Upload(context.Background(),
				NewPath(fmt.Sprintf("/Books/b%d.epub", i)),
				bytes.NewReader(body), int64(len(body)),
				UploadOptions{ChunkSize: 8 * 1024})

			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	for _, err := range failures {
		t.Errorf("concurrent upload failed: %v", err)
	}
	mu.Unlock()

	if peak := p.peakConcurrency(); peak > 1 {
		t.Errorf("%d uploads overlapped on the device; the client must serialize them", peak)
	}
}

func TestChunkSizeClamping(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, DefaultChunkSize},
		{100, DefaultChunkSize},      // below the device write buffer
		{MinChunkSize, MinChunkSize}, //
		{16 * 1024, 16 * 1024},       //
		{MaxChunkSize, MaxChunkSize}, //
		{1 << 20, MaxChunkSize},      // above the device RAM budget
		{-5, DefaultChunkSize},       //
	}
	for _, tt := range tests {
		if got := (UploadOptions{ChunkSize: tt.in}).chunkSize(); got != tt.want {
			t.Errorf("chunkSize(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseProgress(t *testing.T) {
	tests := []struct {
		msg             string
		received, total int64
		ok              bool
	}{
		{"PROGRESS:1024:65536", 1024, 65536, true},
		{"PROGRESS:0:0", 0, 0, true},
		{"PROGRESS: 512 : 1024 ", 512, 1024, true},
		{"DONE", 0, 0, false},
		{"PROGRESS:abc:1", 0, 0, false},
		{"PROGRESS:1", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		got, ok := parseProgress(tt.msg)
		if ok != tt.ok {
			t.Errorf("parseProgress(%q) ok = %v, want %v", tt.msg, ok, tt.ok)
			continue
		}
		if ok && (got.received != tt.received || got.total != tt.total) {
			t.Errorf("parseProgress(%q) = %+v", tt.msg, got)
		}
	}
}

func TestUploadRespectsContextCancellation(t *testing.T) {
	p := newWSPeer(t)
	p.silentReady = true // never send READY

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := p.client().Upload(ctx, NewPath("/Books/a.epub"),
		bytes.NewReader([]byte("data")), 4, UploadOptions{})
	if err == nil {
		t.Fatal("expected an error when the device never answers")
	}
}

func TestForceHTTPSkipsWebSocket(t *testing.T) {
	var gotUpload bool
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		gotUpload = true
		fmt.Fprint(w, "File uploaded successfully")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(strings.TrimPrefix(srv.URL, "http://"))
	body := []byte("data")
	err := c.Upload(context.Background(), NewPath("/Books/a.epub"),
		bytes.NewReader(body), int64(len(body)), UploadOptions{ForceHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if !gotUpload {
		t.Error("ForceHTTP did not use the HTTP endpoint")
	}
}
