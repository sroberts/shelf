// Package config loads shelf's TOML configuration and resolves the XDG
// directories shelf reads and writes.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Transport selects how shelf moves bytes to a device.
type Transport string

const (
	TransportWS     Transport = "ws"     // WebSocket on :81, the primary path
	TransportHTTP   Transport = "http"   // POST /upload multipart
	TransportWebDAV Transport = "webdav" // PUT; not implemented until after M2
	TransportSD     Transport = "sd"     // direct SD-card mount
)

// Chunk size bounds. The device runs on an ESP32-C3 with roughly 380 KB of
// usable RAM and a 4 KB write buffer, so oversized chunks are a real failure
// mode rather than a tuning inefficiency.
const (
	MinChunkSize     = 4 * 1024
	MaxChunkSize     = 64 * 1024
	DefaultChunkSize = 16 * 1024
)

// Config is the parsed contents of config.toml.
type Config struct {
	LibraryRoot    string `toml:"library_root"`
	Inbox          string `toml:"inbox"`
	NamingTemplate string `toml:"naming_template"`

	UI      UI       `toml:"ui"`
	Devices []Device `toml:"device"`
	Convert Convert  `toml:"convert"`
	Sync    Sync     `toml:"sync"`

	// Paths is resolved at load time, not read from the file.
	Paths Paths `toml:"-"`
}

// UI holds presentation preferences. Unused until the TUI lands in M3, but
// parsed now so an existing config file does not start erroring later.
type UI struct {
	Theme    string `toml:"theme"`    // auto | light | dark
	Graphics string `toml:"graphics"` // auto | kitty | sixel | ascii | none
}

// Device describes one configured e-reader.
type Device struct {
	Nickname  string    `toml:"nickname"`
	Host      string    `toml:"host"` // empty triggers discovery
	Root      string    `toml:"root"` // device-side path, always forward-slash
	Transport Transport `toml:"transport"`
	ChunkSize int       `toml:"chunk_size"`
	Optimize  bool      `toml:"optimize"`
	Profile   string    `toml:"profile"`
	Mount     string    `toml:"mount"` // SD transport only
}

// Convert configures the external conversion subprocesses. Unused until M4.
type Convert struct {
	PDF     string   `toml:"pdf"`
	Timeout Duration `toml:"timeout"`
}

// Sync tunes the transfer executor.
type Sync struct {
	InterOpDelay Duration `toml:"inter_op_delay"`
	MaxRetries   int      `toml:"max_retries"`
	Prune        bool     `toml:"prune"`
}

// Duration wraps time.Duration so TOML can carry human strings like "150ms".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler for TOML decoding.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default returns the configuration shelf uses when no file exists.
func Default() Config {
	return Config{
		LibraryRoot: "~/Books",
		// Inbox is deliberately left empty so it is derived from library_root.
		// Defaulting it to a literal ~/Books/_inbox would leave the inbox
		// pointing at the wrong place for anyone who sets library_root and
		// nothing else, which is the common case.
		Inbox:          "",
		NamingTemplate: "{author}/{series} {series_index:02d} - {title}",
		UI:             UI{Theme: "auto", Graphics: "auto"},
		Convert:        Convert{PDF: "ebook-convert", Timeout: Duration{10 * time.Minute}},
		Sync: Sync{
			InterOpDelay: Duration{150 * time.Millisecond},
			MaxRetries:   3,
			Prune:        false,
		},
	}
}

// Load reads config.toml from the standard location. A missing file is not an
// error: shelf works out of the box against ~/Books.
func Load() (Config, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return Config{}, err
	}
	return LoadFile(paths.ConfigFile(), paths)
}

// LoadFile reads a specific config file. Exported so tests and --config can
// point at an arbitrary path.
func LoadFile(file string, paths Paths) (Config, error) {
	cfg := Default()
	cfg.Paths = paths

	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No file is a supported state; fall through with defaults.
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", file, err)
	default:
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", file, err)
		}
	}

	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// normalize expands ~ and fills in per-device defaults.
