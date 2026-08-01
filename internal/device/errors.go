package device

import (
	"errors"
	"fmt"
	"strings"
)

// Typed errors, mapped from the firmware's exact response strings.
//
// The strings below are copied verbatim from the CrossPoint firmware's
// docs/webserver-endpoints.md. They are matched as substrings because the
// device prefixes WebSocket failures with "ERROR:" and returns bare text over
// HTTP, and because a future firmware may add punctuation. Any change to these
// literals is a compatibility break and belongs in compat.go.
var (
	// ErrUploadInProgress means another upload is already running. The device
	// accepts exactly one at a time, globally.
	ErrUploadInProgress = errors.New("device: upload already in progress")

	// ErrDiskFull aborts an entire sync run rather than just the current file.
	ErrDiskFull = errors.New("device: SD write failed, likely full")

	// ErrInvalidStart means a malformed START frame or an invalid size.
	ErrInvalidStart = errors.New("device: invalid START frame")

	// ErrCreateFailed means the device could not open the destination file.
	ErrCreateFailed = errors.New("device: could not create file")

	// ErrNoUpload means binary data arrived without a preceding START.
	ErrNoUpload = errors.New("device: no upload in progress")

	// ErrOverflow means more bytes were sent than the START frame declared.
	ErrOverflow = errors.New("device: upload overflow")

	// ErrProtectedPath covers the paths shelf must never write to.
	ErrProtectedPath = errors.New("device: protected path")

	// ErrNotInTransfer is the single most common failure in practice: the
	// device is asleep or not in File Transfer mode. It is deliberately
	// distinct from a generic timeout so the UI can say something useful.
	ErrNotInTransfer = errors.New("device: not in file transfer mode (is it awake and in File Transfer or Calibre Wireless mode?)")

	// ErrNotFound is returned for a missing device path.
	ErrNotFound = errors.New("device: path not found")

	// ErrMkdirFailed is returned when the firmware refuses to create a folder.
	//
	// Observed on an X4 running 1.4.1: every /mkdir call answers
	// "Failed to create folder" while the SD card is unmounted, even though
	// /api/status reports the device as perfectly healthy. If this appears for
	// every path, suspect the card rather than the path.
	ErrMkdirFailed = errors.New("device: could not create folder")

	// ErrUnsupportedFirmware is returned when the firmware major version is
	// outside the range shelf has been tested against.
	ErrUnsupportedFirmware = errors.New("device: unsupported firmware version")
)

// deviceErrors maps firmware message substrings to typed errors, longest first
// so that a more specific message wins.
var deviceErrors = []struct {
	substr string
	err    error
}{
	{"Upload already in progress", ErrUploadInProgress},
	{"Write failed - disk full?", ErrDiskFull},
	{"Write failed", ErrDiskFull},
	{"Invalid START format", ErrInvalidStart},
	{"Failed to create file", ErrCreateFailed},
	{"No upload in progress", ErrNoUpload},
	{"Upload overflow", ErrOverflow},

	// Not in the firmware docs; observed on hardware running 1.4.1.
	{"Failed to create folder", ErrMkdirFailed},
	{"Missing folder name", ErrMkdirFailed},
	{"Item not found", ErrNotFound},
}

// ParseDeviceError maps a device message to a typed error.
//
// The original message is preserved in the wrapped error so that an
// unrecognized failure is still reported verbatim rather than swallowed.
func ParseDeviceError(msg string) error {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg), "ERROR:"))
	if trimmed == "" {
		return nil
	}

	for _, m := range deviceErrors {
		if strings.Contains(trimmed, m.substr) {
			return fmt.Errorf("%w (device said: %s)", m.err, trimmed)
		}
	}
	return fmt.Errorf("device error: %s", trimmed)
}

// IsFatal reports whether an error should abort an entire sync run rather than
// just the current file. A full SD card will not fix itself by moving to the
// next book, so continuing would produce a long run of identical failures.
func IsFatal(err error) bool {
	return errors.Is(err, ErrDiskFull) ||
		errors.Is(err, ErrNotInTransfer) ||
		errors.Is(err, ErrUnsupportedFirmware)
}

// IsRetryable reports whether an operation is worth attempting again.
func IsRetryable(err error) bool {
	if err == nil || IsFatal(err) {
		return false
	}
	// A concurrent upload clears on its own once the other transfer finishes.
	if errors.Is(err, ErrUploadInProgress) {
		return true
	}
	// Protocol-level mistakes will repeat identically.
	return !errors.Is(err, ErrProtectedPath) &&
		!errors.Is(err, ErrInvalidStart) &&
		!errors.Is(err, ErrOverflow) &&
		!errors.Is(err, ErrNotFound)
}
