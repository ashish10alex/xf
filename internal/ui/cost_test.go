package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
)

// fakeCost answers C with a report, recording what was asked.
type fakeCost struct {
	mu     sync.Mutex
	reqs   []engine.CostRequest
	report *cost.Report
	err    error
}

func (f *fakeCost) call(_ context.Context, req engine.CostRequest, progress func(cost.Progress)) (*cost.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	progress(cost.Progress{Phase: "Dry running", Done: 1, Total: 2})
	if f.err != nil {
		return nil, f.err
	}
	r := *f.report
	r.Comparison = &impact.Comparison{HeadRef: "feat/orders", BaseRef: "origin/main", MergeBase: "abc1234def", HeadLabel: "working tree"}
	if req.Head == changes.HeadPushed {
		r.Comparison.HeadLabel = "pushed commit def5678"
	}
	return &r, nil
}

func (f *fakeCost) requests() []engine.CostRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.CostRequest(nil), f.reqs...)
}

func testCostReport() *cost.Report {
	return &cost.Report{
		Actions: []cost.Action{
			{Name: "demo-project.analytics.daily_orders", FileName: "definitions/daily_orders.sqlx", Type: "incremental",
				Estimate: &cost.Estimate{Bytes: 4 << 40, Accuracy: "UPPER_BOUND"}, Full: &cost.Estimate{Bytes: 9 << 40}, Base: &cost.Estimate{Bytes: 3 << 30},
				Unfiltered: []cost.Unfiltered{{Table: "demo-project.raw.events", Column: "event_date"}}},
			{Name: "demo-project.staging.stg_customers", Type: "view", Estimate: &cost.Estimate{Bytes: 420 << 20}, New: true},
			{Name: "demo-project.analytics.load", Type: "operations", Reason: "BigQuery gives no estimate for a script"},
		},
		CheckedAt: time.Date(2026, 10, 2, 9, 30, 0, 0, time.Local),
		Signature: "sig-1",
	}
}

func newCostTestModel(t *testing.T, fc *fakeCost) (Model, *[]string) {
	t.Helper()
	m, _ := newTestModel(t, "basic.json")
	if fc.report == nil {
		fc.report = testCostReport()
	}
	m.deps.QueryCost = fc.call
	m.deps.DefaultBranch = "main"
	copied := &[]string{}
	m.deps.Copy = func(s string) error {
		*copied = append(*copied, s)
		return nil
	}
	return m, copied
}

func TestCostShowsTheReportAndCopiesItsMarkdown(t *testing.T) {
	fc := &fakeCost{}
	m, copied := newCostTestModel(t, fc)
	m.deps.ProdCompilerOptions = "--default-database=acme-prod"
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 40}, keyMsg("C"))
	if reqs := fc.requests(); len(reqs) != 1 || reqs[0] != (engine.CostRequest{DefaultBranch: "main", Head: changes.HeadWorktree, ProdCompilerOptions: "--default-database=acme-prod"}) {
		t.Fatalf("C asked %+v", reqs)
	}
	assertContains(t, screen(m), "C query cost", "checked at 09:30 · working tree",
		"feat/orders vs origin/main @ abc1234",
		"Total scanned: ≤ 4.00 TiB | Estimated cost: ≤ $25.00",
		"Action", "Type", "Scans", "Cost", "Change", "Notes",
		"analytics.daily_orders", "incremental", "≤ 4.00 TiB", "$25.00", "+4.00 TiB",
		"⚠ no partition filter: event_date of raw.events", "full refresh 9.00 TiB",
		"staging.stg_customers", "420.00 MiB", "$0.003", "new",
		"Not estimated (1, not in the total)", "analytics.load: BigQuery gives no estimate for a script",
		"y copy Markdown", "c check again", "esc close")
	// Without API runs there is no pushed branch to compare.
	assertNotContains(t, screen(m), "a pushed branch")

	m = drive(t, m, nil, press("y")...)
	if got := lastCopy(t, copied); got != cost.Markdown(m.cost.report, cost.Options{}) || !strings.HasPrefix(got, "### Query Cost of Branch Changes") {
		t.Errorf("copied %q", got)
	}
	assertContains(t, screen(m), "copied the query cost report as Markdown")

	// c checks again, asking the engine for a new report.
	m = drive(t, m, nil, press("c")...)
	if reqs := fc.requests(); len(reqs) != 2 || !reqs[1].Refresh {
		t.Fatalf("c asked %+v", reqs)
	}

	// esc closes it; the keys go back to the panes.
	m = drive(t, m, nil, press("esc")...)
	if m.cost.open {
		t.Fatal("esc left the overlay open")
	}
	assertNotContains(t, screen(m), "C query cost")
}

