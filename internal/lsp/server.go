// Package lsp is a small Language Server Protocol server for Dataform and
// dbt projects, run as `xf lsp`. It offers go-to-definition (see package
// navigate), hovers, completion and diagnostics, over a project engine (see
// package engine) that compiles on start and whenever project files change.
package lsp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// Engine is the project engine the server works from: its own
// (*engine.Engine) or one shared with other processes (*remote.Client).
type Engine interface {
	Subscribe() (<-chan engine.State, func())
	State() engine.State
	FirstCompile() <-chan struct{}
	DryRun(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)
}

// Config wires the server to the outside world.
type Config struct {
	// Connect attaches to the engine shared by every xf for the project at
	// root (see package remote). When it is nil or fails, the server runs
	// its own engine from Compile, Watch and DryRun.
	Connect func(ctx context.Context, root string) (Engine, error)
	// Compile compiles the project at root. Nil, or NoCompile returning an
	// error for root, means ref() jumps are off; the error says why.
	Compile   func(ctx context.Context, root string) (*project.Graph, error)
	NoCompile func(root string) error
	// Watch reports changes to the project's files, as backend.Backend's
	// Watch does. Nil means compile once only.
	Watch func(ctx context.Context, root string) (<-chan struct{}, error)
	// Tables fetches a table's BigQuery metadata for hover cards. Nil means
	// cards show only what the compile knows.
	Tables func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error)
	// DryRun asks BigQuery to plan a query in a job in project, for
	// diagnostics and cost hints. Nil means no dry runs.
	DryRun func(ctx context.Context, project, location, query string) (*bigquery.DryRun, error)
	// Credentials is the credentials' generation, for an engine of the
	// server's own (engine.Config.Credentials).
	Credentials func() uint64
	// HoverTimeout bounds the BigQuery calls of one hover; 0 means
	// defaultHoverTimeout.
	HoverTimeout time.Duration
	// Log receives diagnostics about the server itself. Nil discards them.
	Log io.Writer
	// Dbt reports whether the project at root is a dbt project, whose SQL
	// calls ref(), source() and macros in Jinja. Its dry-run errors are
	// placed by lining the SQL between its Jinja tags up with the compiled
	// SQL (see navigate.DbtRegion). Nil means Dataform.
	Dbt func(root string) bool
	// Format formats a Dataform file's SQL (package format): the text of
	// the file at path, in the project at root, formatted. Nil means the
	// server doesn't format.
	Format func(ctx context.Context, root, path string, text []byte) ([]byte, error)
}

const defaultHoverTimeout = 5 * time.Second

// firstCompileWait bounds how long a definition request waits for the first
// compile, so a hung dataform cannot hang the editor.
const firstCompileWait = 30 * time.Second

// errExitWithoutShutdown is returned when the client sends exit without
// shutting down first; the LSP spec asks for a non-zero exit code.
var errExitWithoutShutdown = errors.New("exit without shutdown")

// Serve answers LSP messages from in on out until the client exits or in
// ends.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logw := cfg.Log
	if logw == nil {
		logw = io.Discard
	}
	s := &server{
		cfg:   cfg,
		conn:  newConn(in, out),
		log:   log.New(logw, "xf lsp: ", log.LstdFlags),
		docs:  map[string][]byte{},
		ready: make(chan struct{}),
		diag:  diagState{files: map[string]*fileDiagnostics{}, open: map[string]bool{}, compileErrors: map[string]bool{}},
	}
	for {
		m, err := s.conn.read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if m.IsResponse() {
			s.conn.deliver(m)
			continue
		}
		if m.Method == "exit" {
			if !s.shutdown {
				return errExitWithoutShutdown
			}
			return nil
		}
		if err := s.handle(ctx, m); err != nil {
			return err
		}
	}
}

type server struct {
	cfg  Config
	conn *conn
	log  *log.Logger

	// Set by initialize.
	root  string
	utf16 bool
	dbt   bool
	// showDocs says the editor opens files the server asks it to
	// (window/showDocument).
	showDocs bool

	// eng is set once the engine is up, which closes ready.
	engMu sync.Mutex
	eng   Engine
	// pendingActive is the latest xf/activeFile that came before the
	// engine was up: the first file an editor opens usually does, and is
	// passed on once it is.
	pendingActive *ActiveFileParams
	ready         chan struct{}

	// docs holds the text of the buffers the client has open, by absolute
	// path. Only the message loop touches it.
	docs     map[string][]byte
	shutdown bool

	diagMu sync.Mutex
	diag   diagState
}

