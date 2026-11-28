package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// XDG base directories.
//
// shelf deliberately uses the same layout on macOS as on Linux rather than
// ~/Library/Application Support. A user who keeps a library on a laptop and a
// desktop should find shelf's state in the same place on both, and every path
// in the docs should be copy-pasteable regardless of platform. Honoring the
// XDG_* environment variables means anyone who has opinions about this can
// override it.
const (
	appName = "shelf"

	envConfigHome = "XDG_CONFIG_HOME"
	envDataHome   = "XDG_DATA_HOME"
	envStateHome  = "XDG_STATE_HOME"
	envCacheHome  = "XDG_CACHE_HOME"

	// envStorageDir is set by Outpost, which backs up and restores that one
	// directory and nothing else. Spelled out here rather than imported from
	// internal/outpost so config stays a leaf package.
	envStorageDir = "OUTPOST_STORAGE_DIR"
)

// Paths resolves every directory shelf writes to.
type Paths struct {
	Config string // config.toml, shelves.toml
	Data   string // index.db
	State  string // devices/<uuid>.json, shelf.log
	Cache  string // optimized/, cover thumbnails

	// Storage is the Outpost storage directory everything else sits under,
	// or empty when shelf is not running under Outpost.
	Storage string
}

// DefaultPaths resolves the standard locations for the current user.
//
// Under Outpost every directory moves inside OUTPOST_STORAGE_DIR instead. That
// directory is the unit Outpost backs up and restores, so state written under
// $HOME would survive neither — and progress.db, which cannot be rebuilt, is
// part of that state. The storage directory wins over XDG_* because Outpost
// passes its own environment through, and a stray XDG_DATA_HOME there must
// not route the progress store out of the backup.
func DefaultPaths() (Paths, error) {
	if s := os.Getenv(envStorageDir); filepath.IsAbs(s) {
		return StoragePaths(s), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return Paths{
		Config: baseDir(envConfigHome, home, ".config"),
		Data:   baseDir(envDataHome, home, filepath.Join(".local", "share")),
		State:  baseDir(envStateHome, home, filepath.Join(".local", "state")),
		Cache:  baseDir(envCacheHome, home, ".cache"),
	}, nil
}

// StoragePaths lays shelf's directories out under one storage root.
func StoragePaths(root string) Paths {
	return Paths{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		State:   filepath.Join(root, "state"),
		Cache:   filepath.Join(root, "cache"),
		Storage: root,
	}
}

// LibraryDir is the default library root under Outpost storage.
func (p Paths) LibraryDir() string {
	if p.Storage == "" {
		return ""
	}
	return filepath.Join(p.Storage, "library")
}

// baseDir returns $env/shelf when env holds an absolute path, else
// $HOME/fallback/shelf. Relative values in the environment are ignored, as the
// XDG spec requires.
func baseDir(env, home, fallback string) string {
	if v := os.Getenv(env); filepath.IsAbs(v) {
		return filepath.Join(v, appName)
	}
	return filepath.Join(home, fallback, appName)
}

// ConfigFile is the path to config.toml.
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }

// ShelvesFile is the authoritative, human-editable record of shelf definitions.
// The SQLite index is a cache and may be deleted at any time; this file is the
// one piece of state that cannot be reconstructed by rescanning the library.
func (p Paths) ShelvesFile() string { return filepath.Join(p.Config, "shelves.toml") }

// IndexFile is the derived SQLite cache.
func (p Paths) IndexFile() string { return filepath.Join(p.Data, "index.db") }

// ProgressFile is the reading-progress store.
//
// Under Data rather than Cache deliberately: unlike index.db, this cannot be
// rebuilt from anything. The device pushes reading positions here and keeps no
// synchronised copy, so deleting it loses where you are in every book.
func (p Paths) ProgressFile() string { return filepath.Join(p.Data, "progress.db") }

// AIFile holds answers from the operator's AI models, keyed by book hash.
//
// Under Data, and kept apart from index.db, because it is not disposable:
// every answer cost the operator a model call, and rebuilding the index must
// not mean paying for them all again.
func (p Paths) AIFile() string { return filepath.Join(p.Data, "ai.db") }

// MailStateFile is the cursor of the last mail message imported.
func (p Paths) MailStateFile() string { return filepath.Join(p.Data, "mail.json") }

// MailStageDir is scratch space for building a mail message's EPUB.
func (p Paths) MailStageDir() string { return filepath.Join(p.Cache, "mail") }

// LogFile is where shelf logs; never stdout, which belongs to the display.
func (p Paths) LogFile() string { return filepath.Join(p.State, "shelf.log") }

// DeviceStateDir holds per-device sync manifests, one JSON file per device UUID.
func (p Paths) DeviceStateDir() string { return filepath.Join(p.State, "devices") }

// DeviceStateFile is the local manifest for a single device.
func (p Paths) DeviceStateFile(uuid string) string {
	return filepath.Join(p.DeviceStateDir(), uuid+".json")
}

// OptimizedDir caches device-targeted EPUB builds, keyed by (sha256, profile).
func (p Paths) OptimizedDir() string { return filepath.Join(p.Cache, "optimized") }

// EnsureDirs creates every directory shelf writes to.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.Config, p.Data, p.State, p.Cache, p.DeviceStateDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// expandHome resolves a leading "~" in user-supplied config paths. Config files
// are hand-edited, and "~/Books" is what people actually type.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~"+string(filepath.Separator)) && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(p[2:])), nil
}
