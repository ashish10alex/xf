package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/commanduse"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

const (
	// dryRunTimeout bounds one dry run, as the TUI's does.
	dryRunTimeout = time.Minute
	// tableTimeout bounds one table lookup, and tableMaxAge is how long
	// its answer is shown before the table is looked up again.
	tableTimeout = 30 * time.Second
	tableMaxAge  = time.Minute
	// maxRecent is how many recently selected actions are kept.
	maxRecent = 50
)

// projectState is the open project as the server follows it: the engine's
// state, the dry runs and table lookups the pages' Actions panes asked
// for, and the run.
type projectState struct {
	deps frontend.Deps
	hub  *hub
	save func(session.Session)
	tool string
	stop func()
	// name is what the project goes by (projectName), and worktree where
	// it is when in a worktree, fixed while it is open.
	name     string
	worktree worktreeOf
	// runEnded is told when a run ends, after mu is let go; nil tells no
	// one.
	runEnded func()
	// credsCheck looks for a new Google login before the project answers
	// from its cached dry runs (credentials.go); nil never looks.
	credsCheck func()
	// reopening is set while a reopen opens the project again (reopen.go).
	reopening atomic.Bool

	// searchMu orders replaces and their undo; lastReplace is what the
	// last replace wrote over (search.go).
	searchMu    sync.Mutex
	lastReplace []replaced

	// fetchMu orders the fetches a check starts with; fetchedAt is when the
	// last one ended (checks.go).
	fetchMu   sync.Mutex
	fetchedAt time.Time

	mu       sync.Mutex
	st       engine.State
	compiles int
	// failedAt and failedTook are when the latest compile, when it failed,
	// finished and how long it took, as seen here: the engine keeps the
	// times of the last good compile.
	failedAt   time.Time
	failedTook time.Duration
	dry        map[string]*dryEntry
	tables     map[bigquery.TableRef]*tableEntry
	// lastEditor is the file an editor last said is active.
	lastEditor string
	// focusAsked is the action the engine was last asked to compile ahead.
	focusAsked project.ActionID
	// sess is what to restore when xf next opens the project: the action
	// the page last selected and the recent ones.
	sess session.Session
	// bufs are the source pane's buffers (buffers.go), kept while the
	// project is open and never in sess.
	bufs buffersView
	// prodAsked is set once the prodCompilerOptions prompt was answered
	// or dismissed: it isn't shown again while xf web runs.
	prodAsked bool
	// editor changes the editor o opens files with; nil can't.
	editor *Editor
	// term is the terminal tab's shell, nil until a page first opens it;
	// termEnv is what its shell's environment gets added (Config.TermEnv).
	term    *terminal
	termEnv func() []string
	// install is the CLI the project opened without, nil for none;
	// cliFound is set once it has been found and the project recompiled.
	install *InstallCLI
	// coreFix is the dataform install the errors tab ran, until the
	// installed @dataform/core changes; nil for none.
	coreFix  *coreFix
	cliFound bool
	// bigquery runs scratch queries; nil can't. scratchWarnGB and
	// scratchConfirmGB are the dry run sizes the Scratch view colours and
	// asks at, 0 for the defaults.
	bigquery                              func(ctx context.Context) (*bigquery.Client, error)
	scratchWarnGB, scratchConfirmGB       float64
	scratchDirAbs                         string
	scratchDefProject, scratchDefLocation string
	standalone, startScratch              bool
	// startCheck is the Checks page's tab the next page to start opens on.
	startCheck string
	// sourceAutosaveMs is how long the source pane waits after an edit
	// before saving; 0 is off.
	sourceAutosaveMs int
	// vimKeys are the editor's vim keys (see Project.VimKeys).
	vimKeys map[string]string
	// diffStyle is Source Control's difference to start with (see
	// Project.DiffStyle).
	diffStyle string
	// pythonServer is pythonLanguageServer: "off" checks no Python files.
	pythonServer string
	// sqlfluffBin and sqlfluffConfig are the settings of the same names:
	// what formats SQL, and the config it formats with (ADR 0024).
	sqlfluffBin, sqlfluffConfig string
	// autoHidePane2 hides pane 2 beside a file with no action (see
	// Project.AutoHidePane2).
	autoHidePane2 bool
	// pane2Tab is the tab pane 2 starts on (see Project.Pane2Tab).
	pane2Tab string
	// wrapLines wraps long lines in the editors (see Project.WrapLines).
	wrapLines bool
	// openedWith are the values in use, when the project opened, of the
	// settings that apply on reopen: the page says which have changed.
	openedWith map[string]string
	meta       metaCache
	// changedRes are the Changed Actions R last listed, by head, which a
	// run of them runs.
	changedRes map[changes.Head]*changes.Result
	// base is the branch R, I and C compare with: deps.DefaultBranch until
	// b or the settings page changes it (see base.go).
	base string

	run runState
	// def is defer to prod (defer.go).
	def deferWeb
	// insp are the dependency inspector's runs (inspector.go).
	insp inspectorRuns
	// scm is Source Control's git command running and its output.
	scm scmState
	// py is the project's pyrefly (python.go).
	py pyState
	// forge is the branch's merge request and pipeline (forge.go), and
	// forgeCfg how to reach their host, nil for not at all.
	forge    forgeState
	forgeCfg *Forge
}

// dryEntry is a dry run's answer, which done says has come, at at. One
// older than engine.DryRunMaxAge is asked again when next shown, and
// loading says it is being asked, keeping the old answer until the new
// one comes.
type dryEntry struct {
	loading, done bool
	at            time.Time
	res           *bigquery.DryRun
	err           error
}

// stale says e's answer is old enough to ask again.
func (e *dryEntry) stale(now time.Time) bool {
	return e.done && !e.loading && now.Sub(e.at) >= engine.DryRunMaxAge
}

type tableEntry struct {
	loading, done bool
	at            time.Time
	tbl           *bigquery.Table
	err           error
}

