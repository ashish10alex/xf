package web

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/diagnose"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// The source pane edits the file of the selected action. A save names the
// hash of the text the page loaded, and is refused when the file on disk
// is no longer that text: another editor, a git checkout or a formatter
// changed it, and writing would lose their change. Any text file inside
// the project can be read and written, outside .git (ADR 0006); only an
// action's file gets completion, refs and the dry run's marks.

const (
	// maxSourceBytes is the largest file the pane shows.
	maxSourceBytes = 2 << 20
	// maxSourceBody bounds a request that carries a file's text.
	maxSourceBody = maxSourceBytes + 1<<16
)

// minSourceAutosaveMs is the shortest wait before an autosave: each save
// compiles the project.
const minSourceAutosaveMs = 500

// autosaveMs is the wait the page autosaves after: ms, at least
// minSourceAutosaveMs, or 0 for off.
func autosaveMs(ms int) int {
	if ms <= 0 {
		return 0
	}
	return max(ms, minSourceAutosaveMs)
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// errNotSource is a path the pane may not touch, and errNotAction a file
// without an action, which has no refs or completions.
var (
	errNotSource = errors.New("not a file in the project")
	errNotAction = errors.New("not the file of one of the project's actions")
)

// sourceView is a file as the source pane edits it: its text with "\n"
// line ends and no byte-order mark, and the hash of its bytes on disk.
type sourceView struct {
	File string `json:"file"`
	Text string `json:"text"`
	Hash string `json:"hash"`
	// Lang is "sqlx", "js", "sql", "json", "yaml", "toml", "python",
	// "shell", "hcl" (Terraform), "markdown", or "" for anything else.
	Lang string `json:"lang"`
	// ReadOnly is why the file can't be saved from the pane, "" when it
	// can.
	ReadOnly string `json:"readOnly,omitempty"`
	// Base is the file as git's index has it, which the pane marks the
	// changes since, as gitsigns does: "" for a file git doesn't track
	// yet, all of it new; nil with nothing to compare with (not in a
	// repository, an ignored file, no git).
	Base *string `json:"base,omitempty"`
}

// sourceFile is a file's bytes and how they are written.
type sourceFile struct {
	raw  []byte
	bom  bool
	crlf bool
	// why is why it can't be saved: mixed line ends, not UTF-8, too big.
	why string
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readSource reads the file at path and works out how it is written.
func readSource(path string) (*sourceFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &sourceFile{raw: raw}
	body := raw
	if bytes.HasPrefix(body, utf8BOM) {
		f.bom, body = true, body[len(utf8BOM):]
	}
	crlf, lf, cr := bytes.Count(body, []byte("\r\n")), bytes.Count(body, []byte("\n")), bytes.Count(body, []byte("\r"))
	switch {
	case len(raw) > maxSourceBytes:
		f.why = "too big to edit here"
	case !utf8.Valid(body):
		f.why = "not UTF-8"
	case cr != crlf || (crlf > 0 && crlf != lf):
		f.why = "mixed line endings"
	}
	f.crlf = crlf > 0 && crlf == lf && cr == crlf
	return f, nil
}

// text is the file as the pane edits it.
func (f *sourceFile) text() string {
	body := f.raw
	if f.bom {
		body = body[len(utf8BOM):]
	}
	if f.crlf {
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	}
	return string(body)
}

// encode is text written as the file is: its line ends and byte-order mark.
func (f *sourceFile) encode(text string) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if f.crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if f.bom {
		return append(append([]byte{}, utf8BOM...), text...)
	}
	return []byte(text)
}

func langOf(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".sqlx":
		return "sqlx"
	case ".js":
		return "js"
	case ".sql":
		return "sql"
	case ".json", ".ipynb":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".py", ".pyi":
		return "python"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".tf", ".tfvars", ".hcl":
		return "hcl"
	case ".md", ".markdown":
		return "markdown"
	}
	return ""
}

func (p *projectState) viewOf(file, path string, f *sourceFile) sourceView {
	v := sourceView{File: file, Text: f.text(), Hash: hashOf(f.raw), Lang: langOf(file), ReadOnly: f.why}
	if why := p.libraryWhy(path); why != "" {
		// An installed package's file, which gd showed (ADR 0018).
		v.ReadOnly = why
		return v
	}
	if len(f.raw) <= maxSourceBytes {
		v.Base = gitBase(path)
	}
	return v
}

