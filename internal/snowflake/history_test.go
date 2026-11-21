package snowflake

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDbtRuns(t *testing.T) {
	var seenSQL string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		seenSQL, _ = req["statement"].(string)
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{
				{Name: "QUERY_ID"}, {Name: "QUERY_TEXT"}, {Name: "QUERY_TYPE"}, {Name: "WAREHOUSE_NAME"},
				{Name: "START_MS"}, {Name: "TOTAL_ELAPSED_TIME"}, {Name: "BYTES_SCANNED"}, {Name: "ROWS_PRODUCED"},
			}},
			Data: [][]*string{
				{
					strPtr("q-new"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ CREATE OR REPLACE TABLE shop.marts.fct_orders AS SELECT 1`),
					strPtr("CREATE_TABLE_AS_SELECT"), strPtr("PROD_WH"), strPtr("1731000000000"), strPtr("12500"), strPtr("2097152"), strPtr("100"),
				},
				{
					strPtr("q-old"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ CREATE OR REPLACE TABLE shop.marts.fct_orders AS SELECT 1`),
					strPtr("CREATE_TABLE_AS_SELECT"), strPtr("PROD_WH"), strPtr("1730000000000"), strPtr("5000"), strPtr("1048576"), strPtr("50"),
				},
				{
					strPtr("q-dev"),
					strPtr(`/* {"app": "dbt", "target_name": "dev", "node_id": "model.shop.fct_orders"} */ CREATE OR REPLACE TABLE dev.marts.fct_orders AS SELECT 1`),
					strPtr("CREATE_TABLE_AS_SELECT"), strPtr("DEV_WH"), strPtr("1732000000000"), strPtr("3000"), strPtr("512"), strPtr("10"),
				},
				{
					strPtr("q-show"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ SHOW TABLES`),
					strPtr("SHOW"), strPtr("PROD_WH"), strPtr("1733000000000"), strPtr("100"), strPtr("0"), strPtr("0"),
				},
			},
		})
	})

	since := time.Now().UTC().AddDate(0, 0, -10)
	h, err := c.DbtRuns(context.Background(), since, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenSQL, "DATE_PART(epoch_millisecond, START_TIME)") {
		t.Fatalf("expected timezone-independent DATE_PART in SQL:\n%s", seenSQL)
	}
	if !strings.Contains(seenSQL, since.Format("2006-01-02 15:04:05 +0000")) {
		t.Fatalf("expected since timestamp in SQL:\n%s", seenSQL)
	}
	if h.Days != 10 || h.Dbt != 4 || h.Targets["prod"] != 3 || h.Targets["dev"] != 1 {
		t.Fatalf("History metadata = %+v", h)
	}
	run, ok := h.Runs["model.shop.fct_orders"]
	if !ok || run.QueryID != "q-new" || run.ReadBytes != 2097152 || run.Duration != 12500*time.Millisecond || run.Warehouse != "PROD_WH" {
		t.Fatalf("run = %+v (ok=%v)", run, ok)
	}
}

func TestDbtRunsInfoSchemaFallback(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "ACCOUNT_USAGE") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{"code": "002003", "message": "not authorized"})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{
				{Name: "QUERY_ID"}, {Name: "QUERY_TEXT"}, {Name: "QUERY_TYPE"}, {Name: "WAREHOUSE_NAME"},
				{Name: "START_MS"}, {Name: "TOTAL_ELAPSED_TIME"}, {Name: "BYTES_SCANNED"}, {Name: "ROWS_PRODUCED"},
			}},
			Data: [][]*string{
				{
					strPtr("q-info"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.dim_customers"} */ CREATE OR REPLACE VIEW shop.marts.dim_customers AS SELECT 1`),
					strPtr("CREATE_VIEW"), strPtr("COMPUTE_WH"), strPtr("1731000000000"), strPtr("800"), strPtr("4096"), strPtr("0"),
				},
			},
		})
	})

	h, err := c.DbtRuns(context.Background(), time.Now().AddDate(0, 0, -HistoryDays), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if h.Days != InfoSchemaHistoryDays {
		t.Fatalf("Days = %d, want %d", h.Days, InfoSchemaHistoryDays)
	}
	if _, ok := h.Runs["model.shop.dim_customers"]; !ok {
		t.Fatalf("expected dim_customers run, got %+v", h.Runs)
	}
}

