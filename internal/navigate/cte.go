package navigate

import (
	"bytes"
	"slices"
)

// A port of vscode-dataform-tools' src/cteScanner.ts: it finds CTE
// definitions in sqlx text without a SQL parser. Comments, strings, backtick
// identifiers and ${...} templates are skipped; SQL inside JS template
// literals within ${...} (e.g. ${when(incremental(), `WITH ...`)}) is scanned
// too. Keep the two in step.

// cte is one CTE definition. Offsets are bytes into the scanned text.
type cte struct {
	Name string
	// Start is the start of the name token, including a leading backtick.
	Start int
	// End is just past the ) that closes the CTE's body.
	End int
	// NameStart and NameEnd bound the name without backticks.
	NameStart, NameEnd int
	// ScopeStart is the start of the WITH keyword the CTE belongs to.
	ScopeStart int
	// ScopeEnd is the end of the query the WITH clause belongs to: the
	// enclosing ), a ;, or the end of the segment.
	ScopeEnd int
}

type tokenKind int

const (
	tokWord tokenKind = iota
	tokQuoted
	tokOpen
	tokClose
	tokComma
	tokOther
)

type token struct {
	kind                 tokenKind
	start, end           int
	innerStart, innerEnd int
}

// findCTEs returns every CTE definition in text, in order.
func findCTEs(text []byte) []cte {
	var segments [][]token
	scanSQL(text, 0, false, &segments)
	var out []cte
	for _, toks := range segments {
		out = append(out, ctesInTokens(text, toks)...)
	}
	slices.SortFunc(out, func(a, b cte) int { return a.Start - b.Start })
	return out
}

// findCTEDefinition resolves the CTE called name for a reference at offset,
// ignoring case. It prefers the innermost WITH scope containing the offset,
// then the closest definition above it.
func findCTEDefinition(text []byte, name string, offset int) (cte, bool) {
	var candidates []cte
	for _, c := range findCTEs(text) {
		if bytes.EqualFold([]byte(c.Name), []byte(name)) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return cte{}, false
	}
	best, found := cte{}, false
	for _, c := range candidates {
		if offset >= c.ScopeStart && offset <= c.ScopeEnd && (!found || c.ScopeStart > best.ScopeStart) {
			best, found = c, true
		}
	}
	if found {
		return best, true
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].NameStart <= offset {
			return candidates[i], true
		}
	}
	return candidates[0], true
}

// scanSQL tokenizes SQL from pos into a new segment. When embedded (SQL in a
// JS template literal), a bare backtick ends the segment. It returns the
// offset after the segment.
func scanSQL(text []byte, pos int, embedded bool, segments *[][]token) int {
	idx := len(*segments)
	*segments = append(*segments, nil)
	var toks []token
	defer func() { (*segments)[idx] = toks }()
	push := func(kind tokenKind, start, end, innerStart, innerEnd int) {
		toks = append(toks, token{kind, start, end, innerStart, innerEnd})
	}

	n := len(text)
	for pos < n {
		ch, next := text[pos], byteAt(text, pos+1)
		switch {
		case embedded && ch == '`':
			return pos + 1
		case embedded && ch == '\\' && next == '`':
			innerEnd, end := n, n
			if i := indexFrom(text, "\\`", pos+2); i >= 0 {
				innerEnd, end = i, i+2
			}
			push(tokQuoted, pos, end, pos+2, innerEnd)
			pos = end
		case isSpace(ch):
			pos++
		case ch == '-' && next == '-', ch == '#', ch == '/' && next == '/':
			pos = skipUntil(text, pos, "\n", embedded, false)
		case ch == '/' && next == '*':
			pos = skipUntil(text, pos+2, "*/", embedded, true)
		case ch == '$' && next == '{':
			end := scanJS(text, pos+2, segments)
			push(tokOther, pos, end, pos, end)
			pos = end
		case ch == '\'' || ch == '"':
			end := skipSQLString(text, pos, embedded)
			push(tokOther, pos, end, pos, end)
			pos = end
		case ch == '`':
			innerEnd, end := n, n
			if i := indexFrom(text, "`", pos+1); i >= 0 {
				innerEnd, end = i, i+1
			}
			push(tokQuoted, pos, end, pos+1, innerEnd)
			pos = end
		case isWordByte(ch):
			end := pos + 1
			for end < n && isWordByte(text[end]) {
				end++
			}
			push(tokWord, pos, end, pos, end)
			pos = end
		default:
			kind := tokOther
			switch ch {
			case '(':
				kind = tokOpen
			case ')':
				kind = tokClose
			case ',':
				kind = tokComma
			}
			push(kind, pos, pos+1, pos, pos+1)
			pos++
		}
	}
	return n
}

