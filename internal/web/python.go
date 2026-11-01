package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/pyrefly"
)

// The source pane's Python files are checked by pyrefly's language server
// (ADR 0018): one `pyrefly lsp` for the project, started the first time a
// page asks about a .py file and stopped when the project closes. Each
// request carries the pane's unsaved text, which goes to pyrefly first;
// its errors reach the pages as "python" events.

// pyRequestTimeout bounds one request to pyrefly.
const pyRequestTimeout = 10 * time.Second

// pyRetryAfter is how long a pyrefly that failed to start isn't tried
// again, unless what it runs with changes.
const pyRetryAfter = 30 * time.Second

// errPythonOff is pythonLanguageServer set to "off".
var errPythonOff = errors.New("Python checking is off (pythonLanguageServer)")

// pyState is the project's pyrefly.
type pyState struct {
	mu     sync.Mutex
	client *pyrefly.Client
	// bin is the pyrefly client runs.
	bin string
	// err is why pyrefly last failed to start, at failedAt.
	err      error
	failedAt time.Time
	// files are the page's names of the files pyrefly has, by path: the
	// name relative to the root, or the path for a library file.
	files map[string]string
	// library are the files outside the project pyrefly named, which the
	// pane may show, read-only.
	library map[string]bool
	// lookPath finds pyrefly and the interpreters on PATH; tests change
	// it. nil is exec.LookPath.
	lookPath func(string) (string, error)
}

func (s *pyState) look() func(string) (string, error) {
	if s.lookPath != nil {
		return s.lookPath
	}
	return exec.LookPath
}

// stop stops pyrefly, if it runs.
func (s *pyState) stop() {
	s.mu.Lock()
	c := s.client
	s.client, s.bin, s.err = nil, "", nil
	s.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// pyInterpreter is the interpreter pyrefly runs with: the one picked, or
// the root's .venv or venv, or none ("" leaves it to pyrefly).
func (p *projectState) pyInterpreter() pyrefly.Interpreter {
	p.mu.Lock()
	picked := p.sess.Python
	p.mu.Unlock()
	if picked != "" {
		return pyrefly.Interpreter{Path: picked, Where: "picked"}
	}
	return pyrefly.DefaultInterpreter(p.deps.Root)
}

func (p *projectState) pythonOn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pythonServer != config.PythonOff
}

// pyClient is the project's pyrefly, started if it isn't running.
func (p *projectState) pyClient(ctx context.Context) (*pyrefly.Client, error) {
	if !p.pythonOn() {
		return nil, errPythonOff
	}
	interp := p.pyInterpreter()
	s := &p.py
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		select {
		case <-s.client.Done():
			s.client = nil // it exited: start another
		default:
			return s.client, nil
		}
	}
	bin, err := pyrefly.FindBin(interp.Path, s.look())
	if err != nil {
		return nil, err
	}
	if s.err != nil && bin == s.bin && time.Since(s.failedAt) < pyRetryAfter {
		return nil, s.err
	}
	c, err := pyrefly.Start(ctx, pyrefly.Options{
		Bin: bin, Root: p.deps.Root,
		Interpreter: func() string { return p.pyInterpreter().Path },
		Diagnostics: p.pyPublished,
	})
	s.bin = bin
	if err != nil {
		s.err, s.failedAt = err, time.Now()
		return nil, err
	}
	s.client, s.err = c, nil
	return c, nil
}

// pyPublished tells the pages pyrefly's errors in a file.
func (p *projectState) pyPublished(path string, version int, ds []pyrefly.Diagnostic) {
	p.py.mu.Lock()
	file := p.py.files[path]
	p.py.mu.Unlock()
	if file == "" {
		return // a file no page has open
	}
	p.hub.send("python", pythonEvent{File: file, Version: version, Diagnostics: ds})
}

// pythonEvent is pyrefly's errors in a file, for the version of its text
// a page sent.
type pythonEvent struct {
	File        string               `json:"file"`
	Version     int                  `json:"version"`
	Diagnostics []pyrefly.Diagnostic `json:"diagnostics"`
}

// isAbsFile says whether a page's name for a file is a path, as a library
// file's is, rather than one relative to the root.
func isAbsFile(file string) bool {
	return filepath.IsAbs(filepath.FromSlash(file)) || strings.HasPrefix(file, "/")
}