func (s *server) handle(ctx context.Context, m *message) error {
	if s.shutdown && m.IsRequest() {
		return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "server is shut down"})
	}
	switch m.Method {
	case "initialize":
		var p InitializeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		return s.conn.reply(m.ID, s.initialize(p), nil)
	case "initialized":
		if s.root != "" {
			go s.followCompiles(ctx)
		}
	case methodActiveFile:
		var p ActiveFileParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.activeFile(p)
		}
	case "shutdown":
		s.shutdown = true
		return s.conn.reply(m.ID, nil, nil)
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.setDoc(p.TextDocument.URI, p.TextDocument.Text)
			if path := uriPath(p.TextDocument.URI); path != "" {
				s.opened(path)
			}
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil && len(p.ContentChanges) > 0 {
			// Full sync: the last change holds the whole text.
			s.setDoc(p.TextDocument.URI, p.ContentChanges[len(p.ContentChanges)-1].Text)
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil {
			path := uriPath(p.TextDocument.URI)
			delete(s.docs, path)
			s.closed(path)
		}
	case "textDocument/definition":
		var p TextDocumentPositionParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		return s.conn.reply(m.ID, s.definition(ctx, p), nil)
	case "textDocument/hover":
		var p TextDocumentPositionParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		// BigQuery can be slow, so answer off the message loop; take the
		// buffer text now, while only the loop touches docs.
		path := uriPath(p.TextDocument.URI)
		text, err := s.readFile(path)
		if path == "" || err != nil {
			return s.conn.reply(m.ID, nil, nil)
		}
		go func() {
			if err := s.conn.reply(m.ID, s.hover(ctx, path, text, p.Position), nil); err != nil {
				s.log.Printf("hover reply: %v", err)
			}
		}()
	case "textDocument/completion":
		var p TextDocumentPositionParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		path := uriPath(p.TextDocument.URI)
		text, err := s.readFile(path)
		if path == "" || err != nil {
			return s.conn.reply(m.ID, CompletionList{Items: []CompletionItem{}}, nil)
		}
		s.awaitFirstCompile(ctx) // ref() names come from the compile, as for definition
		return s.conn.reply(m.ID, s.completion(path, text, p.Position), nil)
	case "textDocument/codeAction":
		var p CodeActionParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		return s.conn.reply(m.ID, codeActions(p), nil)
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		path := uriPath(p.TextDocument.URI)
		text, err := s.readFile(path)
		if path == "" || err != nil || s.cfg.Format == nil || s.dbt {
			return s.conn.reply(m.ID, nil, nil)
		}
		// sqlfluff takes a moment: answer off the message loop, with the
		// text taken now, while only the loop touches docs.
		go func() {
			edits, err := s.format(ctx, path, text)
			var rerr *rpcError
			if err != nil {
				rerr = &rpcError{Code: codeRequestFailed, Message: err.Error()}
			}
			if err := s.conn.reply(m.ID, edits, rerr); err != nil {
				s.log.Printf("formatting reply: %v", err)
			}
		}()
	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		return s.conn.reply(m.ID, s.documentSymbols(p), nil)
	default:
		if m.IsRequest() {
			return s.conn.reply(m.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + m.Method})
		}
	}
	return nil
}

