package convert

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
)

// Quality assessment of a converted EPUB.
//
// The spec asks shelf to "be honest in the UI about what conversion produces":
// reflowable text PDFs convert well, two-column academic papers convert poorly,
// and scanned page images convert to garbage without OCR. Finding that out on
// the device, after a slow transfer, is the bad outcome.
//
// This measures the output rather than guessing about the input, and reports
// what it measured alongside the verdict. Deliberately conservative: it flags
// the failure mode it can detect reliably (a scan with no text layer, which
// produces an EPUB of images and almost no words) and does not pretend to
// detect column mangling, which has no heuristic here that survives contact
// with real documents.

// Quality is the headline verdict.
type Quality string

const (
	// QualityGood means substantial extracted text and nothing alarming.
	QualityGood Quality = "good"

	// QualityQuestionable means it converted, but something looks off enough
	// to be worth a look before syncing.
	QualityQuestionable Quality = "questionable"

	// QualityPoor means the output is very unlikely to be readable — almost
	// always a scanned PDF with no text layer.
	QualityPoor Quality = "poor"

	// QualityUnknown means the output could not be inspected.
	QualityUnknown Quality = "unknown"
)

// Assessment is what inspection found.
type Assessment struct {
	Quality Quality `json:"quality"`
	// Reasons are human-readable, and are the whole point: a bare verdict is
	// not actionable.
	Reasons []string `json:"reasons,omitempty"`

	TextChars  int   `json:"text_chars"`
	Images     int   `json:"images"`
	Documents  int   `json:"documents"`
	ImageBytes int64 `json:"image_bytes"`
	TextPerDoc int   `json:"text_per_document"`
}

// OK reports whether the output is worth syncing without comment.
func (a Assessment) OK() bool { return a.Quality == QualityGood }

// Summary renders a one-line verdict.
func (a Assessment) Summary() string {
	if len(a.Reasons) == 0 {
		return string(a.Quality)
	}
	return fmt.Sprintf("%s — %s", a.Quality, strings.Join(a.Reasons, "; "))
}

// Thresholds for the heuristics. Named rather than inline so the reasoning is
// visible and so they can be tuned against real documents.
const (
	// Below this, an EPUB has essentially no readable prose.
	minTotalChars = 500

	// A typical converted page carries hundreds of characters. Well under a
	// hundred per document, with images present, is the signature of a scan.
	scannedCharsPerDoc = 100

	// Images outweighing text this heavily suggests page scans rather than
	// illustrations.
	imageHeavyRatio = 20
)

// Assess inspects a converted EPUB and reports whether it looks readable.
//
// It never fails: an unreadable archive yields QualityUnknown rather than an
// error, because assessment is advisory and must not turn a successful
// conversion into a failed one.
func Assess(epubPath string) Assessment {
	a := Assessment{Quality: QualityUnknown}

	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		a.Reasons = append(a.Reasons, "output could not be opened as an EPUB")
		return a
	}
	defer zr.Close()

	for _, f := range zr.File {
		name := strings.ToLower(path.Clean(f.Name))

		switch {
		case isImageEntry(name):
			a.Images++
			a.ImageBytes += int64(f.UncompressedSize64)

		case isContentDocument(name):
			a.Documents++
			if text, err := extractText(f); err == nil {
				a.TextChars += text
			}
		}
	}

	if a.Documents > 0 {
		a.TextPerDoc = a.TextChars / a.Documents
	}
	return classify(a)
}

// classify turns measurements into a verdict.
func classify(a Assessment) Assessment {
	switch {
	case a.Documents == 0:
		a.Quality = QualityPoor
		a.Reasons = append(a.Reasons, "no content documents in the output")

	case a.TextChars < minTotalChars && a.Images > 0:
		a.Quality = QualityPoor
		a.Reasons = append(a.Reasons, fmt.Sprintf(
			"only %d characters of text across %d images — this looks like a scanned PDF "+
				"with no text layer; run it through OCR first",
			a.TextChars, a.Images))

	case a.TextChars < minTotalChars:
		a.Quality = QualityPoor
		a.Reasons = append(a.Reasons, fmt.Sprintf(
			"only %d characters of text extracted", a.TextChars))

	case a.Images > 0 && a.TextPerDoc < scannedCharsPerDoc:
		a.Quality = QualityQuestionable
		a.Reasons = append(a.Reasons, fmt.Sprintf(
			"averages %d characters per document across %d images — possibly a partial scan",
			a.TextPerDoc, a.Images))

	case a.Images > 0 && a.TextChars > 0 && a.ImageBytes/int64(max(a.TextChars, 1)) > imageHeavyRatio:
		a.Quality = QualityQuestionable
		a.Reasons = append(a.Reasons, fmt.Sprintf(
			"%s of images against %d characters of text — will be large on the device "+
				"and may benefit from optimization",
			humanBytes(a.ImageBytes), a.TextChars))

	default:
		a.Quality = QualityGood
	}
	return a
}

// isImageEntry reports whether an archive entry is an image.
func isImageEntry(name string) bool {
	switch path.Ext(name) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".tif", ".tiff", ".svg":
		return true
	}
	return false
}

// isContentDocument reports whether an entry is reading content, excluding the
// navigation and packaging files that carry no prose.
func isContentDocument(name string) bool {
	switch path.Ext(name) {
	case ".xhtml", ".html", ".htm":
	default:
		return false
	}
	base := path.Base(name)
	// Navigation documents are structure, not text, and counting them would
	// inflate the character count of an otherwise empty book.
	return base != "nav.xhtml" && base != "toc.xhtml" && base != "toc.ncx"
}

// extractText counts visible characters in an XHTML document, skipping markup
// and the contents of script and style elements.
func extractText(f *zip.File) (int, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// Cap the read: a pathological document should not be able to stall a scan.
	data, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	if err != nil {
		return 0, err
	}
	return countVisibleText(string(data)), nil
}

// countVisibleText strips tags and counts non-space characters.
//
// A deliberately crude parser: this is a word-volume metric, not a renderer,
// and pulling in an HTML parser for a heuristic would be the wrong trade.
func countVisibleText(s string) int {
	lower := strings.ToLower(s)
	for _, tag := range []string{"script", "style"} {
		s, lower = stripElement(s, lower, tag)
	}

	var count int
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case inTag:
		case unicode.IsSpace(r):
		default:
			count++
		}
	}
	return count
}

// stripElement removes an element and its contents, case-insensitively.
func stripElement(s, lower, tag string) (string, string) {
	open, close := "<"+tag, "</"+tag
	for {
		i := strings.Index(lower, open)
		if i < 0 {
			return s, lower
		}
		j := strings.Index(lower[i:], close)
		if j < 0 {
			return s[:i], lower[:i]
		}
		end := i + j + len(close)
		// Consume through the closing '>'.
		if k := strings.IndexByte(s[end:], '>'); k >= 0 {
			end += k + 1
		}
		s = s[:i] + s[end:]
		lower = lower[:i] + lower[end:]
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