// libraryFile is the path of a file outside the project the pane may show:
// one pyrefly named.
func (p *projectState) libraryFile(file string) (string, bool) {
	if !isAbsFile(file) {
		return "", false
	}
	path := filepath.Clean(filepath.FromSlash(file))
	p.py.mu.Lock()
	ok := p.py.library[path]
	p.py.mu.Unlock()
	if !ok {
		return "", false
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// libraryWhy is why the pane can't save a file at path: it is an installed
// package's, or "" when it isn't.
func (p *projectState) libraryWhy(path string) string {
	root := evalRoot(p.deps.Root)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if !inside(root, path) {
		return "a library file, outside the project"
	}
	if pyrefly.InLibrary(root, path) {
		return "a library file, in a virtual environment"
	}
	return ""
}

func evalRoot(root string) string {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		return r
	}
	return root
}

// pyPath is where a page's Python file is: one in the project, or a
// library file pyrefly named.
func (p *projectState) pyPath(file string) (string, error) {
	if path, ok := p.libraryFile(file); ok {
		return path, nil
	}
	if langOf(file) != "python" {
		return "", errNotSource
	}
	if _, err := p.sourcePath(file); err != nil {
		return "", err
	}
	return filepath.Join(p.deps.Root, filepath.FromSlash(file)), nil
}

// pyRequest is a request about a Python file's unsaved text, at a place.
type pyRequest struct {
	File string           `json:"file"`
	Text string           `json:"text"`
	Pos  pyrefly.Position `json:"pos"`
}

// pyError answers a request pyrefly can't: 503 with why.
func pyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotSource) {
		sourceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(errorView{Error: err.Error(), Kind: "python"})
}

// pySynced reads a pyRequest, gives pyrefly its text, and answers with
// pyrefly, the file's path, and its text's version and errors.
func (p *projectState) pySynced(w http.ResponseWriter, r *http.Request, req *pyRequest) (c *pyrefly.Client, path string, sync syncView, ok bool) {
	if !readSourceJSON(w, r, req) {
		return nil, "", sync, false
	}
	path, err := p.pyPath(req.File)
	if err != nil {
		pyError(w, err)
		return nil, "", sync, false
	}
	c, err = p.pyClient(r.Context())
	if err != nil {
		pyError(w, err)
		return nil, "", sync, false
	}
	p.py.mu.Lock()
	if p.py.files == nil {
		p.py.files = map[string]string{}
	}
	p.py.files[path] = req.File
	p.py.mu.Unlock()
	v, ds, have, err := c.Sync(path, req.Text)
	if err != nil {
		pyError(w, err)
		return nil, "", sync, false
	}
	sync = syncView{Version: v}
	if have {
		sync.Diagnostics = ds
	}
	return c, path, sync, true
}

// syncView is a text's version, and pyrefly's errors in it when it has
// them already; else they come as a "python" event.
type syncView struct {
	Version     int                  `json:"version"`
	Diagnostics []pyrefly.Diagnostic `json:"diagnostics,omitempty"`
}

// pythonSync gives pyrefly a file's text.
func (p *projectState) pythonSync(w http.ResponseWriter, r *http.Request) {
	var req pyRequest
	if _, _, sync, ok := p.pySynced(w, r, &req); ok {
		writeJSON(w, sync)
	}
}

// pythonClose tells pyrefly a page no longer shows a file.
func (p *projectState) pythonClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	path, err := p.pyPath(req.File)
	if err != nil {
		writeJSON(w, okView{OK: true})
		return
	}
	p.py.mu.Lock()
	c := p.py.client
	delete(p.py.files, path)
	p.py.mu.Unlock()
	if c != nil {
		c.CloseFile(path)
	}
	writeJSON(w, okView{OK: true})
}

// hoverView is pyrefly's hover at a place: Markdown, the range it is of,
// and the files its links go to, by URI, as the page names them.
type hoverView struct {
	pyrefly.Hover
	Links   map[string]pyLocationView `json:"links,omitempty"`
	Version int                       `json:"version"`
}

// fileLink is a file URI in a hover's Markdown, with the place pyrefly
// puts after it: #L<line>,<col>, both from 1.
var fileLink = regexp.MustCompile(`\(file://[^)\s]+\)`)

// pythonHover answers with pyrefly's hover at the place; no Markdown for
// nothing. The files it links to may be shown from now on, as gd's.
func (p *projectState) pythonHover(w http.ResponseWriter, r *http.Request) {
	var req pyRequest
	c, path, sync, ok := p.pySynced(w, r, &req)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pyRequestTimeout)
	defer cancel()
	h, err := c.Hover(ctx, path, req.Pos)
	if err != nil {
		pyError(w, err)
		return
	}
	v := hoverView{Version: sync.Version}
	if h != nil {
		v.Hover = *h
		for _, m := range fileLink.FindAllString(h.Markdown, -1) {
			uri := m[1 : len(m)-1]
			target, place, _ := strings.Cut(uri, "#")
			lp := pyrefly.PathOf(target)
			if lp == "" {
				continue
			}
			loc := p.pageLocation(path, req.File, lp)
			if line, col, ok := strings.Cut(strings.TrimPrefix(place, "L"), ","); ok {
				loc.Line, _ = strconv.Atoi(line)
				loc.Col, _ = strconv.Atoi(col)
				loc.Line, loc.Col = max(loc.Line-1, 0), max(loc.Col-1, 0)
			}
			if v.Links == nil {
				v.Links = map[string]pyLocationView{}
			}
			v.Links[uri] = loc
		}
	}
	writeJSON(w, v)
}

