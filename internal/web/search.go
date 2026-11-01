package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/project"
)

// Searching the project (ADR 0016): the text of the files the tree lists,
// matched here, in Go, so a replace changes what the search listed. The
// page asks again as the query is typed; a replace names the matches it
// changes, and the server skips any that no longer fit the file.

const (
	// maxSearchMatches bounds the matches listed.
	maxSearchMatches = 5000
	// searchTimeout bounds one search.
	searchTimeout = 20 * time.Second
	// snippetBefore and snippetAfter bound, in characters, the text of a
	// match's line shown around it.
	snippetBefore = 20
	snippetAfter  = 200
)

// searchOptions are what a search matches: the query, as text or a
// regular expression (RE2), and the files it looks in.
type searchOptions struct {
	Query string `json:"query"`
	Case  bool   `json:"case"`
	Word  bool   `json:"word"`
	Regex bool   `json:"regex"`
	// Include and Exclude are globs, separated by commas.
	Include string `json:"include"`
	Exclude string `json:"exclude"`
	// Replacing says the replace field shows: each match then says what
	// Replace makes it ($1 and ${name} name groups, with Regex).
	Replacing bool   `json:"replacing"`
	Replace   string `json:"replace"`
}

// searchRequest is a search: its options, and the file the source pane
// shows with unsaved changes, searched as it is typed there.
type searchRequest struct {
	searchOptions
	Shown *struct {
		File string `json:"file"`
		Text string `json:"text"`
	} `json:"shown,omitempty"`
}

// searchMatch is one match. Line is 0-based; Col and Len count UTF-16
// units, as the page's strings do. Before and After are its line around
// it, cut to a few words before and a line after.
type searchMatch struct {
	Line   int    `json:"line"`
	Col    int    `json:"col"`
	Len    int    `json:"len"`
	Text   string `json:"text"`
	With   string `json:"with"`
	Before string `json:"before"`
	After  string `json:"after"`
	// LineText and At (bytes into it) say where it is, for a replace to
	// find it again in a file that changed since.
	LineText string `json:"lineText"`
	At       int    `json:"at"`
}

type searchFile struct {
	Path string `json:"path"`
	// Hash is of the file searched, "" for the source pane's unsaved text:
	// a replace in the same file finds its matches where they were.
	Hash string `json:"hash,omitempty"`
	// Action is the file's first action, which opening it selects.
	Action  project.ActionID `json:"action,omitempty"`
	Matches []searchMatch    `json:"matches"`
}

// searchView is a search's answer. Error is a query that isn't a regular
// expression, or a glob that isn't one.
type searchView struct {
	Files     []searchFile `json:"files"`
	Count     int          `json:"count"`
	Truncated bool         `json:"truncated,omitempty"`
	Error     string       `json:"error,omitempty"`
	// Searched counts the files looked in.
	Searched int `json:"searched"`
}

// matcher finds a query's matches in a text.
type matcher struct {
	re      *regexp.Regexp
	word    bool
	regex   bool
	replace string
}

func newMatcher(o searchOptions) (*matcher, error) {
	expr := o.Query
	if !o.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	// ^ and $ are a line's ends, as in VS Code.
	flags := "(?m)"
	if !o.Case {
		flags = "(?mi)"
	}
	re, err := regexp.Compile(flags + expr)
	if err != nil {
		var se *syntax.Error
		if errors.As(err, &se) {
			return nil, fmt.Errorf("not a regular expression: %s", se.Code)
		}
		return nil, err
	}
	return &matcher{re: re, word: o.Word, regex: o.Regex, replace: o.Replace}, nil
}

// span is a match in a text: bytes [start, end), and its groups.
type span struct {
	start, end int
	groups     []int
}

