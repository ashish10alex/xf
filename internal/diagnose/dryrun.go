package diagnose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// DryRunFunc asks BigQuery to plan one of a's queries without running it.
type DryRunFunc func(ctx context.Context, a *project.Action, query string) (*bigquery.DryRun, error)

// maxParallelDryRuns bounds the dry runs one file has in flight.
const maxParallelDryRuns = 4

var (
	errorAt      = regexp.MustCompile(`\s*at \[(\d+):(\d+)\]\s*$`)
	didYouMean   = regexp.MustCompile(`Did you mean (\w+)\?`)
	notFoundName = regexp.MustCompile(`Not found: (?:Table|View|Dataset|Routine|Function) ([\w.:-]+)`)
	// inlinedCTE starts the CTE dbt makes of an ephemeral model.
	inlinedCTE = regexp.MustCompile(`__dbt__cte__(\w+)\s+as\s*\(`)
)

// part is one statement of a dry-run script, with a way back to the source.
type part struct {
	text string
	// source maps an offset in text to one in the file; nil when there is
	// no way back (a JavaScript file), and errors go on the anchor.
	source func(off int) (int, bool)
}

// script is what one dry run sends: its parts joined by project.Script, so
// it is the same text the TUI dry-runs for the action.
type script struct {
	label  string // "full" or "incremental" for an incremental table's two queries
	action *project.Action
	parts  []part
	cost   bool                 // whether its bytes are reported
	anchor int                  // source offset for errors that can't be placed
	text   string               // built by build
	placed []project.ScriptPart // where each non-empty part is in text
}

func (s *script) build() {
	texts := make([]string, len(s.parts))
	for i, p := range s.parts {
		texts[i] = p.text
	}
	s.text, s.placed = project.Script(texts)
}

// sourceOffset traces an offset in the script back to the file.
func (s *script) sourceOffset(off int) (int, bool) {
	for i := len(s.placed) - 1; i >= 0; i-- {
		p := s.placed[i]
		if off >= p.Start {
			pt := s.parts[p.Stmt]
			if pt.source == nil {
				return 0, false
			}
			return pt.source(off - p.Origin)
		}
	}
	return 0, false
}

// DryRunFile dry-runs every action defined in file (src is its text on
// disk, which is what was compiled) and returns its errors, placed on the
// source they came from, plus a hint with each action's bytes and cost.
// Failures that aren't the query's fault (no login, no network) become one
// hint. It returns nil when ctx is cancelled (not when it times out).
func DryRunFile(ctx context.Context, p navigate.Project, file string, src []byte, run DryRunFunc) []Diagnostic {
	rel, err := filepath.Rel(p.Root, file)
	if err != nil || p.Graph == nil {
		return nil
	}
	var scripts []*script
	var jinja *navigate.Region // a dbt file's, made once
	for _, a := range p.Graph.ActionsInFile(filepath.ToSlash(rel)) {
		if !p.Dbt {
			scripts = append(scripts, scriptsFor(p, a, file, src)...)
			continue
		}
		if jinja == nil {
			r := navigate.DbtRegion(src)
			jinja = &r
		}
		if s := dbtScript(a, src, *jinja); s != nil {
			scripts = append(scripts, s)
		}
	}
	if len(scripts) == 0 {
		return nil
	}

	results := make([]dryRunResult, len(scripts))
	sem := make(chan struct{}, maxParallelDryRuns)
	var wg sync.WaitGroup
	for i, s := range scripts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := run(ctx, s.action, s.text)
			results[i] = dryRunResult{r, err}
		}()
	}
	wg.Wait()
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil // superseded; a deadline still reports that it timed out
	}

	var out []Diagnostic
	add := func(d Diagnostic) {
		if !contains(out, d) {
			out = append(out, d)
		}
	}
	costs := map[int][]string{} // by anchor
	var anchors []int
	for i, s := range scripts {
		r := results[i]
		switch {
		case r.err == nil:
			if s.cost {
				if _, seen := costs[s.anchor]; !seen {
					anchors = append(anchors, s.anchor)
				}
				costs[s.anchor] = append(costs[s.anchor], costText(s.label, r.dryRun))
			}
		case isQueryError(r.err):
			d := queryErrorDiagnostic(file, src, s, r.err)
			if s.label == "incremental" {
				if contains(out, d) {
					continue // the full query fails the same way
				}
				d.Message += " (incremental query)"
			}
			add(d)
		default:
			add(hintAt(file, src, 0, "Dry run didn't run: "+reason(r.err)))
		}
	}
	for _, a := range anchors {
		add(hintAt(file, src, a, "Dry run: "+strings.Join(costs[a], " · ")))
	}
	return out
}

type dryRunResult struct {
	dryRun *bigquery.DryRun
	err    error
}

