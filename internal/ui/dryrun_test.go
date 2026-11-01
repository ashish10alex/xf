package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeBigQuery answers dry runs from canned replies, picked by a substring
// of the SQL; anything else scans 1 KiB.
type fakeBigQuery struct {
	mu      sync.Mutex
	replies map[string]func() (*bigquery.DryRun, error)
	calls   []string // "project location sql"
}

func (f *fakeBigQuery) dryRun(_ context.Context, project, location, sql string) (*bigquery.DryRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, project+" "+location+" "+sql)
	for sub, reply := range f.replies {
		if strings.Contains(sql, sub) {
			return reply()
		}
	}
	return &bigquery.DryRun{Bytes: 1024, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}, nil
}

func (f *fakeBigQuery) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func scans(bytes int64, accuracy string) func() (*bigquery.DryRun, error) {
	return func() (*bigquery.DryRun, error) {
		return &bigquery.DryRun{Bytes: bytes, Accuracy: accuracy, StatementType: "SELECT"}, nil
	}
}

func fails(err error) func() (*bigquery.DryRun, error) {
	return func() (*bigquery.DryRun, error) { return nil, err }
}

func newDryRunModel(t *testing.T, bq *fakeBigQuery, fixtures ...string) (Model, *fakeCompiler) {
	t.Helper()
	fc := &fakeCompiler{t: t, fixtures: fixtures}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), DryRun: bq.dryRun, Editor: make(chan project.EditorEvent)})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	return closePalette(t, m), fc
}

func TestDryRunStatsForEachFileAction(t *testing.T) {
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"GROUP BY order_date, region\n  ) AS data": scans(0, bigquery.AccuracyUnknown), // uniqueKey
		"SUM(":      scans(1288490189, bigquery.AccuracyPrecise), // the table
		"WHERE NOT": fails(&bigquery.APIError{Status: 400, Message: "Unrecognized name: foo at [3:5]"}),
	}}
	m, _ := newDryRunModel(t, bq, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))

	if n := bq.callCount(); n != 3 {
		t.Fatalf("%d dry runs, want one per action: %q", n, bq.calls)
	}
	for _, c := range bq.calls {
		if !strings.HasPrefix(c, "demo-project EU ") {
			t.Errorf("dry run not in the project's default project and location: %q", c)
		}
	}
	view := screen(m)
	assertContains(t, view,
		"▸ TABLE      demo-project.analytics.daily_orders",
		"    dry run      1.20 GiB $0.007",
		"    dry run      ⚠ bytes unknown",
		"    dry run      ✗ Unrecognized name: foo at [3:5]")
	// Each action is its row and its dry run.
	if g := m.geometry(); g.cardH != 6+2 {
		t.Errorf("actions pane height = %d, want 3 rows of 2 lines", g.cardH)
	}

	// Results are kept: coming back to the file asks BigQuery nothing new.
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/weekly_revenue.sqlx"))
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	if n := bq.callCount(); n != 4 {
		t.Errorf("%d dry runs after switching back, want 4 (weekly_revenue's only)", n)
	}
	// A recompile with the same SQL retries only the one that failed.
	m.deps.Engine.Recompile(engine.ReasonFileChanged)
	m = drive(t, m, nil)
	if n := bq.callCount(); n != 5 {
		t.Errorf("%d dry runs after a recompile, want 5", n)
	}
	assertContains(t, screen(m), "✗ Unrecognized name")
}

func TestDryRunIncrementalShowsBothVariants(t *testing.T) {
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"MAX(event_date)":   scans(30<<20, bigquery.AccuracyUpperBound),
		"DATE '2000-01-01'": scans(2<<30, bigquery.AccuracyPrecise),
	}}
	m, _ := newDryRunModel(t, bq, "incremental.json")
	m = startOn(t, m, "demo-project.reporting.daily_events")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	assertContains(t, screen(m), "    full         2.00 GiB $0.012", "    incremental  ≤ 30.00 MiB $0.000",
		"    partition    event_date")
	// Each variant runs after its own pre_operations, which declare checkpoint.
	for _, c := range bq.calls {
		if !strings.HasPrefix(c, "demo-project EU DECLARE checkpoint") {
			t.Errorf("dry run without its pre_operations: %q", c)
		}
	}
}

