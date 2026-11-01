package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

// scriptedEngine sends no states of its own: the test delivers them as
// engineMsgs. It records the actions Focus was asked for.
type scriptedEngine struct{ focused []project.ActionID }

func (*scriptedEngine) Subscribe() (<-chan engine.State, func()) { return nil, func() {} }
func (*scriptedEngine) Recompile(string)                         {}
func (e *scriptedEngine) Focus(id project.ActionID)              { e.focused = append(e.focused, id) }
func (e *scriptedEngine) SetTheme(string)                        {}

// dbtExample is the dbt example's compile, with the SQL of every action
// that has some in state st.
func dbtExample(t *testing.T, st project.SQLState) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := (&dbt.Backend{}).Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range g.Actions {
		if a.Query != "" {
			a.SQL = st
		}
	}
	return g
}

// A dbt project shows as soon as it is parsed, on the action the engine
// compiles first; each action's SQL is dry-run once it is compiled, and
// the action the user moves to is the next one asked for.
func TestStagedGraphShowsAndAsksForTheSelectedAction(t *testing.T) {
	const customers, orders = project.ActionID("alex-personal-dev-01.xf_example.stg_customers"), project.ActionID("alex-personal-dev-01.xf_example.stg_orders")
	bq := &fakeBigQuery{}
	eng := &scriptedEngine{}
	m := New(Deps{Backend: "dbt", Root: "/work/shop", Engine: eng, DryRun: bq.dryRun, Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 160, Height: 50})

	staged := dbtExample(t, project.SQLRaw)
	st := engine.State{}.Begin("", testNow()).Stage(staged, customers)
	m = drive(t, m, nil, engineMsg{st})
	if m.selected == nil || m.selected.ID != customers {
		t.Fatalf("selected %+v, want the action being compiled first", m.selected)
	}
	assertContains(t, screen(m), "xf_example.stg_customers", "· compiling…", "dry run      compiling…")
	if n := bq.callCount(); n != 0 {
		t.Fatalf("%d dry runs of templates: %q", n, bq.calls)
	}
	if !slices.Equal(eng.focused, []project.ActionID{customers}) {
		t.Fatalf("asked the engine for %v", eng.focused)
	}

	// Its compiled SQL arrives: only it is dry-run.
	focused := dbtExample(t, project.SQLRaw)
	focused.Actions[customers].SQL = project.SQLCompiled
	st.Graph, st.Focus = focused, ""
	m = drive(t, m, nil, engineMsg{st})
	assertNotContains(t, screen(m), "· compiling…")
	if n := bq.callCount(); n != 1 {
		t.Fatalf("%d dry runs, want the compiled action's alone: %q", n, bq.calls)
	}

	// Moving to another action asks for it, once.
	m = startOn(t, m, orders)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 160, Height: 50}, tea.WindowSizeMsg{Width: 160, Height: 50})
	if !slices.Equal(eng.focused, []project.ActionID{customers, orders}) {
		t.Fatalf("asked the engine for %v", eng.focused)
	}

	// The compile finishes: nothing is marked any more.
	whole := dbtExample(t, project.SQLCompiled)
	m = drive(t, m, nil, engineMsg{st.Finish(whole, nil, 0, testNow())})
	assertNotContains(t, screen(m), "compiling…")
}

// After a save the other actions keep their last SQL, marked outdated, and
// aren't dry-run until the compile replaces it.
func TestStagedGraphMarksOutdatedSQL(t *testing.T) {
	bq := &fakeBigQuery{}
	compile := func(context.Context) (*project.Graph, error) { return dbtExample(t, project.SQLCompiled), nil }
	fe := newFakeEngine(compile)
	m := New(Deps{Backend: "dbt", Root: "/work/shop", Engine: fe, DryRun: bq.dryRun, Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 160, Height: 50})
	m = startOn(t, m, "alex-personal-dev-01.xf_example.fct_orders")

	st := fe.state.Begin(engine.ReasonFileChanged, testNow()).Stage(dbtExample(t, project.SQLOutdated), "")
	before := bq.callCount()
	m = drive(t, m, nil, engineMsg{st})
	assertContains(t, screen(m), "· outdated, compiling…")
	if n := bq.callCount(); n != before {
		t.Errorf("%d dry runs of outdated SQL: %q", n-before, bq.calls)
	}
}
