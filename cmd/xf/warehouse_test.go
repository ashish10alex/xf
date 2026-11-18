package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/databricks"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Query cost on Databricks is each changed model's last run with the prod
// target, from Query History; a model with none says so, and history with
// no dbt query comment says why.
func TestDatabricksCost(t *testing.T) {
	comment := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.0/sql/warehouses":
			_, _ = io.WriteString(w, `{"warehouses":[{"id":"wh1","name":"Prod jobs"}]}`)
		case "/api/2.0/sql/history/queries":
			text := "create or replace table main.shop.orders as select 1"
			if comment {
				text = `/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.orders"} */ ` + text
			}
			b, _ := json.Marshal(map[string]any{"res": []any{map[string]any{"query_id": "q1", "status": "FINISHED", "statement_type": "CREATE",
				"warehouse_id": "wh1", "query_start_time_ms": time.Now().Add(-time.Hour).UnixMilli(), "duration": 61_000, "query_text": text,
				"metrics": map[string]any{"read_bytes": 3 << 30, "rows_produced_count": 42}}}})
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	token := func(context.Context) (string, error) { return "t", nil }
	pw := &projectWarehouse{dbx: databricks.New(databricks.Config{Host: srv.URL, Token: token, HTTP: srv.Client()})}
	pw.once.Do(func() {})
	orders := project.Target{Database: "main", Schema: "shop", Name: "orders"}
	daily := project.Target{Database: "main", Schema: "shop", Name: "daily"}
	g := project.NewGraph([]*project.Action{
		{ID: orders.ID(), Target: orders, Kind: project.KindTable, DbtName: &project.DbtName{Package: "shop", Name: "orders"}},
		{ID: daily.ID(), Target: daily, Kind: project.KindView, DbtName: &project.DbtName{Package: "shop", Name: "daily"}},
	})
	res := &changes.Result{BaseRef: "origin/main", MergeBase: "abcdef1", HeadRef: "feat", Diff: changes.Diff{Changed: []changes.Changed{
		{ID: orders.ID(), Type: "table"}, {ID: daily.ID(), Type: "view", Reasons: []changes.Reason{changes.ReasonNew}}}}}
	r, err := pw.databricksCost(context.Background(), res, g, "prod", func(cost.Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	p := r.PastRuns
	if p == nil || len(p.Runs) != 2 || p.Target != "prod" || p.Why != "" {
		t.Fatalf("past runs = %+v", p)
	}
	if o := p.Runs[0]; o.Name != "main.shop.orders" || !o.Ran || o.ReadBytes != 3<<30 || o.Rows != 42 || o.Warehouse != "Prod jobs" || o.Duration != 61*time.Second {
		t.Errorf("orders = %+v", o)
	}
	if d := p.Runs[1]; d.Ran || !d.New {
		t.Errorf("daily = %+v", d)
	}
	comment = false
	r, _ = pw.databricksCost(context.Background(), res, g, "prod", func(cost.Progress) {})
	if !strings.Contains(r.PastRuns.Why, "query-comment") {
		t.Errorf("no comment: why = %q", r.PastRuns.Why)
	}
}

// Column impact on Databricks reads schemas from Unity Catalog and plans
// models with DESCRIBE QUERY on the SQL warehouse; the report names the
// warehouse, started by the check when it was stopped, once a model used it.
func TestDatabricksImpactDeps(t *testing.T) {
	var statements []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/2.0/sql/warehouses/wh1":
			_, _ = io.WriteString(w, `{"id":"wh1","name":"Starter","state":"STOPPED"}`)
		case r.URL.Path == "/api/2.0/sql/statements":
			body, _ := io.ReadAll(r.Body)
			statements = append(statements, string(body))
			_, _ = io.WriteString(w, `{"statement_id":"s1","status":{"state":"SUCCEEDED"},"result":{"data_array":[["id","bigint",null]]}}`)
		case strings.HasPrefix(r.URL.Path, "/api/2.1/unity-catalog/tables/"):
			_, _ = io.WriteString(w, `{"name":"orders","table_type":"MANAGED","columns":[{"name":"id","type_text":"bigint"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	token := func(context.Context) (string, error) { return "t", nil }
	pw := &projectWarehouse{dbx: databricks.New(databricks.Config{Host: srv.URL, Token: token, HTTP: srv.Client()})}
	pw.once.Do(func() {})
	ctx := context.Background()
	if _, _, err := pw.databricksImpact(ctx, nil, nil); !errors.Is(err, warehouse.ErrNoWarehouse) {
		t.Fatalf("no warehouse: %v", err)
	}
	pw.dbx.SetWarehouse("wh1")
	deps, used, err := pw.databricksImpact(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !deps.OnDemand || used() != nil {
		t.Errorf("before a check: on demand %v, used %+v", deps.OnDemand, used())
	}
	if cols, err := deps.Schema(ctx, "main.shop.orders"); err != nil || len(cols) != 1 || len(statements) != 0 {
		t.Errorf("schema = %+v, %v; %d statements", cols, err, len(statements))
	}
	if _, err := deps.DryRun(ctx, "main", "", "select 1 as id"); err != nil || len(statements) != 1 || !strings.Contains(statements[0], "DESCRIBE QUERY select 1 as id") {
		t.Fatalf("dry run: %v, %q", err, statements)
	}
	if u := used(); u == nil || u.Name != "Databricks SQL warehouse `Starter`" || !u.Started || u.Lineage != "Unity Catalog" {
		t.Errorf("used = %+v", u)
	}
}

// dbtProject writes a dbt project whose profile's target is of type typ.
func dbtProject(t *testing.T, typ string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"dbt_project.yml": "name: shop\nprofile: shop\n",
		"profiles.yml": "shop:\n  target: dev\n  outputs:\n    dev:\n      type: " + typ +
			"\n      host: dbc-1.cloud.databricks.com\n      http_path: /sql/1.0/warehouses/wh1\n      catalog: main\n      schema: shop\n      project: p\n      dataset: shop\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The language server's hover cards read Unity Catalog for a Databricks
// project, linking to Catalog Explorer, and BigQuery for any other; a
// manifest that says otherwise than the target wins.
func TestCatalogTables(t *testing.T) {
	t.Setenv("DATABRICKS_TOKEN", "dapi-test")
	t.Setenv("DATABRICKS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	opened := 0
	open := func(root string) *projectWarehouse {
		opened++
		return newProjectWarehouse(root, "", func() (config.Loaded, error) { return config.Loaded{}, nil })
	}
	tablesFor := catalogTables(open)
	dbx := dbtProject(t, "databricks")
	src := tablesFor(dbx, nil)
	if src == nil || src.Name != "Unity Catalog" || src.Tables == nil {
		t.Fatalf("a Databricks project's source = %+v", src)
	}
	if u := src.URL(project.Target{Database: "main", Schema: "shop", Name: "orders"}); u != "https://dbc-1.cloud.databricks.com/explore/data/main/shop/orders" {
		t.Errorf("link = %q", u)
	}
	_ = tablesFor(dbx, nil)
	if opened != 1 {
		t.Errorf("the project was opened %d times, want once", opened)
	}
	bq := dbtProject(t, "bigquery")
	if src := tablesFor(bq, nil); src != nil {
		t.Errorf("a BigQuery project's cards left BigQuery: %+v", src)
	}
	if src := tablesFor(bq, &project.Graph{Warehouse: project.Databricks}); src == nil {
		t.Error("a manifest saying Databricks didn't move the cards to Unity Catalog")
	}
}
