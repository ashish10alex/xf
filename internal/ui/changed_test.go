package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeChanges answers R with a result per head. A compile for the working
// tree also puts the result in the fake engine's state, as the real engine
// does, so the markers follow.
type fakeChanges struct {
	mu    sync.Mutex
	fe    *fakeEngine
	reqs  []changes.Request
	res   map[changes.Head]*changes.Result
	err   error
	block chan struct{} // when set, requests with Compile wait on it
}

func (f *fakeChanges) call(ctx context.Context, req changes.Request) (*changes.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	res, err, block := f.res[req.Head], f.err, f.block
	f.mu.Unlock()
	if !req.Compile {
		return nil, nil // nothing cached yet
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if req.Head == changes.HeadWorktree && f.fe != nil {
		f.fe.mu.Lock()
		f.fe.state.Changes = res
		f.fe.pending = append(f.fe.pending, f.fe.state)
		f.fe.mu.Unlock()
	}
	return res, nil
}

func (f *fakeChanges) requests() []changes.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]changes.Request(nil), f.reqs...)
}

func target(schema, name string) project.Target {
	return project.Target{Database: "demo-project", Schema: schema, Name: name}
}

func testChanges(head changes.Head) *changes.Result {
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abc1234def5678", DefaultBranch: "main", HeadRef: "feat/orders", Head: head,
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: "demo-project.analytics.daily_orders", Target: target("analytics", "daily_orders"), FileName: "definitions/daily_orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL, changes.ReasonConfig}},
				{ID: "demo-project.staging.stg_customers", Target: target("staging", "stg_customers"), FileName: "definitions/stg_customers.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonNew}},
			},
			Deleted: []changes.Deleted{{ID: "demo-project.analytics.legacy_orders", FileName: "definitions/legacy_orders.sqlx", Type: "view"}},
		},
	}
	if head == changes.HeadPushed {
		res.HeadRef, res.HeadSHA = "origin/feat/orders", "def5678aaaa"
	}
	return res
}

func newChangedTestModel(t *testing.T, fch *fakeChanges, fa *fakeAPIRunner, fixtures ...string) (Model, *fakeRunner) {
	t.Helper()
	if len(fixtures) == 0 {
		fixtures = []string{"basic.json"}
	}
	fr := &fakeRunner{}
	fc := &fakeCompiler{t: t, fixtures: fixtures}
	fe := newFakeEngine(fc.compile)
	fch.fe = fe
	if fch.res == nil {
		fch.res = map[changes.Head]*changes.Result{changes.HeadWorktree: testChanges(changes.HeadWorktree), changes.HeadPushed: testChanges(changes.HeadPushed)}
	}
	deps := Deps{
		Root:           "/work/demo",
		Engine:         fe,
		StartRun:       fr.start,
		ChangedActions: fch.call,
		DefaultBranch:  "main",
		Now:            testNow,
	}
	if fa != nil {
		deps.StartAPIRun = fa.start
	}
	m := New(deps)
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	return startOn(t, closePalette(t, m), "demo-project.analytics.weekly_revenue"), fr
}

func TestRunChangedListsThenRuns(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	// Startup names the branch without compiling anything.
	if reqs := fch.requests(); len(reqs) != 1 || reqs[0].Compile || reqs[0].DefaultBranch != "main" || reqs[0].Head != changes.HeadWorktree {
		t.Fatalf("startup requests = %+v", reqs)
	}

	m = drive(t, m, nil, press("R")...)
	if reqs := fch.requests(); len(reqs) != 2 || !reqs[1].Compile || reqs[1].Head != changes.HeadWorktree || reqs[1].DefaultBranch != "main" {
		t.Fatalf("R asked %+v", reqs)
	}
	view := screen(m)
	assertContains(t, view, "R run changed", "2 changed actions in feat/orders vs origin/main @ abc1234 (working tree)",
		"2 actions in 2 files: 1 table, 1 view · 1 new, 1 sql, 1 config · 1 removed",
		"definitions/daily_orders.sqlx", "● analytics.daily_orders", "table · sql, config",
		"definitions/stg_customers.sqlx", "● staging.stg_customers", "view · new",
		"removed on this branch, not run", "✗ analytics.legacy_orders",
		"[ ] d deps", "r/enter run 2 changed actions", "esc close")
	if len(fr.calls()) != 0 {
		t.Fatal("R ran before enter")
	}

	// The toggles work inside the overlay.
	m = drive(t, m, nil, press("d")...)
	assertContains(t, screen(m), "[x] d deps")
	m = drive(t, m, nil, press("enter")...)
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.analytics.daily_orders", "demo-project.staging.stg_customers"}, IncludeDeps: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	view = screen(m)
	assertNotContains(t, view, "R run changed")
	assertContains(t, view, "4 log · 2 changed actions",
		"2 changed actions in feat/orders vs origin/main @ abc1234 (working tree)",
		"$ dataform run --actions demo-project.analytics.daily_orders demo-project.staging.stg_customers --include-deps")
}

