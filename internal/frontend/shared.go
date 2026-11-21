package frontend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// APIGitParts is the line that says what an API run runs from checkout g,
// or err reading it, in the views of verb: "run" or "check" (see
// APIGitLine).
func APIGitParts(g dataformapi.GitState, err error, verb string) []GitPart {
	return APIGitLine(g, err, true, verb)
}

// StillRunNote is the R overlay's warning that n unchecked actions run
// anyway, as deps or dependents of the checked ones.
func StillRunNote(deps, dependents bool, n int) string {
	flags, as := "d", "deps"
	switch {
	case deps && dependents:
		flags, as = "d, D", "deps or dependents"
	case dependents:
		flags, as = "D", "dependents"
	}
	verb := "run"
	if n == 1 {
		verb = "runs"
	}
	return fmt.Sprintf("⚠ %s: %d unchecked action%s still %s as %s", flags, n, Plural(n), verb, as)
}

// StillRun counts the unchecked Changed Actions of res (those in skipped
// files) that a run of the checked ones reaches anyway with deps or
// dependents, in g. With an API run it may be off a little, since that
// compiles the pushed branch.
func StillRun(g *project.Graph, res *changes.Result, skip map[string]bool, deps, dependents bool) int {
	if g == nil || res == nil || !deps && !dependents {
		return 0
	}
	var run []changes.Changed
	unchecked := map[project.ActionID]bool{}
	for _, ch := range res.Changed {
		if skip[ch.FileName] {
			unchecked[ch.ID] = true
		} else {
			run = append(run, ch)
		}
	}
	if len(run) == 0 || len(unchecked) == 0 {
		return 0
	}
	reached := map[project.ActionID]bool{}
	// Each flag reaches from the checked actions on its own, as dataform's do.
	walk := func(next func(project.ActionID) []*project.Action) {
		seen := map[project.ActionID]bool{}
		var queue []project.ActionID
		for _, ch := range run {
			queue = append(queue, ch.ID)
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, a := range next(id) {
				if a == nil || seen[a.ID] {
					continue
				}
				seen[a.ID], reached[a.ID] = true, true
				queue = append(queue, a.ID)
			}
		}
	}
	if deps {
		walk(g.Dependencies)
	}
	if dependents {
		walk(g.Dependents)
	}
	n := 0
	for id := range unchecked {
		if reached[id] {
			n++
		}
	}
	return n
}

// RunCommand says what a run with o would run, as the log's first line
// does: the command line, or for an API run its arguments to the Dataform
// API.
func RunCommand(deps Deps, api bool, o project.RunOptions) string {
	switch {
	case api:
		return "Dataform API " + strings.Join(dataform.RunArgs(o), " ")
	case deps.RunCommand != nil:
		return "$ " + deps.RunCommand(o)
	}
	return "$ " + cmdrun.ShellJoin(append([]string{"dataform"}, dataform.RunArgs(o)...))
}

// RunOptionsFor are the run options of ids in g, with their RunName for a
// tool that selects by name.
func RunOptionsFor(g *project.Graph, ids []project.ActionID, deps, dependents, fullRefresh bool) project.RunOptions {
	o := project.RunOptions{
		Actions:           ids,
		IncludeDeps:       deps,
		IncludeDependents: dependents,
		FullRefresh:       fullRefresh,
	}
	if g == nil {
		return o
	}
	for _, id := range ids {
		if a := g.Actions[id]; a != nil && a.RunName != "" {
			o.Names = append(o.Names, a.RunName)
		}
	}
	return o
}

// APIRunOptions is what a front end knows of an API run of targets in the
// project g compiles: main adds the repository and the checkout's state
// (see Deps.StartAPIRun).
func APIRunOptions(g *project.Graph, deps, dependents, fullRefresh bool, targets ...project.Target) dataformapi.Options {
	included := make([]dataformapi.Target, len(targets))
	for i, t := range targets {
		included[i] = dataformapi.Target{Database: t.Database, Schema: t.Schema, Name: t.Name}
	}
	return dataformapi.Options{
		Repo:        dataformapi.Repository{Project: g.DefaultDatabase, Location: g.DefaultLocation},
		JobLocation: g.DefaultLocation,
		Invocation: dataformapi.InvocationConfig{
			IncludedTargets:                      included,
			TransitiveDependenciesIncluded:       deps,
			TransitiveDependentsIncluded:         dependents,
			FullyRefreshIncrementalTablesEnabled: fullRefresh,
		},
	}
}

