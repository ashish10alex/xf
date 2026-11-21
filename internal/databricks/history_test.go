package databricks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// historyWorkspace answers Query History in two pages: dbt's runs of
// main.shop.orders with the prod and dev targets, the describe dbt runs
// after a build, an older prod build, and a query with no dbt comment.
func historyWorkspace(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var asked []string
	day := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC).UnixMilli()
	comment := func(target, node string) string {
		return `/* {"app": "dbt", "dbt_version": "1.9.0", "profile_name": "shop", "target_name": "` + target + `", "node_id": "` + node + `"} */` + "\n"
	}
	q := func(id, typ, text string, start, dur, read int64) string {
		out, _ := json.Marshal(map[string]any{"query_id": id, "status": "FINISHED", "statement_type": typ, "warehouse_id": "wh1",
			"query_start_time_ms": start, "duration": dur, "rows_produced": 5, "query_text": text,
			"metrics": map[string]any{"read_bytes": read, "rows_read_count": read / 100, "rows_produced_count": 1200}})
		return string(out)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/sql/history/queries", func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		if r.URL.Query().Get("page_token") == "" {
			if r.URL.Query().Get("filter_by.query_start_time_range.start_time_ms") == "" || r.URL.Query().Get("include_metrics") != "true" {
				t.Errorf("first page asked %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"res":[`+
				q("d1", "DESCRIBE", comment("prod", "model.shop.orders")+"describe table extended main.shop.orders", day+60_000, 300, 0)+","+
				q("b2", "CREATE", comment("prod", "model.shop.orders")+"create or replace table main.shop.orders as select 1", day, 151_000, 4<<30)+","+
				q("dev", "CREATE", comment("dev", "model.shop.orders")+"create or replace table dev.shop.orders as select 1", day+120_000, 9_000, 1<<20)+","+
				q("x", "SELECT", "select 1", day, 10, 0)+
				`],"next_page_token":"p2","has_next_page":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"res":[`+
			q("b1", "", comment("prod", "model.shop.orders")+"create or replace table main.shop.orders as select 0", day-86_400_000, 90_000, 2<<30)+
			`],"has_next_page":false}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/warehouses", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"warehouses":[{"id":"wh1","name":"Prod jobs","state":"STOPPED"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()}), &asked
}

func TestDbtRunsFromQueryHistory(t *testing.T) {
	c, asked := historyWorkspace(t)
	h, err := c.DbtRuns(context.Background(), time.Now().AddDate(0, 0, -HistoryDays), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 2 || h.Queries != 5 || h.Dbt != 4 || h.Targets["prod"] != 3 || h.Targets["dev"] != 1 || h.Truncated {
		t.Errorf("read %d pages, %+v", len(*asked), h)
	}
	r, ok := h.Runs["model.shop.orders"]
	if !ok || r.QueryID != "b2" || r.Target != "prod" || r.Warehouse != "Prod jobs" || r.Duration != 151*time.Second ||
		r.ReadBytes != 4<<30 || r.ProducedRows != 1200 || r.StatementType != "CREATE" {
		t.Errorf("run = %+v; want the latest prod build, not its describe, nor dev's", r)
	}
	if h, _ := c.DbtRuns(context.Background(), time.Now(), "staging"); len(h.Runs) != 0 || h.Dbt != 4 {
		t.Errorf("another target found %+v", h.Runs)
	}
}

func TestDbtRunsForBurstGroupingMetadataAndEarlyExit(t *testing.T) {
	pages := 0
	day := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC).UnixMilli()
	comment := `/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.orders"} */` + "\n"
	q := func(id, typ string, start, dur, read int64) string {
		out, _ := json.Marshal(map[string]any{
			"query_id": id, "status": "FINISHED", "statement_type": typ, "warehouse_id": "wh1",
			"query_start_time_ms": start, "duration": dur, "query_text": comment + typ,
			"metrics": map[string]any{"read_bytes": read, "rows_read_count": read / 10},
		})
		return string(out)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/2.0/sql/history/queries", func(w http.ResponseWriter, r *http.Request) {
		pages++
		if pages > 1 {
			t.Errorf("expected early exit after page 1 once %d bursts found, got page %d", minRunsForEarlyExit, pages)
		}
		// 5 distinct runs: latest run is a 2-statement burst (CREATE TEMP + MERGE 20s apart).
		_, _ = io.WriteString(w, `{"res":[`+
			q("m5", "MERGE", day+20_000, 10_000, 1<<30)+","+
			q("c5", "CREATE", day, 40_000, 3<<30)+","+
			q("r4", "MERGE", day-1*86_400_000, 30_000, 2<<30)+","+
			q("r3", "MERGE", day-2*86_400_000, 30_000, 2<<30)+","+
			q("r2", "MERGE", day-3*86_400_000, 20_000, 1<<30)+","+
			q("r1", "MERGE", day-4*86_400_000, 20_000, 1<<30)+
			`],"next_page_token":"p2","has_next_page":true}`)
	})
	mux.HandleFunc("GET /api/2.0/sql/warehouses", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"warehouses":[{"id":"wh1","name":"Prod jobs","state":"RUNNING","cluster_size":"Medium","enable_serverless_compute":true}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(Config{Host: srv.URL, Token: static("dapi-test"), HTTP: srv.Client()})

	h, err := c.DbtRunsFor(context.Background(), time.Now().AddDate(0, 0, -30), "prod", []string{"model.shop.orders"})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 || h.Truncated {
		t.Fatalf("pages=%d truncated=%v, want 1 and false", pages, h.Truncated)
	}
	run := h.Runs["model.shop.orders"]
	if run.StatementCount != 2 || run.QueryID != "c5" || run.ReadBytes != 4<<30 || run.Duration != 50*time.Second {
		t.Fatalf("burst run = %+v", run)
	}
	if run.Warehouse != "Prod jobs" || run.WarehouseSize != "Medium" || !run.Serverless {
		t.Fatalf("warehouse metadata = %+v", run)
	}
	if !h.Sampled || run.RunCount != 5 || run.MedianReadBytes != 2<<30 || run.MedianDuration != 30*time.Second {
		t.Fatalf("sampled=%v cadence/medians = %+v", h.Sampled, run)
	}
}
