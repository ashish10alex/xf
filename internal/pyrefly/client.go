// Package pyrefly runs pyrefly's language server (`pyrefly lsp`) for xf
// web's source pane, and finds what it runs with: pyrefly itself, the
// project's Python interpreters, and its config (ADR 0018).
//
// A Client is one running server for one project. The pane's unsaved text
// is sent with Sync before each request about it; pyrefly's errors arrive
// as it publishes them, through Options.Diagnostics. pyrefly asks for the
// interpreter as VS Code's Python extension answers it, the "python"
// section's pythonPath, which ConfigChanged has it ask for again.
package pyrefly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/jsonrpc"
)

// Options are what a Client runs with.
type Options struct {
	// Bin is the pyrefly to run.
	Bin string
	// Root is the project's root, pyrefly's workspace folder.
	Root string
	// Interpreter is the pythonPath pyrefly is given when it asks: ""
	// leaves the choice to pyrefly. nil is "".
	Interpreter func() string
	// Diagnostics is told a file's errors each time pyrefly publishes
	// them, for the version of the text Sync gave it (0 when pyrefly
	// doesn't say). It is called from the Client's own goroutine, with no
	// lock held. nil tells no one.
	Diagnostics func(path string, version int, ds []Diagnostic)
	// Stderr gets pyrefly's log; nil drops it.
	Stderr io.Writer
}

// startTimeout bounds pyrefly's answer to initialize.
const startTimeout = 20 * time.Second

// stopTimeout bounds how long Stop waits for pyrefly to exit by itself.
const stopTimeout = 2 * time.Second

// ErrStopped is a request to a Client that has stopped, or whose pyrefly
// exited.
var ErrStopped = errors.New("pyrefly stopped")

// Client is a running `pyrefly lsp`.
type Client struct {
	opts   Options
	conn   *jsonrpc.Conn
	cmd    *cmdrun.Cmd
	stdin  io.Closer
	cancel context.CancelFunc
	nextID atomic.Int64

	done     chan struct{} // closed once pyrefly exited, or reading it failed
	doneErr  error
	stopOnce sync.Once

	// syncMu orders the files' texts as they are sent; mu guards the
	// maps, and is never held while writing to pyrefly.
	syncMu  sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan *jsonrpc.Message
	docs    map[string]*doc
}

// doc is a file pyrefly was given the text of: the last text, its version
// and the errors published for it.
type doc struct {
	text    string
	version int
	diagVer int
	diags   []Diagnostic
	got     bool
}

// Start runs pyrefly's language server in o.Root and waits for it to
// answer initialize.
func Start(ctx context.Context, o Options) (*Client, error) {
	if o.Interpreter == nil {
		o.Interpreter = func() string { return "" }
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := cmdrun.Command(runCtx, o.Bin, "lsp")
	cmd.Dir = o.Root
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	c := &Client{opts: o, conn: jsonrpc.NewConn(stdout, stdin), cmd: cmd, stdin: stdin, cancel: cancel,
		done: make(chan struct{}), pending: map[int64]chan *jsonrpc.Message{}, docs: map[string]*doc{}}
	go c.read()
	ictx, icancel := context.WithTimeout(ctx, startTimeout)
	defer icancel()
	if err := c.call(ictx, "initialize", initializeParams(o.Root), nil); err != nil {
		c.Stop()
		return nil, fmt.Errorf("pyrefly didn't start: %w", err)
	}
	if err := c.conn.Notify("initialized", struct{}{}); err != nil {
		c.Stop()
		return nil, err
	}
	return c, nil
}

func initializeParams(root string) any {
	md := []string{"markdown", "plaintext"}
	return map[string]any{
		"processId":        nil,
		"rootUri":          URI(root),
		"workspaceFolders": []any{map[string]any{"uri": URI(root), "name": "project"}},
		"clientInfo":       map[string]any{"name": "xf web"},
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"workspace": map[string]any{
				"configuration":          true,
				"workspaceFolders":       true,
				"didChangeConfiguration": map[string]any{"dynamicRegistration": false},
			},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": false, "dynamicRegistration": false},
				"publishDiagnostics": map[string]any{"versionSupport": true},
				"hover":              map[string]any{"contentFormat": md},
				"definition":         map[string]any{"linkSupport": true},
				"completion": map[string]any{
					"completionItem": map[string]any{"snippetSupport": false, "documentationFormat": md, "labelDetailsSupport": true},
				},
				"signatureHelp": map[string]any{
					"signatureInformation": map[string]any{
						"documentationFormat":    md,
						"parameterInformation":   map[string]any{"labelOffsetSupport": true},
						"activeParameterSupport": true,
					},
				},
			},
		},
	}
}

// Done is closed once pyrefly has exited.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why pyrefly exited, once Done is closed.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.doneErr
	default:
		return nil
	}
}

