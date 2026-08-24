package device

import (
	"fmt"
	"strconv"
	"strings"
)

// Firmware compatibility.
//
// CrossPoint is under heavy development. shelf pins the version it has been
// tested against and gates on the reported version string, failing loudly on an
// unknown major rather than guessing at a changed protocol. When a new major
// ships, re-verify the endpoints in docs/webserver-endpoints.md and bump
// MaxMajor here rather than removing the check.
const (
	// TestedVersion is the firmware shelf has been exercised against on real
	// hardware (an X4 reporting 1.4.1).
	TestedVersion = "1.4.1"

	// MinMajor and MaxMajor bound the accepted major version.
	MinMajor = 1
	MaxMajor = 1
)

// Known firmware hazards, observed on real hardware rather than in the docs.
//
// GET /api/settings CRASHES an X4 running 1.4.1.
//
// Observed directly: a single `curl http://<device>/api/settings` returned no
// bytes, hung until timeout, and rebooted the device out of File Transfer mode.
// The on-device crash report carried an EMPTY panic reason, a stack that was
// almost entirely zeros, and last-logs containing only post-reboot hardware
// detection -- the signature of a hard fault or watchdog reset rather than a
// caught error. The device had ~87 KB free heap and a -90 dBm link at the time,
// so low memory may be a contributing factor, but the endpoint streams its JSON
// response and is the prime suspect on its own.
//
// Consequences for shelf:
//   - Nothing in shelf calls /api/settings, and `shelf doctor` does not probe it.
//   - The settings screen planned in the spec must not be built on this endpoint
//     until the crash is understood and fixed upstream.
//   - Any future caller should treat a settings fetch as able to take the device
//     down mid-sync, which would also abort an in-flight upload.
//
// Worth reporting to the firmware project with the crash dump.
const SettingsEndpointUnsafe = true

// Known device models. The status API exposes no serial number or MAC, only
// this string, which is why shelf assigns its own identity in /shelf/device.json.
const (
	ModelX3 = "X3"
	ModelX4 = "X4"
)

// Endpoints the firmware exposes that shelf does not yet use. Listed so they
// are not rediscovered as "missing" later:
//
//	GET  /api/wifi          list saved networks
//	POST /api/wifi          add or update a network
//	POST /api/wifi/delete   remove a network
//	GET|POST /api/fonts*    .cpfont family management
//	GET|POST /api/opds*     saved OPDS catalogs
//
// Font, OPDS, and settings management are planned; Wi-Fi configuration is
// deliberately out of scope, since a tool that can move a device onto a
// different network can also strand it.

// Version is a parsed semantic firmware version.
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// ParseVersion parses a firmware version string, tolerating a leading "v" and
// a missing patch component.
func ParseVersion(s string) (Version, error) {
	v := Version{Raw: s}

	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimPrefix(trimmed, "v")
	// Discard any build or pre-release suffix.
	if i := strings.IndexAny(trimmed, "-+ "); i >= 0 {
		trimmed = trimmed[:i]
	}
	if trimmed == "" {
		return v, fmt.Errorf("empty firmware version")
	}

	parts := strings.Split(trimmed, ".")
	nums := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return v, fmt.Errorf("invalid firmware version %q", s)
		}
		nums[i] = n
	}

	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// String renders the version.
func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// CheckCompat validates a device's reported status against what shelf supports.
//
// An unknown major is an error rather than a warning: the endpoints, the
// WebSocket framing, and the on-device cache layout could all have changed, and
// guessing risks writing garbage onto someone's library.
func CheckCompat(s *Status) error {
	if s == nil {
		return fmt.Errorf("%w: no status available", ErrUnsupportedFirmware)
	}

	// A filesystem-backed transport has no firmware to be compatible with.
	// This gate exists to catch drift in the HTTP and WebSocket contract, and
	// a mounted SD card uses neither -- rejecting it for reporting an empty
	// version would be gating on the wrong thing.
	if s.Mode == ModeLocal {
		return nil
	}

	v, err := ParseVersion(s.Version)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedFirmware, err)
	}

	if v.Major < MinMajor || v.Major > MaxMajor {
		return fmt.Errorf(
			"%w: device reports %s, but shelf has only been tested against %d.x (pinned to %s). "+
				"Re-verify the endpoint contract before raising MaxMajor in internal/device/compat.go",
			ErrUnsupportedFirmware, s.Version, MaxMajor, TestedVersion)
	}
	return nil
}

// CompatWarning returns a non-fatal note when the firmware differs from the
// pinned version within a supported major, or "" when it matches.
func CompatWarning(s *Status) string {
	if s == nil {
		return ""
	}
	// Same reasoning as CheckCompat: a mounted card runs no firmware, so
	// there is nothing for its version to differ from.
	if s.Mode == ModeLocal {
		return ""
	}
	if s.Version == TestedVersion {
		return ""
	}
	return fmt.Sprintf("device firmware %s differs from the tested %s; endpoints may have changed",
		s.Version, TestedVersion)
}

// KnownModel reports whether the model string is one shelf has a profile for.
func KnownModel(model string) bool {
	return model == ModelX3 || model == ModelX4
}