// find lists the matches in text, at most limit of them (0: no bound).
// An empty match is no match.
func (m *matcher) find(text string, limit int) []span {
	var out []span
	for _, g := range m.re.FindAllStringSubmatchIndex(text, -1) {
		s, e := g[0], g[1]
		if s == e {
			continue
		}
		if m.word && !(wordEdge(text, s, true) && wordEdge(text, e, false)) {
			continue
		}
		out = append(out, span{s, e, g})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// wordEdge says whether a whole-word match may start (or end) at i: no
// letter, digit or _ joins it there.
func wordEdge(text string, i int, start bool) bool {
	var r rune
	if start {
		if i == 0 {
			return true
		}
		r, _ = utf8.DecodeLastRuneInString(text[:i])
	} else {
		if i >= len(text) {
			return true
		}
		r, _ = utf8.DecodeRuneInString(text[i:])
	}
	return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

// with is what the match becomes.
func (m *matcher) with(text string, s span) string {
	if !m.regex {
		return m.replace
	}
	return string(m.re.ExpandString(nil, m.replace, text, s.groups))
}

// lineAt is the line of text that byte i is on: its start and end, and
// its 0-based number counted from (from, line).
func lineAt(text string, i, from, line int) (start, end, n int) {
	n = line + strings.Count(text[from:i], "\n")
	start = strings.LastIndexByte(text[:i], '\n') + 1
	end = strings.IndexByte(text[i:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += i
	}
	return start, end, n
}

// matchesIn lists text's matches as the page shows them.
func (m *matcher) matchesIn(text string, limit int) []searchMatch {
	spans := m.find(text, limit)
	out := make([]searchMatch, 0, len(spans))
	from, line := 0, 0
	for _, s := range spans {
		ls, le, n := lineAt(text, s.start, from, line)
		from, line = ls, n
		lineText := text[ls:le]
		end := min(s.end, le)
		before, after := []rune(text[ls:s.start]), []rune(text[end:le])
		b := string(before)
		if len(before) > snippetBefore {
			b = "…" + strings.TrimLeft(string(before[len(before)-snippetBefore:]), " \t")
		} else {
			b = strings.TrimLeft(b, " \t")
		}
		a := string(after)
		if len(after) > snippetAfter {
			a = string(after[:snippetAfter]) + "…"
		}
		out = append(out, searchMatch{
			Line: n, Col: unitsAt(text[ls:s.start], s.start-ls), Len: unitsAt(text[s.start:s.end], s.end-s.start),
			Text: text[s.start:s.end], With: m.with(text, s), Before: b, After: a,
			LineText: lineText, At: s.start - ls,
		})
	}
	return out
}

// globs are the include or exclude field's globs:
// a glob with no / matches a file or folder by name anywhere (*.md,
// node_modules); one with a / matches from the project's root
// (definitions/**); one ending in / is a folder. A folder matches the
// files in it.
type globs []*regexp.Regexp

// globRegexp is one glob as a regular expression of a path.
func globRegexp(g string) (*regexp.Regexp, error) {
	g = strings.TrimPrefix(strings.TrimSuffix(g, "/"), "./")
	var b strings.Builder
	if strings.Contains(strings.TrimPrefix(g, "**/"), "/") {
		b.WriteString("^")
	} else {
		b.WriteString("(^|/)")
		g = strings.TrimPrefix(g, "**/")
	}
	brace := 0
	rs := []rune(g)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '*' && i+1 < len(rs) && rs[i+1] == '*':
			i++
			if i+1 < len(rs) && rs[i+1] == '/' {
				i++
				b.WriteString("(.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{':
			brace++
			b.WriteString("(")
		case c == '}' && brace > 0:
			brace--
			b.WriteString(")")
		case c == ',' && brace > 0:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if brace > 0 {
		return nil, fmt.Errorf("not a glob: %s", g)
	}
	b.WriteString("(/|$)")
	return regexp.Compile(b.String())
}

// globsOf reads a list of globs separated by the commas outside braces.
func globsOf(list string) (globs, error) {
	var out globs
	add := func(g string) error {
		if g = strings.TrimSpace(g); g == "" {
			return nil
		}
		re, err := globRegexp(g)
		if err != nil {
			return err
		}
		out = append(out, re)
		return nil
	}
	depth, at := 0, 0
	for i, c := range list {
		switch c {
		case '{':
			depth++
		case '}':
			depth = max(depth-1, 0)
		case ',':
			if depth == 0 {
				if err := add(list[at:i]); err != nil {
					return nil, err
				}
				at = i + 1
			}
		}
	}
	if err := add(list[at:]); err != nil {
		return nil, err
	}
	return out, nil
}

func (gs globs) match(path string) bool {
	for _, re := range gs {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// searchText reads a file the search may look in: text, not too big to
// edit, with one kind of line end. ok is false for one it skips.
func searchText(path string) (f *sourceFile, text string, ok bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxSourceBytes {
		return nil, "", false
	}
	f, err = readSource(path)
	if err != nil || f.why != "" || isBinary(f.raw) {
		return nil, "", false
	}
	return f, f.text(), true
}

// projectFiles lists the files the tree does.
func (p *projectState) projectFiles(ctx context.Context) ([]string, error) {
	files, _, _, err := gitFiles(ctx, p.deps.Root)
	if err != nil {
		files, _, err = walkFiles(p.deps.Root)
	}
	return files, err
}

// search answers with the matches of a query in the project's files.
func (p *projectState) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !readSourceJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	v, err := p.searchFiles(ctx, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, v)
}

func (p *projectState) searchFiles(ctx context.Context, req searchRequest) (searchView, error) {
	v := searchView{Files: []searchFile{}}
	if req.Query == "" {
		return v, nil
	}
	m, err := newMatcher(req.searchOptions)
	if err != nil {
		v.Error = err.Error()
		return v, nil
	}
	include, err := globsOf(req.Include)
	if err != nil {
		v.Error = "files to include: " + err.Error()
		return v, nil
	}
	exclude, err := globsOf(req.Exclude)
	if err != nil {
		v.Error = "files to exclude: " + err.Error()
		return v, nil
	}
	return p.searchWith(ctx, req, m, include, exclude)
}

func (p *projectState) searchWith(ctx context.Context, req searchRequest, m *matcher, include, exclude globs) (searchView, error) {
	v := searchView{Files: []searchFile{}}
	files, err := p.projectFiles(ctx)
	if err != nil {
		return v, err
	}
	if len(files) > maxTreeFiles {
		files = files[:maxTreeFiles]
	}
	var list []string
	for _, f := range files {
		if (len(include) == 0 || include.match(f)) && !exclude.match(f) {
			list = append(list, f)
		}
	}
	v.Searched = len(list)
	// The files are read and matched side by side, and listed in order.
	found := make([][]searchMatch, len(list))
	hashes := make([]string, len(list))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), 8) {
		wg.Go(func() {
			for i := range next {
				text, ok := "", false
				if req.Shown != nil && req.Shown.File == list[i] {
					text, ok = req.Shown.Text, true
				} else if path, err := p.sourcePath(list[i]); err == nil {
					var f *sourceFile
					if f, text, ok = searchText(path); ok {
						hashes[i] = hashOf(f.raw)
					}
				}
				if ok {
					found[i] = m.matchesIn(text, maxSearchMatches+1)
				}
			}
		})
	}
feed:
	for i := range list {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return v, err
	}
	actions := p.fileActions()
	for i, ms := range found {
		if len(ms) == 0 {
			continue
		}
		if left := maxSearchMatches - v.Count; len(ms) > left {
			ms, v.Truncated = ms[:left], true
		}
		if len(ms) > 0 {
			v.Files = append(v.Files, searchFile{Path: list[i], Hash: hashes[i], Action: actions[list[i]].first, Matches: ms})
			v.Count += len(ms)
		}
		if v.Truncated {
			break
		}
	}
	return v, nil
}

// ---- replacing ----

// replaceRequest replaces the matches named, in each file, with what the
// options make them.
type replaceRequest struct {
	searchOptions
	Files []struct {
		Path    string `json:"path"`
		Hash    string `json:"hash"`
		Matches []struct {
			Line     int    `json:"line"`
			Text     string `json:"text"`
			LineText string `json:"lineText"`
			At       int    `json:"at"`
		} `json:"matches"`
	} `json:"files"`
}

// replaced is a file a replace wrote: what it was, for an undo, and the
// hash of what it wrote, which says whether it changed again since.
type replaced struct {
	file string
	old  []byte
	hash string
}

type replaceView struct {
	Replaced int `json:"replaced"`
	Files    int `json:"files"`
	// Skipped counts the matches that no longer fit their file.
	Skipped int `json:"skipped"`
	// Failed names the files that couldn't be written, with why.
	Failed  []string `json:"failed,omitempty"`
	Message string   `json:"message"`
	// Undo says an undo can put the files back.
	Undo bool `json:"undo"`
}

// matchCount says n matches.
func matchCount(n int) string {
	if n == 1 {
		return "1 match"
	}
	return fmt.Sprintf("%d matches", n)
}

// replace writes the replacements, file by file, and keeps what each file
// was for an undo, in place of the last replace's.
func (p *projectState) replace(w http.ResponseWriter, r *http.Request) {
	var req replaceRequest
	if !readSourceJSON(w, r, &req) {
		return
	}
	m, err := newMatcher(req.searchOptions)
	if err != nil {
		writeError(w, err)
		return
	}
	p.searchMu.Lock()
	defer p.searchMu.Unlock()
	var v replaceView
	var undo []replaced
	for _, rf := range req.Files {
		path, err := p.sourcePath(rf.Path)
		if err != nil {
			v.Failed = append(v.Failed, rf.Path+": "+err.Error())
			continue
		}
		f, text, ok := searchText(path)
		if !ok {
			v.Failed = append(v.Failed, rf.Path+": can't be edited here")
			continue
		}
		spans := m.find(text, 0)
		// A match is found again by its text and where it is on its line:
		// on its line, in the file searched; else on the nearest line with
		// the same text, as lines added above it move it.
		same := rf.Hash != "" && rf.Hash == hashOf(f.raw)
		type key struct {
			line string
			at   int
			text string
		}
		at := map[key][]int{} // span indexes, by key
		lines := make([]int, len(spans))
		from, line := 0, 0
		for i, s := range spans {
			ls, le, n := lineAt(text, s.start, from, line)
			from, line, lines[i] = ls, n, n
			k := key{text[ls:le], s.start - ls, text[s.start:s.end]}
			at[k] = append(at[k], i)
		}
		take := map[int]bool{}
		for _, want := range rf.Matches {
			best := -1
			for _, i := range at[key{want.LineText, want.At, want.Text}] {
				if take[i] || same && lines[i] != want.Line {
					continue
				}
				if best < 0 || abs(lines[i]-want.Line) < abs(lines[best]-want.Line) {
					best = i
				}
			}
			if best < 0 {
				v.Skipped++
				continue
			}
			take[best] = true
		}
		if len(take) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		for i, s := range spans {
			if !take[i] {
				continue
			}
			b.WriteString(text[last:s.start])
			b.WriteString(m.with(text, s))
			last = s.end
		}
		b.WriteString(text[last:])
		out := f.encode(b.String())
		if err := replaceFile(path, out); err != nil {
			v.Failed = append(v.Failed, rf.Path+": "+err.Error())
			continue
		}
		undo = append(undo, replaced{file: rf.Path, old: f.raw, hash: hashOf(out)})
		v.Replaced += len(take)
		v.Files++
	}
	if len(undo) > 0 {
		p.lastReplace = undo
	}
	v.Undo = len(p.lastReplace) > 0
	msg := []string{"replaced " + matchCount(v.Replaced) + " in " + plural(v.Files, "file")}
	if v.Replaced == 0 {
		msg = []string{"replaced nothing"}
	}
	if v.Skipped > 0 {
		msg = append(msg, matchCount(v.Skipped)+" skipped: the file changed")
	}
	if len(v.Failed) > 0 {
		msg = append(msg, strings.Join(v.Failed, "; "))
	}
	v.Message = strings.Join(msg, " · ")
	writeJSON(w, v)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

type undoView struct {
	Restored int `json:"restored"`
	// Skipped names the files that changed since the replace.
	Skipped []string `json:"skipped,omitempty"`
	Message string   `json:"message"`
}

// undoReplace puts back the files the last replace wrote, but for those
// that changed since, which it names.
func (p *projectState) undoReplace(w http.ResponseWriter, r *http.Request) {
	p.searchMu.Lock()
	defer p.searchMu.Unlock()
	if len(p.lastReplace) == 0 {
		writeError(w, errors.New("no replace to undo"))
		return
	}
	var v undoView
	var failed []string
	for _, f := range p.lastReplace {
		path, err := p.sourcePath(f.file)
		if err != nil {
			v.Skipped = append(v.Skipped, f.file)
			continue
		}
		now, err := os.ReadFile(path)
		if err != nil || hashOf(now) != f.hash {
			v.Skipped = append(v.Skipped, f.file)
			continue
		}
		if err := replaceFile(path, f.old); err != nil {
			failed = append(failed, f.file+": "+err.Error())
			continue
		}
		v.Restored++
	}
	p.lastReplace = nil
	msg := []string{"put back " + plural(v.Restored, "file")}
	if len(v.Skipped) > 0 {
		msg = append(msg, "changed since, left as they are: "+strings.Join(v.Skipped, ", "))
	}
	msg = append(msg, failed...)
	v.Message = strings.Join(msg, " · ")
	writeJSON(w, v)
}
