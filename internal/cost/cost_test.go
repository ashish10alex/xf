package cost

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

const (
	gib = int64(1) << 30
	tib = int64(1) << 40
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"100GB": 100 * gib, "100 GiB": 100 * gib, "100gb": 100 * gib, "100G": 100 * gib,
		"1.5TiB": tib + tib/2, "2048": 2048, "512B": 512, "10 MB": 10 << 20, "1KB": 1024, "2PB": 2 << 50,
	} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GB", "-1GB", "ten GB", "1 XB", "1e3GB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) didn't fail", in)
		}
	}
}

// fakeBQ answers dry runs by SQL and partition lookups by table.
type fakeBQ struct {
	mu         sync.Mutex
	dryRuns    map[string]*bigquery.DryRun
	failures   map[string]error
	partitions map[string]*bigquery.Partition
	asked      []string // SQL dry run, in order
	lookups    map[string]int
}

func (f *fakeBQ) dryRun(_ context.Context, proj, _ string, sql string) (*bigquery.DryRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, proj+": "+sql)
	if err := f.failures[sql]; err != nil {
		return nil, err
	}
	if d := f.dryRuns[sql]; d != nil {
		return d, nil
	}
	return nil, errors.New("unexpected dry run of " + sql)
}

func (f *fakeBQ) partition(_ context.Context, t bigquery.TableRef) (*bigquery.Partition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookups == nil {
		f.lookups = map[string]int{}
	}
	f.lookups[t.Table]++
	return f.partitions[t.Table], nil
}

func precise(bytes int64, tables ...string) *bigquery.DryRun {
	d := &bigquery.DryRun{Bytes: bytes, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}
	for _, t := range tables {
		d.Tables = append(d.Tables, bigquery.TableRef{Project: "acme", Dataset: "raw", Table: t})
	}
	return d
}

func action(name string, kind project.ActionKind, query string) *project.Action {
	t := project.Target{Database: "acme", Schema: "marts", Name: name}
	return &project.Action{ID: t.ID(), Target: t, CanonicalTarget: t, Kind: kind, FileName: "definitions/" + name + ".sqlx", Query: query}
}

func graph(actions ...*project.Action) *project.Graph {
	g := project.NewGraph(actions)
	g.DefaultDatabase, g.DefaultLocation = "acme", "EU"
	return g
}

func changed(reason changes.Reason, actions ...*project.Action) *changes.Result {
	res := &changes.Result{BaseRef: "origin/main", MergeBase: "abc1234def", HeadRef: "feat/v2", Head: changes.HeadWorktree}
	for _, a := range actions {
		res.Changed = append(res.Changed, changes.Changed{ID: a.ID, Target: a.Target, FileName: a.FileName, Type: a.Kind.String(), Reasons: []changes.Reason{reason}})
	}
	return res
}

func find(t *testing.T, r *Report, name string) Action {
	t.Helper()
	for _, a := range r.Actions {
		if a.Name == "acme.marts."+name {
			return a
		}
	}
	t.Fatalf("no action %s in %+v", name, r.Actions)
	return Action{}
}

