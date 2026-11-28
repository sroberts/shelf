package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestE2EServeOutpostExtras runs the real binary against fake Outpost AI and
// mail endpoints: a newsletter arrives, becomes a book with an AI-written
// description, is classified by the System 1 model, and is then visible
// through the REST API and the MCP endpoint.
func TestE2EServeOutpostExtras(t *testing.T) {
	var summaries, classifications atomic.Int32
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oai_test" {
			http.Error(w, `{"type":"error","error":{"message":"bad token"}}`, 401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			if body["model"] != "llm" {
				t.Errorf("chat model %v, want llm", body["model"])
			}
			summaries.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"model": "fake-llm", "choices": []any{
				map[string]any{"message": map[string]any{"role": "assistant", "content": "A digest of the week's releases."}},
			}})
		case "/systemone/v1/systemone":
			if body["model"] != "system1" {
				t.Errorf("system1 model %v, want system1", body["model"])
			}
			classifications.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"model": "fake-s1", "answers": map[string]any{
				"genre":   map[string]any{"type": "choice", "choice": "science-fiction", "confidence": 0.9},
				"fiction": map[string]any{"type": "noul", "noul": 0.2},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer aiSrv.Close()

	var deleted atomic.Bool
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer omail_test" {
			http.Error(w, `{"error":"bad token"}`, 401)
			return
		}
		switch {
		case r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]any{"ready": true, "send": false, "addresses": []any{
				map[string]any{"route": "shelf", "alias": false, "address": "you+shelf@gmail.com"}}})
		case r.URL.Path == "/v1/messages" && r.Method == "GET":
			msgs := []any{}
			// Delivered until shelf's cursor passes it, as the real service does.
			if r.URL.Query().Get("after") != "7" {
				msgs = append(msgs, map[string]any{
					"id": 7, "route": "shelf", "to": "you+shelf@gmail.com",
					"from":    map[string]any{"name": "Starlight Weekly", "address": "news@starlight.example"},
					"subject": "Issue 12", "message_id": "issue12@starlight.example",
					"date": "2026-11-20T09:00:00Z", "received": "2026-11-20T09:00:41Z",
					"text": "New releases this week.\n\nThree novels about generation ships.",
				})
			}
			json.NewEncoder(w).Encode(map[string]any{"messages": msgs, "next": 7})
		case r.URL.Path == "/v1/messages/7" && r.Method == "DELETE":
			deleted.Store(true)
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mailSrv.Close()

	storage, home, port := t.TempDir(), t.TempDir(), freePort(t)
	cmd := exec.Command(binPath, "serve", "--scan")
	cmd.Env = append(outpostEnv(t, storage, home, port),
		"OUTPOST_AI_URL="+aiSrv.URL, "OUTPOST_AI_TOKEN=oai_test",
		"OUTPOST_MAIL_URL="+mailSrv.URL, "OUTPOST_MAIL_TOKEN=omail_test")
	cmd.Dir = storage
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHealthy(t, base+"/up")

	// An account the operator made, which signs in everywhere.
	if _, stderr, code := runShelfWithEnvOnly(t, cmd.Env, "user", "add", "--password", "pw", "reader"); code != 0 {
		t.Fatalf("user add: %s", stderr)
	}

	// One the reader registered itself, through an endpoint that asks
	// nobody: progress sync and the catalog, but not the API, MCP or UI.
	resp, err := http.Post(base+"/users/create", "application/json",
		strings.NewReader(`{"username":"stranger","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, path := range []string{"/v1/books", "/v1/accounts", "/"} {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.SetBasicAuth("stranger", "pw")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("self-registered account on %s: %d, want 401", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", base+"/opds", nil)
	req.SetBasicAuth("stranger", "pw")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else if resp.Body.Close(); resp.StatusCode != http.StatusOK {
		t.Errorf("self-registered account on /opds: %d, want 200", resp.StatusCode)
	}

	// The OpenAPI document is public; the API itself is not.
	if code := get(t, base+"/v1/openapi.json", false, nil); code != 200 {
		t.Errorf("openapi.json: %d", code)
	}
	if code := get(t, base+"/v1/books", false, nil); code != 401 {
		t.Errorf("/v1/books without credentials: %d, want 401", code)
	}

	// Wait for the newsletter to arrive, be indexed and be classified.
	var books struct {
		Books []struct {
			ID      int64    `json:"id"`
			Title   string   `json:"title"`
			Authors []string `json:"authors"`
			Series  string   `json:"series"`
			Genre   string   `json:"genre"`
			Path    string   `json:"path"`
		} `json:"books"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		get(t, base+"/v1/books?q=starlight", true, &books)
		if len(books.Books) == 1 && books.Books[0].Genre != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("newsletter never became a classified book; books=%+v\nlog:\n%s", books, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	b := books.Books[0]
	if !strings.Contains(b.Title, "Issue 12") || b.Genre != "science-fiction" || !strings.HasPrefix(b.Path, "Mail/") {
		t.Errorf("book: %+v", b)
	}
	if !deleted.Load() {
		t.Error("the message was not deleted from the mail service after import")
	}
	if summaries.Load() != 1 {
		t.Errorf("%d summaries requested, want 1", summaries.Load())
	}

	// The same book through MCP.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_books","arguments":{"genre":"science-fiction"}}}`
	req, _ = http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.SetBasicAuth("reader", "pw")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "Issue 12") {
		t.Errorf("MCP search: %d %s", resp.StatusCode, raw)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit: %v\n%s", err, out.String())
	}

	// Neither token reached the log.
	if strings.Contains(out.String(), "oai_test") || strings.Contains(out.String(), "omail_test") {
		t.Errorf("a token was logged:\n%s", out.String())
	}

	// A restart asks the models nothing new: answers are kept in storage.
	before := classifications.Load()
	again := exec.Command(binPath, "serve", "--scan")
	again.Env = cmd.Env
	again.Dir = storage
	again.Stdout, again.Stderr = io.Discard, io.Discard
	if err := again.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Process.Kill() })
	waitHealthy(t, base+"/up")
	time.Sleep(time.Second)
	again.Process.Signal(syscall.SIGTERM)
	again.Wait()
	if n := classifications.Load(); n != before {
		t.Errorf("restart re-classified: %d requests, was %d", n, before)
	}
}

func get(t *testing.T, url string, auth bool, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if auth {
		req.SetBasicAuth("reader", "pw")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		json.NewDecoder(resp.Body).Decode(v)
	}
	return resp.StatusCode
}
