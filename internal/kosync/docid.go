// Package kosync speaks the KOReader progress-sync protocol, both as a client
// and as a server shelf can run itself.
//
// CrossPoint's firmware ships a KOReader sync client, so this is how shelf
// learns where you are in a book. The alternative — parsing the device's
// on-card progress.bin — is a non-starter: the format is undocumented and its
// parent section.bin is already at version 30.
package kosync

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Document identity.
//
// KOReader identifies a book by a partial MD5 of its contents: 1024 bytes read
// at a handful of exponentially spaced offsets, hashed together. Two copies of
// the same book match without reading gigabytes, and a book renamed on one
// device still matches on another.
//
// This has to be byte-exact. A hash that is merely reasonable produces no error
// anywhere — it just silently never matches, and reading positions never sync.

const (
	// chunkSize is the number of bytes sampled at each offset.
	chunkSize = 1024

	// sampleCount is the number of offsets tried: i = -1 through 10.
	sampleCount = 12
)

// offsetAt returns the byte offset for sample index i, where i runs -1..10.
//
// The first offset is 0, not 256, and the reason is worth writing down because
// every available piece of documentation says otherwise.
//
// KOReader computes the offset as lshift(1024, 2*i). For i = -1 that is
// lshift(1024, -2). Read as arithmetic, a shift of -2 means "shift right by 2"
// and gives 256 — which is what CrossPoint's own KOReaderDocumentId.h header
// comment claims the offsets are. But KOReader uses LuaJIT's bit library, where
// the shift count is masked to five bits: -2 becomes 30, and 1024 << 30
// overflows 32 bits to exactly 0.
//
// So the real first offset is 0. CrossPoint's implementation returns 0 and is
// correct; only its comment is wrong. Implementing the documented 256 would
// produce a hash that matches neither KOReader nor the device.
func offsetAt(i int) int64 {
	if i < 0 {
		return 0
	}
	return int64(chunkSize) << (2 * uint(i))
}

// Offsets returns the sample offsets in order. Exported so a test can pin them
// against the values read out of KOReader and the CrossPoint firmware.
func Offsets() []int64 {
	out := make([]int64, 0, sampleCount)
	for i := -1; i < sampleCount-1; i++ {
		out = append(out, offsetAt(i))
	}
	return out
}

// DocumentID computes the partial-MD5 identifier for a file.
//
// Sampling stops at the first offset past the end of the file, which is what
// makes a small book hash from just its opening bytes.
func DocumentID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("kosync: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("kosync: stat %s: %w", path, err)
	}
	return documentIDFrom(f, info.Size())
}

// documentIDFrom is the hash over an open file, split out so tests can drive it
// from an in-memory source.
func documentIDFrom(r io.ReaderAt, size int64) (string, error) {
	h := md5.New()
	buf := make([]byte, chunkSize)

	for i := -1; i < sampleCount-1; i++ {
		off := offsetAt(i)
		if off >= size {
			// Past the end. Offsets only grow, so nothing later can land inside
			// the file either.
			break
		}

		n := int64(chunkSize)
		if remaining := size - off; remaining < n {
			n = remaining
		}

		if _, err := r.ReadAt(buf[:n], off); err != nil && err != io.EOF {
			return "", fmt.Errorf("kosync: read at offset %d: %w", off, err)
		}
		h.Write(buf[:n])
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// FilenameID computes the alternative identifier: MD5 of the bare filename.
//
// The device offers this as a match method for libraries where the same book
// has identical names but differing bytes across devices — a re-download, or a
// copy whose metadata was edited on one side only.
func FilenameID(path string) string {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		name = path
	}
	sum := md5.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

// MatchMethod selects how a book is identified.
type MatchMethod string

const (
	// MatchBinary is the partial-MD5 of file contents. KOReader's default and
	// shelf's.
	MatchBinary MatchMethod = "binary"

	// MatchFilename is the MD5 of the filename.
	MatchFilename MatchMethod = "filename"
)

// IDFor computes the identifier for a file under the given method.
func IDFor(path string, method MatchMethod) (string, error) {
	switch method {
	case MatchFilename:
		return FilenameID(path), nil
	case MatchBinary, "":
		return DocumentID(path)
	}
	return "", fmt.Errorf("kosync: unknown match method %q", method)
}

// PasswordKey derives the auth key the protocol sends in place of a password.
//
// It is a plain MD5 of the password, which is weak by any modern standard, but
// it is what KOReader clients send and shelf does not get to redefine the
// protocol. The server stores this value rather than the password itself; see
// the note in store.go about what that does and does not protect.
func PasswordKey(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// FormatPercent renders a read percentage for display.
//
// "done" rather than "100%" is deliberate: the device reports 0.9998 for a book
// read to its last page, so rounding to 100% would make "nearly finished" and
// "finished" indistinguishable — and that is the one distinction a reader
// actually cares about in a list.
//
// Lives here rather than in each frontend so the CLI and the TUI cannot drift
// into disagreeing about what 99.6% means.
func FormatPercent(pct float64) string {
	switch {
	case pct >= 99.5:
		return "done"
	case pct > 0 && pct < 1:
		return "<1%"
	default:
		return fmt.Sprintf("%.0f%%", pct)
	}
}

// NormalizeUsername trims a username for comparison. The protocol is silent on
// case, so shelf preserves it and only strips surrounding space, which is
// almost always a paste artifact rather than intent.
func NormalizeUsername(u string) string { return strings.TrimSpace(u) }
