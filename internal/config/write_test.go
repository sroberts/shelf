package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPaths(dir string) Paths {
	return Paths{Config: dir, Data: dir, State: dir, Cache: dir}
}

// config.toml is hand-written, so adding a device must leave every byte the
// user wrote exactly where it was: comments, ordering, and a last line with no
// newline after it.
func TestAppendDevicePreservesTheExistingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	original := "# my config\nnaming_template = \"{author}/{title}\"  # keep me\n\n[kosync]\nlisten = \":8080\""
	if err := os.WriteFile(file, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg, err := AppendDevice(file, testPaths(dir), Device{Nickname: "reader", Host: "192.168.1.42"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), original+"\n") {
		t.Errorf("existing content was not preserved verbatim:\n%s", got)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want the original 0640", info.Mode().Perm())
	}

	if len(cfg.Devices) != 1 {
		t.Fatalf("loaded %d devices, want 1", len(cfg.Devices))
	}
	d := cfg.Devices[0]
	// Omitted fields must come back as the loader's defaults, exactly as if
	// the block had been typed by hand without them.
	if d.Nickname != "reader" || d.Host != "192.168.1.42" || d.Root != "/Books" ||
		d.Transport != TransportWS || d.ChunkSize != DefaultChunkSize {
		t.Errorf("device = %+v, want reader at 192.168.1.42 with defaults", d)
	}
	if cfg.File != file {
		t.Errorf("cfg.File = %q, want %q", cfg.File, file)
	}
	if cfg.NamingTemplate != "{author}/{title}" {
		t.Errorf("the rest of the config did not survive: naming_template = %q", cfg.NamingTemplate)
	}
}

// A block the loader rejects must leave the file untouched and return the
// loader's own error, so the TUI shows what a hand edit would have shown.
func TestAppendDeviceRejectsADuplicateWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	original := "[[device]]\nnickname = \"reader\"\nhost = \"10.0.0.2\"\n"
	if err := os.WriteFile(file, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := AppendDevice(file, testPaths(dir), Device{Nickname: "reader", Host: "10.0.0.3"})
	if err == nil || !strings.Contains(err.Error(), "duplicate device nickname") {
		t.Fatalf("err = %v, want a duplicate nickname error", err)
	}

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a rejected device changed the file:\n%s", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("staging file left behind: %v", entries)
	}
}

// A config.toml managed by a dotfiles repo is often a symlink. Saving must
// write to the file it points at, not replace the link with a regular file.
func TestAppendDeviceWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "shelf.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("# from dotfiles\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := AppendDevice(link, testPaths(dir), Device{Nickname: "reader", Host: "reader.local"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.toml was replaced by a regular file")
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `nickname = "reader"`) {
		t.Errorf("the link target did not receive the device:\n%s", got)
	}
}

// With no config file yet, adding a device creates one. It may later hold sync
// credentials, so it starts private.
func TestAppendDeviceCreatesAMissingFilePrivately(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "shelf", "config.toml")

	cfg, err := AppendDevice(file, testPaths(dir), Device{Nickname: "reader", Host: "reader.local", Root: "/Library"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v, want 0600", info.Mode().Perm())
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].Root != "/Library" {
		t.Errorf("devices = %+v, want one rooted at /Library", cfg.Devices)
	}
}
