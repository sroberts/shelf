package library

import (
	"fmt"
	"strings"
	"unicode"
)

// The query language is deliberately small. It covers what someone actually
// types at a book list -- a few words, or a field filter, or both -- and
// nothing more:
//
//	earthsea                      bare words go to the full-text index
//	author:"le guin"              field filters, quoted when they contain spaces
//	tag:queue and not tag:done    and / or / not, with parentheses
//	series:Earthsea format:epub   adjacent terms are implicitly ANDed
//
// Anything more elaborate belongs in SQL, and `shelf ls --json | jq` covers it.

// node is a parsed query expression.
type node interface {
	// compile appends a SQL condition and its arguments.
	compile(*condBuilder)
}

type andNode struct{ children []node }
type orNode struct{ children []node }
type notNode struct{ child node }

// fieldNode is a `field:value` filter.
type fieldNode struct {
	field string
	value string
}

// textNode is a bare word or phrase, matched through the full-text index.
type textNode struct{ text string }

// condBuilder accumulates a SQL condition.
type condBuilder struct {
	sb   strings.Builder
	args []any
}

func (c *condBuilder) write(s string) { c.sb.WriteString(s) }
func (c *condBuilder) arg(v any)      { c.args = append(c.args, v) }
func (c *condBuilder) String() string { return c.sb.String() }

func (n andNode) compile(c *condBuilder) { compileGroup(c, n.children, " AND ") }
func (n orNode) compile(c *condBuilder)  { compileGroup(c, n.children, " OR ") }

func compileGroup(c *condBuilder, children []node, op string) {
	if len(children) == 0 {
		c.write("1=1")
		return
	}
	c.write("(")
	for i, child := range children {
		if i > 0 {
			c.write(op)
		}
		child.compile(c)
	}
	c.write(")")
}

func (n notNode) compile(c *condBuilder) {
	c.write("NOT (")
	n.child.compile(c)
	c.write(")")
}

func (n textNode) compile(c *condBuilder) {
	// Prefix matching makes an incremental filter feel responsive: typing
	// "earth" should find "Earthsea" before the word is finished.
	c.write("books.id IN (SELECT rowid FROM books_fts WHERE books_fts MATCH ?)")
	c.arg(ftsQuery(n.text))
}

func (n fieldNode) compile(c *condBuilder) {
	like := "%" + escapeLike(n.value) + "%"

	switch n.field {
	case "title":
		c.write("title LIKE ? ESCAPE '\\'")
		c.arg(like)
	case "author":
		c.write("(author_sort LIKE ? ESCAPE '\\' OR authors LIKE ? ESCAPE '\\')")
		c.arg(like)
		c.arg(like)
	case "series":
		c.write("series LIKE ? ESCAPE '\\'")
		c.arg(like)
	case "publisher":
		c.write("publisher LIKE ? ESCAPE '\\'")
		c.arg(like)
	case "path":
		c.write("path LIKE ? ESCAPE '\\'")
		c.arg(like)

	case "tag":
		// Tags match exactly (case-insensitively); a tag filter is a set
		// membership test, not a search.
		c.write("EXISTS (SELECT 1 FROM tags WHERE tags.book_id = books.id AND tags.tag = ? COLLATE NOCASE)")
		c.arg(n.value)

	case "format":
		c.write("format = ?")
		c.arg(strings.ToLower(n.value))
	case "language", "lang":
		c.write("language = ? COLLATE NOCASE")
		c.arg(n.value)

	default:
		// An unknown field is treated as free text rather than an error, so a
		// stray colon in a title does not produce a parse failure.
		textNode{text: n.field + ":" + n.value}.compile(c)
	}
}

