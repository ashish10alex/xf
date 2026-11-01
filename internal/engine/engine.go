// Package engine keeps one Dataform project compiled and dry-runs its SQL
// for every front end at once: the TUI and the language server. It compiles
// on start, when project files change and when asked, keeps the last good
// compile on hand while the project is broken, and sends the same SQL to
// BigQuery only once. It also keeps the project's Changed Actions (see
// package changes) up to date once a client has asked for them, and checks
// their column impact when asked (see package impact). It knows nothing of
// terminals or editors.
package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// ReasonFileChanged is the Reason of a compile started by a file change.
const ReasonFileChanged = "file changed"

// dryRunTimeout bounds one dry run; BigQuery usually answers in about a second.
const dryRunTimeout = time.Minute

// DryRunMaxAge is how long a dry run's answer is kept: one older is asked
// again, as the tables it reads may have changed since.
const DryRunMaxAge = time.Minute

// Config wires an engine to the outside world.
type Config struct {
	// Compile compiles the project. It must honour ctx cancellation. Nil
	// means the engine never compiles.
	Compile func(ctx context.Context) (*project.Graph, error)
	// Stage, when set, runs before every Compile and shows the project
	// sooner: it parses the project into a graph whose SQL is still
	// templates (see backend.Stager.Stage), given the graph on show, or
	// nil. final says the graph is all Compile would give, so Compile isn't
	// run. Otherwise the graph is shown while Compile runs (State.Partial).
	Stage func(ctx context.Context, prev *project.Graph) (g *project.Graph, final bool, err error)
	// Focus, while a staged graph is on show, compiles one action of it,
	// with the actions shown with it, ahead of Compile and returns the
	// graph with their SQL compiled (see backend.Stager.Focus). Nil compiles none ahead.
	Focus func(ctx context.Context, base *project.Graph, id project.ActionID) (*project.Graph, error)
	// Suggest picks the action to Focus on first when no client has asked
	// for one (see backend.Stager.Suggest); nil picks none.
	Suggest func(ctx context.Context, g *project.Graph) project.ActionID
	// FocusDebounce is how long the engine waits after a client asks for
	// another action before it compiles it, so passing over actions
	// compiles none of them.
	FocusDebounce time.Duration
	// Logf, when set, is told what no state shows: why a Focus failed.
	Logf func(format string, args ...any)
	// Watch reports changes to the project's files, as dataform.Watch does.
	// Nil means compiling only at start and when asked.
	Watch func(ctx context.Context) (<-chan struct{}, error)
	// DryRun asks BigQuery to plan sql in a job in project and location,
	// without running it. Nil turns dry runs off.
	DryRun func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)
	// Changes works out Changed Actions; nil turns them off.
	Changes ChangesSource
	// Impact works out the column impact of Changed Actions (see
	// ColumnImpact); nil turns it off.
	Impact ImpactFunc
	// Cost estimates what the Changed Actions scan (see QueryCost); nil
	// turns it off.
	Cost CostFunc
	// Defer works out what defer to prod reads from prod (see Deferral);
	// nil turns it off.
	Defer Deferrer
	// DeferOn is whether defer to prod starts on; SaveDefer, when set,
	// keeps the choice when a client changes it, and OnDefer, when set, is
	// told of it, as a dbt engine compiles differently with it.
	DeferOn   bool
	SaveDefer func(on bool)
	OnDefer   func(on bool)
	// Saved loads the compile an earlier engine saved (see package
	// compilecache), or returns nil. Start calls it once files are being
	// watched, so an edit made after it looked is never missed. Nil means
	// always compiling at start.
	Saved func() *Saved
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// Credentials returns the credentials' generation (gcpauth.Generation),
	// which goes up at each new login. DryRun forgets the failed dry runs
	// when it has, as a new login may fix them. Nil never forgets them so.
	Credentials func() uint64
}

// Saved is a compile an earlier engine saved.
type Saved struct {
	Graph      *project.Graph
	CompiledAt time.Time
	Took       time.Duration
	// Outdated says why the project may no longer compile to Graph, or is
	// "" when nothing it was compiled from has changed. An engine with a
	// current saved compile doesn't compile at start.
	Outdated string
}

// ChangesSource works out Changed Actions. *changes.Source is one.
type ChangesSource interface {
	// Changes compares head, a compile's JSON, with the base req names
	// (see changes.Source.Changes).
	Changes(ctx context.Context, req changes.Request, head []byte) (*changes.Result, error)
	// Retry forgets failed base compiles.
	Retry()
}