func newProjectState(deps frontend.Deps, h *hub, save func(session.Session)) *projectState {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	p := &projectState{
		deps: deps, hub: h, save: save, tool: cmp.Or(deps.Backend, "dataform"),
		dry: map[string]*dryEntry{}, tables: map[bigquery.TableRef]*tableEntry{},
		sess: deps.Resume, base: deps.DefaultBranch,
	}
	p.deferResetLocked()
	p.sess.Root = deps.Root
	p.st.Compiling, p.st.Started = true, deps.Now()
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	states, unsubscribe := deps.Engine.Subscribe()
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case st, ok := <-states:
				if !ok {
					return
				}
				p.setState(st)
			}
		}
	}()
	if deps.Editor != nil {
		go p.followEditor(ctx)
	}
	go p.watchFiles(ctx)
	if deps.ChangedActions != nil {
		go p.primeChanges()
	}
	go p.loadHistory()
	go p.watchHead(ctx)
	go p.watchSCM(ctx)
	return p
}

func (p *projectState) close() {
	p.stop()
	p.forgeClose()
	p.mu.Lock()
	sess := p.sess
	run := p.run
	term := p.term
	p.mu.Unlock()
	if term != nil {
		term.close()
	}
	p.py.stop()
	if run.running && !run.api && run.cancel != nil {
		run.cancel() // as q does in the TUI; an API run carries on in GCP
	}
	if p.save != nil {
		p.save(sess)
	}
}

// primeChanges tells the engine the default branch once, so the markers
// show when a base compiled earlier today is on disk.
func (p *projectState) primeChanges() {
	ctx, cancel := context.WithTimeout(context.Background(), changedTimeout)
	defer cancel()
	_, _ = p.deps.ChangedActions(ctx, changesRequest(p.baseBranch(), false, false))
}

// setState takes the engine's state. A new compile drops the failed dry
// runs and table lookups, so they are tried again.
func (p *projectState) setState(st engine.State) {
	p.mu.Lock()
	deferChanged := st.Defer != p.st.Defer
	if deferChanged {
		// What was dry run read other tables: dry run it again.
		p.deferResetLocked()
		clear(p.dry)
	} else if st.Compiles != p.compiles || (st.Changes == nil) != (p.st.Changes == nil) {
		// A new compile, or Changed Actions for the Stale Deferral flags.
		p.deferExpireLocked()
	}
	if st.Compiles != p.compiles {
		p.compiles = st.Compiles
		p.forgetFailedDryRuns()
		p.expireTables(true)
		if !st.Compiling && (st.Err != nil || len(st.Errors) > 0) {
			p.failedAt, p.failedTook = time.Now(), 0
			if !st.Started.IsZero() {
				p.failedTook = p.failedAt.Sub(st.Started)
			}
		}
	}
	p.st = st
	v := p.stateViewLocked()
	p.mu.Unlock()
	p.hub.send("state", v)
	if deferChanged {
		p.hub.send("detail", nil)
	}
}

// forgetFailedDryRuns drops failed dry runs so they are tried again: after
// a recompile, or a run that may have built the tables they read (when
// the engine is told to forget its own first; see follow).
func (p *projectState) forgetFailedDryRuns() {
	for k, e := range p.dry {
		if e.done && e.err != nil && !e.loading {
			delete(p.dry, k)
		}
	}
}

// expireTables has tables looked up again when next shown: the failed
// ones, or all of them.
func (p *projectState) expireTables(failedOnly bool) {
	for _, e := range p.tables {
		if !failedOnly || e.err != nil {
			e.at = time.Time{}
		}
	}
}

// followEditor passes the editor's active file on to the pages. A
// CursorHold for the file already reported is dropped, so idling in the
// editor never overrides browsing here.
func (p *projectState) followEditor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-p.deps.Editor:
			if !ok {
				return
			}
			p.mu.Lock()
			if ev.Event == "CursorHold" && ev.File == p.lastEditor {
				p.mu.Unlock()
				continue
			}
			p.lastEditor = ev.File
			v := p.editorViewLocked()
			p.mu.Unlock()
			p.hub.send("editor", v)
		}
	}
}

// editorView is the editor's active file and its actions.
type editorView struct {
	File    string             `json:"file"`
	Actions []project.ActionID `json:"actions"`
}

func (p *projectState) editorViewLocked() editorView {
	v := editorView{File: p.lastEditor}
	if g := p.st.Graph; g != nil && v.File != "" {
		for _, a := range g.ActionsInFile(v.File) {
			v.Actions = append(v.Actions, a.ID)
		}
	}
	return v
}

// stateView is the engine's state as the status line and the panes need
// it.
type stateView struct {
	Compiling   bool      `json:"compiling"`
	Reason      string    `json:"reason,omitempty"`
	Started     time.Time `json:"started"`
	Compiles    int       `json:"compiles"`
	CompiledAt  time.Time `json:"compiledAt,omitzero"`
	Took        float64   `json:"took"`
	Stale       bool      `json:"stale,omitempty"`
	Saved       bool      `json:"saved,omitempty"`
	Outdated    string    `json:"outdated,omitempty"`
	Partial     bool      `json:"partial,omitempty"`
	Focus       string    `json:"focus,omitempty"`
	WatchErr    string    `json:"watchErr,omitempty"`
	HasGraph    bool      `json:"hasGraph"`
	Actions     int       `json:"actions"`
	CoreVersion string    `json:"coreVersion,omitempty"`
	// Notice is the compile's notice, such as why SQL is uncompiled.
	Notice string `json:"notice,omitempty"`
	// Errors counts the compile's errors (1 when it could not run), and
	// DryRunErrors the failed dry runs.
	Errors       int  `json:"errors"`
	DryRunErrors int  `json:"dryRunErrors"`
	DryRunAuth   bool `json:"dryRunAuth,omitempty"`
	// DryRunByFile are the failed dry runs by their action's file, which
	// the status line shows for the file the page shows.
	DryRunByFile map[string]fileDryRun `json:"dryRunByFile,omitempty"`
	// Changed are the Changed Actions, marked in the lists; nil until a
	// base has been compiled.
	Changed    []project.ActionID `json:"changed"`
	Comparison string             `json:"comparison,omitempty"`
	ImpactSig  string             `json:"impactSig,omitempty"`
	// Editor is the editor's active file, "" when none said.
	Editor string `json:"editor,omitempty"`
	// Defer is whether defer to prod is on, and DeferError why it isn't
	// applied, when it isn't.
	Defer      bool   `json:"defer,omitempty"`
	DeferError string `json:"deferError,omitempty"`
}

