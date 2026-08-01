package library

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Naming turns metadata into a relative path using a template such as
//
//	{author}/{series} {series_index:02d} - {title}
//
// Fields that are empty collapse cleanly: a standalone book with no series
// renders as "Herman Melville/Moby-Dick" rather than leaving " 00 - " behind.

// maxComponent is the FAT32 per-component limit in bytes. The device's SD card
// is FAT32, so names are constrained here rather than at transfer time, where a
// rename would cost the book its reading position.
const maxComponent = 255

// fatReserved are the characters FAT32 and Windows forbid in a filename.
const fatReserved = `<>:"/\|?*`

// reservedNames are DOS device names that remain special on FAT volumes.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

var placeholderRE = regexp.MustCompile(`\{([a-z_]+)(?::([^}]+))?\}`)

// separatorEscaper neutralizes path separators inside substituted field values.
var separatorEscaper = strings.NewReplacer("/", "_", `\`, "_")

// RenderTemplate produces a relative, slash-separated path (without extension)
// for a book.
func RenderTemplate(tmpl string, b *Book) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return "", fmt.Errorf("naming template is empty")
	}

	var unknown []string
	out := placeholderRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		groups := placeholderRE.FindStringSubmatch(m)
		name, format := groups[1], groups[2]

		value, ok := templateField(b, name, format)
		if !ok {
			unknown = append(unknown, name)
			return ""
		}
		// Only literal separators in the template may create directory levels.
		// A field value containing a slash -- an author such as "AC/DC", or a
		// title with a date in it -- must not silently nest the book one level
		// deeper than the template says.
		return separatorEscaper.Replace(value)
	})
	if len(unknown) > 0 {
		return "", fmt.Errorf("unknown template field(s): %s", strings.Join(unknown, ", "))
	}

	// Tidy each path component independently so an empty field cannot leave
	// dangling separators or produce an empty directory level.
	parts := strings.Split(out, "/")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = tidyComponent(p)
		if p == "" {
			continue
		}
		cleaned = append(cleaned, SanitizeComponent(p))
	}
	if len(cleaned) == 0 {
		return "", fmt.Errorf("template %q produced an empty path", tmpl)
	}
	return path.Join(cleaned...), nil
}

func templateField(b *Book, name, format string) (string, bool) {
	switch name {
	case "author", "author_sort":
		if name == "author_sort" || b.DisplayAuthor() == "" {
			return b.AuthorSort, true
		}
		return b.DisplayAuthor(), true
	case "title":
		return b.DisplayTitle(), true
	case "series":
		return b.Series, true
	case "series_index":
		// An index without a series is meaningless and would render as a
		// stray number in the filename.
		if b.Series == "" {
			return "", true
		}
		return formatIndex(b.SeriesIndex, format), true
	case "publisher":
		return b.Publisher, true
	case "language":
		return b.Language, true
	case "format":
		return string(b.Format), true
	case "year":
		if len(b.PubDate) >= 4 {
			return b.PubDate[:4], true
		}
		return "", true
	}
	return "", false
}

// formatIndex applies a printf-style width spec such as "02d" to a series index.
func formatIndex(v float64, spec string) string {
	if spec == "" {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	if strings.HasSuffix(spec, "d") {
		return fmt.Sprintf("%"+spec, int(v))
	}
	if strings.HasSuffix(spec, "f") {
		return fmt.Sprintf("%"+spec, v)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// tidyComponent removes the separator debris an empty field leaves behind.
var (
	multiSpaceRE = regexp.MustCompile(`\s+`)
	danglingRE   = regexp.MustCompile(`^[\s\-_,]+|[\s\-_,]+$`)
)

func tidyComponent(s string) string {
	s = multiSpaceRE.ReplaceAllString(s, " ")
	s = danglingRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// SanitizeComponent makes a single path component safe for a FAT32 volume.
//
// This runs at import rather than at sync time on purpose. A name that has to
// be rewritten during transfer would change the book's device path, and every
// path change costs the reader their position in that book.
func SanitizeComponent(s string) string {
	s = NormalizeString(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case strings.ContainsRune(fatReserved, r):
			b.WriteRune('_')
		case r < 0x20 || r == 0x7f:
			// Control characters are dropped rather than substituted.
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}

	out := strings.TrimSpace(multiSpaceRE.ReplaceAllString(b.String(), " "))

	// FAT and Windows both mishandle trailing dots and spaces.
	out = strings.TrimRight(out, ". ")

	if reservedNames[strings.ToLower(out)] {
		out += "_"
	}
	if out == "" {
		out = "untitled"
	}
	return truncateBytes(out, maxComponent)
}

// truncateBytes limits a string to n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], ". ")
}

// SanitizePath sanitizes every component of a slash-separated relative path.
func SanitizePath(p string) string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, SanitizeComponent(part))
	}
	return path.Join(out...)
}

// ValidFATName reports whether a component is already safe for FAT32, and why
// not when it is not. Used by the sync planner to warn before transferring.
func ValidFATName(s string) (bool, string) {
	switch {
	case s == "":
		return false, "empty name"
	case len(s) > maxComponent:
		return false, fmt.Sprintf("name is %d bytes, over the %d-byte limit", len(s), maxComponent)
	case strings.ContainsAny(s, fatReserved):
		return false, fmt.Sprintf("contains a reserved character (%s)", fatReserved)
	case strings.HasSuffix(s, ".") || strings.HasSuffix(s, " "):
		return false, "ends with a dot or space"
	case reservedNames[strings.ToLower(s)]:
		return false, "is a reserved device name"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false, "contains a control character"
		}
	}
	return true, ""
}
