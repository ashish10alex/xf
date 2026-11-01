package navigate

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

// Dataform makes each JavaScript file at the top of includes/ a global
// named after the file: includes/parameters.js is `parameters` in every
// sqlx file and definitions script, and `parameters.X` is what that file
// exports as X.

// Include is a name an includes file declares, as `file.Name` reaches it.
type Include struct {
	// Start and End bound `file.Name` in the source asked about.
	Start, End int
	// Location is where Name is declared in the includes file.
	Location
	// Code is the declaration as written, cut to a few lines: a value's
	// whole statement, a function's signature.
	Code string
	// Doc is the comment just above the declaration, without its markers.
	Doc string
}

// includeLines is how many lines of a declaration Code keeps.
const includeLines = 8

// IncludeAt returns the includes name at offset: the cursor on Name in
// `file.Name`, where includes/file.js declares Name. In a sqlx file only
// its JavaScript counts. It reads one file, the includes file named.
func IncludeAt(p Project, file string, src []byte, offset int) (Include, bool) {
	if p.Dbt || offset < 0 || offset > len(src) {
		return Include{}, false
	}
	start, end := offset, offset
	for start > 0 && isIdentByte(src[start-1]) {
		start--
	}
	for end < len(src) && isIdentByte(src[end]) {
		end++
	}
	if start == end || start < 2 || src[start-1] != '.' {
		return Include{}, false
	}
	objStart := start - 1
	for objStart > 0 && isIdentByte(src[objStart-1]) {
		objStart--
	}
	if objStart == start-1 || objStart > 0 && src[objStart-1] == '.' {
		return Include{}, false
	}
	if !isJSFile(file) && !inSpans(jsSpans(src), start) {
		return Include{}, false
	}
	path := filepath.Join(p.Root, "includes", string(src[objStart:start-1])+".js")
	inc, err := p.read(path)
	if err != nil {
		return Include{}, false
	}
	name := string(src[start:end])
	at, nameAt, kind := declarationOf(inc, name)
	if at < 0 {
		return Include{}, false
	}
	return Include{
		Start:    objStart,
		End:      end,
		Location: locationAt(path, inc, nameAt),
		Code:     declarationCode(inc, at, kind),
		Doc:      docAbove(inc, at),
	}, true
}

// What a declaration is, which says where its Code ends.
const (
	declValue = iota // const X = …, exports.X = …, X: … in module.exports
	declBody         // function X(…) { … }, class X { … }: the part before {
)

// declarations are the ways a file declares name at the top level, most
// telling first, each with where the name is in the match.
func declarations(name string) []struct {
	re   *regexp.Regexp
	kind int
} {
	n := regexp.QuoteMeta(name)
	return []struct {
		re   *regexp.Regexp
		kind int
	}{
		{regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?(?:const|let|var)\s+(` + n + `)\s*=`), declValue},
		{regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?(?:async\s+)?function\s*\*?\s*(` + n + `)\s*\(`), declBody},
		{regexp.MustCompile(`(?m)^[ \t]*(?:export\s+)?class\s+(` + n + `)\b`), declBody},
		{regexp.MustCompile(`(?m)^[ \t]*(?:module\.)?exports\.(` + n + `)\s*=`), declValue},
	}
}

// moduleExports matches the start of a module.exports = { … } object.
var moduleExports = regexp.MustCompile(`(?m)^[ \t]*module\.exports\s*=\s*\{`)

// declarationOf returns where src declares name at its top level: the
// declaration's start and the name's offset, -1 when it doesn't. A
// shorthand export ({ X }) leads to X's own declaration.
func declarationOf(src []byte, name string) (at, nameAt, kind int) {
	for _, d := range declarations(name) {
		for _, m := range d.re.FindAllSubmatchIndex(src, -1) {
			if depthAt(src, m[0]) == 0 {
				return lineStart(src, m[2]), m[2], d.kind
			}
		}
	}
	for _, m := range moduleExports.FindAllIndex(src, -1) {
		if depthAt(src, m[0]) != 0 {
			continue
		}
		body := m[1]
		if k := objectKey(src, body, matchBrace(src, body), name); k >= 0 {
			return lineStart(src, k), k, declValue
		}
	}
	return -1, -1, 0
}

// objectKey returns where `name:` is a key of the object whose body runs
// from start to end, not of an object inside it; -1 when it isn't.
func objectKey(src []byte, start, end int, name string) int {
	depth := 0
	for i := start; i < end; {
		switch c := src[i]; {
		case c == '"' || c == '\'':
			i = stringEnd(src, i)
		case c == '`':
			i = templateEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("//")):
			i = lineEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i = commentEnd(src, i)
		case c == '{' || c == '(' || c == '[':
			depth++
			i++
		case c == '}' || c == ')' || c == ']':
			depth--
			i++
		case isIdentByte(c):
			j := i
			for j < end && isIdentByte(src[j]) {
				j++
			}
			if depth == 0 && string(src[i:j]) == name {
				k := j
				for k < end && isSpace(src[k]) {
					k++
				}
				if k < end && src[k] == ':' {
					return i
				}
			}
			i = j
		default:
			i++
		}
	}
	return -1
}

