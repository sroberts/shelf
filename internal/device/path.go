package device

import (
	"fmt"
	"path"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Path is a path on the device.
//
// It is a distinct type from a local filesystem path on purpose. Device paths
// are always forward-slash separated regardless of the host OS, so joining one
// with filepath.Join would silently produce backslashes on some platforms and
// corrupt the request. The type makes that a compile error instead.
type Path string

// Protected paths. shelf must never write, rename, move, or delete inside
// these. /.crosspoint holds the device's render and progress cache, including
// the progress.bin files that hold reading positions; the other two are
// filesystem bookkeeping.
const (
	crosspointDir = ".crosspoint"
	sysVolInfo    = "System Volume Information"
	xtCache       = "XTCache"
)

// NewPath normalizes a device path: absolute, forward-slash, no trailing slash.
func NewPath(p string) Path {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "/"
	}
	return Path(cleaned)
}

// Join appends elements to a device path.
func (p Path) Join(elems ...string) Path {
	parts := append([]string{string(p)}, elems...)
	return NewPath(path.Join(parts...))
}

// Dir returns the parent directory.
func (p Path) Dir() Path { return NewPath(path.Dir(string(p))) }

// Base returns the final element.
func (p Path) Base() string { return path.Base(string(p)) }

// String renders the path.
func (p Path) String() string { return string(p) }

// Depth counts path components, used to order mkdir operations so that parents
// are created before their children.
func (p Path) Depth() int {
	trimmed := strings.Trim(string(p), "/")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "/") + 1
}

// Ancestors returns every parent directory from shallowest to deepest,
// excluding the root and the path itself.
func (p Path) Ancestors() []Path {
	trimmed := strings.Trim(string(p), "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")

	var out []Path
	for i := 1; i < len(parts); i++ {
		out = append(out, NewPath("/"+strings.Join(parts[:i], "/")))
	}
	return out
}

// IsProtected reports whether a path lies inside a reserved area.
//
// This is checked at the lowest layer, immediately before a request is built,
// so that no caller anywhere in shelf can reach a protected path by
// constructing one itself.
func IsProtected(p Path) bool {
	for _, part := range strings.Split(strings.Trim(string(p), "/"), "/") {
		if part == "" {
			continue
		}
		if part == crosspointDir || part == sysVolInfo || part == xtCache {
			return true
		}
	}
	return false
}

// checkWritable rejects protected paths before any mutating request.
func checkWritable(p Path) error {
	if IsProtected(p) {
		return fmt.Errorf("%w: %s is reserved by the firmware", ErrProtectedPath, p)
	}
	return nil
}

// Fold returns a case-folded, NFC-normalized form for collision detection.
//
// The device's SD card is FAT32, which is case-insensitive regardless of the
// host, so "Moby-Dick.epub" and "moby-dick.epub" are one file there even on a
// Linux workstation where they coexist happily. NFC is applied for the same
// reason paths are normalized everywhere else: APFS hands back NFD, and two
// spellings of the same name must fold together.
//
// This matches library.FoldPath deliberately. The two run on opposite ends of
// the pipeline -- import decides what a local file is named, the planner
// decides where it lands -- and they have to agree on what a collision is.
func (p Path) Fold() string {
	return strings.ToLower(norm.NFC.String(string(p)))
}

// IsHidden reports whether the final component is a dotfile. The firmware
// omits these from listings unless showHiddenFiles is enabled, which is why
// shelf keeps its own state in a visible /shelf directory.
func IsHidden(p Path) bool {
	base := p.Base()
	return strings.HasPrefix(base, ".") && base != "." && base != "/"
}
