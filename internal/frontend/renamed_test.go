package frontend

import (
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// A file whose config's schema changed defines its action under a new
// name: the action of the old one's name follows it, else the file's main
// action.
func TestRenamedAction(t *testing.T) {
	act := func(kind project.ActionKind, schema, name, file string) *project.Action {
		tg := project.Target{Database: "p", Schema: schema, Name: name}
		return &project.Action{ID: tg.ID(), Target: tg, CanonicalTarget: tg, Kind: kind, FileName: file}
	}
	table := act(project.KindTable, "mart", "orders", "orders.sqlx")
	check := act(project.KindAssertion, "mart_assertions", "mart_orders_uniqueKey_0", "orders.sqlx")
	other := act(project.KindTable, "mart", "other", "other.sqlx")
	g := project.NewGraph([]*project.Action{check, table, other})

	for _, c := range []struct {
		id   project.ActionID
		file string
		want *project.Action
	}{
		{"p.mart_old.orders", "orders.sqlx", table},                                 // schema changed
		{"p.mart_old_assertions.mart_old_orders_uniqueKey_0", "orders.sqlx", table}, // no such name now: the main action
		{"p.mart.orders_old", "orders.sqlx", table},                                 // renamed
		{table.ID, "orders.sqlx", nil},                                              // still there
		{"p.mart_old.orders", "gone.sqlx", nil},                                     // the file defines nothing
		{"p.mart_old.orders", "", nil},
	} {
		if got := RenamedAction(g, c.id, c.file); got != c.want {
			t.Errorf("RenamedAction(%s, %s) = %v, want %v", c.id, c.file, got, c.want)
		}
	}
	if got := RenamedAction(g, "p.mart_old.mart_orders_uniqueKey_0", "orders.sqlx"); got != check {
		t.Errorf("an assertion of the same name: got %v, want %v", got, check)
	}
}