func TestRunChangedMarkers(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil)
	assertNotContains(t, screen(m), "changed")
	m = drive(t, m, nil, press("R", "esc")...)

	// The palette marks them and, with an empty query, lists them first.
	m = drive(t, m, nil, press("/")...)
	pv := paletteView(m)
	assertContains(t, pv, "● table", "● view")
	rows := strings.Split(pv, "\n")
	var order []string
	for _, r := range rows {
		for _, name := range []string{"weekly_revenue", "daily_orders ", "stg_customers", "stg_orders"} {
			if strings.Contains(r, name) {
				order = append(order, strings.TrimSpace(name))
			}
		}
	}
	if want := []string{"weekly_revenue", "daily_orders", "stg_customers", "stg_orders"}; !reflect.DeepEqual(order[:4], want) {
		t.Errorf("palette order = %v, want %v first", order, want)
	}
	if strings.Contains(rowWith(pv, "stg_orders"), "●") {
		t.Error("an unchanged action is marked")
	}

	// The Actions pane says how it changed.
	m = drive(t, m, nil, press("esc")...)
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertContains(t, screen(m), "changed      sql, config in feat/orders vs origin/main")
	m = startOn(t, m, "demo-project.staging.stg_orders")
	assertNotContains(t, screen(m), "changed      ")
}

func rowWith(view, s string) string {
	for _, r := range strings.Split(view, "\n") {
		if strings.Contains(r, s) {
			return r
		}
	}
	return ""
}

func TestPaletteCursorStartsOnFirstChangedWithoutSelection(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R", "esc")...)
	m.selected = nil
	m = drive(t, m, nil, press("/")...)
	if a := m.palette.selected(); a == nil || a.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("cursor on %v, want the first changed action", a)
	}
}

func TestRunChangedNothingToRun(t *testing.T) {
	res := testChanges(changes.HeadWorktree)
	res.Changed, res.Deleted, res.OnDefaultBranch, res.HeadRef = nil, nil, true, "main"
	fch := &fakeChanges{res: map[changes.Head]*changes.Result{changes.HeadWorktree: res}}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "You're on main, the branch Run Changed compares against")
	assertNotContains(t, screen(m), "r/enter run")
	m = drive(t, m, nil, press("enter")...)
	if len(fr.calls()) != 0 {
		t.Error("enter ran with nothing changed")
	}
	assertContains(t, screen(m), "R run changed")
}

func TestRunChangedRefusesCompileErrors(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil, "basic.json", "compile_error.json")
	m = drive(t, m, nil, press("c")...)
	m = drive(t, m, nil, press("esc")...) // the errors open by themselves
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "the project has compile errors: fix them first", "e shows the compile errors")
	for _, r := range fch.requests() {
		if r.Compile {
			t.Errorf("asked the engine anyway: %+v", r)
		}
	}
	m = drive(t, m, nil, press("e")...)
	assertNotContains(t, screen(m), "R run changed")
	assertContains(t, screen(m), "compile errors")
}

func TestRunChangedBaseFails(t *testing.T) {
	fch := &fakeChanges{err: errors.New("could not compile abc1234:\ndefinitions/orders.sqlx: Unexpected token")}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "could not compile abc1234:", "definitions/orders.sqlx: Unexpected token", "c tries again")
	m = drive(t, m, nil, press("enter")...)
	if len(fr.calls()) != 0 {
		t.Error("ran after a failure")
	}
	// c retries (the engine forgets the failure) and asks again.
	fch.mu.Lock()
	fch.err = nil
	fch.mu.Unlock()
	m = drive(t, m, nil, press("c")...)
	assertContains(t, screen(m), "2 changed actions")
	m = drive(t, m, nil, press("esc")...)
	assertNotContains(t, screen(m), "R run changed")
}