func TestDryRunWithoutCredentials(t *testing.T) {
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"": fails(fmt.Errorf("%w: could not find default credentials", bigquery.ErrAuth)),
	}}
	m, _ := newDryRunModel(t, bq, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	view := screen(m)
	assertContains(t, view, "no credentials", "dry runs need Google credentials: run `gcloud auth application-default login`, then c")
	if n := strings.Count(view, "dry runs need Google credentials"); n != 1 {
		t.Errorf("credentials hint shown %d times", n)
	}
	// c retries once logged in.
	bq.mu.Lock()
	bq.replies = nil
	bq.mu.Unlock()
	m = drive(t, m, nil, press("c")...)
	assertNotContains(t, screen(m), "no credentials")
	assertContains(t, screen(m), "1.00 KiB $0.000")
}

func TestDryRunErrorsPointAtTheAccount(t *testing.T) {
	const hint = "press , to see the account xf uses"
	info := &ProjectInfo{ADC: func(context.Context) gcpauth.ADC { return gcpauth.ADC{} }}

	// No credentials: in the Actions pane and among the errors.
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"": fails(fmt.Errorf("%w: could not find default credentials", bigquery.ErrAuth)),
	}}
	m, _ := newDryRunModel(t, bq, "basic.json")
	m.deps.Info = info
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	assertContains(t, screen(m), "dry runs need Google credentials", hint)
	m = drive(t, m, nil, press("e")...)
	assertContains(t, screen(m), "no credentials: run", hint)

	// Access denied, which another account may explain; other errors don't.
	for status, want := range map[int]bool{403: true, 400: false} {
		bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
			"": fails(&bigquery.APIError{Status: status, Message: "refused"}),
		}}
		m, _ := newDryRunModel(t, bq, "basic.json")
		m.deps.Info = info
		m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
		m = drive(t, m, nil, press("e")...)
		if got := strings.Contains(screen(m), "access denied? "+hint); got != want {
			t.Errorf("HTTP %d: hint shown %v, want %v:\n%s", status, got, want, screen(m))
		}
	}
}

func TestNoDryRunsWithoutDeps(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertNotContains(t, screen(m), "dry run", "$")
}

func TestActionsPaneScrollsToSelection(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	// Room for one row: the SQL keeps its minimum.
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 100, Height: 20})
	if g := m.geometry(); g.cardH != 1+2 || g.sqlH != sqlMinRows+2 {
		t.Fatalf("actions pane height = %d, SQL %d, want 3 and %d", g.cardH, g.sqlH, sqlMinRows+2)
	}
	assertContains(t, screen(m), "▸ TABLE", "↓ 2 more")
	m = drive(t, m, nil, press("1", "G")...)
	view := screen(m)
	assertContains(t, view, "▸ ASSERTION  demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0", "↑ 2 more")
	assertNotContains(t, view, "TABLE ")
}

func TestActionRowNames(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "raw-project.shop.orders") // a declaration outside the default project
	assertContains(t, screen(m), "▸ DECLARATION  raw-project.shop.orders")

	// Narrow, a long name wraps under itself, at dots and underscores.
	m = startOn(t, m, "demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 50, Height: 30})
	assertContains(t, screen(m),
		"│▸ ASSERTION  demo-project.analytics_assertions. │",
		"│             analytics_daily_orders_assertions_ │",
		"│             uniqueKey_0                        │")

	// A disabled action says so after its name.
	m.graph.Actions["demo-project.analytics.daily_orders"].Disabled = true
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 100, Height: 30})
	assertContains(t, screen(m), "  TABLE      demo-project.analytics.daily_orders  disabled")
}