func (p *projectState) stateViewLocked() stateView {
	st := p.st
	v := stateView{Compiling: st.Compiling, Reason: st.Reason, Started: st.Started, Compiles: st.Compiles,
		CompiledAt: st.CompiledAt, Took: st.Took.Seconds(), Stale: st.Stale, Saved: st.Saved, Outdated: st.Outdated,
		Partial: st.Partial, Focus: string(st.Focus), ImpactSig: st.ImpactSignature, Editor: p.lastEditor,
		Defer: st.Defer, DeferError: p.def.err}
	if st.WatchErr != nil {
		v.WatchErr = st.WatchErr.Error()
	}
	switch {
	case st.Err != nil:
		v.Errors = 1
	default:
		v.Errors = len(st.Errors)
	}
	if g := st.Graph; g != nil {
		v.HasGraph, v.CoreVersion, v.Notice = true, g.CoreVersion, g.Notice
		for _, grp := range g.Grouped() {
			v.Actions += len(grp.Actions)
		}
	}
	if c := st.Changes; c != nil {
		v.Changed = []project.ActionID{}
		for _, ch := range c.Changed {
			v.Changed = append(v.Changed, ch.ID)
		}
		v.Comparison = c.Comparison()
	}
	errs, auth := p.dryRunErrorsLocked()
	v.DryRunErrors, v.DryRunAuth, v.DryRunByFile = len(errs), auth, dryRunsByFile(errs)
	return v
}

func (p *projectState) hasErrors() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Err != nil || len(p.st.Errors) > 0
}

func (p *projectState) graph() *project.Graph {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Graph
}

