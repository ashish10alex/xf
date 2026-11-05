package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeRunEngine defers p.s.raw always, and p.s.stg (changed on the
// branch) unless the run builds it; leftover Proxy Views when asked.
type fakeRunEngine struct {
	mu      sync.Mutex
	proxies []project.Target
	ons     []bool
	sqls    []string
}

func (f *fakeRunEngine) Deferral(_ context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	on := req.On != nil && *req.On
	f.ons = append(f.ons, on)
	if !on {
		return &engine.DeferralResult{Proxies: f.proxies}, nil
	}
	prodRaw, prodStg := tgt("prod", "s", "raw"), tgt("prod", "s", "stg")
	d := &deferral.Deferral{Entries: []deferral.Entry{{Dev: tgt("p", "s", "raw"), Prod: &prodRaw, Status: deferral.Deferred}}}
	if !slices.Contains(req.Actions, "p.s.stg") {
		d.Entries = append(d.Entries, deferral.Entry{Dev: tgt("p", "s", "stg"), Prod: &prodStg, Status: deferral.Deferred, Stale: true})
	}
	return &engine.DeferralResult{Deferral: d, StaleKnown: true}, nil
}

func (f *fakeRunEngine) DryRun(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
	f.mu.Lock()
	f.sqls = append(f.sqls, sql)
	f.mu.Unlock()
	if strings.Contains(sql, "`p.s.fct`") {
		return nil, errors.New("Not found: Table p:s.fct was not found in location EU")
	}
	return &bigquery.DryRun{Bytes: 100, Accuracy: bigquery.AccuracyPrecise}, nil
}

func (f *fakeRunEngine) ChangedActions(context.Context, changes.Request) (*changes.Result, error) {
	return &changes.Result{}, nil
}

func tgt(db, schema, name string) project.Target {
	return project.Target{Database: db, Schema: schema, Name: name}
}

func runGraph() *project.Graph {
	table := func(name, sql string, deps ...string) *project.Action {
		a := &project.Action{ID: project.ActionID("p.s." + name), Target: tgt("p", "s", name), Kind: project.KindTable, Query: sql}
		for _, d := range deps {
			a.DependencyTargets = append(a.DependencyTargets, tgt("p", "s", d))
		}
		return a
	}
	return project.NewGraph([]*project.Action{
		table("raw", "select 1"),
		table("stg", "select 2"),
		table("fct", "select * from `p.s.raw` join `p.s.stg` using (id)", "raw", "stg"),
		table("agg", "select count(*) from `p.s.fct`", "fct"),
	})
}

func newTestRun(t *testing.T, f *fakeRunEngine, args ...string) (*runCtx, *bytes.Buffer) {
	t.Helper()
	ra, err := parseRunArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	ta, err := readToolArgs(backend.Dataform, ra.tool)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	return &runCtx{ra: ra, ta: ta, tool: backend.Dataform, b: dataform.Backend{}, engine: f, g: runGraph(), out: &out,
		writer: func(context.Context) (deferral.Writer, error) { return nil, errors.New("no BigQuery in tests") }}, &out
}

func checkAndDryRun(t *testing.T, rc *runCtx) (*runPlan, int) {
	t.Helper()
	ctx := context.Background()
	plan, err := rc.check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, err := rc.dryRun(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan, code
}

func TestXfRunDeferRefusesStaleReads(t *testing.T) {
	f := &fakeRunEngine{}
	rc, out := newTestRun(t, f, "--actions", "fct", "--include-dependents", "--defer", "--dry-run")
	plan, code := checkAndDryRun(t, rc)
	if code != runNeedsDecision || plan.refusal == nil || plan.refusal.Reason != "stale" {
		t.Fatalf("code %d, refusal %+v", code, plan.refusal)
	}
	if !slices.Equal(plan.runSet, []project.ActionID{"p.s.agg", "p.s.fct"}) {
		t.Errorf("run set %v", plan.runSet)
	}
	if !slices.Contains(f.sqls, "select * from `prod.s.raw` join `prod.s.stg` using (id)") {
		t.Errorf("fct's dry run reads prod: %q", f.sqls)
	}
	text := out.String()
	for _, want := range []string{"↳ p.s.agg", "which this run builds first", "p.s.stg → prod.s.stg  ⚠ changed on this branch", "a run would start: dataform run --actions fct --include-dependents"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if slices.Contains(f.ons, false) {
		t.Errorf("--defer asks with defer on: %v", f.ons)
	}
}

func TestXfRunDeferBuildChangedAddsThemToTheRun(t *testing.T) {
	f := &fakeRunEngine{}
	rc, out := newTestRun(t, f, "--actions", "fct", "--defer", "--defer-build-changed", "--dry-run", "--json")
	plan, code := checkAndDryRun(t, rc)
	if code != runOK || plan.refusal != nil {
		t.Fatalf("code %d, refusal %+v\n%s", code, plan.refusal, out)
	}
	if !slices.Equal(plan.added, []project.ActionID{"p.s.stg"}) || !slices.Equal(plan.args, []string{"--actions", "fct", "--json", "--actions", "p.s.stg"}) {
		t.Errorf("added %v, args %q", plan.added, plan.args)
	}
	if !slices.Equal(plan.runSet, []project.ActionID{"p.s.fct", "p.s.stg"}) {
		t.Errorf("run set %v", plan.runSet)
	}
	if !strings.Contains(out.String(), `"readsFromProd": [`) || !strings.Contains(out.String(), `"added": [`) {
		t.Errorf("JSON plan:\n%s", out)
	}
	if !slices.Contains(f.sqls, "select * from `prod.s.raw` join `p.s.stg` using (id)") {
		t.Errorf("stg is built by the run, so read from dev: %q", f.sqls)
	}
}

func TestXfRunWithoutDeferRefusesLeftoverProxyViews(t *testing.T) {
	f := &fakeRunEngine{proxies: []project.Target{tgt("p", "s", "raw")}}
	rc, _ := newTestRun(t, f, "--actions", "fct", "--dry-run")
	plan, code := checkAndDryRun(t, rc)
	if code != runNeedsDecision || plan.refusal == nil || plan.refusal.Reason != "proxyViews" {
		t.Fatalf("code %d, refusal %+v", code, plan.refusal)
	}
	if slices.Contains(f.ons, true) {
		t.Errorf("without --defer, defer is off: %v", f.ons)
	}
	if !slices.Contains(f.sqls, "select * from `p.s.raw` join `p.s.stg` using (id)") {
		t.Errorf("without --defer, the SQL is as compiled: %q", f.sqls)
	}

	rc, _ = newTestRun(t, f, "--actions", "fct", "--dry-run", "--allow-proxy-views")
	if plan, code := checkAndDryRun(t, rc); code != runOK || plan.refusal != nil {
		t.Errorf("allowed: code %d, refusal %+v", code, plan.refusal)
	}
}
