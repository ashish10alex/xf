package snowflake

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

func TestReadersLineage(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		switch {
		case strings.Contains(sql, "mc.value:directSources"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}}},
				Data: [][]*string{
					{strPtr("SHOP.MARTS.FCT_REVENUE"), strPtr("TOTAL_AMT"), strPtr("AMOUNT")},
				},
			})
		case strings.Contains(sql, "DIRECT_OBJECTS_ACCESSED"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "TARGET_TABLE"}}},
				Data: [][]*string{
					{strPtr("SHOP.MARTS.FCT_REVENUE")},
					{strPtr("SHOP.BI.MANUAL_EXPORT")},
				},
			})
		case strings.Contains(sql, "OBJECT_DEPENDENCIES"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "READER"}}},
				Data: [][]*string{
					{strPtr("SHOP.REPORTING.V_ORDERS")},
				},
			})
		}
	})

	g := project.NewGraph([]*project.Action{
		{ID: "shop.marts.fct_revenue", Target: project.Target{Database: "shop", Schema: "marts", Name: "fct_revenue"}, Kind: project.KindTable},
	})
	readers := NewReaders(c, g, nil)
	ctx := context.Background()

	cols, err := readers.ColumnLinks(ctx, "shop.staging.stg_orders", "amount")
	if err != nil || len(cols) != 1 {
		t.Fatalf("ColumnLinks = %+v, err=%v", cols, err)
	}
	if cols[0].Table != "shop.marts.fct_revenue" || cols[0].Column != "TOTAL_AMT" || cols[0].DependencyType != lineage.Other {
		t.Fatalf("ColumnLinks[0] = %+v", cols[0])
	}

	// TableOnlyReaders returns readers without column-level tracking (MANUAL_EXPORT and V_ORDERS),
	// excluding FCT_REVENUE which has column-level lineage tracked.
	only, err := readers.TableOnlyReaders(ctx, "shop.staging.stg_orders", map[string]bool{})
	if err != nil || len(only) != 2 {
		t.Fatalf("TableOnlyReaders = %+v, err=%v", only, err)
	}

	all, err := readers.TableReaders(ctx, "shop.staging.stg_orders")
	if err != nil || len(all) != 3 {
		t.Fatalf("TableReaders = %+v, err=%v", all, err)
	}
}

func TestReadersFallbackWithoutAccessHistory(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "ACCESS_HISTORY") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "002003",
				"message": "View 'SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY' does not exist or not authorized.",
			})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{{Name: "READER"}}},
			Data: [][]*string{{strPtr("SHOP.REPORTING.V_ORDERS")}},
		})
	})
	readers := NewReaders(c, nil, nil)
	only, err := readers.TableOnlyReaders(context.Background(), "shop.staging.stg_orders", map[string]bool{})
	if err != nil || len(only) != 1 || only[0].Table != "SHOP.REPORTING.V_ORDERS" || !readers.Used() {
		t.Fatalf("fallback TableOnlyReaders = %+v, err=%v, used=%v", only, err, readers.Used())
	}
}

func TestReadersAccessHistoryDeniedWithNoViewsErrors(t *testing.T) {
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "ACCESS_HISTORY") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{
				"code":    "002003",
				"message": "View 'SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY' does not exist or not authorized.",
			})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{{Name: "READER"}}},
			Data: nil,
		})
	})
	readers := NewReaders(c, nil, nil)
	if _, err := readers.ColumnLinks(context.Background(), "shop.staging.stg_orders", "amount"); err == nil {
		t.Fatal("expected error when ACCESS_HISTORY fails and OBJECT_DEPENDENCIES returns no readers")
	}
}

func TestSplitFullQuotedDots(t *testing.T) {
	db, sch, tbl := splitFull(`"my.db"."my.sch"."my.tbl"`)
	if db != "my.db" || sch != "my.sch" || tbl != "my.tbl" {
		t.Fatalf("splitFull = (%q, %q, %q)", db, sch, tbl)
	}
}
