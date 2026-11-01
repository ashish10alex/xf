// Package format formats a Dataform file: its SQL with sqlfluff, and its
// config block itself (ADR 0024). It owns nothing of the page or the
// language server: both hand it text and take text back.
//
// A sqlx file holds SQL in its main query and in its pre_operations and
// post_operations bodies; each such part is formatted where it sits. The
// config block's body is formatted by Config. The rest of the file (js,
// comments between blocks) is returned byte for byte. Inside a SQL part
// each ${ … } is swapped for an identifier while sqlfluff runs, since it
// can't read one.
package format

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/navigate"
)

// ErrNotFound is returned when there is no sqlfluff to run: FindBin
// found none, or Options.Bin is "".
var ErrNotFound = errors.New("sqlfluff not found: uv tool install sqlfluff")

// ParseError is a part sqlfluff could not read, at a position in the text
// that was given (1-based).
type ParseError struct {
	Line, Col int
	Msg       string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Col, e.Msg)
}

// Runner runs sqlfluff: its output, and its exit code (err is for a
// failure to run it at all).
type Runner func(ctx context.Context, bin string, args []string, dir, stdin string) (stdout, stderr string, code int, err error)

// Options say how to run sqlfluff.
type Options struct {
	// Bin is the sqlfluff to run; FindBin finds it.
	Bin string
	// Dir is where sqlfluff runs, so that it finds the configs of the
	// file's folder and the ones above it: the folder of the file.
	Dir string
	// Root is the project's root, where the search for a config of the
	// project's own stops.
	Root string
	// Config is a config file to use instead of the project's, or "".
	Config string
	// Run runs sqlfluff; nil runs the binary.
	Run Runner
}

// timeout bounds one run of sqlfluff.
const timeout = 30 * time.Second

// parallel is how many parts are formatted at once.
const parallel = 4

//go:embed default.sqlfluff
var defaultConfig []byte

// SQLX formats a sqlx file in place: its config block (Config, in Go)
// and its SQL parts (sqlfluff). It returns src unchanged if both already
// are formatted. The two are formatted apart: when one fails and the
// other changed the text, that text comes back with a *PartialError;
// when nothing changed, the error comes back alone, and no text.
func SQLX(ctx context.Context, src []byte, o Options) ([]byte, error) {
	text, cerr := Config(src)
	if cerr != nil {
		text = src
	}
	out, serr := formatText(ctx, text, o, func(text []byte) ([]navigate.SQLPart, bool) { return navigate.SQLParts(text) })
	switch {
	case cerr == nil && serr == nil:
		return out, nil
	case cerr != nil && serr != nil:
		return nil, fmt.Errorf("%w; and %v", serr, cerr)
	case serr != nil:
		if bytes.Equal(text, src) {
			return nil, serr
		}
		return text, &PartialError{Done: "the config block", Skipped: "the SQL", Err: serr}
	default:
		if bytes.Equal(out, src) {
			return nil, cerr
		}
		return out, &PartialError{Done: "the SQL", Skipped: "the config block", Err: cerr}
	}
}

// PartialError comes with the text of a sqlx file one part of which was
// formatted and the other not.
type PartialError struct {
	Done, Skipped string
	Err           error
}

func (e *PartialError) Error() string {
	return "formatted " + e.Done + ", not " + e.Skipped + ": " + e.Err.Error()
}

func (e *PartialError) Unwrap() error { return e.Err }

// Query formats text that is all SQL: a Scratch query, a plain .sql file.
func Query(ctx context.Context, src []byte, o Options) ([]byte, error) {
	return formatText(ctx, src, o, func(text []byte) ([]navigate.SQLPart, bool) {
		p, ok := navigate.QueryPart(text)
		return []navigate.SQLPart{p}, ok
	})
}

