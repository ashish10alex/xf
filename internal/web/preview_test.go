package web

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

func TestPreviewQuery(t *testing.T) {
	inc := &project.Action{Kind: project.KindIncremental, Query: "SELECT full", PreOps: []string{"DECLARE d DATE"},
		IncrementalQuery: "SELECT inc", IncrementalPreOps: []string{"DELETE FROM t WHERE TRUE"}, PostOps: []string{"DROP TABLE x"}}
	if sql, pre, why := previewQuery(inc, false); why != "" || sql != "DECLARE d DATE;\nSELECT full;" || len(pre) != 1 {
		t.Errorf("full = %q %v %q", sql, pre, why)
	}
	if sql, _, _ := previewQuery(inc, true); sql != "DELETE FROM t WHERE TRUE;\nSELECT inc;" {
		t.Errorf("incremental = %q, want its own pre_operations and query, and no post_operations", sql)
	}
	asrt := &project.Action{Kind: project.KindAssertion, Query: "SELECT 1", PreOps: []string{"DELETE FROM t WHERE TRUE"}}
	if sql, _, _ := previewQuery(asrt, false); sql != "SELECT 1" {
		t.Errorf("assertion = %q, want its query alone", sql)
	}
	for _, k := range []project.ActionKind{project.KindOperation, project.KindDeclaration, project.KindNotebook} {
		if _, _, why := previewQuery(&project.Action{Kind: k, Query: "SELECT 1"}, false); why == "" {
			t.Errorf("%s: want a reason it isn't previewed", k)
		}
	}
}

func TestUnsafePreOps(t *testing.T) {
	safe := []string{"DECLARE d DATE DEFAULT CURRENT_DATE();\nSET d = '2024-01-01'", "-- a note\ncreate temp function f(x INT64) AS (x)", "/* x */ SETTINGS"}
	if got := unsafePreOps(safe[:2]); len(got) != 0 {
		t.Errorf("safe = %v, want none", got)
	}
	got := unsafePreOps([]string{"DECLARE x INT64; delete from `p.d.t` where day >= x", safe[2]})
	if !slices.Equal(got, []string{"DELETE FROM `P.D.T`", "SETTINGS"}) {
		t.Errorf("unsafe = %v", got)
	}
}

func TestPreviewEndpoint(t *testing.T) {
	g := testGraph()
	big := project.Target{Database: "p", Schema: "mart", Name: "big"}
	inc := project.Target{Database: "p", Schema: "mart", Name: "inc"}
	op := project.Target{Database: "p", Schema: "mart", Name: "op"}
	g = project.NewGraph(append(slices.Collect(func(yield func(*project.Action) bool) {
		for _, a := range g.Actions {
			if !yield(a) {
				return
			}
		}
	}),
		&project.Action{ID: big.ID(), Target: big, Kind: project.KindTable, FileName: "definitions/big.sqlx", Query: "SELECT BIG"},
		&project.Action{ID: inc.ID(), Target: inc, Kind: project.KindIncremental, FileName: "definitions/inc.sqlx", Query: "SELECT 2",
			IncrementalQuery: "SELECT 3", IncrementalPreOps: []string{"DELETE FROM t WHERE TRUE"}},
		&project.Action{ID: op.ID(), Target: op, Kind: project.KindOperation, FileName: "definitions/op.sqlx", Queries: []string{"SELECT 1"}},
	))
	bq, calls := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{Engine: newFakeEngine(g)}, bq)
	preview := func(body map[string]any) (previewView, int) {
		var v previewView
		code := postStatus(t, c, ts.URL+"/api/preview", body, &v)
		return v, code
	}

	v, code := preview(map[string]any{"id": "p.mart.orders"})
	if code != 200 || v.Job == nil || v.Job.Project != "p" || v.SQL != "SELECT id\nFROM `p.raw.orders`" || v.Name != "mart.orders" || len(v.Ask) > 0 {
		t.Errorf("orders = %d %+v", code, v)
	}
	if !strings.Contains(strings.Join(*calls, "\n"), `"xf-preview":"true"`) {
		t.Errorf("the job has no preview label: %v", *calls)
	}

	// A big scan, or a pre_operation that may change data, is asked about
	// first; force starts it.
	if v, _ := preview(map[string]any{"id": "p.mart.big"}); v.Job != nil || len(v.Ask) != 1 || !strings.Contains(v.Ask[0], "56.00 GiB") || !v.Danger {
		t.Errorf("big = %+v", v)
	}
	if v, _ := preview(map[string]any{"id": "p.mart.big", "force": true}); v.Job == nil {
		t.Errorf("forced big = %+v, want a job", v)
	}
	if v, _ := preview(map[string]any{"id": "p.mart.inc"}); v.Job == nil || v.SQL != "SELECT 2" {
		t.Errorf("full inc = %+v", v)
	}
	if v, _ := preview(map[string]any{"id": "p.mart.inc", "incremental": true}); v.Job != nil || len(v.Ask) != 1 || !strings.Contains(v.Ask[0], "DELETE FROM T") {
		t.Errorf("incremental inc = %+v", v)
	}

	// An operation, or an action it doesn't know, is refused with why.
	var e errorView
	if code := postStatus(t, c, ts.URL+"/api/preview", map[string]any{"id": "p.mart.op"}, &e); code != 422 || !strings.Contains(e.Error, "operation") {
		t.Errorf("operation = %d %+v", code, e)
	}
	if code := postStatus(t, c, ts.URL+"/api/preview", map[string]any{"id": "p.mart.nope"}, &e); code != 422 {
		t.Errorf("unknown = %d %+v", code, e)
	}
}
