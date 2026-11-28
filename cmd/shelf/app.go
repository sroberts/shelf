package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sroberts/shelf/internal/ai"
	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/kosync"
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

// progress loads reading percentages keyed by KOReader document id.
//
// Returns nil when no kosync user is configured, which is the normal state
// before anyone sets up sync. A missing or unreadable store is also nil rather
// than an error: not having read anything yet should not stop you listing your
// library.
func (a *app) progress() map[string]kosync.Progress {
	cfg, err := a.config()
	if err != nil || cfg.Kosync.User == "" {
		return nil
	}

	store, err := kosync.OpenStore(cfg.Paths.ProgressFile())
	if err != nil {
		return nil
	}
	defer store.Close()

	byDoc, err := store.AllProgress(cfg.Kosync.User)
	if err != nil {
		return nil
	}
	return byDoc
}

// percentFor returns a book's read percentage, or nil when none is recorded.
func percentFor(b *library.Book, byDoc map[string]kosync.Progress) *float64 {
	if byDoc == nil || b.DocID == "" {
		return nil
	}
	p, ok := byDoc[b.DocID]
	if !ok {
		return nil
	}
	pct := p.Percentage * 100
	return &pct
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
	// ID is the index row id, accepted back as `id:N` wherever a BOOK is. It
	// is not stable: rebuilding index.db renumbers every book.
	ID          int64             `json:"id"`
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
	Percent     *float64          `json:"percent_read,omitempty"`
	Added       int64             `json:"added_unix"`
	// Genre is the AI-assigned genre, when shelf serve has classified it.
	Genre string `json:"genre,omitempty"`
}

func toJSON(b *library.Book) bookJSON { return toJSONWithProgress(b, nil) }

func toJSONWithProgress(b *library.Book, byDoc map[string]kosync.Progress) bookJSON {
	return bookJSON{
		ID:          b.ID,
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
		Percent:     percentFor(b, byDoc),
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

// genres loads AI-assigned genres keyed by book SHA-256.
//
// Nil when ai.db does not exist: classification runs only under `shelf serve`
// with Outpost's models, and a CLI listing must never create the store as a
// side effect, or `shelf doctor` would report an AI database nothing wrote.
func (a *app) genres() map[string]string {
	cfg, err := a.config()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(cfg.Paths.AIFile()); err != nil {
		return nil
	}
	store, err := ai.OpenStore(cfg.Paths.AIFile())
	if err != nil {
		return nil
	}
	defer store.Close()
	m, err := store.Genres()
	if err != nil {
		return nil
	}
	return m
}

// progressStore opens progress.db, for commands that change it.
func (a *app) progressStore() (*kosync.Store, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		return nil, err
	}
	return kosync.OpenStore(cfg.Paths.ProgressFile())
}

// progressUser is whose progress a command means: --user when given, else
// [kosync] user from the config.
func (a *app) progressUser(flagUser string) (string, error) {
	if u := kosync.NormalizeUsername(flagUser); u != "" {
		return u, nil
	}
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	if cfg.Kosync.User == "" {
		return "", fmt.Errorf("%w: no user: pass --user, or set [kosync] user in config.toml", errUsage)
	}
	return cfg.Kosync.User, nil
}
