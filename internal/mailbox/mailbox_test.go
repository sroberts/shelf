package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/library"
)

const testToken = "omail_secret"

// fakeMail is an in-memory Outpost mail service.
type fakeMail struct {
	mu       sync.Mutex
	msgs     []Message
	deleted  []int64
	status   int // when non-zero, every request answers this
	ready    bool
	addrs    []Address
	afters   []int64
	authSeen []string
	srv      *httptest.Server
}

func newFakeMail(t *testing.T, msgs ...Message) *fakeMail {
	t.Helper()
	f := &fakeMail{msgs: msgs, ready: true, addrs: []Address{
		{Route: "shelf", Address: "you+shelf@gmail.com"},
		{Route: "k7q2", Alias: true, Note: "Sentry alerts", Address: "you+k7q2@gmail.com"},
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMail) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))

	fail := func(status int, msg string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if f.status != 0 {
		fail(f.status, "configured failure")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		fail(http.StatusUnauthorized, "bad token")
		return
	}

	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/me":
		json.NewEncoder(w).Encode(Me{Ready: f.ready, Addresses: f.addrs})

	case r.Method == "GET" && r.URL.Path == "/v1/messages":
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		f.afters = append(f.afters, after)
		var page []Message
		for _, m := range f.msgs {
			if m.ID > after && len(page) < limit {
				page = append(page, m)
			}
		}
		next := after
		if len(page) > 0 {
			next = page[len(page)-1].ID
		}
		json.NewEncoder(w).Encode(map[string]any{"messages": page, "next": next})

	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/messages/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/messages/"), 10, 64)
		kept := f.msgs[:0]
		found := false
		for _, m := range f.msgs {
			if m.ID == id {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		f.msgs = kept
		if !found {
			fail(http.StatusNotFound, "no such message")
			return
		}
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)

	default:
		fail(http.StatusNotFound, "not served")
	}
}

func message(id int64) Message {
	return Message{
		ID: id, Route: "k7q2", To: "you+k7q2@gmail.com",
		From:      From{Name: "Sentry", Address: "noreply@sentry.io"},
		Subject:   "New issue " + strconv.FormatInt(id, 10),
		MessageID: "<msg" + strconv.FormatInt(id, 10) + "@sentry.io>",
		Date:      time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC),
		Received:  time.Date(2026, 11, 8, 9, 0, 41, 0, time.UTC),
		Text:      "Something broke.\n\nIt broke   twice.",
	}
}

type harness struct {
	fake   *fakeMail
	poller *Poller
	db     *library.DB
	root   string
}

func newHarness(t *testing.T, msgs ...Message) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := library.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	fake := newFakeMail(t, msgs...)
	root := filepath.Join(dir, "library")
	return &harness{
		fake: fake, db: db, root: root,
		poller: &Poller{
			Client:      New(fake.srv.URL, testToken),
			DB:          db,
			LibraryRoot: root,
			StateFile:   filepath.Join(dir, "data", "mail.json"),
			StageDir:    filepath.Join(dir, "cache", "mail"),
			Interval:    10 * time.Millisecond,
		},
	}
}

func (h *harness) books(t *testing.T) []*library.Book {
	t.Helper()
	all, err := h.db.All()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvToken, "")
	if FromEnv() != nil {
		t.Error("mail should be off with the variables unset")
	}
	t.Setenv(EnvURL, "http://127.0.0.1:47902")
	if FromEnv() != nil {
		t.Error("mail should be off without a token")
	}
	t.Setenv(EnvToken, testToken)
	if FromEnv() == nil {
		t.Error("mail should be on with both set")
	}
}

func TestPollImportsAMessageAsAnIndexedBook(t *testing.T) {
	h := newHarness(t, message(42))
	n, err := h.poller.Poll(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Poll = %d, %v", n, err)
	}

	books := h.books(t)
	if len(books) != 1 {
		t.Fatalf("%d books indexed, want 1", len(books))
	}
	b := books[0]
	if b.Title != "2026-11-08 New issue 42" {
		t.Errorf("title %q", b.Title)
	}
	if len(b.Authors) != 1 || b.Authors[0] != "Sentry" {
		t.Errorf("authors %v", b.Authors)
	}
	// An alias route names its series after the operator's note for it.
	if b.Series != "Sentry alerts" {
		t.Errorf("series %q", b.Series)
	}
	if b.Format != library.FormatEPUB {
		t.Errorf("format %q", b.Format)
	}
	rel, err := filepath.Rel(h.root, b.Path)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(rel), "Mail/Sentry/") {
		t.Errorf("book at %q, want under %s/Mail/Sentry/", b.Path, h.root)
	}

	f, err := epub.Open(b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := f.Metadata().Subjects; len(got) != 2 || got[0] != "mail" || got[1] != "k7q2" {
		t.Errorf("subjects %v", got)
	}

	if len(h.fake.deleted) != 1 || h.fake.deleted[0] != 42 {
		t.Errorf("deleted %v, want [42]", h.fake.deleted)
	}
	for _, a := range h.fake.authSeen {
		if a != "Bearer "+testToken {
			t.Errorf("Authorization %q", a)
		}
	}
	if entries, _ := os.ReadDir(h.poller.StageDir); len(entries) != 0 {
		t.Errorf("staging left %d file(s) behind", len(entries))
	}
}

