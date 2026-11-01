package web

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/diagnose"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

const (
	// changedTimeout bounds waiting for Changed Actions, which may compile
	// the merge-base; impactTimeout and costTimeout a check's report.
	changedTimeout = 10 * time.Minute
	impactTimeout  = 30 * time.Minute
	costTimeout    = 30 * time.Minute
	// prodSaveTimeout bounds compiling with prodCompilerOptions and saving
	// them.
	prodSaveTimeout = 5 * time.Minute
	// baseFetchEvery is how long a fetch keeps the checks' base current:
	// opening a check again within it doesn't go to the network.
	baseFetchEvery = time.Minute
)

func changesRequest(branch string, api, compile bool) changes.Request {
	return changes.Request{DefaultBranch: branch, Head: changesHead(api), Compile: compile}
}

// changedView is the R overlay's list of the Changed Actions.
type changedView struct {
	// What is "the working tree" or "the pushed branch".
	What       string                `json:"what"`
	Count      int                   `json:"count"`
	Title      string                `json:"title"`
	Summary    string                `json:"summary,omitempty"`
	NoChanges  string                `json:"noChanges,omitempty"`
	Comparison string                `json:"comparison,omitempty"`
	Rows       []frontend.ChangedRow `json:"rows"`
	Files      int                   `json:"files"`
}

func whatOf(api bool) string {
	if api {
		return "the pushed branch"
	}
	return "the working tree"
}

// changed works out the Changed Actions of the working tree, or of the
// pushed branch for an API run, against the default branch, compiling the
// base the first time.
func (p *projectState) changed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		API bool `json:"api"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.ChangedActions == nil {
		writeError(w, errors.New("run changed is not available"))
		return
	}
	if !req.API && p.hasErrors() {
		writeError(w, engine.ErrStale) // a run would fail on them, and the stale graph hides the edits
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changedTimeout)
	defer cancel()
	res, err := p.deps.ChangedActions(ctx, changesRequest(p.baseBranch(), req.API, true))
	if err == nil && res == nil {
		err = errors.New("the base was not compiled")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	p.mu.Lock()
	if p.changedRes == nil {
		p.changedRes = map[changes.Head]*changes.Result{}
	}
	p.changedRes[changesHead(req.API)] = res
	p.mu.Unlock()
	v := changedView{What: whatOf(req.API), Count: len(res.Changed), Title: res.Title(), Comparison: res.Comparison(),
		Rows: nonNil(frontend.ChangedRows(res))}
	if len(res.Changed) == 0 {
		v.NoChanges = res.NoChangesMessage()
	} else {
		v.Summary = frontend.ChangesSummary(res)
	}
	files := map[string]bool{}
	for _, c := range res.Changed {
		files[c.FileName] = true
	}
	v.Files = len(files)
	writeJSON(w, v)
}

// changedPreview is the R overlay's footer for the files not skipped and
// the toggles: what r runs and what the deps or dependents add back.
func (p *projectState) changedPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Skip    []string   `json:"skip"`
		Toggles runToggles `json:"toggles"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	t := req.Toggles
	p.mu.Lock()
	g, res := p.st.Graph, p.changedRes[changesHead(t.API)]
	p.mu.Unlock()
	type previewView struct {
		Checked   int    `json:"checked"`
		Files     int    `json:"files"`
		Run       int    `json:"run"`
		Label     string `json:"label,omitempty"`
		Command   string `json:"command,omitempty"`
		StillRun  int    `json:"stillRun"`
		StillNote string `json:"stillNote,omitempty"`
	}
	v := previewView{}
	if res == nil || g == nil {
		writeJSON(w, v)
		return
	}
	skip := setOf(req.Skip)
	seen := map[string]bool{}
	var ids []project.ActionID
	for _, c := range res.Changed {
		if !seen[c.FileName] {
			seen[c.FileName] = true
			v.Files++
			if !skip[c.FileName] {
				v.Checked++
			}
		}
		if !skip[c.FileName] {
			ids = append(ids, c.ID)
		}
	}
	v.Run = len(ids)
	if v.Run > 0 {
		v.Label = frontend.CountRun(v.Run, len(res.Changed))
		v.Command = frontend.RunCommand(p.deps, t.API, frontend.RunOptionsFor(g, ids, t.Deps, t.Dependents, t.Full))
	}
	v.StillRun = frontend.StillRun(g, res, skip, t.Deps, t.Dependents)
	if v.StillRun > 0 {
		v.StillNote = frontend.StillRunNote(t.Deps, t.Dependents, v.StillRun)
	}
	writeJSON(w, v)
}