func TestDbtRunsForPushdownBurstsCreditsAndMedians(t *testing.T) {
	var seenSQL string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		seenSQL, _ = req["statement"].(string)
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{
				{Name: "QUERY_ID"}, {Name: "QUERY_TEXT"}, {Name: "QUERY_TYPE"}, {Name: "WAREHOUSE_NAME"},
				{Name: "WAREHOUSE_SIZE"}, {Name: "START_MS"}, {Name: "TOTAL_ELAPSED_TIME"}, {Name: "BYTES_SCANNED"},
				{Name: "ROWS_PRODUCED"}, {Name: "CREDITS"},
			}},
			Data: [][]*string{
				// Latest burst: CREATE TEMP (heavy) + MERGE 30s later -> merged into 1 run!
				{
					strPtr("q-merge"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ MERGE INTO shop.marts.fct_orders`),
					strPtr("MERGE"), strPtr("PROD_WH"), strPtr("Large"), strPtr("1731000030000"), strPtr("2000"), strPtr("1000000"), strPtr("10"), strPtr("0.05"),
				},
				{
					strPtr("q-temp"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ CREATE TEMPORARY TABLE shop.marts.fct_orders__dbt_tmp AS SELECT 1`),
					strPtr("CREATE_TABLE_AS_SELECT"), strPtr("PROD_WH"), strPtr("Large"), strPtr("1731000000000"), strPtr("10000"), strPtr("5000000"), strPtr("100"), strPtr("0.20"),
				},
				// Earlier run (1 day before).
				{
					strPtr("q-day1"),
					strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.fct_orders"} */ MERGE INTO shop.marts.fct_orders`),
					strPtr("MERGE"), strPtr("PROD_WH"), strPtr("Large"), strPtr("1730913600000"), strPtr("6000"), strPtr("2000000"), strPtr("50"), strPtr("0.10"),
				},
			},
		})
	})

	h, err := c.DbtRunsFor(context.Background(), time.Now().AddDate(0, 0, -30), "prod", []string{"model.shop.fct_orders"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenSQL, `'"model.shop.fct_orders"'`) || !strings.Contains(seenSQL, "QUERY_ATTRIBUTION_HISTORY") || !strings.Contains(seenSQL, "STARTSWITH(QUERY_TYPE, 'CREATE_')") {
		t.Fatalf("expected node_id pushdown, CREATE_* wildcard, and attribution join:\n%s", seenSQL)
	}
	run := h.Runs["model.shop.fct_orders"]
	if run.StatementCount != 2 || run.QueryID != "q-temp" || run.ReadBytes != 6000000 || run.Duration != 12*time.Second || run.WarehouseSize != "Large" {
		t.Fatalf("burst run = %+v", run)
	}
	if run.Credits < 0.249 || run.Credits > 0.251 {
		t.Fatalf("Credits = %v, want 0.25", run.Credits)
	}
	if run.RunCount != 2 || run.MedianReadBytes != 4000000 || run.MedianDuration != 9*time.Second {
		t.Fatalf("cadence/medians = %+v", run)
	}
}

func TestExecuteFetchesAllPartitions(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("partition") == "1" {
			writeJSONResp(w, http.StatusOK, statementResp{
				Data: [][]*string{{strPtr("row-page-1")}},
			})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Handle: "stmt-multi",
			Meta: &resultSet{
				RowType:       []rowType{{Name: "VAL"}},
				PartitionInfo: []partitionInfo{{RowCount: 1}, {RowCount: 1}},
			},
			Data: [][]*string{{strPtr("row-page-0")}},
		})
	})
	st, err := c.execute(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Data) != 2 || *st.Data[0][0] != "row-page-0" || *st.Data[1][0] != "row-page-1" {
		t.Fatalf("expected both partitions merged, got %d rows", len(st.Data))
	}
}

func TestDbtRunsFallsBackFromAttributionToPlainAccountUsage(t *testing.T) {
	calls := 0
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "QUERY_ATTRIBUTION_HISTORY") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{"code": "002003", "message": "QUERY_ATTRIBUTION_HISTORY not authorized"})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{
				{Name: "QUERY_ID"}, {Name: "QUERY_TEXT"}, {Name: "QUERY_TYPE"}, {Name: "WAREHOUSE_NAME"},
				{Name: "WAREHOUSE_SIZE"}, {Name: "START_MS"}, {Name: "TOTAL_ELAPSED_TIME"}, {Name: "BYTES_SCANNED"}, {Name: "ROWS_PRODUCED"},
			}},
			Data: [][]*string{{
				strPtr("q1"),
				strPtr(`/* {"app": "dbt", "target_name": "prod", "node_id": "model.shop.orders"} */ CREATE TABLE x AS SELECT 1`),
				strPtr("CREATE_TABLE_AS_SELECT"), strPtr("PROD_WH"), strPtr("Medium"), strPtr("1731000000000"), strPtr("4000"), strPtr("1024"), strPtr("1"),
			}},
		})
	})

	h, err := c.DbtRunsFor(context.Background(), time.Now().AddDate(0, 0, -HistoryDays), "prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || h.Days != HistoryDays || h.Runs["model.shop.orders"].WarehouseSize != "Medium" {
		t.Fatalf("calls=%d days=%d run=%+v", calls, h.Days, h.Runs["model.shop.orders"])
	}
}