// scriptsFor builds what to dry-run for an action, as the VS Code extension
// does: the query after its pre_operations (so variables they declare
// resolve), both variants of an incremental table, pre_operations with
// post_operations, and an assertion's or operation's statements.
func scriptsFor(p navigate.Project, a *project.Action, file string, src []byte) []*script {
	isSQLX := strings.HasSuffix(file, ".sqlx")
	anchor := 0
	if !isSQLX {
		loc := navigate.ActionLocation(p, a)
		anchor = offsetOf(src, loc.Line, loc.Col)
	}
	var regions navigate.SQLXRegions
	if isSQLX {
		regions = navigate.Regions(src)
	}
	// region returns a part for compiled SQL that came from r (nil for JS).
	region := func(r *navigate.Region, compiled string) part {
		pt := part{text: compiled}
		if r != nil {
			m := r.Align(compiled)
			pt.source = m.Source
		}
		return pt
	}
	ops := func(compiled []string, rs []navigate.Region) []part {
		var out []part
		for i, c := range compiled {
			var r *navigate.Region
			if isSQLX && len(compiled) == len(rs) {
				r = &rs[i]
			}
			out = append(out, region(r, c))
		}
		return out
	}
	var main *navigate.Region
	if isSQLX {
		main = &regions.Main
	}

	var out []*script
	add := func(label string, cost bool, parts ...part) {
		s := &script{label: label, action: a, parts: parts, cost: cost, anchor: anchor}
		if s.build(); s.text != "" {
			out = append(out, s)
		}
	}
	pre := ops(a.PreOps, regions.PreOps)
	switch a.Kind {
	case project.KindTable, project.KindView:
		add("", true, append(pre, region(main, a.Query))...)
	case project.KindIncremental:
		add("full", true, append(pre, region(main, a.Query))...)
		add("incremental", true, append(ops(a.IncrementalPreOps, regions.PreOps), region(main, a.IncrementalQuery))...)
	case project.KindAssertion:
		if a.ParentAction != nil {
			// Generated from the table's config: errors go on its assertions key.
			s := &script{action: a, parts: []part{{text: a.Query}}, anchor: assertionsKey(src, regions)}
			s.build()
			out = append(out, s)
		} else {
			add("", true, region(main, a.Query))
		}
	case project.KindOperation:
		joined := strings.Join(a.Queries, "---")
		var m *navigate.SourceMap
		if main != nil {
			m = main.Align(joined)
		}
		var parts []part
		at := 0
		for _, q := range a.Queries {
			pt := part{text: q}
			if m != nil {
				offset := at
				pt.source = func(off int) (int, bool) { return m.Source(offset + off) }
			}
			parts = append(parts, pt)
			at += len(q) + len("---")
		}
		add("", true, parts...)
	}
	if len(a.PostOps) > 0 {
		add("", false, append(pre, ops(a.PostOps, regions.PostOps)...)...)
	}
	return out
}

// dbtScript is what to dry-run for a dbt action: its compiled query, which
// is all project.DryRunQueries has for one (hooks are uncompiled Jinja),
// traced back to the file through its Jinja (see navigate.DbtRegion). It is
// nil for an action with nothing to dry-run, and for one whose SQL isn't
// the current compile's, which may not be from the file as it is.
//
// dbt puts the CTE of each ephemeral model the query uses ahead of the
// model's own SQL; an error in one goes on the ref() that brought it in.
func dbtScript(a *project.Action, src []byte, jinja navigate.Region) *script {
	if a.SQL != project.SQLCompiled || len(a.DryRunQueries()) == 0 {
		return nil
	}
	m := jinja.AlignSkipping(a.Query)
	source := func(off int) (int, bool) {
		if first, ok := m.First(); ok && off < first {
			if c := inlinedCTE.FindAllStringSubmatch(a.Query[:min(off, len(a.Query))], -1); c != nil {
				if tag := navigate.DbtRefTag(src, c[len(c)-1][1]); tag >= 0 {
					return tag, true
				}
			}
		}
		return m.Source(off)
	}
	s := &script{action: a, cost: true, parts: []part{{text: a.Query, source: source}}}
	if s.build(); s.text == "" {
		return nil
	}
	return s
}

// assertionsKey is the offset of the assertions key in a sqlx config block,
// or 0.
func assertionsKey(src []byte, r navigate.SQLXRegions) int {
	if r.ConfigStart < 0 {
		return 0
	}
	if i := bytes.Index(src[r.ConfigStart:r.ConfigEnd], []byte("assertions")); i >= 0 {
		return r.ConfigStart + i
	}
	return 0
}