// tags lists the project's tags the query matches, and the actions a run
// of each starts with.
func (p *projectState) tags(w http.ResponseWriter, r *http.Request) {
	g := p.graph()
	if g == nil {
		writeJSON(w, map[string]any{"tags": []frontend.Tag{}, "total": 0})
		return
	}
	all := frontend.FindTags(g, "")
	writeJSON(w, map[string]any{"tags": nonNil(frontend.FindTags(g, r.URL.Query().Get("q"))), "total": len(all)})
}

// checkRequest asks for the column impact or the query cost: of the
// working tree, or of the pushed branch for an API run; refresh checks
// again when a report on the same changes is kept. Token tells its
// progress events from other pages'.
type checkRequest struct {
	API     bool   `json:"api"`
	Refresh bool   `json:"refresh"`
	Token   string `json:"token"`
}

// fetchBase fetches origin before a check, so its merge-base is with the
// default branch as it is now, not as of the last fetch (xf never fetched,
// so a report could compare with a branch days behind). A check that asked
// to refresh always fetches; any other at most once a baseFetchEvery. A
// fetch that fails (offline, no access) doesn't fail the check: it compares
// with what the checkout has.
func (p *projectState) fetchBase(ctx context.Context, force bool, token string) {
	if p.deps.Branches == nil {
		return
	}
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	if !force && p.deps.Now().Sub(p.fetchedAt) < baseFetchEvery {
		return
	}
	p.hub.send("progress", progressEvent{Token: token, Text: "Fetching origin"})
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	err := p.deps.Branches.Fetch(fctx)
	p.fetchedAt = p.deps.Now()
	if err == nil {
		p.sendBranch()
		p.forgeCheck()
	}
}

// progressEvent is how far a check got: "Dry running 3/7", and for the
// query cost the report so far, once only the merge-base is left.
type progressEvent struct {
	Token   string    `json:"token"`
	Text    string    `json:"text"`
	Partial *costView `json:"partial,omitempty"`
}

// askProdLocked says whether the prodCompilerOptions prompt goes first:
// where nothing sets them, once while xf web runs.
func (p *projectState) askProdLocked() bool {
	return !p.prodAsked && p.deps.SaveProdOptions != nil && p.deps.Unset == config.KeyProdCompilerOptions
}

// impactView is a column impact report as the I overlay shows it.
type impactView struct {
	Prod      bool   `json:"prod,omitempty"` // the prompt goes first
	What      string `json:"what"`
	Page      any    `json:"page,omitempty"`
	Markdown  string `json:"markdown,omitempty"`
	Unset     string `json:"unset,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	HeadLabel string `json:"headLabel,omitempty"`
	Signature string `json:"signature,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
	Empty     bool   `json:"empty,omitempty"`
}

