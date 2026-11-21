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
