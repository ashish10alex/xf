package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/jsonrpc"
	"github.com/ashish10alex/xf/internal/project"
)

// Client is one process's view of the shared engine. It has the methods of
// an engine that front ends use, and keeps working across servers: when its
// server goes, it dials again, which starts a new one, and carries on.
type Client struct {
	dial  func(context.Context) (net.Conn, error)
	parse func([]byte) (*project.Graph, error)
	build Build
	log   *log.Logger
	ctx   context.Context
	stop  context.CancelFunc

	mu    sync.Mutex
	state engine.State
	subs  map[*subscriber]struct{}
	// conn is the live connection; nil between servers. up is closed once
	// there is one, and replaced when it goes.
	conn    *jsonrpc.Conn
	up      chan struct{}
	nextID  int64
	pending map[int64]chan *jsonrpc.Message
	// progress hears xf/impactProgress, by the token of its request.
	progress map[int64]func(impact.Progress)
	// costProgress hears xf/costProgress, likewise.
	costProgress map[int64]func(cost.Progress)
	// showDocument opens files for the server's xf/showDocument; nil
	// when this client has no editor.
	showDocument func(ctx context.Context, file string, line int) error
	// active is the last active file this client reported, told again to
	// each new server.
	active *activeFileParams
	// focus is the action last asked for with Focus; "" before any.
	focus project.ActionID

	firstDone chan struct{}
	firstOnce sync.Once
	editor    chan project.EditorEvent
	done      chan struct{}
}

// ClientConfig is what Connect needs.
type ClientConfig struct {
	// Dial connects to the server, starting one if need be (see Dialer).
	Dial func(context.Context) (net.Conn, error)
	// Parse rebuilds the graph the server sends, with the project's
	// backend (see backend.Backend.Parse).
	Parse func([]byte) (*project.Graph, error)
	// Build is this client's build. A server from an older one stops
	// when the client connects, so the client starts one of its own build.
	Build Build
	// Log receives what the client does; nil discards it.
	Log *log.Logger
}

// Connect dials the server and keeps a connection to one until Close. It
// fails only when the first dial does.
func Connect(ctx context.Context, cfg ClientConfig) (*Client, error) {
	c, err := cfg.Dial(ctx)
	if err != nil {
		return nil, err
	}
	logger := cfg.Log
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	cctx, stop := context.WithCancel(context.Background())
	cl := &Client{
		dial:         cfg.Dial,
		parse:        cfg.Parse,
		build:        cfg.Build,
		log:          logger,
		ctx:          cctx,
		stop:         stop,
		subs:         map[*subscriber]struct{}{},
		up:           make(chan struct{}),
		pending:      map[int64]chan *jsonrpc.Message{},
		progress:     map[int64]func(impact.Progress){},
		costProgress: map[int64]func(cost.Progress){},
		firstDone:    make(chan struct{}),
		editor:       make(chan project.EditorEvent, 1),
		done:         make(chan struct{}),
		// Until the server says otherwise, a compile is on its way.
		state: engine.State{}.Begin("", time.Now()),
	}
	go cl.run(c)
	return cl, nil
}

// Close hangs up for good.
func (c *Client) Close() {
	c.stop()
	<-c.done
}