func (p *projectState) impact(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.ColumnImpact == nil {
		writeError(w, errors.New("column impact is not available"))
		return
	}
	p.mu.Lock()
	if p.askProdLocked() {
		p.mu.Unlock()
		writeJSON(w, impactView{Prod: true, What: whatOf(req.API)})
		return
	}
	d := p.deps
	d.DefaultBranch = p.base
	p.mu.Unlock()
	if !req.API && p.hasErrors() {
		writeError(w, engine.ErrStale)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), impactTimeout)
	defer cancel()
	p.fetchBase(ctx, req.Refresh, req.Token)
	rep, err := d.ColumnImpact(ctx, engine.ImpactRequest{DefaultBranch: d.DefaultBranch, Head: changesHead(req.API), Refresh: req.Refresh,
		DevCompilerOptions: d.DevCompilerOptions, ProdCompilerOptions: d.ProdCompilerOptions, DbtProdTarget: d.DbtProdTarget, Unset: d.Unset},
		progress(p, req.Token, func(pr impact.Progress) (string, *costView) { return progressText(pr.Phase, pr.Done, pr.Total), nil }))
	if err != nil {
		writeError(w, err)
		return
	}
	l := impact.BuildLedger(rep)
	v := impactView{What: whatOf(req.API), Page: impact.PageData(rep), Markdown: impact.Markdown(rep), Unset: frontend.UnsetImpactHint(rep),
		CheckedAt: rep.CheckedAt.Local().Format("15:04"), Signature: rep.Signature, Stale: rep.Stale, Empty: len(l.Rows) == 0}
	if c := rep.Comparison; c != nil {
		v.HeadLabel = c.HeadLabel
	}
	writeJSON(w, v)
}

// progress sends a check's progress to the pages, the newest at most
// every tenth of a second.
func progress[P any](p *projectState, token string, text func(P) (string, *costView)) func(P) {
	var mu sync.Mutex
	var last time.Time
	return func(pr P) {
		t, partial := text(pr)
		mu.Lock()
		if partial == nil && time.Since(last) < 100*time.Millisecond {
			mu.Unlock()
			return
		}
		last = time.Now()
		mu.Unlock()
		p.hub.send("progress", progressEvent{Token: token, Text: t, Partial: partial})
	}
}

// progressText is "Dry running 3/7".
func progressText(phase string, done, total int) string {
	if phase == "" {
		return "Working out the changed actions"
	}
	if total == 0 {
		return phase
	}
	return fmt.Sprintf("%s %d/%d", phase, done, total)
}

// costView is a query cost report as the C overlay shows it.
type costView struct {
	Prod        bool               `json:"prod,omitempty"`
	What        string             `json:"what"`
	Report      *frontend.CostView `json:"report,omitempty"`
	CheckedAt   string             `json:"checkedAt,omitempty"`
	HeadLabel   string             `json:"headLabel,omitempty"`
	Signature   string             `json:"signature,omitempty"`
	Stale       bool               `json:"stale,omitempty"`
	BasePending bool               `json:"basePending,omitempty"`
}

func costViewOf(rep *cost.Report, api bool) *costView {
	r := frontend.CostReport(rep)
	v := &costView{What: whatOf(api), Report: &r, CheckedAt: rep.CheckedAt.Local().Format("15:04"), Signature: rep.Signature,
		Stale: rep.Stale, BasePending: rep.BasePending}
	if c := rep.Comparison; c != nil {
		v.HeadLabel = c.HeadLabel
	}
	return v
}

func (p *projectState) cost(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.QueryCost == nil {
		writeError(w, errors.New("query cost is not available"))
		return
	}
	p.mu.Lock()
	if p.askProdLocked() {
		p.mu.Unlock()
		writeJSON(w, costView{Prod: true, What: whatOf(req.API)})
		return
	}
	d := p.deps
	d.DefaultBranch = p.base
	p.mu.Unlock()
	if !req.API && p.hasErrors() {
		writeError(w, engine.ErrStale)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), costTimeout)
	defer cancel()
	p.fetchBase(ctx, req.Refresh, req.Token)
	rep, err := d.QueryCost(ctx, engine.CostRequest{DefaultBranch: d.DefaultBranch, Head: changesHead(req.API), Refresh: req.Refresh,
		ProdCompilerOptions: d.ProdCompilerOptions, DbtProdTarget: d.DbtProdTarget, Unset: d.Unset},
		progress(p, req.Token, func(pr cost.Progress) (string, *costView) {
			var partial *costView
			if pr.Partial != nil {
				partial = costViewOf(pr.Partial, req.API)
			}
			return progressText(pr.Phase, pr.Done, pr.Total), partial
		}))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, costViewOf(rep, req.API))
}