// isQueryError reports whether BigQuery rejected the query itself.
func isQueryError(err error) bool {
	var api *bigquery.APIError
	return errors.As(err, &api) && (api.Status == http.StatusBadRequest || api.Status == http.StatusNotFound)
}

func queryErrorDiagnostic(file string, src []byte, s *script, err error) Diagnostic {
	var api *bigquery.APIError
	errors.As(err, &api)
	msg := api.Message
	d := Diagnostic{File: file, Severity: Error, Source: SourceDryRun}
	if m := didYouMean.FindStringSubmatch(msg); m != nil {
		d.Fix = m[1]
	}

	off, placed := -1, false
	if m := errorAt.FindStringSubmatchIndex(msg); m != nil {
		line, _ := strconv.Atoi(msg[m[2]:m[3]])
		col, _ := strconv.Atoi(msg[m[4]:m[5]])
		msg = msg[:m[0]]
		off, placed = s.sourceOffset(scriptOffset(s.text, line, col))
	} else if m := notFoundName.FindStringSubmatch(msg); m != nil {
		// No position: find the table's name in the script instead, as
		// Dataform writes it (`p.d.t`) or dbt does (`p`.`d`.`t`).
		name := strings.Replace(m[1], ":", ".", 1)
		for _, n := range []string{name, strings.ReplaceAll(name, ".", "`.`")} {
			if i := strings.Index(s.text, n); i >= 0 {
				off, placed = s.sourceOffset(i)
				break
			}
		}
	}
	d.Message = msg
	if !placed {
		off = s.anchor
	}
	d.Line, d.Start, d.End = rangeAt(src, off)
	return d
}

// scriptOffset converts BigQuery's 1-based [line:column] (column counted in
// characters) to a byte offset in text.
func scriptOffset(text string, line, col int) int {
	off := 0
	for ; line > 1; line-- {
		i := strings.IndexByte(text[off:], '\n')
		if i < 0 {
			return len(text)
		}
		off += i + 1
	}
	for ; col > 1 && off < len(text) && text[off] != '\n'; col-- {
		_, size := utf8.DecodeRuneInString(text[off:])
		off += size
	}
	return off
}

// rangeAt is the line and range to mark for an error at source offset off:
// the identifier there, the ${...} or Jinja tag it is in, or the rest of the
// line.
func rangeAt(src []byte, off int) (line, start, end int) {
	line, start = lineCol(src, off)
	text := lineText(src, line)
	end = start
	switch {
	case bytes.HasPrefix(text[min(start, len(text)):], []byte("${")):
		if i := bytes.IndexByte(text[start:], '}'); i >= 0 {
			end = start + i + 1
		}
	case bytes.HasPrefix(text[min(start, len(text)):], []byte("{{")):
		if i := bytes.Index(text[start:], []byte("}}")); i >= 0 {
			end = start + i + 2
		}
	case bytes.HasPrefix(text[min(start, len(text)):], []byte("{%")):
		if i := bytes.Index(text[start:], []byte("%}")); i >= 0 {
			end = start + i + 2
		}
	default:
		for end < len(text) && isWordByte(text[end]) {
			end++
		}
	}
	if end == start {
		// Nothing to point at: mark the line's text.
		start = len(text) - len(bytes.TrimLeft(text, " \t"))
		end = len(bytes.TrimRight(text, " \t"))
		if start >= end {
			start, end = 0, len(text)
		}
	}
	return line, start, end
}

func hintAt(file string, src []byte, off int, msg string) Diagnostic {
	line, _ := lineCol(src, off)
	return Diagnostic{File: file, Line: line, Start: 0, End: len(lineText(src, line)), Severity: Hint, Source: SourceDryRun, Message: msg}
}

func reason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, bigquery.ErrAuth):
		return err.Error() + "; " + bigquery.AuthHint
	}
	return err.Error()
}

// costText describes a dry run's bytes and on-demand cost.
func costText(label string, d *bigquery.DryRun) string {
	// BigQuery says UNKNOWN both when it has no estimate (and reports 0) and
	// alongside a real figure; only the first means there is nothing to show.
	text := "bytes unknown"
	if d.Accuracy != bigquery.AccuracyUnknown || d.Bytes > 0 {
		text = fmt.Sprintf("%s processed (≈ %s)", formatBytes(d.Bytes), formatUSD(d.CostUSD()))
	}
	switch label {
	case "full":
		return "full refresh " + text
	case "incremental":
		return "incremental " + text
	}
	return text
}

// formatBytes uses binary units with the short names the BigQuery console
// shows.
func formatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

func formatUSD(v float64) string {
	if v >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}

// offsetOf converts a 0-based line and byte column to an offset in src.
func offsetOf(src []byte, line, col int) int {
	off := 0
	for ; line > 0; line-- {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return min(off+col, len(src))
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