// projectBoot is what a page starts from in a project.
type projectBoot struct {
	Root    string `json:"root"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Target  string `json:"target,omitempty"`
	// Sep is the OS's path separator, to make absolute paths of files.
	Sep           string       `json:"sep"`
	DefaultBranch string       `json:"defaultBranch,omitempty"`
	Caps          capabilities `json:"caps"`
	Kinds         []string     `json:"kinds"`
	Notice        string       `json:"notice,omitempty"`
	Resume        resumeView   `json:"resume"`
	State         stateView    `json:"state"`
	Run           runView      `json:"run"`
	History       historyView  `json:"history"`
	EditorFile    editorView   `json:"editorFile"`
	// Scratch says how the page starts when it is about scratch queries.
	Scratch scratchBoot `json:"scratch"`
	// Check is the Checks page's tab the page opens on (xf impact --web).
	Check string `json:"check,omitempty"`
	// SourceAutosaveMs is how long the source pane waits after an edit
	// before saving, at least minSourceAutosaveMs; 0 is off.
	SourceAutosaveMs int `json:"sourceAutosaveMs,omitempty"`
	// VimKeys are the editor's vim keys, by config key.
	VimKeys map[string]string `json:"vimKeys"`
	// DiffStyle is how Source Control shows a difference to start with.
	DiffStyle string `json:"diffStyle"`
	// AutoHidePane2 hides pane 2 while the source pane shows a file with
	// no action.
	AutoHidePane2 bool `json:"autoHidePane2"`
	// Pane2Tab is the tab pane 2 starts on in a new browser tab: sql or
	// columns.
	Pane2Tab string `json:"pane2Tab"`
	// WrapLines wraps long lines in the page's editors.
	WrapLines bool `json:"wrapLines"`
	// CommandUses are how often the palette's commands were done, which
	// it lists most used first.
	CommandUses map[string]commanduse.Use `json:"commandUses"`
}

// capabilities says what this project can do, as the TUI hides the keys
// and panes of what it can't.
type capabilities struct {
	Run        bool `json:"run"`
	API        bool `json:"api"`
	APIDefault bool `json:"apiDefault"`
	Changed    bool `json:"changed"`
	Impact     bool `json:"impact"`
	Cost       bool `json:"cost"`
	Info       bool `json:"info"`
	Open       bool `json:"open"`
	Follow     bool `json:"follow"`
	History    bool `json:"history"`
	DryRun     bool `json:"dryRun"`
	Table      bool `json:"table"`
	// Editor says the page can change the editor o opens files with.
	Editor bool `json:"editor"`
	// Scratch says the page can make scratch queries and run them.
	Scratch bool `json:"scratch"`
	// Branches says B can list and switch the checkout's branches.
	Branches bool `json:"branches"`
	// Terminal says the drawer has a terminal tab (macOS and Linux).
	Terminal bool `json:"terminal"`
	// Watch says the page can follow a workflow xf did not start.
	Watch bool `json:"watch"`
	// Defer shows defer to prod's switch.
	Defer bool `json:"defer"`
	// Inspector says the dependency inspector can dry run and run its
	// queries (ADR 0033).
	Inspector bool `json:"inspector"`
}

type resumeView struct {
	Selected project.ActionID   `json:"selected,omitempty"`
	Recent   []project.ActionID `json:"recent"`
	// Buffers are the source pane's, which POST /api/buffers keeps.
	Buffers *buffersView `json:"buffers,omitempty"`
}

func (p *projectState) boot() *projectBoot {
	d := p.deps
	p.mu.Lock()
	defer p.mu.Unlock()
	d.DefaultBranch = p.base
	// Only the first page opens the Checks page; a reload stays where it was.
	defer func() { p.startCheck = "" }()
	return &projectBoot{
		Root: d.Root, Name: cmp.Or(p.name, lastElem(d.Root)), Backend: p.tool, Target: d.Target, Sep: string(filepath.Separator),
		DefaultBranch: d.DefaultBranch, Kinds: frontend.KindWords(p.tool), Notice: d.Notice,
		Caps: capabilities{
			Run: d.StartRun != nil, API: d.StartAPIRun != nil, APIDefault: d.RunAPI && d.StartAPIRun != nil,
			Changed: d.ChangedActions != nil, Impact: d.ColumnImpact != nil, Cost: d.QueryCost != nil,
			Info: d.Info != nil, Open: d.Open != nil, Follow: d.Editor != nil, History: d.Runs != nil,
			DryRun: d.DryRun != nil, Table: d.Table != nil, Editor: p.editor != nil, Scratch: p.bigquery != nil,
			Branches: d.Branches != nil, Terminal: termSupported, Watch: d.WatchWorkflow != nil, Defer: p.deferCaps(),
			Inspector: p.bigquery != nil && d.DryRun != nil && !p.standalone,
		},
		Resume: resumeView{Selected: p.sess.Selected, Recent: append([]project.ActionID{}, p.sess.Recent...),
			Buffers: &buffersView{List: append([]buffer{}, p.bufs.List...), Next: p.bufs.Next}},
		State:            p.stateViewLocked(),
		Run:              p.runViewLocked(true),
		History:          p.historyViewLocked(),
		EditorFile:       p.editorViewLocked(),
		Scratch:          scratchBoot{Standalone: p.standalone, Start: p.startScratch},
		Check:            p.startCheck,
		CommandUses:      d.CommandUses.Load(),
		SourceAutosaveMs: autosaveMs(p.sourceAutosaveMs),
		VimKeys:          p.vimKeys,
		DiffStyle:        diffStyleOf(p.diffStyle),
		AutoHidePane2:    p.autoHidePane2,
		Pane2Tab:         pane2TabOf(p.pane2Tab),
		WrapLines:        p.wrapLines,
	}
}

// commandUsed counts a use of the palette command a page did, and answers
// with the counts, the TUI's uses among them.
func (p *projectState) commandUsed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		http.Error(w, "bad request: no title", http.StatusBadRequest)
		return
	}
	// Counting is only a convenience, so a failure to keep it is not said.
	uses, _ := p.deps.CommandUses.Add(req.Title)
	writeJSON(w, uses)
}

func (s *Server) projectRoutes(mux *http.ServeMux) {
	route := func(pattern string, h func(p *projectState, w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if p := s.project(w, r); p != nil {
				h(p, w, r)
			}
		})
	}
	route("GET /api/action", (*projectState).action)
	route("POST /api/find", (*projectState).find)
	route("POST /api/command", (*projectState).commandUsed)
	route("POST /api/compile", func(p *projectState, w http.ResponseWriter, r *http.Request) {
		p.deps.Engine.Recompile("")
		writeJSON(w, okView{OK: true})
	})
	route("POST /api/session", (*projectState).session)
	route("POST /api/buffers", (*projectState).buffers)
	route("POST /api/buffers/files", (*projectState).bufferFiles)
	route("POST /api/open", (*projectState).open)
	route("GET /api/errors", (*projectState).errorsView)
	route("GET /api/target", (*projectState).target)
	route("GET /api/editor", (*projectState).editorChoices)
	route("POST /api/editor", (*projectState).setEditor)
	route("GET /api/info", (*projectState).info)
	route("POST /api/run", (*projectState).startRun)
	route("POST /api/run/cancel", (*projectState).cancelRun)
	route("POST /api/watch", (*projectState).watchWorkflow)
	route("POST /api/term/input", (*projectState).termInput)
	route("POST /api/term/fix", (*projectState).termFix)
	route("POST /api/term/resize", (*projectState).termResize)
	route("GET /api/term/state", (*projectState).termState)
	route("GET /api/git", (*projectState).git)
	route("GET /api/branch", (*projectState).branch)
	route("GET /api/branches", (*projectState).branches)
	route("POST /api/branch/switch", (*projectState).switchBranch)
	route("POST /api/branch/fetch", (*projectState).fetchBranches)
	route("GET /api/bases", (*projectState).bases)
	route("POST /api/base", (*projectState).setBase)
	route("GET /api/run", func(p *projectState, w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		v := p.runViewLocked(true)
		p.mu.Unlock()
		writeJSON(w, v)
	})
	route("GET /api/history", func(p *projectState, w http.ResponseWriter, r *http.Request) {
		p.loadHistory()
		p.mu.Lock()
		v := p.historyViewLocked()
		p.mu.Unlock()
		writeJSON(w, v)
	})
	route("GET /api/history/jobs", (*projectState).historyJobs)
	route("POST /api/history/clear", (*projectState).clearHistory)
	route("POST /api/history/refresh", (*projectState).refreshHistory)
	route("POST /api/changed", (*projectState).changed)
	route("POST /api/changed/preview", (*projectState).changedPreview)
	route("GET /api/tags", (*projectState).tags)
	route("POST /api/impact", (*projectState).impact)
	route("POST /api/cost", (*projectState).cost)
	route("POST /api/prod", (*projectState).prod)
	route("POST /api/defer", (*projectState).setDefer)
	route("POST /api/defer/proxies/remove", (*projectState).removeProxies)
	route("GET /api/defer/refs", (*projectState).refLabels)
	route("GET /api/inspector", (*projectState).inspector)
	route("POST /api/inspector/state", (*projectState).inspectorState)
	route("POST /api/inspector/added", (*projectState).inspectorAdded)
	route("POST /api/inspector/dryrun", (*projectState).inspectorDryRun)
	route("POST /api/inspector/run", (*projectState).inspectorRun)
	route("POST /api/inspector/cancel", (*projectState).inspectorCancel)
	route("GET /api/inspector/presets", (*projectState).inspectorPresets)
	route("POST /api/inspector/presets", (*projectState).inspectorSavePreset)
	route("POST /api/inspector/presets/delete", (*projectState).inspectorDeletePreset)
	route("GET /api/scratch", (*projectState).scratchList)
	route("GET /api/scratch/file", (*projectState).scratchGet)
	route("POST /api/scratch/create", (*projectState).scratchCreate)
	route("POST /api/scratch/save", (*projectState).scratchSave)
	route("POST /api/scratch/delete", (*projectState).scratchDelete)
	route("POST /api/scratch/dryrun", (*projectState).scratchDryRun)
	route("POST /api/scratch/run", (*projectState).scratchRun)
	route("GET /api/scratch/job", (*projectState).scratchJob)
	route("GET /api/scratch/rows", (*projectState).scratchRows)
	route("POST /api/scratch/cancel", (*projectState).scratchCancel)
	route("GET /api/scratch/csv", (*projectState).scratchCSV)
	route("GET /api/scratch/defaults", (*projectState).scratchDefaultsHandler)
	route("POST /api/scratch/target", (*projectState).scratchTarget)
	route("POST /api/scratch/filter", (*projectState).scratchFilter)
	route("GET /api/scratch/runs", (*projectState).scratchRunsList)
	route("POST /api/scratch/runs", (*projectState).scratchRunsAdd)
	route("GET /api/scratch/meta/models", (*projectState).scratchModels)
	route("GET /api/scratch/meta/datasets", (*projectState).scratchDatasets)
	route("GET /api/scratch/meta/tables", (*projectState).scratchTables)
	route("GET /api/scratch/meta/table", (*projectState).scratchTable)
	route("POST /api/scratch/meta/refresh", (*projectState).scratchRefresh)
	route("GET /api/source", (*projectState).source)
	route("POST /api/source/save", (*projectState).sourceSave)
	route("POST /api/format", (*projectState).formatText)
	route("POST /api/source/complete", (*projectState).sourceComplete)
	route("POST /api/source/refs", (*projectState).sourceRefs)
	route("POST /api/source/definition", (*projectState).sourceDefinition)
	route("POST /api/source/include", (*projectState).sourceInclude)
	route("GET /api/source/diagnostics", (*projectState).sourceDiagnostics)
	route("POST /api/source/symbols", (*projectState).sourceSymbols)
	route("POST /api/source/findfile", (*projectState).sourceFindFile)
	route("POST /api/source/paths", (*projectState).sourcePaths)
	route("POST /api/markdown", (*projectState).markdownPreview)
	// An <img> sends no header, so its URL names the project.
	mux.HandleFunc("GET /api/markdown/image", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(projectHeader, r.URL.Query().Get("project"))
		if p := s.project(w, r); p != nil {
			p.markdownImage(w, r)
		}
	})
	route("GET /api/files", (*projectState).files)
	route("GET /api/files/action", (*projectState).actionOfFile)
	route("POST /api/files/new", (*projectState).newFile)
	route("POST /api/files/rename", (*projectState).renameFile)
	route("POST /api/files/delete", (*projectState).deleteFile)
	route("POST /api/search", (*projectState).search)
	route("POST /api/search/replace", (*projectState).replace)
	route("POST /api/search/undo", (*projectState).undoReplace)
	scmRoutes(route)
	forgeRoutes(route)
	pythonRoutes(route)
}

type okView struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// Kind tells some answers apart: "editor" when o needs another
	// editor than the one it has, "dirty" when a worktree to remove has
	// changes it would lose.
	Kind string `json:"kind,omitempty"`
}

// session keeps the page's selection, for when xf next opens the project.
func (p *projectState) session(w http.ResponseWriter, r *http.Request) {
	var req resumeView
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Recent) > maxRecent {
		req.Recent = req.Recent[:maxRecent]
	}
	p.mu.Lock()
	p.sess.Selected, p.sess.Recent = req.Selected, req.Recent
	p.mu.Unlock()
	writeJSON(w, okView{OK: true})
}

// find is the palette's search (see frontend.FindActions).
func (p *projectState) find(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string             `json:"query"`
		First []project.ActionID `json:"first"`
		Limit int                `json:"limit"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.mu.Lock()
	g, changed := p.st.Graph, p.changedSetLocked()
	p.mu.Unlock()
	type findView struct {
		Items   []actionRef `json:"items"`
		Found   int         `json:"found"`
		Total   int         `json:"total"`
		BadKind string      `json:"badKind,omitempty"`
	}
	if g == nil {
		writeJSON(w, findView{Items: []actionRef{}})
		return
	}
	var first []*project.Action
	for _, id := range req.First {
		if a := g.Actions[id]; a != nil && a.Kind != project.KindUnknown {
			first = append(first, a)
		}
	}
	found, bad := frontend.FindActions(g, p.tool, req.Query, first)
	v := findView{Items: []actionRef{}, Found: len(found), BadKind: bad}
	for _, a := range g.Actions {
		if a.Kind != project.KindUnknown {
			v.Total++
		}
	}
	limit := cmp.Or(req.Limit, 500)
	for _, a := range found[:min(len(found), limit)] {
		ref := refOf(a)
		ref.Changed = changed[a.ID]
		v.Items = append(v.Items, ref)
	}
	writeJSON(w, v)
}