// prod answers the prodCompilerOptions prompt: options to compile with
// and save, Default to save that the default targets are prod, or Dismiss
// to check against the defaults this time.
func (p *projectState) prod(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options string `json:"options"`
		Default bool   `json:"default"`
		Dismiss bool   `json:"dismiss"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.mu.Lock()
	save := p.deps.SaveProdOptions
	if req.Dismiss || save == nil {
		p.prodAsked = true
		p.mu.Unlock()
		writeJSON(w, okView{OK: true})
		return
	}
	p.mu.Unlock()
	options := strings.TrimSpace(req.Options)
	if req.Default {
		options = ""
	} else if options == "" {
		writeError(w, errors.New("type the compiler options, or pick the default targets"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), prodSaveTimeout)
	defer cancel()
	if err := save(ctx, options); err != nil {
		writeError(w, err)
		return
	}
	p.mu.Lock()
	p.prodAsked = true
	p.deps.ProdCompilerOptions, p.deps.Unset = options, ""
	frontend.SetInfoSetting(p.deps.Info, config.KeyProdCompilerOptions, options, config.ProjectFile)
	p.mu.Unlock()
	writeJSON(w, okView{OK: true, Message: "saved " + config.KeyProdCompilerOptions + " to " + config.ProjectFile})
}

// dryRunError is a failed dry run the e overlay lists.
type dryRunError struct {
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	File   string `json:"file,omitempty"`
	ID     string `json:"id"`
	Msg    string `json:"msg"`
	Denied bool   `json:"denied,omitempty"`
}

// dryRunErrorsLocked are the failed dry runs of the graph's actions as
// compiled now, and whether any failed for want of credentials (see the
// TUI's dryRunErrors).
func (p *projectState) dryRunErrorsLocked() (errs []dryRunError, auth bool) {
	g := p.st.Graph
	if p.deps.DryRun == nil || g == nil {
		return nil, false
	}
	failed := false
	for _, e := range p.dry {
		if e.done && e.err != nil {
			failed = true
			break
		}
	}
	if !failed {
		return nil, false
	}
	for _, id := range slices.Sorted(maps.Keys(g.Actions)) {
		a := g.Actions[id]
		proj, location := g.DryRunTarget(a)
		for _, q := range p.deferredLocked(a).DryRunQueries() {
			e := p.dry[dryRunKey(proj, location, q.SQL)]
			switch {
			case e == nil || !e.done || e.err == nil:
			case errors.Is(e.err, bigquery.ErrAuth):
				auth = true
			default:
				msg, denied := e.err.Error(), false
				if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
					msg, denied = apiErr.Message, apiErr.Status == http.StatusForbidden
				}
				errs = append(errs, dryRunError{Name: a.Target.Schema + "." + a.Target.Name, Label: q.Label, File: a.FileName,
					ID: string(a.ID), Msg: msg, Denied: denied})
			}
		}
	}
	return errs, auth
}

// retryOldFailuresLocked asks again the failed dry runs older than
// engine.DryRunMaxAge, so the e overlay soon drops those fixed since,
// returning what to run once the lock is let go.
func (p *projectState) retryOldFailuresLocked() []func() {
	g := p.st.Graph
	if p.deps.DryRun == nil || g == nil {
		return nil
	}
	now := p.deps.Now()
	var old []*project.Action
	for _, id := range slices.Sorted(maps.Keys(g.Actions)) {
		a := p.deferredLocked(g.Actions[id])
		proj, location := g.DryRunTarget(a)
		for _, q := range a.DryRunQueries() {
			if e := p.dry[dryRunKey(proj, location, q.SQL)]; e != nil && e.err != nil && e.stale(now) {
				old = append(old, a)
				break
			}
		}
	}
	return p.startDryRunsLocked(g, old)
}

// fileDryRun is a file's failed dry runs, as the status line shows them
// beside the file: how many, and the first one's action and message.
type fileDryRun struct {
	Count int    `json:"count"`
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg"`
}