// pageLocation is the start of file lp as the page names it, when pyrefly
// names it about the page's file (path, the page's name file): relative to
// the root for the project's own, else a library file's path, which the
// pane may show from now on.
func (p *projectState) pageLocation(path, file, lp string) pyLocationView {
	if lp == path {
		return pyLocationView{File: file, Library: isAbsFile(file)}
	}
	if rel, ok := p.relFile(lp); ok && p.libraryWhy(lp) == "" {
		return pyLocationView{File: rel}
	}
	p.py.mu.Lock()
	if p.py.library == nil {
		p.py.library = map[string]bool{}
	}
	p.py.library[filepath.Clean(lp)] = true
	p.py.mu.Unlock()
	return pyLocationView{File: filepath.ToSlash(lp), Library: true}
}

// pyLocationView is where something is defined: File is relative to the
// root, or a path with Library set for a file of an installed package
// (read-only in the pane); line counts from 0, col is UTF-16.
type pyLocationView struct {
	File    string `json:"file"`
	Library bool   `json:"library,omitempty"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
}

// pythonDefinition answers with where the symbol at the place is defined.
// Files outside the project it names may be shown, read-only, from now on.
func (p *projectState) pythonDefinition(w http.ResponseWriter, r *http.Request) {
	var req pyRequest
	c, path, sync, ok := p.pySynced(w, r, &req)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pyRequestTimeout)
	defer cancel()
	locs, err := c.Definition(ctx, path, req.Pos)
	if err != nil {
		pyError(w, err)
		return
	}
	out := []pyLocationView{}
	for _, l := range locs {
		v := p.pageLocation(path, req.File, l.Path)
		v.Line, v.Col = l.Range.Start.Line, l.Range.Start.Character
		out = append(out, v)
	}
	writeJSON(w, struct {
		Locations []pyLocationView `json:"locations"`
		Version   int              `json:"version"`
	}{out, sync.Version})
}

// relFile is path relative to the root, with forward slashes, when it is
// in the project (the root as given, or with its links followed).
func (p *projectState) relFile(path string) (string, bool) {
	for _, root := range []string{p.deps.Root, evalRoot(p.deps.Root)} {
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			if top, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); strings.EqualFold(top, ".git") {
				return "", false
			}
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// pythonComplete answers with what can be typed at the place.
func (p *projectState) pythonComplete(w http.ResponseWriter, r *http.Request) {
	var req pyRequest
	c, path, sync, ok := p.pySynced(w, r, &req)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pyRequestTimeout)
	defer cancel()
	items, incomplete, err := c.Complete(ctx, path, req.Pos)
	if err != nil {
		pyError(w, err)
		return
	}
	writeJSON(w, struct {
		Items      []pyrefly.Completion `json:"items"`
		Incomplete bool                 `json:"incomplete"`
		Version    int                  `json:"version"`
	}{items, incomplete, sync.Version})
}

// pythonSignature answers with the call being written at the place, null
// for none.
func (p *projectState) pythonSignature(w http.ResponseWriter, r *http.Request) {
	var req pyRequest
	c, path, sync, ok := p.pySynced(w, r, &req)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pyRequestTimeout)
	defer cancel()
	sig, err := c.SignatureHelp(ctx, path, req.Pos)
	if err != nil {
		pyError(w, err)
		return
	}
	writeJSON(w, struct {
		Signature *pyrefly.Signature `json:"signature"`
		Version   int                `json:"version"`
	}{sig, sync.Version})
}

// pythonStatusView is what the page shows of the project's pyrefly.
type pythonStatusView struct {
	Off bool `json:"off,omitempty"`
	// Missing is why pyrefly can't run: not found, or it failed to start.
	Missing string `json:"missing,omitempty"`
	// Interpreter is the Python pyrefly runs with, "" for pyrefly's own
	// choice, and Where says why: "picked", or where it was found.
	Interpreter string `json:"interpreter"`
	Where       string `json:"where"`
	// Label is how the status line names it (pyrefly.Label).
	Label string `json:"label,omitempty"`
	// Config is pyrefly's config file for the file, relative to the root
	// when it is in the project; "" for none.
	Config string `json:"config,omitempty"`
	// Offer says the page may offer to write a pyrefly.toml: there is no
	// config and the offer hasn't been turned down.
	Offer bool `json:"offer,omitempty"`
}

func (p *projectState) pythonStatusOf(file string) pythonStatusView {
	if !p.pythonOn() {
		return pythonStatusView{Off: true}
	}
	interp := p.pyInterpreter()
	v := pythonStatusView{Interpreter: interp.Path, Where: interp.Where, Label: pyrefly.Label(interp.Path)}
	if _, err := pyrefly.FindBin(interp.Path, p.py.look()); err != nil {
		v.Missing = err.Error()
	} else {
		p.py.mu.Lock()
		if p.py.err != nil {
			v.Missing = p.py.err.Error()
		}
		p.py.mu.Unlock()
	}
	dir := p.deps.Root
	library := false
	if path, err := p.pyPath(file); err == nil {
		dir = filepath.Dir(path)
		library = p.libraryWhy(path) != ""
	}
	if cfg := pyrefly.ConfigFile(dir); cfg != "" {
		v.Config = filepath.ToSlash(cfg)
		if rel, ok := p.relFile(cfg); ok {
			v.Config = rel
		}
	}
	p.mu.Lock()
	declined := p.sess.PyreflyDeclined
	p.mu.Unlock()
	v.Offer = v.Config == "" && !declined && !library && v.Missing == ""
	return v
}

// pythonStatus answers with the project's pyrefly, for ?file.
func (p *projectState) pythonStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, p.pythonStatusOf(r.URL.Query().Get("file")))
}

// pythonInterpreters answers with the Pythons the picker lists, and the
// one in use.
func (p *projectState) pythonInterpreters(w http.ResponseWriter, r *http.Request) {
	interp := p.pyInterpreter()
	list := pyrefly.Interpreters(p.deps.Root, os.Getenv, p.py.look())
	if interp.Path != "" && !containsInterpreter(list, interp.Path) {
		list = append([]pyrefly.Interpreter{interp}, list...)
	}
	writeJSON(w, struct {
		Current string                `json:"current"`
		Where   string                `json:"where"`
		List    []pyrefly.Interpreter `json:"list"`
	}{interp.Path, interp.Where, list})
}

func containsInterpreter(list []pyrefly.Interpreter, path string) bool {
	for _, i := range list {
		if i.Path == path {
			return true
		}
	}
	return false
}

// pythonSetInterpreter keeps the interpreter picked for the project, ""
// for the one xf finds, and has pyrefly take it: it asks again, or starts
// again when the new interpreter's environment has its own pyrefly.
func (p *projectState) pythonSetInterpreter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	path := strings.TrimSpace(req.Path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.deps.Root, path)
		}
		path = filepath.Clean(path)
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			writeError(w, errors.New(req.Path+" is not a Python interpreter: no such file"))
			return
		}
	}
	p.mu.Lock()
	p.sess.Python = path
	p.mu.Unlock()
	interp := p.pyInterpreter()
	s := &p.py
	s.mu.Lock()
	c, running := s.client, s.bin
	s.err = nil
	s.mu.Unlock()
	if c != nil {
		if bin, err := pyrefly.FindBin(interp.Path, s.look()); err == nil && bin != running {
			s.stop()
		} else {
			c.ConfigChanged()
		}
	}
	writeJSON(w, p.pythonStatusOf(""))
}

// pythonConfig writes the project's pyrefly.toml (create), or keeps that
// the offer was turned down, so it isn't made again. pyrefly starts again
// to read a new config.
func (p *projectState) pythonConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Create bool `json:"create"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Create {
		p.mu.Lock()
		p.sess.PyreflyDeclined = true
		p.mu.Unlock()
		writeJSON(w, okView{OK: true})
		return
	}
	path, err := pyrefly.WriteConfig(p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	p.py.stop()
	p.hub.send("files", struct{}{})
	rel, _ := p.relFile(path)
	writeJSON(w, okView{OK: true, Message: "wrote " + rel})
}

func pythonRoutes(route func(string, func(*projectState, http.ResponseWriter, *http.Request))) {
	route("GET /api/python/status", (*projectState).pythonStatus)
	route("POST /api/python/sync", (*projectState).pythonSync)
	route("POST /api/python/close", (*projectState).pythonClose)
	route("POST /api/python/hover", (*projectState).pythonHover)
	route("POST /api/python/definition", (*projectState).pythonDefinition)
	route("POST /api/python/complete", (*projectState).pythonComplete)
	route("POST /api/python/signature", (*projectState).pythonSignature)
	route("GET /api/python/interpreters", (*projectState).pythonInterpreters)
	route("POST /api/python/interpreter", (*projectState).pythonSetInterpreter)
	route("POST /api/python/config", (*projectState).pythonConfig)
}
