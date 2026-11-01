package engine

import (
	"context"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// compile is one compile of the project. With Config.Stage it has three
// parts: the project is parsed and shown, about a second in; then the one
// action somebody is looking at is compiled, as often as they look at
// another; and all the while the whole project compiles, which is the
// result. A tool can take minutes over that last part (see dbt.Backend.Stage).
func (e *Engine) compile(ctx context.Context, reason string) (*project.Graph, error) {
	if e.cfg.Stage == nil {
		return e.cfg.Compile(ctx)
	}
	g, final, err := e.cfg.Stage(ctx, e.State().Graph)
	if err != nil || final || g == nil {
		return g, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	first := e.firstFocus(ctx, g, reason)
	e.update(func(s State) State { return s.Stage(g, first) })
	if e.cfg.Focus != nil {
		fctx, stop := context.WithCancel(ctx)
		defer stop()
		go e.focusLoop(fctx, first)
	}
	return e.cfg.Compile(ctx)
}

// Focus asks for id's SQL to be compiled ahead of the rest of the project,
// in place of the action asked for before. While a staged graph is on show
// (State.Partial) that happens now; the next staged graph starts with it
// too. It doesn't wait, and does nothing without Config.Focus.
func (e *Engine) Focus(id project.ActionID) {
	if e.cfg.Focus == nil {
		return
	}
	e.mu.Lock()
	e.focus = id
	e.mu.Unlock()
	select {
	case e.focusWake <- struct{}{}:
	default: // the loop is yet to see the last one, and reads focus then
	}
}

// firstFocus is the action of the staged graph g to compile first. After a
// file change it is the file just saved, which Suggest picks out as the
// only one with a new template; otherwise the action a client asked for,
// and failing that whatever Suggest picks.
func (e *Engine) firstFocus(ctx context.Context, g *project.Graph, reason string) project.ActionID {
	if e.cfg.Focus == nil {
		return ""
	}
	pending := func(id project.ActionID) bool {
		a := g.Actions[id]
		return a != nil && a.SQL != project.SQLCompiled
	}
	e.mu.Lock()
	asked := e.focus
	e.mu.Unlock()
	saved := reason == ReasonFileChanged
	var suggested project.ActionID
	if e.cfg.Suggest != nil && (saved || !pending(asked)) {
		suggested = e.cfg.Suggest(ctx, g)
	}
	switch a := g.Actions[suggested]; {
	case saved && a != nil && a.SQL == project.SQLRaw:
		return suggested
	case pending(asked):
		return asked
	case !saved && pending(suggested):
		return suggested
	}
	return ""
}

// focusLoop compiles one action at a time ahead of the compile under way,
// starting with first, until ctx ends: when a client asks for another, it
// waits for them to settle on it, drops the one in flight and starts
// theirs.
func (e *Engine) focusLoop(ctx context.Context, first project.ActionID) {
	var running project.ActionID
	var done chan struct{}
	stop := func() {}
	defer func() { stop() }()
	start := func(id project.ActionID) {
		stop()
		fctx, cancel := context.WithCancel(ctx)
		d := make(chan struct{})
		running, done, stop = id, d, cancel
		go func() {
			defer close(d)
			e.focusOn(fctx, id)
		}()
	}
	if first != "" {
		start(first)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			running, done = "", nil
			continue
		case <-e.focusWake:
		}
		if !e.settle(ctx) {
			return
		}
		if id := e.wanted(); id != "" && id != running {
			start(id)
		}
	}
}

// settle waits until no client has asked for another action for
// FocusDebounce. It returns false once ctx ends.
func (e *Engine) settle(ctx context.Context) bool {
	if e.cfg.FocusDebounce <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(e.cfg.FocusDebounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-e.focusWake:
			timer.Reset(e.cfg.FocusDebounce)
		case <-timer.C:
			return true
		}
	}
}

// wanted is the action a client asked for, if the staged graph on show has
// yet to get its compiled SQL; "" otherwise.
func (e *Engine) wanted() project.ActionID {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.state.Partial || e.state.Graph == nil {
		return ""
	}
	if a := e.state.Graph.Actions[e.focus]; a != nil && a.SQL != project.SQLCompiled {
		return e.focus
	}
	return ""
}

// focusOn compiles id ahead of the rest and shows the staged graph with
// its SQL, unless the graph on show has changed by then. An action that
// doesn't compile shows its errors; the compile under way reports them
// again, or clears them.
func (e *Engine) focusOn(ctx context.Context, id project.ActionID) {
	var base *project.Graph
	e.updateIf(func(s State) (State, bool) {
		if !s.Partial {
			return s, false
		}
		base = s.Graph
		if s.Focus == id {
			return s, false
		}
		s.Focus = id
		return s, true
	})
	if base == nil {
		return
	}
	g, err := e.cfg.Focus(ctx, base, id)
	if ctx.Err() != nil {
		return // dropped for another action, or the compile is over
	}
	if err != nil && e.cfg.Logf != nil {
		e.cfg.Logf("compiling %s ahead of the project: %v", id, err)
	}
	e.updateIf(func(s State) (State, bool) {
		if !s.Partial || s.Graph != base {
			return s, false
		}
		changed := s.Focus == id
		if changed {
			s.Focus = ""
		}
		switch {
		case err != nil || g == nil:
		case g.HasErrors():
			s.Errors, changed = g.Errors, true
		default:
			s.Graph, changed = g, true
		}
		return s, changed
	})
}

// whole waits until the graph on show isn't a staged one: until the
// compile filling it in has finished.
func (e *Engine) whole(ctx context.Context) error {
	if !e.State().Partial {
		return nil
	}
	states, cancel := e.Subscribe()
	defer cancel()
	for {
		select {
		case s := <-states:
			if !s.Partial {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
