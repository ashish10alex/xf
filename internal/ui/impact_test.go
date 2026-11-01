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
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
)

// fakeImpact answers I with a report, recording what was asked.
type fakeImpact struct {
	mu     sync.Mutex
	reqs   []engine.ImpactRequest
	report *impact.Report
	err    error
}

func (f *fakeImpact) call(_ context.Context, req engine.ImpactRequest, progress func(impact.Progress)) (*impact.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	progress(impact.Progress{Phase: "Dry running", Done: 1, Total: 2})
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

func (f *fakeImpact) requests() []engine.ImpactRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.ImpactRequest(nil), f.reqs...)
}

func testReport() *impact.Report {
	return &impact.Report{
		ChangedCount: 2,
		AtRisk: []impact.Table{{
			Table: "demo-project.analytics.daily_orders", Type: "table",
			Columns: []impact.Column{{
				Column: "order_total", Change: impact.ColumnChange{Kind: impact.Dropped},
				Readers: []impact.Reader{{Table: "demo-project.reporting.weekly_revenue", Column: "order_total", DependencyType: lineage.ExactCopy, FilePath: "definitions/weekly_revenue.sqlx"}},
			}},
		}},
		Safe:      []impact.Table{{Table: "demo-project.staging.stg_customers", Type: "view", Columns: []impact.Column{}}},
		Unchecked: []impact.Unchecked{},
		CheckedAt: time.Date(2026, 10, 2, 9, 30, 0, 0, time.Local),
		Signature: "sig-1",
	}
}

func newImpactTestModel(t *testing.T, fi *fakeImpact) (Model, *[]string) {
	t.Helper()
	m, _ := newTestModel(t, "basic.json")
	if fi.report == nil {
		fi.report = testReport()
	}
	m.deps.ColumnImpact = fi.call
	m.deps.DefaultBranch = "main"
	copied := &[]string{}
	m.deps.Copy = func(s string) error {
		*copied = append(*copied, s)
		return nil
	}
	return m, copied
}

func TestImpactShowsTheReportAndCopiesItsMarkdown(t *testing.T) {
	fi := &fakeImpact{}
	m, copied := newImpactTestModel(t, fi)
	m = drive(t, m, nil, press("I")...)
	if reqs := fi.requests(); len(reqs) != 1 || reqs[0] != (engine.ImpactRequest{DefaultBranch: "main", Head: changes.HeadWorktree}) {
		t.Fatalf("I asked %+v", reqs)
	}
	assertContains(t, screen(m), "I column impact", "checked at 09:30 · working tree",
		"Column impact · feat/orders → main",
		"1 to fix before merging.", "base abc1234",
		"#", "Model", "What changed", "Who is affected",
		"1  FIX", "2  ok", "1 · analytics.daily_orders",
		"FIX", "analytics.daily_orders", "drops order_total",
		"reporting.weekly_revenue · exact copy · not changed here",
		"ok", "staging.stg_customers", "keeps every column",
		"Every reader (1)",
		"y copy Markdown", "c check again", "esc close")

	m = drive(t, m, nil, press("y")...)
	if got := lastCopy(t, copied); got != impact.Markdown(m.impact.report) || !strings.HasPrefix(got, "### 🚨 Column impact") {
		t.Errorf("copied %q", got)
	}
	assertContains(t, screen(m), "copied the column impact report as Markdown")

	// c checks again, asking the engine for a new report.
	m = drive(t, m, nil, press("c")...)
	if reqs := fi.requests(); len(reqs) != 2 || !reqs[1].Refresh {
		t.Fatalf("c asked %+v", reqs)
	}

	// esc closes it; the keys go back to the panes.
	m = drive(t, m, nil, press("esc")...)
	if m.impact.open {
		t.Fatal("esc left the overlay open")
	}
	assertNotContains(t, screen(m), "I column impact")
}

func TestImpactFlagsAReportTheBranchHasMovedOnFrom(t *testing.T) {
	fi := &fakeImpact{}
	m, _ := newImpactTestModel(t, fi)
	m = drive(t, m, nil, press("I")...)
	assertNotContains(t, screen(m), "the branch has changed")

	// A compile changes what the branch changes.
	m = drive(t, m, nil, engineMsg{engine.State{Compiles: m.compiles, Graph: m.graph, ImpactSignature: "sig-2"}})
	assertContains(t, screen(m), "⚠ the branch has changed since this was checked at 09:30 · c checks again")

	// The engine can say so too, e.g. for the pushed branch.
	fi.report.Stale = true
	m = drive(t, m, nil, engineMsg{engine.State{Compiles: m.compiles, Graph: m.graph, ImpactSignature: "sig-1"}})
	m = drive(t, m, nil, press("c")...)
	assertContains(t, screen(m), "the branch has changed since")
}

