package library

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizePath canonicalizes a filesystem path for storage in the index.
//
// This exists because of a genuine cross-platform hazard rather than tidiness.
// APFS returns filenames decomposed (NFD), while Linux filesystems generally
// hold whatever bytes were written, usually composed (NFC). "Ursula Le Guin"
// written on Linux and read on macOS produces different byte sequences for the
// same name, so a library scanned on both machines would accumulate duplicate
// rows for every book with a non-ASCII character in its path.
//
// Everything stored or compared inside shelf is NFC. The raw OS-native form is
// used only for the actual syscall, which is why callers open files with the
// path the walk gave them, not with this one.
func NormalizePath(p string) string {
	return norm.NFC.String(filepath.Clean(p))
}

// NormalizeString applies the same normalization to metadata text, so that a
// title typed one way matches a title stored the other.
func NormalizeString(s string) string {
	return norm.NFC.String(s)
}

// FoldPath returns a case-folded, normalized form for collision detection.
//
// Case matters here on every platform, not just the case-insensitive ones: the
// device's FAT32 SD card is case-insensitive regardless of the host, so two
// books that differ only in case will collide on the device even when they
// coexist happily on a Linux workstation.
func FoldPath(p string) string {
	return strings.ToLower(NormalizePath(p))
}

// SameFile reports whether two paths refer to the same entry, accounting for
// normalization differences between platforms.
func SameFile(a, b string) bool {
	return NormalizePath(a) == NormalizePath(b)
}
