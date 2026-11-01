// Package diagnose turns a project's compile errors and BigQuery dry runs
// into diagnostics on its source files. It knows nothing of editors: the
// dry run is a function it is given.
package diagnose

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// Severity of a diagnostic.
type Severity int

const (
	Error Severity = iota
	Hint
)

// Diagnostic sources: a compile's is its tool's ("dataform compile", "dbt
// compile").
const (
	SourceDryRun = "bigquery dry run"
)

// Diagnostic is a message about a range of one line of a file.
type Diagnostic struct {
	// File is an absolute path.
	File string
	// Line is 0-based; Start and End are byte columns in it.
	Line, Start, End int
	Severity         Severity
	Source, Message  string
	// Fix is what BigQuery suggests replacing the range with ("Did you mean
	// X?"); empty when it suggests nothing.
	Fix string
}

// stackLine matches the file:line a syntax error's stack starts with.
var stackLine = regexp.MustCompile(`^(\S+?):(\d+)(?::\d+)?\s*$`)

// unresolved finds the name in "Could not resolve "x"" and in a missing
// dependency's `"name":"x"`, and in dbt-core's "depends on a node named
// 'x'".
var unresolved = []*regexp.Regexp{
	regexp.MustCompile(`Could not resolve "([^"]+)"`),
	regexp.MustCompile(`"name":"([^"]+)"`),
	regexp.MustCompile(`depends on a node named '([^']+)'`),
}

// Compile returns the diagnostics for the errors of a compile by tool
// ("dataform", "dbt"), by file. Each error goes on the line its stack
// names, on the ref() it couldn't resolve, or else on the file's first
// line.
func Compile(root, tool string, errs []project.CompilationError, read func(string) ([]byte, error)) map[string][]Diagnostic {
	out := map[string][]Diagnostic{}
	for _, e := range errs {
		if e.FileName == "" {
			continue
		}
		file := filepath.Join(root, filepath.FromSlash(e.FileName))
		src, err := read(file)
		if err != nil {
			src = nil
		}
		d := Diagnostic{File: file, Severity: Error, Source: tool + " compile", Message: compileMessage(e)}
		d.Line, d.Start, d.End, _ = compileErrorRange(e, src)
		if !contains(out[file], d) {
			out[file] = append(out[file], d)
		}
	}
	return out
}

// Kinds of compile error, as xf web's errors tab groups them.
const (
	// KindRoot is a syntax error, or any error not of another kind: the
	// errors to fix first.
	KindRoot = "root"
	// KindUnresolved is a ref() that names no action.
	KindUnresolved = "unresolved"
	// KindMissing is a dependency that names no action, and dbt-core's
	// "depends on a node named".
	KindMissing = "missing"
)

// Kind says which kind of compile error e is.
func Kind(e project.CompilationError) string {
	switch {
	case strings.Contains(e.Message, "Missing dependency"), strings.Contains(e.Message, "depends on a node named"):
		return KindMissing
	case strings.Contains(e.Message, "Could not resolve"):
		return KindUnresolved
	}
	return KindRoot
}

// NeedsCoreInstall says a failed dataform compile's output asks for
// `dataform install`: the project has no @dataform/core installed, or one
// the CLI won't run (dataform 3.1 checks that their versions match).
func NeedsCoreInstall(output string) bool {
	return strings.Contains(output, "@dataform/core") && strings.Contains(output, "dataform install")
}

// NeedsInitCreds says a line of a dataform run's output asks for
// `dataform init-creds`: the project has no .df-credentials.json.
func NeedsInitCreds(line string) bool {
	return strings.Contains(line, "Missing credentials JSON file")
}

// Line is the 1-based line of root's file that e is about, read with read:
// the line a syntax error's stack names, or the line of the ref() it could
// not resolve; 0 when e names neither.
func Line(root string, e project.CompilationError, read func(string) ([]byte, error)) int {
	if e.FileName == "" {
		return 0
	}
	src, err := read(filepath.Join(root, filepath.FromSlash(e.FileName)))
	if err != nil {
		src = nil
	}
	line, _, _, ok := compileErrorRange(e, src)
	if !ok {
		return 0
	}
	return line + 1
}

