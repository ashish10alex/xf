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
	caps    func() capabilities
}

// newDatabricksTest serves a Databricks project whose orders table is
// built (Unity Catalog has it) and whose fresh view isn't, counting the dry
// runs xf would run on its own.
func newDatabricksTest(t *testing.T, chosen bool) *databricksTest {
	t.Helper()
	dt := &databricksTest{dbx: &fakeDatabricks{}}
	deps := frontend.Deps{Backend: "dbt", Engine: newFakeEngine(databricksGraph()), Now: time.Now,
		DryRun: func(context.Context, string, string, string) (*warehouse.Check, error) {
			dt.mu.Lock()
			dt.dryRuns++
			dt.mu.Unlock()
			return &warehouse.Check{}, nil
		},
		QueryCost: func(context.Context, engine.CostRequest, func(cost.Progress)) (*cost.Report, error) { return nil, nil },
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
	if !c.Check || c.Warehouse != project.Databricks || c.Cost || c.Impact || c.Defer || c.Scratch || c.Inspector || c.DryRun {
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
