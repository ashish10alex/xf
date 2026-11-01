// Package ui is the Bubble Tea front end of xf.
package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

type paneID int

// Pane numbers match the keys that focus them: 0 is the SQL, the main
// view, and the rest count down the screen, as in lazygit.
const (
	paneSQL paneID = iota
	paneActions
	paneLineage
	paneRun
	paneLog
)

const (
	// minHeight is what the focused pane alone needs; below what the
	// stacked panes need, xf shows only the focused pane (see geometry).
	minWidth, minHeight = 40, 10
	runPaneH            = 3
	// lineageClosedH is the collapsed Lineage pane: its border, titled with
	// the entry counts.
	lineageClosedH = 2
	// sqlMinRows is the SQL the Actions and Lineage panes leave when they
	// grow, unless the terminal is too short for it.
	sqlMinRows = 8
	// maxRecent is how many recently selected actions the palette remembers.
	maxRecent = 50
)

// Model is the root Bubble Tea model.
type Model struct {
	// started is when the model was made, for Deps.StartupQuiet.
	started time.Time
	deps    Deps
	keys    keyMap
	styles  styles
	help    help.Model

	width, height int
	focus         paneID

	// compile state, as the engine last reported it
	events         <-chan engine.State
	graph          *project.Graph // last good compile (or the first compile, errors and all)
	compiles       int            // compiles finished so far; a higher count is a new result
	compiling      bool
	compileReason  string // shown while compiling, e.g. "file changed"
	compileStarted time.Time
	animating      bool // the compile animation's tick loop is running
	animFrame      int
	initCmd        tea.Cmd
	compiledAt     time.Time
	compileTook    time.Duration
	stale          bool // the latest compile failed; graph is from an earlier one
	saved          bool // graph is a compile an earlier xf serve saved
	// outdated says why the saved graph may not match the files, while a
	// compile runs to replace it.
	outdated string
	// partial is set while the graph is the project as parsed, its compile
	// still under way (engine.State.Partial): each action then says how far
	// its own SQL is compiled. focusAsked is the action the engine was last
	// asked to compile ahead of the rest.
	partial      bool
	focusAsked   project.ActionID
	errors       []project.CompilationError
	compileErr   error
	showErrors   bool
	errorsView   errorsOverlay // the e overlay
	info         infoOverlay   // the , overlay
	watchNoticed bool          // the footer has said why files aren't watched
	graphNotice  string        // the compile's notice the footer last showed
	// changes are the Changed Actions as the engine last reported them,
	// marked in the palette and the Actions pane; nil until a base has
	// been compiled (R).
	changes *changes.Result
	changed changedOverlay // the R overlay
	impact  impactOverlay  // the I overlay
	cost    costOverlay    // the C overlay
	prod    prodPrompt     // asks for prodCompilerOptions before I or C
	// impactSig is the column impact signature of changes (see
	// engine.State.ImpactSignature).
	impactSig string

	// theme is auto, light or dark, and termDark whether the terminal's
	// background is dark, which auto follows.
	theme         string
	termDark      bool
	palette       palette
	actionsOffset int                     // first visible line of the Actions pane
	dryRuns       map[string]*dryRunEntry // by dryRunKey
	tables        map[bigquery.TableRef]*tableEntry
	lineage       lineagePane
	lineageOpen   bool // the Lineage pane is expanded (O toggles it)
	sql           sqlPane
	cols          columnsView // the SQL pane's other view (S swaps them)
	selected      *project.Action
	// paneOf is the action whose siblings the Actions pane lists when that
	// isn't the selected one's (see selectInPane); "" for the selected one.
	paneOf      project.ActionID
	recent      []project.ActionID // selected actions, most recent first
	incremental bool               // show the incremental variant of incremental tables

	// run state
	flags     runFlags
	apiGit    apiGitState
	running   bool
	run       *project.Run
	runAPI    bool          // the run in flight (or last run) is in GCP, through the Dataform API
	runURL    func() string // the last run's console URL, "" until known; nil for local runs
	cancelRun context.CancelFunc
	runLabel  string // what the run in flight (or last run) runs: "analytics.orders", "5 changed actions"
	runShort  string // the same for the status bar: "orders"
	log       logPane
	runTable  runTable // a run's actions and their cost, shown in the log pane
	// jobsFind is the box / types a run table's search in, while
	// jobsTyping (see handleJobsKey).
	jobsFind   textinput.Model
	jobsTyping bool
	logTab     logTab // the log pane's tab, when it has them (see logtabs.go)
	history    historyView
	// confirmClear is set while X asks whether to forget the kept runs.
	confirmClear bool
	showLog      bool
	cancelling   bool              // ctrl+c asked the run to stop; another ctrl+c force-kills
	confirmQuit  bool              // q pressed; q again or y quits, cancelling a local run
	confirmFull  []*project.Action // r pressed with full-refresh on; y runs these actions
	confirmTags  []string          // the same for a run of tags (see tagsview.go)
	tags         tagsOverlay       // the T overlay
	branches     branchOverlay     // the B overlay
	git          gitStatus         // the status line's branch
	// heads says a watched git file changed (see gitbranch.Repo.Watch);
	// nil without a watch.
	heads <-chan struct{}

	// editor state: the selection follows the file open in nvim
	following     bool
	lastEditor    string // last file the editor reported
	pendingEditor string // a file to show once following resumes or a graph arrives

	zoomed  bool // the focused pane fills the screen
	sqlDrag bool // the mouse button went down in the SQL pane and is held

	flash string // one-off message shown in the footer until the next key
	// leaving is what the editor xf quit for is to open (see Leaving).
	leaving *editorLocation
	keyHelp helpOverlay // the "?" overlay
	search  sqlSearch   // the zoomed SQL pane's "/" query line

	// copiedRel is the action whose relative path the last key copied, so Y
	// again copies the absolute one.
	copiedRel project.ActionID
}