// projectPath is where file (relative to the root, with forward slashes)
// is in the project: the folder it is in with links followed, and its own
// name, a link not followed. The file needn't exist, nor the folders it
// would be in, but the nearest of them that does must be in the project,
// and none of it in .git.
func (p *projectState) projectPath(file string) (string, error) {
	if file == "" || strings.Contains(file, "\\") || strings.HasPrefix(file, "/") || filepath.IsAbs(filepath.FromSlash(file)) {
		return "", errNotSource
	}
	for part := range strings.SplitSeq(file, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return "", errNotSource
		}
	}
	root, err := filepath.EvalSymlinks(p.deps.Root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(p.deps.Root, filepath.FromSlash(file))
	have, rest := filepath.Dir(path), filepath.Base(path)
	for {
		if _, err := os.Lstat(have); err == nil {
			break
		}
		parent := filepath.Dir(have)
		if parent == have {
			return "", errNotSource
		}
		rest = filepath.Join(filepath.Base(have), rest)
		have = parent
	}
	real, err := filepath.EvalSymlinks(have)
	if err != nil || !inside(root, real) {
		return "", errNotSource
	}
	return filepath.Join(real, rest), nil
}

// inside says whether path is root or in it, and not in its .git; both
// have their links followed.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return !strings.EqualFold(top, ".git")
}

// sourcePath is where file is on disk, its links followed, when it is a
// regular file in the project (see projectPath).
func (p *projectState) sourcePath(file string) (string, error) {
	entry, err := p.projectPath(file)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(p.deps.Root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(entry)
	if err != nil || !inside(root, path) {
		return "", errNotSource
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", errNotSource
	}
	return path, nil
}

// actionSourcePath is sourcePath for the file of one of the graph's
// actions: completion, refs and the like only work there.
func (p *projectState) actionSourcePath(file string) (string, error) {
	g := p.graph()
	if g == nil {
		return "", errors.New("no compile result yet")
	}
	if len(g.ActionsInFile(file)) == 0 {
		return "", errNotAction
	}
	return p.sourcePath(file)
}

// sourceError answers a failed source request: 403 for a path the pane
// may not touch.
func sourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotSource) || errors.Is(err, errNotAction) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	writeError(w, err)
}

// readSourceJSON decodes a request that carries a file's text.
func readSourceJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// source answers with ?file as the pane edits it, or shows a library
// file pyrefly named (a path), read-only.
func (p *projectState) source(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	path, ok := p.libraryFile(file)
	var err error
	if !ok {
		path, err = p.sourcePath(file)
	}
	if err != nil {
		sourceError(w, err)
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSourceBytes {
		writeError(w, errors.New(file+" is too big to show here ("+strconv.FormatInt(fi.Size()>>20, 10)+" MB)"))
		return
	}
	f, err := readSource(path)
	if err != nil {
		writeError(w, err)
		return
	}
	if isBinary(f.raw) {
		writeError(w, errors.New(file+" isn't a text file"))
		return
	}
	writeJSON(w, p.viewOf(file, path, f))
}

// isBinary says whether raw looks like something other than text: a NUL
// in its first 8 KB, as git decides.
func isBinary(raw []byte) bool {
	return bytes.IndexByte(raw[:min(len(raw), 8000)], 0) >= 0
}

// conflictView is a refused save's answer: the file as it is on disk now.
type conflictView struct {
	Error   string     `json:"error"`
	Kind    string     `json:"kind"`
	Current sourceView `json:"current"`
}

// sourceSave writes a file the pane edited, when it is still the text the
// pane loaded (hash); else it answers 409 with what is on disk. The write
// replaces the file in one step and keeps its line ends, byte-order mark
// and permissions. The engine's watcher then compiles it.
func (p *projectState) sourceSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
		Text string `json:"text"`
		Hash string `json:"hash"`
	}
	if !readSourceJSON(w, r, &req) {
		return
	}
	path, err := p.sourcePath(req.File)
	if err != nil {
		sourceError(w, err)
		return
	}
	f, err := readSource(path)
	if err != nil {
		writeError(w, err)
		return
	}
	if hashOf(f.raw) != req.Hash {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(conflictView{Error: req.File + " changed on disk", Kind: "conflict", Current: p.viewOf(req.File, path, f)})
		return
	}
	if why := cmp.Or(f.why, p.libraryWhy(path)); why != "" {
		writeError(w, errors.New(req.File+" can't be saved here: "+why))
		return
	}
	out := f.encode(req.Text)
	if err := replaceFile(path, out); err != nil {
		writeError(w, err)
		return
	}
	saved := &sourceFile{raw: out, bom: f.bom, crlf: f.crlf}
	writeJSON(w, p.viewOf(req.File, path, saved))
}

