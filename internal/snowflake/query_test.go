package snowflake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/warehouse"
)

func TestAsyncQueryAndPartitionPaging(t *testing.T) {
	polls := 0
	cancelled := false
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements" && r.URL.Query().Get("async") == "true":
			writeJSONResp(w, http.StatusAccepted, statementResp{Handle: "stmt-42"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/stmt-42":
			if part := r.URL.Query().Get("partition"); part == "1" {
				writeJSONResp(w, http.StatusOK, statementResp{
					Handle: "stmt-42",
					Data:   [][]*string{{strPtr("3"), strPtr("gamma")}},
				})
				return
			}
			polls++
			if polls == 1 {
				writeJSONResp(w, http.StatusAccepted, statementResp{Handle: "stmt-42"})
				return
			}
			writeJSONResp(w, http.StatusOK, statementResp{
				Handle: "stmt-42",
				Meta: &resultSet{
					NumRows:       3,
					PartitionInfo: []partitionInfo{{RowCount: 2}, {RowCount: 1}},
					RowType: []rowType{
						{Name: "ID", Type: "fixed", Precision: int64Ptr(38), Scale: int64Ptr(0)},
						{Name: "NAME", Type: "text", Length: int64Ptr(100)},
					},
				},
				Data: [][]*string{
					{strPtr("1"), strPtr("alpha")},
					{strPtr("2"), nil},
				},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
			cancelled = true
			writeJSONResp(w, http.StatusOK, map[string]any{"code": "090001"})
		}
	})

	ctx := context.Background()
	ref, err := c.StartQuery(ctx, warehouse.QueryOptions{Project: "SHOP", SQL: "SELECT id, name FROM items"})
	if err != nil || ref.ID != "stmt-42" || ref.Project != "SHOP" {
		t.Fatalf("StartQuery = (%+v, %v)", ref, err)
	}
	st1, err := c.QueryJob(ctx, ref)
	if err != nil || st1.State != "RUNNING" {
		t.Fatalf("QueryJob(1) = (%+v, %v)", st1, err)
	}
	st2, err := c.QueryJob(ctx, ref)
	if err != nil || st2.State != "DONE" || st2.Error != "" {
		t.Fatalf("QueryJob(2) = (%+v, %v)", st2, err)
	}
	p1, err := c.Rows(ctx, ref, "", 1)
	if err != nil || len(p1.Rows) != 1 || p1.Rows[0][1] != "alpha" || p1.NextToken != "0:1" || p1.Total != 3 {
		t.Fatalf("Rows(1) = %+v, err=%v", p1, err)
	}
	p2, err := c.Rows(ctx, ref, p1.NextToken, 1)
	if err != nil || len(p2.Rows) != 1 || p2.Rows[0][1] != nil || p2.NextToken != "1:0" {
		t.Fatalf("Rows(2) = %+v, err=%v", p2, err)
	}
	p3, err := c.Rows(ctx, ref, p2.NextToken, 5)
	if err != nil || len(p3.Rows) != 1 || p3.Rows[0][1] != "gamma" || p3.NextToken != "" {
		t.Fatalf("Rows(3) = %+v, err=%v", p3, err)
	}
	if err := c.CancelQuery(ctx, ref); err != nil || !cancelled {
		t.Fatalf("CancelQuery err=%v cancelled=%v", err, cancelled)
	}
}

func TestMultiStatementAndResultGone(t *testing.T) {
	var seenMulti string
	c, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if params, ok := req["parameters"].(map[string]any); ok {
				seenMulti, _ = params["MULTI_STATEMENT_COUNT"].(string)
			}
			writeJSONResp(w, http.StatusAccepted, statementResp{Handle: "multi-parent"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/multi-parent":
			writeJSONResp(w, http.StatusOK, statementResp{
				Handle:           "multi-parent",
				StatementHandles: []string{"multi-1", "multi-2"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/multi-2":
			writeJSONResp(w, http.StatusOK, statementResp{
				Handle: "multi-2",
				Meta: &resultSet{
					NumRows:       1,
					PartitionInfo: []partitionInfo{{RowCount: 1}},
					RowType:       []rowType{{Name: "N", Type: "fixed", Precision: int64Ptr(38), Scale: int64Ptr(0)}},
				},
				Data: [][]*string{{strPtr("42")}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/expired-1":
			writeJSONResp(w, http.StatusNotFound, map[string]any{"message": "Statement expired"})
		}
	})

	ctx := context.Background()
	ref, err := c.StartQuery(ctx, warehouse.QueryOptions{Project: "SHOP", SQL: "USE ROLE ANALYST; SELECT 42 AS n"})
	if err != nil {
		t.Fatal(err)
	}
	if seenMulti != "0" {
		t.Fatalf("MULTI_STATEMENT_COUNT = %q, want 0", seenMulti)
	}
	st, err := c.QueryJob(ctx, ref)
	if err != nil || st.State != "DONE" {
		t.Fatalf("QueryJob = (%+v, %v)", st, err)
	}
	page, err := c.Rows(ctx, ref, "", 10)
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0] != "42" {
		t.Fatalf("multi-statement Rows = (%+v, %v)", page, err)
	}
	_, err = c.Rows(ctx, warehouse.JobRef{Project: "SHOP", ID: "expired-1"}, "", 10)
	if !errors.Is(err, ErrResultGone) {
		t.Fatalf("expired handle err = %v, want ErrResultGone", err)
	}
}