// New builds the model and subscribes to the engine, whose first compile is
// shown as under way until it reports otherwise.
func New(deps Deps) Model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	m := Model{
		deps:        deps,
		keys:        newKeyMap(),
		theme:       validTheme(deps.Theme),
		termDark:    true,
		styles:      themeStyles(validTheme(deps.Theme), true),
		help:        help.New(),
		focus:       paneSQL,
		palette:     newPalette(cmp.Or(deps.Backend, "dataform")),
		tags:        newTagsOverlay(),
		branches:    newBranchOverlay(),
		prod:        newProdPrompt(),
		keyHelp:     newHelpOverlay(),
		info:        newInfoOverlay(),
		cols:        newColumnsView(),
		jobsFind:    newJobsFind(),
		search:      newSQLSearch(),
		dryRuns:     map[string]*dryRunEntry{},
		tables:      map[bigquery.TableRef]*tableEntry{},
		incremental: true,
		flash:       deps.Notice,
		following:   deps.Editor != nil,
		recent:      slices.Clone(deps.Resume.Recent),
	}
	m.flags.api = deps.RunAPI && deps.StartAPIRun != nil
	m.events, _ = deps.Engine.Subscribe() // for the life of the program
	m.compiling = true
	m.compileStarted = deps.Now()
	m.started = deps.Now()
	if deps.Branches != nil {
		m.heads, _ = deps.Branches.Watch(context.Background(), headDebounce) // for the life of the program
	}
	m.initCmd = tea.Batch(m.startAnim(), m.readAPIGit(), m.readGit())
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.initCmd, waitForEngine(m.events), waitForEditor(m.deps.Editor), waitForHead(m.heads), m.primeChanges(), m.loadHistory())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok && m.deps.Now().Sub(m.started) < m.deps.StartupQuiet {
		return m, nil // likely a split terminal reply, not a key
	}
	next, cmd := m.update(msg)
	nm := next.(Model)
	if nm.flags.api && !m.flags.api {
		cmd = tea.Batch(cmd, nm.readAPIGit()) // whichever view toggled it
	}
	// Pane sizes can depend on content (stacked panes shrink to fit), so
	// re-fit after every message. Likewise any message can change what the
	// Actions pane lists, so dry-run whatever it has no result for, and
	// look its tables up.
	nm.layout()
	nm.askFocus()
	return nm, tea.Batch(cmd, nm.startDryRuns(), nm.startTableLookups())
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.impact.linesW != m.impactInner() {
			m.drawImpact()
		}
		if m.cost.linesW != m.impactInner() {
			m.drawCost()
		}
		return m, nil
	case tea.BackgroundColorMsg:
		m.termDark = msg.IsDark()
		m.restyle()
		return m, nil
	case engineMsg:
		return m, tea.Batch(m.handleEngine(msg.state), waitForEngine(m.events))
	case apiGitMsg:
		m.handleAPIGit(msg)
		return m, nil
	case tea.FocusMsg:
		return m, tea.Batch(m.readAPIGit(), m.readGit()) // a commit or push made elsewhere
	case gitStatusMsg:
		m.handleGitStatus(msg)
		return m, nil
	case headMsg:
		return m, tea.Batch(m.readGit(), m.readAPIGit(), waitForHead(m.heads))
	case branchListMsg:
		m.handleBranchList(msg)
		return m, nil
	case branchSwitchMsg:
		return m, m.handleBranchSwitch(msg)
	case branchFetchMsg:
		return m, m.handleBranchFetch(msg)
	case dryRunMsg:
		m.handleDryRun(msg)
	case tableMsg:
		m.handleTable(msg)
		return m, nil
	case changedMsg:
		m.handleChanged(msg)
		return m, nil
	case impactMsg:
		m.handleImpact(msg)
		return m, nil
	case impactProgressMsg:
		return m, m.handleImpactProgress(msg)
	case costMsg:
		m.handleCost(msg)
		return m, nil
	case costProgressMsg:
		return m, m.handleCostProgress(msg)
	case prodSavedMsg:
		return m, m.handleProdSaved(msg)
	case infoVersionMsg:
		if msg.seq == m.info.seq {
			m.info.version, m.info.versionErr, m.info.versionDone = msg.version, msg.err, true
		}
		return m, nil
	case infoGitMsg:
		if msg.seq == m.info.seq {
			m.info.git, m.info.gitErr, m.info.gitDone = msg.git, msg.err, true
		}
		return m, nil
	case infoADCMsg:
		if msg.seq == m.info.seq {
			m.info.adc, m.info.adcDone = msg.adc, true
		}
		return m, nil
	case infoGcloudMsg:
		if msg.seq == m.info.seq {
			m.info.gcloud, m.info.gcloudDone = msg.gcloud, true
		}
		return m, nil
	case animTickMsg:
		return m, m.handleAnimTick()
	case editorMsg:
		m.handleEditor(msg.ev)
		return m, waitForEditor(m.deps.Editor)
	case runStartedMsg:
		m.run, m.runURL = msg.run, msg.run.URL
		var git tea.Cmd
		if m.runAPI {
			git = m.readAPIGit() // StartAPIRun read what it runs
		}
		if m.logTab == tabHistory {
			m.logTab = tabLog
		}
		if msg.run.Report != nil {
			return m, tea.Batch(waitForRunEvent(msg.run), jobsTick(), git)
		}
		return m, tea.Batch(waitForRunEvent(msg.run), git)
	case jobsTickMsg:
		if !m.running {
			return m, nil
		}
		m.syncRunTable()
		return m, jobsTick()
	case historyLoadedMsg:
		return m, m.setHistory(msg)
	case runRefreshedMsg:
		m.setRefreshed(msg.entry)
		return m, nil
	case historyClearedMsg:
		if msg.err != nil {
			m.flash = "could not clear the history: " + msg.err.Error()
			return m, nil
		}
		m.flash = "history cleared"
		if m.running {
			m.flash = "history cleared, but for the run that is going"
		}
		m.history.open, m.history.cursor, m.history.offset = nil, 0, 0
		if t := &m.runTable; t.kept != nil {
			t.kept, t.report = nil, nil
		}
		return m, m.loadHistory()
	case runStartErrMsg:
		m.running, m.cancelRun = false, nil
		what := m.tool()
		if m.runAPI {
			what = "the API run"
		}
		m.log.append(m.styles.errorText.Render("✗ could not start " + what + ": " + msg.err.Error()))
		var git tea.Cmd
		if m.runAPI {
			git = m.readAPIGit() // the git line says why too
		}
		if errors.Is(msg.err, dataformapi.ErrAuth) {
			m.log.append(m.styles.warnText.Render("  to log in, " + dataformapi.AuthHint))
			if hint := m.accountHint(); hint != "" {
				m.log.append(m.styles.dim.Render("  " + hint))
			}
		}
		m.log.title = "failed to start"
		return m, git
	case runLineMsg:
		m.log.append(msg.line)
		m.syncRunTable()
		return m, waitForRunEvent(m.run)
	case runFinishedMsg:
		m.syncRunTable()
		m.running, m.cancelRun, m.run, m.cancelling = false, nil, nil, false
		m.log.finish(m.styles, msg.res)
		m.expireTables(false) // the run may have built them
		// or the tables failed dry runs read: the engine forgets its
		// failures before startDryRuns asks again.
		if forget := m.deps.ForgetFailedDryRuns; forget != nil {
			forget()
		}
		m.forgetFailedDryRuns()
		return m, m.loadHistory()
	case urlOpenedMsg:
		if msg.err != nil {
			m.flash = "could not open the browser: " + msg.err.Error()
		}
		return m, nil
	case openedMsg:
		if msg.opening.Run != nil {
			file := msg.file
			return m, tea.ExecProcess(msg.opening.Run, func(err error) tea.Msg { return openedMsg{file: file, err: err} })
		}
		if msg.opening.Leave {
			if m.running {
				m.flash = "a run is in progress: o opens neovim when it ends (ctrl+c cancels it)"
				return m, nil
			}
			m.leaving = &editorLocation{file: msg.path, line: msg.line}
			return m, tea.Quit
		}
		switch {
		case msg.err != nil:
			m.flash = "could not open " + msg.file + ": " + msg.err.Error()
		case msg.asked:
			m.flash = "opened " + msg.file + " in your editor"
		}
		return m, nil
	case fullScreenMsg:
		if msg.err != nil {
			m.flash = "could not toggle full screen in nvim: " + msg.err.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m, m.handleClick(msg.Mouse())
	case tea.MouseMotionMsg:
		m.handleDrag(msg.Mouse())
		return m, nil
	case tea.MouseReleaseMsg:
		m.sqlDrag = false
		return m, nil
	case tea.MouseWheelMsg:
		m.handleWheel(msg.Mouse())
		return m, nil
	}
	if m.prod.open {
		var cmd tea.Cmd
		m.prod.input, cmd = m.prod.input.Update(msg) // cursor blink, paste
		return m, cmd
	}
	if m.palette.open {
		return m, m.palette.update(msg)
	}
	if m.tags.open {
		return m, m.tags.update(msg)
	}
	if m.branches.open {
		var cmd tea.Cmd
		before := m.branches.input.Value()
		m.branches.input, cmd = m.branches.input.Update(msg) // cursor blink, paste
		if m.branches.input.Value() != before {
			m.branches.filter() // a paste; a blink keeps the cursor where it is
		}
		return m, cmd
	}
	if m.keyHelp.typing {
		var cmd tea.Cmd
		m.keyHelp.input, cmd = m.keyHelp.input.Update(msg) // cursor blink
		return m, cmd
	}
	if m.search.typing {
		var cmd tea.Cmd
		m.search.input, cmd = m.search.input.Update(msg) // cursor blink
		return m, cmd
	}
	return m, nil
}

// handleEngine applies what the engine reports: a compile under way, or a
// new result. After the first good compile the palette opens if nothing is
// selected, since there is nothing to show yet.
func (m *Model) handleEngine(st engine.State) tea.Cmd {
	var anim tea.Cmd
	if t := validTheme(st.Theme); st.Theme != "" && t != m.theme {
		m.theme = t // another client changed it
		m.restyle()
	}
	if st.WatchErr != nil && !m.watchNoticed {
		m.watchNoticed = true
		m.flash = "file watching is off (" + st.WatchErr.Error() + "); press c to recompile"
	}
	m.changes = st.Changes
	m.impactSig = st.ImpactSignature
	m.palette.changed = changedSet(st.Changes)
	m.compiling = st.Compiling
	if st.Compiling {
		if !st.Started.Equal(m.compileStarted) {
			anim = tea.Batch(m.readAPIGit(), m.readGit()) // a file changed, or c
		}
		m.compileReason, m.compileStarted = st.Reason, st.Started
		anim = tea.Batch(anim, m.startAnim())
	}
	m.partial = st.Partial
	// A staged graph arrives, and gets its actions' SQL, between compiles.
	if st.Compiles == m.compiles && st.Graph == m.graph && (!st.Partial || len(st.Errors) == len(m.errors)) {
		return anim
	}
	m.compiles = st.Compiles
	m.forgetFailedDryRuns()
	m.expireTables(true)
	hadErrors := m.hasErrors()
	first := m.graph == nil

	m.compileErr, m.errors, m.stale = st.Err, st.Errors, st.Stale
	if st.Graph != nil && st.Graph.Notice != m.graphNotice {
		m.graphNotice = st.Graph.Notice
		if m.graphNotice != "" {
			m.flash = m.graphNotice
		}
	}
	m.saved, m.outdated = st.Saved, st.Outdated
	if st.Graph != m.graph {
		m.compiledAt, m.compileTook = st.CompiledAt, st.Took
		m.setGraph(st.Graph)
		if m.pendingEditor != "" && m.following {
			m.showEditorFile(m.pendingEditor)
		}
	}
	if m.hasErrors() && !hadErrors {
		m.openErrors()
	}
	if hadErrors && !m.hasErrors() {
		m.showErrors = false // fixed: what's left are dry runs, which retry now
	}
	if first && m.graph != nil && m.selected == nil {
		if a := m.graph.Actions[m.deps.Resume.Selected]; a != nil && a.Kind != project.KindUnknown {
			m.selectAction(a) // back where the last xf quit
			return anim
		}
	}
	if first && m.graph != nil && m.selected == nil {
		// Nobody has picked an action: show the one being compiled first,
		// the file worked on last.
		if a := m.graph.Actions[st.Focus]; a != nil && st.Focus != "" && a.Kind != project.KindUnknown {
			m.selectAction(a)
			return anim
		}
	}
	if first && m.graph != nil && m.selected == nil && !m.showErrors {
		if m.deps.Beside {
			m.selectFirstAction()
			return anim
		}
		return tea.Batch(anim, m.openPalette())
	}
	return anim
}

