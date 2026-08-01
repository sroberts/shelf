package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/library"
)

// app holds the lazily-initialized state shared by commands. Config and the
// index are opened on first use so that commands which need neither stay fast
// and cannot fail on an unrelated misconfiguration.
type app struct {
	configPath      string
	libraryOverride string

	cfg      *config.Config
	db       *library.DB
	shelvesL bool // shelves have been loaded from TOML into the index
}

// config loads and caches the configuration.
func (a *app) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}

	file := a.configPath
	if file == "" {
		file = paths.ConfigFile()
	}

	cfg, err := config.LoadFile(file, paths)
	if err != nil {
		return nil, err
	}
	if a.libraryOverride != "" {
		cfg.LibraryRoot = a.libraryOverride
	}

	a.cfg = &cfg
	return a.cfg, nil
}

// index opens the library index, creating its directory as needed, and loads
// the shelf definitions from TOML.
//
// Shelves are loaded on every open because the TOML file is authoritative: a
// user may have edited it by hand, or the index may have been deleted and
// rebuilt, and either way the file wins.
func (a *app) index() (*library.DB, error) {
	if a.db != nil {
		return a.db, nil
	}

	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		return nil, err
	}

	db, err := library.Open(cfg.Paths.IndexFile())
	if err != nil {
		return nil, err
	}

	if err := db.LoadShelves(cfg.Paths.ShelvesFile()); err != nil {
		db.Close()
		return nil, err
	}

	a.db = db
	a.shelvesL = true
	return a.db, nil
}

// saveShelves writes shelf definitions back to their authoritative TOML file.
func (a *app) saveShelves() error {
	if a.db == nil {
		return nil
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	return a.db.SaveShelves(cfg.Paths.ShelvesFile())
}

func (a *app) close() {
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
}

// emitJSON writes one newline-delimited JSON object, the format `--json` uses
// so output pipes straight into jq.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(v)
}

// bookJSON is the stable JSON shape for a book. It is defined explicitly rather
// than marshaling library.Book so that internal field changes cannot silently
// alter the documented output of `shelf ls --json`.
type bookJSON struct {
	Path        string            `json:"path"`
	Title       string            `json:"title"`
	Authors     []string          `json:"authors,omitempty"`
	AuthorSort  string            `json:"author_sort,omitempty"`
	Series      string            `json:"series,omitempty"`
	SeriesIndex float64           `json:"series_index,omitempty"`
	Format      string            `json:"format"`
	Size        int64             `json:"size"`
	Language    string            `json:"language,omitempty"`
	Publisher   string            `json:"publisher,omitempty"`
	PubDate     string            `json:"pubdate,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Identifiers map[string]string `json:"identifiers,omitempty"`
	SHA256      string            `json:"sha256"`
	Added       int64             `json:"added_unix"`
}

func toJSON(b *library.Book) bookJSON {
	return bookJSON{
		Path:        b.Path,
		Title:       b.DisplayTitle(),
		Authors:     b.Authors,
		AuthorSort:  b.AuthorSort,
		Series:      b.Series,
		SeriesIndex: b.SeriesIndex,
		Format:      string(b.Format),
		Size:        b.Size,
		Language:    b.Language,
		Publisher:   b.Publisher,
		PubDate:     b.PubDate,
		Tags:        b.Tags,
		Identifiers: b.Identifiers,
		SHA256:      b.SHA256,
		Added:       b.AddedUnix,
	}
}

// humanSize formats a byte count for display.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