// Stop asks pyrefly to shut down, and stops it if it doesn't soon.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		select {
		case <-c.done:
		default:
			ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			if c.call(ctx, "shutdown", nil, nil) == nil {
				_ = c.conn.Notify("exit", nil)
			}
			cancel()
			_ = c.stdin.Close()
			select {
			case <-c.done:
			case <-time.After(stopTimeout):
			}
		}
		c.cancel()
		_ = c.cmd.Wait()
	})
}

// read reads pyrefly's messages until it exits.
func (c *Client) read() {
	var err error
	for {
		var m *jsonrpc.Message
		if m, err = c.conn.Read(); err != nil {
			break
		}
		switch {
		case m.IsResponse():
			id, _ := strconv.ParseInt(string(m.ID), 10, 64)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.IsRequest():
			c.answer(m)
		case m.Method == "textDocument/publishDiagnostics":
			c.published(m.Params)
		}
	}
	if errors.Is(err, io.EOF) {
		err = ErrStopped
	}
	c.mu.Lock()
	c.doneErr = err
	pending := c.pending
	c.pending = map[int64]chan *jsonrpc.Message{}
	c.mu.Unlock()
	close(c.done)
	for _, ch := range pending {
		close(ch)
	}
	go c.Stop() // reaps a pyrefly that exited by itself
}

// answer answers a request pyrefly makes of xf: the interpreter, and
// null to the rest (registering capabilities, progress tokens).
func (c *Client) answer(m *jsonrpc.Message) {
	if m.Method != "workspace/configuration" {
		_ = c.conn.Reply(m.ID, nil, nil)
		return
	}
	var p struct {
		Items []struct {
			Section string `json:"section"`
		} `json:"items"`
	}
	_ = json.Unmarshal(m.Params, &p)
	out := make([]any, len(p.Items))
	for i, it := range p.Items {
		if it.Section != "python" {
			continue
		}
		if path := c.opts.Interpreter(); path != "" {
			out[i] = map[string]any{"pythonPath": path}
		} else {
			out[i] = map[string]any{}
		}
	}
	_ = c.conn.Reply(m.ID, out, nil)
}

func (c *Client) published(raw json.RawMessage) {
	var p publishParams
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	path := PathOf(p.URI)
	if path == "" {
		return
	}
	ds := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, d := range p.Diagnostics {
		ds = append(ds, d.diagnostic())
	}
	version := 0
	if p.Version != nil {
		version = *p.Version
	}
	c.mu.Lock()
	if d := c.docs[path]; d != nil {
		if version == 0 {
			version = d.version
		}
		d.diagVer, d.diags, d.got = version, ds, true
	}
	c.mu.Unlock()
	if c.opts.Diagnostics != nil {
		c.opts.Diagnostics(path, version, ds)
	}
}

// call sends a request and decodes its result into out (nil drops it).
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan *jsonrpc.Message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return ErrStopped
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.conn.Call(id, method, params); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrStopped
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = c.conn.Notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	}
}

// Sync gives pyrefly path's text, opening the file the first time and
// sending the whole text again when it changed. It answers with the text's
// version, and the errors pyrefly has published for that version, if it
// has.
func (c *Client) Sync(path, text string) (version int, ds []Diagnostic, have bool, err error) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	select {
	case <-c.done:
		return 0, nil, false, ErrStopped
	default:
	}
	c.mu.Lock()
	d := c.docs[path]
	var send func() error
	switch {
	case d == nil:
		d = &doc{text: text, version: 1}
		c.docs[path] = d
		send = func() error {
			return c.conn.Notify("textDocument/didOpen", map[string]any{
				"textDocument": map[string]any{"uri": URI(path), "languageId": "python", "version": 1, "text": text},
			})
		}
	case d.text != text:
		d.version++
		d.text = text
		v := d.version
		send = func() error {
			return c.conn.Notify("textDocument/didChange", map[string]any{
				"textDocument":   map[string]any{"uri": URI(path), "version": v},
				"contentChanges": []any{map[string]any{"text": text}},
			})
		}
	}
	version = d.version
	if d.got && d.diagVer == d.version {
		ds, have = append([]Diagnostic(nil), d.diags...), true
	}
	c.mu.Unlock()
	if send != nil {
		if err := send(); err != nil {
			return 0, nil, false, err
		}
	}
	return version, ds, have, nil
}

// CloseFile tells pyrefly the file is no longer open: it reads it from
// disk again.
func (c *Client) CloseFile(path string) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.mu.Lock()
	open := c.docs[path] != nil
	delete(c.docs, path)
	c.mu.Unlock()
	if open {
		_ = c.conn.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": URI(path)}})
	}
}

// Open are the files pyrefly has open.
func (c *Client) Open() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.docs))
	for p := range c.docs {
		out = append(out, p)
	}
	return out
}

// ConfigChanged has pyrefly ask for the interpreter again.
func (c *Client) ConfigChanged() {
	_ = c.conn.Notify("workspace/didChangeConfiguration", map[string]any{"settings": map[string]any{}})
}

func at(path string, pos Position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": URI(path)}, "position": pos}
}