// selectFirstAction selects the palette's first runnable action (by name),
// or its first action if none can be run.
func (m *Model) selectFirstAction() {
	items := paletteItems(m.graph)
	if len(items) == 0 {
		return
	}
	pick := items[0].Action
	for _, it := range items {
		if it.Action.Runnable() {
			pick = it.Action
			break
		}
	}
	m.selectAction(pick)
}

// askFocus asks the engine to compile the selected action ahead of the
// rest of the project, once per selection. The engine does when the graph
// is a staged one and the action's SQL is yet to come, and remembers the
// action for the next staged graph.
func (m *Model) askFocus() {
	if m.selected == nil || m.selected.ID == m.focusAsked {
		return
	}
	m.focusAsked = m.selected.ID
	m.deps.Engine.Focus(m.selected.ID)
}

// sqlNote says what a's SQL is when it isn't the current compile's: its
// template, or an earlier compile's SQL (see project.Action.SQL). It is ""
// for compiled SQL.
func (m Model) sqlNote(a *project.Action) string {
	switch {
	case a == nil || a.SQL == project.SQLCompiled:
		return ""
	case a.SQL == project.SQLOutdated && m.partial:
		return "outdated, compiling…"
	case a.SQL == project.SQLOutdated:
		return "outdated"
	case m.partial:
		return "compiling…"
	}
	return "not compiled"
}

func (m *Model) hasErrors() bool { return m.compileErr != nil || len(m.errors) > 0 }

// setGraph swaps in a new graph, keeping the selected action if it still
// exists; otherwise nothing is selected.
func (m *Model) setGraph(g *project.Graph) {
	m.graph = g
	if m.selected != nil {
		m.selected = g.Actions[m.selected.ID]
		if m.selected != nil && m.selected.Kind == project.KindUnknown {
			m.selected = nil
		}
	}
	m.refreshSelection()
}

// openPalette shows the "/" palette. With an empty query it lists the current
// action, then the actions of the file open in the editor, then recent ones.
func (m *Model) openPalette() tea.Cmd {
	if m.graph == nil {
		m.flash = "no actions yet: waiting for a compile"
		return nil
	}
	var first []*project.Action
	if m.selected != nil {
		first = append(first, m.selected)
	}
	if m.following && m.lastEditor != "" {
		first = append(first, m.graph.ActionsInFile(m.lastEditor)...)
	}
	if m.changes != nil {
		for _, c := range m.changes.Changed {
			if a := m.graph.Actions[c.ID]; a != nil {
				first = append(first, a)
			}
		}
	}
	for _, id := range m.recent {
		if a := m.graph.Actions[id]; a != nil && a.Kind != project.KindUnknown {
			first = append(first, a)
		}
	}
	m.zoomed = false
	m.palette.commands, m.palette.subject = m.commands(), m.selected
	byUse(m.palette.commands, m.deps.CommandUses.Load())
	return m.palette.show(paletteItems(m.graph), first, m.selected != nil)
}

// pickFromPalette selects the palette's highlighted action and focuses the
// SQL, or does its highlighted command.
func (m *Model) pickFromPalette() tea.Cmd {
	a, c := m.palette.selected(), m.palette.selectedCommand()
	m.palette.close()
	if c != nil {
		// Counting is only a convenience, so a failure to keep it is
		// not said.
		_, _ = m.deps.CommandUses.Add(c.title)
		return c.run(m)
	}
	if a == nil {
		return nil
	}
	if m.selected != nil && m.selected != a {
		m.lineage.push(m.selected.ID) // backspace in Lineage goes back
	}
	m.selectAction(a)
	m.focus = paneSQL
	return nil
}

// refreshSelection re-renders the panes that show the selected action.
func (m *Model) refreshSelection() {
	m.sql.setAction(m.selected, m.incremental, m.styles)
	m.cols.setAction(m.selected)
	m.lineage.setAction(m.graph, m.selected)
}

func (m *Model) refreshSQL() {
	m.sql.setAction(m.selected, m.incremental, m.styles)
}

// handleEditor follows the file the editor made active. A CursorHold for the
// file already reported is ignored, so idling in nvim never overrides
// browsing in xf; BufEnter and FocusGained always apply.
func (m *Model) handleEditor(ev project.EditorEvent) {
	if ev.Event == "CursorHold" && ev.File == m.lastEditor {
		return
	}
	m.lastEditor = ev.File
	if !m.following || m.graph == nil {
		m.pendingEditor = ev.File
		return
	}
	m.showEditorFile(ev.File)
}

// showEditorFile selects the file's first action (the table, view or
// operation before its assertions), unless one of its actions is already
// selected. A file with no actions leaves the selection alone.
func (m *Model) showEditorFile(file string) {
	m.pendingEditor = ""
	actions := m.graph.ActionsInFile(file)
	if len(actions) == 0 {
		m.flash = file + ": no actions"
		return
	}
	if m.flash != "" && strings.HasSuffix(m.flash, ": no actions") {
		m.flash = ""
	}
	if m.selected != nil && m.selected.FileName == file {
		return // already showing it; keep whichever of its actions the user picked
	}
	if m.selected != nil {
		m.lineage.push(m.selected.ID) // backspace goes back
	}
	m.selectAction(actions[0])
}

func (m *Model) toggleFollow() {
	if m.deps.Editor == nil {
		m.flash = "not following an editor: add nvim/plugin/xf.lua to neovim"
		return
	}
	m.following = !m.following
	if !m.following {
		m.flash = "following nvim paused (F resumes)"
		return
	}
	m.flash = "following nvim"
	if m.pendingEditor != "" && m.graph != nil {
		m.showEditorFile(m.pendingEditor)
	}
}

// openInEditor asks the editor to open the selected action's file, at the
// line that defines it (see navigate.ActionLocation).
func (m *Model) openInEditor() tea.Cmd {
	if m.deps.Open == nil {
		m.flash = "o needs an editor"
		return nil
	}
	if m.selected == nil || m.selected.FileName == "" {
		m.flash = "no file to open"
		return nil
	}
	a, root, open := m.selected, m.deps.Root, m.deps.Open
	return func() tea.Msg {
		loc := navigate.ActionLocation(navigate.Project{Root: root}, a)
		opening, err := open(loc.File, loc.Line)
		return openedMsg{file: a.FileName, err: err, opening: opening, path: loc.File, line: loc.Line, asked: true}
	}
}

// toggleFullScreen asks the editor xf runs beside to show xf full
// screen, or to put it back in its split.
func (m *Model) toggleFullScreen() tea.Cmd {
	if m.deps.FullScreen == nil {
		m.flash = "Z needs xf inside nvim (xf --nvim, or :Xf)"
		return nil
	}
	full := m.deps.FullScreen
	return func() tea.Msg { return fullScreenMsg{err: full()} }
}

// cycleInFile selects the next (dir 1) or previous (dir -1) action in the
// Actions pane, such as the assertions generated from a table's config or a
// dbt model's tests, wrapping around at the ends.
func (m *Model) cycleInFile(dir int) {
	actions := m.paneActions()
	if len(actions) < 2 {
		m.flash = aloneMessage(m.graph, m.selected)
		return
	}
	i := slices.Index(actions, m.selected)
	m.selectInPane(actions[(i+dir+len(actions))%len(actions)])
}

// selectInPane selects a, one of the Actions pane's actions, keeping the
// pane's list: moving onto a dbt test that is also another model's leaves
// the pane on this model.
func (m *Model) selectInPane(a *project.Action) {
	anchor := m.paneAnchor()
	m.selectAction(a)
	if anchor != nil && anchor != a {
		m.paneOf = anchor.ID
	}
}

