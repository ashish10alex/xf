package frontend

import (
	"slices"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

// Pane 2's list puts the tables read from prod (or that can't be) first,
// then those read from dev, each in dependency order.
func TestDeferHintsListProdFirst(t *testing.T) {
	tgt := func(name string) project.Target { return project.Target{Database: "dev", Schema: "s", Name: name} }
	built1, read1, decl, read2, built2 := tgt("built1"), tgt("read1"), tgt("decl"), tgt("read2"), tgt("built2")
	a := &project.Action{ID: tgt("a").ID(), Target: tgt("a"), Kind: project.KindTable,
		DependencyTargets: []project.Target{built1, read1, decl, read2, built2}}
	g := project.NewGraph([]*project.Action{a, {ID: decl.ID(), Target: decl, Kind: project.KindDeclaration}})
	prod := project.Target{Database: "prod", Schema: "s", Name: "read"}
	d := &deferral.Deferral{
		Entries: []deferral.Entry{
			{Dev: read1, Prod: &prod, Status: deferral.Deferred},
			{Dev: read2, Prod: &prod, Status: deferral.Unreadable},
		},
		BuiltInDev: []deferral.BuiltInDev{{Dev: built1}, {Dev: built2}},
	}
	order, _ := DeferHints(g, a, d, time.Now())
	want := []project.ActionID{read1.ID(), read2.ID(), built1.ID(), decl.ID(), built2.ID()}
	if !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
}
