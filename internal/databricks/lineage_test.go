package databricks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// lineageWorkspace answers Unity Catalog's lineage and table calls for
// main.shop.orders, which three tables read: daily reads its discount
// column, totals is tracked and reads no discount, and export (a table a
// notebook writes) has no column lineage at all. A notebook reads it too.
func lineageWorkspace(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	mux := http.NewServeMux()
	field := func(r *http.Request, name string) string {
		if v := r.URL.Query().Get(name); v != "" {
			return v
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body[name]
	}
	mux.HandleFunc("GET /api/2.0/lineage-tracking/table-lineage", func(w http.ResponseWriter, r *http.Request) {
		if field(r, "table_name") != "main.shop.orders" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `{"upstreams":[{"tableInfo":{"name":"raw","catalog_name":"main","schema_name":"src"}}],
			"downstreams":[
				{"tableInfo":{"name":"daily","catalog_name":"main","schema_name":"shop","table_type":"TABLE"}},
				{"tableInfo":{"name":"totals","catalog_name":"main","schema_name":"shop","table_type":"TABLE"}},
				{"tableInfo":{"name":"export","catalog_name":"main","schema_name":"shop","table_type":"TABLE"}},
				{"notebookInfos":[{"workspace_id":1,"notebook_id":2}]}]}`)
	})
	mux.HandleFunc("GET /api/2.0/lineage-tracking/column-lineage", func(w http.ResponseWriter, r *http.Request) {
		table, column := r.URL.Query().Get("table_name"), r.URL.Query().Get("column_name")
		mu.Lock()
		calls = append(calls, table+"."+column)
		mu.Unlock()
		switch table {
		case "main.shop.orders":
			if column == "discount" {
				_, _ = io.WriteString(w, `{"downstream_cols":[{"name":"discount","catalog_name":"main","schema_name":"shop","table_name":"daily"}]}`)
				return
			}
		case "main.shop.totals":
			_, _ = io.WriteString(w, `{"upstream_cols":[{"name":"amount","catalog_name":"main","schema_name":"shop","table_name":"orders"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /api/2.1/unity-catalog/tables/{name}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"x","table_type":"MANAGED","columns":[{"name":"a","type_text":"int"},{"name":"b","type_text":"int"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()}), &calls
}

func TestReadersFromUnityCatalog(t *testing.T) {
	c, _ := lineageWorkspace(t)
	g := project.NewGraph([]*project.Action{{ID: "main.shop.daily", Kind: project.KindTable}})
	r := NewReaders(c, g, nil)
	ctx := context.Background()

	links, err := r.ColumnLinks(ctx, "main.shop.orders", "discount")
	if err != nil || len(links) != 1 || links[0] != (lineage.ColumnLink{Table: "main.shop.daily", Column: "discount", DependencyType: lineage.Other}) {
		t.Fatalf("column links = %+v, %v", links, err)
	}
	only, err := r.TableOnlyReaders(ctx, "main.shop.orders", map[string]bool{"main.shop.daily": true})
	if err != nil || len(only) != 1 || only[0].Table != "main.shop.export" || only[0].DependencyType != lineage.TableOnly {
		t.Fatalf("table-only readers = %+v, %v; want export alone: totals is tracked and has no link", only, err)
	}
	all, err := r.TableReaders(ctx, "main.shop.orders")
	var names []string
	for _, l := range all {
		names = append(names, l.Table)
	}
	if err != nil || !slices.Equal(names, []string{"main.shop.daily", "main.shop.totals", "main.shop.export"}) {
		t.Errorf("table readers = %v, %v; want the three tables, not the notebook", names, err)
	}
}