// selectAction makes a the selected action and remembers it for the
// palette. The Actions pane shows a's siblings (see project.Graph.Siblings).
func (m *Model) selectAction(a *project.Action) {
	m.selected = a
	m.paneOf = ""
	m.recent = slices.DeleteFunc(m.recent, func(id project.ActionID) bool { return id == a.ID })
	m.recent = slices.Insert(m.recent, 0, a.ID)
	if len(m.recent) > maxRecent {
		m.recent = m.recent[:maxRecent]
	}
	m.refreshSelection()
}

// setFocus focuses a pane; focusing Lineage expands it.
func (m *Model) setFocus(p paneID) {
	m.focus = p
	if p == paneLineage {
		m.lineageOpen = true
	}
}

// toggleLineage expands or collapses the Lineage pane. Collapsing it while
// focused moves the focus to the SQL.
func (m *Model) toggleLineage() {
	m.lineageOpen = !m.lineageOpen
	if !m.lineageOpen && m.focus == paneLineage {
		m.focus = paneSQL
		m.zoomed = false
	}
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys
	m.flash = ""
	if !key.Matches(msg, k.CopyPath) {
		m.copiedRel = ""
	}

	if key.Matches(msg, k.Cancel) {
		if m.branches.open { // over R, I or C as b's picker
			m.branches.close()
			return m, nil
		}
		if m.prod.open {
			return m, m.closeProd()
		}
		if m.changed.open {
			m.closeChanged()
			return m, nil
		}
		if m.impact.open {
			m.closeImpact()
			return m, nil
		}
		if m.cost.open {
			m.closeCost()
			return m, nil
		}
		if m.tags.open {
			m.tags.close()
			return m, nil
		}
		if m.confirmFull != nil || m.confirmTags != nil {
			m.confirmFull, m.confirmTags = nil, nil
			m.flash = "full refresh cancelled"
			return m, nil
		}
		if m.running {
			m.confirmQuit = false
			m.cancelOrKillRun()
			return m, nil
		}
		return m, tea.Quit
	}
	if m.confirmQuit {
		m.confirmQuit = false
		if key.Matches(msg, k.Quit, k.Confirm) {
			if m.running && !m.runAPI {
				m.cancelRun()
			}
			return m, tea.Quit
		}
		return m, nil
	}
	if m.confirmClear {
		m.confirmClear = false
		if key.Matches(msg, k.Confirm) {
			return m, m.clearHistory()
		}
		m.flash = "history kept"
		return m, nil
	}
	if m.confirmFull != nil || m.confirmTags != nil {
		run, tags := m.confirmFull, m.confirmTags
		m.confirmFull, m.confirmTags = nil, nil
		switch {
		case !key.Matches(msg, k.Confirm):
			m.flash = "full refresh cancelled"
		case tags != nil:
			return m, m.launchTagRun(tags)
		default:
			return m, m.launchRun(run)
		}
		return m, nil
	}

	if m.branches.open { // over R, I or C as b's picker
		return m, m.handleBranchesKey(msg)
	}
	if m.prod.open {
		return m, m.handleProdKey(msg)
	}
	if m.changed.open {
		return m, m.handleChangedKey(msg)
	}
	if m.impact.open {
		return m, m.handleImpactKey(msg)
	}
	if m.cost.open {
		return m, m.handleCostKey(msg)
	}
	if m.showErrors {
		return m, m.handleErrorsKey(msg)
	}
	if m.info.open {
		return m, m.handleInfoKey(msg)
	}
	if m.keyHelp.open {
		return m, m.handleHelpKey(msg)
	}

	if m.palette.open {
		return m, m.handlePaletteKey(msg)
	}
	if m.tags.open {
		return m, m.handleTagsKey(msg)
	}

	if m.search.typing {
		return m, m.handleSearchKey(msg)
	}
	if m.columnsFocused() {
		if handled, cmd := m.handleColumnsKey(msg); handled {
			return m, cmd
		}
	} else if m.focus == paneSQL {
		if handled, cmd := m.handleSQLKey(msg); handled {
			return m, cmd
		}
	} else if m.focus == paneLog {
		if handled, cmd := m.handleJobsKey(msg); handled {
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, k.Escape) && m.zoomed:
		m.zoomed = false
		return m, nil
	case key.Matches(msg, k.Find):
		return m, m.openPalette()
	case key.Matches(msg, k.ToggleLineage):
		m.toggleLineage()
		return m, nil
	case key.Matches(msg, k.NextInFile):
		m.cycleInFile(1)
		return m, nil
	case key.Matches(msg, k.PrevInFile):
		m.cycleInFile(-1)
		return m, nil
	case key.Matches(msg, k.Zoom):
		m.zoomed = !m.zoomed
		return m, nil
	case key.Matches(msg, k.Quit):
		return m, m.quit()
	case key.Matches(msg, k.Run):
		return m, m.startRun()
	case key.Matches(msg, k.RunChanged):
		return m, m.openChanged()
	case key.Matches(msg, k.RunTags):
		return m, m.openTags()
	case key.Matches(msg, k.Impact):
		return m, m.openImpact()
	case key.Matches(msg, k.Cost):
		return m, m.openCost()
	case key.Matches(msg, k.Deps):
		m.flags.includeDeps = !m.flags.includeDeps
		return m, nil
	case key.Matches(msg, k.Dependents):
		m.flags.includeDependents = !m.flags.includeDependents
		return m, nil
	case key.Matches(msg, k.FullRefresh):
		m.flags.fullRefresh = !m.flags.fullRefresh
		return m, nil
	case key.Matches(msg, k.API):
		m.toggleAPI()
		return m, nil
	case key.Matches(msg, k.SelectedOnly) && m.canRun():
		m.flags.selectedOnly = !m.flags.selectedOnly
		return m, nil
	case key.Matches(msg, k.Follow):
		m.toggleFollow()
		return m, nil
	case key.Matches(msg, k.Open):
		return m, m.openInEditor()
	case key.Matches(msg, k.FullScreen):
		return m, m.toggleFullScreen()
	case key.Matches(msg, k.Log):
		m.toggleLog()
		return m, nil
	case key.Matches(msg, k.Compile):
		m.deps.Engine.Recompile("")
		return m, nil
	case key.Matches(msg, k.Help):
		m.keyHelp.show()
		return m, nil
	case key.Matches(msg, k.Incremental):
		m.toggleIncremental()
		return m, nil
	case key.Matches(msg, k.Columns):
		m.toggleColumns()
		return m, nil
	case key.Matches(msg, k.Errors):
		m.showErrorsOrSay()
		return m, nil
	case key.Matches(msg, k.Info):
		return m, m.openInfo()
	case key.Matches(msg, k.Branch) && m.deps.Branches != nil:
		return m, m.openBranches()
	case key.Matches(msg, k.NextPane):
		m.cycleFocus(1)
		return m, nil
	case key.Matches(msg, k.PrevPane):
		m.cycleFocus(-1)
		return m, nil
	case key.Matches(msg, k.FocusSQL):
		m.setFocus(paneSQL)
		return m, nil
	case key.Matches(msg, k.FocusActions):
		m.setFocus(paneActions)
		return m, nil
	case key.Matches(msg, k.FocusLineage):
		m.setFocus(paneLineage)
		return m, nil
	case key.Matches(msg, k.FocusRun):
		if m.canRun() {
			m.setFocus(paneRun)
		}
		return m, nil
	case key.Matches(msg, k.FocusLog):
		m.focusLog()
		return m, nil
	}

	switch m.focus {
	case paneActions:
		return m, m.handleActionsKey(msg)
	case paneLineage:
		return m, m.handleLineageKey(msg)
	case paneLog:
		return m, m.handleLogKey(msg)
	}
	return m, nil
}

// quit asks before quitting: the footer says what quitting does to a run in
// progress, and q again or y quits.
func (m *Model) quit() tea.Cmd {
	m.confirmQuit = true
	return nil
}

// toggleAPI switches runs between the tool's CLI and the Dataform API.
func (m *Model) toggleAPI() {
	if m.deps.StartAPIRun == nil {
		m.flash = "API runs are not available"
		return
	}
	m.flags.api = !m.flags.api
}

// toggleLog shows the log and focuses it, or hides it.
func (m *Model) toggleLog() {
	m.showLog = !m.showLog
	switch {
	case m.showLog:
		m.setFocus(paneLog)
	case m.focus == paneLog:
		m.focus = paneSQL
	}
	m.layout()
}

func (m *Model) focusLog() {
	if m.showLog {
		m.setFocus(paneLog)
	} else {
		m.flash = "no log yet: r runs the selected action (L shows the log)"
	}
}

