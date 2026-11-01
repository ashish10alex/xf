package changes

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// These cases are vscode-dataform-tools' src/test/suite/compiledGraphDiff.test.ts.

func target(name string) map[string]any {
	return map[string]any{"database": "proj", "schema": "ds", "name": name}
}

func table(name string, overrides map[string]any) map[string]any {
	t := map[string]any{"type": "table", "fileName": "definitions/" + name + ".sqlx", "target": target(name), "query": "select 1 as " + name, "tags": []any{}}
	for k, v := range overrides {
		t[k] = v
	}
	return t
}

func graph(t *testing.T, parts map[string][]map[string]any) []byte {
	t.Helper()
	g := map[string]any{"tables": []any{}, "operations": []any{}, "assertions": []any{}, "notebooks": []any{}, "declarations": []any{}}
	for k, v := range parts {
		g[k] = v
	}
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func compare(t *testing.T, base, head []byte) Diff {
	t.Helper()
	d, err := Compare(base, head)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type idReasons struct {
	id      project.ActionID
	reasons []Reason
}

func changedReasons(d Diff) []idReasons {
	var out []idReasons
	for _, c := range d.Changed {
		out = append(out, idReasons{c.ID, c.Reasons})
	}
	return out
}

func TestCompareSameGraph(t *testing.T) {
	g := graph(t, map[string][]map[string]any{"tables": {table("orders", nil)}})
	if d := compare(t, g, g); len(d.Changed) != 0 || len(d.Deleted) != 0 {
		t.Errorf("got %+v, want no changes", d)
	}
}

func TestCompareNewSQLAndDeleted(t *testing.T) {
	base := graph(t, map[string][]map[string]any{"tables": {table("orders", nil), table("legacy", nil)}})
	head := graph(t, map[string][]map[string]any{"tables": {table("orders", map[string]any{"query": "select 2"}), table("customers", nil)}})
	d := compare(t, base, head)
	want := []idReasons{{"proj.ds.customers", []Reason{ReasonNew}}, {"proj.ds.orders", []Reason{ReasonSQL}}}
	if got := changedReasons(d); !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
	if len(d.Deleted) != 1 || d.Deleted[0].ID != "proj.ds.legacy" {
		t.Errorf("deleted = %v, want proj.ds.legacy", d.Deleted)
	}
	if c := d.Changed[0]; c.Target != (project.Target{Database: "proj", Schema: "ds", Name: "customers"}) || c.FileName != "definitions/customers.sqlx" || c.Type != "table" {
		t.Errorf("changed[0] = %+v", c)
	}
}

func TestCompareConfigButNotDocsOrTags(t *testing.T) {
	base := graph(t, map[string][]map[string]any{"tables": {table("orders", nil), table("customers", nil)}})
	head := graph(t, map[string][]map[string]any{"tables": {
		table("orders", map[string]any{"bigquery": map[string]any{"partitionBy": "DATE(ts)"}}),
		table("customers", map[string]any{
			"tags":              []any{"daily"},
			"actionDescriptor":  map[string]any{"description": "All customers"},
			"dependencyTargets": []any{target("x")},
		}),
	}})
	want := []idReasons{{"proj.ds.orders", []Reason{ReasonConfig}}}
	if got := changedReasons(compare(t, base, head)); !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestCompareOmittedDefaults(t *testing.T) {
	base := graph(t, map[string][]map[string]any{"tables": {table("orders", nil)}})
	head := graph(t, map[string][]map[string]any{"tables": {table("orders", map[string]any{"preOps": []any{}, "protected": false, "bigquery": map[string]any{}})}})
	if d := compare(t, base, head); len(d.Changed) != 0 {
		t.Errorf("changed = %v, want none", d.Changed)
	}
}

func TestCompareOperationsAssertionsNotebooksNotDeclarations(t *testing.T) {
	op := map[string]any{"fileName": "definitions/op.sqlx", "target": target("op"), "queries": []any{"delete from x"}}
	assertion := map[string]any{"fileName": "definitions/a.sqlx", "target": target("a"), "query": "select 1"}
	notebook := map[string]any{"fileName": "definitions/n.ipynb", "target": target("n"), "notebookContents": "{}"}
	declaration := map[string]any{"fileName": "definitions/src.sqlx", "target": target("src")}
	with := func(m map[string]any, k string, v any) map[string]any {
		out := map[string]any{}
		for kk, vv := range m {
			out[kk] = vv
		}
		out[k] = v
		return out
	}
	base := graph(t, map[string][]map[string]any{"operations": {op}, "assertions": {assertion}, "notebooks": {notebook}})
	head := graph(t, map[string][]map[string]any{
		"operations":   {with(op, "queries", []any{"delete from y"})},
		"assertions":   {with(assertion, "query", "select 2")},
		"notebooks":    {notebook},
		"declarations": {declaration},
	})
	var got [][2]string
	for _, c := range compare(t, base, head).Changed {
		got = append(got, [2]string{string(c.ID), c.Type})
	}
	want := [][2]string{{"proj.ds.a", "assertion"}, {"proj.ds.op", "operations"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestCompareRealCompileAgainstItself(t *testing.T) {
	for _, name := range []string{"basic.json", "incremental.json"} {
		data := readFixture(t, name)
		if d := compare(t, data, data); len(d.Changed) != 0 || len(d.Deleted) != 0 {
			t.Errorf("%s: got %+v, want no changes", name, d)
		}
	}
}

func TestCompareKeepsGeneratedAssertionsParent(t *testing.T) {
	gen := map[string]any{"fileName": "definitions/orders.sqlx", "target": target("ds_orders_assertions_uniqueKey_0"), "parentAction": target("orders"), "query": "select 1"}
	own := map[string]any{"fileName": "definitions/check.sqlx", "target": target("check"), "query": "select 1"}
	base := graph(t, map[string][]map[string]any{"tables": {table("orders", nil)}, "assertions": {gen, own}})
	head := graph(t, map[string][]map[string]any{"tables": {table("orders", nil)}})
	var parents []project.ActionID
	for _, d := range compare(t, base, head).Deleted {
		parents = append(parents, d.Parent)
	}
	// Sorted by file: check.sqlx, then orders.sqlx.
	if want := []project.ActionID{"", "proj.ds.orders"}; !reflect.DeepEqual(parents, want) {
		t.Errorf("deleted parents = %v, want %v", parents, want)
	}
	d := compare(t, head, base)
	if len(d.Changed) != 2 || d.Changed[1].Parent != "proj.ds.orders" || d.Changed[0].Parent != "" {
		t.Errorf("changed = %+v", d.Changed)
	}
}

func TestReasonsText(t *testing.T) {
	c := Changed{Reasons: []Reason{ReasonSQL, ReasonConfig}}
	if got := c.ReasonsText(); got != "sql, config" {
		t.Errorf("got %q", got)
	}
}

// Dataform on Windows names files with backslashes: what changed is named
// with forward slashes, as everywhere else in xf.
func TestCompareNamesFilesWithSlashes(t *testing.T) {
	win := map[string]any{"fileName": `definitions\staging\orders.sqlx`}
	base := graph(t, map[string][]map[string]any{"tables": {table("orders", win)}})
	win["query"] = "select 2"
	head := graph(t, map[string][]map[string]any{"tables": {table("orders", win)}})
	d := compare(t, base, head)
	if len(d.Changed) != 1 || d.Changed[0].FileName != "definitions/staging/orders.sqlx" {
		t.Errorf("changed = %+v", d.Changed)
	}
}