func formatText(ctx context.Context, src []byte, o Options, partsOf func([]byte) ([]navigate.SQLPart, bool)) ([]byte, error) {
	crlf := bytes.Contains(src, []byte("\r\n"))
	text := src
	if crlf {
		text = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	}
	parts, ok := partsOf(text)
	if !ok {
		return nil, errors.New("a block or a ${ is never closed, so where the SQL is can't be told")
	}
	s := &session{o: o, src: text}
	defer s.close()
	if err := s.prepare(); err != nil {
		return nil, err
	}

	done := make([]string, len(parts))
	errs := make([]error, len(parts))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, p := range parts {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			done[i], errs[i] = s.part(ctx, p)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	last := 0
	for i, p := range parts {
		out.Write(text[last:p.Start])
		out.WriteString(done[i])
		last = p.End
	}
	out.Write(text[last:])
	res := out.Bytes()
	if crlf {
		res = bytes.ReplaceAll(res, []byte("\n"), []byte("\r\n"))
	}
	return res, nil
}

// session is one format: the text and the config it runs with.
type session struct {
	o    Options
	src  []byte
	args []string // the config's flags
	tmp  string   // the default config's file, if used
}

func (s *session) close() {
	if s.tmp != "" {
		_ = os.Remove(s.tmp)
	}
}

// prepare settles which config sqlfluff runs with: the one named, else
// the project's own, else xf's default, written to a temporary file. A
// config named or xf's comes with --ignore-local-config, since --config
// alone is laid over the project's own rather than used in its place.
func (s *session) prepare() error {
	switch {
	case s.o.Config != "":
		s.args = []string{"--ignore-local-config", "--config", s.o.Config}
	case hasProjectConfig(s.o.Dir, s.o.Root):
	default:
		f, err := os.CreateTemp("", "xf-sqlfluff-*.cfg")
		if err != nil {
			return err
		}
		s.tmp = f.Name()
		_, err = f.Write(defaultConfig)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		s.args = []string{"--ignore-local-config", "--config", s.tmp}
	}
	return nil
}

// part formats one part of the text and returns the text that replaces it.
func (s *session) part(ctx context.Context, p navigate.SQLPart) (string, error) {
	seg := string(s.src[p.Start:p.End])
	core := strings.Trim(seg, " \t\r\n")
	if core == "" {
		return seg, nil
	}
	lead := seg[:strings.Index(seg, core)]
	trail := seg[len(lead)+len(core):]
	coreStart := p.Start + len(lead)
	spans := make([][2]int, len(p.Islands))
	for i, is := range p.Islands {
		spans[i] = [2]int{is[0] - coreStart, is[1] - coreStart}
	}

	// Each ${ } becomes an identifier, which parses wherever a name or
	// an expression can stand. If the part then won't parse and a ${ }
	// has a line to itself (a clause: ${when(incremental(), `WHERE …`)}),
	// those become comments, which parse anywhere.
	var h hidden
	var out string
	for _, comments := range []bool{false, true} {
		h = hide(core, spans, comments)
		if comments && !h.anyAlone {
			break
		}
		o, perr, err := s.format(ctx, h.text, comments || !h.anyAlone)
		if err != nil {
			return "", err
		}
		if perr == nil {
			out = o
			break
		}
		if comments || !h.anyAlone {
			off := offsetAt(h.text, perr.Line, perr.Col)
			perr.Line, perr.Col = lineCol(s.src, coreStart+h.original(off))
			perr.Msg = h.loosely(perr.Msg)
			return "", perr
		}
	}
	out = strings.Trim(out, " \t\r\n")
	if out == "" {
		return "", errors.New("sqlfluff returned nothing for a part that has SQL")
	}
	if i := strings.LastIndexByte(lead, '\n'); i >= 0 {
		out = indentLines(out, lead[i+1:])
	}
	restored, ok := h.restore(out)
	if !ok {
		return "", errors.New("sqlfluff changed a ${ … } it was given, so nothing was formatted")
	}
	return lead + restored + trail, nil
}

// format runs sqlfluff over one part's text. A part it can't read comes
// back as a ParseError, with its position in text when locate asks for it
// (that takes a second run of sqlfluff).
func (s *session) format(ctx context.Context, text string, locate bool) (string, *ParseError, error) {
	base := []string{"--dialect", "bigquery", "--templater", "raw", "--nocolor"}
	args := append(append([]string{"format", "-"}, base...), s.args...)
	stdout, stderr, code, err := s.run(ctx, args, text)
	if err != nil {
		return "", nil, err
	}
	if code != 0 && strings.Contains(stderr, "parsing errors") {
		if !locate {
			return "", &ParseError{Line: 1, Col: 1}, nil
		}
		return "", s.locate(ctx, append(base, s.args...), text), nil
	}
	// 1 is "some violations stay"; a format leaves those, and what it
	// printed is formatted. Anything above is sqlfluff failing to run.
	if code > 1 || (code == 1 && stdout == "") {
		return "", nil, fmt.Errorf("sqlfluff: %s", firstLine(stderr, stdout))
	}
	return stdout, nil, nil
}

var positionPrefix = regexp.MustCompile(`^Line \d+, Position \d+: `)

// locate asks sqlfluff where a part it can't read goes wrong.
func (s *session) locate(ctx context.Context, flags []string, text string) *ParseError {
	args := append([]string{"lint", "-", "--format", "json"}, flags...)
	stdout, _, _, err := s.run(ctx, args, text)
	var files []struct {
		Violations []struct {
			Code        string `json:"code"`
			Description string `json:"description"`
			Line        int    `json:"start_line_no"`
			Pos         int    `json:"start_line_pos"`
		} `json:"violations"`
	}
	if err == nil && json.Unmarshal([]byte(stdout), &files) == nil {
		for _, f := range files {
			for _, v := range f.Violations {
				if v.Code == "PRS" || v.Code == "TMP" {
					return &ParseError{Line: v.Line, Col: v.Pos, Msg: positionPrefix.ReplaceAllString(v.Description, "")}
				}
			}
		}
	}
	return &ParseError{Line: 1, Col: 1, Msg: "sqlfluff could not read the SQL"}
}

func (s *session) run(ctx context.Context, args []string, stdin string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s.o.Bin == "" {
		return "", "", 0, ErrNotFound
	}
	run := s.o.Run
	if run == nil {
		run = execRunner
	}
	stdout, stderr, code, err := run(ctx, s.o.Bin, args, s.o.Dir, stdin)
	if err != nil && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)) {
		return "", "", 0, ErrNotFound
	}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", "", 0, fmt.Errorf("sqlfluff took longer than %s", timeout)
	}
	return stdout, stderr, code, err
}

