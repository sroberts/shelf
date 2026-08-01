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
)

// Paths resolves every directory shelf writes to.
type Paths struct {
	Config string // config.toml, shelves.toml
	Data   string // index.db
	State  string // devices/<uuid>.json, shelf.log
	Cache  string // optimized/, cover thumbnails
}

// DefaultPaths resolves the standard locations for the current user.
func DefaultPaths() (Paths, error) {
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