// ftsQuery converts user input into an FTS5 MATCH expression.
//
// FTS5 has its own operator syntax, and passing raw user input through would
// turn a stray quote or hyphen into a syntax error. Every token is quoted, and
// a trailing prefix wildcard is added to the last one.
func ftsQuery(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return `""`
	}

	parts := make([]string, 0, len(fields))
	for i, f := range fields {
		quoted := `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		if i == len(fields)-1 {
			quoted += "*"
		}
		parts = append(parts, quoted)
	}
	return strings.Join(parts, " ")
}

// escapeLike escapes the LIKE wildcards so a literal % or _ in a search term
// does not silently match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// --- lexer ---

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokWord
	tokColon
	tokLParen
	tokRParen
)

type token struct {
	kind tokenKind
	text string
	// quoted records that the word came from a quoted string, so that "and"
	// in quotes is a search term rather than an operator.
	quoted bool
}

func lex(s string) []token {
	var toks []token
	runes := []rune(s)

	for i := 0; i < len(runes); {
		r := runes[i]

		switch {
		case unicode.IsSpace(r):
			i++

		case r == '(':
			toks = append(toks, token{kind: tokLParen})
			i++
		case r == ')':
			toks = append(toks, token{kind: tokRParen})
			i++
		case r == ':':
			toks = append(toks, token{kind: tokColon})
			i++

		case r == '"' || r == '\'':
			quote := r
			i++
			var sb strings.Builder
			for i < len(runes) && runes[i] != quote {
				sb.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				i++ // closing quote
			}
			toks = append(toks, token{kind: tokWord, text: sb.String(), quoted: true})

		default:
			var sb strings.Builder
			for i < len(runes) {
				c := runes[i]
				if unicode.IsSpace(c) || c == '(' || c == ')' || c == ':' || c == '"' || c == '\'' {
					break
				}
				sb.WriteRune(c)
				i++
			}
			toks = append(toks, token{kind: tokWord, text: sb.String()})
		}
	}
	return append(toks, token{kind: tokEOF})
}

// --- parser ---

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) atEnd() bool { return p.toks[p.pos].kind == tokEOF }

// isOperator reports whether an unquoted word is a boolean keyword.
func isOperator(t token, word string) bool {
	return t.kind == tokWord && !t.quoted && strings.EqualFold(t.text, word)
}

// ParseQuery parses a query string into a filter. An empty query matches
// everything.
func parseQuery(s string) (node, error) {
	p := &parser{toks: lex(s)}
	if p.atEnd() {
		return andNode{}, nil
	}

	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if !p.atEnd() {
		return nil, fmt.Errorf("unexpected %q in query", p.peek().text)
	}
	return n, nil
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	var children []node
	for isOperator(p.peek(), "or") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if children == nil {
			children = []node{left}
		}
		children = append(children, right)
	}
	if children == nil {
		return left, nil
	}
	return orNode{children: children}, nil
}

func (p *parser) parseAnd() (node, error) {
	var children []node

	for {
		if p.atEnd() || p.peek().kind == tokRParen || isOperator(p.peek(), "or") {
			break
		}
		// An explicit "and" is optional; adjacent terms are ANDed anyway.
		if isOperator(p.peek(), "and") {
			p.next()
			continue
		}

		n, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		children = append(children, n)
	}

	switch len(children) {
	case 0:
		return nil, fmt.Errorf("empty expression")
	case 1:
		return children[0], nil
	default:
		return andNode{children: children}, nil
	}
}

func (p *parser) parseUnary() (node, error) {
	if isOperator(p.peek(), "not") || (p.peek().kind == tokWord && p.peek().text == "-" && !p.peek().quoted) {
		p.next()
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notNode{child: child}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	t := p.peek()

	switch t.kind {
	case tokLParen:
		p.next()
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		p.next()
		return n, nil

	case tokWord:
		p.next()
		// A word followed by ":" is a field filter.
		if p.peek().kind == tokColon && !t.quoted {
			p.next()
			if p.peek().kind != tokWord {
				return nil, fmt.Errorf("expected a value after %q:", t.text)
			}
			value := p.next()
			return fieldNode{field: strings.ToLower(t.text), value: value.text}, nil
		}
		// A leading "-" negates, matching common search-box convention.
		if !t.quoted && strings.HasPrefix(t.text, "-") && len(t.text) > 1 {
			return notNode{child: textNode{text: strings.TrimPrefix(t.text, "-")}}, nil
		}
		return textNode{text: t.text}, nil

	case tokRParen:
		return nil, fmt.Errorf("unexpected closing parenthesis")
	}
	return nil, fmt.Errorf("unexpected end of query")
}