func TestDryRunErrorWraps(t *testing.T) {
	msg := "Unrecognized name: foo; Did you mean food? at [3:5] in the WHERE clause of the rowConditions assertion"
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"": fails(&bigquery.APIError{Status: 400, Message: msg}),
	}}
	m, _ := newDryRunModel(t, bq, "basic.json")
	m = startOn(t, m, "demo-project.analytics.weekly_revenue")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 30})
	assertContains(t, screen(m),
		"│    dry run      ✗ Unrecognized name: foo; Did you mean   │",
		"│                 food? at [3:5] in the WHERE clause of the│",
		"│                 rowConditions assertion                  │")
}

func TestFormatDryRun(t *testing.T) {
	tests := []struct {
		d    bigquery.DryRun
		want string
		warn bool
	}{
		{bigquery.DryRun{Bytes: 0, Accuracy: "PRECISE"}, "0 B $0.000", false},
		{bigquery.DryRun{Bytes: 512, Accuracy: "PRECISE"}, "512 B $0.000", false},
		{bigquery.DryRun{Bytes: 1288490189, Accuracy: "PRECISE"}, "1.20 GiB $0.007", false},
		{bigquery.DryRun{Bytes: 3 << 40, Accuracy: "UPPER_BOUND"}, "≤ 3.00 TiB $18.750", false},
		{bigquery.DryRun{Bytes: 5 << 20, Accuracy: "LOWER_BOUND"}, "≥ 5.00 MiB $0.000", false},
		{bigquery.DryRun{Bytes: 0, Accuracy: "UNKNOWN"}, "⚠ bytes unknown", true},
		{bigquery.DryRun{Bytes: 0, Accuracy: "LOWER_BOUND", StatementType: "SCRIPT"}, "⚠ no estimate for script", true},
		{bigquery.DryRun{Bytes: 1 << 30, Accuracy: "PRECISE", StatementType: "SCRIPT"}, "1.00 GiB $0.006", false},
	}
	for _, tt := range tests {
		got, warn := formatDryRun(&tt.d)
		if got != tt.want || warn != tt.warn {
			t.Errorf("formatDryRun(%+v) = %q, %v; want %q, %v", tt.d, got, warn, tt.want, tt.warn)
		}
	}
}

func TestOutdatedSavedCompileShowsWithoutDryRuns(t *testing.T) {
	bq := &fakeBigQuery{}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	saved, err := fc.compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fe := newFakeEngine(fc.compile)
	fe.state = fe.state.Load(&engine.Saved{Graph: saved, CompiledAt: testNow(), Took: 4 * time.Second, Outdated: "project files changed"})
	loaded := fe.state
	fe.hold = true // the startup compile hasn't finished
	m := New(Deps{Root: "/work/demo", Engine: fe, DryRun: bq.dryRun, Editor: make(chan project.EditorEvent)})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40}, engineMsg{loaded})
	m = closePalette(t, m)
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))

	assertContains(t, screen(m), "demo-project.analytics.daily_orders", "compiled 21:07:06 (4.0s), saved", "outdated · ", "compiling… (project files changed)")
	if n := bq.callCount(); n != 0 {
		t.Errorf("%d dry runs of the outdated SQL: %q", n, bq.calls)
	}

	fe.hold = false
	m = drive(t, m, nil)
	assertNotContains(t, screen(m), "saved", "outdated")
	if n := bq.callCount(); n != 3 {
		t.Errorf("%d dry runs once the compile finished, want one per action", n)
	}
}

// newDbtDryRunModel opens the dbt example (dbt v2's compile) with dry runs
// answered by bq; notice, when set, is the compile's, as for a dbt v2
// project with hooks that was only parsed.
func newDbtDryRunModel(t *testing.T, bq *fakeBigQuery, notice string) Model {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	compile := func(context.Context) (*project.Graph, error) {
		g, err := (&dbt.Backend{}).Parse(data)
		if err == nil {
			g.Notice = notice
		}
		return g, err
	}
	m := New(Deps{Backend: "dbt", Root: "/work/shop", Engine: newFakeEngine(compile), DryRun: bq.dryRun, Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 160, Height: 50})
	return closePalette(t, m)
}

