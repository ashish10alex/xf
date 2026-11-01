package format

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/navigate"
)

// A config block's body is JavaScript, nearly always one object literal of
// strings, numbers, booleans, arrays and nested objects. Config formats
// that much itself, without sqlfluff (ADR 0024): one property to a line,
// two spaces to a level, `key: value`, an array on one line when it fits
// in 80 columns and holds no comment, no comma after the last item. Any
// other value (a call, an arrow function, a sum) is kept as written.
// Comments stay, and so does one blank line where there was one.
//
// The result is checked: its tokens must be the input's, less the commas
// that only end a list. Anything else is refused, with nothing changed.

const (
	configIndent = "  "
	configWidth  = 80
)

// Config formats the body of each config block of a sqlx file, and
// returns the rest of it byte for byte. A body it can't read is a
// ParseError, and no text.
func Config(src []byte) ([]byte, error) {
	crlf := bytes.Contains(src, []byte("\r\n"))
	text := src
	if crlf {
		text = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	}
	bodies, ok := navigate.ConfigBodies(text)
	if !ok {
		return nil, errors.New("a block or a ${ is never closed, so where the config block ends can't be told")
	}
	if len(bodies) == 0 {
		return src, nil
	}
	var out bytes.Buffer
	last := 0
	for _, b := range bodies {
		body, err := configBody(string(text[b[0]:b[1]]), lineIndent(text, b[0]))
		if err != nil {
			var ce *configError
			if errors.As(err, &ce) {
				line, col := lineCol(text, b[0]+ce.off)
				return nil, &ParseError{Line: line, Col: col, Msg: "in the config block, " + ce.msg}
			}
			return nil, err
		}
		out.Write(text[last:b[0]])
		out.WriteString(body)
		last = b[1]
	}
	out.Write(text[last:])
	res := out.Bytes()
	if crlf {
		res = bytes.ReplaceAll(res, []byte("\n"), []byte("\r\n"))
	}
	return res, nil
}

// configError is a body that can't be read, at an offset in it.
type configError struct {
	off int
	msg string
}

func (e *configError) Error() string { return e.msg }

// lineIndent is the spaces and tabs that start the line holding text[i].
func lineIndent(text []byte, i int) string {
	start := bytes.LastIndexByte(text[:i], '\n') + 1
	end := start
	for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
		end++
	}
	return string(text[start:end])
}

// configBody formats one body, the text between the braces of a config
// block whose line starts with base.
func configBody(body, base string) (string, error) {
	toks, err := tokenize(body)
	if err != nil {
		return "", err
	}
	if len(toks) == 0 {
		return body, nil
	}
	p := &parser{src: body, toks: toks}
	top, err := p.list("", true)
	if err != nil {
		return "", err
	}
	out := printList(top, base+configIndent) + "\n" + base
	got, err := tokenize(out)
	if err != nil || !sameTokens(toks, got) {
		return "", &configError{msg: "formatting would change what it says, so it was left as it is"}
	}
	return out, nil
}

// ---- tokens ----

type tokKind uint8

const (
	tokWord    tokKind = iota // a name, a number, a keyword, a.b, ...rest
	tokString                 // '…', "…", `…` or a /regex/
	tokPunct                  // { } [ ] ( ) , :
	tokOp                     // any other character
	tokComment                // // … (less the spaces after it) or /* … */
)

type token struct {
	kind       tokKind
	text       string
	start, end int
	// nl is how many newlines come between the token before (or the
	// start) and this one.
	nl int
}

