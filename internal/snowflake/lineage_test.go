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
		case strings.Contains(sql, "ACCESS_HISTORY"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
				}},
				Data: [][]*string{
					// Projected column link (SELECT amount AS total_amt).
					{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.MARTS.FCT_REVENUE"), strPtr("TOTAL_AMT"), strPtr("AMOUNT"), strPtr("DIRECT")},
					// Filter/JOIN column read (WHERE status = 'paid'), not projected.
					{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.MARTS.FCT_REVENUE"), strPtr(""), strPtr("STATUS"), strPtr("ACCESSED")},
					// Duplicate ACCESSED row for already-projected AMOUNT (should not duplicate).
					{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.MARTS.FCT_REVENUE"), strPtr(""), strPtr("AMOUNT"), strPtr("ACCESSED")},
					// Table-only read with no column list recorded.
					{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.BI.MANUAL_EXPORT"), strPtr(""), strPtr(""), strPtr("ACCESSED")},
				},
			})
		case strings.Contains(sql, "OBJECT_DEPENDENCIES"):
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
				Data: [][]*string{
					{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.REPORTING.V_ORDERS")},
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
		t.Fatalf("ColumnLinks(amount) = %+v, err=%v", cols, err)
	}
	if cols[0].Table != "shop.marts.fct_revenue" || cols[0].Column != "TOTAL_AMT" || cols[0].DependencyType != lineage.Other {
		t.Fatalf("ColumnLinks[0] = %+v", cols[0])
	}

	// WHERE/JOIN column reads from DIRECT_OBJECTS_ACCESSED are caught too.
	whereCols, err := readers.ColumnLinks(ctx, "shop.staging.stg_orders", "status")
	if err != nil || len(whereCols) != 1 || whereCols[0].Table != "shop.marts.fct_revenue" || whereCols[0].Column != "" {
		t.Fatalf("ColumnLinks(status) = %+v, err=%v", whereCols, err)
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
			Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
			Data: [][]*string{{strPtr("SHOP.STAGING.STG_ORDERS"), strPtr("SHOP.REPORTING.V_ORDERS")}},
		})
	})
	readers := NewReaders(c, nil, nil)
	only, err := readers.TableOnlyReaders(context.Background(), "shop.staging.stg_orders", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "ACCESS_HISTORY unavailable") {
		t.Fatalf("expected partial ACCESS_HISTORY error instead of swallowing it, got err=%v", err)
	}
	if len(only) != 1 || only[0].Table != "SHOP.REPORTING.V_ORDERS" || !readers.Used() {
		t.Fatalf("fallback TableOnlyReaders = %+v, used=%v", only, readers.Used())
	}
	colLinks, colErr := readers.ColumnLinks(context.Background(), "shop.staging.stg_orders", "amount")
	if colErr == nil || len(colLinks) != 1 || colLinks[0].Table != "SHOP.REPORTING.V_ORDERS" {
		t.Fatalf("fallback ColumnLinks = %+v, err=%v", colLinks, colErr)
	}
}

