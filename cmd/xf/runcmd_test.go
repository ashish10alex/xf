package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

func TestParseRunArgsSplitsXfsFlagsFromTheTools(t *testing.T) {
	r, err := parseRunArgs([]string{"proj", "--actions", "a", "b", "--defer", "--include-deps", "--dry-run", "--json", "--defer-allow-stale=true", "--backend=dataform", "--", "--defer"})
	if err != nil {
		t.Fatal(err)
	}
	if r.dir != "proj" || r.backend != "dataform" || !r.deferOn || !r.dryRun || !r.json || !r.allowStale || r.buildChanged {
		t.Errorf("xf's flags: %+v", r)
	}
	want := []string{"--actions", "a", "b", "--include-deps", "--json", "--", "--defer"}
	if !slices.Equal(r.tool, want) {
		t.Errorf("the tool's: %q, want %q", r.tool, want)
	}
	if _, err := parseRunArgs([]string{"proj", "--dir", "other"}); err == nil {
		t.Error("the dir given twice")
	}
	if _, err := parseRunArgs([]string{"--defer=maybe"}); err == nil {
		t.Error("a bool flag's value")
	}
	if r, _ := parseRunArgs([]string{"--help"}); !r.help {
		t.Error("--help")
	}
}

func TestReadToolArgsDataform(t *testing.T) {
	ta, err := readToolArgs(backend.Dataform, []string{"--actions", "a", "p.d.b", "--tags=t1", "t2", "--include-deps", "--no-include-dependents", "--timeout", "10m", "--schema-suffix=x", "--vars", "k=v", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ta.actions, []string{"a", "p.d.b"}) || !slices.Equal(ta.tags, []string{"t1", "t2"}) || !ta.deps || ta.dependents || !ta.selects {
		t.Errorf("selection: %+v", ta)
	}
	if !slices.Equal(ta.compile, []string{"--schema-suffix", "--vars"}) {
		t.Errorf("compile flags: %q", ta.compile)
	}
	if ta, _ := readToolArgs(backend.Dataform, []string{"--full-refresh"}); ta.selects {
		t.Error("no selection: the whole project")
	}
	for _, bad := range [][]string{{"--actions"}, {"--timeout"}, {"stray"}} {
		if _, err := readToolArgs(backend.Dataform, bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestReadToolArgsDbt(t *testing.T) {
	ta, err := readToolArgs(backend.DBT, []string{"-s", "fct_orders+", "tag:x", "--exclude", "y", "--threads", "4", "--full-refresh", "-t", "prod", "--selector", "nightly", "--indirect-selection=cautious"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-s", "fct_orders+", "tag:x", "--exclude", "y", "--selector", "nightly", "--indirect-selection", "cautious"}
	if !slices.Equal(ta.selection, want) || !ta.selects {
		t.Errorf("selection %q, want %q", ta.selection, want)
	}
	if !slices.Equal(ta.compile, []string{"-t"}) {
		t.Errorf("compile flags: %q", ta.compile)
	}
}

func TestDataformMatchAsTheCLI(t *testing.T) {
	g := project.NewGraph([]*project.Action{
		{ID: "p.a.orders", Target: project.Target{Database: "p", Schema: "a", Name: "orders"}, Kind: project.KindTable},
		{ID: "p.b.orders", Target: project.Target{Database: "p", Schema: "b", Name: "orders"}, Kind: project.KindTable},
		{ID: "p.a.stg_x", Target: project.Target{Database: "p", Schema: "a", Name: "stg_x"}, Kind: project.KindView},
		{ID: "p.a.stg_y", Target: project.Target{Database: "p", Schema: "a", Name: "stg_y"}, Kind: project.KindView},
	})
	ids, err := dataformMatch(g, []string{"p.b.orders", "stg_x", "missing"})
	if err != nil || !slices.Equal(ids, []project.ActionID{"p.b.orders", "p.a.stg_x"}) {
		t.Errorf("%v %v", ids, err)
	}
	if _, err := dataformMatch(g, []string{"orders"}); err == nil || !strings.Contains(err.Error(), "2 actions") {
		t.Errorf("a name two actions have: %v", err)
	}
	if ids, _ := dataformMatch(g, []string{"stg_*"}); len(ids) != 2 {
		t.Errorf("a wildcard: %v", ids)
	}
}

func TestRefusalNamesTheFlags(t *testing.T) {
	r := refusalOf(frontend.DeferCheck{Stop: frontend.DeferStopStale, Detail: []string{"d.s.a reads p.s.a"}}, backend.Dataform)
	if r == nil || r.Reason != "stale" || !strings.Contains(strings.Join(r.Flags, ";"), "--defer-build-changed") || !strings.Contains(r.Title, "changes on this branch") {
		t.Errorf("stale: %+v", r)
	}
	r = refusalOf(frontend.DeferCheck{Stop: frontend.DeferStopLeftover, Detail: []string{"d.s.a", "d.s.b"}}, backend.Dataform)
	if r == nil || !strings.Contains(r.Title, "2 upstream proxy views") || !strings.Contains(strings.Join(r.Flags, ";"), "--allow-proxy-views") {
		t.Errorf("leftover: %+v", r)
	}
	if refusalOf(frontend.DeferCheck{}, backend.Dataform) != nil {
		t.Error("nothing to refuse")
	}
}

func TestPlanText(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	built := now.Add(-2 * time.Hour)
	text := planText(dryRunPlan{
		Tool: "dataform", Defer: true, Command: "dataform run --actions p.a.x",
		Builds: []plannedRun{
			{ID: "p.a.x", Type: "table", DryRuns: []plannedDryRun{{Bytes: 1 << 30, Estimate: "1.0 GB $0.006"}}},
			{ID: "p.a.y", Type: "view", DryRuns: []plannedDryRun{{BuiltByRun: "p.a.x"}}},
			{ID: "p.a.z", Type: "table", DryRuns: []plannedDryRun{{Error: "Syntax error\nmore"}}},
		},
		Prod: []plannedRead{{Dev: "d.a.s", Prod: "p.a.s", Stale: true}},
		Dev:  []plannedDev{{Table: "d.a.t", BuiltAt: &built}},
		TotalBytes: 1 << 30, Failed: 1,
	}, now)
	for _, want := range []string{
		"with defer to prod", "a run would start: dataform run --actions p.a.x", "builds 3 actions",
		"✓ p.a.x", "↳ p.a.y  reads p.a.x, which this run builds first", "✗ p.a.z  Syntax error\n",
		"reads 1 upstream table from prod", "d.a.s → p.a.s  ⚠ changed on this branch",
		"d.a.t · built 2h ago", "1 dry run failed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}