func TestCostShowsTheBranchBeforeTheMergeBase(t *testing.T) {
	m, _ := newCostTestModel(t, &fakeCost{})
	// A check under way that has estimated the branch.
	m.cost.open, m.cost.loading, m.cost.seq = true, true, 1
	partial := testCostReport()
	partial.BasePending = true
	partial.Actions[0].Base = nil
	done := make(chan struct{})
	close(done)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 40},
		costProgressMsg{seq: 1, p: cost.Progress{Phase: "Compiling the merge-base", Partial: partial}, done: done})
	view := screen(m)
	assertContains(t, view, "estimating the same actions at the merge-base…", "Compiling the merge-base",
		"analytics.daily_orders", "≤ 4.00 TiB", "…")
	assertNotContains(t, view, "+4.00 TiB", "y copy Markdown")

	// The whole report takes its place.
	m = drive(t, m, nil, costMsg{seq: 1, report: testCostReport()})
	assertContains(t, screen(m), "+4.00 TiB", "checked at 09:30", "y copy Markdown")
	assertNotContains(t, screen(m), "at the merge-base…")

	// Progress for a request that was replaced is dropped.
	m = drive(t, m, nil, costProgressMsg{seq: 0, p: cost.Progress{Partial: partial}, done: done})
	assertContains(t, screen(m), "+4.00 TiB")
}

func TestCostSaysWhyThereIsNoChangeColumn(t *testing.T) {
	fc := &fakeCost{report: testCostReport()}
	fc.report.BaseError = "could not compile abc1234:\ndefinitions/x.sqlx: boom"
	fc.report.Actions[0].Base = nil
	m, _ := newCostTestModel(t, fc)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 40}, keyMsg("C"))
	assertContains(t, screen(m), "No change is shown: the merge-base could not be compiled.", "definitions/x.sqlx: boom")
}

func TestCostFlagsAReportTheBranchHasMovedOnFrom(t *testing.T) {
	fc := &fakeCost{}
	m, _ := newCostTestModel(t, fc)
	m = drive(t, m, nil, press("C")...)
	assertNotContains(t, screen(m), "the branch has changed")
	m = drive(t, m, nil, engineMsg{engine.State{Compiles: m.compiles, Graph: m.graph, ImpactSignature: "sig-2"}})
	assertContains(t, screen(m), "⚠ the branch has changed since this was checked at 09:30 · c checks again")
}

func TestCostComparesThePushedBranchWithA(t *testing.T) {
	fc := &fakeCost{}
	m, _ := newCostTestModel(t, fc)
	// A dbt project has no API runs: a stays on the working tree.
	m = drive(t, m, nil, press("C", "a")...)
	if reqs := fc.requests(); len(reqs) != 1 {
		t.Fatalf("a asked %+v", reqs)
	}
	assertContains(t, screen(m), "only the working tree can be estimated")

	m.deps.StartAPIRun = (&fakeAPIRunner{}).start
	m = drive(t, m, nil, press("a")...)
	reqs := fc.requests()
	if len(reqs) != 2 || reqs[1].Head != changes.HeadPushed {
		t.Fatalf("a asked %+v", reqs)
	}
	assertContains(t, screen(m), "pushed commit def5678", "a working tree")
}

func TestCostErrors(t *testing.T) {
	fc := &fakeCost{err: errors.New("could not compile abc1234:\ndefinitions/x.sqlx: boom")}
	m, _ := newCostTestModel(t, fc)
	m = drive(t, m, nil, press("C")...)
	assertContains(t, screen(m), "could not compile abc1234:", "definitions/x.sqlx: boom", "c tries again")
	m = drive(t, m, nil, press("y")...)
	assertContains(t, screen(m), "no report to copy yet")

	// With compile errors in the working tree it refuses, as R does.
	m.errors = append(m.errors, m.graph.Errors...)
	m.compileErr = errors.New("boom")
	m = drive(t, m, nil, press("c")...)
	assertContains(t, screen(m), "the project has compile errors", "e shows the compile errors")
}

func TestCostOffHidesC(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("C")...)
	assertContains(t, screen(m), "query cost is not available")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("?"))
	assertNotContains(t, screen(m), "query cost of changes")

	on, _ := newCostTestModel(t, &fakeCost{})
	on = drive(t, on, nil, tea.WindowSizeMsg{Width: 120, Height: 50}, keyMsg("?"))
	assertContains(t, screen(on), "query cost of changes", "C query cost")
}

func TestCostScrollsAndFitsTheWidth(t *testing.T) {
	m, _ := newCostTestModel(t, &fakeCost{})
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 20}, keyMsg("C"))
	assertContains(t, screen(m), "feat/orders vs origin/main")
	for _, l := range m.cost.lines {
		if w := ansi.StringWidth(l); w > m.impactInner() {
			t.Errorf("line is %d wide, the overlay %d: %q", w, m.impactInner(), ansi.Strip(l))
		}
	}
	m = drive(t, m, nil, press("G")...)
	assertContains(t, screen(m), "partitioned table it reads.")
	assertNotContains(t, screen(m), "feat/orders vs origin/main")

	// A resize draws the report again for the new width.
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	if m.cost.linesW != m.impactInner() {
		t.Errorf("drawn %d wide, the overlay is %d", m.cost.linesW, m.impactInner())
	}
}