func TestReadersPreloadBatchesIntoTwoQueries(t *testing.T) {
	calls := 0
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "ACCESS_HISTORY") {
			writeJSONResp(w, http.StatusOK, statementResp{
				Meta: &resultSet{RowType: []rowType{
					{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
				}},
				Data: [][]*string{
					{strPtr("SHOP.STG.ORDERS"), strPtr("SHOP.MARTS.FCT_A"), strPtr("AMT"), strPtr("AMT"), strPtr("DIRECT")},
					{strPtr("SHOP.STG.USERS"), strPtr("SHOP.MARTS.FCT_B"), strPtr("EMAIL"), strPtr("EMAIL"), strPtr("DIRECT")},
				},
			})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{{Name: "SOURCE_TABLE"}, {Name: "READER"}}},
			Data: [][]*string{
				{strPtr("SHOP.STG.ORDERS"), strPtr("SHOP.RPT.V_A")},
				{strPtr("SHOP.STG.USERS"), strPtr("SHOP.RPT.V_B")},
			},
		})
	})

	readers := NewReaders(c, nil, nil)
	ctx := context.Background()
	readers.Preload(ctx, []string{"shop.stg.orders", "shop.stg.users"})

	aCols, err := readers.ColumnLinks(ctx, "shop.stg.orders", "amt")
	if err != nil || len(aCols) != 1 || aCols[0].Table != "SHOP.MARTS.FCT_A" {
		t.Fatalf("orders ColumnLinks = %+v, err=%v", aCols, err)
	}
	bCols, err := readers.ColumnLinks(ctx, "shop.stg.users", "email")
	if err != nil || len(bCols) != 1 || bCols[0].Table != "SHOP.MARTS.FCT_B" {
		t.Fatalf("users ColumnLinks = %+v, err=%v", bCols, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 batched ACCOUNT_USAGE queries for 2 tables, got %d", calls)
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

func TestReadersPreloadCachesErrorsAndSurfacesObjectDependenciesFailure(t *testing.T) {
	// 1. Failed Preload should cache the error so subsequent per-table calls
	// don't re-run failing ACCOUNT_USAGE queries.
	failCalls := 0
	c1, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		failCalls++
		writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{"code": "002003", "message": "not authorized"})
	})
	r1 := NewReaders(c1, nil, nil)
	ctx := context.Background()
	r1.Preload(ctx, []string{"shop.stg.a", "shop.stg.b"})
	if _, err := r1.ColumnLinks(ctx, "shop.stg.a", "id"); err == nil {
		t.Fatal("expected cached preload error for a")
	}
	if _, err := r1.ColumnLinks(ctx, "shop.stg.b", "id"); err == nil {
		t.Fatal("expected cached preload error for b")
	}
	if failCalls != 2 {
		t.Fatalf("failCalls = %d, want 2 (only the single Preload batch)", failCalls)
	}

	// 2. When ACCESS_HISTORY succeeds and OBJECT_DEPENDENCIES fails, ColumnLinks
	// keeps the column links AND returns the partial error.
	c2, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sql, _ := req["statement"].(string)
		if strings.Contains(sql, "OBJECT_DEPENDENCIES") {
			writeJSONResp(w, http.StatusUnprocessableEntity, map[string]any{"code": "002003", "message": "OBJECT_DEPENDENCIES denied"})
			return
		}
		writeJSONResp(w, http.StatusOK, statementResp{
			Meta: &resultSet{RowType: []rowType{
				{Name: "SOURCE_TABLE"}, {Name: "TARGET_TABLE"}, {Name: "TARGET_COL"}, {Name: "SOURCE_COL"}, {Name: "KIND"},
			}},
			Data: [][]*string{
				{strPtr("SHOP.STG.ORDERS"), strPtr("SHOP.MARTS.FCT"), strPtr("AMT"), strPtr("AMT"), strPtr("DIRECT")},
			},
		})
	})
	r2 := NewReaders(c2, nil, nil)
	links, err := r2.ColumnLinks(ctx, "shop.stg.orders", "amt")
	if err == nil || !strings.Contains(err.Error(), "OBJECT_DEPENDENCIES") {
		t.Fatalf("expected OBJECT_DEPENDENCIES partial error, got %v", err)
	}
	if len(links) != 1 || links[0].Table != "SHOP.MARTS.FCT" || links[0].Column != "AMT" {
		t.Fatalf("expected ACCESS_HISTORY column link preserved, got %+v", links)
	}
}

func TestParseClusterByNestedCommasAndQuoteLiteralBackslash(t *testing.T) {
	got := parseClusterBy(`LINEAR(DATE_TRUNC('DAY', TS), SUBSTRING(USER_ID, 1, 5), EVENT_DATE)`)
	want := []string{"DATE_TRUNC('DAY', TS)", "SUBSTRING(USER_ID, 1, 5)", "EVENT_DATE"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("parseClusterBy = %#v, want %#v", got, want)
	}
	if q := quoteLiteral(`a\b'c`); q != `'a\\b''c'` {
		t.Fatalf("quoteLiteral = %q, want %q", q, `'a\\b''c'`)
	}
}
