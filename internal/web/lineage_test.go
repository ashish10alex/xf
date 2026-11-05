package web

import (
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// The Lineage tab lists views, then tables, then the rest, with assertions
// last; within a kind the graph's order stays.
func TestByLineageKind(t *testing.T) {
	act := func(name string, k project.ActionKind) *project.Action {
		return &project.Action{ID: project.ActionID(name), Kind: k}
	}
	got := byLineageKind([]*project.Action{
		act("decl", project.KindDeclaration),
		act("t1", project.KindTable),
		act("check", project.KindAssertion),
		act("op", project.KindOperation),
		act("inc", project.KindIncremental),
		act("t2", project.KindTable),
		act("v1", project.KindView),
		act("v2", project.KindView),
	})
	want := []string{"v1", "v2", "t1", "t2", "inc", "op", "decl", "check"}
	for i, a := range got {
		if string(a.ID) != want[i] {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	}
}

func ids(as []*project.Action) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a.ID)
	}
	return out
}