func TestRunChangedWaitsThenCloses(t *testing.T) {
	fch := &fakeChanges{block: make(chan struct{})}
	defer close(fch.block)
	m, _ := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "comparing the working tree with main…", "The first time today, this compiles the merge-base")
	m = drive(t, m, nil, press("enter")...) // nothing to run yet
	assertContains(t, screen(m), "comparing the working tree")
	m = drive(t, m, nil, press("esc")...)
	assertNotContains(t, screen(m), "R run changed")
	// A late answer to the closed overlay is dropped.
	m = drive(t, m, nil, changedMsg{seq: m.changed.seq, res: testChanges(changes.HeadWorktree)})
	assertNotContains(t, screen(m), "R run changed")
}

func TestRunChangedFullRefreshWarns(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R", "f")...)
	assertContains(t, screen(m), "⚠ full refresh: incremental tables lose their rows", "r/enter run full refresh of 2 changed actions")
	_ = drive(t, m, nil, press("y")...)
	if got := fr.calls(); len(got) != 1 || !got[0].FullRefresh || len(got[0].Actions) != 2 {
		t.Fatalf("runs = %+v", got)
	}
}

func TestRunChangedThroughAPI(t *testing.T) {
	fch := &fakeChanges{}
	fa := &fakeAPIRunner{}
	m, fr := newChangedTestModel(t, fch, fa)
	m = drive(t, m, nil, press("a", "R")...)
	reqs := fch.requests()
	if last := reqs[len(reqs)-1]; last.Head != changes.HeadPushed || !last.Compile {
		t.Fatalf("R with a on asked %+v", last)
	}
	assertContains(t, screen(m), "in origin/feat/orders vs origin/main @ abc1234 (pushed commit def5678)", "[x] a api", "r/enter run in GCP 2 changed actions")

	// a in the overlay switches back to the working tree and asks again.
	m = drive(t, m, nil, press("a")...)
	reqs = fch.requests()
	if last := reqs[len(reqs)-1]; last.Head != changes.HeadWorktree {
		t.Fatalf("a in the overlay asked %+v", last)
	}
	assertContains(t, screen(m), "(working tree)", "[ ] a api")
	m = drive(t, m, nil, press("a")...)
	_ = drive(t, m, nil, press("D", "enter")...)
	if len(fr.calls()) != 0 {
		t.Error("an API run ran dataform")
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.opts) != 1 {
		t.Fatalf("API runs = %d", len(fa.opts))
	}
	inv := fa.opts[0].Invocation
	want := []dataformapi.Target{{Database: "demo-project", Schema: "analytics", Name: "daily_orders"}, {Database: "demo-project", Schema: "staging", Name: "stg_customers"}}
	if !reflect.DeepEqual(inv.IncludedTargets, want) || !inv.TransitiveDependentsIncluded {
		t.Errorf("invocation = %+v", inv)
	}
	if fa.opts[0].Repo.Project != "demo-project" {
		t.Errorf("repo = %+v", fa.opts[0].Repo)
	}
}

func TestRunChangedUnavailable(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "run changed is not available")
	m = drive(t, m, nil, press("?")...)
	assertNotContains(t, screen(m), "run changed actions")

	fch := &fakeChanges{}
	m, _ = newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "run changed actions")
	m = drive(t, m, nil, press("esc", "3")...)
	assertContains(t, screen(m), "R run changed actions")
}

func TestRunChangedNotDuringARun(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil)
	m.running = true
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "a run is already in progress")
	assertNotContains(t, screen(m), "R run changed")
}

