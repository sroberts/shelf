package device

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run shelf's parsers over responses captured from real hardware
// (an X4 on firmware 1.4.1), rather than over JSON invented to match the
// parser. That distinction is the whole point: a hand-written fixture only
// proves the code agrees with itself.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Skipf("fixture %s not captured: %v", name, err)
	}
	return data
}

func TestParseRealStatus(t *testing.T) {
	var s Status
	if err := json.Unmarshal(readFixture(t, "status.json"), &s); err != nil {
		t.Fatalf("shelf cannot parse a real device status response: %v", err)
	}

	if s.Device != ModelX4 {
		t.Errorf("Device = %q, want X4", s.Device)
	}
	if !KnownModel(s.Device) {
		t.Errorf("%q should be a known model", s.Device)
	}
	if s.Mode != "STA" {
		t.Errorf("Mode = %q", s.Mode)
	}
	if s.Version == "" || s.FreeHeap == 0 {
		t.Errorf("status is missing fields: %+v", s)
	}

	// The captured firmware must satisfy the compatibility gate; if it does
	// not, the pin in compat.go is wrong.
	if err := CheckCompat(&s); err != nil {
		t.Errorf("the device shelf was tested against fails its own compat check: %v", err)
	}

	v, err := ParseVersion(s.Version)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s.Version, err)
	}
	if v.Major != 1 {
		t.Errorf("major = %d, want 1", v.Major)
	}
}

// The captured status must match the version pinned in compat.go, so that the
// pin cannot drift away from the hardware it claims to describe.
func TestPinnedVersionMatchesCapturedHardware(t *testing.T) {
	var s Status
	if err := json.Unmarshal(readFixture(t, "status.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.Version != TestedVersion {
		t.Errorf("compat.go pins %q but the captured device reports %q; update one of them",
			TestedVersion, s.Version)
	}
	if w := CompatWarning(&s); w != "" {
		t.Errorf("the tested device should not produce a compat warning: %s", w)
	}
}

func TestParseRealFileListing(t *testing.T) {
	var entries []FileEntry
	if err := json.Unmarshal(readFixture(t, "files_root.json"), &entries); err != nil {
		t.Fatalf("shelf cannot parse a real device listing: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the captured listing is empty")
	}

	var epubs int
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("entry with no name: %+v", e)
		}
		if !e.IsDirectory && e.Size <= 0 {
			t.Errorf("file %q has size %d", e.Name, e.Size)
		}
		if e.IsEpub {
			epubs++
			if !strings.HasSuffix(strings.ToLower(e.Name), ".epub") {
				t.Errorf("isEpub set on %q", e.Name)
			}
		}

		// The firmware hides dotfiles unless showHiddenFiles is enabled, which
		// is why shelf keeps its state in a visible /shelf directory. Confirm
		// the captured listing really does omit them.
		if strings.HasPrefix(e.Name, ".") {
			t.Errorf("the device listed a dotfile (%q); shelf's assumption that "+
				"dotfiles are hidden may no longer hold", e.Name)
		}
	}
	if epubs == 0 {
		t.Error("the captured listing contains no EPUBs, so isEpub went untested")
	}
}

// The listing carries no modification time and no checksum. Change detection
// must never come to depend on either, so assert their absence directly against
// the real response rather than trusting the struct definition.
func TestRealListingHasNoMtimeOrChecksum(t *testing.T) {
	var raw []map[string]any
	if err := json.Unmarshal(readFixture(t, "files_root.json"), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Skip("empty listing")
	}

	forbidden := []string{"mtime", "modified", "mtimeUnix", "time", "date",
		"sha256", "md5", "checksum", "hash", "crc"}

	for _, entry := range raw {
		for key := range entry {
			lower := strings.ToLower(key)
			for _, f := range forbidden {
				if lower == strings.ToLower(f) {
					t.Errorf("the firmware now returns %q; shelf's size-based change "+
						"detection could be improved to use it", key)
				}
			}
		}
	}

	// Document exactly what the device does return.
	var keys []string
	for key := range raw[0] {
		keys = append(keys, key)
	}
	t.Logf("device listing fields: %v", keys)
}

// The discovery reply parser must handle the exact bytes the hardware sends.
func TestParseRealDiscoveryReply(t *testing.T) {
	reply := string(readFixture(t, "discovery.txt"))

	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: DiscoveryPort}
	got, ok := parseReply(reply, addr)
	if !ok {
		t.Fatalf("shelf cannot parse the real discovery reply: %q", reply)
	}

	if got.Hostname == "" {
		t.Errorf("hostname not extracted from %q", reply)
	}
	if got.WSPort != WebSocketPort {
		t.Errorf("WSPort = %d, want %d", got.WSPort, WebSocketPort)
	}
	if got.Addr != "192.168.1.42" {
		t.Errorf("Addr = %q", got.Addr)
	}
	t.Logf("parsed %q -> hostname=%q ws=%d", reply, got.Hostname, got.WSPort)
}