func TestRunEstimatesEveryChangedActionAndItsMergeBase(t *testing.T) {
	stg := action("stg_pageviews", project.KindView, "select * from raw.pageviews where day = current_date()")
	fct := action("fct_order_items", project.KindIncremental, "select * from raw.orders")
	fct.IncrementalQuery = "select * from raw.orders where ts > '2026-01-01'"
	check := action("fct_order_items_unique", project.KindAssertion, "select id from marts.fct_order_items group by 1 having count(*) > 1")
	added := action("dim_new", project.KindTable, "select 1")
	script := action("load", project.KindOperation, "")
	script.Queries = []string{"create temp table t as select 1", "select * from t"}
	head := graph(stg, fct, check, added, script)

	stgBase := action("stg_pageviews", project.KindView, "select * from raw.pageviews")
	fctBase := action("fct_order_items", project.KindIncremental, "select * from raw.orders")
	fctBase.IncrementalQuery = "select * from raw.orders where ts > '2025-01-01'"
	base := graph(stgBase, fctBase, check, script)

	scriptSQL := script.DryRunQueries()[0].SQL
	bq := &fakeBQ{
		dryRuns: map[string]*bigquery.DryRun{
			stg.Query:                precise(400 << 20),
			stgBase.Query:            precise(2 * gib),
			fct.Query:                precise(4 * tib),
			fct.IncrementalQuery:     {Bytes: 3 * gib, Accuracy: bigquery.AccuracyUpperBound, StatementType: "SELECT"},
			fctBase.IncrementalQuery: precise(5 * gib),
			check.Query:              {Bytes: 10 << 20, Accuracy: bigquery.AccuracyLowerBound, StatementType: "SELECT"},
			added.Query:              precise(0),
			scriptSQL:                {StatementType: "SCRIPT", Accuracy: bigquery.AccuracyUnknown},
		},
	}
	res := changed(changes.ReasonSQL, stg, fct, check, script)
	res.Changed = append(res.Changed, changed(changes.ReasonNew, added).Changed...)
	res.Deleted = []changes.Deleted{{ID: "acme.marts.gone", Type: "table"}}

	var phases []string
	var partial *Report
	now := time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC)
	r, err := Run(context.Background(), res, Head{Graph: head}, Deps{
		DryRun: bq.dryRun,
		Base:   func(context.Context) (*project.Graph, error) { return base, nil },
		Now:    func() time.Time { return now },
	}, func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Partial != nil {
			partial = p.Partial
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	// Most bytes first; what couldn't be estimated last.
	var order []string
	for _, a := range r.Actions {
		order = append(order, strings.TrimPrefix(a.Name, "acme.marts."))
	}
	if got := strings.Join(order, " "); got != "fct_order_items stg_pageviews fct_order_items_unique dim_new load" {
		t.Errorf("order %s", got)
	}

	// An incremental table counts its incremental run, with its full
	// rebuild beside it, and is compared with the merge-base's.
	f := find(t, r, "fct_order_items")
	if f.Estimate == nil || f.Estimate.Bytes != 3*gib || f.Estimate.Accuracy != bigquery.AccuracyUpperBound || f.Full == nil || f.Full.Bytes != 4*tib {
		t.Errorf("incremental %+v full %+v", f.Estimate, f.Full)
	}
	if d, ok := f.Delta(); !ok || d != -2*gib {
		t.Errorf("incremental delta %d, %v", d, ok)
	}
	if d, ok := find(t, r, "stg_pageviews").Delta(); !ok || d != 400<<20-2*gib {
		t.Errorf("view delta %d, %v", d, ok)
	}
	// The same SQL on both sides: no change.
	if got := DeltaText(find(t, r, "fct_order_items_unique")); got != "no change" {
		t.Errorf("assertion change %q", got)
	}
	// A new action has no merge-base to compare with.
	if n := find(t, r, "dim_new"); !n.New || n.Base != nil || DeltaText(n) != "new" {
		t.Errorf("new action %+v", n)
	}
	// A script BigQuery gives no figure for is listed, and counts for
	// nothing.
	if s := find(t, r, "load"); s.Estimate != nil || s.Reason != "BigQuery gives no estimate for a script" {
		t.Errorf("script %+v", s)
	}
	if total, bound := r.Total(); total != 3*gib+400<<20+10<<20 || bound != "≥ " {
		t.Errorf("total %d %q", total, bound)
	}
	if r.Unestimated() != 1 || !r.CheckedAt.Equal(now) || r.BasePending || r.Comparison.HeadRef != "feat/v2" {
		t.Errorf("report %+v", r)
	}
	if !r.Exceeds(3*gib) || r.Exceeds(4*gib) || r.Exceeds(0) {
		t.Error("Exceeds")
	}

	// The branch's estimates are handed over before the merge-base's.
	if partial == nil || !partial.BasePending || len(partial.Actions) != 5 || find(t, partial, "fct_order_items").Base != nil {
		t.Errorf("partial %+v", partial)
	}
	if got := strings.Join(phases, " | "); got != "Dry running | Compiling the merge-base | Dry running the merge-base" {
		t.Errorf("phases %s", got)
	}
	for _, sql := range bq.asked {
		if !strings.HasPrefix(sql, "acme: ") {
			t.Errorf("dry run in %s", sql)
		}
	}
}

func TestRunMarksReadsOfPartitionedTablesThatNeverNameTheColumn(t *testing.T) {
	unfiltered := action("unfiltered", project.KindTable, "select id from `acme.raw.events` -- where event_date = current_date()")
	filtered := action("filtered", project.KindTable, "select id from raw.events where EVENT_DATE >= '2026-01-01'")
	ingestion := action("ingestion", project.KindTable, "select id from raw.logs")
	pruned := action("pruned", project.KindTable, "select id from raw.logs where _PARTITIONDATE = current_date()")
	throughView := action("through_view", project.KindTable, "select id from raw.recent_events")
	plain := action("plain", project.KindTable, "select id from raw.customers")
	bq := &fakeBQ{
		dryRuns: map[string]*bigquery.DryRun{
			unfiltered.Query:  precise(tib, "events"),
			filtered.Query:    precise(gib, "events"),
			ingestion.Query:   precise(gib, "logs"),
			pruned.Query:      precise(gib, "logs"),
			throughView.Query: precise(gib, "events"), // the view's table, which the SQL doesn't name
			plain.Query:       precise(gib, "customers"),
		},
		partitions: map[string]*bigquery.Partition{"events": {Column: "event_date"}, "logs": {}},
	}
	actions := []*project.Action{unfiltered, filtered, ingestion, pruned, throughView, plain}
	r, err := Run(context.Background(), changed(changes.ReasonNew, actions...), Head{Graph: graph(actions...)},
		Deps{DryRun: bq.dryRun, Partition: bq.partition}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Unfiltered{
		"unfiltered": {{Table: "acme.raw.events", Column: "event_date"}},
		"ingestion":  {{Table: "acme.raw.logs", Column: "_PARTITIONTIME"}},
	}
	for _, a := range actions {
		got, w := find(t, r, a.Target.Name).Unfiltered, want[a.Target.Name]
		if len(got) != len(w) || len(w) == 1 && got[0] != w[0] {
			t.Errorf("%s: unfiltered %+v, want %+v", a.Target.Name, got, w)
		}
	}
	// Each table is looked up once, however many actions read it.
	if bq.lookups["events"] != 1 || bq.lookups["logs"] != 1 {
		t.Errorf("lookups %v", bq.lookups)
	}
}

func TestRunWithoutAMergeBase(t *testing.T) {
	a := action("orders", project.KindTable, "select 1")
	broken := action("broken", project.KindTable, "select nope")
	bq := &fakeBQ{
		dryRuns:  map[string]*bigquery.DryRun{a.Query: precise(gib)},
		failures: map[string]error{broken.Query: &bigquery.APIError{Status: 400, Message: "Unrecognized name: nope at [1:8]"}},
	}
	res := changed(changes.ReasonSQL, a, broken)
	r, err := Run(context.Background(), res, Head{Graph: graph(a, broken)}, Deps{
		DryRun: bq.dryRun,
		Base: func(context.Context) (*project.Graph, error) {
			return nil, errors.New("could not compile abc1234:\nboom")
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The branch is estimated all the same; only the change is missing.
	if o := find(t, r, "orders"); o.Estimate == nil || o.Base != nil || DeltaText(o) != "" {
		t.Errorf("orders %+v", o)
	}
	if r.BaseError != "could not compile abc1234:\nboom" || r.BasePending {
		t.Errorf("base error %q", r.BaseError)
	}
	if b := find(t, r, "broken"); b.Estimate != nil || b.Reason != "dry run failed: Unrecognized name: nope at [1:8]" {
		t.Errorf("broken %+v", b)
	}
}

func TestRunFailsWhenBigQueryRefusesEveryDryRun(t *testing.T) {
	a, b := action("a", project.KindTable, "select 1"), action("b", project.KindTable, "select 2")
	forbidden := &bigquery.APIError{Status: 403, Message: "Access Denied: User does not have bigquery.jobs.create permission"}
	bq := &fakeBQ{failures: map[string]error{a.Query: forbidden, b.Query: errors.Join(bigquery.ErrAuth, errors.New("token expired"))}}
	res := changed(changes.ReasonSQL, a, b)
	_, err := Run(context.Background(), res, Head{Graph: graph(a, b)}, Deps{DryRun: bq.dryRun}, nil)
	if err == nil || !strings.Contains(err.Error(), "BigQuery refused every dry run: Access Denied") {
		t.Fatalf("got %v", err)
	}
	// One that got through makes it a report: the refused action is listed.
	bq.failures = map[string]error{a.Query: forbidden}
	bq.dryRuns = map[string]*bigquery.DryRun{b.Query: precise(gib)}
	r, err := Run(context.Background(), res, Head{Graph: graph(a, b)}, Deps{DryRun: bq.dryRun}, nil)
	if err != nil || r.Unestimated() != 1 {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestRunDryRunsAnotherCompileOfTheProject(t *testing.T) {
	a := action("orders", project.KindTable, "select * from dev.raw")
	lost := action("lost", project.KindTable, "select 2")
	prodTarget := project.Target{Database: "acme-prod", Schema: "marts", Name: "orders"}
	prod := &project.Action{ID: prodTarget.ID(), Target: prodTarget, Kind: project.KindTable, Query: "select * from prod.raw"}
	prodGraph := project.NewGraph([]*project.Action{prod})
	prodGraph.DefaultDatabase, prodGraph.DefaultLocation = "acme-prod", "EU"
	prodBase := &project.Action{ID: prodTarget.ID(), Target: prodTarget, Kind: project.KindTable, Query: "select id from prod.raw"}
	bq := &fakeBQ{dryRuns: map[string]*bigquery.DryRun{prod.Query: precise(8 * gib), prodBase.Query: precise(gib)}}
	head := Head{Graph: prodGraph, Compiled: "prodCompilerOptions", ID: func(id project.ActionID) project.ActionID {
		if id == a.ID {
			return prod.ID
		}
		return ""
	}}
	r, err := Run(context.Background(), changed(changes.ReasonSQL, a, lost), head, Deps{
		DryRun: bq.dryRun,
		Base: func(context.Context) (*project.Graph, error) {
			return project.NewGraph([]*project.Action{prodBase}), nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Named as xf names it, estimated as prod compiles it, in prod's project.
	o := find(t, r, "orders")
	if d, ok := o.Delta(); o.Estimate == nil || o.Estimate.Bytes != 8*gib || !ok || d != 7*gib || o.DryRunAs != "acme-prod.marts.orders" {
		t.Errorf("orders %+v", o)
	}
	if bq.asked[0] != "acme-prod: select * from prod.raw" {
		t.Errorf("asked %v", bq.asked)
	}
	if l := find(t, r, "lost"); l.Estimate != nil || l.Reason != "nothing in the compile with prodCompilerOptions matches it" {
		t.Errorf("lost %+v", l)
	}
	if r.Compiled != "prodCompilerOptions" || r.DryRunIn != "acme-prod (EU)" {
		t.Errorf("compiled %q, dry run in %q", r.Compiled, r.DryRunIn)
	}
}

func TestRunWithNothingToEstimate(t *testing.T) {
	r, err := Run(context.Background(), changed(changes.ReasonSQL), Head{Graph: graph()}, Deps{
		Base: func(context.Context) (*project.Graph, error) {
			t.Error("compiled a merge-base for nothing")
			return nil, nil
		},
	}, nil)
	if err != nil || len(r.Actions) != 0 || r.Actions == nil {
		t.Fatalf("got %+v, %v", r, err)
	}
	text := Text(r, Options{Threshold: gib})
	for _, want := range []string{"0 changed actions in feat/v2 vs origin/main @ abc1234 (working tree)", "Nothing to estimate", "Total scanned: 0 B | Estimated cost: $0.000", "✓ Gate passed"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
}

func testReport() *Report {
	r := &Report{
		Actions: []Action{
			{Name: "acme.marts.fct_order_items", FileName: "definitions/fct_order_items.sqlx", Type: "incremental",
				Estimate: &Estimate{Bytes: 4*tib + tib/5, Accuracy: bigquery.AccuracyUpperBound}, Full: &Estimate{Bytes: 9 * tib},
				Base: &Estimate{Bytes: 3 * gib}, Unfiltered: []Unfiltered{{Table: "acme.raw.events", Column: "event_date"}}},
			{Name: "acme.marts.dim_customers", FileName: "definitions/dim_customers.sqlx", Type: "table", Estimate: &Estimate{Bytes: gib + gib*4/5}, New: true},
			{Name: "acme.marts.stg_pageviews", FileName: "definitions/stg_pageviews.sqlx", Type: "view", Estimate: &Estimate{Bytes: 420 << 20}, Base: &Estimate{Bytes: 420 << 20}},
			{Name: "acme.marts.load", Type: "operations", Reason: "BigQuery gives no estimate for a script"},
		},
		CheckedAt: time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC),
	}
	r.Comparison = changedComparison()
	return r
}

func TestText(t *testing.T) {
	got := Text(testReport(), Options{Threshold: 100 * gib})
	want := `4 changed actions in feat/v2 vs origin/main @ abc1234 (working tree)
⚠ marts.fct_order_items  ≤ 4.20 TiB  $26.25  +4.20 TiB (+$26.23)  (was 3.00 GiB · $0.018)  (full refresh 9.00 TiB · $56.25)  [MISSING PARTITION FILTER: event_date on raw.events]
✓ marts.dim_customers      1.80 GiB  $0.011  new
✓ marts.stg_pageviews    420.00 MiB  $0.003  no change
? marts.load             not estimated: BigQuery gives no estimate for a script
────────────────────────────────────────────────────────────
Total scanned: ≤ 4.20 TiB | Estimated cost: ≤ $26.26
✗ Gate failed: 4.20 TiB exceeds the limit of 100.00 GiB
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Another price changes the dollars, never the gate.
	if cheap := Text(testReport(), Options{Threshold: 100 * gib, USDPerTiB: 1}); !strings.Contains(cheap, "Estimated cost: ≤ $4.20") || !strings.Contains(cheap, "✗ Gate failed") {
		t.Errorf("at $1 per TiB:\n%s", cheap)
	}
	if strings.Contains(Text(testReport(), Options{}), "Gate") {
		t.Error("a gate line without a threshold")
	}
}

func TestReportsSayWhenTheDefaultTargetsWereDryRun(t *testing.T) {
	r := testReport()
	if md := Markdown(r, Options{}); strings.Contains(md, "default target") {
		t.Errorf("a hint though the setting is set:\n%s", md)
	}
	r.Unset = "prodCompilerOptions"
	hint := "Dry run against the project's default targets, which may be dev tables. Set `prodCompilerOptions` to dry run against prod."
	if md := Markdown(r, Options{}); !strings.HasSuffix(md, "<sub>"+hint+"</sub>\n") {
		t.Error(md)
	}
	if text := Text(r, Options{}); !strings.Contains(text, "\n⚠ "+strings.ReplaceAll(hint, "`", "")+"\n") {
		t.Error(text)
	}
	if data, err := JSON(r, Options{}); err != nil || !strings.Contains(string(data), `"unsetSetting": "prodCompilerOptions"`) {
		t.Errorf("%s, %v", data, err)
	}
	r.Unset = "dbtProdTarget"
	if got := UnsetHint(r); got != "Dry run against the default target of your dbt profile, which may be a dev target. Set `dbtProdTarget` to dry run against prod." {
		t.Error(got)
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestTextInColor(t *testing.T) {
	plain := Text(testReport(), Options{Threshold: 100 * gib})
	got := Text(testReport(), Options{Threshold: 100 * gib, Color: true})
	// Colour changes nothing but the escapes: the columns still line up.
	if stripped := ansiRE.ReplaceAllString(got, ""); stripped != plain {
		t.Errorf("without its escapes:\n%s\nwant:\n%s", stripped, plain)
	}
	for _, want := range []string{
		"\x1b[33m⚠\x1b[0m marts.fct_order_items", "\x1b[1;33m≤ 4.20 TiB\x1b[0m", "\x1b[31m+4.20 TiB (+$26.23)\x1b[0m",
		"\x1b[33m[MISSING PARTITION FILTER: event_date on raw.events]\x1b[0m",
		"\x1b[32m✓\x1b[0m marts.dim_customers", "\x1b[36mnew\x1b[0m",
		"\x1b[33m?\x1b[0m marts.load", "\x1b[2mnot estimated: BigQuery gives no estimate for a script\x1b[0m",
		"\x1b[1;31m✗ Gate failed: 4.20 TiB exceeds the limit of 100.00 GiB\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("coloured text missing %q:\n%q", want, got)
		}
	}
	if passed := Text(testReport(), Options{Threshold: 100 * tib, Color: true}); !strings.Contains(passed, "\x1b[32m✓ Gate passed") {
		t.Errorf("a passed gate:\n%q", passed)
	}
}

func TestMarkdown(t *testing.T) {
	r := testReport()
	r.BaseError = "could not compile abc1234:\nboom"
	got := Markdown(r, Options{Threshold: 100 * gib})
	for _, want := range []string{
		"### Query Cost of Branch Changes",
		"**Branch:** `feat/v2` vs `origin/main` @ `abc1234` | working tree",
		"**Total scanned:** ≤ 4.20 TiB | **Estimated cost:** ≤ $26.26 (on demand, at $6.25 per TiB)",
		"❌ Gate failed: 4.20 TiB exceeds the limit of 100.00 GiB",
		"| Action | Type | Full build (dry run) | Incremental (dry run) | Change | Notes |",
		"| `marts.fct_order_items` | incremental | 9.00 TiB · $56.25 | **≤ 4.20 TiB** · $26.25 | +4.20 TiB (+$26.23), was 3.00 GiB · $0.018 | ⚠️ no partition filter: `event_date` of `raw.events` · `definitions/fct_order_items.sqlx` |",
		"| `marts.stg_pageviews` | view | **420.00 MiB** · $0.003 | – | no change | `definitions/stg_pageviews.sqlx` |",
		"| `marts.dim_customers` | table | **1.80 GiB** · $0.011 | – | new | `definitions/dim_customers.sqlx` |",
		"Change is blank: the merge-base could not be compiled (could not compile abc1234:).",
		"**Not estimated (1)**, and not in the total:",
		"BigQuery gives no estimate for a script (1):\n\n- `marts.load`\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown missing %q:\n%s", want, got)
		}
	}

	notFound := func(table string) string {
		return "dry run failed: Not found: Table acme:" + table + " was not found in location EU"
	}
	r2 := &Report{Actions: []Action{
		{Name: "acme.marts.a_new", Type: "table", New: true, Reason: "BigQuery gave no estimate"},
		{Name: "acme.marts.b_new", Type: "table", New: true, Reason: "BigQuery gave no estimate"},
		{Name: "acme.marts.child", Type: "table", Reason: notFound("marts.a_new")},
		{Name: "acme.checks.a1", Type: "assertion", Reason: notFound("marts.a_new")},
		{Name: "acme.checks.a2", Type: "assertion", Reason: notFound("marts.a_new")},
		{Name: "acme.checks.gone", Type: "assertion", Reason: notFound("raw.gone")},
	}}
	md2 := Markdown(r2, Options{})
	for _, want := range []string{
		"⚠️ **Reads a missing table (1):**\n\n- `checks.gone` reads `raw.gone` *(not on this branch: check it)*",
		"**Waits on a new table added on this branch (3):**\n\n- Waiting on `marts.a_new`: `marts.child` + 2 assertions",
		"BigQuery gave no estimate (2):\n\n- `marts.a_new` *(new)*, `marts.b_new` *(new)*",
	} {
		if !strings.Contains(md2, want) {
			t.Errorf("Markdown root-cause grouping missing %q:\n%s", want, md2)
		}
	}
}

func TestJSON(t *testing.T) {
	data, err := JSON(testReport(), Options{Threshold: 100 * gib})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version    int
		TotalBytes int64
		TotalBound string
		TotalUsd   float64
		UsdPerTib  float64
		Gate       struct {
			ThresholdBytes int64
			Exceeded       bool
		}
		Unestimated int
		Actions     []struct {
			Name, File, Type, Accuracy, Error string
			Bytes, DeltaBytes                 *int64
			Usd                               *float64
			New                               bool
			FullRefresh, Base                 *struct{ Bytes int64 }
			Warnings                          []struct{ Kind, Table, Column string }
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.TotalBytes != 4*tib+tib/5+gib+gib*4/5+420<<20 || got.TotalBound != "upper" || got.UsdPerTib != 6.25 ||
		got.Gate.ThresholdBytes != 100*gib || !got.Gate.Exceeded || got.Unestimated != 1 || len(got.Actions) != 4 {
		t.Fatalf("document %+v", got)
	}
	f := got.Actions[0]
	if f.Name != "acme.marts.fct_order_items" || f.File != "definitions/fct_order_items.sqlx" || *f.Bytes != 4*tib+tib/5 || *f.DeltaBytes != 4*tib+tib/5-3*gib ||
		f.FullRefresh.Bytes != 9*tib || f.Base.Bytes != 3*gib || f.Accuracy != "UPPER_BOUND" ||
		len(f.Warnings) != 1 || f.Warnings[0].Kind != "missing_partition_filter" || f.Warnings[0].Column != "event_date" {
		t.Errorf("first action %+v", f)
	}
	if n := got.Actions[1]; !n.New || n.DeltaBytes != nil || n.Warnings == nil {
		t.Errorf("new action %+v", n)
	}
	if u := got.Actions[3]; u.Bytes != nil || u.Usd != nil || u.Error == "" {
		t.Errorf("unestimated action %+v", u)
	}
	// An action that wasn't estimated says so with nulls, not zeros.
	if !strings.Contains(string(data), `"bytes": null`) {
		t.Errorf("no null bytes in:\n%s", data)
	}
}

func TestJSONTotals(t *testing.T) {
	type doc struct {
		DryRunIn                        string
		BaseTotalBytes, DeltaTotalBytes *int64
		LaterBytes                      *int64
		LaterBound                      string
		LaterUsd                        *float64
		Actions                         []struct{ Name, DryRunAs string }
	}
	read := func(r *Report) doc {
		t.Helper()
		data, err := JSON(r, Options{})
		var d doc
		if err == nil {
			err = json.Unmarshal(data, &d)
		}
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// The new action counts nothing at the merge-base, the unestimated one
	// is left out of both totals.
	r := testReport()
	r.DryRunIn = "acme-prod (EU)"
	r.Actions[0].DryRunAs = "acme-prod.marts.fct_order_items"
	d := read(r)
	total, _ := r.Total()
	if base := 3*gib + 420<<20; d.BaseTotalBytes == nil || *d.BaseTotalBytes != base || *d.DeltaTotalBytes != total-base {
		t.Errorf("base total %v, delta %v; want %d, %d", d.BaseTotalBytes, d.DeltaTotalBytes, base, total-base)
	}
	if d.DryRunIn != "acme-prod (EU)" || d.Actions[0].DryRunAs != "acme-prod.marts.fct_order_items" || d.Actions[1].DryRunAs != "" {
		t.Errorf("where the dry runs ran: %+v", d)
	}
	if d.LaterBytes != nil || d.LaterUsd != nil {
		t.Errorf("later runs without a first build: %+v", d)
	}

	// An estimated action with no base leaves the change unknown.
	r = testReport()
	r.Actions[2].Base = nil
	if d := read(r); d.BaseTotalBytes != nil || d.DeltaTotalBytes != nil {
		t.Errorf("a total change with a base missing: %+v", d)
	}
	r = testReport()
	r.BaseError = "compile failed"
	if d := read(r); d.BaseTotalBytes != nil || d.DeltaTotalBytes != nil {
		t.Errorf("a total change without the merge-base: %+v", d)
	}

	// A first build counts in full; each later run counts its incremental
	// run instead.
	r = testReport()
	r.Actions[0].FirstBuild, r.Actions[0].Full, r.Actions[0].Incremental = true, nil, &Estimate{Bytes: 2 * gib, Accuracy: bigquery.AccuracyLowerBound}
	d = read(r)
	if later := 2*gib + gib + gib*4/5 + 420<<20; d.LaterBytes == nil || *d.LaterBytes != later || d.LaterBound != "lower" || d.LaterUsd == nil {
		t.Errorf("later runs %v %q %v, want %d lower", d.LaterBytes, d.LaterBound, d.LaterUsd, later)
	}
}

func TestStripCommentsAndNames(t *testing.T) {
	sql := "select '--not a comment', `a-b.c.events` /* event_date */ from t # event_date\nwhere x = 1 -- event_date"
	code := strings.ToLower(stripComments(sql))
	if names(code, "event_date") {
		t.Errorf("a commented column counted: %q", code)
	}
	if !names(code, "events") || !names(code, "X") || names(code, "event") || names(code, "") {
		t.Errorf("names over %q", code)
	}
	if !strings.Contains(code, "--not a comment") {
		t.Errorf("a string was cut: %q", code)
	}
}

func changedComparison() *impact.Comparison {
	return &impact.Comparison{HeadRef: "feat/v2", BaseRef: "origin/main", MergeBase: "abc1234def", HeadLabel: "working tree"}
}

func TestRuns(t *testing.T) {
	inc, full, then := &Estimate{Bytes: 1}, &Estimate{Bytes: 2}, &Estimate{Bytes: 3}
	for _, c := range []struct {
		a                 Action
		full, incremental *Estimate
		counted           RunKind
	}{
		{Action{Type: "table", Estimate: full}, full, nil, RunFull},
		{Action{Type: "incremental", Estimate: inc, Full: full}, full, inc, RunIncremental},
		{Action{Type: "incremental", Estimate: full, FirstBuild: true, Incremental: then}, full, then, RunFull},
		{Action{Type: "table"}, nil, nil, ""},
	} {
		f, i, counted := c.a.Runs()
		if f != c.full || i != c.incremental || counted != c.counted {
			t.Errorf("%+v: Runs() = %v, %v, %q", c.a, f, i, counted)
		}
	}
}

func notFound(table string) error {
	return &bigquery.APIError{Status: 404, Message: "Not found: Table " + strings.Replace(table, ".", ":", 1) + " was not found in location EU"}
}

func TestRunPricesAnUnbuiltIncrementalAsItsFullBuild(t *testing.T) {
	snap := action("snapshot", project.KindIncremental, "select * from raw.orders")
	snap.IncrementalQuery = "select * from raw.orders where ts > (select max(ts) from `acme.marts.snapshot`)"
	bq := &fakeBQ{
		dryRuns:  map[string]*bigquery.DryRun{snap.Query: precise(2 * gib)},
		failures: map[string]error{snap.IncrementalQuery: notFound("acme.marts.snapshot")},
	}
	r, err := Run(context.Background(), changed(changes.ReasonNew, snap), Head{Graph: graph(snap)}, Deps{DryRun: bq.dryRun}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := find(t, r, "snapshot")
	if a.Estimate == nil || a.Estimate.Bytes != 2*gib || !a.FirstBuild || a.Full != nil || a.Reason != "" || a.Incremental != nil {
		t.Fatalf("an incremental whose table isn't built is priced as its full build, with no full refresh beside it: %+v", a)
	}
	if _, _, ok := r.Later(); ok || LaterLine(r, Options{}) != "" {
		t.Error("later runs are known though dev has no copy")
	}

	// Dev has built it: later runs are its incremental run, on dev's copy.
	bq.dryRuns["select * from raw.orders where ts > (select max(ts) from `acme_dev.marts.snapshot`)"] = precise(gib / 4)
	inDev := func(context.Context, project.ActionID) (project.Target, time.Time, bool) {
		return project.Target{Database: "acme_dev", Schema: "marts", Name: "snapshot"}, time.Time{}, true
	}
	r, err = Run(context.Background(), changed(changes.ReasonNew, snap), Head{Graph: graph(snap)}, Deps{DryRun: bq.dryRun, InDev: inDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a = find(t, r, "snapshot")
	if a.Estimate == nil || a.Estimate.Bytes != 2*gib || a.Incremental == nil || a.Incremental.Bytes != gib/4 || len(a.FromDev) != 0 {
		t.Fatalf("later runs are priced on dev's copy, and the first build reads nothing from dev: %+v", a)
	}
	if got := LaterLine(r, Options{}); got != "Each run after the first: 256.00 MiB | $0.002" {
		t.Errorf("LaterLine = %q", got)
	}
	if got := FirstBuildNote(a, Options{}); got != "first run: full build; then 256.00 MiB · $0.002 a run, sized on dev's copy" {
		t.Errorf("FirstBuildNote = %q", got)
	}

	// Another table missing isn't the incremental's own: no estimate.
	other := action("other", project.KindIncremental, "select * from raw.orders")
	other.IncrementalQuery = "select * from `acme.marts.gone`"
	bq.failures[other.IncrementalQuery] = notFound("acme.marts.gone")
	r, err = Run(context.Background(), changed(changes.ReasonSQL, other), Head{Graph: graph(other)}, Deps{DryRun: bq.dryRun}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := find(t, r, "other"); a.Estimate != nil || a.FirstBuild {
		t.Fatalf("a missing upstream table is not a first build: %+v", a)
	}
}

func TestRunReadsATableTheBranchAddsFromDev(t *testing.T) {
	added := action("agg", project.KindTable, "select * from raw.orders")
	added2 := action("agg2", project.KindTable, "select 2")
	report := action("report", project.KindTable, "select * from `acme.marts.agg` join `acme.marts.agg2` using (id)")
	partly := "select * from `acme_dev.marts.agg` join `acme.marts.agg2` using (id)"
	devSQL := "select * from `acme_dev.marts.agg` join `acme_dev.marts.agg2` using (id)"
	bq := &fakeBQ{
		dryRuns: map[string]*bigquery.DryRun{added.Query: precise(gib), added2.Query: precise(1), devSQL: precise(3 << 20)},
		failures: map[string]error{
			report.Query: notFound("acme.marts.agg"),
			partly:       notFound("acme.marts.agg2"),
		},
	}
	built := time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var asked []project.ActionID
	inDev := func(_ context.Context, id project.ActionID) (project.Target, time.Time, bool) {
		mu.Lock()
		asked = append(asked, id)
		mu.Unlock()
		parts := strings.Split(string(id), ".")
		return project.Target{Database: "acme_dev", Schema: parts[1], Name: parts[2]}, built, true
	}
	res := changed(changes.ReasonNew, added, added2)
	res.Changed = append(res.Changed, changed(changes.ReasonSQL, report).Changed...)
	g := graph(added, added2, report)

	r, err := Run(context.Background(), res, Head{Graph: g}, Deps{DryRun: bq.dryRun}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := find(t, r, "report"); a.Estimate != nil || len(a.FromDev) > 0 {
		t.Fatalf("without InDev nothing is read from dev: %+v", a)
	}

	r, err = Run(context.Background(), res, Head{Graph: g}, Deps{DryRun: bq.dryRun, InDev: inDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := find(t, r, "report")
	want := []DevRead{{Table: "acme.marts.agg", Dev: "acme_dev.marts.agg", BuiltAt: built}, {Table: "acme.marts.agg2", Dev: "acme_dev.marts.agg2", BuiltAt: built}}
	if a.Estimate == nil || a.Estimate.Bytes != 3<<20 || len(a.FromDev) != 2 || a.FromDev[0] != want[0] || a.FromDev[1] != want[1] {
		t.Fatalf("both tables the branch adds are read from dev: %+v", a)
	}

	// A table in a dataset the branch adds too: BigQuery names the dataset.
	inNew := action("fresh", project.KindTable, "select 3")
	inNew.Target.Schema, inNew.ID = "newds", "acme.newds.fresh"
	reader := action("reader", project.KindTable, "select * from `acme.newds.fresh`")
	bq.dryRuns[inNew.Query] = precise(1)
	bq.failures[reader.Query] = &bigquery.APIError{Status: 404, Message: "Not found: Dataset acme:newds was not found in location EU"}
	bq.dryRuns["select * from `acme_dev.newds.fresh`"] = precise(5)
	res = changed(changes.ReasonNew, inNew)
	res.Changed = append(res.Changed, changed(changes.ReasonSQL, reader).Changed...)
	r, err = Run(context.Background(), res, Head{Graph: graph(inNew, reader)}, Deps{DryRun: bq.dryRun, InDev: inDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := find(t, r, "reader"); a.Estimate == nil || a.Estimate.Bytes != 5 || len(a.FromDev) != 1 || a.FromDev[0].Dev != "acme_dev.newds.fresh" {
		t.Fatalf("a table in a new dataset is read from dev: %+v", a)
	}

	// A missing table the branch doesn't add is never looked for in dev.
	gone := action("gone_reader", project.KindTable, "select * from `acme.marts.gone`")
	bq.failures[gone.Query] = notFound("acme.marts.gone")
	asked = nil
	r, err = Run(context.Background(), changed(changes.ReasonSQL, gone), Head{Graph: graph(gone)}, Deps{DryRun: bq.dryRun, InDev: inDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := find(t, r, "gone_reader"); a.Estimate != nil || len(asked) > 0 {
		t.Fatalf("a table not on the branch isn't looked for in dev: %+v, asked %v", a, asked)
	}
}

func TestExplanation(t *testing.T) {
	r := testReport()
	r.DryRunIn = "acme-prod (EU)"
	if got, want := Explanation(r), "The actions came from `feat/v2` diffed against `main`, and their queries, on the branch and at the merge-base, were dry run against the live tables in `acme-prod` (EU); nothing was run."; got != want {
		t.Errorf("Explanation:\n got %s\nwant %s", got, want)
	}

	// No merge-base, the defaults, a pushed branch, and no one project.
	r.BaseError, r.Unset, r.DryRunIn = "boom", "prodCompilerOptions", ""
	r.Comparison.HeadLabel = "pushed commit abc1234"
	if got, want := Explanation(r), "The actions came from the pushed `feat/v2` diffed against `main`, and their queries were dry run against the default target tables in BigQuery; nothing was run."; got != want {
		t.Errorf("Explanation:\n got %s\nwant %s", got, want)
	}

	r.Actions = []Action{{Name: "acme.marts.load", Reason: "BigQuery gives no estimate for a script"}}
	if got := Explanation(r); got != "" {
		t.Errorf("Explanation with nothing estimated = %q, want \"\"", got)
	}
	r.Comparison = nil
	if got := Explanation(r); got != "" {
		t.Errorf("Explanation without a comparison = %q, want \"\"", got)
	}
}