// State is what the engine knows about the project's compile. The graph and
// errors in it are shared: read them, never change them.
type State struct {
	// Compiling is set while a compile runs, from Started, for Reason
	// (ReasonFileChanged, or "" at start and when asked).
	Compiling bool
	Reason    string
	Started   time.Time
	// Compiles counts the compile results the engine has had: compiles
	// that finished, and a saved compile loaded at start.
	Compiles int
	// Graph is the last good compile, or the first compile, errors and all;
	// nil until one finishes. CompiledAt and Took are about Graph's compile.
	Graph      *project.Graph
	CompiledAt time.Time
	Took       time.Duration
	// Errors are the latest compile's errors, and Err is set when it could
	// not run at all.
	Errors []project.CompilationError
	Err    error
	// Stale is set when the latest compile failed and Graph is from an
	// earlier one.
	Stale bool
	// Saved is set while Graph is a compile an earlier engine saved, and
	// Outdated, while a compile runs to replace it, says why it may no
	// longer match the project (see Config.Saved).
	Saved    bool
	Outdated string
	// Partial is set while Graph is a staged one (see Config.Stage): the
	// project as parsed, shown while its compile runs. Its actions say how
	// far their own SQL is compiled (project.Action.SQL). Focus is the
	// action being compiled ahead of the rest, "" when none is.
	Partial bool
	Focus   project.ActionID
	// WatchErr says why project files aren't being watched, when they
	// aren't: compiles then happen only when asked.
	WatchErr error
	// Changes are Graph's Changed Actions against the default branch a
	// client last asked about, kept up to date after every good compile
	// once that branch's base has been compiled; nil until then. A base is
	// compiled only when a client asks (see Engine.ChangedActions).
	Changes *changes.Result
	// ImpactSignature is Changes' impact.Signature, when column impact or
	// query cost is on: a report on the working tree with another
	// signature is out of date.
	ImpactSignature string
	// Defer is whether defer to prod is on (see Engine.SetDefer).
	Defer bool
	// Theme is the TUI's colour theme (auto, light or dark) a client last
	// set, "" until one has; every TUI on the engine follows it. xf web
	// keeps its own.
	Theme string
}

// SetTheme makes theme the one every client shows, telling them if it
// changed.
func (e *Engine) SetTheme(theme string) {
	e.updateIf(func(s State) (State, bool) {
		if s.Theme == theme {
			return s, false
		}
		s.Theme = theme
		return s, true
	})
}

// Begin is the state once a compile for reason has started at at.
func (s State) Begin(reason string, at time.Time) State {
	s.Compiling, s.Reason, s.Started = true, reason, at
	return s
}

// Stage is the state once the project has been parsed into g, with its
// compile still under way and focus being compiled ahead of the rest.
func (s State) Stage(g *project.Graph, focus project.ActionID) State {
	s.Graph, s.Partial, s.Focus = g, true, focus
	s.Saved, s.Outdated = false, "" // its actions say what is outdated now
	return s
}

// Finish is the state once a compile has finished at at, taking took, with
// graph g or error err. A compile with errors replaces the graph only when
// there is none: the last good compile stays in use, marked stale, until the
// project compiles cleanly again.
func (s State) Finish(g *project.Graph, err error, took time.Duration, at time.Time) State {
	s.Compiling, s.Reason = false, ""
	s.Partial, s.Focus = false, ""
	s.Compiles++
	s.Outdated = "" // stale now says whether the graph is behind
	switch {
	case err != nil:
		s.Err, s.Errors, s.Stale = err, nil, s.Graph != nil
	case g.HasErrors() && s.Graph != nil:
		s.Err, s.Errors, s.Stale = nil, g.Errors, true
	default:
		s.Err, s.Errors, s.Stale = nil, g.Errors, false
		s.Graph, s.CompiledAt, s.Took = g, at, took
		s.Saved = false
	}
	return s
}

// Load is the state once saved has been loaded at start: a finished
// compile, with a compile to replace it under way when it is outdated.
func (s State) Load(saved *Saved) State {
	s.Compiles++
	s.Graph, s.CompiledAt, s.Took = saved.Graph, saved.CompiledAt, saved.Took
	s.Saved, s.Outdated = true, saved.Outdated
	s.Compiling = saved.Outdated != ""
	if !s.Compiling {
		s.Reason, s.Started = "", time.Time{}
	}
	return s
}