func (p *projectState) changedSetLocked() map[project.ActionID]bool {
	set := map[project.ActionID]bool{}
	if c := p.st.Changes; c != nil {
		for _, ch := range c.Changed {
			set[ch.ID] = true
		}
	}
	return set
}

// detail is a line under an action's row in the Actions pane.
type detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Tone is "", "dim", "warn", "error" or "ok".
	Tone string `json:"tone,omitempty"`
	// Dry marks a dry run's line, which a compile makes out of date.
	Dry bool `json:"dry,omitempty"`
}

// paneAction is a row of the Actions pane and its details.
type paneAction struct {
	actionRef
	Details []detail `json:"details"`
}

// lineageView is the selected action's dependencies and dependents.
type lineageView struct {
	Up   []actionRef `json:"up"`
	Down []actionRef `json:"down"`
}

// sqlView is the SQL pane's text: each line's tokens, as chroma's short
// class names and their text, and the plain lines the cursor moves in.
type sqlView struct {
	Lines [][][2]string `json:"lines"`
	Plain []string      `json:"plain"`
	// Empty is set when the action has no SQL: Plain is the one line
	// saying so, shown dim and without line numbers.
	Empty bool `json:"empty,omitempty"`
}

// columnsView is the columns the selected action's query returns, or why
// there are none.
type columnsView struct {
	Fields []bigquery.Field `json:"fields"`
	Reason string           `json:"reason,omitempty"`
	Failed bool             `json:"failed,omitempty"`
}

// runPreview is what r would run with the page's toggles.
type runPreview struct {
	Actions []project.ActionID `json:"actions"`
	Label   string             `json:"label,omitempty"`
	// Name is the bare name of a run of one action, for narrow places.
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	// Why is why nothing runs: "views can't be run".
	Why string `json:"why,omitempty"`
	// Defer says what the run reads from prod with defer to prod on:
	// "reads 2 upstream tables from prod".
	Defer string `json:"defer,omitempty"`
}

