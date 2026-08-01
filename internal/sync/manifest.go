// Package sync plans and executes transfers between a local library and a
// device.
//
// The planner in plan.go is a pure function over (local set, device listing,
// manifest). It performs no I/O and reads no clock, which is what makes the
// decision table in the spec directly testable.
package sync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sroberts/shelf/internal/device"
)

// ManifestVersion is the schema version of the per-device state file.
const ManifestVersion = 1

// Manifest is shelf's record of what it has placed on one device.
//
// It is stored locally and mirrored to /shelf/manifest.json on the device, so
// that a second machine can reconstruct the same state rather than treating
// every book as new.
type Manifest struct {
	Version      int     `json:"version"`
	DeviceUUID   string  `json:"device_uuid"`
	Nickname     string  `json:"nickname"`
	Model        string  `json:"model"`
	Firmware     string  `json:"firmware"`
	Root         string  `json:"root"`
	LastSyncUnix int64   `json:"last_sync_unix"`
	Entries      []Entry `json:"entries"`
}

// Entry records one book's placement on the device.
type Entry struct {
	// SHA256 is the hash of the bytes actually sent, which after optimization
	// is not the hash of the local source file. Recording what was sent means
	// an unchanged optimizer profile produces no churn.
	SHA256 string `json:"sha256"`

	// DevicePath is pinned. Once a book occupies a path it keeps that path,
	// because every rename, move, and re-upload clears the firmware's
	// .crosspoint cache for it, destroying the reading position.
	DevicePath string `json:"device_path"`

	DeviceSize      int64  `json:"device_size"`
	LocalPath       string `json:"local_path"`
	UploadedUnix    int64  `json:"uploaded_unix"`
	Optimized       bool   `json:"optimized,omitempty"`
	OptimizeProfile string `json:"optimize_profile,omitempty"`
}

// NewManifest creates an empty manifest with a fresh device identity.
func NewManifest(nickname, root string) *Manifest {
	return &Manifest{
		Version:    ManifestVersion,
		DeviceUUID: NewUUIDv7(),
		Nickname:   nickname,
		Root:       root,
	}
}

// ByLocalPath indexes entries by their local file path.
func (m *Manifest) ByLocalPath() map[string]*Entry {
	out := make(map[string]*Entry, len(m.Entries))
	for i := range m.Entries {
		out[m.Entries[i].LocalPath] = &m.Entries[i]
	}
	return out
}

// ByDevicePath indexes entries by their pinned device path.
func (m *Manifest) ByDevicePath() map[device.Path]*Entry {
	out := make(map[device.Path]*Entry, len(m.Entries))
	for i := range m.Entries {
		out[device.NewPath(m.Entries[i].DevicePath)] = &m.Entries[i]
	}
	return out
}

// Put inserts or replaces an entry, keyed by local path.
func (m *Manifest) Put(e Entry) {
	for i := range m.Entries {
		if m.Entries[i].LocalPath == e.LocalPath {
			m.Entries[i] = e
			return
		}
	}
	m.Entries = append(m.Entries, e)
}

// RemoveLocal drops the entry for a local path, reporting whether it existed.
func (m *Manifest) RemoveLocal(localPath string) bool {
	for i := range m.Entries {
		if m.Entries[i].LocalPath == localPath {
			m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// RemoveDevice drops the entry occupying a device path.
func (m *Manifest) RemoveDevice(p device.Path) bool {
	for i := range m.Entries {
		if device.NewPath(m.Entries[i].DevicePath) == p {
			m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// LoadManifest reads a manifest from disk. A missing file yields a new, empty
// manifest rather than an error, since that is the state before a first sync.
func LoadManifest(path, nickname, root string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewManifest(nickname, root), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	// A manifest from a future schema is not safely usable: its path pinning
	// may mean something different, and guessing risks moving books.
	if m.Version > ManifestVersion {
		return nil, fmt.Errorf("manifest %s is version %d, but this shelf understands %d",
			path, m.Version, ManifestVersion)
	}
	if m.Version == 0 {
		m.Version = ManifestVersion
	}
	if m.DeviceUUID == "" {
		m.DeviceUUID = NewUUIDv7()
	}
	return &m, nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save(path string) error {
	m.Version = ManifestVersion

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".manifest-*.json")
	if err != nil {
		return fmt.Errorf("create temp manifest: %w", err)
	}
	name := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// MarshalJSON is used when mirroring the manifest to the device.
func (m *Manifest) Bytes() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Touch records the time of a completed sync.
func (m *Manifest) Touch(now time.Time) { m.LastSyncUnix = now.Unix() }

// DeviceMarker is the file shelf writes to identify a device.
//
// It lives in a visible /shelf directory rather than a dotfile: the firmware
// hides dotfiles from listings unless showHiddenFiles is enabled and refuses to
// serve some of them, and one extra folder in the device's browser is a small
// price for avoiding that whole class of failure.
type DeviceMarker struct {
	Version    int    `json:"version"`
	DeviceUUID string `json:"device_uuid"`
	Nickname   string `json:"nickname"`
	Created    int64  `json:"created_unix"`
}

// Device-side paths for shelf's own state.
const (
	ShelfDir     = "/shelf"
	MarkerPath   = "/shelf/device.json"
	ManifestPath = "/shelf/manifest.json"
)

// NewUUIDv7 generates a time-ordered UUID.
//
// Hand-rolled rather than pulling in a dependency: the layout is a 48-bit
// big-endian millisecond timestamp, 4 version bits, 12 random bits, 2 variant
// bits, and 62 more random bits.
func NewUUIDv7() string {
	var b [16]byte

	ms := time.Now().UnixMilli()
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	// crypto/rand.Read never returns an error in current Go; it panics on a
	// broken entropy source, which is not a condition shelf can recover from.
	rand.Read(b[6:])

	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant

	hexed := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32])
}