// toggleIncremental switches the SQL shown for an incremental table between
// its incremental and full variants.
func (m *Model) toggleIncremental() {
	m.incremental = !m.incremental
	if m.selected == nil || !m.selected.HasIncrementalVariant() {
		m.flash = "v only applies to incremental tables with an incremental variant"
	}
	m.refreshSQL()
}

// showErrorsOrSay opens the errors overlay, or says there are none.
func (m *Model) showErrorsOrSay() {
	if m.anyErrors() {
		m.openErrors()
	} else {
		m.flash = "no errors"
	}
}

// cancelOrKillRun asks the run to stop as a terminal's ctrl+c would
// (dataform skips the actions it hasn't started, though BigQuery jobs it
// already submitted carry on; dbt cancels its jobs and stops), or
// force-kills it if that was already asked for.
//
// For a run in GCP the first ctrl+c asks Dataform to cancel the invocation,
// and the second stops watching it.
func (m *Model) cancelOrKillRun() {
	if !m.cancelling {
		m.cancelling = true
		m.cancelRun()
		m.log.title = "cancelling…"
		msg := "^C cancelling: dataform is stopping; BigQuery jobs it already started keep running (ctrl+c again force-kills)"
		switch {
		case m.runAPI:
			msg = "^C cancelling the workflow invocation (ctrl+c again stops watching)"
		case m.tool() == "dbt":
			msg = "^C cancelling: dbt is stopping (ctrl+c again force-kills)"
		}
		m.log.append(m.styles.warnText.Render(msg))
		return
	}
	if m.run != nil && m.run.Kill != nil {
		m.run.Kill()
		if !m.runAPI {
			m.log.append(m.styles.errorText.Render("^C force-killed; BigQuery jobs that were still running may carry on"))
		}
	}
}

// ExitNotice is what to print after the program exits: where to follow a
// run in GCP that was still going. It is empty otherwise.
func (m Model) ExitNotice() string {
	if !m.running || !m.runAPI {
		return ""
	}
	if m.run != nil && m.run.URL != nil {
		if u := m.run.URL(); u != "" {
			return "the workflow invocation is still running in GCP: " + u
		}
	}
	return "an API run was still starting when xf quit; check the Dataform console for its workflow invocation"
}

// Session is what to restore when xf next starts in this project.
// editorLocation is a file (an absolute path) and a 0-based line in it.
type editorLocation struct {
	file string
	line int
}

// Leaving reports that xf quit to be replaced by the editor (see
// Opening.Leave), which is to open file at a 0-based line.
func (m Model) Leaving() (file string, line int, ok bool) {
	if m.leaving == nil {
		return "", 0, false
	}
	return m.leaving.file, m.leaving.line, true
}

func (m Model) Session() session.Session {
	s := session.Session{Root: m.deps.Root, Recent: m.recent}
	if m.selected != nil {
		s.Selected = m.selected.ID
	}
	return s
}

// startRun runs the actions of the selected action's pane (see runActions)
// with the current flags. A full refresh drops and rebuilds tables, so it
// asks first and runs on y.
func (m *Model) startRun() tea.Cmd {
	a := m.selected
	run := m.runActions()
	switch {
	case m.running:
		m.flash = "a run is already in progress (ctrl+c cancels it)"
		return nil
	case a == nil:
		m.flash = "select an action to run"
		return nil
	case len(run) == 0:
		m.flash = a.Kind.String() + "s can't be run"
		return nil
	case m.deps.StartRun == nil:
		m.flash = "running is not available"
		return nil
	}
	if m.flags.fullRefresh {
		m.confirmFull = run
		return nil
	}
	return m.launchRun(run)
}

// launchRun runs actions with the current flags.
func (m *Model) launchRun(actions []*project.Action) tea.Cmd {
	ids := actionIDs(actions)
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancelRun, m.runAPI, m.runURL = true, cancel, m.flags.api, nil
	m.runLabel, m.runShort = runLabel(actions), runLabel(actions)
	if len(actions) == 1 {
		m.runShort = actions[0].Target.Name
	}
	m.log.reset(m.styles, m.runCommand(ids...))
	m.runTable.reset()
	if !m.showLog {
		m.showLog = true
		m.layout()
	}
	if m.runAPI {
		targets := make([]project.Target, len(actions))
		for i, a := range actions {
			targets[i] = a.Target
		}
		return startRunCmd(ctx, m.deps.StartAPIRun, m.flags.apiOptions(m.graph, targets...))
	}
	return startRunCmd(ctx, m.deps.StartRun, m.runOptions(ids...))
}

func (m *Model) handleLogKey(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, ok := m.handleRunTableKey(msg); ok {
		return cmd
	}
	k := m.keys
	p := &m.log
	switch {
	case key.Matches(msg, k.OpenURL):
		return m.openRunURL()
	case key.Matches(msg, k.Up):
		p.scrollBy(-1)
	case key.Matches(msg, k.Down):
		p.scrollBy(1)
	case key.Matches(msg, k.HalfDown):
		p.scrollBy(max(p.height/2, 1))
	case key.Matches(msg, k.HalfUp):
		p.scrollBy(-max(p.height/2, 1))
	case key.Matches(msg, k.Top):
		p.top()
		p.follow = false
	case key.Matches(msg, k.Bottom):
		p.bottom()
		p.follow = true
	}
	return nil
}

// lastRunURL is the last API run's console URL, "" when there is none (yet).
func (m Model) lastRunURL() string {
	if m.runURL == nil {
		return ""
	}
	return m.runURL()
}

// openRunURL opens the last API run's workflow invocation in the browser.
func (m *Model) openRunURL() tea.Cmd {
	u := m.lastRunURL()
	switch {
	case m.deps.OpenURL == nil:
		m.flash = "can't open a browser here"
		return nil
	case u == "" && m.runAPI && m.running:
		m.flash = "no workflow invocation yet: Dataform is still compiling"
		return nil
	case u == "":
		m.flash = "u opens an API run's workflow invocation; this run has none"
		return nil
	}
	m.flash = "opening the workflow invocation in the browser"
	open := m.deps.OpenURL
	return func() tea.Msg { return urlOpenedMsg{open(u)} }
}

// handlePaletteKey handles a key while the palette is open: the arrows and
// ctrl+n/ctrl+p move (j/k are typed into the query), enter picks and esc
// cancels.
func (m *Model) handlePaletteKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	p := &m.palette
	rows := p.box(m.width, m.geometry().bodyH).rows
	switch {
	case key.Matches(msg, k.Escape):
		p.close()
	case key.Matches(msg, k.Accept):
		return m.pickFromPalette()
	case msg.String() == "up" || msg.String() == "ctrl+p":
		p.move(-1, rows)
	case msg.String() == "down" || msg.String() == "ctrl+n":
		p.move(1, rows)
	case msg.String() == "pgup":
		p.move(-rows, rows)
	case msg.String() == "pgdown":
		p.move(rows, rows)
	case key.Matches(msg, k.Find) && p.input.Value() == "":
		// already finding: the palette opens by itself after the first
		// compile, so / from habit shouldn't become the query
	default:
		return p.update(msg)
	}
	return nil
}

// handleActionsKey moves the selection through the file's actions; enter
// shows the selected one's SQL, u opens its table in BigQuery, y copies its
// name and Y its file's path.
func (m *Model) handleActionsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.OpenTable):
		return m.openTableURL(m.selected)
	case key.Matches(msg, k.CopyTarget):
		return m.copyTarget(m.selected)
	case key.Matches(msg, k.CopyPath):
		return m.copyPath(m.selected)
	case key.Matches(msg, k.Up):
		m.moveInActions(-1)
	case key.Matches(msg, k.Down):
		m.moveInActions(1)
	case key.Matches(msg, k.Top):
		m.moveInActions(-2)
	case key.Matches(msg, k.Bottom):
		m.moveInActions(2)
	case key.Matches(msg, k.ShowSQL):
		m.focus = paneSQL
		m.zoomed = false
	}
	return nil
}

// hasTable reports whether a's target is a table or view in BigQuery (see
// project.ActionKind.HasTable).
func hasTable(a *project.Action) bool {
	return a.Kind.HasTable() && a.Target.Database != ""
}

// targetKeys are the footer's bindings for a's table, described briefly so
// the global ones still fit: u when it has one to open, and y (and Y when it
// has a file).
func (m Model) targetKeys(a *project.Action) []key.Binding {
	if a == nil {
		return nil
	}
	cp := m.keys.CopyTarget
	if a.FileName != "" {
		cp.SetHelp("y/Y", "copy name/path")
	} else {
		cp.SetHelp("y", "copy name")
	}
	if m.deps.OpenURL != nil && hasTable(a) {
		open := m.keys.OpenTable
		open.SetHelp("u", "BigQuery")
		return []key.Binding{open, cp}
	}
	return []key.Binding{cp}
}

