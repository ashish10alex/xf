package web

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// Defer to prod for runs (ADR 0031). Dataform runs its own compiled SQL,
// so before a deferred run xf makes a Proxy View at each upstream Dev
// Target that isn't built, reading its Prod Target. First the run is
// checked, as the extension checks it, and the page asks what to do when
// it would fail or read what it shouldn't: the run request comes back
// with the answers in DeferOK.

// deferRunTimeout bounds checking a run: it may wait for Changed Actions.
const deferRunTimeout = 3 * time.Minute

// deferAsk is a question the page asks before the run starts.
type deferAsk struct {
	Title   string        `json:"title"`
	Detail  []string      `json:"detail,omitempty"`
	Note    string        `json:"note,omitempty"`
	Choices []deferChoice `json:"choices"`
}

// deferChoice is an answer to a deferAsk: its ID goes in DeferOK.
type deferChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// The answers.
const (
	// deferDeps runs with dependencies instead, building them in dev.
	deferDeps = "deps"
	// deferChanged adds the Stale Deferrals to the run, built in dev
	// before what reads them.
	deferChanged = frontend.DeferAnswerChanged
	// deferStale runs with Stale Deferrals all the same.
	deferStale = frontend.DeferAnswerStale
	// deferLeftover runs with defer off though Proxy Views read prod.
	deferLeftover = frontend.DeferAnswerLeftover
	// deferAPI and deferAPIAlways let a deferred API run read prod as the
	// repository's service account, once or from now on.
	deferAPI       = frontend.DeferAnswerAPI
	deferAPIAlways = "apiAlways"
	// deferWithout runs without defer to prod when it can't be worked out.
	deferWithout = "without"
)

// runAnswer is /api/run's answer: okView, or a question first.
type runAnswer struct {
	okView
	Ask *deferAsk `json:"ask,omitempty"`
}

// deferPlan is what a checked run does: the Proxy Views to make, what to
// log and keep in the history.
type deferPlan struct {
	entries []deferral.Entry
	lines   []logLine
	pairs   []string
	// dbtState is where a dbt run's --defer-state reads prod's manifest.
	dbtState string
	// retry is the run to start instead: with dependencies, or with
	// functions or Stale Deferrals added to it.
	retry *runRequest
}

func depsChoice(req runRequest) []deferChoice {
	if req.Toggles.Deps {
		return nil
	}
	return []deferChoice{{deferDeps, "Run with dependencies"}}
}

// withActions is req run as the actions of runSet and extra, which keeps
// what it ran and adds to it: dependencies and dependents are in runSet.
func withActions(req runRequest, runSet []project.ActionID, extra ...project.ActionID) *runRequest {
	r := req
	r.Kind, r.Tags, r.Skip = "actions", nil, nil
	r.IDs = append(slices.Clone(runSet), extra...)
	r.Toggles.Deps, r.Toggles.Dependents = false, false
	return &r
}

