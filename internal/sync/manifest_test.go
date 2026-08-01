package sync

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/device"
)

func TestManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices", "device.json")

	m := NewManifest("x4", "/Books")
	m.Model, m.Firmware = "X4", "1.0.0"
	m.Put(Entry{
		SHA256: "abc", DevicePath: "/Books/a.epub", DeviceSize: 100,
		LocalPath: "/home/scott/Books/a.epub", UploadedUnix: 1753800000,
	})
	m.Touch(time.Unix(1753900000, 0))

	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := LoadManifest(path, "x4", "/Books")
	if err != nil {
		t.Fatal(err)
	}

	if got.DeviceUUID != m.DeviceUUID || got.Nickname != "x4" || got.Model != "X4" {
		t.Errorf("identity not preserved: %+v", got)
	}
	if got.LastSyncUnix != 1753900000 {
		t.Errorf("LastSyncUnix = %d", got.LastSyncUnix)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d", len(got.Entries))
	}
	e := got.Entries[0]
	if e.SHA256 != "abc" || e.DevicePath != "/Books/a.epub" || e.DeviceSize != 100 {
		t.Errorf("entry = %+v", e)
	}
}

// The first sync has no manifest; that is a normal state, not an error.
func TestLoadMissingManifest(t *testing.T) {
	m, err := LoadManifest(filepath.Join(t.TempDir(), "nope.json"), "x4", "/Books")
	if err != nil {
		t.Fatalf("a missing manifest should not be an error: %v", err)
	}
	if m.DeviceUUID == "" {
		t.Error("a fresh manifest should have a device UUID")
	}
	if m.Nickname != "x4" || m.Root != "/Books" {
		t.Errorf("manifest = %+v", m)
	}
}

// A manifest from a newer shelf must be refused rather than misinterpreted:
// its path pinning could mean something different, and guessing risks moving
// books and destroying reading positions.
func TestLoadRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	os.WriteFile(path, []byte(`{"version": 999, "device_uuid": "x"}`), 0o644)

	_, err := LoadManifest(path, "x4", "/Books")
	if err == nil {
		t.Fatal("expected an error for a future schema version")
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadRejectsCorruptManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{not json`), 0o644)

	if _, err := LoadManifest(path, "x4", "/Books"); err == nil {
		t.Error("expected an error for a corrupt manifest")
	}
}

func TestManifestPutReplacesByLocalPath(t *testing.T) {
	m := NewManifest("x4", "/Books")

	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub", SHA256: "v1", DeviceSize: 10})
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub", SHA256: "v2", DeviceSize: 20})

	if len(m.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(m.Entries))
	}
	if m.Entries[0].SHA256 != "v2" || m.Entries[0].DeviceSize != 20 {
		t.Errorf("entry = %+v, want the replacement", m.Entries[0])
	}
}

func TestManifestRemove(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub"})
	m.Put(Entry{LocalPath: "/l/b.epub", DevicePath: "/Books/b.epub"})

	if !m.RemoveLocal("/l/a.epub") {
		t.Error("RemoveLocal reported no entry")
	}
	if m.RemoveLocal("/l/a.epub") {
		t.Error("RemoveLocal removed the same entry twice")
	}

	if !m.RemoveDevice(device.NewPath("/Books/b.epub")) {
		t.Error("RemoveDevice reported no entry")
	}
	if len(m.Entries) != 0 {
		t.Errorf("entries = %+v", m.Entries)
	}
}

func TestManifestIndexes(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/Sub/a.epub"})

	if e := m.ByLocalPath()["/l/a.epub"]; e == nil {
		t.Error("ByLocalPath missed the entry")
	}
	// Device paths are normalized, so lookups are not sensitive to formatting.
	if e := m.ByDevicePath()[device.NewPath("/Books/Sub/a.epub")]; e == nil {
		t.Error("ByDevicePath missed the entry")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.json")

	m := NewManifest("x4", "/Books")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	// Overwriting must also be clean.
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".manifest-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// UUIDv7 layout: 48-bit big-endian millisecond timestamp, version 7, RFC 4122
// variant. Hand-rolled to avoid a dependency, so it needs checking.
func TestUUIDv7(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	seen := map[string]bool{}
	var previous string

	for i := 0; i < 200; i++ {
		u := NewUUIDv7()

		if !pattern.MatchString(u) {
			t.Fatalf("UUID %q does not match the v7 layout", u)
		}
		if seen[u] {
			t.Fatalf("duplicate UUID %q", u)
		}
		seen[u] = true

		// The timestamp prefix must be non-decreasing, which is the point of
		// v7 over v4: manifests sort by creation order.
		prefix := strings.ReplaceAll(u[:13], "-", "")
		if previous != "" && prefix < previous {
			t.Errorf("timestamp went backwards: %s then %s", previous, prefix)
		}
		previous = prefix
	}
}

func TestUUIDv7EncodesCurrentTime(t *testing.T) {
	before := time.Now().UnixMilli()
	u := NewUUIDv7()
	after := time.Now().UnixMilli()

	raw, err := hex.DecodeString(strings.ReplaceAll(u, "-", ""))
	if err != nil {
		t.Fatal(err)
	}

	var ms int64
	for i := 0; i < 6; i++ {
		ms = ms<<8 | int64(raw[i])
	}
	if ms < before-1000 || ms > after+1000 {
		t.Errorf("embedded timestamp %d outside [%d, %d]", ms, before, after)
	}
}

func TestManifestBytes(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub"})

	data, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("mirrored manifest should end with a newline")
	}
	if !strings.Contains(string(data), "/Books/a.epub") {
		t.Errorf("manifest bytes = %s", data)
	}
}

// shelf's device state lives in a visible directory, not a dotfile, because the
// firmware hides dotfiles from listings unless showHiddenFiles is enabled and
// refuses to serve some of them.
func TestDeviceStatePathsAreVisible(t *testing.T) {
	for _, p := range []string{ShelfDir, MarkerPath, ManifestPath} {
		if strings.Contains(p, "/.") {
			t.Errorf("%s is a dotfile path; shelf state must stay visible", p)
		}
		if device.IsProtected(device.NewPath(p)) {
			t.Errorf("%s collides with a firmware-protected path", p)
		}
	}
}
