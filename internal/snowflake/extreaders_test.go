package snowflake

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
)

func TestReadersExternalSelectConsumersAndQueryHistoryFallback(t *testing.T) {
	var gotEnrichedSQL string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		switch {
		case strings.Contains(sql, "/* xf:query-history */") && strings.Contains(sql, "QUERY_HISTORY"):
			gotEnrichedSQL = sql
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "USER_NAME"}, {Name: "ROLE_NAME"},
					{Name: "QUERY_TAG"}, {Name: "QUERY_COUNT"}, {Name: "LAST_SEEN"},
					{Name: "COLS_JSON"}, {Name: "HAS_UNTRACKED"}, {Name: "SAMPLE_QUERY"}, {Name: "SAMPLE_JOB_ID"},
				}},
				Data: [][]*string{
					// Looker dashboard reading CUSTOMER_TIER (45 queries, job sf-col-1) and ORDER_ID (320 queries, job sf-tbl-9)
					{
						strPtr("SHOP.MARTS.FCT_ORDERS"),
						strPtr("LOOKER_SVC"), strPtr("BI_ROLE"),
						strPtr(`{"looker":true,"explore":"orders","dashboard_id":"412"}`),
						strPtr("320"), strPtr("2026-11-22T15:00:00Z"),
						strPtr(`[{"col":"CUSTOMER_TIER","n":45,"last":"2026-11-21T12:00:00Z","job":"sf-col-1"},{"col":"ORDER_ID","n":320,"last":"2026-11-22T15:00:00Z","job":"sf-tbl-9"}]`), strPtr("false"),
						strPtr("SELECT CUSTOMER_TIER, ORDER_ID FROM SHOP.MARTS.FCT_ORDERS"), strPtr("sf-tbl-9"),
					},
					// Tableau via role/user reading AMOUNT (different column)
					{
						strPtr("SHOP.MARTS.FCT_ORDERS"),
						strPtr("TABLEAU_PROD"), strPtr("TABLEAU_ROLE"),
						strPtr(""),
						strPtr("85"), strPtr("2026-11-21T10:00:00Z"),
						strPtr(`["AMOUNT"]`), strPtr("false"),
						strPtr("SELECT AMOUNT FROM SHOP.MARTS.FCT_ORDERS"), strPtr("sf-tab-1"),
					},
					// Own xf scratch query -> must be filtered out
					{
						strPtr("SHOP.MARTS.FCT_ORDERS"),
						strPtr("DEV_USER"), strPtr("SYSADMIN"),
						strPtr("xf:scratch"),
						strPtr("5"), strPtr("2026-11-22T16:00:00Z"),
						strPtr(`["CUSTOMER_TIER"]`), strPtr("false"),
						strPtr("SELECT CUSTOMER_TIER FROM SHOP.MARTS.FCT_ORDERS"), strPtr("sf-xf-1"),
					},
					// dbt test query -> must be filtered out
					{
						strPtr("SHOP.MARTS.FCT_ORDERS"),
						strPtr("DBT_CI"), strPtr("TRANSFORMER"),
						strPtr(""),
						strPtr("12"), strPtr("2026-11-22T16:00:00Z"),
						strPtr(`["CUSTOMER_TIER"]`), strPtr("false"),
						strPtr(`/* {"app": "dbt", "node_id": "test.shop.not_null"} */ SELECT CUSTOMER_TIER FROM SHOP.MARTS.FCT_ORDERS`), strPtr("sf-dbt-1"),
					},
				},
			})
		case strings.Contains(sql, "ACCESS_HISTORY"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
				}},
			})
		case strings.Contains(sql, "OBJECT_DEPENDENCIES"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
			})
		}
	})

	readers := NewReaders(c, nil, nil).WithExternalReaders(14)
	ctx := context.Background()
	readers.Preload(ctx, []string{"shop.marts.fct_orders"})

	// Verify SQL uses ordinal GROUP BY (no ambiguous USER_NAME), pre-filters CTE, covers BASE_OBJECTS_ACCESSED, captures MAX_BY query ID, and filters dbt/xf in SQL.
	for _, want := range []string{"GROUP BY 1, 2, 3, 4", "BASE_OBJECTS_ACCESSED", "WITH accessed AS", `NOT ILIKE '%"app": "dbt"%'`, "MAX_BY(QUERY_ID, QUERY_START_TIME)"} {
		if !strings.Contains(gotEnrichedSQL, want) {
			t.Errorf("enriched SQL missing %q:\n%s", want, gotEnrichedSQL)
		}
	}

	links, err := readers.ColumnLinks(ctx, "shop.marts.fct_orders", "customer_tier")
	if err != nil {
		t.Fatalf("ColumnLinks error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1 (Looker only, filtering xf & dbt): %+v", len(links), links)
	}
	if links[0].Consumer == nil || links[0].Consumer.Tool != "Looker" || links[0].Consumer.Asset != "explore orders, dashboard 412" || links[0].Consumer.Queries != 45 || links[0].Consumer.LastSeen != "2026-11-21T12:00:00Z" || links[0].Consumer.SampleJobID != "sf-col-1" || links[0].DependencyType != lineage.Other {
		t.Errorf("unexpected Looker link (want per-column count 45, job sf-col-1): %+v (%+v)", links[0], links[0].Consumer)
	}

	// TableReaders includes both Looker (full table count 320, job sf-tbl-9) and Tableau (still excluding xf & dbt).
	all, err := readers.TableReaders(ctx, "shop.marts.fct_orders")
	if err != nil || len(all) != 2 {
		t.Fatalf("TableReaders = %+v, err=%v (want 2)", all, err)
	}
	if all[0].Consumer == nil || all[0].Consumer.Queries != 320 || all[0].Consumer.SampleJobID != "sf-tbl-9" {
		t.Errorf("TableReaders Looker = %+v, want 320 / sf-tbl-9", all[0].Consumer)
	}
	if len(readers.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %v", readers.Warnings())
	}
}

