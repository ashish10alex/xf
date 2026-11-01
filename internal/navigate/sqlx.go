package navigate

import "bytes"

// span is a byte range [start, end) of a source file.
type span struct{ start, end int }

// jsSpans returns the parts of a sqlx file that hold JavaScript: the bodies of
// its config { … } and js { … } blocks and the inside of every ${ … }. The
// rest is SQL. Unterminated blocks run to the end of the file, which is what
// a half-typed buffer needs.
func jsSpans(src []byte) []span {
	var out []span
	for i := 0; i < len(src); {
		switch {
		case bytes.HasPrefix(src[i:], []byte("--")):
			i = lineEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i = commentEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("${")):
			end := matchBrace(src, i+2)
			out = append(out, span{i + 2, end})
			i = end + 1
		default:
			if body, ok := jsBlockBody(src, i); ok {
				end := matchBrace(src, body)
				out = append(out, span{body, end})
				i = end + 1
				continue
			}
			i++
		}
	}
	return out
}

// jsBlockBody reports whether a config or js block starts at i and, if so,
// the offset just after its opening brace.
func jsBlockBody(src []byte, i int) (int, bool) {
	if i > 0 && isIdentByte(src[i-1]) {
		return 0, false
	}
	for _, kw := range []string{"config", "js"} {
		if !bytes.HasPrefix(src[i:], []byte(kw)) {
			continue
		}
		j := i + len(kw)
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if j < len(src) && src[j] == '{' {
			return j + 1, true
		}
	}
	return 0, false
}

// matchBrace returns the offset of the } that closes the { just before
// start, skipping JavaScript strings, template literals and comments. It
// returns len(src) when the brace is never closed.
func matchBrace(src []byte, start int) int {
	depth := 1
	for i := start; i < len(src); {
		switch c := src[i]; {
		case c == '"' || c == '\'':
			i = stringEnd(src, i)
		case c == '`':
			i = templateEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("//")):
			i = lineEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i = commentEnd(src, i)
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
			i++
		default:
			i++
		}
	}
	return len(src)
}

// stringEnd returns the offset just past the quoted string starting at i.
func stringEnd(src []byte, i int) int {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q, '\n':
			return j + 1
		}
	}
	return len(src)
}

// templateEnd returns the offset just past the template literal starting at
// i, stepping over its ${ … } substitutions.
func templateEnd(src []byte, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch {
		case src[j] == '\\':
			j++
		case src[j] == '`':
			return j + 1
		case bytes.HasPrefix(src[j:], []byte("${")):
			j = matchBrace(src, j+2)
		}
	}
	return len(src)
}

func lineEnd(src []byte, i int) int {
	if n := bytes.IndexByte(src[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(src)
}

func commentEnd(src []byte, i int) int {
	if n := bytes.Index(src[i+2:], []byte("*/")); n >= 0 {
		return i + 2 + n + 2
	}
	return len(src)
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// JSSpans returns where a sqlx file holds JavaScript, as [start, end)
// offsets: the bodies of its config { … } and js { … } blocks and the
// inside of every ${ … }.
func JSSpans(src []byte) [][2]int {
	var out [][2]int
	for _, s := range jsSpans(src) {
		out = append(out, [2]int{s.start, s.end})
	}
	return out
}