func execRunner(ctx context.Context, bin string, args []string, dir, stdin string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	return out.String(), errb.String(), 0, err
}

// firstLine is the first line with text in the arguments.
func firstLine(ss ...string) string {
	for _, s := range ss {
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
	}
	return "it failed"
}

// loosely is msg with the names standing for ${ … } (which sqlfluff may
// cut short) called that.
func (h hidden) loosely(msg string) string {
	return regexp.MustCompile("(?i)(-- )?"+regexp.QuoteMeta(h.prefix)+`\d+_*(\.\.\.)?`).ReplaceAllString(msg, "${…}")
}

// offsetAt is the byte offset of a 1-based line and column (in
// characters) in text.
func offsetAt(text string, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(text[off:], '\n')
		if i < 0 {
			return len(text)
		}
		off += i + 1
	}
	for c := 1; c < col && off < len(text) && text[off] != '\n'; c++ {
		_, n := decodeRune(text[off:])
		off += n
	}
	return off
}

// lineCol is the 1-based line and column (in characters) of a byte
// offset in src.
func lineCol(src []byte, off int) (int, int) {
	off = min(off, len(src))
	line := 1 + bytes.Count(src[:off], []byte("\n"))
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	return line, 1 + len([]rune(string(src[start:off])))
}

// hasProjectConfig says whether the folder of the file, or one above it
// up to the project's root, holds a config sqlfluff reads.
func hasProjectConfig(dir, root string) bool { return FindConfig(dir, root) != "" }

// FindConfig is the config file sqlfluff reads for a file in dir: the
// nearest of a .sqlfluff, a tox.ini or setup.cfg with a [sqlfluff] section
// and a pyproject.toml with [tool.sqlfluff], in dir or a folder above it up
// to the project's root. It is "" when there is none.
func FindConfig(dir, root string) string {
	for d := dir; d != ""; {
		if p := filepath.Join(d, ".sqlfluff"); fileExists(p) {
			return p
		}
		for _, c := range [][2]string{{"tox.ini", "[sqlfluff"}, {"setup.cfg", "[sqlfluff"}, {"pyproject.toml", "[tool.sqlfluff"}} {
			if p := filepath.Join(d, c[0]); fileHas(p, c[1]) {
				return p
			}
		}
		parent := filepath.Dir(d)
		if d == root || parent == d || root == "" {
			break
		}
		d = parent
	}
	return ""
}

func fileHas(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(needle))
}

// FindBin finds the sqlfluff to run: configured (the sqlfluffBin
// setting), else the project's virtualenv's, else the one on PATH.
func FindBin(configured, root string, lookPath func(string) (string, error)) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) && root != "" && strings.ContainsAny(configured, `/\`) {
			configured = filepath.Join(root, configured)
		}
		return configured, nil
	}
	bin, sub := "sqlfluff", "bin"
	if runtime.GOOS == "windows" {
		bin, sub = "sqlfluff.exe", "Scripts"
	}
	for _, venv := range []string{".venv", "venv"} {
		if p := filepath.Join(root, venv, sub, bin); root != "" && fileExists(p) {
			return p, nil
		}
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if p, err := lookPath("sqlfluff"); err == nil {
		return p, nil
	}
	return "", ErrNotFound
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