// depthAt is how many brackets are open at offset, strings and comments
// skipped.
func depthAt(src []byte, offset int) int {
	depth := 0
	for i := 0; i < offset; {
		switch c := src[i]; {
		case c == '"' || c == '\'':
			i = stringEnd(src, i)
		case c == '`':
			i = templateEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("//")):
			i = lineEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i = commentEnd(src, i)
		case c == '{' || c == '(' || c == '[':
			depth++
			i++
		case c == '}' || c == ')' || c == ']':
			depth = max(0, depth-1)
			i++
		default:
			i++
		}
	}
	return depth
}

// declarationCode is the declaration starting at at, as written: a value's
// statement to its end, a function's or class's head without its body;
// at most includeLines lines, with a … line when cut.
func declarationCode(src []byte, at, kind int) string {
	end := statementEnd(src, at, kind)
	lines := strings.Split(strings.TrimRight(string(src[at:end]), " \t\r\n"), "\n")
	cut := len(lines) > includeLines
	if cut {
		lines = lines[:includeLines]
	}
	indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " \t"))
	for i, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if n := len(l) - len(strings.TrimLeft(l, " \t")); n >= indent {
			l = l[indent:]
		}
		lines[i] = l
	}
	if cut {
		lines = append(lines, "…")
	}
	return strings.Join(lines, "\n")
}

// statementEnd is where the declaration starting at at ends: a ; or , at
// its own level, a line break after a complete value, or, for a function
// (or a value that is one, as an arrow), the { of its body.
func statementEnd(src []byte, at, kind int) int {
	depth := 0
	seen := false // past the = or : that starts a value
	for i := at; i < len(src); {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			i = stringEnd(src, i)
		case c == '`':
			i = templateEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte("//")):
			if n := bytes.IndexByte(src[i:], '\n'); n >= 0 {
				i += n
			} else {
				i = len(src)
			}
		case bytes.HasPrefix(src[i:], []byte("/*")):
			i = commentEnd(src, i)
		case c == '{' && depth == 0 && (kind == declBody || opensBody(src, at, i)):
			return i
		case c == '{' || c == '(' || c == '[':
			depth++
			i++
		case c == '}' || c == ')' || c == ']':
			if depth == 0 {
				return i // the end of the object a key is in
			}
			depth--
			i++
		case depth == 0 && kind == declValue && (c == ';' || c == ',') && seen:
			return i
		case depth == 0 && c == '\n' && seen && kind == declValue && valueDone(src, at, i):
			return i
		default:
			if c == '=' || c == ':' {
				seen = true
			}
			i++
		}
	}
	return len(src)
}

// opensBody says whether the { at i opens a function's body: it follows
// => or the ) of a parameter list.
func opensBody(src []byte, from, i int) bool {
	before := bytes.TrimRight(src[from:i], " \t\r\n")
	return bytes.HasSuffix(before, []byte("=>")) || bytes.HasSuffix(before, []byte(")"))
}

// valueDone says whether the line break at i ends a value: the line does
// not end in an operator and the next does not start with one.
func valueDone(src []byte, from, i int) bool {
	before := bytes.TrimRight(src[from:i], " \t\r")
	if len(before) == 0 || bytes.ContainsRune([]byte("=+-*/%?:&|,(<>!"), rune(before[len(before)-1])) {
		return false
	}
	after := bytes.TrimLeft(src[i:], " \t\r\n")
	return len(after) == 0 || !bytes.ContainsRune([]byte(".+-*/%?:&|"), rune(after[0]))
}

// docAbove is the comment on the lines just above the line at at: a run
// of // lines or a /* */ block, its markers taken off.
func docAbove(src []byte, at int) string {
	lines := strings.Split(string(src[:at]), "\n")
	lines = lines[:len(lines)-1] // the declaration's own line, up to at
	var doc []string
	i := len(lines) - 1
	if i >= 0 && strings.HasSuffix(strings.TrimSpace(lines[i]), "*/") {
		for ; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			done := strings.HasPrefix(l, "/*")
			l = strings.TrimSuffix(l, "*/")
			l = strings.TrimLeft(l, "/*")
			doc = append([]string{strings.TrimSpace(l)}, doc...)
			if done {
				break
			}
		}
		if i < 0 {
			return ""
		}
	} else {
		for ; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(l, "//") {
				break
			}
			doc = append([]string{strings.TrimSpace(strings.TrimLeft(l, "/"))}, doc...)
		}
	}
	return strings.TrimSpace(strings.Join(doc, "\n"))
}

func lineStart(src []byte, offset int) int {
	return bytes.LastIndexByte(src[:offset], '\n') + 1
}