// run serves connections one after another until Close.
func (c *Client) run(conn net.Conn) {
	defer close(c.done)
	for {
		c.serve(conn)
		if c.ctx.Err() != nil {
			return
		}
		c.log.Printf("lost the server; starting another")
		for {
			var err error
			if conn, err = c.dial(c.ctx); err == nil {
				break
			}
			if c.ctx.Err() != nil {
				return
			}
			c.log.Printf("reconnect: %v", err)
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// serve reads one connection until it ends.
func (c *Client) serve(nc net.Conn) {
	stopped := context.AfterFunc(c.ctx, func() { _ = nc.Close() })
	defer stopped()
	defer func() { _ = nc.Close() }()
	jc := jsonrpc.NewConn(nc, nc)
	c.mu.Lock()
	c.conn = jc
	close(c.up)
	active, focus := c.active, c.focus
	c.mu.Unlock()
	// Written while the loop below reads: a server may write first, and a
	// Windows pipe's write waits for the other end to read.
	go func() {
		if err := jc.Notify(methodHello, helloParams{Build: c.build}); err != nil {
			c.log.Printf("hello: %v", err)
			return
		}
		if focus != "" {
			_ = jc.Notify(methodFocus, focusParams{Action: focus})
		}
		if active != nil {
			_ = jc.Notify(methodActiveFile, *active)
		}
	}()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.up = make(chan struct{})
		for id, ch := range c.pending {
			close(ch) // the caller asks the next server
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()

	serverCompiles := 0
	for {
		m, err := jc.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) && c.ctx.Err() == nil {
				c.log.Printf("read: %v", err)
			}
			return
		}
		switch {
		case m.IsResponse():
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method == methodState:
			var ws wireState
			if err := json.Unmarshal(m.Params, &ws); err != nil {
				c.log.Printf("bad state: %v", err)
				continue
			}
			c.apply(ws, &serverCompiles)
		case m.Method == methodImpactProgress:
			var p impactProgressParams
			if json.Unmarshal(m.Params, &p) != nil {
				continue
			}
			c.mu.Lock()
			fn := c.progress[p.Token]
			c.mu.Unlock()
			if fn != nil {
				fn(p.Progress)
			}
		case m.Method == methodCostProgress:
			var p costProgressParams
			if json.Unmarshal(m.Params, &p) != nil {
				continue
			}
			c.mu.Lock()
			fn := c.costProgress[p.Token]
			c.mu.Unlock()
			if fn != nil {
				fn(p.Progress)
			}
		case m.Method == methodShowDocument && m.IsRequest():
			go c.answerShowDocument(jc, m)
		case m.Method == methodActiveFile:
			var p activeFileParams
			if json.Unmarshal(m.Params, &p) == nil {
				latest(c.editor, project.EditorEvent{Event: p.Event, File: p.File})
			}
		}
	}
}

// answerShowDocument opens the file the server's xf/showDocument names,
// with the editor HandleShowDocument gave, and says how it went.
func (c *Client) answerShowDocument(jc *jsonrpc.Conn, m *jsonrpc.Message) {
	var p showDocumentParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		return
	}
	c.mu.Lock()
	open := c.showDocument
	c.mu.Unlock()
	err := ErrNoEditor
	if open != nil {
		err = open(c.ctx, p.File, p.Line)
	}
	switch {
	case errors.Is(err, ErrNoEditor):
		_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: codeNoEditor, Message: err.Error()})
	case err != nil:
		_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
	default:
		_ = jc.Reply(m.ID, nil, nil)
	}
}

// latest sends ev, replacing an event nobody has read yet.
func latest(ch chan project.EditorEvent, ev project.EditorEvent) {
	for {
		select {
		case ch <- ev:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// apply merges a server's state into the client's. Compiles count this
// client's view, so they keep rising across servers; a new server that has
// no graph yet leaves the last one on show until it compiles.
func (c *Client) apply(ws wireState, serverCompiles *int) {
	var g *project.Graph
	if len(ws.Graph) > 0 {
		var err error
		if g, err = c.parse(ws.Graph); err != nil {
			c.log.Printf("bad graph: %v", err)
			return
		}
	}
	c.mu.Lock()
	st := c.state
	st.Compiling, st.Reason, st.Started = ws.Compiling, ws.Reason, ws.Started
	st.WatchErr = textErr(ws.WatchErr)
	st.Changes, st.ImpactSignature = ws.Changes, ws.ImpactSignature
	if ws.Theme != "" {
		st.Theme = ws.Theme
	}
	st.Defer = ws.Defer
	st.Saved, st.Outdated = ws.Saved, ws.Outdated
	st.Partial, st.Focus = ws.Partial, ws.Focus
	finished := ws.Compiles > *serverCompiles
	*serverCompiles = ws.Compiles
	if ws.Partial {
		// An action compiled ahead of the rest reports its errors now.
		st.Errors = ws.Errors
	}
	if finished {
		st.Compiles++
		st.Errors, st.Err, st.Stale = ws.Errors, textErr(ws.Err), ws.Stale
	}
	if g != nil {
		st.Graph, st.CompiledAt, st.Took = g, ws.CompiledAt, ws.Took
	}
	c.state = st
	subs := make([]*subscriber, 0, len(c.subs))
	for sub := range c.subs {
		subs = append(subs, sub)
	}
	c.mu.Unlock()
	if finished {
		c.firstOnce.Do(func() { close(c.firstDone) })
	}
	for _, sub := range subs {
		sub.send(st)
	}
}

type subscriber struct {
	ch   chan engine.State
	done chan struct{}
}

func (sub *subscriber) send(s engine.State) {
	select {
	case sub.ch <- s:
	case <-sub.done:
	}
}

// Subscribe returns a channel that receives the current state straight
// away and then every change to it, until cancel is called.
func (c *Client) Subscribe() (<-chan engine.State, func()) {
	sub := &subscriber{ch: make(chan engine.State, 16), done: make(chan struct{})}
	c.mu.Lock()
	sub.ch <- c.state
	c.subs[sub] = struct{}{}
	c.mu.Unlock()
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, sub)
			c.mu.Unlock()
			close(sub.done)
		})
	}
}