// Snippet is the code and caret a syntax error's stack shows, with the
// error after them (without the path line and the stack frames); "" when
// e's stack shows no code.
func Snippet(e project.CompilationError) string {
	lines := strings.Split(e.Stack, "\n")
	if len(lines) < 2 || !stackLine.MatchString(lines[0]) {
		return ""
	}
	var context []string
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "    at ") {
			break
		}
		context = append(context, l)
	}
	return strings.TrimRight(strings.Join(context, "\n"), "\n ")
}

// compileMessage is the error's message plus, for a syntax error, its
// Snippet.
func compileMessage(e project.CompilationError) string {
	snippet := Snippet(e)
	if snippet == "" || strings.Contains(e.Message, snippet) {
		return e.Message
	}
	return e.Message + "\n" + snippet
}

// compileErrorRange is where in src e goes; ok is false when e names no
// place, and the range is then src's first line.
func compileErrorRange(e project.CompilationError, src []byte) (line, start, end int, ok bool) {
	first, _, _ := strings.Cut(e.Stack, "\n")
	if m := stackLine.FindStringSubmatch(first); m != nil && strings.HasSuffix(project.SlashPath(m[1]), e.FileName) {
		n, _ := strconv.Atoi(m[2])
		code := ""
		if lines := strings.SplitN(e.Stack, "\n", 3); len(lines) > 1 {
			code = lines[1]
		}
		n = codeLine(src, n-1, code)
		return n, 0, len(lineText(src, n)), true
	}
	for _, re := range unresolved {
		m := re.FindStringSubmatch(e.Message)
		if m == nil {
			continue
		}
		if i := findQuoted(src, m[1]); i >= 0 {
			l, c := lineCol(src, i)
			return l, c, c + len(m[1]), true
		}
	}
	return 0, 0, len(lineText(src, 0)), false
}

// codeLine is the line of src nearest to line n that reads code, the line
// a syntax error's stack quotes, or n when none does or n does. The
// stack's line is off for a .sqlx file, whose config block Dataform
// compiles inside more JavaScript.
func codeLine(src []byte, n int, code string) int {
	code = strings.TrimSpace(code)
	if code == "" || strings.TrimSpace(string(lineText(src, n))) == code {
		return n
	}
	best := -1
	for i, l := range strings.Split(string(src), "\n") {
		if strings.TrimSpace(l) == code && (best < 0 || abs(i-n) < abs(best-n)) {
			best = i
		}
	}
	if best < 0 {
		return n
	}
	return best
}

func abs(n int) int { return max(n, -n) }

// findQuoted returns the offset of name inside a ref() call, or else of the
// first "name" or 'name' in src; -1 if there is none.
func findQuoted(src []byte, name string) int {
	q := regexp.QuoteMeta(name)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\bref\s*\([^)]*?["'` + "`" + `](` + q + `)["'` + "`" + `]`),
		regexp.MustCompile(`["'` + "`" + `](` + q + `)["'` + "`" + `]`),
	} {
		if m := re.FindSubmatchIndex(src); m != nil {
			return m[2]
		}
	}
	return -1
}

func contains(ds []Diagnostic, d Diagnostic) bool {
	for _, x := range ds {
		if x == d {
			return true
		}
	}
	return false
}

// lineCol converts a byte offset to a 0-based line and byte column.
func lineCol(src []byte, off int) (int, int) {
	off = min(max(off, 0), len(src))
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	return bytes.Count(src[:off], []byte("\n")), off - start
}

// lineText returns line n of src without its line ending.
func lineText(src []byte, n int) []byte {
	for ; n > 0; n-- {
		i := bytes.IndexByte(src, '\n')
		if i < 0 {
			return nil
		}
		src = src[i+1:]
	}
	if i := bytes.IndexByte(src, '\n'); i >= 0 {
		src = src[:i]
	}
	return bytes.TrimSuffix(src, []byte("\r"))
}