// openTableURL opens a's table or view in the BigQuery console.
func (m *Model) openTableURL(a *project.Action) tea.Cmd {
	switch {
	case m.deps.OpenURL == nil:
		m.flash = "can't open a browser here"
		return nil
	case a == nil:
		m.flash = "no action selected"
		return nil
	case !hasTable(a):
		m.flash = "u opens a table or view in BigQuery; " + string(a.ID) + " has none"
		return nil
	}
	m.flash = "opening " + string(a.ID) + " in BigQuery"
	open, u := m.deps.OpenURL, a.Target.ConsoleURL()
	return func() tea.Msg { return urlOpenedMsg{open(u)} }
}

// copyTarget copies a's name quoted for BigQuery, `project.dataset.name`,
// to paste into a query.
func (m *Model) copyTarget(a *project.Action) tea.Cmd {
	if a == nil {
		m.flash = "no action selected"
		return nil
	}
	name := "`" + string(a.ID) + "`"
	cmd := m.yank(name, 1, false)
	m.flash = "copied " + name
	return cmd
}

// copyPath copies the path of the file defining a, relative to the project
// root; straight after that, the absolute path.
func (m *Model) copyPath(a *project.Action) tea.Cmd {
	switch {
	case a == nil:
		m.flash = "no action selected"
		return nil
	case a.FileName == "":
		m.flash = string(a.ID) + " is not defined in a file of this project"
		return nil
	}
	if m.copiedRel == a.ID {
		m.copiedRel = ""
		path := filepath.Join(m.deps.Root, filepath.FromSlash(a.FileName))
		cmd := m.yank(path, 1, false)
		m.flash = "copied " + path
		return cmd
	}
	m.copiedRel = a.ID
	cmd := m.yank(a.FileName, 1, false)
	m.flash = "copied " + a.FileName + " · Y again copies the absolute path"
	return cmd
}

func (m *Model) handleLineageKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	p := &m.lineage
	switch {
	case key.Matches(msg, k.OpenTable):
		return m.openTableURL(p.selected())
	case key.Matches(msg, k.CopyTarget):
		return m.copyTarget(p.selected())
	case key.Matches(msg, k.CopyPath):
		return m.copyPath(p.selected())
	case key.Matches(msg, k.Up):
		p.move(-1)
	case key.Matches(msg, k.Down):
		p.move(1)
	case key.Matches(msg, k.Top):
		p.moveToEnd(-1)
	case key.Matches(msg, k.Bottom):
		p.moveToEnd(1)
	case key.Matches(msg, k.Jump):
		a := p.selected()
		switch {
		case a == nil:
		case a.Kind == project.KindUnknown:
			m.flash = string(a.ID) + " is not defined in this project"
		default:
			if m.selected != nil {
				p.push(m.selected.ID)
			}
			m.selectAction(a)
		}
	case key.Matches(msg, k.Back):
		for {
			id, ok := p.pop()
			if !ok {
				m.flash = "no earlier action to go back to"
				return nil
			}
			// Skip actions that disappeared in a recompile.
			if a := m.graph.Actions[id]; a != nil && a.Kind != project.KindUnknown {
				m.selectAction(a)
				return nil
			}
		}
	}
	return nil
}

// tool names the project's tool: "dataform" or "dbt".
func (m Model) tool() string { return cmp.Or(m.deps.Backend, "dataform") }

// canRun reports whether the project's actions can be run from here; the
// Run pane and its keys are hidden when they can't.
func (m Model) canRun() bool { return m.deps.StartRun != nil }

// tabOrder is the order tab cycles through the panes, top to bottom.
func (m *Model) tabOrder() []paneID {
	order := []paneID{paneActions, paneLineage, paneRun, paneSQL}
	if !m.canRun() {
		order = slices.DeleteFunc(order, func(p paneID) bool { return p == paneRun })
	}
	if m.showLog {
		order = append(order, paneLog)
	}
	return order
}

func (m *Model) cycleFocus(dir int) {
	tabOrder := m.tabOrder()
	for i, p := range tabOrder {
		if p == m.focus {
			m.setFocus(tabOrder[(i+dir+len(tabOrder))%len(tabOrder)])
			return
		}
	}
	m.setFocus(tabOrder[0])
}

// geometry is the height of each pane, top to bottom: the Actions pane, the
// Lineage pane (a titled border when collapsed), the one-line Run pane, the
// SQL and the log. Every pane is full width. solo is a terminal too short
// to stack them, which shows the focused pane alone, as if zoomed.
type geometry struct {
	bodyH                 int
	cardH, lineageH, runH int
	sqlH, logH            int
	solo                  bool
}

// geometry gives the SQL every row the other panes don't need, but keeps
// sqlMinRows of it when it can. The Actions pane shrinks to its content;
// expanded, the Lineage pane does too, up to 40% of what Actions leaves.
// On a short terminal the log gives up rows first, down to one; shorter
// still, the layout is solo.
func (m Model) geometry() geometry {
	g := geometry{bodyH: m.height - 2, lineageH: lineageClosedH, runH: runPaneH}
	switch {
	case !m.canRun():
		g.runH = 0
	case m.flags.api:
		g.runH++ // the git line
	}
	rest := g.bodyH - g.runH
	// need is the fewest rows the Actions, Lineage and SQL panes take:
	// a row of content each, in its border.
	need := 3 + lineageClosedH + 3
	if m.lineageOpen {
		need++
	}
	if m.showLog {
		g.logH = min(max(rest*3/10, 5), rest-need)
		rest -= g.logH
	}
	if rest < need || (m.showLog && g.logH < 3) {
		g.solo = true
		return g
	}
	sqlMinH := sqlMinRows + 2
	lines, _, _ := m.actionsLines(m.width-2, false)
	g.cardH = min(max(len(lines), 1)+2, max(rest-lineageClosedH-sqlMinH, 3))
	rest -= g.cardH
	if m.lineageOpen {
		g.lineageH = min(max(len(m.lineage.rows), 1)+2, max(min(rest*4/10, rest-sqlMinH), 3))
	}
	g.sqlH = rest - g.lineageH
	return g
}

// moreNote describes rows scrolled out of a list pane: "↑ 2 · ↓ 9 more".
func moreNote(above, below int) string {
	var parts []string
	if above > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d", above))
	}
	if below > 0 {
		parts = append(parts, fmt.Sprintf("↓ %d", below))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + " more"
}

func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	g := m.geometry()
	actionsH := g.cardH - 2
	m.lineage.setHeight(g.lineageH - 2)
	m.sql.setHeight(g.sqlH - 2)
	m.sql.setWidth(m.width - 2)
	m.log.setHeight(max(g.logH-2, 1))
	if m.zoomed || g.solo {
		full := g.bodyH - 2
		switch m.focus {
		case paneLineage:
			m.lineage.setHeight(full)
		case paneSQL:
			m.sql.setHeight(full)
		case paneLog:
			m.log.setHeight(full)
		case paneActions:
			actionsH = full
		}
	}
	m.scrollActions(m.width-2, actionsH)
	if m.palette.open {
		m.palette.scrollIntoView(m.palette.box(m.width, g.bodyH).rows)
	}
	if m.log.follow {
		m.log.bottom()
	}
	m.help.SetWidth(m.width)
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	// Clicks focus panes, the wheel scrolls them and dragging in the SQL pane
	// selects text for y to copy. Terminals still select text with a modifier
	// held (Option in iTerm2 and Terminal, Shift in most others).
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "xf · " + filepath.Base(m.deps.Root)
	// Getting focus back reads the checkout again for the git line.
	v.ReportFocus = m.flags.api && m.deps.APIGit != nil
	return v
}

