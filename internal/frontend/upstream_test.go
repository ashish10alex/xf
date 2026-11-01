package frontend

import (
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// The walk goes upstream breadth first to the depth asked, through an
// action with no table, leaving out assertions; an edge is kept to a
// node seen before.
func TestUpstream(t *testing.T) {
	tg := func(name string) project.Target { return project.Target{Database: "p", Schema: "d_dev", Name: name} }
	act := func(kind project.ActionKind, name string, deps ...string) *project.Action {
		a := &project.Action{ID: tg(name).ID(), Target: tg(name), CanonicalTarget: project.Target{Database: "p", Schema: "d", Name: name}, Kind: kind}
		for _, d := range deps {
			a.DependencyTargets = append(a.DependencyTargets, tg(d))
		}
		return a
	}
	g := project.NewGraph([]*project.Action{
		act(project.KindTable, "orders", "stg", "eph", "check"),
		act(project.KindView, "stg", "raw"),
		act(project.KindEphemeral, "eph", "raw", "src"),
		act(project.KindAssertion, "check"),
		act(project.KindDeclaration, "raw"),
		act(project.KindTable, "src", "deeper"),
		act(project.KindTable, "deeper"),
	})
	nodes, edges := Upstream(g, tg("orders").ID(), 2)
	var got []string
	for _, n := range nodes {
		got = append(got, n.Name+":"+string(rune('0'+n.Depth))+map[bool]string{true: "", false: "-"}[n.Table])
	}
	if want := []string{"orders:0", "stg:1", "eph:1-", "raw:2", "src:2"}; !slices.Equal(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	if len(edges) != 5 { // orders→stg, orders→eph, stg→raw, eph→raw, eph→src
		t.Errorf("edges = %v, want 5", edges)
	}
	if nodes[0].Key != "d.orders" {
		t.Errorf("key = %q, want the canonical target's d.orders", nodes[0].Key)
	}
	if n, _ := Upstream(g, "p.d_dev.none", 3); n != nil {
		t.Errorf("a model not in the graph: %v", n)
	}
}

func TestPresetKey(t *testing.T) {
	model := &project.Action{Target: project.Target{Schema: "dbt_alex", Name: "orders"}, DbtName: &project.DbtName{Package: "shop", Name: "orders", Version: "2"}}
	src := &project.Action{Target: project.Target{Schema: "raw", Name: "o"}, DbtName: &project.DbtName{Source: "erp", Name: "orders"}}
	for a, want := range map[*project.Action]string{model: "shop.orders.v2", src: "source:erp.orders"} {
		if got := PresetKey(a); got != want {
			t.Errorf("PresetKey = %q, want %q", got, want)
		}
	}
}