// JobRow is an action of a run as the log pane's jobs table shows it.
type JobRow struct {
	Target   string `json:"target"`
	State    string `json:"state"`
	Raw      string `json:"raw"` // the state as the tool names it
	Duration string `json:"duration,omitempty"`
	Slot     string `json:"slot,omitempty"`
	Billed   string `json:"billed,omitempty"`
	Cost     string `json:"cost,omitempty"`
	Failure  string `json:"failure,omitempty"`
	JobURL   string `json:"jobUrl,omitempty"`
	// What the page sorts by: the numbers the cells are written from, as
	// JobSortNumbers has them.
	DurationMs int64 `json:"durationMs,omitempty"`
	// HasDuration and HasJob say the numbers are known: 0 B billed is a
	// real figure, to sort first when smallest comes first, and not a job
	// that has not been read.
	HasDuration bool    `json:"hasDuration,omitempty"`
	HasJob      bool    `json:"hasJob,omitempty"`
	SlotMs      int64   `json:"slotMs,omitempty"`
	Bytes       int64   `json:"bytes,omitempty"`
	CostUSD     float64 `json:"costUsd,omitempty"`
}

// JobsSummary is the line over the jobs table: how many actions are in
// each state, and how long the run has taken so far.
type JobsSummary struct {
	Actions   int `json:"actions"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"` // or cancelled
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Skipped   int `json:"skipped"` // or disabled
	// Reading is how many finished actions' jobs have not been read yet.
	Reading int    `json:"reading,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
}

// SummarizeJobs is r's summary; now tells how long a run that has not
// ended has taken so far, and zero leaves that out.
func SummarizeJobs(r *project.RunReport, now time.Time) JobsSummary {
	s := JobsSummary{Actions: len(r.Actions)}
	for _, a := range r.Actions {
		if a.JobID != "" && a.Job == nil && a.JobErr == "" && (a.State == "SUCCEEDED" || a.State == "FAILED") {
			s.Reading++
		}
		switch a.State {
		case "SUCCEEDED":
			s.Succeeded++
		case "FAILED", "CANCELLED":
			s.Failed++
		case "RUNNING":
			s.Running++
		case "SKIPPED", "DISABLED":
			s.Skipped++
		default:
			s.Pending++
		}
	}
	if d, ok := r.Duration(); ok {
		s.Elapsed = FormatDuration(d)
	} else if !r.Started.IsZero() && !now.IsZero() {
		s.Elapsed = FormatDuration(now.Sub(r.Started))
	}
	return s
}

// JobsTable is r's actions as the jobs table shows them, and the totals
// row under them; now tells how long a running job has taken so far.
func JobsTable(r *project.RunReport, now time.Time) (rows []JobRow, total JobRow) {
	for _, a := range r.Actions {
		c := JobCellsOf(a, now)
		row := JobRow{
			Target: strings.TrimPrefix(a.Target.Schema+"."+a.Target.Name, "."), State: ActionState(a.State), Raw: a.State,
			Duration: c.Duration, Slot: c.Slot, Billed: c.Billed, Cost: c.Cost,
			Failure: strings.Join(strings.Fields(a.FailureReason), " "), JobURL: a.JobURL,
		}
		row.DurationMs, row.SlotMs, row.Bytes, row.CostUSD = JobSortNumbers(a)
		_, row.HasDuration = a.Duration()
		row.HasJob = a.Job != nil
		rows = append(rows, row)
	}
	t := JobTotalsOf(r)
	total = JobRow{Target: t.Label, Duration: t.Duration, Slot: t.Slot, Billed: t.Billed, Cost: t.Cost}
	return rows, total
}

// JobCells are the figures of an action's row in the jobs table, "" for
// those not known (yet).
type JobCells struct {
	// Label is the totals row's "Total (2 BigQuery jobs)"; "" for an action.
	Label                        string
	Duration, Slot, Billed, Cost string
}

// JobCellsOf is a's figures; now tells how long a running job has taken so
// far, and zero leaves that out.
func JobCellsOf(a project.RunAction, now time.Time) JobCells {
	var c JobCells
	if d, ok := a.Duration(); ok {
		c.Duration = FormatDuration(d)
	} else if a.State == "RUNNING" && a.JobID != "" && !a.Started.IsZero() && !now.IsZero() {
		c.Duration = FormatDuration(now.Sub(a.Started))
	}
	switch {
	case a.Job != nil:
		c.Slot = FormatDuration(time.Duration(a.Job.SlotMs) * time.Millisecond)
		c.Billed = FormatBytes(a.Job.BytesBilled)
		c.Cost = FormatCost(a.Job.CostUSD)
	case a.JobErr != "":
		c.Billed = "n/a"
	}
	return c
}

// JobTotalsOf is the row under the jobs table: the run's own duration,
// since actions run in parallel, and the sums of the jobs looked up so far.
func JobTotalsOf(r *project.RunReport) JobCells {
	t := r.Totals()
	c := JobCells{Label: "Total"}
	if t.Jobs > 0 {
		c.Label = fmt.Sprintf("Total (%d BigQuery job%s)", t.Jobs, Plural(t.Jobs))
	}
	if d, ok := r.Duration(); ok {
		c.Duration = FormatDuration(d)
	}
	if t.Jobs > 0 {
		c.Slot = FormatDuration(time.Duration(t.SlotMs) * time.Millisecond)
		c.Billed = FormatBytes(t.BytesBilled)
		c.Cost = FormatCost(t.CostUSD)
	}
	return c
}

// HistoryRow is a kept run as the history tab lists it.
type HistoryRow struct {
	ID       string `json:"id"`
	When     string `json:"when"`
	Via      string `json:"via"`
	Jobs     int    `json:"jobs"`
	State    string `json:"state"`
	Raw      string `json:"raw"`
	Duration string `json:"duration,omitempty"`
	Billed   string `json:"billed,omitempty"`
	Cost     string `json:"cost,omitempty"`
	Command  string `json:"command,omitempty"`
	URL      string `json:"url,omitempty"`
	// Title is what the log pane's title says of the run once opened.
	Title string `json:"title"`
	// Deferred are the upstream tables the run read from prod, "dev →
	// prod" (defer to prod).
	Deferred []string `json:"deferred,omitempty"`
}

// HistoryRows are kept runs as the history tab lists them, now telling
// today's from the others.
func HistoryRows(entries []runhistory.Entry, now time.Time) []HistoryRow {
	out := make([]HistoryRow, len(entries))
	for i, e := range entries {
		r := HistoryRow{ID: e.ID, When: When(now, e.Started), Via: e.Via, Jobs: e.Jobs(), State: ActionState(e.State),
			Raw: e.State, Command: e.Cmdline, Title: KeptTitle(now, &e), Deferred: e.Deferred}
		if d, ok := e.Duration(); ok {
			r.Duration = FormatDuration(d)
		}
		if e.Report != nil {
			r.URL = e.Report.URL
			if t := e.Report.Totals(); t.Jobs > 0 {
				r.Billed, r.Cost = FormatBytes(t.BytesBilled), FormatCost(t.CostUSD)
			}
		}
		out[i] = r
	}
	return out
}

// KeptTitle says which kept run a table of jobs is of: "cli · 6 Oct 17:40 ·
// ✓ succeeded".
func KeptTitle(now time.Time, e *runhistory.Entry) string {
	return e.Via + " · " + When(now, e.Started) + " · " + ActionState(e.State)
}

// When is t short enough for a column: the time of day today (as of now),
// with the day before that.
func When(now, t time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "today " + t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

// FlattenFields lists a schema's columns by name, each RECORD followed by
// its fields by their path, as the columns view does.
func FlattenFields(fields []warehouse.Column) []warehouse.Column {
	return flattenFields(nil, "", fields)
}

// LandingInfo is what xf web's settings page shows outside any project:
// this xf, the tools on PATH and the Google accounts.
type LandingInfo struct {
	Exe, Build string
	Tools      []LandingTool
	// ADC and Gcloud look up the Google accounts; nil leaves them out.
	ADC    func(ctx context.Context) gcpauth.ADC
	Gcloud func(ctx context.Context) gcpauth.CLI
}

// LandingTool is a tool xf can call, as PATH finds it.
type LandingTool struct {
	Name string
	// Bin is its path, "" when PATH has none, and Note what to know of it.
	Bin, Note string
	// Version asks Bin for its version; nil leaves it out.
	Version func(ctx context.Context) (string, error)
}

// LandingInfoRows are info's rows, with the tools' versions and the
// Google accounts asked for now, each within the , overlay's timeout.
func LandingInfoRows(ctx context.Context, info LandingInfo) []InfoRow {
	var out []InfoRow
	LandingInfoStream(ctx, info, func(rows []InfoRow) { out = rows })
	return out
}

// landingRead is what LandingInfoStream has read so far: each tool's
// version, once done, and the Google accounts.
type landingRead struct {
	versions         []string
	versionDone      []bool
	adc              gcpauth.ADC
	cli              gcpauth.CLI
	adcDone, cliDone bool
}

// LandingInfoStream gives emit the rows LandingInfoRows answers with as
// they arrive: at once, with "…" for each lookup still going, and again
// as each lands. It returns after the last; emit is never called twice
// at once.
func LandingInfoStream(ctx context.Context, info LandingInfo, emit func([]InfoRow)) {
	o := landingRead{versions: make([]string, len(info.Tools)), versionDone: make([]bool, len(info.Tools))}
	var mu sync.Mutex
	mu.Lock()
	emit(landingRows(info, o))
	mu.Unlock()
	var wg sync.WaitGroup
	// ask runs a lookup, then under mu sets what it read and sends.
	ask := func(read func(ctx context.Context) func(o *landingRead)) {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, InfoTimeout)
			defer cancel()
			set := read(ctx)
			mu.Lock()
			defer mu.Unlock()
			set(&o)
			emit(landingRows(info, o))
		})
	}
	for i, t := range info.Tools {
		if t.Bin == "" || t.Version == nil {
			continue
		}
		ask(func(ctx context.Context) func(*landingRead) {
			v, err := t.Version(ctx)
			if err != nil {
				v = "(unknown) " + FirstLine(err.Error())
			}
			return func(o *landingRead) { o.versions[i], o.versionDone[i] = v, true }
		})
	}
	if info.ADC != nil {
		ask(func(ctx context.Context) func(*landingRead) {
			a := info.ADC(ctx)
			return func(o *landingRead) { o.adc, o.adcDone = a, true }
		})
	}
	if info.Gcloud != nil {
		ask(func(ctx context.Context) func(*landingRead) {
			c := info.Gcloud(ctx)
			return func(o *landingRead) { o.cli, o.cliDone = c, true }
		})
	}
	wg.Wait()
}

// landingRows are info's rows with what o has read: "…" for a lookup
// still going.
func landingRows(info LandingInfo, o landingRead) []InfoRow {
	out := []InfoRow{{Title: "binaries"}, {Label: "xf", Value: info.Exe}, {Value: info.Build}}
	for i, t := range info.Tools {
		if t.Bin == "" {
			out = append(out, InfoRow{Label: t.Name, Value: "(not on PATH)", Note: t.Note})
			continue
		}
		out = append(out, InfoRow{Label: t.Name, Value: t.Bin, Note: t.Note})
		switch {
		case o.versions[i] != "":
			out = append(out, InfoRow{Value: o.versions[i]})
		case t.Version != nil && !o.versionDone[i]:
			out = append(out, InfoRow{Value: "…"})
		}
	}
	if info.ADC == nil && info.Gcloud == nil {
		return out
	}
	out = append(out, InfoRow{Title: "google cloud"})
	var cloud [][3]string
	switch {
	case info.ADC == nil:
	case o.adcDone:
		cloud = o.adc.Rows(false)
	default:
		cloud = [][3]string{{"xf uses", "…", ""}}
	}
	switch {
	case info.Gcloud == nil:
	case o.cliDone:
		cloud = append(cloud, o.cli.Rows(o.adc)...)
	default:
		cloud = append(cloud, [3]string{"gcloud", "…", ""})
	}
	for _, r := range cloud {
		if r[1] != "" || r[2] != "" {
			out = append(out, InfoRow{Label: r[0], Value: r[1], Note: r[2], Warn: gcpauth.Warn(r)})
		}
	}
	return out
}

// CostView is a query cost report as the C overlay lays it out.
type CostView struct {
	// Compared is "branch vs main @ abc1234", Compiled what the dry runs
	// were compiled with, DryRunIn the project and location they ran in,
	// FromDev how many tables the branch adds were read from dev, and
	// Unset the warning that the defaults were.
	Compared string `json:"compared,omitempty"`
	// Explain says in a sentence what was dry run, as HTML; "" when
	// nothing was.
	Explain  string `json:"explain,omitempty"`
	Compiled string `json:"compiled,omitempty"`
	DryRunIn string `json:"dryRunIn,omitempty"`
	FromDev  string `json:"fromDev,omitempty"`
	Unset    string `json:"unset,omitempty"`
	Total    string `json:"total"`
	// Later is the total of each run after the first, when an incremental
	// table's first build is in Total.
	Later string `json:"later,omitempty"`
	// Rows are the estimated actions, most bytes first, laid out as the
	// text report lays them out (cost.Table): TotalRow is their total,
	// Dataset the one dataset they are all in, BaseLabel and HeadLabel
	// name the merge-base's and branch's columns, and Footnotes are the
	// notes too long for a row. Empty says why there are no rows.
	Rows      []CostRow     `json:"rows"`
	TotalRow  cost.TableRow `json:"totalRow"`
	Dataset   string        `json:"dataset,omitempty"`
	BaseLabel string        `json:"baseLabel,omitempty"`
	HeadLabel string        `json:"headLabel"`
	Footnotes []string      `json:"footnotes,omitempty"`
	Empty     string        `json:"empty,omitempty"`
	// BaseError is why no change is shown.
	BaseError string `json:"baseError,omitempty"`
	// NotEstimated are the actions that couldn't be estimated.
	NotEstimated *CostSkipped `json:"notEstimated,omitempty"`
	Footnote     string       `json:"footnote"`
	Markdown     string       `json:"markdown"`
	// Past is a Databricks report's past runs, in place of Rows.
	Past *CostPast `json:"past,omitempty"`
}

// CostRow is an estimated action of a CostView: its row of the table,
// with the notes short enough to go on it.
type CostRow struct {
	cost.TableRow
	Notes string `json:"notes,omitempty"`
	// Warn says a query reads a partitioned table without its filter.
	Warn bool `json:"warn,omitempty"`
}

// CostPast is a report of past runs (Databricks, ADR 0042) as the cost tab
// shows it, in place of the estimates' rows.
type CostPast struct {
	// Line says what the runs are; Why why none was found.
	Line       string        `json:"line"`
	Why        string        `json:"why,omitempty"`
	NoReadRows bool          `json:"noReadRows,omitempty"`
	Rows       []CostPastRow `json:"rows"`
}

// CostPastRow is a changed model and its last run; Note says why it has
// none.
type CostPastRow struct {
	Action    string `json:"action"`
	Type      string `json:"type"`
	When      string `json:"when,omitempty"`
	Took      string `json:"took,omitempty"`
	Read      string `json:"read,omitempty"`
	ReadRows  string `json:"readRows,omitempty"`
	Warehouse string `json:"warehouse,omitempty"`
	Note      string `json:"note,omitempty"`
}

func pastView(r *cost.Report) *CostPast {
	p := r.PastRuns
	v := &CostPast{Line: cost.PastLine(r, func(s string) string { return s }) + ".", Why: strings.ReplaceAll(p.Why, "`", ""), NoReadRows: p.NoReadRows, Rows: []CostPastRow{}}
	for _, run := range p.Runs {
		row := CostPastRow{Action: impact.ShortTable(run.Name), Type: run.Type}
		if run.Ran {
			row.When, row.Took, row.Read, row.Warehouse = run.When(), run.Took(), warehouse.FormatBytes(run.ReadBytes), run.Warehouse
			if !p.NoReadRows {
				row.ReadRows = cost.RowsText(run.ReadRows)
			}
		} else {
			row.Note = run.NoRunText(p)
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}

// CostReport lays r out as the C overlay and the cost tab do.
func CostReport(r *cost.Report) CostView {
	// Rows is never nil, so a report whose every action failed to estimate
	// sends [], not null, which xf web's cost tab could not draw.
	v := CostView{Compiled: r.Compiled, DryRunIn: r.DryRunIn, FromDev: cost.FromDevLine(r), Total: cost.TotalLine(r, cost.Options{}),
		Footnote: CostTableFootnote, Markdown: cost.Markdown(r, cost.Options{}), Rows: []CostRow{},
		Explain: impact.InlineHTML(cost.Explanation(r))}
	if c := r.Comparison; c != nil {
		v.Compared = c.HeadRef + " vs " + c.BaseRef + " @ " + c.MergeBase[:min(len(c.MergeBase), 7)]
	}
	if hint := cost.UnsetHint(r); hint != "" {
		v.Unset = "⚠ " + strings.ReplaceAll(hint, "`", "")
	}
	var estimated []cost.Action
	for _, a := range r.Actions {
		if a.Estimate != nil {
			estimated = append(estimated, a)
		}
	}
	t := cost.Layout(r, estimated, cost.Options{})
	v.TotalRow, v.Dataset, v.BaseLabel, v.HeadLabel, v.Footnotes = t.Total, t.Dataset, t.BaseLabel, t.HeadLabel, t.Footnotes
	for _, tr := range t.Rows {
		a := tr.Action
		var notes []string
		for _, u := range a.Unfiltered {
			notes = append(notes, "⚠ no partition filter: "+u.Column+" of "+impact.ShortTable(u.Table))
		}
		if tr.FullRefresh != "" {
			notes = append(notes, "full refresh "+tr.FullRefresh)
		}
		for _, d := range a.FromDev {
			note := "reads dev's " + impact.ShortTable(d.Dev)
			if built := RelTime(r.CheckedAt, d.BuiltAt); built != "" {
				note += " (built " + built + ")"
			}
			notes = append(notes, note)
		}
		v.Rows = append(v.Rows, CostRow{TableRow: tr, Notes: strings.Join(notes, " · "), Warn: len(a.Unfiltered) > 0})
	}
	v.Later = cost.LaterLine(r, cost.Options{})
	v.NotEstimated = costSkipped(r)
	if p := r.PastRuns; p != nil {
		v.Past, v.Footnote = pastView(r), PastFootnote
		if len(p.Runs) == 0 {
			v.Empty = "Nothing to show: the branch changes no model."
		}
		return v
	}
	if len(r.Actions) == 0 {
		v.Empty = "Nothing to estimate: the branch changes no action with a query to dry run."
	}
	if r.BaseError != "" {
		v.BaseError = "No change is shown: the merge-base could not be compiled. " + strings.ReplaceAll(r.BaseError, "\n", " ")
	}
	return v
}

// UnsetImpactHint is the I report's warning that it compared with the
// default targets, "" when it didn't.
func UnsetImpactHint(r *impact.Report) string {
	if hint := impact.UnsetHint(r); hint != "" {
		return "⚠ " + strings.ReplaceAll(hint, "`", "")
	}
	return ""
}

// JobSortNumbers are what a's number columns sort by: how long it took in
// ms, its slot ms, the bytes it was billed and their cost. Each is zero
// while unknown: a running action's duration is still growing, so it sorts
// with the actions that have none yet.
func JobSortNumbers(a project.RunAction) (durationMs, slotMs, bytes int64, costUSD float64) {
	if d, ok := a.Duration(); ok {
		durationMs = d.Milliseconds()
	}
	if a.Job != nil {
		slotMs, bytes, costUSD = a.Job.SlotMs, a.Job.BytesBilled, a.Job.CostUSD
	}
	return
}