// A dbt model's pane lists its tests from schema.yml too, each dry-run with
// its compiled query alone, in the model's project, BigQuery picking where.
func TestDbtDryRunsModelAndItsTests(t *testing.T) {
	bq := &fakeBigQuery{}
	m := newDbtDryRunModel(t, bq, "")
	m = startOn(t, m, "alex-personal-dev-01.xf_example.stg_customers")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 50}) // Update starts dry runs
	view := screen(m)
	assertContains(t, view, "VIEW", "xf_example.stg_customers", "not_null_stg_customers_email", "unique_stg_customers_customer_id",
		"relationships_stg_orders_96411fe0c89b49c3f4da955dfd358ba0", "dry run")
	if n := bq.callCount(); n != 5 {
		t.Fatalf("%d dry runs, want the model's and its 4 tests': %q", n, bq.calls)
	}
	for _, c := range bq.calls {
		if !strings.HasPrefix(c, "alex-personal-dev-01  ") || strings.Contains(c, "{{") {
			t.Errorf("dry run = %q, want the compiled SQL in the model's project, no location", c)
		}
	}

	// Moving onto the relationships test, which is stg_orders', keeps this
	// model's list.
	for range 3 {
		m = drive(t, m, nil, press("]")...)
	}
	if m.selected.Target.Name != "relationships_stg_orders_96411fe0c89b49c3f4da955dfd358ba0" {
		t.Fatalf("selected %s", m.selected.ID)
	}
	assertContains(t, screen(m), "not_null_stg_customers_email")
	assertNotContains(t, screen(m), "not_null_stg_orders_order_id")

	// Picked from elsewhere, a test shows its home model's list.
	m = startOn(t, m, m.selected.ID)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 50}) // Update starts dry runs
	assertContains(t, screen(m), "xf_example.stg_orders", "not_null_stg_orders_order_id")
	assertNotContains(t, screen(m), "not_null_stg_customers_email")
}

// SQL that dbt only parsed is Jinja, which BigQuery can't plan.
func TestDbtDryRunsNotCompiled(t *testing.T) {
	bq := &fakeBigQuery{}
	m := newDbtDryRunModel(t, bq, "SQL not compiled: hooks")
	m = startOn(t, m, "alex-personal-dev-01.xf_example.stg_customers")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 50}) // Update starts dry runs
	assertContains(t, screen(m), "dry run      not compiled")
	if n := bq.callCount(); n != 0 {
		t.Errorf("%d dry runs of uncompiled SQL: %q", n, bq.calls)
	}
}

func TestDryRunErrorsShowInTheErrorsBox(t *testing.T) {
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"WHERE NOT": fails(&bigquery.APIError{Status: 400, Message: "Unrecognized name: foo at [3:5]\nDid you mean food?"}),
	}}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	var copied string
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), DryRun: bq.dryRun, Editor: make(chan project.EditorEvent),
		Copy: func(s string) error { copied = s; return nil }})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	m = closePalette(t, m)
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	assertContains(t, screen(m), "idle · ✗ 1 dry run error (e)")
	if m.showErrors {
		t.Fatal("a failed dry run opened the errors box by itself")
	}

	m = drive(t, m, nil, press("e")...)
	view := screen(m)
	assertContains(t, view, "dry run errors", "Unrecognized name: foo at [3:5]", "Did you mean food?", "definitions/daily_orders.sqlx")
	assertNotContains(t, view, "compile errors")

	m = drive(t, m, nil, press("y")...)
	if !strings.Contains(copied, "dry run errors\n") || !strings.Contains(copied, "  Did you mean food?") {
		t.Errorf("y copied %q, want the box's text", copied)
	}
	if strings.Contains(copied, "\x1b[") {
		t.Errorf("y copied styling: %q", copied)
	}
	assertContains(t, screen(m), "copied the errors")
}