func (m Model) render() string {
	if m.width == 0 {
		return ""
	}
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf("terminal too small: %dx%d (need %dx%d)", m.width, m.height, minWidth, minHeight)
	}
	g := m.geometry()
	var body string
	if m.zoomed || g.solo {
		body = m.renderPane(m.focus, g.bodyH)
	} else {
		panes := []string{
			m.renderPane(paneActions, g.cardH),
			m.renderPane(paneLineage, g.lineageH),
		}
		if g.runH > 0 {
			panes = append(panes, m.renderPane(paneRun, g.runH))
		}
		panes = append(panes, m.renderPane(paneSQL, g.sqlH))
		if m.showLog {
			panes = append(panes, m.renderPane(paneLog, g.logH))
		}
		body = lipgloss.JoinVertical(lipgloss.Left, panes...)
	}

	switch {
	case m.branches.open: // over R, I or C as b's picker
		body = overlay(body, m.branchesBox(g.bodyH), m.width, g.bodyH)
	case m.prod.open:
		body = overlay(body, m.prodBox(g.bodyH), m.width, g.bodyH)
	case m.changed.open:
		body = overlay(body, m.changedBox(g.bodyH), m.width, g.bodyH)
	case m.impact.open:
		body = overlay(body, m.impactBox(g.bodyH), m.width, g.bodyH)
	case m.cost.open:
		body = overlay(body, m.costBox(g.bodyH), m.width, g.bodyH)
	case m.confirmFull != nil || m.confirmTags != nil:
		body = overlay(body, fullRefreshBox(m.styles, m.flags, m.tool(), m.confirmFull, m.confirmTags, m.width, g.bodyH), m.width, g.bodyH)
	case m.showErrors:
		body = overlay(body, m.errorsBox(g), m.width, g.bodyH)
	case m.info.open:
		body = overlay(body, m.infoBox(g), m.width, g.bodyH)
	case m.keyHelp.open:
		body = overlay(body, m.helpBox(), m.width, g.bodyH)
	case m.palette.open:
		body = overlay(body, m.palette.view(m.styles, m.palette.box(m.width, g.bodyH)), m.width, g.bodyH)
	case m.tags.open:
		body = overlay(body, m.tagsBox(g.bodyH), m.width, g.bodyH)
	}
	return body + "\n" + m.statusLine() + "\n" + m.footerLine()
}

func (m Model) sqlTitle() string {
	t := "0 sql"
	if m.cols.shown {
		t += " │ columns"
	}
	a := m.selected
	if a == nil {
		return t
	}
	t += " · " + a.Target.Schema + "." + a.Target.Name + " · " + a.Kind.String()
	if m.uncompiled() {
		t += " · not compiled"
	} else if note := m.sqlNote(a); note != "" {
		t += " · " + note
	}
	if a.HasIncrementalVariant() {
		if m.incremental {
			t += " [incremental]"
		} else {
			t += " [full]"
		}
	}
	if note := m.sqlSearchNote(); note != "" {
		t += " · " + note
	}
	return t
}

// lineageTitle is "▾ 2 lineage", or when collapsed "▸ 2 lineage · ↑2 ↓4"
// with the entry counts, since the pane shows nothing else.
func (m Model) lineageTitle() string {
	if m.lineageOpen {
		return "▾ 2 lineage"
	}
	t := "▸ 2 lineage"
	if m.selected != nil && m.graph != nil {
		t += fmt.Sprintf(" · ↑%d ↓%d", m.lineage.up, m.lineage.down)
	}
	return t
}

// renderPane draws one pane, full width.
func (m Model) renderPane(id paneID, h int) string {
	s := m.styles
	w := m.width
	focused := m.focus == id
	var title, body, footer string
	switch id {
	case paneActions:
		title = m.actionsTitle()
		body, footer = m.actionsBody(w-2, h-2)
	case paneLineage:
		title = m.lineageTitle()
		switch {
		case !m.lineageOpen:
			footer = "O opens"
		case m.graph == nil && m.compiling:
			body = m.waitingLine()
		case m.graph == nil:
			body = s.dim.Render("no compile result")
		default:
			body = m.lineage.view(s, w-2, focused)
			footer = moreNote(m.lineage.hiddenEntries())
		}
	case paneRun:
		title = "3 run"
		body = runPaneLine(s, m.flags, m.deps.StartAPIRun != nil, m.selected, m.runActions(), m.running, m.cancelling, w-2)
		if m.flags.api {
			body += "\n" + m.apiGitLine("run")
		}
	case paneSQL:
		title, body = m.sqlTitle(), m.sqlBody()
		if m.cols.shown {
			body, footer = m.columnsBody(), m.columnsFooter()
		}
	case paneLog:
		title, body, footer = m.logTitle(), m.logBody(w-2, focused), m.logFooter()
	}
	if m.zoomed {
		title += " · zoom (esc)"
	} else if m.geometry().solo {
		title += " · tab: next pane"
	}
	return s.apiPane(title, footer, body, w, h, focused, id == paneRun && m.flags.api)
}

func (m Model) logTitle() string {
	t := "4 log"
	if m.hasTabs() {
		t = m.tabsTitle()
		switch h := m.history; {
		case m.logTab == tabHistory && h.open != nil:
			return t + " · " + m.keptTitle(h.open)
		case m.logTab == tabHistory:
			return t
		case m.logTab == tabJobs && m.runTable.kept != nil:
			return t + " · last run · " + m.keptTitle(m.runTable.kept)
		}
	}
	if m.runLabel != "" {
		t += " · " + m.runLabel
	}
	t += " · " + m.log.title
	switch {
	case m.cancelling && m.runAPI:
		t += " · ctrl+c stops watching"
	case m.cancelling:
		t += " · ctrl+c force-kills"
	case m.running:
		t += " · ctrl+c cancels"
	}
	return t
}

func (m Model) sqlBody() string {
	if m.graph == nil {
		if m.compiling {
			return m.waitingLine()
		}
		return m.styles.dim.Render("no compile result — press e for details, c to retry")
	}
	return strings.Join(m.sql.view(m.styles, m.focus == paneSQL), "\n")
}

func (m Model) statusLine() string {
	s := m.styles
	parts := []string{filepath.Base(m.deps.Root)}
	if m.deps.Target != "" {
		parts = append(parts, "target "+m.deps.Target)
	}
	if m.graph != nil {
		n := 0
		for _, grp := range m.graph.Grouped() {
			n += len(grp.Actions)
		}
		parts = append(parts, fmt.Sprintf("%d actions", n))
		if m.graph.CoreVersion != "" {
			label := "core "
			if m.deps.Backend == "dbt" {
				label = "dbt "
			}
			parts = append(parts, label+m.graph.CoreVersion)
		}
	}
	if !m.compiledAt.IsZero() {
		compiled := fmt.Sprintf("compiled %s (%.1fs)", m.compiledAt.Format("15:04:05"), m.compileTook.Seconds())
		if m.saved {
			compiled += ", saved"
		}
		parts = append(parts, compiled)
	}
	var state string
	switch {
	case m.compiling:
		state = "compiling…"
		if m.compileReason != "" {
			state += " (" + m.compileReason + ")"
		} else if m.outdated != "" {
			state += " (" + m.outdated + ")"
		}
		state = m.spinner() + " " + s.warnText.Render(state)
		if m.outdated != "" {
			state = s.warnText.Render("outdated · ") + state
		}
	case m.hasErrors():
		n := len(m.errors)
		if m.compileErr != nil {
			n = 1
		}
		state = s.errorText.Render(fmt.Sprintf("✗ %d compile error%s (e)", n, plural(n)))
		if m.stale {
			state = s.warnText.Render("stale · ") + state
		}
	default:
		state = s.okText.Render("idle")
		if errs, _ := m.dryRunErrors(); len(errs) > 0 {
			state += s.statusBar.Render(" · ") + s.errorText.Render(fmt.Sprintf("✗ %d dry run error%s (e)", len(errs), plural(len(errs))))
		}
	}
	if m.running {
		where := ""
		if m.runAPI {
			where = " in GCP"
		}
		state += s.statusBar.Render(" · ") + s.warnText.Render("▶ running "+m.runShort+where+" (ctrl+c cancels)")
	}
	if m.deps.Editor != nil {
		if m.following {
			state += s.statusBar.Render(" · following nvim")
		} else {
			state += s.statusBar.Render(" · ") + s.warnText.Render("follow paused (F)")
		}
	}
	head := s.statusBar.Render(parts[0] + " · ")
	if branch := m.branchLine(); branch != "" {
		head += branch + m.baseLine() + s.statusBar.Render(" · ")
	}
	line := s.badge.Render(strings.ToUpper(m.tool())) + " " + head + s.statusBar.Render(strings.Join(parts[1:], " · ")+" · ") + state
	return ansi.Truncate(line, m.width, "…")
}

