package impact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
)

func TestJSON(t *testing.T) {
	r := &Report{
		Comparison: &Comparison{HeadRef: "feat/x", BaseRef: "origin/main", MergeBase: "52863aa0c1", HeadLabel: "working tree"},
		AtRisk: []Table{{Table: "p.marts.orders", Type: "table", Columns: []Column{{
			Column: "discount", Change: ColumnChange{Kind: Dropped},
			Readers: []Reader{{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy}},
		}, {
			Column: "total", Change: ColumnChange{Kind: TypeChanged, From: "INT64", To: "STRING"},
			Readers: []Reader{{Table: "p.marts.daily", Column: "total", DependencyType: lineage.ExactCopy, ChangedOnBranch: true, ProbablyUpdated: true}},
		}}}, {Table: "p.marts.legacy", Type: "table", Deleted: true}},
	}
	data, err := JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != float64(JSONVersion) || got["worst"] != "critical" {
		t.Errorf("version %v, worst %v", got["version"], got["worst"])
	}
	if c := got["comparison"].(map[string]any); c["headRef"] != "feat/x" {
		t.Errorf("comparison %v", c)
	}
	tables := got["atRisk"].([]any)
	if len(tables) != 2 {
		t.Fatalf("atRisk %v", tables)
	}
	orders := tables[0].(map[string]any)
	cols := orders["columns"].([]any)
	drop, retype := cols[0].(map[string]any), cols[1].(map[string]any)
	if orders["table"] != "p.marts.orders" || orders["severity"] != "critical" {
		t.Errorf("table %v", orders)
	}
	if drop["column"] != "discount" || drop["change"].(map[string]any)["kind"] != Dropped || drop["severity"] != "critical" {
		t.Errorf("dropped column %v", drop)
	}
	// Retyped, but its only reader is updated on this branch.
	if retype["column"] != "total" || retype["severity"] != "low" {
		t.Errorf("retyped column %v", retype)
	}
	// A deleted table nobody reads.
	if gone := tables[1].(map[string]any); gone["deleted"] != true || gone["severity"] != "low" || len(gone["columns"].([]any)) != 0 {
		t.Errorf("deleted table %v", gone)
	}
	// Lists with nothing in them are empty, not null.
	for _, k := range []string{"safe", "new", "unchecked"} {
		if l, ok := got[k].([]any); !ok || len(l) != 0 {
			t.Errorf("%s = %v, want []", k, got[k])
		}
	}

	// A column nobody reads has no readers, not null ones.
	data, _ = JSON(&Report{AtRisk: []Table{{Table: "p.a", Type: "table", Columns: []Column{{Column: "c", Change: ColumnChange{Kind: Dropped}}}}}})
	if strings.Contains(string(data), "null") {
		t.Errorf("null in %s", data)
	}

	// Nothing at risk: no worst.
	data, _ = JSON(&Report{})
	got = nil
	_ = json.Unmarshal(data, &got)
	if _, ok := got["worst"]; ok {
		t.Errorf("worst with nothing at risk: %s", data)
	}
}