func TestReadersFallbackWhenQueryHistoryDenied(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		switch {
		case strings.Contains(sql, "/* xf:query-history */") && strings.Contains(sql, "QUERY_HISTORY"):
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "002003",
				"message": "View 'SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY' does not exist or not authorized.",
			})
		case strings.Contains(sql, "/* xf:query-history */"):
			// Fallback ACCESS_HISTORY-only query succeeds!
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "USER_NAME"}, {Name: "ROLE_NAME"},
					{Name: "QUERY_TAG"}, {Name: "QUERY_COUNT"}, {Name: "LAST_SEEN"},
					{Name: "COLS_JSON"}, {Name: "HAS_UNTRACKED"}, {Name: "SAMPLE_QUERY"},
				}},
				Data: [][]*string{{
					strPtr("SHOP.MARTS.FCT_ORDERS"), strPtr("TABLEAU_PROD"), strPtr(""),
					strPtr(""), strPtr("42"), strPtr("2026-11-22T10:00:00Z"),
					strPtr(`["CUSTOMER_TIER"]`), strPtr("false"), strPtr(""),
				}},
			})
		case strings.Contains(sql, "ACCESS_HISTORY"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
				}},
			})
		case strings.Contains(sql, "OBJECT_DEPENDENCIES"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
			})
		}
	})

	readers := NewReaders(c, nil, nil).WithExternalReaders(14)
	links, err := readers.ColumnLinks(context.Background(), "shop.marts.fct_orders", "customer_tier")
	if err != nil {
		t.Fatalf("expected nil column error on QUERY_HISTORY fallback, got %v", err)
	}
	if len(links) != 1 || links[0].Consumer == nil || links[0].Consumer.Tool != "Tableau" || links[0].Consumer.Queries != 42 {
		t.Fatalf("unexpected fallback links: %+v", links)
	}
	if w := readers.Warnings(); len(w) != 1 || !strings.Contains(w[0], "QUERY_HISTORY unavailable") {
		t.Errorf("expected single QUERY_HISTORY fallback warning, got %v", w)
	}
}

func TestReadersDoesNotFallbackOnNonAuthError(t *testing.T) {
	extCalls := 0
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		switch {
		case strings.Contains(sql, "/* xf:query-history */"):
			extCalls++
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "001003",
				"message": "SQL compilation error: syntax error",
			})
		case strings.Contains(sql, "ACCESS_HISTORY"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
				}},
			})
		case strings.Contains(sql, "OBJECT_DEPENDENCIES"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
			})
		}
	})

	readers := NewReaders(c, nil, nil).WithExternalReaders(14)
	_, err := readers.ColumnLinks(context.Background(), "shop.marts.fct_orders", "customer_tier")
	if err != nil {
		t.Fatalf("expected nil column error, got %v", err)
	}
	if extCalls != 1 {
		t.Errorf("extCalls = %d, want 1 (must not run fallback query on syntax error)", extCalls)
	}
	if w := readers.Warnings(); len(w) != 1 || !strings.Contains(w[0], "syntax error") {
		t.Errorf("expected real error in single warning, got %v", w)
	}
}

func TestClientCheckTagsOwnQuery(t *testing.T) {
	var gotTag, gotSQL string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotSQL, _ = req["statement"].(string)
		if params, ok := req["parameters"].(map[string]any); ok {
			gotTag, _ = params["QUERY_TAG"].(string)
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{{Name: "ID", Type: "text"}}},
		})
	})

	if _, err := c.Check(context.Background(), "SHOP", "", "SELECT id FROM shop.marts.fct_orders"); err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if !strings.HasPrefix(gotTag, "xf:") {
		t.Errorf("Check QUERY_TAG = %q, want xf:* prefix", gotTag)
	}
	if !strings.Contains(gotSQL, "/* xf:check */") || !lineage.IsOwnXFQuery("", gotSQL, nil) {
		t.Errorf("Check SQL must carry /* xf:check */ marker, got: %q", gotSQL)
	}
}