func TestRunChangedFoldsGeneratedAssertions(t *testing.T) {
	const ds = "demo-project.sales"
	gen := func(table, suffix string, reasons ...changes.Reason) changes.Changed {
		return changes.Changed{ID: project.ActionID(ds + "_assertions.sales_" + table + "_assertions_" + suffix), FileName: "definitions/" + table + ".sqlx",
			Type: "assertion", Reasons: reasons, Parent: project.ActionID(ds + "." + table)}
	}
	tbl := func(name string, reasons ...changes.Reason) changes.Changed {
		return changes.Changed{ID: project.ActionID(ds + "." + name), Target: project.Target{Database: "demo-project", Schema: "sales", Name: name},
			FileName: "definitions/" + name + ".sqlx", Type: "table", Reasons: reasons}
	}
	res := testChanges(changes.HeadWorktree)
	res.Changed = []changes.Changed{
		// Generated assertions sort before their table: they still fold under it.
		gen("0120_RULES", "rowConditions", changes.ReasonNew),
		gen("0120_RULES", "uniqueKey_0", changes.ReasonNew),
		tbl("0120_RULES", changes.ReasonNew),
		tbl("0200_PRODUCTS", changes.ReasonSQL),
		gen("0200_PRODUCTS", "uniqueKey_0", changes.ReasonSQL),
		gen("0200_PRODUCTS", "uniqueKey_1", changes.ReasonSQL),
		gen("0200_PRODUCTS", "nonNull", changes.ReasonSQL),
		// A hand-written assertion keeps its own row.
		{ID: "demo-project.order_assertions.0405_ORDERS_WO_CUSTOMER", Target: project.Target{Database: "demo-project", Schema: "order_assertions", Name: "0405_ORDERS_WO_CUSTOMER"},
			FileName: "definitions/0405_ORDERS_WO_CUSTOMER.sqlx", Type: "assertion", Reasons: []changes.Reason{changes.ReasonNew}},
		// Only the rowConditions changed: the table is there for them.
		gen("0300_ORDERS", "rowConditions", changes.ReasonSQL),
	}
	res.Deleted = []changes.Deleted{
		{ID: ds + ".0900_OLD", FileName: "definitions/0900_OLD.sqlx", Type: "table"},
		{ID: ds + "_assertions.sales_0900_OLD_assertions_uniqueKey_0", FileName: "definitions/0900_OLD.sqlx", Type: "assertion", Parent: ds + ".0900_OLD"},
	}
	fch := &fakeChanges{res: map[changes.Head]*changes.Result{changes.HeadWorktree: res}}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	view := screen(m)
	assertContains(t, view,
		"9 changed actions in feat/orders",
		"9 actions in 4 files: 2 tables, 7 assertions · 4 new, 5 sql · 2 removed",
		"● sales.0120_RULES", "table · new", "↳ 2 assertions: rowConditions, uniqueKey",
		"● sales.0200_PRODUCTS", "table · sql", "↳ 3 assertions: uniqueKey ×2, nonNull",
		"● order_assertions.0405_ORDERS_WO_CUSTOMER", "assertion · new",
		"○ sales.0300_ORDERS", "unchanged", "↳ 1 assertion: rowConditions",
		"✗ sales.0900_OLD", "↳ 1 assertion: uniqueKey",
		"r/enter run 9 changed actions")
	assertNotContains(t, view, "_assertions_")
	// Every table's row comes once, whatever order its assertions came in.
	if n := strings.Count(view, "sales.0120_RULES"); n != 1 {
		t.Errorf("0120_RULES listed %d times", n)
	}
	// The summaries line up their tails with the rows'.
	row, summary := rowWith(view, "● sales.0120_RULES"), rowWith(view, "↳ 2 assertions")
	col := func(line string) int { return ansi.StringWidth(line[:strings.LastIndex(line, "new")]) }
	if col(row) != col(summary) {
		t.Errorf("tails don't line up:\n%s\n%s", row, summary)
	}

	// All of them still run.
	_ = drive(t, m, nil, press("enter")...)
	if got := fr.calls(); len(got) != 1 || len(got[0].Actions) != 9 {
		t.Fatalf("runs = %+v", got)
	}
}

