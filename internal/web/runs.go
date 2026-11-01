package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
)

// The run is the server's, not a page's: one at a time, as in the TUI,
// shown by every page, and still going when the page that started it is
// closed. Its log and its report (an API run's actions and their jobs)
// are kept here, and sent to the pages as they grow.

// runState is the run in flight, or the last one.
type runState struct {
	running, cancelling bool
	// api: the run is in GCP, through the Dataform API.
	api bool
	// watching: the run is a workflow invocation xf did not start, which
	// it only follows (see watch.go); looking: one is being looked up
	// before it becomes the run.
	watching, looking bool
	// seq counts the runs, so a page tells a new run's log from the last.
	seq int
	// label names what runs ("analytics.orders", "5 changed actions"),
	// short the same for the status line, and title is the log's state:
	// "running…", "✓ exit 0 · 3.1s".
	label, short, title string
	// command runs a local run again in a terminal: a cd to the project
	// and the command line; "" for a run in GCP.
	command string
	lines   []logLine
	// sent is how many lines the pages have been sent.
	sent   int
	run    *project.Run
	cancel context.CancelFunc
	// report is the run's actions as of the last look, nil for a run that
	// reports none.
	report *project.RunReport
	// ran is set once a run was started here: until then the jobs tab
	// shows the last kept run's.
	ran bool

	history    []runhistory.Entry
	historyErr string
	refreshing map[string]bool
}

// logLine is a line of the log, and its tone: "", "dim", "warn", "error"
// or "ok".
type logLine struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	// Spans colour Text as the tool did (see colorLine); nil for a line
	// in one tone.
	Spans []logSpan `json:"spans,omitempty"`
	// Copy is what the line's copy button copies.
	Copy string `json:"copy,omitempty"`
	// Fix is a command that may get the run going, which the line offers
	// to run in the terminal tab; nil for none.
	Fix *fix `json:"fix,omitempty"`
}

// runToggles are the Run pane's toggles.
type runToggles struct {
	Deps       bool `json:"deps"`
	Dependents bool `json:"dependents"`
	Full       bool `json:"full"`
	API        bool `json:"api"`
}

