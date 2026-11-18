package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/databricks"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

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