func TestRunChangedShowsRenames(t *testing.T) {
	const ds = "demo-project.inventory"
	res := testChanges(changes.HeadWorktree)
	res.Changed = append(res.Changed, changes.Changed{ID: ds + ".0200_STOCK_COVER", Target: project.Target{Database: "demo-project", Schema: "inventory", Name: "0200_STOCK_COVER"},
		FileName: "definitions/0200_STOCK_COVER.sqlx", Type: "incremental", Reasons: []changes.Reason{changes.ReasonNew},
		RenamedFrom: ds + ".0200_STOCK_MODEL", RenamedFromFile: "definitions/0200_STOCK_MODEL.sqlx", Similarity: 95})
	res.Deleted = append(res.Deleted, changes.Deleted{ID: ds + ".0200_STOCK_MODEL", FileName: "definitions/0200_STOCK_MODEL.sqlx", Type: "incremental", RenamedTo: ds + ".0200_STOCK_COVER"})
	fch := &fakeChanges{res: map[changes.Head]*changes.Result{changes.HeadWorktree: res}}
	m, _ := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	view := screen(m)
	assertContains(t, view,
		"3 actions in 3 files: 1 table, 1 view, 1 incremental table · 1 new, 1 renamed, 1 sql, 1 config",
		"definitions/0200_STOCK_COVER.sqlx  ← 0200_STOCK_MODEL.sqlx (95%)",
		"● inventory.0200_STOCK_COVER", "incremental · renamed",
		"✗ analytics.legacy_orders")
	assertNotContains(t, view, "inventory.0200_STOCK_MODEL")
	if got := changesSummary(res); !strings.HasSuffix(got, "1 config · 1 removed") {
		t.Errorf("summary = %q, want the renamed one not counted as removed", got)
	}
}

func TestAssertionKind(t *testing.T) {
	for id, want := range map[project.ActionID]string{
		"p.ds_assertions.ds_orders_assertions_uniqueKey_0":    "uniqueKey",
		"p.ds_assertions.ds_orders_assertions_uniqueKey_12":   "uniqueKey",
		"p.ds_assertions.ds_orders_assertions_rowConditions":  "rowConditions",
		"p.ds_assertions.ds_orders_assertions_nonNull":        "nonNull",
		"ds_assertions.ds_orders_v2_assertions_rowConditions": "rowConditions",
		"p.ds_assertions.custom_check":                        "custom_check",
	} {
		if got := assertionKind(id); got != want {
			t.Errorf("assertionKind(%s) = %q, want %q", id, got, want)
		}
	}
}

func TestChangesSummary(t *testing.T) {
	res := &changes.Result{Diff: changes.Diff{Changed: []changes.Changed{
		{FileName: "a.sqlx", Type: "incremental", Reasons: []changes.Reason{changes.ReasonNew}},
		{FileName: "b.sqlx", Type: "operations", Reasons: []changes.Reason{changes.ReasonSQL}},
		{FileName: "b.sqlx", Type: "operations", Reasons: []changes.Reason{changes.ReasonSQL, changes.ReasonConfig}},
		{FileName: "c.ipynb", Type: "notebook", Reasons: []changes.Reason{changes.ReasonConfig}},
		{FileName: "d.yaml", Type: "graph", Reasons: []changes.Reason{changes.ReasonNew}},
	}}}
	want := "5 actions in 4 files: 1 incremental table, 2 operations, 1 notebook, 1 other · 2 new, 2 sql, 2 config"
	if got := changesSummary(res); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// dbt's Changed Actions are typed by kind, and some changed only through a
// macro.
func TestChangesSummaryDbt(t *testing.T) {
	res := &changes.Result{Diff: changes.Diff{Changed: []changes.Changed{
		{FileName: "models/a.sql", Type: "view", Reasons: []changes.Reason{changes.ReasonUpstream}},
		{FileName: "models/a.yml", Type: "test", Reasons: []changes.Reason{changes.ReasonNew}},
		{FileName: "seeds/c.csv", Type: "seed", Reasons: []changes.Reason{changes.ReasonSQL}},
		{FileName: "snapshots/s.yml", Type: "snapshot", Reasons: []changes.Reason{changes.ReasonConfig, changes.ReasonUpstream}},
	}}}
	want := "4 actions in 4 files: 1 view, 1 seed, 1 snapshot, 1 test · 1 new, 1 sql, 1 config, 2 upstream"
	if got := changesSummary(res); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRunChangedKeepsListWhileAskingAgain(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, &fakeAPIRunner{})
	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "(working tree)", "r/enter run 2 changed actions")

	// a asks again for the pushed branch; the answer is slow.
	fch.mu.Lock()
	fch.block = make(chan struct{})
	fch.mu.Unlock()
	defer close(fch.block)
	m = drive(t, m, nil, press("a")...)
	view := screen(m)
	assertContains(t, view, "R run changed", "comparing the pushed branch with main…",
		"● analytics.daily_orders", "definitions/stg_customers.sqlx", "[x] a api")
	assertNotContains(t, view, "r/enter run", "The first time today")
	m = drive(t, m, nil, press("enter")...)
	if len(fr.calls()) != 0 {
		t.Fatal("enter ran the old list while asking again")
	}

	// The answer replaces it.
	m = drive(t, m, nil, changedMsg{seq: m.changed.seq, res: testChanges(changes.HeadPushed)})
	view = screen(m)
	assertContains(t, view, "(pushed commit def5678)", "r/enter run in GCP 2 changed actions")
	assertNotContains(t, view, "comparing the pushed branch")
}