// skipUntil skips past terminator (consuming it when consume is set). In
// embedded SQL it stops before a bare backtick.
func skipUntil(text []byte, pos int, terminator string, embedded, consume bool) int {
	n := len(text)
	for pos < n {
		if bytes.HasPrefix(text[pos:], []byte(terminator)) {
			if consume {
				return pos + len(terminator)
			}
			return pos
		}
		if embedded {
			if text[pos] == '\\' && byteAt(text, pos+1) == '`' {
				pos += 2
				continue
			}
			if text[pos] == '`' {
				return pos
			}
		}
		pos++
	}
	return n
}

func skipSQLString(text []byte, pos int, embedded bool) int {
	closing := text[pos : pos+1]
	if triple := bytes.Repeat(closing, 3); bytes.HasPrefix(text[pos:], triple) {
		closing = triple
	}
	n := len(text)
	pos += len(closing)
	for pos < n {
		if text[pos] == '\\' {
			pos += 2
			continue
		}
		if embedded && text[pos] == '`' {
			return pos
		}
		if bytes.HasPrefix(text[pos:], closing) {
			return pos + len(closing)
		}
		pos++
	}
	return n
}

// scanJS skips the JavaScript inside ${...} from just after the ${, scanning
// any template literals in it as SQL. It returns the offset after the
// matching }.
func scanJS(text []byte, pos int, segments *[][]token) int {
	n := len(text)
	depth := 1
	for pos < n {
		ch, next := text[pos], byteAt(text, pos+1)
		switch {
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return pos + 1
			}
		case ch == '\'' || ch == '"':
			pos++
			for pos < n && text[pos] != ch && text[pos] != '\n' {
				if text[pos] == '\\' {
					pos += 2
				} else {
					pos++
				}
			}
		case ch == '`':
			pos = scanSQL(text, pos+1, true, segments)
			continue
		case ch == '/' && next == '/':
			pos = skipUntil(text, pos, "\n", false, false)
			continue
		case ch == '/' && next == '*':
			pos = skipUntil(text, pos+2, "*/", false, true)
			continue
		}
		pos++
	}
	return n
}

func ctesInTokens(text []byte, toks []token) []cte {
	matching := map[int]int{}
	var stack []int
	for i, t := range toks {
		switch {
		case t.kind == tokOpen:
			stack = append(stack, i)
		case t.kind == tokClose && len(stack) > 0:
			matching[stack[len(stack)-1]] = i
			stack = stack[:len(stack)-1]
		}
	}
	segmentEnd := 0
	if len(toks) > 0 {
		segmentEnd = toks[len(toks)-1].end
	}
	isWord := func(i int, word string) bool {
		return i < len(toks) && toks[i].kind == tokWord &&
			bytes.EqualFold(text[toks[i].start:toks[i].end], []byte(word))
	}
	kindAt := func(i int) (tokenKind, bool) {
		if i < len(toks) {
			return toks[i].kind, true
		}
		return 0, false
	}

	var out []cte
	for i := range toks {
		if !isWord(i, "WITH") {
			continue
		}
		j := i + 1
		if isWord(j, "RECURSIVE") {
			j++
		}
		var clause []cte
		for j < len(toks) {
			name := toks[j]
			isIdent := name.kind == tokQuoted || name.kind == tokWord && isIdentStart(text[name.start])
			if !isIdent {
				break
			}
			k := j + 1
			if kind, ok := kindAt(k); ok && kind == tokOpen {
				// An optional column list: name (col1, col2) AS (...)
				closeCols, ok := matching[k]
				if !ok {
					break
				}
				k = closeCols + 1
			}
			if kind, ok := kindAt(k + 1); !isWord(k, "AS") || !ok || kind != tokOpen {
				break
			}
			closeBody, closed := matching[k+1]
			end := segmentEnd
			if closed {
				end = toks[closeBody].end
			}
			clause = append(clause, cte{
				Name:       string(text[name.innerStart:name.innerEnd]),
				Start:      name.start,
				End:        end,
				NameStart:  name.innerStart,
				NameEnd:    name.innerEnd,
				ScopeStart: toks[i].start,
			})
			if kind, ok := kindAt(closeBody + 1); !closed || !ok || kind != tokComma {
				break
			}
			j = closeBody + 2
		}
		scopeEnd := scopeEndOf(text, toks, i, segmentEnd)
		for c := range clause {
			clause[c].ScopeEnd = scopeEnd
		}
		out = append(out, clause...)
	}
	return out
}

func scopeEndOf(text []byte, toks []token, withIndex, segmentEnd int) int {
	depth := 0
	for _, t := range toks[withIndex+1:] {
		switch {
		case t.kind == tokOpen:
			depth++
		case t.kind == tokClose:
			if depth == 0 {
				return t.start
			}
			depth--
		case depth == 0 && t.kind == tokOther && text[t.start] == ';':
			return t.start
		}
	}
	return segmentEnd
}

func byteAt(text []byte, i int) byte {
	if i < len(text) {
		return text[i]
	}
	return 0
}

func indexFrom(text []byte, sub string, from int) int {
	if from > len(text) {
		return -1
	}
	if i := bytes.Index(text[from:], []byte(sub)); i >= 0 {
		return from + i
	}
	return -1
}

// isWordByte matches SQL word characters.
func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