// deferPlanFor checks the run of runSet that req asks for. It returns the
// plan to run with, or a question to ask first.
func (p *projectState) deferPlanFor(ctx context.Context, req runRequest, runSet []project.ActionID) (*deferPlan, *deferAsk) {
	p.mu.Lock()
	on := p.st.Defer
	dataform := p.tool == "dataform"
	opts := p.prodOptionsLocked()
	base := p.base
	approved := p.sess.DeferAPIApproved
	p.mu.Unlock()
	if p.deps.Deferral == nil || len(runSet) == 0 {
		return &deferPlan{}, nil
	}
	if !dataform {
		return p.dbtDeferPlan(ctx, on, runSet, opts, base)
	}
	ok := setOf(req.DeferOK)
	if ok[deferDeps] {
		r := req
		r.Toggles.Deps = true
		r.DeferOK = slices.DeleteFunc(slices.Clone(req.DeferOK), func(s string) bool { return s == deferDeps })
		return &deferPlan{retry: &r}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, deferRunTimeout)
	defer cancel()
	res, err := p.deps.Deferral(ctx, engine.DeferralRequest{Actions: runSet, ProdCompilerOptions: opts, DefaultBranch: base, Proxies: true, Fresh: true})
	if err != nil {
		if !on || ok[deferWithout] {
			return &deferPlan{}, nil // a failed look for leftovers doesn't stop a run
		}
		return nil, &deferAsk{Title: "Defer to prod can't be worked out for this run.", Detail: []string{err.Error()},
			Choices: []deferChoice{{deferWithout, "Run without it"}}}
	}
	answers := setOf(req.DeferOK)
	answers[frontend.DeferAnswerAPI] = ok[deferAPI] || ok[deferAPIAlways]
	check := frontend.CheckDeferredRun(frontend.DeferRun{
		On: on, Deferral: res.Deferral, Proxies: res.Proxies,
		MissingDatasets: func(entries []deferral.Entry) map[string]bool { return p.missingDevDatasets(ctx, entries) },
		Tags:            req.Kind == "tags", API: req.Toggles.API, APIApproved: approved, Answers: answers,
	})
	switch check.Stop {
	case frontend.DeferStopLeftover:
		n := len(check.Proxies)
		return nil, &deferAsk{
			Title:   fmt.Sprintf("Defer to prod is off, but %s from an earlier deferred run, so this run would read prod for %s.", plural(n, "upstream proxy view"), map[bool]string{true: "it", false: "them"}[n == 1]),
			Detail:  check.Detail,
			Note:    "Running with dependencies builds them in dev, replacing the proxy views. The palette's \"Defer to prod: remove proxy views\" removes them.",
			Choices: append(depsChoice(req), deferChoice{deferLeftover, "Run anyway"}),
		}
	case frontend.DeferStopBlocked:
		return nil, &deferAsk{
			Title:   fmt.Sprintf("Can't defer %s to prod, so this run would fail.", plural(len(check.Detail), "upstream table")),
			Detail:  check.Detail,
			Note:    "xf never makes a dev dataset. Running with dependencies builds these in dev.",
			Choices: depsChoice(req),
		}
	case frontend.DeferStopStale:
		var choices []deferChoice
		if req.Kind != "tags" && !req.Toggles.Deps {
			choices = append(choices, deferChoice{deferChanged, "Build changed upstream first"})
		}
		choices = append(append(choices, depsChoice(req)...), deferChoice{deferStale, "Run anyway"})
		return nil, &deferAsk{
			Title:   fmt.Sprintf("%s read from prod changed on this branch, so the run would use the prod version.", plural(len(check.Detail), "upstream table")),
			Detail:  check.Detail,
			Note:    "Build changed upstream first adds only these to the run, built in dev before the actions that read them.",
			Choices: choices,
		}
	case frontend.DeferStopAPI:
		return nil, &deferAsk{
			Title:   fmt.Sprintf("This run reads %s from prod through proxy views. It runs as the repository's service account (or Dataform's default service agent), which needs BigQuery Data Viewer on:", plural(res.Deferral.Deferred(), "upstream table")),
			Detail:  check.Detail,
			Choices: []deferChoice{{deferAPI, "Run"}, {deferAPIAlways, "Run and don't ask again"}},
		}
	}
	if len(check.Add) > 0 {
		r := withActions(req, runSet, check.Add...)
		r.DeferOK = req.DeferOK
		if !check.AddRoutines {
			r.DeferOK = append(slices.DeleteFunc(slices.Clone(req.DeferOK), func(s string) bool { return s == deferChanged }), deferStale)
		}
		return &deferPlan{retry: r}, nil
	}
	if !on || res.Deferral == nil {
		return &deferPlan{}, nil
	}
	if ok[deferAPIAlways] {
		p.mu.Lock()
		p.sess.DeferAPIApproved = true
		p.mu.Unlock()
	}
	d := res.Deferral
	plan := &deferPlan{entries: check.Entries, pairs: frontend.DeferredPairs(d)}
	if line := frontend.DeferRunLine(d); line != "" {
		plan.lines = append(plan.lines, logLine{Text: "defer to prod: " + line, Tone: "dim"})
		for _, pair := range plan.pairs {
			plan.lines = append(plan.lines, logLine{Text: "  " + pair, Tone: "dim"})
		}
	}
	return plan, nil
}