// State returns the latest state.
func (c *Client) State() engine.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// FirstCompile is closed once a compile has finished.
func (c *Client) FirstCompile() <-chan struct{} { return c.firstDone }

// Editor reports the file active in the editor, as editors tell the server.
// Only the newest event waits to be read.
func (c *Client) Editor() <-chan project.EditorEvent { return c.editor }

// Recompile asks the server for a compile. Between servers it does nothing:
// the next server compiles when it starts.
func (c *Client) Recompile(reason string) {
	c.notify(methodRecompile, recompileParams{Reason: reason})
}

// ForgetFailedDryRuns has the server try failed dry runs again (see
// engine.Engine.ForgetFailedDryRuns).
func (c *Client) ForgetFailedDryRuns() {
	c.notify(methodForgetFailedDryRuns, struct{}{})
}

// Focus asks the server to compile id ahead of the rest of the project
// (see engine.Engine.Focus). A server this client moves to is asked again.
func (c *Client) Focus(id project.ActionID) {
	c.mu.Lock()
	c.focus = id
	c.mu.Unlock()
	c.notify(methodFocus, focusParams{Action: id})
}

// SetTheme tells the server, and so every client, the colour theme (auto,
// light or dark) to show.
func (c *Client) SetTheme(theme string) {
	c.notify(methodSetTheme, setThemeParams{Theme: theme})
}

// ActiveFile tells the server, and so every client, the file (an absolute
// path) the editor made active with event. A server this client moves to
// is told the last one again.
func (c *Client) ActiveFile(event, file string) {
	p := activeFileParams{Event: event, File: file}
	c.mu.Lock()
	c.active = &p
	c.mu.Unlock()
	c.notify(methodActiveFile, p)
}

func (c *Client) notify(method string, params any) {
	c.mu.Lock()
	jc := c.conn
	c.mu.Unlock()
	if jc != nil {
		if err := jc.Notify(method, params); err != nil {
			c.log.Printf("%s: %v", method, err)
		}
	}
}

// HandleShowDocument makes open how this client's editor opens a file
// (an absolute path, at a 0-based line) when another xf asks with
// ShowDocument. Only a client that reports the active file is asked.
func (c *Client) HandleShowDocument(open func(ctx context.Context, file string, line int) error) {
	c.mu.Lock()
	c.showDocument = open
	c.mu.Unlock()
}

// ShowDocument asks the editor that last reported the active file to open
// file (an absolute path) at a 0-based line. It fails with ErrNoEditor
// when there is none, or it can't.
func (c *Client) ShowDocument(ctx context.Context, file string, line int) error {
	m, err := c.call(ctx, methodShowDocument, showDocumentParams{File: file, Line: line})
	switch {
	case err != nil:
		return err
	case m.Error == nil:
		return nil
	case m.Error.Code == codeNoEditor:
		return ErrNoEditor
	}
	return errors.New(m.Error.Message)
}

// DryRun asks the server to dry-run sql (see engine.Engine.DryRun). If the
// server goes before answering, the next one is asked.
func (c *Client) DryRun(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
	m, err := c.call(ctx, methodDryRun, dryRunParams{Project: project, Location: location, SQL: sql})
	if err != nil {
		return nil, err
	}
	return decodeDryRun(m)
}

// SetDefer turns defer to prod on or off for every client of the engine
// (see engine.Engine.SetDefer).
func (c *Client) SetDefer(on bool) {
	c.notify(methodSetDefer, setDeferParams{On: on})
}

// Deferral asks the server what defer to prod reads from prod for req (see
// engine.Engine.Deferral).
func (c *Client) Deferral(ctx context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error) {
	m, err := c.call(ctx, methodDeferral, req)
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	var res engine.DeferralResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("deferral result: %w", err)
	}
	return &res, nil
}

