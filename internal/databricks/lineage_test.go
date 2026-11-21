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
		t.Fatalf("table-only readers = %+v, %v; want export alone: totals is tracked and notebook:2 is entity-only", only, err)
	}
	all, err := r.TableReaders(ctx, "main.shop.orders")
	var names []string
	for _, l := range all {
		names = append(names, l.Table)
	}
	if err != nil || !slices.Equal(names, []string{"main.shop.daily", "main.shop.totals", "main.shop.export", "notebook:2"}) {
		t.Errorf("table readers = %v, %v; want the three tables plus notebook:2", names, err)
	}
}

func TestDownstreamEntityLineageSkipsTableProbe(t *testing.T) {
	tableProbes := 0
	var gotInclude string
	var gotBodyLen int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/lineage-tracking/table-lineage", func(w http.ResponseWriter, r *http.Request) {
		gotInclude = r.URL.Query().Get("include_entity_lineage")
		b, _ := io.ReadAll(r.Body)
		gotBodyLen = len(b)
		_, _ = io.WriteString(w, `{"downstreams":[
			{"notebookInfos":[{"notebook_id":11}]},
			{"jobInfos":[{"job_id":22}]},
			{"dashboardInfos":[{"dashboard_id":"dash-1"}]},
			{"lakeviewDashboardInfos":[{"dashboard_id":"lv-2"}]},
			{"pipelineInfos":[{"pipeline_id":"pipe-3"}]},
			{"queryInfos":[{"query_id":"q-4"}]}
		]}`)
	})
	mux.HandleFunc("GET /api/2.1/unity-catalog/tables/{name}", func(w http.ResponseWriter, r *http.Request) {
		tableProbes++
		http.Error(w, "should not probe entity readers", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})
	r := NewReaders(c, nil, nil)

	only, err := r.TableOnlyReaders(context.Background(), "main.shop.orders", nil)
	if err != nil {
		t.Fatalf("TableOnlyReaders: %v", err)
	}
	if len(only) != 0 {
		t.Errorf("TableOnlyReaders should omit table-only entity readers for column drops, got %+v", only)
	}
	if gotInclude != "true" || gotBodyLen != 0 {
		t.Errorf("include_entity_lineage=%q bodyLen=%d, want true and 0", gotInclude, gotBodyLen)
	}
	if tableProbes != 0 {
		t.Errorf("tableProbes = %d, want 0 for entity readers", tableProbes)
	}
	all, err := r.TableReaders(context.Background(), "main.shop.orders")
	if err != nil {
		t.Fatalf("TableReaders: %v", err)
	}
	var got []string
	for _, l := range all {
		got = append(got, l.Table)
	}
	want := []string{"notebook:11", "job:22", "dashboard:dash-1", "dashboard:lv-2", "pipeline:pipe-3", "query:q-4"}
	if !slices.Equal(got, want) {
		t.Errorf("TableReaders entity readers = %v, want %v", got, want)
	}
}

func TestPartitionAndClusterColumns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.1/unity-catalog/tables/{name}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"name": "events",
			"table_type": "MANAGED",
			"properties": {"clusteringColumns": "[[\"event_date\"],[\"user_id\"]]"},
			"columns": [{"name": "event_date", "type_text": "date", "partition_index": 0}, {"name": "user_id", "type_text": "bigint"}]
		}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})
	cols, err := c.PartitionAndClusterColumns(context.Background(), "main", "raw", "events")
	if err != nil || !slices.Equal(cols, []string{"event_date", "user_id"}) {
		t.Fatalf("PartitionAndClusterColumns = %v, %v", cols, err)
	}
}

func TestHasColumnLineageDeduplicatesAndRetriesTransientErrors(t *testing.T) {
	var mu sync.Mutex
	tableCalls := 0
	failTable := true
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/lineage-tracking/table-lineage", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"downstreams":[{"tableInfo":{"name":"totals","catalog_name":"main","schema_name":"shop"}}]}`)
	})
	mux.HandleFunc("GET /api/2.1/unity-catalog/tables/{name}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tableCalls++
		fail := failTable
		mu.Unlock()
		if fail {
			http.Error(w, `{"message":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"name":"totals","table_type":"MANAGED","columns":[{"name":"a","type_text":"int"}]}`)
	})
	mux.HandleFunc("GET /api/2.0/lineage-tracking/column-lineage", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"upstream_cols":[{"name":"amount","catalog_name":"main","schema_name":"shop","table_name":"orders"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})
	r := NewReaders(c, nil, nil)
	ctx := context.Background()

	// First call fails to probe table metadata: fails safe as untracked, but does not cache false.
	only, err := r.TableOnlyReaders(ctx, "main.shop.orders", nil)
	if err != nil || len(only) != 1 || only[0].Table != "main.shop.totals" {
		t.Fatalf("transient error call: got %+v, %v; want totals as TableOnly", only, err)
	}

	// Next call succeeds and caches tracked=true across concurrent callers.
	mu.Lock()
	failTable = false
	tableCalls = 0
	mu.Unlock()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := r.TableOnlyReaders(ctx, "main.shop.orders", nil)
			if err != nil || len(got) != 0 {
				t.Errorf("retry call: got %+v, %v; want none (totals is tracked)", got, err)
			}
		})
	}
	wg.Wait()
	if tableCalls != 1 {
		t.Errorf("tableCalls = %d, want 1 (single-flight deduplicated)", tableCalls)
	}
}
