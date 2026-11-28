// Package ui serves the web UI, built from web/ into dist/ and embedded.
//
// The UI is a client of /v1 like any other: it has no endpoints of its own,
// so the web UI, the CLI, the TUI and MCP all go through the same Service and
// cannot drift apart. This package only serves files.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// Embedding * rather than dist keeps `go build` working before the UI has
// been built (dist is not committed); only the dist subtree is served.
//
//go:embed *
var files embed.FS

const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Shelf</title></head>
<body style="font-family:system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem;line-height:1.5">
<h1>The web UI isn't built</h1>
<p>Build it, then rebuild the binary:</p>
<pre>cd web &amp;&amp; npm ci &amp;&amp; npm run build
go build -o shelf ./cmd/shelf</pre>
<p>Everything else works without it: the catalog at <code>/opds</code>, the REST API under
<code>/v1</code> (described at <a href="/v1/openapi.json">/v1/openapi.json</a>), MCP at
<code>/mcp</code>, and the <code>shelf</code> command and its terminal interface.</p>
</body></html>
`

// Handler serves dist:
//
//   - /assets/* with an immutable cache header, since Vite puts a content hash
//     in every name there;
//   - sw.js and manifest.webmanifest with no-cache, because a stale worker
//     would pin an old build, and with the manifest's own content type, which
//     Go's MIME table lacks;
//   - a missing path that has a file extension as 404, so a stale asset
//     reference fails loudly instead of being answered with HTML;
//   - anything else as index.html with no-cache, for client-side routing.
//
// Without dist it serves a page saying how to build it.
func Handler(log *slog.Logger) http.Handler {
	dist, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // fs.Sub fails only on an invalid path
	}
	return newHandler(dist, log)
}

// rootFiles are the files at the root of dist that need headers of their own.
var rootFiles = map[string]map[string]string{
	"sw.js":                {"Cache-Control": "no-cache"},
	"manifest.webmanifest": {"Cache-Control": "no-cache", "Content-Type": "application/manifest+json"},
}

func newHandler(dist fs.FS, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Warn("web ui not built; serving a placeholder", "hint", "cd web && npm ci && npm run build")
		index = []byte(notBuiltPage)
	}
	fileServer := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		// index.html goes through the SPA branch: the file server would
		// redirect it to /.
		if name != "" && name != "index.html" {
			if fi, err := fs.Stat(dist, name); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				for k, v := range rootFiles[name] {
					w.Header().Set(k, v)
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}