// actionView is everything the panes show of the selected action.
type actionView struct {
	actionRef
	Target string `json:"target"`
	// Canonical is the target before a prefix or suffix (Dataform's
	// canonicalTarget), when one changed it; the target section marks what
	// they add.
	Canonical      string `json:"canonical,omitempty"`
	Schema         string `json:"schema"`
	FileAbs        string `json:"fileAbs,omitempty"`
	HasIncremental bool   `json:"hasIncremental,omitempty"`
	// At is where the action is defined in its file: the top of a sqlx
	// file, the name in a JavaScript file's publish() or declare().
	At placeView `json:"at"`
	// SQLNote is why the SQL isn't the current compile's ("outdated,
	// compiling…"), "" when it is; Uncompiled is set for a project whose
	// SQL is templates BigQuery can't plan.
	SQLNote    string `json:"sqlNote,omitempty"`
	Uncompiled bool   `json:"uncompiled,omitempty"`
	// Anchor is the action whose siblings the Actions pane lists.
	Anchor project.ActionID `json:"anchor"`
	Pane   []paneAction     `json:"pane"`
	// Alone is what ] and [ say when the pane lists the action alone.
	Alone string `json:"alone,omitempty"`
	// Externals is the declarations table, for a file of declarations or
	// dbt sources alone: all of them, in file order (ADR 0015).
	Externals []externalRow `json:"externals,omitempty"`
	// TableType is a declaration's or source's table type in BigQuery
	// (TABLE, VIEW, …), once looked up: pane 2 opens a view on its SQL and
	// anything else on its columns.
	TableType string      `json:"tableType,omitempty"`
	Auth      string      `json:"auth,omitempty"`
	Lineage   lineageView `json:"lineage"`
	SQL       sqlView     `json:"sql"`
	Columns   columnsView `json:"columns"`
	Run       runPreview  `json:"run"`
	// Defer is what defer to prod reads the action's upstream tables
	// from; nil while it is off.
	Defer *deferView `json:"defer,omitempty"`
}

// action answers with the panes' view of an action: ?id (or, once id is
// gone, the action ?file defines in its place), the anchor the
// Actions pane stays on, incremental (1 shows an incremental table's
// incremental SQL), and the run toggles (selectedOnly, deps, dependents,
// full, api). It also has the pane's actions dry-run and their tables
// looked up, and the engine compile the action ahead of the rest.
func (p *projectState) action(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	flag := func(name string) bool { b, _ := strconv.ParseBool(q.Get(name)); return b }
	incremental := q.Get("incremental") != "0"
	p.checkCreds()
	p.mu.Lock()
	g := p.st.Graph
	if g == nil {
		p.mu.Unlock()
		http.Error(w, "no compile result yet", http.StatusConflict)
		return
	}
	a := g.Actions[project.ActionID(q.Get("id"))]
	if a == nil || a.Kind == project.KindUnknown {
		// Renamed by an edit to its file's config: the action that file
		// defines now, which the page then selects.
		a = frontend.RenamedAction(g, project.ActionID(q.Get("id")), q.Get("file"))
	}
	if a == nil {
		p.mu.Unlock()
		http.Error(w, "no such action", http.StatusNotFound)
		return
	}
	anchor := a
	if b := g.Actions[project.ActionID(q.Get("anchor"))]; b != nil && slices.Contains(g.Siblings(b), a) {
		anchor = b
	}
	pane := g.Siblings(anchor)
	if a.ID != p.focusAsked {
		p.focusAsked = a.ID
		p.deps.Engine.Focus(a.ID)
	}
	ready, starts := p.deferStartsLocked(pane)
	// What is dry run and shown reads the Deferred Actions from prod; it
	// is dry run once that is known.
	dpane := make([]*project.Action, len(pane))
	for i, b := range pane {
		dpane[i] = p.deferredLocked(b)
	}
	da := p.deferredLocked(a)
	starts = append(starts, p.startLookupsLocked(g, dpane, ready)...)

	v := actionView{actionRef: refOf(a), Target: string(a.ID), Schema: a.Target.Schema, HasIncremental: a.HasIncrementalVariant(),
		SQLNote: p.sqlNoteLocked(a), Uncompiled: g.Notice != "", Anchor: anchor.ID}
	if c := a.CanonicalTarget; c.Name != "" && c != a.Target {
		v.Canonical = string(c.ID())
	}
	if len(pane) < 2 {
		v.Alone = frontend.AloneMessage(g, a)
	}
	externals, externalActs := p.externalsLocked(g, a)
	var viewQuery string
	if e := p.externalTableLocked(a); a.Kind.External() && e != nil && e.done && e.tbl != nil {
		v.TableType, viewQuery = e.tbl.Type, e.tbl.ViewQuery
	}
	if a.FileName != "" {
		v.FileAbs = filepath.Join(p.deps.Root, filepath.FromSlash(a.FileName))
	}
	changed := p.changedSetLocked()
	v.Changed = changed[a.ID]
	auth := false
	for i, b := range pane {
		ref := refOf(b)
		ref.Changed = changed[b.ID]
		details, failed := p.detailsLocked(g, dpane[i], incremental)
		auth = auth || failed
		v.Pane = append(v.Pane, paneAction{actionRef: ref, Details: details})
	}
	if auth {
		v.Auth = "dry runs need Google credentials: " + bigquery.AuthHint + ", then c"
	}
	for _, d := range g.Dependencies(a.ID) {
		v.Lineage.Up = append(v.Lineage.Up, refOf(d))
	}
	for _, d := range g.Dependents(a.ID) {
		v.Lineage.Down = append(v.Lineage.Down, refOf(d))
	}
	v.Lineage.Up, v.Lineage.Down = nonNil(v.Lineage.Up), nonNil(v.Lineage.Down)
	v.Columns = p.columnsLocked(g, da, incremental)
	v.Defer = p.deferViewLocked(g, a)
	v.Run = p.runPreviewLocked(g, a, pane, flag("selectedOnly"), runToggles{Deps: flag("deps"), Dependents: flag("dependents"),
		Full: flag("full"), API: flag("api")})
	p.mu.Unlock()
	for _, start := range starts {
		go start()
	}
	v.SQL = sqlOf(da, incremental)
	if viewQuery != "" {
		v.SQL = viewSQL(viewQuery)
	}
	if a.FileName != "" {
		v.At = p.actionPlace(a)
	}
	if externals != nil {
		v.Externals = p.placeExternals(externals, externalActs)
	}
	writeJSON(w, v)
}

// placeView is a place in a file: line counts from 0, col is a UTF-16
// column in it.
type placeView struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