func (m Model) footerLine() string {
	if m.confirmQuit {
		ask := "quit xf? q again or y quits"
		switch {
		case m.running && m.runAPI:
			ask = "a run is in progress and carries on in GCP: q again or y quits"
		case m.running:
			ask = "a run is in progress: q again or y cancels it and quits"
		}
		return m.styles.warnText.Render(ask + ", any other key stays")
	}
	if m.confirmClear {
		s := m.styles
		return s.warnText.Render(fmt.Sprintf("forget the %d kept run%s of this project? ", len(m.history.entries), plural(len(m.history.entries)))) +
			s.footerKey.Render("y") + " " + s.footerDesc.Render("clear") + s.footerDesc.Render(" · any other key keeps them")
	}
	if m.confirmFull != nil || m.confirmTags != nil {
		s := m.styles
		return s.warnText.Render("confirm full refresh: ") + s.footerKey.Render("y") + " " + s.footerDesc.Render("run") +
			s.footerDesc.Render(" · any other key cancels")
	}
	if m.flash != "" {
		return m.styles.warnText.Render(m.flash)
	}
	k := m.keys
	var b []key.Binding
	switch {
	case m.keyHelp.typing:
		return ansi.Truncate(m.keyHelp.input.View(), m.width, "…")
	case m.info.typing:
		return ansi.Truncate(m.info.input.View(), m.width, "…")
	case m.search.typing:
		return ansi.Truncate(m.search.input.View(), m.width, "…")
	case m.keyHelp.open:
		b = m.helpFooter()
	case m.showErrors:
		zoom := k.Zoom
		zoom.SetHelp("z", "zoom")
		if m.errorsView.zoomed {
			zoom.SetHelp("z", "unzoom")
		}
		copyAll := k.CopyReport
		copyAll.SetHelp("y", "copy")
		b = []key.Binding{k.Close, copyAll, k.Compile, zoom}
	case m.info.open:
		b = m.infoFooter()
	case m.branches.open && m.branches.base:
		pick := k.Accept
		pick.SetHelp("enter", "compare with")
		move := k.Down
		move.SetHelp("↑/↓", "move")
		back := k.Escape
		back.SetHelp("esc", "back")
		b = []key.Binding{pick, k.FetchBranches, move, back}
	case m.prod.open, m.changed.open, m.impact.open, m.cost.open:
		b = []key.Binding{k.Close}
	case m.palette.open:
		move := k.Down
		move.SetHelp("↑/↓", "move")
		cancel := k.Escape
		cancel.SetHelp("esc", "cancel")
		b = []key.Binding{k.Accept, move, k.FindKind, k.Commands, cancel}
	case m.tags.open:
		toggle := k.ToggleTag
		toggle.SetHelp("space", "check")
		run := k.RunChecked
		run.SetHelp("enter", "run")
		move := k.Down
		move.SetHelp("↑/↓", "move")
		esc := k.Escape
		esc.SetHelp("esc", "run options")
		b = []key.Binding{toggle, run, move, esc}
		if m.tags.options {
			search := k.Find
			search.SetHelp("/", "search")
			esc.SetHelp("esc", "close")
			b = []key.Binding{k.Deps, k.Dependents, k.FullRefresh}
			if m.deps.StartAPIRun != nil {
				api := k.API
				api.SetHelp("a", "api")
				b = append(b, api)
			}
			b = append(b, toggle, run, search, esc)
		}
	case m.branches.open:
		sw := k.Accept
		sw.SetHelp("enter", "switch")
		move := k.Down
		move.SetHelp("↑/↓", "move")
		esc := k.Escape
		esc.SetHelp("esc", "close")
		b = []key.Binding{sw, k.FetchBranches, move, esc}
	case m.columnsFocused():
		b = m.columnsKeys()
	case m.focus == paneLog && m.jobsTyping && (&m).shownTable() != nil:
		b = m.jobsFindKeys()
	case m.focus == paneSQL && m.sql.visual != visualNone:
		cancel := k.Escape
		cancel.SetHelp("esc", "cancel")
		b = []key.Binding{k.Yank, k.OtherEnd, cancel}
	case m.focus == paneSQL:
		move := k.Down
		move.SetHelp("hjkl", "move")
		word := k.WordNext
		word.SetHelp("w/b", "word")
		visual := k.Visual
		visual.SetHelp("v/V", "select")
		yank := k.YankLine
		yank.SetHelp("yy", "copy line")
		columns := k.Columns
		columns.SetHelp("S", "columns")
		b = []key.Binding{move, word, visual, yank, columns}
		if m.sql.hlHits {
			clear := k.Escape
			clear.SetHelp("esc", "clear search")
			b = []key.Binding{clear, k.NextMatch, move, visual, yank}
		}
	case m.focus == paneActions:
		move := k.Down
		move.SetHelp("j/k", "select")
		b = append([]key.Binding{move, k.ShowSQL}, m.targetKeys(m.selected)...)
	case m.focus == paneLineage:
		hide := k.ToggleLineage
		hide.SetHelp("O", "hide")
		b = append([]key.Binding{k.Down, k.Jump}, m.targetKeys(m.lineage.selected())...)
		b = append(b, k.Back, hide)
	case m.focus == paneRun:
		if m.deps.ChangedActions != nil {
			b = append(b, k.RunChanged)
		}
		b = append(b, k.RunTags, k.Deps, k.Dependents, k.FullRefresh)
		if m.deps.StartAPIRun != nil {
			b = append(b, k.API)
		}
		b = append(b, k.SelectedOnly)
	case m.focus == paneLog:
		b = []key.Binding{k.Down, k.HalfDown, k.Top, k.Bottom}
		if m.hasTabs() {
			b = m.tabsKeys()
			break
		}
		if m.runTable.has() {
			swap := k.RunTable
			if m.runTable.shown() {
				swap.SetHelp("t", "log")
				if a := m.runTable.selected(); m.deps.OpenURL != nil && a != nil && a.JobURL != "" {
					b = append([]key.Binding{k.OpenJob}, b...)
				}
			}
			b = append([]key.Binding{swap}, b...)
		}
		if m.deps.OpenURL != nil && m.lastRunURL() != "" {
			// First, so a narrow footer doesn't cut it off.
			b = append([]key.Binding{k.OpenURL}, b...)
		}
	}
	if !m.showErrors && !m.info.open && !m.keyHelp.open && !m.palette.open && !m.tags.open && !m.branches.open && !m.prod.open && !m.changed.open && !m.impact.open && !m.cost.open {
		find := k.Find
		find.SetHelp("/", "find")
		switch {
		case m.columnsFilterable():
			find.SetHelp("/", "filter")
		case m.focus == paneSQL && m.zoomed && len(m.sql.text) > 0:
			find.SetHelp("/", "search")
		case m.focus == paneLog && !m.jobsTyping && (&m).shownTable() != nil:
			find.SetHelp("/", "search")
			sort := k.Columns
			sort.SetHelp("S", "sort")
			b = append([]key.Binding{sort}, b...)
			if (&m).shownTable().query != "" {
				clear := k.Escape
				clear.SetHelp("esc", "clear search")
				b = append([]key.Binding{clear}, b...)
			}
		}
		if !m.cols.typing && !m.jobsTyping {
			b = append([]key.Binding{find}, b...)
		}
		if len(m.paneActions()) > 1 {
			next := k.NextInFile
			next.SetHelp("[/]", "file actions")
			b = append(b, next)
		}
		if m.selected != nil && m.selected.HasIncrementalVariant() {
			b = append(b, k.Incremental)
		}
		if m.deps.Open != nil && m.selected != nil && m.selected.FileName != "" {
			b = append(b, k.Open)
		}
		if m.running {
			// Put cancel first so it survives a narrow footer being cut off.
			cancel := k.Cancel
			cancel.SetHelp("ctrl+c", "cancel run")
			switch {
			case m.cancelling && m.runAPI:
				cancel.SetHelp("ctrl+c", "stop watching")
			case m.cancelling:
				cancel.SetHelp("ctrl+c", "force-kill run")
			}
			b = append([]key.Binding{cancel}, b...)
		} else if m.canRun() {
			b = append(b, k.Run)
		}
		switch {
		case m.showLog:
			b = append(b, k.Log)
			b[len(b)-1].SetHelp("L", "hide log")
		case m.runLabel != "":
			b = append(b, k.Log)
			b[len(b)-1].SetHelp("L", "show log")
		}
		if m.zoomed && !(m.focus == paneSQL && m.sql.hlHits && !m.cols.shown) {
			b = append(b, k.Escape)
			b[len(b)-1].SetHelp("esc", "unzoom")
		} else {
			b = append(b, k.Zoom)
		}
		if m.deps.FullScreen != nil {
			b = append(b, k.FullScreen)
		}
		b = append(b, k.Compile)
		if m.anyErrors() {
			b = append(b, k.Errors)
		}
		b = append(b, k.NextPane, k.Help, k.Quit)
	}
	line := m.help.ShortHelpView(b)
	if run := m.runActions(); m.focus == paneRun && !m.showErrors && !m.info.open && !m.keyHelp.open && !m.changed.open && len(run) > 0 {
		line = m.styles.dim.Render(m.runCommand(actionIDs(run)...)+"  ·  ") + line
	}
	return ansi.Truncate(line, m.width, "…")
}

// overlay draws box centred over base.
func overlay(base, box string, w, h int) string {
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	x, y := max((w-bw)/2, 0), max((h-bh)/2, 0)
	c := lipgloss.NewCanvas(w, h)
	c.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	))
	return c.Render()
}
