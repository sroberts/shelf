package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultPathsHonorsXDG(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envConfigHome, filepath.Join(root, "cfg"))
	t.Setenv(envDataHome, filepath.Join(root, "data"))
	t.Setenv(envStateHome, filepath.Join(root, "state"))
	t.Setenv(envCacheHome, filepath.Join(root, "cache"))

	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}

	want := Paths{
		Config: filepath.Join(root, "cfg", "shelf"),
		Data:   filepath.Join(root, "data", "shelf"),
		State:  filepath.Join(root, "state", "shelf"),
		Cache:  filepath.Join(root, "cache", "shelf"),
	}
	if p != want {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

// TestDefaultPathsFallbackIsPlatformIndependent is the guard on the deliberate
// choice not to use ~/Library/Application Support on macOS. A user with a
// library on both a Mac and a Linux box should find shelf's state in the same
// relative place on each.
func TestDefaultPathsFallbackIsPlatformIndependent(t *testing.T) {
	for _, env := range []string{envConfigHome, envDataHome, envStateHome, envCacheHome} {
		t.Setenv(env, "")
	}

	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	want := Paths{
		Config: filepath.Join(home, ".config", "shelf"),
		Data:   filepath.Join(home, ".local", "share", "shelf"),
		State:  filepath.Join(home, ".local", "state", "shelf"),
		Cache:  filepath.Join(home, ".cache", "shelf"),
	}
	if p != want {
		t.Errorf("on %s got %+v, want %+v", runtime.GOOS, p, want)
	}
}

// Relative XDG values must be ignored, per the XDG base directory spec.
func TestDefaultPathsIgnoresRelativeXDG(t *testing.T) {
	t.Setenv(envConfigHome, "relative/path")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".config", "shelf"); p.Config != want {
		t.Errorf("got %q, want %q", p.Config, want)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	paths := Paths{Config: t.TempDir()}
	cfg, err := LoadFile(filepath.Join(paths.Config, "does-not-exist.toml"), paths)
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if cfg.Sync.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.Sync.MaxRetries)
	}
	if cfg.Sync.InterOpDelay.Duration != 150*time.Millisecond {
		t.Errorf("InterOpDelay = %v, want 150ms", cfg.Sync.InterOpDelay)
	}
	if !filepath.IsAbs(cfg.LibraryRoot) {
		t.Errorf("LibraryRoot %q should be absolute", cfg.LibraryRoot)
	}
	if strings.Contains(cfg.LibraryRoot, "~") {
		t.Errorf("LibraryRoot %q still contains ~", cfg.LibraryRoot)
	}
}

func TestLoadFullConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	// Mirrors the example in spec section 11.
	write(t, file, `
library_root = "~/Books"
naming_template = "{author}/{series} {series_index:02d} - {title}"

[ui]
theme = "dark"

[[device]]
nickname   = "x4"
host       = "crosspoint.local"
root       = "Books/"
transport  = "ws"
chunk_size = 16384
optimize   = true
profile    = "x4-v1"

[convert]
pdf = "ebook-convert"
timeout = "10m"

[sync]
inter_op_delay = "150ms"
max_retries = 3
prune = false
`)

	cfg, err := LoadFile(file, Paths{Config: dir})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(cfg.Devices))
	}
	d := cfg.Devices[0]
	if d.Nickname != "x4" || d.Transport != TransportWS || d.ChunkSize != 16384 {
		t.Errorf("unexpected device: %+v", d)
	}
	// Device roots are normalized to a leading slash and no trailing slash so
	// they can be joined with the "path" package without doubling separators.
	if d.Root != "/Books" {
		t.Errorf("Root = %q, want %q", d.Root, "/Books")
	}
	if cfg.Convert.Timeout.Duration != 10*time.Minute {
		t.Errorf("Timeout = %v, want 10m", cfg.Convert.Timeout)
	}
	// Inbox defaults to a subdirectory of the library root when unset.
	if want := filepath.Join(cfg.LibraryRoot, "_inbox"); cfg.Inbox != want {
		t.Errorf("Inbox = %q, want %q", cfg.Inbox, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "unknown transport",
			toml:    "[[device]]\nnickname='a'\ntransport='carrier-pigeon'",
			wantErr: "unknown transport",
		},
		{
			name:    "missing nickname",
			toml:    "[[device]]\nhost='crosspoint.local'",
			wantErr: "nickname is required",
		},
		{
			name:    "duplicate nickname",
			toml:    "[[device]]\nnickname='x4'\n[[device]]\nnickname='x4'",
			wantErr: "duplicate device nickname",
		},
		{
			name:    "sd transport without mount",
			toml:    "[[device]]\nnickname='sd'\ntransport='sd'",
			wantErr: "requires mount",
		},
		{
			name:    "chunk size over device RAM budget",
			toml:    "[[device]]\nnickname='x4'\nchunk_size=131072",
			wantErr: "chunk_size 131072 out of range",
		},
		{
			name:    "chunk size under device write buffer",
			toml:    "[[device]]\nnickname='x4'\nchunk_size=512",
			wantErr: "chunk_size 512 out of range",
		},
		{
			name:    "empty naming template",
			toml:    `naming_template = ""`,
			wantErr: "naming_template must not be empty",
		},
		{
			name:    "zero retries",
			toml:    "[sync]\nmax_retries = 0",
			wantErr: "max_retries must be at least 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "config.toml")
			write(t, file, tt.toml)

			_, err := LoadFile(file, Paths{Config: dir})
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDeviceByName(t *testing.T) {
	two := Config{Devices: []Device{{Nickname: "x3"}, {Nickname: "x4"}}}
	one := Config{Devices: []Device{{Nickname: "x4"}}}

	if _, err := (&Config{}).DeviceByName(""); err == nil {
		t.Error("expected an error when no devices are configured")
	}

	// A single configured device is unambiguous, so --device is optional.
	if d, err := one.DeviceByName(""); err != nil || d.Nickname != "x4" {
		t.Errorf("got (%v, %v), want the sole device", d.Nickname, err)
	}

	// With several devices, guessing would risk writing books to the wrong
	// hardware, so this must be an error rather than a default.
	_, err := two.DeviceByName("")
	if err == nil || !strings.Contains(err.Error(), "multiple devices") {
		t.Errorf("err = %v, want a 'multiple devices' error", err)
	}

	if _, err := two.DeviceByName("nope"); err == nil {
		t.Error("expected an error for an unknown device name")
	}
	if d, err := two.DeviceByName("x3"); err != nil || d.Nickname != "x3" {
		t.Errorf("got (%v, %v), want x3", d.Nickname, err)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct{ in, want string }{
		{"~", home},
		{"~/Books", filepath.Join(home, "Books")},
		{"/absolute/Books", "/absolute/Books"},
		{"relative/Books", "relative/Books"},
		// A leading ~ that is part of a name must not be expanded.
		{"~notauser/Books", "~notauser/Books"},
	}
	for _, tt := range tests {
		got, err := expandHome(tt.in)
		if err != nil {
			t.Errorf("expandHome(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEnsureDirs(t *testing.T) {
	root := t.TempDir()
	p := Paths{
		Config: filepath.Join(root, "cfg"),
		Data:   filepath.Join(root, "data"),
		State:  filepath.Join(root, "state"),
		Cache:  filepath.Join(root, "cache"),
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{p.Config, p.Data, p.State, p.Cache, p.DeviceStateDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("stat %s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
	// Must be safe to call repeatedly.
	if err := p.EnsureDirs(); err != nil {
		t.Errorf("second EnsureDirs: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