// actionPlace is where a is defined in its file on disk, as o opens it.
func (p *projectState) actionPlace(a *project.Action) placeView {
	var src []byte
	np := navigate.Project{Root: p.deps.Root, ReadFile: func(path string) ([]byte, error) {
		b, err := os.ReadFile(path)
		src = b
		return b, err
	}}
	loc := navigate.ActionLocation(np, a)
	v := placeView{Line: loc.Line}
	if lines := strings.Split(string(src), "\n"); loc.Line < len(lines) {
		v.Col = unitsAt(lines[loc.Line], min(loc.Col, len(lines[loc.Line])))
	}
	return v
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// hasTable reports whether a's target is a table or view in BigQuery.
func hasTable(a *project.Action) bool { return a.Kind.HasTable() && a.Target.Database != "" }

// sqlNoteLocked says what a's SQL is when it isn't the current compile's
// (see the TUI's sqlNote).
func (p *projectState) sqlNoteLocked(a *project.Action) string {
	switch {
	case a.SQL == project.SQLCompiled:
		return ""
	case a.SQL == project.SQLOutdated && p.st.Partial:
		return "outdated, compiling…"
	case a.SQL == project.SQLOutdated:
		return "outdated"
	case p.st.Partial:
		return "compiling…"
	}
	return "not compiled"
}

func dryRunKey(project, location, sql string) string {
	return project + "\x00" + location + "\x00" + sql
}

// startLookupsLocked starts the dry runs and table lookups of actions
// that have no answer yet (or an old one), returning what to run once the
// lock is let go.
func (p *projectState) startLookupsLocked(g *project.Graph, actions []*project.Action, dry bool) []func() {
	var starts []func()
	if dry {
		starts = p.startDryRunsLocked(g, actions)
	}
	if lookup := p.deps.Table; lookup != nil {
		now := p.deps.Now()
		for _, a := range actions {
			if !hasOutputTable(a) {
				continue
			}
			ref := bigquery.TableRef{Project: a.Target.Database, Dataset: a.Target.Schema, Table: a.Target.Name}
			e := p.tables[ref]
			if e == nil {
				e = &tableEntry{}
				p.tables[ref] = e
			}
			if e.loading || e.done && now.Sub(e.at) < tableMaxAge {
				continue
			}
			e.loading = true
			starts = append(starts, func() {
				ctx, cancel := context.WithTimeout(context.Background(), tableTimeout)
				defer cancel()
				tbl, err := lookup(ctx, ref.Project, ref.Dataset, ref.Table)
				p.mu.Lock()
				e.loading, e.done, e.at, e.tbl, e.err = false, true, p.deps.Now(), tbl, err
				p.mu.Unlock()
				p.hub.send("detail", nil)
			})
		}
	}
	return starts
}

// startDryRunsLocked starts the dry runs of actions that have no answer
// yet (or an old one), returning what to run once the lock is let go.
func (p *projectState) startDryRunsLocked(g *project.Graph, actions []*project.Action) []func() {
	var starts []func()
	if dry := p.deps.DryRun; dry != nil && p.st.Outdated == "" && g.Notice == "" {
		now := p.deps.Now()
		for _, a := range actions {
			proj, location := g.DryRunTarget(a)
			if proj == "" || p.sqlNoteLocked(a) != "" {
				continue
			}
			for _, dq := range a.DryRunQueries() {
				key := dryRunKey(proj, location, dq.SQL)
				e := p.dry[key]
				if e == nil {
					e = &dryEntry{}
					p.dry[key] = e
				} else if !e.stale(now) {
					continue
				}
				e.loading = true
				sql := dq.SQL
				starts = append(starts, func() {
					ctx, cancel := context.WithTimeout(context.Background(), dryRunTimeout)
					defer cancel()
					res, err := dry(ctx, proj, location, sql)
					p.mu.Lock()
					e.loading, e.done, e.at, e.res, e.err = false, true, p.deps.Now(), res, err
					v := p.stateViewLocked()
					p.mu.Unlock()
					p.hub.send("detail", nil)
					p.hub.send("state", v)
				})
			}
		}
	}
	return starts
}

// detailsLocked are the lines under a's row (see the TUI's actionDetails):
// how it changed on the branch, its dry runs, when its table was last
// updated and its partitioning. auth reports a dry run that failed for
// want of credentials.
func (p *projectState) detailsLocked(g *project.Graph, a *project.Action, incremental bool) (details []detail, auth bool) {
	if c, ok := p.st.Changes.Find(a.ID); ok {
		details = append(details, detail{Label: "changed", Value: c.ReasonsText() + " " + p.st.Changes.Comparison(), Tone: "warn"})
	}
	if p.deps.DryRun != nil {
		proj, location := g.DryRunTarget(a)
		for _, q := range a.DryRunQueries() {
			e := p.dry[dryRunKey(proj, location, q.SQL)]
			d := detail{Label: cmp.Or(q.Label, "dry run"), Dry: true}
			dim := q.Label != "" && (q.Label == "incremental") != incremental
			switch note := p.sqlNoteLocked(a); {
			case g.Notice != "":
				d.Value, d.Tone = "not compiled", "dim"
			case note != "":
				d.Value, d.Tone = note, "dim"
			case e == nil || !e.done:
				d.Value, d.Tone = "running…", "dim"
			case errors.Is(e.err, bigquery.ErrAuth):
				auth = true
				d.Value, d.Tone = "no credentials", "warn"
			case e.err != nil:
				msg := e.err.Error()
				if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
					msg = apiErr.Message
				}
				d.Value, d.Tone = "✗ "+firstLine(msg), "error"
			default:
				text, warn := frontend.FormatDryRun(e.res)
				d.Value = text
				if warn {
					d.Tone = "warn"
				}
			}
			if dim && d.Tone == "" {
				d.Tone = "dim"
			}
			details = append(details, d)
		}
	}
	if d, ok := p.updatedLocked(a); ok {
		details = append(details, d)
	}
	if a.PartitionBy != "" {
		details = append(details, detail{Label: "partition", Value: a.PartitionBy})
	}
	return nonNil(details), auth
}

