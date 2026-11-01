package project

import (
	"slices"
	"testing"
)

// An external action is shown alone, with its own tests, and never as
// another action's sibling.
func TestSiblingsOfExternalActions(t *testing.T) {
	act := func(kind ActionKind, name, file string, deps ...string) *Action {
		tg := Target{Schema: "s", Name: name}
		a := &Action{ID: tg.ID(), Target: tg, CanonicalTarget: tg, Kind: kind, FileName: file}
		for _, d := range deps {
			a.DependencyTargets = append(a.DependencyTargets, Target{Schema: "s", Name: d})
		}
		return a
	}
	g := NewGraph([]*Action{
		// A declarations file that also publishes a table.
		act(KindDeclaration, "customers", "definitions/sources.js"),
		act(KindDeclaration, "orders", "definitions/sources.js"),
		act(KindTable, "summary", "definitions/sources.js", "orders"),
		act(KindAssertion, "summary_rows", "definitions/sources.js", "summary"),
		// dbt sources and their tests, all in one YAML file.
		act(KindSource, "raw_customers", "models/_sources.yml"),
		act(KindSource, "raw_orders", "models/_sources.yml"),
		act(KindTest, "not_null_raw_customers_id", "models/_sources.yml", "raw_customers"),
		act(KindTest, "not_null_raw_orders_id", "models/_sources.yml", "raw_orders"),
		// Reads a source, but its home is the model.
		act(KindTable, "stg_orders", "models/stg_orders.sql", "raw_orders"),
		act(KindTest, "relationships_stg_orders", "models/_sources.yml", "stg_orders", "raw_customers"),
		// Reads two sources, so it has no home.
		act(KindTest, "same_ids", "models/_sources.yml", "raw_customers", "raw_orders"),
	})
	names := func(actions []*Action) []string {
		var out []string
		for _, a := range actions {
			out = append(out, a.Target.Name)
		}
		return out
	}
	tests := []struct {
		of   string
		want []string
	}{
		{"customers", []string{"customers"}},
		{"orders", []string{"orders"}},
		{"summary", []string{"summary", "summary_rows"}},
		{"summary_rows", []string{"summary", "summary_rows"}},
		{"raw_customers", []string{"raw_customers", "not_null_raw_customers_id"}},
		{"not_null_raw_customers_id", []string{"raw_customers", "not_null_raw_customers_id"}},
		{"raw_orders", []string{"raw_orders", "not_null_raw_orders_id"}},
		{"relationships_stg_orders", []string{"stg_orders", "relationships_stg_orders"}},
		{"same_ids", []string{"not_null_raw_customers_id", "not_null_raw_orders_id", "relationships_stg_orders", "same_ids"}},
	}
	for _, tt := range tests {
		a := g.Actions[Target{Schema: "s", Name: tt.of}.ID()]
		if got := names(g.Siblings(a)); !slices.Equal(got, tt.want) {
			t.Errorf("siblings of %s = %q\nwant %q", tt.of, got, tt.want)
		}
	}
}
