package ui

import (
	"bytes"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Shelf</title>")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		"icons/icon.svg":       {Data: []byte("<svg/>")},
		"sw.js":                {Data: []byte("self.x=1")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Shelf"}`)},
	}
	h := newHandler(dist, slog.New(slog.DiscardHandler))

	const immutable = "public, max-age=31536000, immutable"
	const html = "text/html; charset=utf-8"
	tests := []struct {
		name, method, target string
		status               int
		body, cache, ctype   string
	}{
		{"root", "GET", "/", 200, "<title>Shelf</title>", "no-cache", html},
		{"index by name", "GET", "/index.html", 200, "<title>Shelf</title>", "no-cache", html},
		{"book route", "GET", "/books/12", 200, "<title>Shelf</title>", "no-cache", html},
		{"shelf route with escapes", "GET", "/shelves/To%20read", 200, "<title>Shelf</title>", "no-cache", html},
		{"route head", "HEAD", "/reading", 200, "", "no-cache", html},
		{"asset", "GET", "/assets/index-abc.js", 200, "console.log(1)", immutable, "text/javascript; charset=utf-8"},
		{"asset css", "GET", "/assets/index-abc.css", 200, "body{}", immutable, "text/css; charset=utf-8"},
		{"icon", "GET", "/icons/icon.svg", 200, "<svg/>", "", "image/svg+xml"},
		{"worker", "GET", "/sw.js", 200, "self.x=1", "no-cache", "text/javascript; charset=utf-8"},
		{"manifest", "GET", "/manifest.webmanifest", 200, "Shelf", "no-cache", "application/manifest+json"},
		{"missing asset", "GET", "/assets/index-old.js", 404, "", "", ""},
		{"missing file", "GET", "/robots.txt", 404, "", "", ""},
		{"directory", "GET", "/assets", 200, "<title>Shelf</title>", "no-cache", html},
		{"dot dot", "GET", "/../../etc/passwd", 200, "<title>Shelf</title>", "no-cache", html},
		{"post", "POST", "/", 405, "", "", ""},
		{"delete", "DELETE", "/books/1", 405, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, h, tt.method, tt.target)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if tt.body != "" && !strings.Contains(rec.Body.String(), tt.body) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.body)
			}
			if got := rec.Header().Get("Cache-Control"); tt.status == 200 && got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := rec.Header().Get("Content-Type"); tt.ctype != "" && got != tt.ctype {
				t.Errorf("Content-Type = %q, want %q", got, tt.ctype)
			}
		})
	}
}

func TestHandlerNotBuilt(t *testing.T) {
	var logs bytes.Buffer
	h := newHandler(fstest.MapFS{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	for _, target := range []string{"/", "/books/1"} {
		rec := serve(t, h, "GET", target)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "npm run build") {
			t.Errorf("GET %s = %d %q, want the not-built page", target, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, h, "GET", "/assets/index.js"); rec.Code != 404 {
		t.Errorf("missing asset = %d, want 404", rec.Code)
	}
	// Once at startup, not once per request.
	if n := strings.Count(logs.String(), "web ui not built"); n != 1 {
		t.Errorf("logged %d warnings, want 1: %s", n, logs.String())
	}
}

// TestDistEmbedded is for release builds, where a binary without the UI is a
// packaging mistake rather than a developer who has not run npm yet.
func TestDistEmbedded(t *testing.T) {
	if os.Getenv("SHELF_REQUIRE_UI") != "1" {
		t.Skip("set SHELF_REQUIRE_UI=1 to require the built UI")
	}
	if _, err := fs.Stat(files, "dist/index.html"); err != nil {
		t.Fatalf("dist/index.html is not embedded: %v", err)
	}
	rec := serve(t, Handler(slog.New(slog.DiscardHandler)), "GET", "/")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || bytes.Contains(body, []byte("isn't built")) {
		t.Fatalf("GET / = %d, want the built index.html", rec.Code)
	}
}