// updatedLocked is the line saying when a's table was last updated:
// "2026-10-07 11:27 · 3h ago", an error tone when that was before today.
// An operation with hasOutput has one too.
func (p *projectState) updatedLocked(a *project.Action) (detail, bool) {
	if p.deps.Table == nil || !hasOutputTable(a) {
		return detail{}, false
	}
	d := detail{Label: "updated", Tone: "dim"}
	e := p.tables[bigquery.TableRef{Project: a.Target.Database, Dataset: a.Target.Schema, Table: a.Target.Name}]
	var apiErr *bigquery.APIError
	switch {
	case e == nil || !e.done:
		d.Value = "checking…"
	case errors.As(e.err, &apiErr) && apiErr.Status == http.StatusNotFound:
		d.Value = "N/A"
	case errors.Is(e.err, bigquery.ErrAuth):
		d.Value, d.Tone = "no credentials", "warn"
	case apiErr != nil:
		d.Value, d.Tone = "✗ "+firstLine(apiErr.Message), "error"
	case e.err != nil:
		d.Value, d.Tone = "✗ "+firstLine(e.err.Error()), "error"
	case e.tbl.LastModified.IsZero():
		d.Value = "N/A"
	default:
		t, now := e.tbl.LastModified.Local(), p.deps.Now().Local()
		d.Value, d.Tone = t.Format("2006-01-02 15:04")+" · "+frontend.RelTime(now, t), ""
		if t.Format(time.DateOnly) < now.Format(time.DateOnly) {
			d.Tone = "error" // not updated today
		}
	}
	return d, true
}

// columnsLocked is a's columns from its dry run's schema, or why there
// are none (see the TUI's columns).
func (p *projectState) columnsLocked(g *project.Graph, a *project.Action, incremental bool) columnsView {
	why := func(reason string, failed bool) columnsView {
		return columnsView{Fields: []bigquery.Field{}, Reason: reason, Failed: failed}
	}
	switch {
	case a.Kind.External():
		return p.externalColumnsLocked(a)
	case p.deps.DryRun == nil:
		return why("no dry runs here, so no columns", false)
	case g.Notice != "":
		return why("not compiled: BigQuery can't plan this project's SQL", false)
	}
	queries := a.DryRunQueries()
	if len(queries) == 0 {
		return why(a.Kind.String()+": nothing to dry run, so no columns", false)
	}
	q := queries[0]
	for _, x := range queries {
		if x.Label != "" && (x.Label == "incremental") == incremental {
			q = x
		}
	}
	if p.st.Outdated != "" || p.sqlNoteLocked(a) != "" {
		return why("waiting for the compile…", false)
	}
	proj, location := g.DryRunTarget(a)
	if proj == "" {
		return why("no project to dry run in", false)
	}
	e := p.dry[dryRunKey(proj, location, q.SQL)]
	switch {
	case e == nil || !e.done:
		return why("dry run…", false)
	case errors.Is(e.err, bigquery.ErrAuth):
		return why("no credentials for the dry run — press e for details", true)
	case e.err != nil:
		msg := e.err.Error()
		if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
			msg = apiErr.Message
		}
		return why("✗ "+firstLine(msg)+" — press e for details", true)
	}
	fields := frontend.FlattenFields(e.res.Schema)
	switch {
	case len(fields) > 0:
		// A column's description from the action's config, where BigQuery
		// has none.
		desc := map[string]string{}
		for _, c := range a.Columns {
			desc[strings.Join(c.Path, ".")] = c.Description
		}
		for i := range fields {
			if fields[i].Description == "" {
				fields[i].Description = desc[fields[i].Name]
			}
		}
		return columnsView{Fields: fields}
	case a.Kind == project.KindOperation || e.res.StatementType == "SCRIPT":
		return why(a.Kind.String()+": a dry run of a script has no schema", false)
	}
	return why("the dry run returned no schema", false)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// open asks the editor to open an action's file at the line defining it,
// as o does.
func (p *projectState) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID project.ActionID `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, p.openAction(req.ID))
}

func (p *projectState) openAction(id project.ActionID) okView {
	g := p.graph()
	if p.deps.Open == nil {
		return okView{Message: "o needs an editor"}
	}
	var a *project.Action
	if g != nil {
		a = g.Actions[id]
	}
	if a == nil || a.FileName == "" {
		return okView{Message: "no file to open"}
	}
	loc := navigate.ActionLocation(navigate.Project{Root: p.deps.Root}, a)
	opening, err := p.deps.Open(loc.File, loc.Line)
	switch {
	case err != nil:
		return okView{Message: "could not open " + a.FileName + ": " + err.Error()}
	case opening.Run != nil || opening.Leave:
		// A terminal editor needs the terminal, which the browser hasn't.
		msg := "o opens " + a.FileName + " in a terminal editor, which xf web can't run: open the project in an editor that runs xf lsp, or set the editor setting to a GUI editor"
		if p.editor != nil {
			return okView{Message: msg, Kind: "editor"}
		}
		return okView{Message: msg}
	}
	return okView{OK: true, Message: "opened " + a.FileName + " in your editor"}
}

// editorChoicesView is the editor o uses and the ones it could.
type editorChoicesView struct {
	Current string         `json:"current"`
	From    string         `json:"from"`
	Usable  bool           `json:"usable"`
	Choices []EditorChoice `json:"choices"`
}

func (p *projectState) editorChoices(w http.ResponseWriter, r *http.Request) {
	if p.editor == nil {
		http.Error(w, "the editor can't be changed here", http.StatusNotFound)
		return
	}
	v := editorChoicesView{Choices: nonNil(p.editor.Choices())}
	v.Current, v.From, v.Usable = p.editor.Current()
	writeJSON(w, v)
}

// setEditor makes a command the editor o uses, saved for every project or
// for this one, and opens an action's file with it when asked.
func (p *projectState) setEditor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string           `json:"command"`
		Project bool             `json:"project"`
		Open    project.ActionID `json:"open"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.editor == nil {
		writeError(w, errors.New("the editor can't be changed here"))
		return
	}
	command := strings.TrimSpace(req.Command)
	file, err := p.editor.Set(command, req.Project)
	if err != nil {
		writeError(w, err)
		return
	}
	p.mu.Lock()
	frontend.SetInfoSetting(p.deps.Info, "editor", command, file)
	if p.deps.Info != nil {
		p.deps.Info.EditorCommand = command
	}
	p.mu.Unlock()
	v := okView{OK: true, Message: "o opens files with " + command + " now (saved to " + file + ")"}
	if req.Open != "" {
		if opened := p.openAction(req.Open); !opened.OK {
			v.Message += "; " + opened.Message
		} else {
			v.Message = opened.Message + " · saved " + command + " to " + file
		}
	}
	writeJSON(w, v)
}