// Engine is one project's compile loop and dry-run cache.
type Engine struct {
	cfg      Config
	triggers chan string

	// sendMu makes updates one at a time, so subscribers see states in
	// order even when they come from more than one goroutine.
	sendMu sync.Mutex
	mu     sync.Mutex
	state  State
	// branch is the default branch Changes are worked out against, set by
	// the last ChangedActions request; "" until one.
	branch string
	subs   map[*subscriber]struct{}
	// firstDone is closed once the first compile has finished, good or not,
	// or at Start when there is nothing to compile or a current saved
	// compile was loaded.
	firstDone chan struct{}
	firstOnce sync.Once
	dryRuns   map[dryRunKey]*dryRun
	// credsGen is the credentials' generation the dry runs were asked
	// with (Config.Credentials).
	credsGen uint64
	// impacts is the last column impact check per branch and head.
	impacts map[impactKey]*impactRun
	// costs is the last cost check per branch and head.
	costs map[costKey]*costRun
	// focus is the action a client last asked to have compiled first, and
	// focusWake tells the compile under way that it changed.
	focus     project.ActionID
	focusWake chan struct{}
}

// New makes an engine. Nothing happens until Start.
func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	e := &Engine{
		cfg:       cfg,
		triggers:  make(chan string, 1),
		subs:      map[*subscriber]struct{}{},
		firstDone: make(chan struct{}),
		dryRuns:   map[dryRunKey]*dryRun{},
		impacts:   map[impactKey]*impactRun{},
		costs:     map[costKey]*costRun{},
		focusWake: make(chan struct{}, 1),
	}
	e.state.Defer = cfg.DeferOn && cfg.Defer != nil
	if cfg.Compile != nil {
		// The first compile is as good as started: front ends show it.
		e.state = e.state.Begin("", cfg.Now())
	}
	if cfg.Credentials != nil {
		e.credsGen = cfg.Credentials()
	}
	return e
}

// Start compiles the project now and again whenever it changes or Recompile
// asks, until ctx ends. Call it once. It returns why files aren't being
// watched, if they aren't; the engine still compiles when asked.
func (e *Engine) Start(ctx context.Context) error {
	if e.cfg.Compile == nil {
		e.firstOnce.Do(func() { close(e.firstDone) })
		return nil
	}
	var changes <-chan struct{}
	var watchErr error
	if e.cfg.Watch != nil {
		changes, watchErr = e.cfg.Watch(ctx)
	}
	e.mu.Lock()
	e.state.WatchErr = watchErr
	e.mu.Unlock()
	compileNow := true
	if e.cfg.Saved != nil {
		if saved := e.cfg.Saved(); saved != nil && saved.Graph != nil {
			e.update(func(s State) State { return s.Load(saved) })
			compileNow = saved.Outdated != ""
		}
	}
	go e.loop(ctx, changes, compileNow)
	return watchErr
}

// Recompile asks for a compile, cancelling one in flight. It doesn't wait.
// Asked for by hand (any reason but ReasonFileChanged), it also retries
// base compiles that failed.
func (e *Engine) Recompile(reason string) {
	if reason != ReasonFileChanged && e.cfg.Changes != nil {
		e.cfg.Changes.Retry()
	}
	for {
		select {
		case e.triggers <- reason:
			return
		default:
		}
		// A compile is already asked for: this reason supersedes its.
		select {
		case <-e.triggers:
		default:
		}
	}
}

// State returns what the engine knows now.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// FirstCompile is closed once the first compile has finished, good or not,
// or a current saved compile was loaded: once the graph matches the files.
func (e *Engine) FirstCompile() <-chan struct{} { return e.firstDone }

type compileResult struct {
	graph *project.Graph
	err   error
	took  time.Duration
}