// Hover is what pyrefly says of the symbol at pos in path; nil for nothing.
func (c *Client) Hover(ctx context.Context, path string, pos Position) (*Hover, error) {
	var res *struct {
		Contents json.RawMessage `json:"contents"`
		Range    *Range          `json:"range"`
	}
	if err := c.call(ctx, "textDocument/hover", at(path, pos), &res); err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	md := markdownOf(res.Contents)
	if md == "" {
		return nil, nil
	}
	return &Hover{Markdown: md, Range: res.Range}, nil
}

// Definition is where the symbol at pos in path is defined.
func (c *Client) Definition(ctx context.Context, path string, pos Position) ([]Location, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/definition", at(path, pos), &raw); err != nil {
		return nil, err
	}
	return locationsOf(raw), nil
}

// locationsOf reads a definition's answer: a Location, a list of them, or
// a list of LocationLinks.
func locationsOf(raw json.RawMessage) []Location {
	type loc struct {
		URI                  string `json:"uri"`
		Range                *Range `json:"range"`
		TargetURI            string `json:"targetUri"`
		TargetSelectionRange *Range `json:"targetSelectionRange"`
		TargetRange          *Range `json:"targetRange"`
	}
	var list []loc
	if json.Unmarshal(raw, &list) != nil {
		var one loc
		if json.Unmarshal(raw, &one) != nil || (one.URI == "" && one.TargetURI == "") {
			return nil
		}
		list = []loc{one}
	}
	out := []Location{}
	for _, l := range list {
		uri, r := l.URI, l.Range
		if l.TargetURI != "" {
			uri, r = l.TargetURI, l.TargetSelectionRange
			if r == nil {
				r = l.TargetRange
			}
		}
		path := PathOf(uri)
		if path == "" || r == nil {
			continue
		}
		out = append(out, Location{Path: path, Range: *r})
	}
	return out
}

// Complete is what can be typed at pos in path, and whether pyrefly said
// the list is incomplete (typing more asks again).
func (c *Client) Complete(ctx context.Context, path string, pos Position) ([]Completion, bool, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/completion", at(path, pos), &raw); err != nil {
		return nil, false, err
	}
	var items []lspCompletionItem
	incomplete := false
	if json.Unmarshal(raw, &items) != nil {
		var list struct {
			IsIncomplete bool                `json:"isIncomplete"`
			Items        []lspCompletionItem `json:"items"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, false, nil
		}
		items, incomplete = list.Items, list.IsIncomplete
	}
	out := make([]Completion, 0, len(items))
	for _, it := range items {
		out = append(out, it.completion())
	}
	return out, incomplete, nil
}

// SignatureHelp is the call being written at pos in path; nil for none.
func (c *Client) SignatureHelp(ctx context.Context, path string, pos Position) (*Signature, error) {
	var res *struct {
		Signatures []struct {
			Label         string          `json:"label"`
			Documentation json.RawMessage `json:"documentation"`
			Parameters    []struct {
				Label json.RawMessage `json:"label"`
			} `json:"parameters"`
			ActiveParameter *int `json:"activeParameter"`
		} `json:"signatures"`
		ActiveSignature int  `json:"activeSignature"`
		ActiveParameter *int `json:"activeParameter"`
	}
	if err := c.call(ctx, "textDocument/signatureHelp", at(path, pos), &res); err != nil {
		return nil, err
	}
	if res == nil || len(res.Signatures) == 0 {
		return nil, nil
	}
	i := min(max(res.ActiveSignature, 0), len(res.Signatures)-1)
	s := res.Signatures[i]
	out := &Signature{Label: s.Label, Doc: markdownOf(s.Documentation), Params: [][2]int{}, Active: -1}
	for _, p := range s.Parameters {
		var off [2]int
		var name string
		switch {
		case json.Unmarshal(p.Label, &off) == nil:
		case json.Unmarshal(p.Label, &name) == nil:
			// A parameter named by its text: found in the label, in order.
			from := 0
			if n := len(out.Params); n > 0 {
				from = out.Params[n-1][1]
			}
			off = [2]int{-1, -1}
			if at := utf16Index(s.Label, name, from); at >= 0 {
				off = [2]int{at, at + utf16Len(name)}
			}
		default:
			off = [2]int{-1, -1}
		}
		out.Params = append(out.Params, off)
	}
	switch {
	case s.ActiveParameter != nil:
		out.Active = *s.ActiveParameter
	case res.ActiveParameter != nil:
		out.Active = *res.ActiveParameter
	}
	if out.Active >= len(out.Params) {
		out.Active = -1
	}
	return out, nil
}

// utf16Index is where sub is in s at or after the UTF-16 offset from, in
// UTF-16 units; -1 when it isn't.
func utf16Index(s, sub string, from int) int {
	b, n := len(s), 0
	for i, r := range s {
		if n >= from {
			b = i
			break
		}
		n += utf16Rune(r)
	}
	if n < from {
		return -1
	}
	i := strings.Index(s[b:], sub)
	if i < 0 {
		return -1
	}
	return n + utf16Len(s[b:b+i])
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16Rune(r)
	}
	return n
}

func utf16Rune(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}
