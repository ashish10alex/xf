package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/project"
)

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