func (e *Engine) loop(ctx context.Context, changes <-chan struct{}, compileNow bool) {
	reason := ""
	if !compileNow && !e.wait(ctx, &changes, &reason) {
		return
	}
	for {
		cctx, cancel := context.WithCancel(ctx)
		e.update(func(s State) State { return s.Begin(reason, e.cfg.Now()) })
		done := make(chan compileResult, 1)
		go func() {
			start := time.Now()
			g, err := e.compile(cctx, reason)
			done <- compileResult{g, err, time.Since(start)}
		}()

		finished, superseded := false, false
		for !finished && !superseded && ctx.Err() == nil {
			select {
			case r := <-done:
				finished = true
				if ctx.Err() == nil && !errors.Is(r.err, context.Canceled) {
					// Only the dry runs: a compile builds no tables, so the
					// defer lookups stay.
					e.mu.Lock()
					e.forgetFailedDryRunsLocked()
					e.mu.Unlock()
					e.update(func(s State) State { return s.Finish(r.graph, r.err, r.took, e.cfg.Now()) })
					if r.err == nil && !r.graph.HasErrors() {
						go e.refreshChanges(ctx, r.graph)
					}
				}
				e.firstOnce.Do(func() { close(e.firstDone) })
			case reason = <-e.triggers:
				superseded = true
			case _, ok := <-changes:
				if !ok {
					changes = nil // watching stopped; compile when asked
					continue
				}
				reason, superseded = ReasonFileChanged, true
			case <-ctx.Done():
			}
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		if superseded {
			continue // start again, dropping the cancelled compile's result
		}
		if !e.wait(ctx, &changes, &reason) {
			return
		}
	}
}

// wait waits for the next compile to be asked for, by Recompile or a file
// change, and sets reason to why. It returns false once ctx ends.
func (e *Engine) wait(ctx context.Context, changes *<-chan struct{}, reason *string) bool {
	for {
		select {
		case *reason = <-e.triggers:
			return true
		case _, ok := <-*changes:
			if !ok {
				*changes = nil // watching stopped; compile when asked
				continue
			}
			*reason = ReasonFileChanged
			return true
		case <-ctx.Done():
			return false
		}
	}
}

// update changes the state and tells every subscriber, one update at a
// time so subscribers see states in order.
func (e *Engine) update(f func(State) State) {
	e.updateIf(func(s State) (State, bool) { return f(s), true })
}

// updateIf is update for a change that may not apply: f reports whether it
// changed anything, and nobody is told when it didn't.
func (e *Engine) updateIf(f func(State) (State, bool)) {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	e.mu.Lock()
	next, changed := f(e.state)
	if !changed {
		e.mu.Unlock()
		return
	}
	e.state = next
	if next.Compiles > 0 && next.Outdated == "" && !next.Partial {
		// Closed with the state in place and before anyone is told, so
		// whoever sees the first result, or waits for it, finds both.
		e.firstOnce.Do(func() { close(e.firstDone) })
	}
	s := e.state
	subs := make([]*subscriber, 0, len(e.subs))
	for sub := range e.subs {
		subs = append(subs, sub)
	}
	e.mu.Unlock()
	for _, sub := range subs {
		sub.send(s)
	}
}

type subscriber struct {
	ch   chan State
	done chan struct{}
}

func (sub *subscriber) send(s State) {
	select {
	case sub.ch <- s:
	case <-sub.done:
	}
}

// Subscribe returns a channel that receives the current state straight
// away and then every change to it, in order, until cancel is called. A
// subscriber that stops reading holds the compile loop up, so read promptly
// or cancel.
func (e *Engine) Subscribe() (<-chan State, func()) {
	sub := &subscriber{ch: make(chan State, 16), done: make(chan struct{})}
	e.mu.Lock()
	sub.ch <- e.state // the buffer is empty: this never blocks
	e.subs[sub] = struct{}{}
	e.mu.Unlock()
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subs, sub)
			e.mu.Unlock()
			close(sub.done)
		})
	}
}

// ErrNoDryRun is returned by DryRun when dry runs are off.
var ErrNoDryRun = errors.New("dry runs are off")

type dryRunKey struct{ project, location, sql string }

// dryRun is one dry run, in flight until done is closed; at is when it
// answered.
type dryRun struct {
	done chan struct{}
	at   time.Time
	res  *bigquery.DryRun
	err  error
}

