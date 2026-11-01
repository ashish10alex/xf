package remote

import (
	"context"
	"encoding/json"
	"errors"
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

// DefaultGrace is how long a server waits for a client before exiting.
const DefaultGrace = 5 * time.Second

// ServerConfig is what Serve needs.
type ServerConfig struct {
	// Root is the project root, for turning editor paths into project ones.
	Root string
	// Engine is the started engine to share.
	Engine *engine.Engine
	// Table fetches a table's BigQuery metadata; nil turns lookups off.
	Table func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error)
	// Build is this server's build. It stops when a client from a newer
	// one connects, so that client starts a server of its own build (see
	// Build).
	Build Build
	// Grace is how long the server waits with no clients before it stops:
	// after starting and after the last one leaves. 0 means DefaultGrace.
	Grace time.Duration
	// Log receives what the server does; nil discards it.
	Log *log.Logger
}

// Serve shares the engine with every client that connects on ln until ctx
// ends, the server has had no clients for the grace period, or a client
// from a newer build connects. It closes ln, which removes the socket.
func Serve(ctx context.Context, ln net.Listener, cfg ServerConfig) error {
	if cfg.Grace == 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.Log == nil {
		cfg.Log = log.New(io.Discard, "", 0)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{cfg: cfg, clients: map[*jsonrpc.Conn]struct{}{}, count: make(chan int, 1), replaced: make(chan struct{})}
	closeLn := sync.OnceFunc(func() { _ = ln.Close() })
	defer closeLn()

	conns := make(chan net.Conn)
	acceptErr := make(chan error, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			select {
			case conns <- c:
			case <-ctx.Done():
				_ = c.Close()
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel() // runs first: stops the client goroutines wg waits for
	idle := time.NewTimer(cfg.Grace)
	defer idle.Stop()
	n := 0
	for {
		select {
		case c := <-conns:
			n++
			idle.Stop()
			cfg.Log.Printf("client connected (%d)", n)
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serveConn(ctx, c)
				select {
				case s.count <- -1:
				case <-ctx.Done():
				}
			}()
		case d := <-s.count:
			n += d
			cfg.Log.Printf("client left (%d)", n)
			if n == 0 {
				idle.Reset(cfg.Grace)
			}
		case <-idle.C:
			cfg.Log.Printf("no clients for %s; stopping", cfg.Grace)
			return nil
		case <-s.replaced:
			// Stop listening before hanging up on the clients, so none of
			// them dials this server again.
			closeLn()
			return nil
		case err := <-acceptErr:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
}

type server struct {
	cfg ServerConfig
	// count receives -1 whenever a client goes.
	count chan int
	// replaced is closed when a client from a newer build connects.
	replaced    chan struct{}
	replaceOnce sync.Once

	mu      sync.Mutex
	clients map[*jsonrpc.Conn]struct{}

	// activeMu orders the editor's active file: it is held while a new
	// one is passed on and while a joining client is told the current one,
	// so no client ends up with an older one.
	activeMu sync.Mutex
	active   *activeFileParams // the editor's last, if any
	// editor is the client that last reported the active file, which
	// xf/showDocument asks; nil when it has gone. Guarded by mu.
	editor *editorConn

	// callMu guards the server's own requests to clients.
	callMu  sync.Mutex
	nextID  int64
	pending map[int64]chan *jsonrpc.Message
}

// editorConn is a client connection, and done is closed when it ends.
type editorConn struct {
	jc   *jsonrpc.Conn
	done <-chan struct{}
}

// showDocumentWait bounds how long the server waits for an editor to open
// a file.
const showDocumentWait = 10 * time.Second

// serveConn answers one client until it hangs up or ctx ends.
func (s *server) serveConn(ctx context.Context, c net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = c.Close() // unblocks the reads below
	}()
	jc := jsonrpc.NewConn(c, c)
	s.activeMu.Lock()
	s.mu.Lock()
	s.clients[jc] = struct{}{}
	s.mu.Unlock()
	// A xf that starts after the editor follows the file already open.
	if s.active != nil {
		_ = jc.Notify(methodActiveFile, *s.active)
	}
	s.activeMu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, jc)
		if s.editor != nil && s.editor.jc == jc {
			s.editor = nil
		}
		s.mu.Unlock()
	}()

	states, unsubscribe := s.cfg.Engine.Subscribe()
	defer unsubscribe()
	go func() {
		var sent *project.Graph
		for {
			select {
			case st := <-states:
				if err := jc.Notify(methodState, encodeState(st, &sent)); err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		m, err := jc.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				s.cfg.Log.Printf("read: %v", err)
			}
			return
		}
		if m.IsResponse() {
			s.deliver(m)
			continue
		}
		s.handle(ctx, jc, m)
	}
}

// deliver hands a client's answer to the server's request that waits for
// it.
func (s *server) deliver(m *jsonrpc.Message) {
	var id int64
	if json.Unmarshal(m.ID, &id) != nil {
		return
	}
	s.callMu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.callMu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// showDocument asks the editor that last reported the active file to open
// p's file, through its client.
func (s *server) showDocument(ctx context.Context, p showDocumentParams) error {
	s.mu.Lock()
	ed := s.editor
	s.mu.Unlock()
	if ed == nil {
		return ErrNoEditor
	}
	reply := make(chan *jsonrpc.Message, 1)
	s.callMu.Lock()
	if s.pending == nil {
		s.pending = map[int64]chan *jsonrpc.Message{}
	}
	s.nextID++
	id := s.nextID
	s.pending[id] = reply
	s.callMu.Unlock()
	defer func() {
		s.callMu.Lock()
		delete(s.pending, id)
		s.callMu.Unlock()
	}()
	if err := ed.jc.Call(id, methodShowDocument, p); err != nil {
		return ErrNoEditor
	}
	select {
	case m := <-reply:
		switch {
		case m.Error == nil:
			return nil
		case m.Error.Code == codeNoEditor:
			return ErrNoEditor
		default:
			return errors.New(m.Error.Message)
		}
	case <-ed.done:
		return ErrNoEditor
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(showDocumentWait):
		return errors.New("the editor didn't answer")
	}
}

// encodeState puts st on the wire, with the graph's JSON only when it isn't
// *sent, the graph last sent on this connection.
func encodeState(st engine.State, sent **project.Graph) wireState {
	ws := wireState{
		Compiling:  st.Compiling,
		Reason:     st.Reason,
		Started:    st.Started,
		Compiles:   st.Compiles,
		HasGraph:   st.Graph != nil,
		CompiledAt: st.CompiledAt,
		Took:       st.Took,
		Errors:     st.Errors,
		Err:        errText(st.Err),
		Stale:      st.Stale,
		Saved:      st.Saved,
		Outdated:   st.Outdated,
		Partial:    st.Partial,
		Focus:      st.Focus,
		WatchErr:   errText(st.WatchErr),
		Changes:    st.Changes,
		// The signature travels with the changes it is of.
		ImpactSignature: st.ImpactSignature,
		Theme:           st.Theme,
		Defer:           st.Defer,
	}
	if st.Graph != nil && st.Graph != *sent {
		ws.Graph = st.Graph.JSON
		*sent = st.Graph
	}
	return ws
}

func (s *server) handle(ctx context.Context, jc *jsonrpc.Conn, m *jsonrpc.Message) {
	bad := func(err error) {
		if m.IsRequest() {
			_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
		}
	}
	switch m.Method {
	case methodHello:
		var p helloParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		if p.Build.newer(s.cfg.Build) {
			s.replaceOnce.Do(func() {
				s.cfg.Log.Printf("a client of a newer xf (%s; this is %s) connected; stopping so it starts one of its own", p.Build, s.cfg.Build)
				close(s.replaced)
			})
		}
	case methodRecompile:
		var p recompileParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		s.cfg.Engine.Recompile(p.Reason)
	case methodForgetFailedDryRuns:
		s.cfg.Engine.ForgetFailedDryRuns()
	case methodFocus:
		var p focusParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		s.cfg.Engine.Focus(p.Action)
	case methodSetTheme:
		var p setThemeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		s.cfg.Engine.SetTheme(p.Theme)
	case methodSetDefer:
		var p setDeferParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		s.cfg.Engine.SetDefer(p.On)
	case methodDeferUnreadable:
		var p deferUnreadableParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		_ = jc.Reply(m.ID, s.cfg.Engine.DeferUnreadable(p.Targets), nil)
	case methodDeferral:
		var p engine.DeferralRequest
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := s.cfg.Engine.Deferral(ctx, p)
			if ctx.Err() != nil {
				return // as for a dry run: the client asks the next server
			}
			if err != nil {
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodActiveFile:
		var p activeFileParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		s.mu.Lock()
		s.editor = &editorConn{jc: jc, done: ctx.Done()}
		s.mu.Unlock()
		if ev, ok := project.EditorEventFor(s.cfg.Root, p.Event, p.File); ok {
			p := activeFileParams{Event: ev.Event, File: ev.File}
			s.activeMu.Lock()
			s.active = &p
			s.broadcast(methodActiveFile, p)
			s.activeMu.Unlock()
		}
	case methodDryRun:
		var p dryRunParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := s.cfg.Engine.DryRun(ctx, p.Project, p.Location, p.SQL)
			if ctx.Err() != nil {
				// The server or this client is going: say nothing, so the
				// client asks the next server rather than taking this
				// cancellation for the dry run's result.
				return
			}
			if err != nil {
				data, _ := json.Marshal(encodeDryRunError(err))
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error(), Data: data})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodTable:
		var p tableParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := (*bigquery.Table)(nil), engine.ErrNoDryRun
			if s.cfg.Table != nil {
				res, err = s.cfg.Table(ctx, p.Project, p.Dataset, p.Table)
			}
			if ctx.Err() != nil {
				return // as for a dry run: the client asks the next server
			}
			if err != nil {
				data, _ := json.Marshal(encodeDryRunError(err))
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error(), Data: data})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodChangedActions:
		var p changes.Request
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := s.cfg.Engine.ChangedActions(ctx, p)
			if ctx.Err() != nil {
				return // as for a dry run: the client asks the next server
			}
			if err != nil {
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodColumnImpact:
		var p columnImpactParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := s.cfg.Engine.ColumnImpact(ctx, p.ImpactRequest, func(pr impact.Progress) {
				_ = jc.Notify(methodImpactProgress, impactProgressParams{Token: p.Token, Progress: pr})
			})
			if ctx.Err() != nil {
				return // as for a dry run: the client asks the next server
			}
			if err != nil {
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodQueryCost:
		var p queryCostParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			res, err := s.cfg.Engine.QueryCost(ctx, p.CostRequest, func(pr cost.Progress) {
				_ = jc.Notify(methodCostProgress, costProgressParams{Token: p.Token, Progress: pr})
			})
			if ctx.Err() != nil {
				return // as for a dry run: the client asks the next server
			}
			if err != nil {
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
				return
			}
			_ = jc.Reply(m.ID, res, nil)
		}()
	case methodShowDocument:
		var p showDocumentParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			bad(err)
			return
		}
		go func() {
			err := s.showDocument(ctx, p)
			switch {
			case ctx.Err() != nil:
				return // the client is going
			case errors.Is(err, ErrNoEditor):
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: codeNoEditor, Message: err.Error()})
			case err != nil:
				_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternal, Message: err.Error()})
			default:
				_ = jc.Reply(m.ID, nil, nil)
			}
		}()
	default:
		if m.IsRequest() {
			_ = jc.Reply(m.ID, nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found: " + m.Method})
		}
	}
}

// broadcast notifies every client.
func (s *server) broadcast(method string, params any) {
	s.mu.Lock()
	clients := make([]*jsonrpc.Conn, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.Notify(method, params)
	}
}