func tokenize(s string) ([]token, error) {
	var toks []token
	nl := 0
	for i := 0; i < len(s); {
		c, start := s[i], i
		kind := tokOp
		switch {
		case c == '\n':
			nl++
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
			continue
		case strings.HasPrefix(s[i:], "//"):
			kind, i = tokComment, lineEndAt(s, i)
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return nil, &configError{i, "a /* comment is never closed"}
			}
			kind, i = tokComment, i+2+j+2
		case c == '"' || c == '\'':
			j, err := quotedEnd(s, i)
			if err != nil {
				return nil, err
			}
			kind, i = tokString, j
		case c == '`':
			j, err := templateEnd(s, i)
			if err != nil {
				return nil, err
			}
			kind, i = tokString, j
		case c == '/' && regexMayStart(toks):
			j, err := regexEnd(s, i)
			if err != nil {
				return nil, err
			}
			kind, i = tokString, j
		case isWordByte(c):
			for i < len(s) && isWordByte(s[i]) {
				i++
			}
			kind = tokWord
		case strings.IndexByte("{}[](),:", c) >= 0:
			kind, i = tokPunct, i+1
		default:
			i++
		}
		text := s[start:i]
		if kind == tokComment && strings.HasPrefix(text, "//") {
			text = strings.TrimRight(text, " \t\r")
		}
		toks = append(toks, token{kind: kind, text: text, start: start, end: i, nl: nl})
		nl = 0
	}
	return toks, nil
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c == '.' || c >= 0x80 ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// regexMayStart says a / after toks starts a regex rather than divides:
// it comes where a value starts, not after one.
func regexMayStart(toks []token) bool {
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		switch t.kind {
		case tokComment:
			continue
		case tokString:
			return false
		case tokWord:
			switch t.text {
			case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
				return true
			}
			return false
		case tokPunct:
			return !strings.Contains(")]}", t.text)
		}
		return true // an operator
	}
	return true
}

// regexEnd is the offset just past the /regex/flags at s[i].
func regexEnd(s string, i int) (int, error) {
	class := false
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if class {
				continue
			}
			for j++; j < len(s) && isWordByte(s[j]); j++ {
			}
			return j, nil
		case '\n':
			return 0, &configError{i, "a /regex/ is never closed on its line"}
		}
	}
	return 0, &configError{i, "a /regex/ is never closed"}
}

// lineEndAt is the offset of the newline that ends s[i]'s line, or len(s).
func lineEndAt(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(s)
}

// quotedEnd is the offset just past the '…' or "…" string at s[i].
func quotedEnd(s string, i int) (int, error) {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case s[i]:
			return j + 1, nil
		case '\n':
			return 0, &configError{i, "a string is never closed on its line"}
		}
	}
	return 0, &configError{i, "a string is never closed"}
}

// templateEnd is the offset just past the `…` template literal at s[i],
// stepping over its ${ … }.
func templateEnd(s string, i int) (int, error) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case s[j] == '\\':
			j++
		case s[j] == '`':
			return j + 1, nil
		case strings.HasPrefix(s[j:], "${"):
			k, err := braceEnd(s, j+2)
			if err != nil {
				return 0, err
			}
			j = k
		}
	}
	return 0, &configError{i, "a ` string is never closed"}
}

// braceEnd is the offset of the } that closes the { just before s[i].
func braceEnd(s string, i int) (int, error) {
	from := i - 2
	for depth := 1; i < len(s); {
		switch c := s[i]; {
		case c == '"' || c == '\'':
			j, err := quotedEnd(s, i)
			if err != nil {
				return 0, err
			}
			i = j
		case c == '`':
			j, err := templateEnd(s, i)
			if err != nil {
				return 0, err
			}
			i = j
		case strings.HasPrefix(s[i:], "//"):
			i = lineEndAt(s, i)
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return 0, &configError{i, "a /* comment is never closed"}
			}
			i += 2 + j + 2
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			if depth == 0 {
				return i, nil
			}
			i++
		default:
			i++
		}
	}
	return 0, &configError{max(from, 0), "a ${ is never closed"}
}

// sameTokens says a and b are the same code, less the commas that only
// end a list, which JavaScript reads the same either way, and the same
// comments in the same order (one may move past a comma).
func sameTokens(a, b []token) bool {
	a, b = significant(a), significant(b)
	split := func(toks []token) (code, comments []token) {
		for _, t := range toks {
			if t.kind == tokComment {
				comments = append(comments, t)
			} else {
				code = append(code, t)
			}
		}
		return code, comments
	}
	ac, am := split(a)
	bc, bm := split(b)
	return sameTexts(ac, bc) && sameTexts(am, bm)
}

func sameTexts(a, b []token) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].kind != b[i].kind || a[i].text != b[i].text {
			return false
		}
	}
	return true
}

