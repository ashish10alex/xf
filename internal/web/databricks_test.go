package web

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// fakeDatabricks is a SQL warehouse that is stopped until a check starts
// it, and counts the checks.
type fakeDatabricks struct {
	warehouse.Warehouse
	mu      sync.Mutex
	running bool
	checked []string
	queries []string
	polls   int
}

func (f *fakeDatabricks) Running(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, nil
}

func (f *fakeDatabricks) Check(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = true
	f.checked = append(f.checked, sql)
	return &warehouse.Check{Schema: []warehouse.Column{{Name: "order_id", Type: "bigint"}, {Name: "total", Type: "decimal(10,2)"}}}, nil
}

// StartQuery keeps the query; it stays pending, its warehouse starting,
// until QueryJob has been asked twice.
func (f *fakeDatabricks) StartQuery(_ context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, o.SQL)
	return warehouse.JobRef{Project: "workspace", ID: "st-1"}, nil
}

func (f *fakeDatabricks) QueryJob(context.Context, warehouse.JobRef) (*warehouse.QueryStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.polls++; f.polls < 2 {
		return &warehouse.QueryStatus{State: "PENDING", DMLRows: -1}, nil
	}
	f.running = true
	return &warehouse.QueryStatus{State: "DONE", StatementType: "SELECT", DMLRows: -1}, nil
}

func (f *fakeDatabricks) Rows(context.Context, warehouse.JobRef, string, int) (*warehouse.Page, error) {
	return &warehouse.Page{Schema: []warehouse.Column{{Name: "order_id", Type: "BIGINT"}}, Rows: [][]any{{"1"}, {"2"}}, Total: 2, Complete: true}, nil
}

func (f *fakeDatabricks) Describe(_ context.Context, catalog, schema, table string) (*warehouse.TableDetail, error) {
	return &warehouse.TableDetail{Type: "MANAGED", Schema: []warehouse.Column{{Name: "order_id", Type: "bigint"}}, NumRows: -1, NumBytes: -1}, nil
}

func (f *fakeDatabricks) DefaultProject() string { return "workspace" }

func databricksGraph() *project.Graph {
	orders := project.Target{Database: "workspace", Schema: "shop", Name: "orders"}
	fresh := project.Target{Database: "workspace", Schema: "shop", Name: "fresh"}
	g := project.NewGraph([]*project.Action{
		{ID: orders.ID(), Target: orders, Kind: project.KindTable, FileName: "models/orders.sql", Query: "select 1 as order_id",
			PreOps: []string{"set x = 1"}},
		{ID: fresh.ID(), Target: fresh, Kind: project.KindView, FileName: "models/fresh.sql", Query: "select 2 as id"},
	})
	g.Warehouse = project.Databricks
	return g
}

// databricksTest is a Databricks project served over HTTP.
type databricksTest struct {
	dbx     *fakeDatabricks
	mu      sync.Mutex
	dryRuns int
	get     func(id string) actionView
	post    func(path string, body, v any)
	getJSON func(path string, v any)
	caps    func() capabilities
}

// newDatabricksTest serves a Databricks project whose orders table is
// built (Unity Catalog has it) and whose fresh view isn't, counting the dry
// runs xf would run on its own.
func newDatabricksTest(t *testing.T, chosen bool) *databricksTest {
	t.Helper()
	dt := &databricksTest{dbx: &fakeDatabricks{}}
	deps := frontend.Deps{Backend: "dbt", Engine: newFakeEngine(databricksGraph()), Now: time.Now, Root: t.TempDir(),
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			dt.mu.Lock()
			dt.dryRuns++
			dt.mu.Unlock()
			return &warehouse.Check{}, nil
		},
		QueryCost: func(context.Context, engine.CostRequest, func(cost.Progress)) (*cost.Report, error) { return nil, nil },
		ColumnImpact: func(context.Context, engine.ImpactRequest, func(impact.Progress)) (*impact.Report, error) {
			return nil, nil
		},
		Table: func(_ context.Context, catalog, schema, table string) (*warehouse.Table, error) {
			if table != "orders" {
				return nil, &warehouse.APIError{Status: 404, Message: "Table does not exist"}
			}
			return &warehouse.Table{Type: "MANAGED", LastModified: time.Now(), Schema: []warehouse.Column{{Name: "order_id", Type: "bigint"}}}, nil
		},
	}
	p := &Project{Deps: deps, Databricks: &Databricks{TargetType: project.Databricks, AuthHint: "set DATABRICKS_TOKEN",
		Client: func(context.Context) (warehouse.Warehouse, error) { return dt.dbx, nil },
		Host:   func() string { return "https://dbc-1.cloud.databricks.com" },
		Chosen: func() bool { return chosen }}}
	srv, ts, c := newTestProject(t, p)
	dt.caps = func() capabilities { return srv.only().boot().Caps }
	dt.get = func(id string) actionView {
		t.Helper()
		var v actionView
		getJSON(t, c, ts.URL+"/api/action?id="+id, &v)
		return v
	}
	dt.post = func(path string, body, v any) { t.Helper(); postJSON(t, c, ts.URL+path, body, v) }
	dt.getJSON = func(path string, v any) { t.Helper(); getJSON(t, c, ts.URL+path, v) }
	return dt
}

