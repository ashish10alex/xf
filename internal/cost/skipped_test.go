package cost

import (
	"reflect"
	"testing"
)

// Compiled with prodCompilerOptions, a missing table is named in prod's
// project, the branch's actions in the project's own.
func TestSkipsMatchTheNameTheDryRunsUsed(t *testing.T) {
	r := &Report{Actions: []Action{
		{Name: "dev.marts.a_new", DryRunAs: "prod.marts.a_new", Type: "table", New: true, Reason: "BigQuery gave no estimate"},
		{Name: "dev.marts.b", DryRunAs: "prod.marts.b", Type: "table",
			Reason: "dry run failed: Not found: Table prod:marts.a_new was not found in location EU"},
	}}
	g := Skips(r)
	if len(g) != 2 || g[1].Actions[0].Action.Name != "dev.marts.b" || g[1].Actions[0].Tag() != "new on this branch" {
		t.Errorf("got %+v", g)
	}
}

func TestSkipsGroupByWhy(t *testing.T) {
	notFound := func(table string) string {
		return "dry run failed: Not found: Table acme:" + table + " was not found in location EU"
	}
	r := &Report{Actions: []Action{
		{Name: "acme.marts.orders", Type: "table", Estimate: &Estimate{Bytes: 1}},
		{Name: "acme.marts.a_new", Type: "table", New: true, Reason: "BigQuery gave no estimate"},
		{Name: "acme.marts.b", Type: "table", Reason: notFound("marts.a_new")},
		{Name: "acme.checks.c", Type: "assertion", Reason: notFound("raw.gone")},
		{Name: "acme.marts.d", Type: "view", Reason: notFound("marts.orders")},
		{Name: "acme.marts.e", Type: "operations", Reason: "dry run failed: Syntax error\nat [1:1]"},
	}}
	type row struct {
		name, missing string
		tag           string
	}
	type group struct {
		why, hint string
		rows      []row
	}
	var got []group
	for _, g := range Skips(r) {
		gr := group{why: g.Why, hint: g.Hint}
		for _, s := range g.Actions {
			gr.rows = append(gr.rows, row{s.Action.Name, s.Missing, s.Tag()})
		}
		got = append(got, gr)
	}
	hint := "A dry run needs every table it reads to exist. A table this branch adds exists only once a run builds it."
	want := []group{
		{"Reads a table not in BigQuery (location EU)", hint, []row{
			{"acme.marts.b", "acme.marts.a_new", "new on this branch"},
			{"acme.checks.c", "acme.raw.gone", ""},
			{"acme.marts.d", "acme.marts.orders", "changed on this branch"},
		}},
		{"BigQuery gave no estimate", "", []row{{"acme.marts.a_new", "", ""}}},
		{"dry run failed: Syntax error", "", []row{{"acme.marts.e", "", ""}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}