// dbtDeferPlan is a dbt run's: dbt defers itself, given prod's manifest.
func (p *projectState) dbtDeferPlan(ctx context.Context, on bool, runSet []project.ActionID, opts *string, base string) (*deferPlan, *deferAsk) {
	if !on {
		return &deferPlan{}, nil
	}
	res, err := p.deps.Deferral(ctx, engine.DeferralRequest{Actions: runSet, ProdCompilerOptions: opts, DefaultBranch: base})
	if err != nil || res.DbtState == "" {
		msg := "no manifest of prod"
		if err != nil {
			msg = err.Error()
		}
		return nil, &deferAsk{Title: "Defer to prod can't be applied to this run.", Detail: []string{msg}, Choices: []deferChoice{{deferWithout, "Run without it"}}}
	}
	plan := &deferPlan{dbtState: res.DbtState, pairs: frontend.DeferredPairs(res.Deferral)}
	plan.lines = append(plan.lines, logLine{Text: "defer to prod: dbt --defer --defer-state " + res.DbtState, Tone: "dim"})
	return plan, nil
}

// missingDevDatasets are the dev datasets of entries that don't exist.
func (p *projectState) missingDevDatasets(ctx context.Context, entries []deferral.Entry) map[string]bool {
	out := map[string]bool{}
	if p.deps.DeferWriter == nil || len(entries) == 0 {
		return out
	}
	w, err := p.deps.DeferWriter(ctx)
	if err != nil {
		return out
	}
	return deferral.MissingDevDatasets(ctx, w, entries)
}

// makeProxies makes the plan's Proxy Views before the run starts, saying
// in the log what it did.
func (p *projectState) makeProxies(ctx context.Context, plan *deferPlan) error {
	if len(plan.entries) == 0 {
		return nil
	}
	if p.deps.DeferWriter == nil {
		return fmt.Errorf("defer to prod can't make proxy views here")
	}
	w, err := p.deps.DeferWriter(ctx)
	if err != nil {
		return err
	}
	r, err := deferral.EnsureProxies(ctx, w, plan.entries)
	if err != nil {
		return fmt.Errorf("defer to prod: couldn't make the proxy views, so the run wasn't started: %w", err)
	}
	if s := r.String(); s != "" {
		p.mu.Lock()
		p.appendLocked(logLine{Text: "defer to prod: " + s, Tone: "dim"})
		p.mu.Unlock()
		p.flushLog(false)
	}
	return nil
}

// runDeferLocked is what the run preview says a run of actions reads
// from prod, from the Deferrals known: the run checks again when it
// starts.
func (p *projectState) runDeferLocked(g *project.Graph, actions []*project.Action, t runToggles) string {
	if !p.deferOnLocked() {
		return ""
	}
	ids := make([]project.ActionID, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	in := map[project.ActionID]bool{}
	for _, id := range deferral.RunSet(g, project.RunOptions{Actions: ids, IncludeDeps: t.Deps, IncludeDependents: t.Dependents}) {
		in[id] = true
	}
	merged := &deferral.Deferral{}
	seen := map[project.ActionID]bool{}
	for _, a := range actions {
		k := p.def.byAction[a.ID]
		if k == nil || k.d == nil {
			continue
		}
		for _, e := range k.d.Entries {
			if !in[e.Dev.ID()] && !seen[e.Dev.ID()] {
				seen[e.Dev.ID()] = true
				merged.Entries = append(merged.Entries, e)
			}
		}
	}
	return frontend.DeferRunLine(merged)
}

func targetIDs(ts []project.Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t.ID())
	}
	return out
}