func TestRunChangedRunsOnR(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	m = drive(t, m, nil, press("r")...)
	if got := fr.calls(); len(got) != 1 || len(got[0].Actions) != 2 {
		t.Fatalf("runs = %+v, want the 2 changed actions", got)
	}
	view := screen(m)
	assertNotContains(t, view, "R run changed")
	assertContains(t, view, "4 log · 2 changed actions")
}

func TestRunChangedUncheckFiles(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	view := screen(m)
	assertContains(t, view, "[x] definitions/daily_orders.sqlx", "[x] definitions/stg_customers.sqlx",
		"space toggle file", "x all", "j/k move")
	assertNotContains(t, view, "files checked", "[x] definitions/legacy_orders.sqlx", "[ ] definitions/legacy_orders.sqlx")

	// The cursor starts on the first file; space unchecks it.
	m = drive(t, m, nil, press("space")...)
	view = screen(m)
	assertContains(t, view, "[ ] definitions/daily_orders.sqlx", "[x] definitions/stg_customers.sqlx",
		"2 actions in 2 files", "1 of 2 files checked", "r/enter run 1 of 2 changed actions")
	m = drive(t, m, nil, press("enter")...)
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.staging.stg_customers"}}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	assertContains(t, screen(m), "4 log · 1 of 2 changed actions",
		"$ dataform run --actions demo-project.staging.stg_customers")
}

func TestRunChangedToggleFromAnyRow(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	// Rows: daily_orders.sqlx, its action, stg_customers.sqlx, its action.
	m = drive(t, m, nil, press("R")...)
	m = drive(t, m, nil, press("j", "space")...)
	assertContains(t, screen(m), "[ ] definitions/daily_orders.sqlx")
	m = drive(t, m, nil, press("j", "j", "space")...)
	view := screen(m)
	assertContains(t, view, "[ ] definitions/stg_customers.sqlx", "nothing checked · space includes a file, x all")
	assertNotContains(t, view, "r/enter run")
	m = drive(t, m, nil, press("enter", "r")...)
	if len(fr.calls()) != 0 {
		t.Fatal("ran with nothing checked")
	}
	assertContains(t, screen(m), "R run changed")

	// x checks them all, and again unchecks them all.
	m = drive(t, m, nil, press("x")...)
	assertContains(t, screen(m), "[x] definitions/daily_orders.sqlx", "[x] definitions/stg_customers.sqlx", "r/enter run 2 changed actions")
	m = drive(t, m, nil, press("x")...)
	assertContains(t, screen(m), "[ ] definitions/daily_orders.sqlx", "[ ] definitions/stg_customers.sqlx", "nothing checked")
	// With one checked, x unchecks it rather than checking the rest.
	m = drive(t, m, nil, press("space", "x")...)
	assertContains(t, screen(m), "nothing checked")
}

func TestRunChangedCursorSkipsRemoved(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	m = drive(t, m, nil, press("j", "j", "j", "j", "j", "space")...)
	if m.changed.cursor != 3 {
		t.Errorf("cursor = %d, want the last changed row", m.changed.cursor)
	}
	assertContains(t, screen(m), "[ ] definitions/stg_customers.sqlx", "[x] definitions/daily_orders.sqlx")
	m = drive(t, m, nil, press("g")...)
	if m.changed.cursor != 0 {
		t.Errorf("g: cursor = %d", m.changed.cursor)
	}
	m = drive(t, m, nil, press("G")...)
	if m.changed.cursor != 3 {
		t.Errorf("G: cursor = %d", m.changed.cursor)
	}
}