func (s *server) initialize(p InitializeParams) InitializeResult {
	root := uriPath(p.RootURI)
	if root == "" && len(p.WorkspaceFolders) > 0 {
		root = uriPath(p.WorkspaceFolders[0].URI)
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	s.root = root

	var res InitializeResult
	res.ServerInfo.Name = "xf"
	res.Capabilities.Experimental = &Experimental{XfActiveFile: true, XfDbt: true}
	res.Capabilities.PositionEncoding = encodingUTF16
	if slices.Contains(p.Capabilities.General.PositionEncodings, encodingUTF8) {
		res.Capabilities.PositionEncoding = encodingUTF8
	}
	s.utf16 = res.Capabilities.PositionEncoding == encodingUTF16
	s.showDocs = p.Capabilities.Window.ShowDocument.Support
	s.dbt = s.cfg.Dbt != nil && s.cfg.Dbt(root)
	res.Capabilities.TextDocumentSync = syncFull
	res.Capabilities.DefinitionProvider = true
	res.Capabilities.DocumentSymbolProvider = true
	res.Capabilities.HoverProvider = true
	res.Capabilities.DocumentFormattingProvider = s.cfg.Format != nil && !s.dbt
	res.Capabilities.CodeActionProvider = true
	if s.dbt {
		// { for {{, . for package.macro, quotes for ref('…').
		res.Capabilities.CompletionProvider = &CompletionOptions{TriggerCharacters: []string{"{", ".", `"`, "'"}}
	} else {
		res.Capabilities.CompletionProvider = &CompletionOptions{TriggerCharacters: []string{"$", `"`, "'"}}
	}
	kind := "Dataform"
	if s.dbt {
		kind = "dbt"
	}
	s.log.Printf("%s project root %s, %s positions", kind, s.root, res.Capabilities.PositionEncoding)
	return res
}

func (s *server) setDoc(uri, text string) {
	if path := uriPath(uri); path != "" {
		s.docs[path] = []byte(text)
	}
}

// readFile prefers the client's open buffer over the file on disk.
func (s *server) readFile(path string) ([]byte, error) {
	if text, ok := s.docs[path]; ok {
		return text, nil
	}
	return os.ReadFile(path)
}

// definition answers textDocument/definition. A nil result encodes as null,
// which clients show as "no locations found".
func (s *server) definition(ctx context.Context, p TextDocumentPositionParams) []Location {
	path := uriPath(p.TextDocument.URI)
	if path == "" {
		return nil
	}
	text, err := s.readFile(path)
	if err != nil {
		return nil
	}
	s.awaitFirstCompile(ctx)
	proj := navigate.Project{Root: s.root, Graph: s.currentGraph(), ReadFile: s.readFile, Dbt: s.dbt}
	var out []Location
	for _, l := range navigate.Definition(proj, path, text, byteOffset(text, p.Position, s.utf16)) {
		pos := Position{Line: l.Line}
		if l.Col > 0 {
			target, err := s.readFile(l.File)
			if err == nil {
				pos.Character = character(lineAt(target, l.Line), l.Col, s.utf16)
			}
		}
		out = append(out, Location{URI: pathURI(l.File), Range: Range{Start: pos, End: pos}})
	}
	return out
}

// documentSymbols answers textDocument/documentSymbol with the file's
// outline as a flat list: the refs and tables it reads, then its CTEs.
func (s *server) documentSymbols(p DocumentSymbolParams) []DocumentSymbol {
	path := uriPath(p.TextDocument.URI)
	text, err := s.readFile(path)
	if path == "" || err != nil {
		return []DocumentSymbol{}
	}
	syms := navigate.DocumentSymbols(path, text)
	if s.dbt {
		syms = navigate.DbtDocumentSymbols(path, text)
	}
	return s.toDocumentSymbols(text, navigate.ReferencesFirst(syms))
}

func (s *server) toDocumentSymbols(text []byte, syms []navigate.Symbol) []DocumentSymbol {
	out := make([]DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		kind := symbolKindVariable
		switch sym.Kind {
		case navigate.SymbolCTE:
			kind = symbolKindStruct
		case navigate.SymbolMacro:
			kind = symbolKindFunction
		}
		out = append(out, DocumentSymbol{
			Name:           sym.Name,
			Detail:         sym.Kind.Detail(),
			Kind:           kind,
			Range:          Range{Start: positionAt(text, sym.Start, s.utf16), End: positionAt(text, sym.End, s.utf16)},
			SelectionRange: Range{Start: positionAt(text, sym.SelStart, s.utf16), End: positionAt(text, sym.SelEnd, s.utf16)},
			Children:       s.toDocumentSymbols(text, sym.Children),
		})
	}
	return out
}

// hover answers textDocument/hover with a card for each table referred to
// at pos, or each macro a dbt macro call may run. It runs on its own
// goroutine: it must not touch docs.
func (s *server) hover(ctx context.Context, path string, text []byte, pos Position) *Hover {
	s.awaitFirstCompile(ctx)
	proj := navigate.Project{Root: s.root, Graph: s.currentGraph(), Dbt: s.dbt}
	off := byteOffset(text, pos, s.utf16)
	if macros := navigate.MacrosAt(proj, path, text, off); len(macros) > 0 {
		cards := make([]string, len(macros))
		for i, m := range macros {
			cards[i] = macroCard(m)
		}
		r := Range{Start: positionAt(text, macros[0].Start, s.utf16), End: positionAt(text, macros[0].End, s.utf16)}
		return &Hover{Contents: MarkupContent{Kind: "markdown", Value: strings.Join(cards, "\n\n---\n\n")}, Range: &r}
	}
	refs := navigate.TableRefsAt(proj, path, text, off)
	if len(refs) == 0 {
		return nil
	}
	timeout := cmp.Or(s.cfg.HoverTimeout, defaultHoverTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cards := make([]string, len(refs))
	for i, ref := range refs {
		var tbl *bigquery.Table
		var err error
		// An ephemeral model, a test or an operation without hasOutput has
		// no table to look up.
		if ref.Source != navigate.SourceSelf && s.cfg.Tables != nil && (ref.Action == nil || hasOutputTable(ref.Action)) {
			t := ref.Target
			if tbl, err = s.cfg.Tables(ctx, t.Database, t.Schema, t.Name); err != nil {
				s.log.Printf("table metadata for %s: %v", t.ID(), err)
			}
		}
		cards[i] = card(ref, tbl, err, timeout)
	}
	r := Range{Start: positionAt(text, refs[0].Start, s.utf16), End: positionAt(text, refs[0].End, s.utf16)}
	return &Hover{Contents: MarkupContent{Kind: "markdown", Value: strings.Join(cards, "\n\n---\n\n")}, Range: &r}
}

// methodActiveFile is how the nvim plugin says which file it made active;
// the server passes it on to the other xf processes for the project.
const methodActiveFile = "xf/activeFile"

// ActiveFileParams are xf/activeFile's: the autocmd and the file's
// absolute path.
type ActiveFileParams struct {
	Event string `json:"event"`
	File  string `json:"file"`
}

// editorFollower is an Engine that passes the editor's active file on.
type editorFollower interface {
	ActiveFile(event, file string)
}

// documentShower is an Engine through which other xf processes have the
// editor open files: the TUI's o key.
type documentShower interface {
	HandleShowDocument(open func(ctx context.Context, file string, line int) error)
}

// showDocumentWait bounds how long the server waits for the editor to open
// a file.
const showDocumentWait = 10 * time.Second

// showDocument asks the editor to open file (an absolute path) at a
// 0-based line, and focus it.
func (s *server) showDocument(ctx context.Context, file string, line int) error {
	ctx, cancel := context.WithTimeout(ctx, showDocumentWait)
	defer cancel()
	at := Position{Line: line}
	m, err := s.conn.call(ctx, "window/showDocument", ShowDocumentParams{URI: pathURI(file), TakeFocus: true, Selection: &Range{Start: at, End: at}})
	if err != nil {
		return err
	}
	if m.Error != nil {
		return errors.New(m.Error.Message)
	}
	var res ShowDocumentResult
	if err := json.Unmarshal(m.Result, &res); err != nil || !res.Success {
		return fmt.Errorf("the editor didn't open %s", file)
	}
	return nil
}

// activeFile passes the editor's active file on to the engine, or keeps
// it for when the engine is up.
func (s *server) activeFile(p ActiveFileParams) {
	s.engMu.Lock()
	eng := s.eng
	if eng == nil {
		s.pendingActive = &p
	}
	s.engMu.Unlock()
	if f, ok := eng.(editorFollower); ok {
		f.ActiveFile(p.Event, p.File)
	}
}

// engine returns the engine, or nil until it is up.
func (s *server) engine() Engine {
	s.engMu.Lock()
	defer s.engMu.Unlock()
	return s.eng
}

// startEngine attaches to the shared engine or, failing that, starts one
// of the server's own. It returns why the engine can't compile, or nil.
func (s *server) startEngine(ctx context.Context) (Engine, error) {
	if s.cfg.Connect != nil {
		eng, err := s.cfg.Connect(ctx, s.root)
		if err == nil {
			s.log.Printf("attached to the shared engine")
			return eng, nil
		}
		s.log.Printf("running an engine of my own: %v", err)
	}
	ec := engine.Config{DryRun: s.cfg.DryRun, Credentials: s.cfg.Credentials}
	var noCompile error
	switch {
	case s.cfg.Compile == nil:
		noCompile = errors.New("nothing to compile with")
	case s.cfg.NoCompile != nil:
		noCompile = s.cfg.NoCompile(s.root)
	}
	if noCompile == nil {
		ec.Compile = func(ctx context.Context) (*project.Graph, error) { return s.cfg.Compile(ctx, s.root) }
	}
	if s.cfg.Watch != nil {
		ec.Watch = func(ctx context.Context) (<-chan struct{}, error) { return s.cfg.Watch(ctx, s.root) }
	}
	eng := engine.New(ec)
	if err := eng.Start(ctx); err != nil {
		s.log.Printf("not watching files, compiling once only: %v", err)
	}
	return eng, noCompile
}

// awaitFirstCompile waits, briefly, for the first compile, so a jump asked
// for while the server is starting up gets an answer instead of nothing.
func (s *server) awaitFirstCompile(ctx context.Context) {
	timeout := time.After(firstCompileWait)
	select {
	case <-s.ready:
	case <-ctx.Done():
		return
	case <-timeout:
		return
	}
	select {
	case <-s.engine().FirstCompile():
	case <-ctx.Done():
	case <-timeout:
	}
}

// currentGraph is the last good compile, or nil.
func (s *server) currentGraph() *project.Graph {
	if eng := s.engine(); eng != nil {
		return eng.State().Graph
	}
	return nil
}

// followCompiles brings the engine up and reports each compile it finishes.
func (s *server) followCompiles(ctx context.Context) {
	eng, noCompile := s.startEngine(ctx)
	if c, ok := eng.(interface{ Close() }); ok {
		defer c.Close()
	}
	states, cancel := eng.Subscribe()
	defer cancel()
	s.engMu.Lock()
	s.eng = eng
	pending := s.pendingActive
	s.pendingActive = nil
	s.engMu.Unlock()
	// Before the active file goes on, so the editor can be asked to open
	// files as soon as it is the one other xf ask.
	if d, ok := eng.(documentShower); ok && s.showDocs {
		d.HandleShowDocument(s.showDocument)
	}
	if f, ok := eng.(editorFollower); ok && pending != nil {
		f.ActiveFile(pending.Event, pending.File)
	}
	close(s.ready)
	if noCompile != nil {
		s.warn(fmt.Sprintf("ref() jumps are off: %v", noCompile))
		return
	}
	seen, warned, deferOn := 0, false, false
	for {
		var st engine.State
		select {
		case <-ctx.Done():
			return
		case st = <-states:
		}
		if st.Compiles == seen {
			// Turning defer to prod on or off changes what is dry run.
			if st.Defer != deferOn && st.Graph != nil && st.Err == nil {
				deferOn = st.Defer
				s.afterCompile(ctx, st)
			}
			continue
		}
		seen, deferOn = st.Compiles, st.Defer
		if st.Err != nil {
			s.log.Printf("compile failed: %v", st.Err)
			if !warned && st.Graph == nil {
				s.warn(fmt.Sprintf("ref() jumps are off until the project compiles: %v", st.Err))
				warned = true
			}
			continue
		}
		if st.Stale {
			s.log.Printf("compile has %d errors; keeping the last good graph", len(st.Errors))
		} else {
			s.log.Printf("compiled %d actions", len(st.Graph.Actions))
		}
		s.afterCompile(ctx, st)
	}
}

// warn shows a message in the editor.
func (s *server) warn(msg string) {
	s.log.Print(msg)
	if err := s.conn.notify("window/showMessage", ShowMessageParams{Type: messageWarning, Message: "xf: " + msg}); err != nil {
		s.log.Printf("show message: %v", err)
	}
}