func (c *Config) normalize() error {
	var err error
	if c.LibraryRoot, err = expandHome(c.LibraryRoot); err != nil {
		return err
	}
	if c.LibraryRoot, err = filepath.Abs(c.LibraryRoot); err != nil {
		return fmt.Errorf("resolve library_root: %w", err)
	}

	if c.Inbox == "" {
		c.Inbox = filepath.Join(c.LibraryRoot, "_inbox")
	} else {
		if c.Inbox, err = expandHome(c.Inbox); err != nil {
			return err
		}
		if c.Inbox, err = filepath.Abs(c.Inbox); err != nil {
			return fmt.Errorf("resolve inbox: %w", err)
		}
	}

	for i := range c.Devices {
		d := &c.Devices[i]
		if d.Transport == "" {
			d.Transport = TransportWS
		}
		if d.Root == "" {
			d.Root = "/Books"
		}
		// Device paths are always forward-slash, never filepath-joined.
		d.Root = "/" + strings.Trim(strings.ReplaceAll(d.Root, "\\", "/"), "/")
		if d.ChunkSize == 0 {
			d.ChunkSize = DefaultChunkSize
		}
		if d.Mount != "" {
			if d.Mount, err = expandHome(d.Mount); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate rejects configurations that would fail confusingly later.
func (c *Config) Validate() error {
	if c.NamingTemplate == "" {
		return errors.New("naming_template must not be empty")
	}

	seen := make(map[string]bool, len(c.Devices))
	for i, d := range c.Devices {
		where := fmt.Sprintf("device %d", i)
		if d.Nickname != "" {
			where = fmt.Sprintf("device %q", d.Nickname)
		}

		if d.Nickname == "" {
			return fmt.Errorf("%s: nickname is required", where)
		}
		if seen[d.Nickname] {
			return fmt.Errorf("duplicate device nickname %q", d.Nickname)
		}
		seen[d.Nickname] = true

		switch d.Transport {
		case TransportWS, TransportHTTP, TransportWebDAV:
			// Network transports discover the host when it is not set.
		case TransportSD:
			if d.Mount == "" {
				return fmt.Errorf("%s: transport \"sd\" requires mount", where)
			}
		default:
			return fmt.Errorf("%s: unknown transport %q", where, d.Transport)
		}

		if d.ChunkSize < MinChunkSize || d.ChunkSize > MaxChunkSize {
			return fmt.Errorf("%s: chunk_size %d out of range [%d, %d]",
				where, d.ChunkSize, MinChunkSize, MaxChunkSize)
		}
	}

	if c.Sync.MaxRetries < 1 {
		return fmt.Errorf("sync.max_retries must be at least 1, got %d", c.Sync.MaxRetries)
	}
	if c.Sync.InterOpDelay.Duration < 0 {
		return errors.New("sync.inter_op_delay must not be negative")
	}
	return nil
}

// DeviceByName returns the named device, or the only configured device when
// name is empty. Ambiguity is an error rather than a silent guess: picking the
// wrong device means writing books to hardware the user did not mean.
func (c *Config) DeviceByName(name string) (Device, error) {
	if len(c.Devices) == 0 {
		return Device{}, errors.New("no devices configured; add a [[device]] block to config.toml")
	}
	if name == "" {
		if len(c.Devices) == 1 {
			return c.Devices[0], nil
		}
		return Device{}, fmt.Errorf("multiple devices configured; pass --device with one of: %s",
			strings.Join(c.DeviceNames(), ", "))
	}
	for _, d := range c.Devices {
		if d.Nickname == name {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("no device named %q; configured: %s",
		name, strings.Join(c.DeviceNames(), ", "))
}

// DeviceNames lists configured device nicknames in file order.
func (c *Config) DeviceNames() []string {
	names := make([]string, len(c.Devices))
	for i, d := range c.Devices {
		names[i] = d.Nickname
	}
	return names
}