// DeferUnreadable tells the server of Prod Targets a query was denied (see
// engine.Engine.DeferUnreadable).
func (c *Client) DeferUnreadable(ctx context.Context, ts []project.Target) (bool, error) {
	m, err := c.call(ctx, methodDeferUnreadable, deferUnreadableParams{Targets: ts})
	if err != nil {
		return false, err
	}
	if m.Error != nil {
		return false, errors.New(m.Error.Message)
	}
	var added bool
	err = json.Unmarshal(m.Result, &added)
	return added, err
}

// Table asks the server for the BigQuery metadata of project.dataset.table.
// A table that doesn't exist is a *bigquery.APIError with Status 404.
func (c *Client) Table(ctx context.Context, project, dataset, table string) (*bigquery.Table, error) {
	m, err := c.call(ctx, methodTable, tableParams{Project: project, Dataset: dataset, Table: table})
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, decodeDryRunError(m.Error)
	}
	var res bigquery.Table
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("table result: %w", err)
	}
	return &res, nil
}

// ChangedActions asks the server for the Changed Actions (see
// engine.Engine.ChangedActions). If the server goes before answering, the
// next one is asked.
func (c *Client) ChangedActions(ctx context.Context, req changes.Request) (*changes.Result, error) {
	m, err := c.call(ctx, methodChangedActions, req)
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	var res *changes.Result
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("changed actions result: %w", err)
	}
	return res, nil
}

// ColumnImpact asks the server for the column impact of the Changed
// Actions (see engine.Engine.ColumnImpact), telling progress, when not nil,
// how far it got. If the server goes before answering, the next one is
// asked.
func (c *Client) ColumnImpact(ctx context.Context, req engine.ImpactRequest, progress func(impact.Progress)) (*impact.Report, error) {
	c.mu.Lock()
	c.nextID++
	token := c.nextID
	if progress != nil {
		c.progress[token] = progress
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.progress, token)
		c.mu.Unlock()
	}()
	m, err := c.call(ctx, methodColumnImpact, columnImpactParams{ImpactRequest: req, Token: token})
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	var res *impact.Report
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("column impact result: %w", err)
	}
	if res == nil {
		return nil, errors.New("column impact: no report")
	}
	return res, nil
}

// QueryCost asks the server what the Changed Actions would scan (see
// engine.Engine.QueryCost), telling progress, when not nil, how far it got.
// If the server goes before answering, the next one is asked.
func (c *Client) QueryCost(ctx context.Context, req engine.CostRequest, progress func(cost.Progress)) (*cost.Report, error) {
	c.mu.Lock()
	c.nextID++
	token := c.nextID
	if progress != nil {
		c.costProgress[token] = progress
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.costProgress, token)
		c.mu.Unlock()
	}()
	m, err := c.call(ctx, methodQueryCost, queryCostParams{CostRequest: req, Token: token})
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	var res *cost.Report
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("query cost result: %w", err)
	}
	if res == nil {
		return nil, errors.New("query cost: no report")
	}
	return res, nil
}

// call sends a request and waits for its answer, asking the next server
// when the one it asked goes before answering.
func (c *Client) call(ctx context.Context, method string, params any) (*jsonrpc.Message, error) {
	for {
		c.mu.Lock()
		jc, up := c.conn, c.up
		var id int64
		var reply chan *jsonrpc.Message
		if jc != nil {
			c.nextID++
			id = c.nextID
			reply = make(chan *jsonrpc.Message, 1)
			c.pending[id] = reply
		}
		c.mu.Unlock()
		if jc == nil {
			select {
			case <-up:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-c.ctx.Done():
				return nil, errors.New(method + ": client closed")
			}
		}
		if err := jc.Call(id, method, params); err != nil {
			c.forget(id)
			// The connection is going: give it a moment to, then ask the next.
			select {
			case <-time.After(20 * time.Millisecond):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		select {
		case m, ok := <-reply:
			if !ok {
				continue // the server went; ask the next one
			}
			return m, nil
		case <-ctx.Done():
			c.forget(id)
			return nil, ctx.Err()
		}
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// decodeDryRunError is the error the server encoded in e (see dryRunError).
func decodeDryRunError(e *jsonrpc.Error) error {
	var d dryRunError
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &d) != nil {
		return errors.New(e.Message)
	}
	return d.err()
}

func decodeDryRun(m *jsonrpc.Message) (*bigquery.DryRun, error) {
	if m.Error != nil {
		return nil, decodeDryRunError(m.Error)
	}
	var res bigquery.DryRun
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, fmt.Errorf("dry run result: %w", err)
	}
	return &res, nil
}