// significant is toks less each comma followed, past any comments, by a
// closing bracket or the end.
func significant(toks []token) []token {
	var out []token
	for i, t := range toks {
		if t.kind == tokPunct && t.text == "," {
			j := i + 1
			for j < len(toks) && toks[j].kind == tokComment {
				j++
			}
			if j == len(toks) || (toks[j].kind == tokPunct && strings.Contains("}])", toks[j].text)) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// ---- reading ----

// node is a value: an object or an array (open is '{' or '['), or
// anything else, kept as written (text).
type node struct {
	open  byte
	text  string
	items []*item
	// tail is the comments after the last item; tailBlank says a blank
	// line comes before them.
	tail      []token
	tailBlank bool
}

// item is a property of an object (key is "" for one with no key, a
// shorthand or a ...spread) or an item of an array.
type item struct {
	lead  []token // comments on lines of their own before it
	blank bool    // a blank line before it
	key   string
	value *node
	trail []token // comments after it on its line
}

type parser struct {
	src  string
	toks []token
	pos  int
}

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *parser) isPunct(i int, text string) bool {
	return i < len(p.toks) && p.toks[i].kind == tokPunct && p.toks[i].text == text
}

// atEnd says the list being read ends here: at close, or for the body
// itself ("") at the end of the text.
func (p *parser) atEnd(close string) bool { return p.endsAt(p.pos, close) }

// endsAt says the list being read ends at toks[i].
func (p *parser) endsAt(i int, close string) bool {
	if close == "" {
		return i >= len(p.toks)
	}
	return p.isPunct(i, close)
}

func (p *parser) errHere(msg string) error {
	off := len(p.src)
	if t := p.peek(); t != nil {
		off = t.start
	}
	return &configError{off, msg}
}

// list reads the items of an object or array up to close.
func (p *parser) list(close string, object bool) (*node, error) {
	n := &node{}
	for {
		var lead []token
		blank := false
		for t := p.peek(); t != nil && t.kind == tokComment; t = p.peek() {
			if len(lead) == 0 {
				blank = t.nl >= 2
			}
			lead = append(lead, *t)
			p.pos++
		}
		if p.atEnd(close) {
			n.tail, n.tailBlank = lead, blank && len(n.items) > 0
			return n, nil
		}
		if p.peek() == nil {
			return nil, p.errHere("a { or [ is never closed")
		}
		if len(lead) == 0 {
			blank = p.peek().nl >= 2
		}
		it := &item{lead: lead, blank: blank}
		if t := p.peek(); object && (t.kind == tokWord || t.kind == tokString) && p.isPunct(p.pos+1, ":") {
			it.key = t.text
			p.pos += 2
			if t := p.peek(); t != nil && t.kind == tokComment {
				return nil, p.errHere("a comment between a key and its value isn't handled")
			}
		}
		v, err := p.value(close)
		if err != nil {
			return nil, err
		}
		it.value = v
		p.trailing(it)
		// Comments on lines of their own go with the item before a , that
		// follows them, else with what comes next.
		next := p.pos
		for next < len(p.toks) && p.toks[next].kind == tokComment {
			next++
		}
		if p.isPunct(next, ",") {
			for ; p.pos < next; p.pos++ {
				it.trail = append(it.trail, p.toks[p.pos])
			}
			p.pos++
			p.trailing(it)
		} else if !p.endsAt(next, close) {
			p.pos = next
			if close == "" {
				return nil, p.errHere("expected a , here")
			}
			return nil, p.errHere("expected a , or " + close + " here")
		}
		n.items = append(n.items, it)
	}
}

// trailing takes the comments that follow on the same line as it.
func (p *parser) trailing(it *item) {
	for t := p.peek(); t != nil && t.kind == tokComment && t.nl == 0; t = p.peek() {
		it.trail = append(it.trail, *t)
		p.pos++
	}
}

// value reads one value of a list that ends at close.
func (p *parser) value(close string) (*node, error) {
	if t := p.peek(); t != nil && t.kind == tokPunct && (t.text == "{" || t.text == "[") {
		if j := p.match(p.pos); j >= 0 && p.endsValue(j+1, close) {
			p.pos++
			inner := map[string]string{"{": "}", "[": "]"}[t.text]
			n, err := p.list(inner, t.text == "{")
			if err != nil {
				return nil, err
			}
			p.pos++
			n.open = t.text[0]
			return n, nil
		}
	}
	// Anything else is an expression: the tokens up to a , or a closing
	// bracket at its own level, kept as written.
	// At its own level a : belongs to a ? (a ternary): any other means a
	// , is missing before a key, and a ; ends a statement, which a value
	// isn't.
	start, depth, ternaries := p.pos, 0, 0
scan:
	for ; p.pos < len(p.toks); p.pos++ {
		t := p.toks[p.pos]
		if t.kind == tokComment && depth == 0 {
			break
		}
		if depth == 0 && t.kind == tokOp {
			switch {
			case t.text == ";":
				return nil, p.errHere("expected a , here")
			case t.text == "?" && p.ternary(p.pos):
				ternaries++
			}
		}
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case ":":
			if depth > 0 {
				break
			}
			if ternaries == 0 {
				return nil, &configError{p.toks[p.pos-1].start, "a , is missing before this"}
			}
			ternaries--
		case "{", "[", "(":
			depth++
		case "}", "]", ")":
			if depth == 0 {
				break scan
			}
			depth--
		case ",":
			if depth == 0 {
				break scan
			}
		}
	}
	if p.pos == start {
		return nil, p.errHere("a value is missing here")
	}
	if depth != 0 {
		return nil, &configError{p.toks[start].start, "a bracket in this value is never closed"}
	}
	return &node{text: p.src[p.toks[start].start:p.toks[p.pos-1].end]}, nil
}

// ternary says the ? at toks[i] starts a ternary, not a ?? or a ?.
func (p *parser) ternary(i int) bool {
	adjacent := func(j int, text string) bool {
		return j >= 0 && j < len(p.toks) && strings.HasPrefix(p.toks[j].text, text) &&
			(p.toks[j].start == p.toks[i].end || p.toks[j].end == p.toks[i].start)
	}
	return !adjacent(i-1, "?") && !adjacent(i+1, "?") && !adjacent(i+1, ".")
}

// match is the index of the token that closes the bracket at toks[i], or
// -1.
func (p *parser) match(i int) int {
	depth := 0
	for j := i; j < len(p.toks); j++ {
		if p.toks[j].kind != tokPunct {
			continue
		}
		switch p.toks[j].text {
		case "{", "[", "(":
			depth++
		case "}", "]", ")":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// endsValue says a value may end before toks[i]: what follows it, past
// any comments, is a , or the end of the list.
func (p *parser) endsValue(i int, close string) bool {
	for i < len(p.toks) && p.toks[i].kind == tokComment {
		i++
	}
	return p.isPunct(i, ",") || p.endsAt(i, close)
}

// ---- printing ----

// printList is the items of n, each starting on a new line at indent.
func printList(n *node, indent string) string {
	var b strings.Builder
	for i, it := range n.items {
		if it.blank && i > 0 {
			b.WriteString("\n")
		}
		for _, c := range it.lead {
			b.WriteString("\n" + indent + c.text)
		}
		line := indent
		if it.key != "" {
			line += it.key + ": "
		}
		comma := i < len(n.items)-1
		b.WriteString("\n" + line + printValue(it.value, indent, width(line), comma))
		if comma {
			b.WriteString(",")
		}
		for _, c := range it.trail {
			b.WriteString(" " + c.text)
		}
	}
	if n.tailBlank {
		b.WriteString("\n")
	}
	for _, c := range n.tail {
		b.WriteString("\n" + indent + c.text)
	}
	return b.String()
}

// printValue is v placed at column col of a line that starts with indent;
// comma says a comma follows it.
func printValue(v *node, indent string, col int, comma bool) string {
	if v.open == 0 {
		return v.text
	}
	if len(v.items) == 0 && len(v.tail) == 0 {
		return map[byte]string{'{': "{}", '[': "[]"}[v.open]
	}
	if v.open == '[' {
		if flat, ok := flatArray(v); ok {
			end := col + width(flat)
			if comma {
				end++
			}
			if end <= configWidth {
				return flat
			}
		}
	}
	closer := map[byte]string{'{': "}", '[': "]"}[v.open]
	return string(v.open) + printList(v, indent+configIndent) + "\n" + indent + closer
}

// flatArray is v on one line, if it can be: no comments, and no object or
// value that takes more than one line.
func flatArray(v *node) (string, bool) {
	if len(v.tail) > 0 {
		return "", false
	}
	parts := make([]string, len(v.items))
	for i, it := range v.items {
		if len(it.lead) > 0 || len(it.trail) > 0 {
			return "", false
		}
		switch x := it.value; {
		case x.open == 0 && !strings.Contains(x.text, "\n"):
			parts[i] = x.text
		case x.open == '[':
			s, ok := flatArray(x)
			if !ok {
				return "", false
			}
			parts[i] = s
		case x.open == '{' && len(x.items) == 0 && len(x.tail) == 0:
			parts[i] = "{}"
		default:
			return "", false
		}
	}
	return "[" + strings.Join(parts, ", ") + "]", true
}

func width(s string) int { return utf8.RuneCountInString(s) }
