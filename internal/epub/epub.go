// Package epub reads and writes EPUB 2 and EPUB 3 metadata in place.
//
// The design constraint that shapes this package: shelf edits books the user
// already owns, so a metadata write must never invalidate the archive. Reads
// are lenient (real-world EPUBs are full of small violations); writes are
// conservative and preserve every byte they were not explicitly asked to
// change.
package epub

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Namespaces used in the OCF container and OPF package documents.
const (
	nsContainer = "urn:oasis:names:tc:opendocument:xmlns:container"
	nsOPF       = "http://www.idpf.org/2007/opf"
	nsDC        = "http://purl.org/dc/elements/1.1/"

	containerPath = "META-INF/container.xml"
	mimetypePath  = "mimetype"
	mimetypeValue = "application/epub+zip"

	// Guard against absurd OPF documents; a package document is text and a
	// large one is a few hundred KB.
	maxOPFSize = 32 << 20
)

// Errors returned by this package. Callers distinguish "this file is not a
// usable EPUB" from I/O failure, because a scan must skip the former and
// report the latter.
var (
	ErrNotEPUB      = errors.New("epub: not an EPUB archive")
	ErrNoContainer  = errors.New("epub: missing META-INF/container.xml")
	ErrNoRootfile   = errors.New("epub: container.xml declares no rootfile")
	ErrNoOPF        = errors.New("epub: package document not found")
	ErrNoCover      = errors.New("epub: no cover image")
	ErrCorruptOPF   = errors.New("epub: malformed package document")
	ErrMissingEntry = errors.New("epub: manifest entry missing from archive")
)

// File is an open EPUB archive.
type File struct {
	zr     *zip.Reader
	closer io.Closer

	opfPath string // archive-relative path to the package document
	opfData []byte // raw bytes, retained for byte-preserving write-back
	pkg     *pkgDoc
}

// Open opens an EPUB file for reading.
func Open(name string) (*File, error) {
	rc, err := zip.OpenReader(name)
	if err != nil {
		// A file that is not a zip is not an EPUB; say so in those terms.
		if errors.Is(err, zip.ErrFormat) {
			return nil, fmt.Errorf("%s: %w", name, ErrNotEPUB)
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	f, err := newFile(&rc.Reader)
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f.closer = rc
	return f, nil
}

// OpenReader opens an EPUB from an in-memory or otherwise seekable source.
func OpenReader(r io.ReaderAt, size int64) (*File, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		if errors.Is(err, zip.ErrFormat) {
			return nil, ErrNotEPUB
		}
		return nil, err
	}
	return newFile(zr)
}

func newFile(zr *zip.Reader) (*File, error) {
	f := &File{zr: zr}

	opfPath, err := f.rootfilePath()
	if err != nil {
		return nil, err
	}
	f.opfPath = opfPath

	data, err := f.readEntry(opfPath, maxOPFSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoOPF, opfPath)
	}
	f.opfData = data

	pkg, err := parsePackage(data)
	if err != nil {
		return nil, err
	}
	f.pkg = pkg
	return f, nil
}

// Close releases the underlying file handle. Safe on an OpenReader-created
// File, where there is nothing to close.
func (f *File) Close() error {
	if f.closer != nil {
		return f.closer.Close()
	}
	return nil
}

// OPFPath is the archive-relative path to the package document.
func (f *File) OPFPath() string { return f.opfPath }

// Version reports the OPF package version, e.g. "2.0" or "3.0".
func (f *File) Version() string { return f.pkg.Version }

// container mirrors META-INF/container.xml.
type container struct {
	XMLName   xml.Name `xml:"urn:oasis:names:tc:opendocument:xmlns:container container"`
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

// rootfilePath resolves the package document path from the OCF container.
func (f *File) rootfilePath() (string, error) {
	data, err := f.readEntry(containerPath, 1<<20)
	if err != nil {
		return "", ErrNoContainer
	}

	var c container
	if err := unmarshalLenient(data, &c); err != nil {
		return "", fmt.Errorf("%w: container.xml: %v", ErrCorruptOPF, err)
	}
	if len(c.Rootfiles) == 0 {
		return "", ErrNoRootfile
	}

	// Prefer the OPF media type; some archives list several rootfiles.
	for _, rf := range c.Rootfiles {
		if rf.MediaType == "application/oebps-package+xml" && rf.FullPath != "" {
			return path.Clean(rf.FullPath), nil
		}
	}
	if c.Rootfiles[0].FullPath == "" {
		return "", ErrNoRootfile
	}
	return path.Clean(c.Rootfiles[0].FullPath), nil
}

// readEntry reads a single archive entry, refusing entries larger than limit.
func (f *File) readEntry(name string, limit int64) ([]byte, error) {
	e := f.entry(name)
	if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrMissingEntry, name)
	}
	rc, err := e.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}

// entry finds an archive entry by name, tolerating the leading "./" that some
// producers emit and falling back to a case-insensitive match.
func (f *File) entry(name string) *zip.File {
	name = path.Clean(name)
	for _, e := range f.zr.File {
		if path.Clean(e.Name) == name {
			return e
		}
	}
	for _, e := range f.zr.File {
		if strings.EqualFold(path.Clean(e.Name), name) {
			return e
		}
	}
	return nil
}

// resolveHref turns an OPF-relative href into an archive-relative path.
func (f *File) resolveHref(href string) string {
	if i := strings.IndexAny(href, "#?"); i >= 0 {
		href = href[:i]
	}
	href = unescapeHref(href)
	if strings.HasPrefix(href, "/") {
		return path.Clean(strings.TrimPrefix(href, "/"))
	}
	return path.Clean(path.Join(path.Dir(f.opfPath), href))
}

// unescapeHref decodes the percent-encoding that OPF hrefs may carry. It is
// deliberately minimal: a failed decode returns the input rather than an error,
// since a literal "%" in a filename is more likely than a real escape.
func unescapeHref(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// unmarshalLenient decodes XML that may declare a legacy or incorrect charset.
// Real libraries contain EPUBs with windows-1252 declarations and UTF-8 bytes,
// and refusing to read them helps nobody.
func unmarshalLenient(data []byte, v any) error {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	return dec.Decode(v)
}

// IsEPUB reports whether the file at name looks like an EPUB, cheaply. It
// checks the zip structure and the mimetype entry without parsing the OPF.
func IsEPUB(name string) bool {
	fi, err := os.Stat(name)
	if err != nil || fi.IsDir() || fi.Size() < 22 { // 22 = minimum zip EOCD
		return false
	}
	rc, err := zip.OpenReader(name)
	if err != nil {
		return false
	}
	defer rc.Close()

	for _, e := range rc.File {
		if path.Clean(e.Name) == containerPath {
			return true
		}
	}
	return false
}