func TestPollPagesAndPersistsTheCursor(t *testing.T) {
	var msgs []Message
	for i := int64(1); i <= pageSize+5; i++ {
		msgs = append(msgs, message(i))
	}
	h := newHarness(t, msgs...)

	n, err := h.poller.Poll(context.Background())
	if err != nil || n != pageSize+5 {
		t.Fatalf("Poll = %d, %v", n, err)
	}
	if got := len(h.books(t)); got != pageSize+5 {
		t.Errorf("%d books, want %d", got, pageSize+5)
	}

	data, err := os.ReadFile(h.poller.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var c cursorFile
	json.Unmarshal(data, &c)
	if c.After != pageSize+5 {
		t.Errorf("cursor %d, want %d", c.After, pageSize+5)
	}

	// A second pass asks from the cursor and imports nothing.
	h.fake.afters = nil
	if n, err := h.poller.Poll(context.Background()); err != nil || n != 0 {
		t.Errorf("second Poll = %d, %v", n, err)
	}
	if len(h.fake.afters) == 0 || h.fake.afters[0] != pageSize+5 {
		t.Errorf("second poll asked after %v", h.fake.afters)
	}
}

// A crash after importing but before deleting, with the cursor lost, must not
// put the same message in the library twice.
func TestRedeliveryDoesNotDuplicate(t *testing.T) {
	h := newHarness(t, message(7))
	if _, err := h.poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	os.Remove(h.poller.StateFile)
	h.fake.mu.Lock()
	h.fake.msgs = []Message{message(7)}
	h.fake.mu.Unlock()

	calls := 0
	h.poller.Summarize = func(context.Context, string, string) (string, error) {
		calls++
		return "different every time " + strconv.Itoa(calls), nil
	}
	n, err := h.poller.Poll(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("re-delivery Poll = %d, %v", n, err)
	}
	if got := len(h.books(t)); got != 1 {
		t.Errorf("%d books after re-delivery, want 1", got)
	}
	if calls != 0 {
		t.Errorf("a duplicate was sent to the model %d time(s)", calls)
	}
	if len(h.fake.msgs) != 0 {
		t.Error("the re-delivered message should still be deleted")
	}
}

func TestHTMLOnlyMessageBecomesText(t *testing.T) {
	m := message(3)
	m.Text = ""
	m.HTML = `<html><head><title>x</title><style>p{color:red}</style></head><body>
		<script>alert("no")</script><!-- tracking -->
		<p>Hello &amp; welcome,<br>reader.</p><div>Second&nbsp;block <b>bold</b></div>
		<img src="https://tracker.example/pixel.gif"></body></html>`
	h := newHarness(t, m)
	if _, err := h.poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := paragraphs(bodyText(m))
	want := []string{"Hello & welcome,", "reader.", "Second block bold"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("paragraphs %q, want %q", got, want)
	}
	if len(h.books(t)) != 1 {
		t.Error("HTML-only message was not imported")
	}
}

func TestSummaryBecomesTheDescription(t *testing.T) {
	h := newHarness(t, message(1), message(2))
	h.poller.Summarize = func(_ context.Context, subject, text string) (string, error) {
		if subject == "New issue 2" {
			return "", errors.New("503 no model")
		}
		if !strings.Contains(text, "Something broke.") {
			t.Errorf("summarizer got %q", text)
		}
		return "A short summary.", nil
	}
	n, err := h.poller.Poll(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Poll = %d, %v (a failed summary must not stop the import)", n, err)
	}

	descs := map[string]string{}
	for _, b := range h.books(t) {
		f, err := epub.Open(b.Path)
		if err != nil {
			t.Fatal(err)
		}
		descs[b.Title] = f.Metadata().Description
		f.Close()
	}
	if d := descs["2026-11-08 New issue 1"]; d != "A short summary." {
		t.Errorf("description %q", d)
	}
	if d := descs["2026-11-08 New issue 2"]; d != "" {
		t.Errorf("failed summary left description %q", d)
	}
}

func TestPlainRouteIsSeriesMail(t *testing.T) {
	m := message(5)
	m.Route = "shelf"
	m.From = From{Address: "news@example.com"}
	m.Subject = ""
	h := newHarness(t, m)
	if _, err := h.poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := h.books(t)[0]
	if b.Series != "Mail" || b.Authors[0] != "news@example.com" || b.Title != "2026-11-08 (no subject)" {
		t.Errorf("got series %q authors %v title %q", b.Series, b.Authors, b.Title)
	}
}

// No Gmail account (503) is a normal state: Run keeps going quietly and
// imports once mail is available.
func TestRunToleratesNoAccount(t *testing.T) {
	h := newHarness(t, message(9))
	h.fake.mu.Lock()
	h.fake.status = http.StatusServiceUnavailable
	h.fake.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.poller.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	h.fake.mu.Lock()
	h.fake.status = 0
	h.fake.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if all, _ := h.db.All(); len(all) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(h.books(t)) != 1 {
		t.Error("message never imported after the account appeared")
	}
}

func TestRunStopsOnABadToken(t *testing.T) {
	h := newHarness(t, message(1))
	h.poller.Client = New(h.fake.srv.URL, "omail_wrong")

	done := make(chan struct{})
	go func() { h.poller.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept retrying a token the service refuses")
	}
	if len(h.books(t)) != 0 {
		t.Error("imported with a bad token")
	}
}

func TestAPIErrorCarriesTheMessage(t *testing.T) {
	h := newHarness(t)
	h.fake.status = http.StatusBadGateway
	_, _, err := h.poller.Client.Messages(context.Background(), 0, 10)
	var e *APIError
	if !errors.As(err, &e) || e.Status != http.StatusBadGateway || e.Message != "configured failure" {
		t.Errorf("got %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error text carries the token")
	}
}