func TestRunChangedKeepsUncheckedUntilClosed(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, &fakeAPIRunner{})
	// Uncheck stg_customers, with the cursor on its action.
	m = drive(t, m, nil, press("R")...)
	m = drive(t, m, nil, press("j", "j", "j", "space")...)

	// Asking again keeps it unchecked, and the cursor on its row, though
	// daily_orders is gone from the new list.
	res := testChanges(changes.HeadPushed)
	res.Changed = res.Changed[1:]
	fch.mu.Lock()
	fch.res[changes.HeadPushed] = res
	fch.mu.Unlock()
	m = drive(t, m, nil, press("a")...)
	view := screen(m)
	assertContains(t, view, "(pushed commit def5678)", "[ ] definitions/stg_customers.sqlx", "nothing checked")
	assertNotContains(t, view, "daily_orders.sqlx")
	if m.changed.cursor != 1 || m.changed.cursorKey != "action demo-project.staging.stg_customers" {
		t.Errorf("cursor = %d %q", m.changed.cursor, m.changed.cursorKey)
	}
	// A file that comes back starts checked.
	m = drive(t, m, nil, press("a")...)
	assertContains(t, screen(m), "[x] definitions/daily_orders.sqlx", "[ ] definitions/stg_customers.sqlx")

	// Closing forgets it.
	m = drive(t, m, nil, press("esc", "R")...)
	m = drive(t, m, nil)
	assertContains(t, screen(m), "[x] definitions/stg_customers.sqlx", "r/enter run 2 changed actions")
	if m.changed.cursor != 0 {
		t.Errorf("cursor = %d after reopening", m.changed.cursor)
	}
}

func TestRunChangedDepsStillRunUnchecked(t *testing.T) {
	fch := &fakeChanges{}
	m, fr := newChangedTestModel(t, fch, nil)
	// daily_orders reads stg_customers: unchecked, d still runs it.
	m = drive(t, m, nil, press("R")...)
	m = drive(t, m, nil, press("G", "space")...)
	assertNotContains(t, screen(m), "⚠ d")
	m = drive(t, m, nil, press("d")...)
	assertContains(t, screen(m), "⚠ d: 1 unchecked action still runs as deps")
	m = drive(t, m, nil, press("D")...)
	assertContains(t, screen(m), "⚠ d, D: 1 unchecked action still runs as deps or dependents")

	// D alone, with daily_orders unchecked: it reads stg_customers.
	m = drive(t, m, nil, press("d", "space", "g", "space")...)
	assertContains(t, screen(m), "⚠ D: 1 unchecked action still runs as dependents")
	m = drive(t, m, nil, press("D")...)
	assertNotContains(t, screen(m), "⚠ D")

	_ = drive(t, m, nil, press("D", "enter")...)
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.staging.stg_customers"}, IncludeDependents: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
}

func TestRunChangedHelp(t *testing.T) {
	fch := &fakeChanges{}
	m, _ := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "R run changed", "check/uncheck file", "check/uncheck all files")

	m, _ = newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("?")...)
	assertNotContains(t, screen(m), "check/uncheck file")
}

func TestRunChangedCursorScrolls(t *testing.T) {
	res := testChanges(changes.HeadWorktree)
	res.Changed = nil
	for i := range 30 {
		name := fmt.Sprintf("t%02d", i)
		res.Changed = append(res.Changed, changes.Changed{ID: project.ActionID("demo-project.analytics." + name), Target: target("analytics", name),
			FileName: "definitions/" + name + ".sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}})
	}
	fch := &fakeChanges{res: map[changes.Head]*changes.Result{changes.HeadWorktree: res}}
	m, fr := newChangedTestModel(t, fch, nil)
	m = drive(t, m, nil, press("R")...)
	assertNotContains(t, screen(m), "definitions/t25.sqlx")

	// Down to t25's file, which scrolls into view, and uncheck it.
	var down []string
	for range 50 {
		down = append(down, "j")
	}
	m = drive(t, m, nil, press(down...)...)
	m = drive(t, m, nil, press("space")...)
	view := screen(m)
	assertContains(t, view, "[ ] definitions/t25.sqlx", "29 of 30 files checked", "run 29 of 30 changed actions")
	assertNotContains(t, view, "definitions/t00.sqlx")

	// Back up to the top, which scrolls back.
	m = drive(t, m, nil, press("ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u")...)
	m = drive(t, m, nil, press("space")...)
	assertContains(t, screen(m), "[ ] definitions/t00.sqlx", "28 of 30 files checked")
	_ = drive(t, m, nil, press("enter")...)
	if got := fr.calls(); len(got) != 1 || len(got[0].Actions) != 28 {
		t.Fatalf("runs = %+v", got)
	}
}