// replaceFile writes data to path in one step: a temporary file beside it,
// renamed over it, with its permissions.
func replaceFile(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xf-save-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ---- completion, refs and diagnostics ----

// The page counts offsets in UTF-16 units, as JavaScript does; navigate
// and diagnose count bytes.

// byteAt is the byte offset of the UTF-16 offset u in s.
func byteAt(s string, u int) int {
	n := 0
	for i, r := range s {
		if n >= u {
			return i
		}
		n += utf16Len(r)
	}
	return len(s)
}

// unitsAt is the UTF-16 offset of the byte offset b in s.
func unitsAt(s string, b int) int {
	n := 0
	for i, r := range s {
		if i >= b {
			break
		}
		n += utf16Len(r)
	}
	return n
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// navProject is the project as navigate sees it.
func (p *projectState) navProject() navigate.Project {
	return navigate.Project{Root: p.deps.Root, Graph: p.graph(), Dbt: p.tool == "dbt"}
}

// sourceRequest is a request about the pane's unsaved text.
type sourceRequest struct {
	File   string `json:"file"`
	Text   string `json:"text"`
	Offset int    `json:"offset"`
}

// readSourceRequest decodes a sourceRequest about an action's file and
// answers with its absolute path.
func (p *projectState) readSourceRequest(w http.ResponseWriter, r *http.Request, req *sourceRequest) (string, bool) {
	if !readSourceJSON(w, r, req) {
		return "", false
	}
	if _, err := p.actionSourcePath(req.File); err != nil {
		sourceError(w, err)
		return "", false
	}
	return filepath.Join(p.deps.Root, filepath.FromSlash(req.File)), true
}

// completionView is one completion: insert replaces the text from from to
// the cursor.
type completionView struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Insert string `json:"insert"`
	Filter string `json:"filter,omitempty"`
	From   int    `json:"from"`
	Kind   string `json:"kind"`
}

// sourceComplete answers with what can be typed at offset in the text:
// navigate's completions, as xf lsp offers them (a ${ref()} after a $,
// names in a config's dependencies and tags; dbt's refs, sources and
// macros).
func (p *projectState) sourceComplete(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	abs, ok := p.readSourceRequest(w, r, &req)
	if !ok {
		return
	}
	src := []byte(req.Text)
	out := []completionView{}
	for _, c := range navigate.Completions(p.navProject(), abs, src, byteAt(req.Text, req.Offset)) {
		kind := "ref"
		switch c.Kind {
		case navigate.CompletionTag:
			kind = "tag"
		case navigate.CompletionMacro:
			kind = "macro"
		case navigate.CompletionDependency:
			kind = "dependency"
		}
		out = append(out, completionView{Label: c.Label, Detail: c.Detail, Insert: c.InsertText, Filter: c.FilterText,
			From: unitsAt(req.Text, c.Start), Kind: kind})
	}
	writeJSON(w, struct {
		Items []completionView `json:"items"`
	}{out})
}

// refView is a table the text refers to, where it does.
type refView struct {
	From    int    `json:"from"`
	To      int    `json:"to"`
	Project string `json:"project"`
	Dataset string `json:"dataset"`
	Table   string `json:"table"`
	// Source is "ref", "raw" or "self".
	Source string `json:"source"`
	// Action is the project's action for it, "" for a table outside.
	Action string `json:"action,omitempty"`
	// HasTable is false for an action with no table of its own (an
	// operation without hasOutput, an ephemeral model): there are no
	// columns to look up.
	HasTable bool `json:"hasTable"`
	// Kind is the action's kind ("operation", "ephemeral"…), "" for a
	// table outside: the hover says why there is no schema.
	Kind string `json:"kind,omitempty"`
	// ConsoleURL is the table's page in the BigQuery console, as u opens
	// an action's: "" with no table, or no project to find it in.
	ConsoleURL string `json:"consoleUrl,omitempty"`
}

// sourceRefs answers with every table the text refers to, and where its
// JavaScript is (a sqlx file's config and js blocks and ${}), which has
// no SQL columns.
func (p *projectState) sourceRefs(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	abs, ok := p.readSourceRequest(w, r, &req)
	if !ok {
		return
	}
	src := []byte(req.Text)
	refs := []refView{}
	for _, t := range navigate.TableRefs(p.navProject(), abs, src) {
		v := refView{From: unitsAt(req.Text, t.Start), To: unitsAt(req.Text, t.End),
			Project: t.Target.Database, Dataset: t.Target.Schema, Table: t.Target.Name, HasTable: true}
		switch t.Source {
		case navigate.SourceRaw:
			v.Source = "raw"
		case navigate.SourceSelf:
			v.Source = "self"
		default:
			v.Source = "ref"
		}
		if t.Action != nil {
			v.Action = string(t.Action.ID)
			v.HasTable = hasOutputTable(t.Action)
			v.Kind = t.Action.Kind.String()
		}
		if v.HasTable && v.Project != "" {
			v.ConsoleURL = t.Target.ConsoleURL()
		}
		refs = append(refs, v)
	}
	js := [][2]int{}
	if langOf(req.File) == "sqlx" {
		for _, s := range navigate.JSSpans(src) {
			js = append(js, [2]int{unitsAt(req.Text, s[0]), unitsAt(req.Text, s[1])})
		}
	}
	writeJSON(w, struct {
		Refs []refView `json:"refs"`
		JS   [][2]int  `json:"js"`
	}{refs, js})
}

// locationView is where something is defined: line counts from 0, col is
// a UTF-16 column in it.
type locationView struct {
	// File is relative to the root, with forward slashes; Outside says it
	// is not inside the project.
	File    string `json:"file"`
	Outside bool   `json:"outside,omitempty"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	// Action is the action defined there, "" when the file is not an
	// action's (an include, a macro): the pane can't show it.
	Action string `json:"action,omitempty"`
}

// sourceDefinition answers with where the symbol at offset in the text is
// defined, as xf lsp's go-to-definition does: a ref's action, a require's
// file, a CTE, and in dbt a ref's model, a source or a macro.
func (p *projectState) sourceDefinition(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	abs, ok := p.readSourceRequest(w, r, &req)
	if !ok {
		return
	}
	np := p.navProject()
	out := []locationView{}
	for _, l := range navigate.Definition(np, abs, []byte(req.Text), byteAt(req.Text, req.Offset)) {
		out = append(out, p.locationOf(np, abs, req.Text, l))
	}
	writeJSON(w, struct {
		Locations []locationView `json:"locations"`
	}{out})
}

// locationOf is l as the page counts it: a path relative to the root and
// a UTF-16 column, of text when l is in the file asked about (abs) and of
// the file on disk when not, and the action defined there.
func (p *projectState) locationOf(np navigate.Project, abs, text string, l navigate.Location) locationView {
	v := locationView{Line: l.Line, Col: l.Col}
	rel, err := filepath.Rel(p.deps.Root, l.File)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		v.File, v.Outside = filepath.ToSlash(l.File), true
	} else {
		v.File = filepath.ToSlash(rel)
	}
	if l.File != abs {
		if b, err := os.ReadFile(l.File); err == nil {
			text = string(b)
		} else {
			text = ""
		}
	}
	if lines := strings.Split(text, "\n"); l.Line < len(lines) {
		v.Col = unitsAt(lines[l.Line], min(l.Col, len(lines[l.Line])))
	}
	if !v.Outside {
		v.Action = string(actionAt(np, v.File, l))
	}
	return v
}

// includeView is the card of an includes name (parameters.X): where X is
// declared, its code and the comment above it; from and to bound
// parameters.X in the text asked about.
type includeView struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Code string `json:"code"`
	Doc  string `json:"doc,omitempty"`
	locationView
}

// sourceInclude answers with the includes name at offset in a Dataform
// file's JavaScript (parameters.X, X declared in includes/parameters.js),
// or found false. It reads that one file and nothing else.
func (p *projectState) sourceInclude(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	abs, ok := p.readSourceRequest(w, r, &req)
	if !ok {
		return
	}
	np := p.navProject()
	inc, found := navigate.IncludeAt(np, abs, []byte(req.Text), byteAt(req.Text, req.Offset))
	var v *includeView
	if found {
		v = &includeView{From: unitsAt(req.Text, inc.Start), To: unitsAt(req.Text, inc.End),
			Code: inc.Code, Doc: inc.Doc, locationView: p.locationOf(np, abs, req.Text, inc.Location)}
	}
	writeJSON(w, struct {
		Include *includeView `json:"include"`
	}{v})
}

// lineColAt returns the 0-based line number and 0-based UTF-16 column of the
// byte offset in s.
func lineColAt(s string, byteOffset int) (line, col int) {
	if byteOffset > len(s) {
		byteOffset = len(s)
	}
	sub := s[:byteOffset]
	line = strings.Count(sub, "\n")
	lastNL := strings.LastIndex(sub, "\n")
	if lastNL < 0 {
		col = unitsAt(sub, len(sub))
	} else {
		after := sub[lastNL+1:]
		col = unitsAt(after, len(after))
	}
	return line, col
}

// symbolView is one entry of a file's outline for the symbol picker.
type symbolView struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	Line   int    `json:"line"`
	Col    int    `json:"col"`
}

// sourceSymbols answers with the outline of the file: its refs, literal
// BigQuery tables, CTEs and macros, ordered with references first.
func (p *projectState) sourceSymbols(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	abs, ok := p.readSourceRequest(w, r, &req)
	if !ok {
		return
	}
	src := []byte(req.Text)
	var syms []navigate.Symbol
	if p.tool == "dbt" {
		syms = navigate.DbtDocumentSymbols(abs, src)
	} else {
		syms = navigate.DocumentSymbols(abs, src)
	}
	syms = navigate.ReferencesFirst(syms)
	out := make([]symbolView, 0, len(syms))
	for _, s := range syms {
		selStart, selEnd := s.SelStart, s.SelEnd
		if selStart == 0 && selEnd == 0 && (s.Start != 0 || s.End != 0) {
			selStart, selEnd = s.Start, s.End
		}
		if selEnd < selStart {
			selEnd = selStart
		}
		line, col := lineColAt(req.Text, selStart)
		out = append(out, symbolView{
			Name:   s.Name,
			Kind:   s.Kind.Detail(),
			Detail: s.Kind.Detail(),
			From:   unitsAt(req.Text, selStart),
			To:     unitsAt(req.Text, selEnd),
			Line:   line + 1,
			Col:    col,
		})
	}
	writeJSON(w, struct {
		Symbols []symbolView `json:"symbols"`
	}{out})
}

// actionAt is the action defined at l in file (relative, slashed): the one
// whose definition starts there, else the file's first, or "" when file is
// no action's.
func actionAt(np navigate.Project, file string, l navigate.Location) project.ActionID {
	if np.Graph == nil {
		return ""
	}
	var first project.ActionID
	for _, a := range np.Graph.Actions {
		if a.FileName != file || a.Kind == project.KindUnknown {
			continue
		}
		if navigate.ActionLocation(np, a) == l {
			return a.ID
		}
		if first == "" || a.ID < first {
			first = a.ID
		}
	}
	return first
}

// diagnosticView is a diagnostic on the pane's text: line counts from 0,
// start and end are UTF-16 columns in it.
type diagnosticView struct {
	Line     int    `json:"line"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source"`
}

// sourceDryRunTimeout bounds a file's dry runs, as xf lsp's do.
const sourceDryRunTimeout = 20 * time.Second

// sourceDiagnostics answers with ?file's errors, placed in it: the last
// compile's, and its dry runs' (as in xf lsp), for the file
// as it is on disk, whose hash the answer carries.
func (p *projectState) sourceDiagnostics(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	path, err := p.sourcePath(file)
	if err != nil {
		sourceError(w, err)
		return
	}
	f, err := readSource(path)
	if err != nil {
		writeError(w, err)
		return
	}
	p.mu.Lock()
	errs, g := p.st.Errors, p.st.Graph
	p.mu.Unlock()
	abs := filepath.Join(p.deps.Root, filepath.FromSlash(file))
	body := f.raw
	if f.bom {
		body = body[len(utf8BOM):]
	}
	read := func(name string) ([]byte, error) {
		if name == abs {
			return body, nil
		}
		return os.ReadFile(name)
	}
	ds := diagnose.Compile(p.deps.Root, p.tool, errs, read)[abs]
	if len(ds) == 0 && p.deps.DryRun != nil && g != nil {
		ctx, cancel := context.WithTimeout(r.Context(), sourceDryRunTimeout)
		defer cancel()
		deferred := p.deferredFile(ctx, g, file)
		run := func(ctx context.Context, a *project.Action, query string) (*bigquery.DryRun, error) {
			project, location := g.DryRunTarget(a)
			if d := deferred[a.ID]; d != nil {
				query = deferral.Rewrite(query, d.Entries)
			}
			return p.deps.DryRun(ctx, project, location, query)
		}
		ds = append(ds, diagnose.DryRunFile(ctx, navigate.Project{Root: p.deps.Root, Graph: g, Dbt: p.tool == "dbt"}, abs, body, run)...)
	}
	lines := strings.Split(f.text(), "\n")
	out := []diagnosticView{}
	for _, d := range ds {
		if d.Severity != diagnose.Error || d.Line < 0 || d.Line >= len(lines) {
			continue
		}
		line := lines[d.Line]
		out = append(out, diagnosticView{Line: d.Line, Start: unitsAt(line, d.Start), End: unitsAt(line, d.End),
			Severity: "error", Message: d.Message, Source: d.Source})
	}
	writeJSON(w, struct {
		Hash        string           `json:"hash"`
		Diagnostics []diagnosticView `json:"diagnostics"`
	}{hashOf(f.raw), out})
}
