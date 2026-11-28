package mailbox

import (
	"html"
	"regexp"
	"strings"
)

// Size caps. A newsletter is a few thousand words; anything far past this is a
// mistake or abuse, and the reader has ~380 KB of RAM to lay a chapter out in.
const (
	maxParagraphs = 2000
	maxTextBytes  = 1 << 20
)

var (
	// Elements whose content is never prose. Matched non-greedily and case-
	// insensitively; mail HTML is not well-formed enough to do better without
	// a parser, and no parser is in the dependency set.
	// Go's regexp has no backreferences, so each element gets its own.
	dropElementREs = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, tag := range []string{"script", "style", "head", "title", "noscript", "template", "svg"} {
			out = append(out, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</\s*`+tag+`\s*>`))
		}
		return out
	}()
	commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Tags that end a block of text become paragraph breaks.
	blockTagRE = regexp.MustCompile(`(?i)</?(p|div|br|tr|li|h[1-6]|blockquote|table|section|article|header|footer|ul|ol|hr|pre)\b[^>]*>`)
	anyTagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRE    = regexp.MustCompile(`[ \t\f\v\r\x{00a0}]+`)
	breaksRE   = regexp.MustCompile(`\n\s*\n+`)
)

// htmlToText reduces an HTML body to plain text with blank lines between
// blocks. It is deliberately lossy: the goal is something readable on e-ink,
// not a faithful rendering, and dropping every tag means nothing from the
// sender's markup can reach the EPUB.
func htmlToText(s string) string {
	s = commentRE.ReplaceAllString(s, "")
	for _, re := range dropElementREs {
		s = re.ReplaceAllString(s, "")
	}
	s = blockTagRE.ReplaceAllString(s, "\n\n")
	s = anyTagRE.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// paragraphs splits text on blank lines and collapses whitespace inside each.
func paragraphs(text string) []string {
	if len(text) > maxTextBytes {
		text = text[:maxTextBytes]
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, block := range breaksRE.Split(text, -1) {
		lines := strings.Split(block, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimSpace(spaceRE.ReplaceAllString(l, " "))
		}
		p := strings.TrimSpace(strings.Join(lines, " "))
		p = strings.Join(strings.Fields(p), " ")
		if p == "" {
			continue
		}
		out = append(out, p)
		if len(out) == maxParagraphs {
			break
		}
	}
	return out
}

// bodyText picks the message's plain-text part, falling back to its HTML.
// Plain text is preferred because it is what the sender wrote for readers
// without HTML, which an e-ink reader effectively is.
func bodyText(m Message) string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	return htmlToText(m.HTML)
}