func (dt *databricksTest) dryRunCount() int {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return dt.dryRuns
}

// waitView asks get until ok says its view is the one wanted.
func waitView(t *testing.T, get func() actionView, ok func(actionView) bool) actionView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := get()
		if ok(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("never got the view wanted; last: %+v", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func checkLine(v actionView) detail {
	for _, d := range v.Pane[0].Details {
		if d.Label == "check" {
			return d
		}
	}
	return detail{}
}

// A Databricks project is never dry run on its own: the built table's
// columns come from Unity Catalog, its link goes to Catalog Explorer, and
// what Databricks lacks is hidden.
func TestDatabricksChecksOnlyWhenAsked(t *testing.T) {
	dt := newDatabricksTest(t, true)
	get := dt.get
	v := waitView(t, func() actionView { return get("workspace.shop.orders") }, func(v actionView) bool { return v.Columns.Note != "" })
	if len(v.Columns.Fields) != 1 || v.Columns.Fields[0].Type != "bigint" || !strings.Contains(v.Columns.Note, "Unity Catalog") {
		t.Errorf("columns = %+v", v.Columns)
	}
	if d := checkLine(v); !strings.HasPrefix(d.Value, "not checked") {
		t.Errorf("check line = %+v", d)
	}
	if v.ConsoleURL != "https://dbc-1.cloud.databricks.com/explore/data/workspace/shop/orders" {
		t.Errorf("link = %q", v.ConsoleURL)
	}
	fresh := waitView(t, func() actionView { return get("workspace.shop.fresh") }, func(v actionView) bool { return !strings.HasPrefix(v.Columns.Reason, "looking") })
	if !strings.HasPrefix(fresh.Columns.Reason, "not built yet") {
		t.Errorf("an unbuilt view's columns: %+v", fresh.Columns)
	}
	if n := dt.dryRunCount(); n != 0 || len(dt.dbx.checked) != 0 {
		t.Errorf("%d dry runs and %d checks ran unasked", n, len(dt.dbx.checked))
	}
	c := dt.caps()
	if !c.Check || c.Warehouse != project.Databricks || c.Cost || !c.Impact || c.Defer || !c.Scratch || !c.Inspector || c.DryRun {
		t.Errorf("caps = %+v", c)
	}
}

// A check runs the model's query alone on the SQL warehouse, which says it
// is starting while the check waits, and its columns replace Unity
// Catalog's.
func TestDatabricksCheck(t *testing.T) {
	dt := newDatabricksTest(t, true)
	var ok okView
	dt.post("/api/check", map[string]string{"id": "workspace.shop.orders"}, &ok)
	if !ok.OK {
		t.Fatalf("check = %+v", ok)
	}
	v := waitView(t, func() actionView { return dt.get("workspace.shop.orders") }, func(v actionView) bool { return strings.HasPrefix(checkLine(v).Value, "✓") })
	if d := checkLine(v); !strings.HasPrefix(d.Value, "✓ 2 columns") || !strings.HasSuffix(d.Value, "warehouse started") {
		t.Errorf("check line = %+v", d)
	}
	if len(v.Columns.Fields) != 2 || !strings.HasPrefix(v.Columns.Note, "from the check") {
		t.Errorf("columns = %+v", v.Columns)
	}
	if len(dt.dbx.checked) != 1 || dt.dbx.checked[0] != "select 1 as order_id" {
		t.Errorf("checked %q, want the query without its hooks", dt.dbx.checked)
	}
	if n := dt.dryRunCount(); n != 0 {
		t.Errorf("%d dry runs", n)
	}
}

// Without a SQL warehouse, a check asks for one.
func TestDatabricksCheckAsksForAWarehouse(t *testing.T) {
	dt := newDatabricksTest(t, false)
	var ok okView
	dt.post("/api/check", map[string]string{"id": "workspace.shop.orders"}, &ok)
	if ok.OK || ok.Kind != "warehouse" {
		t.Errorf("check = %+v, want it to ask for a warehouse", ok)
	}
	if len(dt.dbx.checked) != 0 {
		t.Errorf("checked %q", dt.dbx.checked)
	}
}

// A dbt project whose target is BigQuery keeps its dry runs on its own,
// however it can reach Databricks.
func TestBigQueryDbtStillDryRuns(t *testing.T) {
	var mu sync.Mutex
	dryRuns := 0
	deps := frontend.Deps{Backend: "dbt", Engine: newFakeEngine(testGraph()), Now: time.Now,
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			mu.Lock()
			dryRuns++
			mu.Unlock()
			return &warehouse.Check{Schema: []warehouse.Column{{Name: "id", Type: "INT64"}}}, nil
		}}
	srv, ts, c := newTestProject(t, &Project{Deps: deps, Databricks: &Databricks{TargetType: project.BigQuery}})
	if caps := srv.only().boot().Caps; caps.Check || caps.Warehouse != project.BigQuery || !caps.DryRun {
		t.Errorf("caps = %+v", caps)
	}
	var v actionView
	getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders", &v)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return dryRuns > 0 })
	if v.ConsoleURL != (project.Target{Database: "p", Schema: "mart", Name: "orders"}).ConsoleURL() {
		t.Errorf("link = %q", v.ConsoleURL)
	}
	var ok okView
	postJSON(t, c, ts.URL+"/api/check", map[string]string{"id": "p.mart.orders"}, &ok)
	if ok.OK {
		t.Error("a BigQuery project took an on-demand check")
	}
}

// Scratch queries on Databricks: the dry run as SQL is typed calls nothing
// and still says what writes; a run goes to the SQL warehouse, saying so
// while it starts; a model's columns select from its table named part by
// part; a table's card links to Catalog Explorer.
func TestDatabricksScratch(t *testing.T) {
	dt := newDatabricksTest(t, true)
	var f scratchFile
	dt.post("/api/scratch/create", map[string]any{"id": "workspace.shop.orders", "form": "columns"}, &f)
	if !strings.Contains(f.SQL, "FROM workspace.shop.orders") || f.Project != "workspace" {
		t.Errorf("scratch = %+v", f)
	}
	var d scratchDryView
	dt.post("/api/scratch/dryrun", map[string]any{"id": f.ID, "sql": "select * from workspace.shop.orders"}, &d)
	if !d.OK || !d.NoDryRun || d.Writes || d.StatementType != "SELECT" {
		t.Errorf("dry run = %+v", d)
	}
	dt.post("/api/scratch/dryrun", map[string]any{"id": f.ID, "sql": "-- tidy\ndelete from workspace.shop.orders"}, &d)
	if !d.Writes || d.StatementType != "DELETE" {
		t.Errorf("a DELETE's dry run = %+v", d)
	}
	if n := dt.dryRunCount(); n != 0 || len(dt.dbx.checked) != 0 || len(dt.dbx.queries) != 0 {
		t.Fatalf("typing reached the warehouse: %d dry runs, %d checks, %d queries", n, len(dt.dbx.checked), len(dt.dbx.queries))
	}

	var ref warehouse.JobRef
	dt.post("/api/scratch/run", map[string]any{"id": f.ID, "sql": "select 1"}, &ref)
	if ref.ID != "st-1" || len(dt.dbx.queries) != 1 {
		t.Fatalf("run = %+v, queries %q", ref, dt.dbx.queries)
	}
	q := "project=workspace&id=st-1"
	var job jobView
	dt.getJSON("/api/scratch/job?"+q, &job)
	if job.State != "PENDING" || !job.Starting {
		t.Errorf("first poll = %+v, starting %v", job.QueryStatus, job.Starting)
	}
	job = jobView{}
	dt.getJSON("/api/scratch/job?"+q, &job)
	if job.State != "DONE" || job.Starting || job.Cost != "" {
		t.Errorf("second poll = %+v", job)
	}
	var page warehouse.Page
	dt.getJSON("/api/scratch/rows?"+q, &page)
	if page.Total != 2 || len(page.Rows) != 2 {
		t.Errorf("rows = %+v", page)
	}

	var card tableCard
	dt.getJSON("/api/scratch/meta/table?project=workspace&dataset=shop&table=orders", &card)
	if card.Detail == nil || card.URL != "https://dbc-1.cloud.databricks.com/explore/data/workspace/shop/orders" {
		t.Errorf("card = %+v", card)
	}
}

// The inspector on Databricks reads each table named part by part, and its
// dry run calls nothing.
func TestDatabricksInspector(t *testing.T) {
	dt := newDatabricksTest(t, true)
	var d scratchDryView
	dt.post("/api/inspector/dryrun", map[string]string{"table": "workspace.shop.orders", "filter": "order_id = 1"}, &d)
	if !d.OK || !d.NoDryRun || d.Writes {
		t.Errorf("dry run = %+v", d)
	}
	var runs map[string]*inspectorRun
	dt.post("/api/inspector/run", map[string]any{"key": "orders", "rows": []map[string]string{{"key": "r1", "table": "workspace.shop.orders", "filter": "order_id = 1"}}}, &runs)
	if r := runs["r1"]; r == nil || r.SQL != "SELECT * FROM workspace.shop.orders WHERE order_id = 1" {
		t.Errorf("runs = %+v", runs)
	}
	if n := dt.dryRunCount(); n != 0 || len(dt.dbx.checked) != 0 {
		t.Errorf("%d dry runs, %d checks", n, len(dt.dbx.checked))
	}
}

func TestDialect(t *testing.T) {
	bq, dbx := dialect{}, dialect{databricks: true}
	if got := bq.table("p", "d", "t"); got != "`p.d.t`" {
		t.Errorf("BigQuery table = %s", got)
	}
	if got := dbx.table("main", "my schema", "t"); got != "main.`my schema`.t" {
		t.Errorf("Databricks table = %s", got)
	}
	if got := dbx.ident("a`b"); got != "`a``b`" {
		t.Errorf("Databricks ident = %s", got)
	}
	if got := bq.ident("a`b"); got != "`a\\`b`" {
		t.Errorf("BigQuery ident = %s", got)
	}
}
