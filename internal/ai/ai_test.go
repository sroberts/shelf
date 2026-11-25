package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/library"
)

const testToken = "oai_secret-token-value"

// fakeOutpost stands in for Outpost's AI endpoint.
type fakeOutpost struct {
	t        *testing.T
	srv      *httptest.Server
	requests atomic.Int32

	mu      sync.Mutex
	handler func(w http.ResponseWriter, path string, body map[string]any)
	paths   []string
	models  []string
}

func newFake(t *testing.T) *fakeOutpost {
	f := &fakeOutpost{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"bad token"}}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.models = append(f.models, fmt.Sprint(body["model"]))
		h := f.handler
		f.mu.Unlock()
		h(w, r.URL.Path, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOutpost) set(h func(w http.ResponseWriter, path string, body map[string]any)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func (f *fakeOutpost) client() *Client { return New(f.srv.URL, testToken) }

func errorReply(status int, msg string) func(http.ResponseWriter, string, map[string]any) {
	return func(w http.ResponseWriter, _ string, _ map[string]any) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"api_error","message":%q}}`, msg)
	}
}

func genreReply(genre string, confidence float64) func(http.ResponseWriter, string, map[string]any) {
	return func(w http.ResponseWriter, _ string, _ map[string]any) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "laya:latest",
			"answers": map[string]any{
				"genre":   map[string]any{"type": "choice", "choice": genre, "confidence": confidence},
				"fiction": map[string]any{"type": "noul", "noul": 0.9},
			},
		})
	}
}

func TestFromEnvIsNilWhenUnset(t *testing.T) {
	for _, env := range [][2]string{{"", ""}, {"http://127.0.0.1:1", ""}, {"", "tok"}} {
		t.Setenv(EnvURL, env[0])
		t.Setenv(EnvToken, env[1])
		if c := FromEnv(); c != nil {
			t.Errorf("URL=%q token=%q: got a client, want AI off", env[0], env[1])
		}
	}
	t.Setenv(EnvURL, "http://127.0.0.1:1/")
	t.Setenv(EnvToken, "tok")
	if c := FromEnv(); c == nil || c.baseURL != "http://127.0.0.1:1" {
		t.Errorf("got %+v", c)
	}
}

func TestChatSendsLLMOnTheOpenAIPath(t *testing.T) {
	f := newFake(t)
	f.set(func(w http.ResponseWriter, _ string, body map[string]any) {
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "gemma4:latest",
			"choices": []any{map[string]any{"message": map[string]any{"content": " hi there \n"}}},
		})
	})
	text, model, err := f.client().Chat(context.Background(), "be brief", "say hi", 64)
	if err != nil {
		t.Fatal(err)
	}
	if text != "hi there" || model != "gemma4:latest" {
		t.Errorf("got %q from %q", text, model)
	}
	if f.paths[0] != "/openai/v1/chat/completions" || f.models[0] != "llm" {
		t.Errorf("sent model %q to %q", f.models[0], f.paths[0])
	}
}

func TestSystem1SendsSystem1(t *testing.T) {
	f := newFake(t)
	f.set(genreReply("fantasy", 0.8))
	answers, model, err := f.client().System1(context.Background(), "a wizard school",
		map[string]Question{"genre": {Type: "choice", Instructions: "?", Criteria: Genres}})
	if err != nil {
		t.Fatal(err)
	}
	if answers["genre"].Choice != "fantasy" || model != "laya:latest" {
		t.Errorf("got %+v from %q", answers, model)
	}
	if f.paths[0] != "/systemone/v1/systemone" || f.models[0] != "system1" {
		t.Errorf("sent model %q to %q", f.models[0], f.paths[0])
	}
}

func TestErrorsCarryStatusAndMessageButNotTheToken(t *testing.T) {
	f := newFake(t)

	cases := []struct {
		status int
		check  func(error) bool
	}{
		{403, Forbidden}, {503, Unavailable}, {502, Unavailable}, {429, RateLimited},
	}
	for _, tc := range cases {
		f.set(errorReply(tc.status, "no LLM; token was "+testToken))
		_, _, err := f.client().Chat(context.Background(), "", "x", 0)
		if !tc.check(err) {
			t.Errorf("%d: predicate false for %v", tc.status, err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("%d: error leaks the token: %v", tc.status, err)
		}
		if !strings.Contains(err.Error(), "no LLM") {
			t.Errorf("%d: message lost: %v", tc.status, err)
		}
	}

	bad := New(f.srv.URL, "wrong")
	if _, _, err := bad.Chat(context.Background(), "", "x", 0); !Unauthorized(err) {
		t.Errorf("wrong token: got %v", err)
	}
}

func TestRetryAfterIsParsed(t *testing.T) {
	f := newFake(t)
	f.set(func(w http.ResponseWriter, _ string, _ map[string]any) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	})
	_, _, err := f.client().Chat(context.Background(), "", "x", 0)
	e, ok := err.(*APIError)
	if !ok || e.RetryAfter != 7*time.Second || e.Message != "slow down" {
		t.Errorf("got %#v", err)
	}
}

func TestErrorMessageShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"type":"error","error":{"type":"api_error","message":"outpost says"}}`: "outpost says",
		`{"error":"ollama says"}`: "ollama says",
		`{"message":"flat"}`:      "flat",
		`plain text`:              "plain text",
	} {
		if got := errorMessage([]byte(body)); got != want {
			t.Errorf("%s: got %q, want %q", body, got, want)
		}
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Genre("abc"); ok {
		t.Error("unclassified book reported as classified")
	}
	if err := s.SetGenre("abc", "fantasy", nil, "laya"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGenre("def", "", nil, "laya"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if g, ok := s.Genre("abc"); !ok || g != "fantasy" {
		t.Errorf("got %q %v", g, ok)
	}
	if g, ok := s.Genre("def"); !ok || g != "" {
		t.Errorf("unsure answer: got %q %v, want classified and blank", g, ok)
	}
	all, err := s.Genres()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["abc"] != "fantasy" {
		t.Errorf("Genres() = %v", all)
	}
}

// --- classifier ---

func newClassifier(t *testing.T, f *fakeOutpost, n int) *Classifier {
	t.Helper()
	db, err := library.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i := range n {
		if err := db.Upsert(&library.Book{
			SHA256: fmt.Sprintf("%064x", i+1), Path: fmt.Sprintf("/lib/book-%d.txt", i),
			Format: library.FormatTXT, Title: fmt.Sprintf("Book %d", i), Size: 1, AddedUnix: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Classifier{
		Client: f.client(), Store: store, DB: db,
		sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func classified(t *testing.T, c *Classifier) map[string]string {
	t.Helper()
	out := map[string]string{}
	books, _ := c.DB.All()
	for _, b := range books {
		if g, ok := c.Store.Genre(b.SHA256); ok {
			out[b.SHA256] = g
		}
	}
	return out
}

func TestClassifierAsksOnce(t *testing.T) {
	f := newFake(t)
	f.set(genreReply("fantasy", 0.9))
	c := newClassifier(t, f, 3)

	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 3 {
		t.Fatalf("%d requests for 3 books", n)
	}
	for sum, g := range classified(t, c) {
		if g != "fantasy" {
			t.Errorf("%s: %q", sum, g)
		}
	}

	// Answers are paid for: a second pass asks nothing.
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 3 {
		t.Errorf("second pass made %d more requests", n-3)
	}
}

func TestClassifierLeavesUnsureAnswersBlank(t *testing.T) {
	f := newFake(t)
	f.set(genreReply("horror", 0.3))
	c := newClassifier(t, f, 1)
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := classified(t, c)
	if len(got) != 1 {
		t.Fatalf("got %v, want one classified book", got)
	}
	for _, g := range got {
		if g != "" {
			t.Errorf("low-confidence answer recorded as %q", g)
		}
	}
}

func TestClassifierIgnoresLabelsItDidNotOffer(t *testing.T) {
	f := newFake(t)
	f.set(genreReply("cookbook", 0.99))
	c := newClassifier(t, f, 1)
	c.Run(context.Background())
	for _, g := range classified(t, c) {
		if g != "" {
			t.Errorf("unknown label recorded as %q", g)
		}
	}
}

// 403, 503 and 502 end the pass after the first request and mark nothing, so
// the books are asked about once the model is available.
func TestClassifierStopsWithoutMarkingWhenUnavailable(t *testing.T) {
	for _, status := range []int{401, 403, 502, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFake(t)
			f.set(errorReply(status, "nope"))
			c := newClassifier(t, f, 3)
			if err := c.Run(context.Background()); err != nil {
				t.Fatalf("an AI outage must not fail the caller: %v", err)
			}
			if n := f.requests.Load(); n != 1 {
				t.Errorf("%d requests; the pass should stop at the first", n)
			}
			if got := classified(t, c); len(got) != 0 {
				t.Errorf("marked %v during an outage", got)
			}
		})
	}
}

func TestClassifierRetriesAfterRateLimit(t *testing.T) {
	f := newFake(t)
	var calls atomic.Int32
	ok := genreReply("mystery", 0.8)
	f.set(func(w http.ResponseWriter, p string, b map[string]any) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ok(w, p, b)
	})
	c := newClassifier(t, f, 1)
	var waited time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { waited = d; return nil }

	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited != 3*time.Second {
		t.Errorf("waited %v, want the Retry-After of 3s", waited)
	}
	for _, g := range classified(t, c) {
		if g != "mystery" {
			t.Errorf("got %q after retry", g)
		}
	}
}

// A request the provider refuses as malformed is this book's problem, not an
// outage: it is marked blank so it cannot be retried forever, and the pass
// carries on.
func TestClassifierSkipsABookTheProviderRefuses(t *testing.T) {
	f := newFake(t)
	f.set(errorReply(http.StatusBadRequest, "cannot take that question"))
	c := newClassifier(t, f, 2)
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("%d requests, want one per book", n)
	}
	if got := classified(t, c); len(got) != 2 {
		t.Errorf("got %v, want both marked", got)
	}
}

func TestClassifierStopsOnCancel(t *testing.T) {
	f := newFake(t)
	f.set(genreReply("fantasy", 0.9))
	c := newClassifier(t, f, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d requests after cancel", n)
	}
}