// runView is the run as the pages show it.
type runView struct {
	Seq        int    `json:"seq"`
	Running    bool   `json:"running"`
	Cancelling bool   `json:"cancelling,omitempty"`
	API        bool   `json:"api,omitempty"`
	Watching   bool   `json:"watching,omitempty"`
	Label      string `json:"label,omitempty"`
	Short      string `json:"short,omitempty"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	// Lines is the whole log when asked for (a page that starts or
	// reconnects); LineCount is its length either way.
	Lines     []logLine `json:"lines,omitempty"`
	LineCount int       `json:"lineCount"`
	Ran       bool      `json:"ran"`
	Jobs      *jobsView `json:"jobs,omitempty"`
	// Command is the run's command to paste into a terminal, for y.
	Command string `json:"command,omitempty"`
}

// jobsView is a run's report as the jobs tab shows it.
type jobsView struct {
	State   string               `json:"state,omitempty"`
	URL     string               `json:"url,omitempty"`
	Rows    []frontend.JobRow    `json:"rows"`
	Total   frontend.JobRow      `json:"total"`
	Summary frontend.JobsSummary `json:"summary"`
	Title   string               `json:"title,omitempty"`
}

func jobsOf(r *project.RunReport, now time.Time) *jobsView {
	if r == nil {
		return nil
	}
	rows, total := frontend.JobsTable(r, now)
	return &jobsView{State: r.State, URL: r.URL, Rows: nonNil(rows), Total: total, Summary: frontend.SummarizeJobs(r, now)}
}

func (p *projectState) runViewLocked(withLines bool) runView {
	r := &p.run
	v := runView{Seq: r.seq, Running: r.running, Cancelling: r.cancelling, API: r.api, Watching: r.watching, Label: r.label, Short: r.short,
		Title: r.title, LineCount: len(r.lines), Ran: r.ran, Command: r.command}
	if r.run != nil && r.run.URL != nil {
		v.URL = r.run.URL()
	}
	if r.report != nil && v.URL == "" {
		v.URL = r.report.URL
	}
	if withLines {
		v.Lines = append([]logLine{}, r.lines...)
	}
	now := time.Time{}
	if r.running {
		now = p.deps.Now()
	}
	v.Jobs = jobsOf(r.report, now)
	return v
}

// runPreviewLocked is what r runs from the Actions pane: its runnable
// actions, or with selected only the selected action if runnable.
func (p *projectState) runPreviewLocked(g *project.Graph, a *project.Action, pane []*project.Action, selectedOnly bool, t runToggles) runPreview {
	if p.deps.StartRun == nil {
		return runPreview{Actions: []project.ActionID{}, Why: "running is not available"}
	}
	if selectedOnly {
		pane = []*project.Action{a}
	}
	var run []*project.Action
	for _, b := range pane {
		if b.Runnable() {
			run = append(run, b)
		}
	}
	v := runPreview{Actions: []project.ActionID{}}
	if len(run) == 0 {
		v.Why = a.Kind.String() + "s can't be run"
		return v
	}
	for _, b := range run {
		v.Actions = append(v.Actions, b.ID)
	}
	v.Label = frontend.RunLabel(run)
	if len(run) == 1 {
		v.Name = run[0].Target.Name
	}
	v.Command = frontend.RunCommand(p.deps, t.API && p.deps.StartAPIRun != nil, frontend.RunOptionsFor(g, v.Actions, t.Deps, t.Dependents, t.Full))
	v.Defer = p.runDeferLocked(g, run, t)
	return v
}

// gitView is the line under the toggles while api is on, for the views
// that run and for those that check (see frontend.APIGitParts).
type gitView struct {
	Run   []frontend.GitPart `json:"run"`
	Check []frontend.GitPart `json:"check"`
}

// git reads the checkout as an API run sees it. The page asks when api
// turns on, when a compile starts and when it gets focus back.
func (p *projectState) git(w http.ResponseWriter, r *http.Request) {
	if p.deps.APIGit == nil {
		writeJSON(w, gitView{Run: []frontend.GitPart{}, Check: []frontend.GitPart{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := p.deps.APIGit(ctx)
	writeJSON(w, gitView{Run: frontend.APIGitParts(g, err, "run"), Check: frontend.APIGitParts(g, err, "check")})
}

// gitTimeout bounds one read of the checkout, as the TUI's does.
const gitTimeout = 10 * time.Second

// runRequest asks for a run: of actions (the Actions pane's), of the
// Changed Actions in the files not skipped, or of tags.
type runRequest struct {
	Kind    string             `json:"kind"` // actions, changed or tags
	IDs     []project.ActionID `json:"ids"`
	Skip    []string           `json:"skip"`
	Tags    []string           `json:"tags"`
	Toggles runToggles         `json:"toggles"`
	// DeferOK are the answers to defer to prod's questions so far (see
	// deferPlanFor).
	DeferOK []string `json:"deferOK,omitempty"`
}

func (p *projectState) startRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if !readJSON(w, r, &req) {
		return
	}
	p.startRunReq(w, r, req)
}

// startRunReq starts the run req asks for, once defer to prod has checked
// it (defer_run.go).
func (p *projectState) startRunReq(w http.ResponseWriter, r *http.Request, req runRequest) {
	d := p.deps
	if d.StartRun == nil {
		writeJSON(w, okView{Message: "running is not available"})
		return
	}
	t := req.Toggles
	if t.API && d.StartAPIRun == nil {
		writeJSON(w, okView{Message: "API runs are not available"})
		return
	}
	p.mu.Lock()
	if p.run.running {
		p.mu.Unlock()
		writeJSON(w, okView{Message: "a run is already in progress (ctrl+c cancels it)"})
		return
	}
	g := p.st.Graph
	if g == nil {
		p.mu.Unlock()
		writeJSON(w, okView{Message: "no actions yet: waiting for a compile"})
		return
	}
	var (
		header, label, short string
		ids                  []project.ActionID
		targets              []project.Target
		opts                 project.RunOptions
		api                  dataformapi.Options
	)
	lines := []logLine{}
	switch req.Kind {
	case "tags":
		if len(req.Tags) == 0 {
			p.mu.Unlock()
			writeJSON(w, okView{Message: "nothing checked · space checks a tag"})
			return
		}
		opts = frontend.RunOptionsFor(g, nil, t.Deps, t.Dependents, t.Full)
		opts.Tags = req.Tags
		api = frontend.APIRunOptions(g, t.Deps, t.Dependents, t.Full)
		api.Invocation.IncludedTargets, api.Invocation.IncludedTags = nil, req.Tags
		label = frontend.TagsLabel(req.Tags)
		short = label
		lines = append(lines, logLine{Text: frontend.RunCommand(d, t.API, opts), Tone: "dim"})
	case "changed":
		res := p.changedRes[changesHead(t.API)]
		if res == nil {
			p.mu.Unlock()
			writeJSON(w, okView{Message: "no changed actions to run: open R again"})
			return
		}
		skip := setOf(req.Skip)
		for _, c := range res.Changed {
			if !skip[c.FileName] {
				ids, targets = append(ids, c.ID), append(targets, c.Target)
			}
		}
		if len(ids) == 0 {
			p.mu.Unlock()
			writeJSON(w, okView{Message: "nothing checked · space includes a file, x all"})
			return
		}
		label = frontend.CountRun(len(ids), len(res.Changed))
		short = label
		opts = frontend.RunOptionsFor(g, ids, t.Deps, t.Dependents, t.Full)
		api = frontend.APIRunOptions(g, t.Deps, t.Dependents, t.Full, targets...)
		header = label + " " + res.Title()
		lines = append(lines, logLine{Text: header, Tone: "dim"}, logLine{Text: frontend.RunCommand(d, t.API, opts), Tone: "dim"})
	default:
		var actions []*project.Action
		for _, id := range req.IDs {
			if a := g.Actions[id]; a != nil && a.Runnable() {
				actions = append(actions, a)
			}
		}
		if len(actions) == 0 {
			p.mu.Unlock()
			writeJSON(w, okView{Message: "select an action to run"})
			return
		}
		for _, a := range actions {
			ids, targets = append(ids, a.ID), append(targets, a.Target)
		}
		label, short = frontend.RunLabel(actions), frontend.RunLabel(actions)
		if len(actions) == 1 {
			short = actions[0].Target.Name
		}
		opts = frontend.RunOptionsFor(g, ids, t.Deps, t.Dependents, t.Full)
		api = frontend.APIRunOptions(g, t.Deps, t.Dependents, t.Full, targets...)
		lines = append(lines, logLine{Text: frontend.RunCommand(d, t.API, opts), Tone: "dim"})
	}
	// Defer to prod checks the run first: it may ask, or run more.
	runSet := deferral.RunSet(g, opts)
	p.mu.Unlock()
	plan, ask := p.deferPlanFor(r.Context(), req, runSet)
	if ask != nil {
		writeJSON(w, runAnswer{Ask: ask})
		return
	}
	if plan.retry != nil {
		p.startRunReq(w, r, *plan.retry)
		return
	}
	p.mu.Lock()
	if p.run.running {
		p.mu.Unlock()
		writeJSON(w, okView{Message: "a run is already in progress (ctrl+c cancels it)"})
		return
	}
	lines = append(lines, plan.lines...)
	opts.Deferred, api.Deferred = plan.pairs, plan.pairs
	opts.DeferState = plan.dbtState
	ctx, cancel := context.WithCancel(context.Background())
	p.run.seq++
	seq := p.run.seq
	p.run.running, p.run.cancelling, p.run.api, p.run.watching, p.run.ran = true, false, t.API, false, true
	p.run.label, p.run.short, p.run.title = label, short, "running…"
	p.run.lines, p.run.sent, p.run.run, p.run.cancel, p.run.report = lines, len(lines), nil, cancel, nil
	p.run.command = ""
	if !t.API {
		p.run.command = terminalCommand(d.Root, strings.TrimPrefix(frontend.RunCommand(d, false, opts), "$ "))
	}
	v := p.runViewLocked(true)
	p.mu.Unlock()
	p.hub.send("run", v)
	writeJSON(w, okView{OK: true})

	go func() {
		if err := p.makeProxies(ctx, plan); err != nil {
			p.startFailed(seq, err)
			return
		}
		var run *project.Run
		var err error
		if t.API {
			run, err = d.StartAPIRun(ctx, api)
		} else {
			opts.Color = true // the log shows the tool's colours
			run, err = d.StartRun(ctx, opts)
		}
		if err != nil {
			p.startFailed(seq, err)
			return
		}
		p.follow(seq, run)
	}()
}

func setOf(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set
}

// startFailed says the run seq could not start.
func (p *projectState) startFailed(seq int, err error) {
	p.mu.Lock()
	if p.run.seq != seq {
		p.mu.Unlock()
		return
	}
	what := p.tool
	if p.run.api {
		what = "the API run"
	}
	p.run.running, p.run.cancel = false, nil
	p.appendLocked(logLine{Text: "✗ could not start " + what + ": " + err.Error(), Tone: "error"})
	if errors.Is(err, dataformapi.ErrAuth) {
		p.appendLocked(logLine{Text: "  to log in, " + dataformapi.AuthHint, Tone: "warn"})
	}
	p.run.title = "failed to start"
	p.mu.Unlock()
	p.flushLog(true)
	p.tellRunEnded()
}

// tellRunEnded tells runEnded that the run is over.
func (p *projectState) tellRunEnded() {
	if p.runEnded != nil {
		p.runEnded()
	}
}

func (p *projectState) appendLocked(l logLine) { p.run.lines = append(p.run.lines, l) }

// flushLog sends the lines the pages haven't had, and with status the
// run's state too.
func (p *projectState) flushLog(status bool) {
	p.mu.Lock()
	r := &p.run
	type logEvent struct {
		Seq   int       `json:"seq"`
		From  int       `json:"from"`
		Lines []logLine `json:"lines"`
	}
	var ev *logEvent
	if r.sent < len(r.lines) {
		ev = &logEvent{Seq: r.seq, From: r.sent, Lines: append([]logLine{}, r.lines[r.sent:]...)}
		r.sent = len(r.lines)
	}
	var v *runView
	if status {
		rv := p.runViewLocked(false)
		v = &rv
	}
	p.mu.Unlock()
	if ev != nil {
		p.hub.send("log", ev)
	}
	if v != nil {
		p.hub.send("run", v)
	}
}

// follow reads the run seq's lines until it ends, sending them to the
// pages every tenth of a second and its report every second.
func (p *projectState) follow(seq int, run *project.Run) {
	p.mu.Lock()
	if p.run.seq != seq {
		p.mu.Unlock()
		return
	}
	p.run.run = run
	p.mu.Unlock()
	p.flushLog(true)

	stop := make(chan struct{})
	go func() {
		lines, jobs := time.NewTicker(100*time.Millisecond), time.NewTicker(time.Second)
		defer lines.Stop()
		defer jobs.Stop()
		for {
			select {
			case <-stop:
				return
			case <-lines.C:
				p.flushLog(false)
			case <-jobs.C:
				p.syncReport()
				p.flushLog(true)
			}
		}
	}()
	for line := range run.Lines {
		p.mu.Lock()
		p.appendLocked(colorLine(line))
		p.mu.Unlock()
	}
	res := <-run.Done
	close(stop)
	p.syncReport()
	p.mu.Lock()
	p.run.running, p.run.cancelling, p.run.cancel = false, false, nil
	p.finishLocked(res)
	p.expireTables(false) // the run may have built them
	// or the tables failed dry runs read: the engine forgets its failures
	// before the page, asked for the details again, dry-runs them anew.
	if forget := p.deps.ForgetFailedDryRuns; forget != nil {
		forget()
	}
	p.forgetFailedDryRuns()
	p.deferExpireLocked() // the run may have built upstream tables in dev
	p.mu.Unlock()
	p.flushLog(true)
	p.hub.send("detail", nil)
	p.tellRunEnded()
	p.loadHistory()
}

func (p *projectState) syncReport() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.run.run; r != nil && r.Report != nil {
		if rep := r.Report(); rep != nil {
			p.run.report = rep
		}
	}
}

// finishLocked ends the log with how the run ended, as the TUI's log pane
// does.
func (p *projectState) finishLocked(res project.RunResult) {
	secs := fmt.Sprintf("%.1fs", res.Duration.Seconds())
	r := &p.run
	add := func(text, tone, title string) {
		p.appendLocked(logLine{Text: text, Tone: tone})
		r.title = title
	}
	switch {
	case res.Detached:
		add("◌ stopped watching · "+secs+": the invocation carries on in GCP", "warn", "stopped watching · "+secs)
	case res.State != "" && res.Err == nil:
		st := strings.ToLower(res.State)
		switch {
		case res.Canceled:
			add("✗ "+st+" · "+secs, "warn", st+" · "+secs)
		case res.ExitCode != 0:
			add("✗ "+st+" · "+secs, "error", "✗ "+st+" · "+secs)
		default:
			add("✓ "+st+" · "+secs, "ok", "✓ "+st+" · "+secs)
		}
	case res.Canceled:
		add("✗ cancelled · "+secs, "warn", "cancelled · "+secs)
	case res.Err != nil:
		add("✗ "+res.Err.Error()+" · "+secs, "error", "failed · "+secs)
	case res.ExitCode != 0:
		add(fmt.Sprintf("✗ exit %d · %s", res.ExitCode, secs), "error", fmt.Sprintf("✗ exit %d · %s", res.ExitCode, secs))
	default:
		add("✓ exit 0 · "+secs, "ok", "✓ exit 0 · "+secs)
	}
	// A local run that failed may want what xf can't give it, such as a
	// prompt answered: say how to run it in a terminal.
	if r.command != "" && !res.Canceled && (res.Err != nil || res.ExitCode != 0) {
		p.appendLocked(logLine{Text: "to run it in a terminal, copy its command (y):", Tone: "dim", Copy: r.command})
	}
	if !res.Canceled && (res.Err != nil || res.ExitCode != 0) {
		if f := p.runFixLocked(); f != nil {
			p.appendLocked(logLine{Text: f.Why + ": " + f.Command, Fix: f})
		}
	}
}

// terminalCommand is cmdline, which runs in root, as one line to paste
// into a terminal anywhere.
func terminalCommand(root, cmdline string) string {
	if root == "" {
		return cmdline
	}
	return cmdrun.ShellJoin([]string{"cd", root}) + " && " + cmdline
}

// cancelRun asks the run to stop as ctrl+c does in the TUI; asked again,
// it force-kills it, or for a run in GCP stops watching it.
func (p *projectState) cancelRun(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	run := &p.run
	if !run.running {
		p.mu.Unlock()
		writeJSON(w, okView{Message: "no run in progress"})
		return
	}
	if !run.cancelling {
		run.cancelling = true
		if run.cancel != nil {
			run.cancel()
		}
		run.title = "cancelling…"
		msg := "^C cancelling: dataform is stopping; BigQuery jobs it already started keep running (ctrl+c again force-kills)"
		switch {
		case run.watching:
			msg = "^C stops watching: the workflow carries on in GCP"
		case run.api:
			msg = "^C cancelling the workflow invocation (ctrl+c again stops watching)"
		case p.tool == "dbt":
			msg = "^C cancelling: dbt is stopping (ctrl+c again force-kills)"
		}
		p.appendLocked(logLine{Text: msg, Tone: "warn"})
	} else if run.run != nil && run.run.Kill != nil {
		run.run.Kill()
		if !run.api {
			p.appendLocked(logLine{Text: "^C force-killed; BigQuery jobs that were still running may carry on", Tone: "error"})
		}
	}
	p.mu.Unlock()
	p.flushLog(true)
	writeJSON(w, okView{OK: true})
}

// historyView is the kept runs, as the history tab lists them.
type historyView struct {
	Rows []frontend.HistoryRow `json:"rows"`
	Err  string                `json:"err,omitempty"`
	// Last is the jobs of the newest kept run, which the jobs tab shows
	// until a run is started here.
	Last *jobsView `json:"last,omitempty"`
}

func (p *projectState) historyViewLocked() historyView {
	r := &p.run
	v := historyView{Rows: nonNil(frontend.HistoryRows(r.history, p.deps.Now())), Err: r.historyErr}
	if len(r.history) > 0 {
		e := r.history[0]
		v.Last = jobsOf(e.Report, time.Time{})
		if v.Last == nil {
			v.Last = &jobsView{Rows: []frontend.JobRow{}}
		}
		v.Last.Title = frontend.HistoryRows(r.history[:1], p.deps.Now())[0].Title
	}
	return v
}

// loadHistory reads the kept runs and sends them to the pages, looking up
// what became of the newest if it was never seen to end.
func (p *projectState) loadHistory() {
	runs := p.deps.Runs
	if runs == nil {
		return
	}
	entries, err := runs.List()
	p.mu.Lock()
	p.run.history, p.run.historyErr = entries, ""
	if err != nil {
		p.run.historyErr = err.Error()
	}
	v := p.historyViewLocked()
	p.mu.Unlock()
	p.hub.send("history", v)
	if len(entries) > 0 {
		p.refreshKept(entries[0].ID)
	}
}

// refreshKept looks up what became of a kept run that was never seen to
// end and that nobody follows: not the one going here.
func (p *projectState) refreshKept(id string) bool {
	runs := p.deps.Runs
	p.mu.Lock()
	i := -1
	for j, e := range p.run.history {
		if e.ID == id {
			i = j
		}
	}
	if runs == nil || i < 0 || !p.run.history[i].Stale(p.deps.Now()) || p.run.running && i == 0 || p.run.refreshing[id] {
		p.mu.Unlock()
		return false
	}
	if p.run.refreshing == nil {
		p.run.refreshing = map[string]bool{}
	}
	p.run.refreshing[id] = true
	e := p.run.history[i]
	p.mu.Unlock()
	go func() {
		got, _ := runs.Refresh(context.Background(), e)
		p.mu.Lock()
		delete(p.run.refreshing, id)
		for j := range p.run.history {
			if p.run.history[j].ID == got.ID {
				p.run.history[j] = got
			}
		}
		v := p.historyViewLocked()
		p.mu.Unlock()
		p.hub.send("history", v)
	}()
	return true
}

func (p *projectState) refreshHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, okView{OK: p.refreshKept(req.ID)})
}

// historyJobs answers with a kept run's jobs.
func (p *projectState) historyJobs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.run.history {
		if e.ID == id {
			v := jobsOf(e.Report, time.Time{})
			if v == nil {
				v = &jobsView{Rows: []frontend.JobRow{}}
			}
			v.Title = frontend.HistoryRows([]runhistory.Entry{e}, p.deps.Now())[0].Title
			writeJSON(w, v)
			return
		}
	}
	http.Error(w, "no such run", http.StatusNotFound)
}

// clearHistory forgets the kept runs, but for one that is going.
func (p *projectState) clearHistory(w http.ResponseWriter, r *http.Request) {
	runs := p.deps.Runs
	if runs == nil {
		writeJSON(w, okView{Message: "no history here"})
		return
	}
	if err := runs.Clear(); err != nil {
		writeJSON(w, okView{Message: "could not clear the history: " + err.Error()})
		return
	}
	p.mu.Lock()
	msg := "history cleared"
	if p.run.running {
		msg = "history cleared, but for the run that is going"
	}
	if !p.run.ran {
		p.run.report = nil
	}
	p.mu.Unlock()
	p.loadHistory()
	writeJSON(w, okView{OK: true, Message: msg})
}

func changesHead(api bool) changes.Head {
	if api {
		return changes.HeadPushed
	}
	return changes.HeadWorktree
}