func TestImpactComparesThePushedBranchWithA(t *testing.T) {
	fi := &fakeImpact{}
	m, _ := newImpactTestModel(t, fi)
	m.deps.StartAPIRun = (&fakeAPIRunner{}).start
	m = drive(t, m, nil, press("I", "a")...)
	reqs := fi.requests()
	if len(reqs) != 2 || reqs[1].Head != changes.HeadPushed {
		t.Fatalf("a asked %+v", reqs)
	}
	assertContains(t, screen(m), "pushed commit def5678", "a working tree")
}

func TestImpactErrors(t *testing.T) {
	fi := &fakeImpact{err: errors.New("could not compile abc1234:\ndefinitions/x.sqlx: boom")}
	m, _ := newImpactTestModel(t, fi)
	m = drive(t, m, nil, press("I")...)
	assertContains(t, screen(m), "could not compile abc1234:", "definitions/x.sqlx: boom", "c tries again")
	assertNotContains(t, screen(m), "y copy Markdown")
	m = drive(t, m, nil, press("y")...)
	assertContains(t, screen(m), "no report to copy yet")

	// With compile errors in the working tree it refuses, as R does.
	m.errors = append(m.errors, m.graph.Errors...)
	m.compileErr = errors.New("boom")
	m = drive(t, m, nil, press("c")...)
	assertContains(t, screen(m), "the project has compile errors", "e shows the compile errors")
}

func TestImpactOffHidesI(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("I")...)
	assertContains(t, screen(m), "column impact is not available")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("?"))
	assertNotContains(t, screen(m), "column impact of changes")
}

func TestImpactScrollsAndFitsTheWidth(t *testing.T) {
	fi := &fakeImpact{}
	m, _ := newImpactTestModel(t, fi)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 20}, keyMsg("I"))
	assertContains(t, screen(m), "Column impact · feat/orders → main")
	assertNotContains(t, screen(m), "h/l", "**", "<details>", "<br>", "`")
	for _, l := range m.impact.lines {
		if w := ansi.StringWidth(l); w > m.impactInner() {
			t.Errorf("line is %d wide, the overlay %d: %q", w, m.impactInner(), ansi.Strip(l))
		}
	}
	m = drive(t, m, nil, press("G")...)
	assertContains(t, screen(m), "the branch's table: a pass means it no longer reads")
	assertNotContains(t, screen(m), "Column impact · feat/orders → main")

	// A resize draws the report again for the new width.
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	if m.impact.linesW != m.impactInner() {
		t.Errorf("drawn %d wide, the overlay is %d", m.impact.linesW, m.impactInner())
	}
}

func TestImpactPassesTheCompilerOptions(t *testing.T) {
	fi := &fakeImpact{}
	m, _ := newImpactTestModel(t, fi)
	m.deps.DevCompilerOptions = "--schema-suffix=ashish"
	m.deps.ProdCompilerOptions = "--default-database=acme-prod"
	_ = drive(t, m, nil, press("I")...)
	if reqs := fi.requests(); len(reqs) != 1 || reqs[0].DevCompilerOptions != "--schema-suffix=ashish" || reqs[0].ProdCompilerOptions != "--default-database=acme-prod" {
		t.Fatalf("I asked %+v", reqs)
	}
}

func TestImpactReportShowsRenames(t *testing.T) {
	r := &impact.Report{ChangedCount: 1, Against: []string{"p"},
		AtRisk: []impact.Table{
			{Table: "p.inventory.0200_STOCK_COVER", Type: "incremental", New: true, RenamedFrom: "p.inventory.0200_STOCK_MODEL", FromOld: true,
				Columns: []impact.Column{{Column: "UNITS", Change: impact.ColumnChange{Kind: impact.Dropped}}}},
			{Table: "p.inventory.0200_STOCK_MODEL", Type: "incremental", Deleted: true, RenamedTo: "p.inventory.0200_STOCK_COVER"},
		}}
	view := ansi.Strip(strings.Join(newStyles(true).impactReportLines(r, 120), "\n"))
	assertContains(t, view,
		"0200_STOCK_MODEL → 0200_STOCK_COVER", "renamed, drops UNITS",
		"dataset inventory", "renamed: renamed on this branch")
	assertNotContains(t, view, "deleted", "new: nothing reads it yet")
	if n := strings.Count(view, "0200_STOCK_MODEL"); n != 1 {
		t.Errorf("the rename takes %d rows, want one:\n%s", n, view)
	}
}