// DryRun asks BigQuery to plan sql in a job in project and location,
// without running it. Results are kept by what was sent, so the same SQL
// asked for again, by any front end, costs no second call, and a call in
// flight is shared. An answer is kept for DryRunMaxAge; a failure also
// goes when the next compile finishes, at a new login, or at
// ForgetFailedDryRuns. ctx
// bounds only this caller's wait: a call others share runs on, bounded by
// its own timeout.
func (e *Engine) DryRun(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
	if e.cfg.DryRun == nil {
		return nil, ErrNoDryRun
	}
	key := dryRunKey{project, location, sql}
	var gen uint64
	if e.cfg.Credentials != nil {
		gen = e.cfg.Credentials()
	}
	e.mu.Lock()
	if gen != e.credsGen {
		e.credsGen = gen
		e.forgetFailedDryRunsLocked()
	}
	d := e.dryRuns[key]
	if d != nil && d.expired(e.cfg.Now()) {
		d = nil
	}
	if d == nil {
		d = &dryRun{done: make(chan struct{})}
		e.dryRuns[key] = d
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), dryRunTimeout)
			defer cancel()
			res, err := e.cfg.DryRun(rctx, project, location, sql)
			e.mu.Lock()
			d.at, d.res, d.err = e.cfg.Now(), res, err
			e.mu.Unlock()
			close(d.done)
		}()
	}
	e.mu.Unlock()
	select {
	case <-d.done:
		return d.res, d.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// expired says d answered more than DryRunMaxAge before now. Call with
// e.mu held: at is set under it.
func (d *dryRun) expired(now time.Time) bool {
	select {
	case <-d.done:
		return now.Sub(d.at) >= DryRunMaxAge
	default:
		return false
	}
}

// ForgetFailedDryRuns drops failed dry runs so they are tried again: after
// a compile, which is also what recompiling does once you have logged in,
// and after a run, which may have built the tables they read.
func (e *Engine) ForgetFailedDryRuns() {
	if e.cfg.Defer != nil {
		e.cfg.Defer.Forget()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forgetFailedDryRunsLocked()
}

func (e *Engine) forgetFailedDryRunsLocked() {
	for k, d := range e.dryRuns {
		select {
		case <-d.done:
			if d.err != nil {
				delete(e.dryRuns, k)
			}
		default:
		}
	}
}

// ErrNoChanges is returned by ChangedActions when they are off.
var ErrNoChanges = errors.New("run changed is not available")

// ErrStale is returned by ChangedActions for the working tree while the
// project has compile errors: a run would fail on them, and diffing the
// last good compile would hide the latest edits.
var ErrStale = errors.New("the project has compile errors: fix them first (e shows them)")

// ChangedActions works out the Changed Actions req asks for, and makes
// req.DefaultBranch the branch State.Changes follows. For the working tree
// it waits for the first compile, and for the one under way when a staged
// graph is on show, and compares the current graph; with
// req.Compile it refuses while the project has compile errors. A base
// compile the caller stops waiting for carries on, and lands in the cache.
func (e *Engine) ChangedActions(ctx context.Context, req changes.Request) (*changes.Result, error) {
	res, _, err := e.changedActions(ctx, req)
	return res, err
}

// changedActions is ChangedActions, with the graph the working tree's
// Changed Actions were worked out from; nil for the pushed commit's.
func (e *Engine) changedActions(ctx context.Context, req changes.Request) (*changes.Result, *project.Graph, error) {
	if e.cfg.Changes == nil {
		return nil, nil, ErrNoChanges
	}
	if req.DefaultBranch != "" {
		e.mu.Lock()
		e.branch = req.DefaultBranch
		e.mu.Unlock()
	}
	if req.Head == changes.HeadPushed {
		res, err := e.cfg.Changes.Changes(ctx, req, nil)
		return res, nil, err
	}
	select {
	case <-e.firstDone:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if err := e.whole(ctx); err != nil {
		return nil, nil, err
	}
	st := e.State()
	if req.Compile && (st.Stale || st.Err != nil || len(st.Errors) > 0) {
		return nil, nil, ErrStale
	}
	if st.Graph == nil || st.Graph.HasErrors() {
		return nil, nil, ErrStale
	}
	res, err := e.cfg.Changes.Changes(ctx, req, st.Graph.JSON)
	if err == nil {
		e.setChanges(st.Graph, res)
	}
	return res, st.Graph, err
}

// refreshChanges works out g's Changed Actions from cached bases only, once
// a client has named the default branch, and shows them if g is still the
// current graph. A base that isn't cached (a new merge-base) leaves none
// until a client asks for a compile.
func (e *Engine) refreshChanges(ctx context.Context, g *project.Graph) {
	e.mu.Lock()
	branch := e.branch
	e.mu.Unlock()
	if branch == "" || e.cfg.Changes == nil {
		return
	}
	res, err := e.cfg.Changes.Changes(ctx, changes.Request{DefaultBranch: branch, Head: changes.HeadWorktree}, g.JSON)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		res = nil // not a repository any more, a failed base, …: no markers
	}
	e.setChanges(g, res)
}

// setChanges shows res as g's Changed Actions, unless a newer compile has
// replaced g.
func (e *Engine) setChanges(g *project.Graph, res *changes.Result) {
	sig := ""
	if res != nil && (e.cfg.Impact != nil || e.cfg.Cost != nil) {
		sig = impact.Signature(res, g)
	}
	e.updateIf(func(s State) (State, bool) {
		if s.Graph != g || s.Changes == res {
			return s, false
		}
		s.Changes, s.ImpactSignature = res, sig
		return s, true
	})
}
