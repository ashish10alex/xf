package frontend

import (
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// The declarations table lists a file of two or more external actions,
// whatever tests it also defines, and no other file.
func TestExternalsInFile(t *testing.T) {
	act := func(kind project.ActionKind, name, file string) *project.Action {
		tg := target("raw", name)
		return &project.Action{ID: tg.ID(), Target: tg, CanonicalTarget: tg, Kind: kind, FileName: file}
	}
	a := act(project.KindDeclaration, "a", "sources.js")
	b := act(project.KindDeclaration, "b", "sources.js")
	lone := act(project.KindDeclaration, "lone", "lone.sqlx")
	mixedDecl := act(project.KindDeclaration, "m", "mixed.js")
	mixedTable := act(project.KindTable, "t", "mixed.js")
	src1 := act(project.KindSource, "orders", "sources.yml")
	src2 := act(project.KindSource, "customers", "sources.yml")
	test := act(project.KindTest, "not_null_orders_id", "sources.yml")
	g := project.NewGraph([]*project.Action{a, b, lone, mixedDecl, mixedTable, src1, src2, test})

	for _, c := range []struct {
		of   *project.Action
		want int
	}{
		{a, 2},
		{lone, 0},       // one declaration: shown alone
		{mixedDecl, 0},  // a file that also builds a table
		{mixedTable, 0}, // not external
		{src1, 2},       // the test in sources.yml isn't listed
		{test, 0},
	} {
		if got := ExternalsInFile(g, c.of); len(got) != c.want {
			t.Errorf("%s: got %d actions, want %d", c.of.ID, len(got), c.want)
		}
	}
}