// dryRunsByFile are errs by the file of their action.
func dryRunsByFile(errs []dryRunError) map[string]fileDryRun {
	var out map[string]fileDryRun
	for _, e := range errs {
		if e.File == "" {
			continue
		}
		if out == nil {
			out = map[string]fileDryRun{}
		}
		f, ok := out[e.File]
		if !ok {
			f = fileDryRun{Name: e.Name, Label: e.Label, Msg: clip(firstLine(e.Msg), 300)}
		}
		f.Count++
		out[e.File] = f
	}
	return out
}

// clip is s cut to n runes, with an ellipsis when cut.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// errorsView answers with what pane 2's errors tab and the e overlay show:
// the compile's errors, then the dry runs'.
func (p *projectState) errorsView(w http.ResponseWriter, r *http.Request) {
	type compileError struct {
		File    string `json:"file"`
		Message string `json:"message"`
		// Kind is a diagnose kind, Line the file's 1-based line (0 when
		// the error names none) and Snippet the code a syntax error shows.
		Kind    string `json:"kind"`
		Line    int    `json:"line,omitempty"`
		Snippet string `json:"snippet,omitempty"`
		// Action is an action of the file in the last good compile, which
		// the source pane shows the file by; "" when there is none.
		Action string `json:"action,omitempty"`
	}
	type view struct {
		Tool       string         `json:"tool"`
		CompileErr string         `json:"compileErr,omitempty"`
		Errors     []compileError `json:"errors"`
		Stale      bool           `json:"stale,omitempty"`
		// Bin is the tool's binary; At and Took are when the failed
		// compile finished and how long it took, as seen here.
		Bin    string        `json:"bin,omitempty"`
		At     time.Time     `json:"at,omitzero"`
		Took   float64       `json:"took,omitempty"`
		DryRun []dryRunError `json:"dryRun"`
		Auth   string        `json:"auth,omitempty"`
		// Fix is the command that may get the compile going again, such
		// as one that installs the CLI it couldn't find; nil for none.
		Fix *fix `json:"fix,omitempty"`
	}
	p.checkCreds()
	missing := p.cliMissing()
	p.mu.Lock()
	v := view{Tool: p.tool, Stale: p.st.Stale, Errors: []compileError{}, At: p.failedAt, Took: p.failedTook.Seconds()}
	if p.deps.Info != nil && p.install == nil { // else looked up at each compile
		v.Bin = p.deps.Info.ToolBin
	}
	if p.st.Err != nil {
		v.CompileErr = p.st.Err.Error()
	}
	v.Fix = p.fixLocked(missing)
	for _, e := range p.st.Errors {
		ce := compileError{File: e.FileName, Message: e.Message, Kind: diagnose.Kind(e),
			Line: diagnose.Line(p.deps.Root, e, os.ReadFile), Snippet: diagnose.Snippet(e)}
		if g := p.st.Graph; g != nil && e.FileName != "" {
			if as := g.ActionsInFile(e.FileName); len(as) > 0 {
				ce.Action = string(as[0].ID)
			}
		}
		v.Errors = append(v.Errors, ce)
	}
	errs, auth := p.dryRunErrorsLocked()
	v.DryRun = nonNil(errs)
	if auth {
		v.Auth = "no credentials: " + bigquery.AuthHint
	}
	starts := p.retryOldFailuresLocked()
	p.mu.Unlock()
	for _, start := range starts {
		go start()
	}
	writeJSON(w, v)
}

// info answers with the , overlay's rows, as writeRows sends them;
// ?following says whether the page follows the editor.
func (p *projectState) info(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	d, editor := p.deps, p.lastEditor
	if d.Info != nil { // setEditor changes it
		info := *d.Info
		info.Settings = slices.Clone(info.Settings)
		d.Info = &info
	}
	p.mu.Unlock()
	following := r.URL.Query().Get("following") == "1"
	writeRows(w, r, func(ctx context.Context, emit func([]frontend.InfoRow)) {
		frontend.ProjectInfoStream(ctx, d, editor, following, emit)
	})
}
