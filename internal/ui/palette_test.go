package ui

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/commanduse"
	"github.com/ashish10alex/xf/internal/project"
)

// query opens the palette and types q.
func query(t *testing.T, m Model, q string) Model {
	t.Helper()
	return drive(t, m, nil, append(press("/"), typeText(q)...)...)
}

// listedKinds is the kinds of the actions the palette lists.
func listedKinds(m Model) map[project.ActionKind]int {
	kinds := map[project.ActionKind]int{}
	for _, r := range m.palette.list {
		kinds[r.item.Action.Kind]++
	}
	return kinds
}

func dbtModel(t *testing.T, fr *fakeRunner) Model {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	compile := func(context.Context) (*project.Graph, error) { return (&dbt.Backend{}).Parse(data) }
	m := New(Deps{Backend: "dbt", Root: "/work/shop", Engine: newFakeEngine(compile), StartRun: fr.start, Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	return closePalette(t, m)
}

// TestPaletteKindFilter: a "$" word keeps one kind, the other words still
// match names, and several "$" words add up.
func TestPaletteKindFilter(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	all := map[project.ActionKind]int{}
	for _, a := range m.graph.Actions {
		all[a.Kind]++
	}
	if all[project.KindView] == 0 || all[project.KindTable] == 0 || all[project.KindAssertion] == 0 {
		t.Fatalf("fixture kinds = %v", all)
	}

	for q, want := range map[string]map[project.ActionKind]int{
		"$view":        {project.KindView: all[project.KindView]},
		"$VIEW":        {project.KindView: all[project.KindView]},
		"$asrt":        {project.KindAssertion: all[project.KindAssertion]}, // the abbreviation
		"$a":           {project.KindAssertion: all[project.KindAssertion]}, // the start of a name
		"$table $view": {project.KindTable: all[project.KindTable], project.KindView: all[project.KindView]},
	} {
		if got := listedKinds(query(t, m, q)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q lists %v, want %v", q, got, want)
		}
	}

	// The name words narrow the kind's actions, wherever the "$" word is.
	for _, q := range []string{"$view stg_orders", "stg_orders $view"} {
		pm := query(t, m, q)
		if len(pm.palette.list) != 1 || pm.palette.list[0].item.Action.ID != "demo-project.staging.stg_orders" {
			t.Errorf("%q lists %d actions", q, len(pm.palette.list))
		}
	}
	if pm := query(t, m, "$table stg_orders"); len(pm.palette.list) != 0 {
		t.Errorf("$table lists the view stg_orders")
	}

	// dbt's kinds aren't Dataform's: the palette says which there are, as
	// it does for "$" alone.
	view := paletteView(query(t, m, "$snapshot"))
	assertContains(t, view, "no kind $snapshot, only $table $view $incremental", "$assertion", "$declaration")
	assertNotContains(t, view, "$seed", "$test", "$model")
	assertContains(t, paletteView(query(t, m, "$")), "kinds: $table $view")
	assertContains(t, screen(query(t, m, "")), "$kind find one kind")
}

func TestPaletteKindFilterDbt(t *testing.T) {
	m := dbtModel(t, &fakeRunner{})
	for q, want := range map[string][]project.ActionKind{
		"$snapshot": {project.KindSnapshot},
		"$seed":     {project.KindSeed},
		"$test":     {project.KindTest},
		"$s":        {project.KindSeed, project.KindSnapshot, project.KindSource},
		"$model":    {project.KindTable, project.KindView, project.KindIncremental, project.KindMaterializedView, project.KindEphemeral},
	} {
		got := listedKinds(query(t, m, q))
		if len(got) != len(want) {
			t.Errorf("%q lists %v, want %v", q, got, want)
		}
		for _, k := range want {
			if got[k] == 0 {
				t.Errorf("%q lists no %s: %v", q, k, got)
			}
		}
	}
	view := paletteView(query(t, m, "$assertion"))
	assertContains(t, view, "no kind $assertion, only $model $table", "$snapshot", "$test", "$exposure")
}

// TestPaletteCommands: ">" lists commands, with the keys that do the same,
// and enter does the highlighted one.
func TestPaletteCommands(t *testing.T) {
	fr := &fakeRunner{}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")

	pm := query(t, m, ">")
	view := paletteView(pm)
	// The action is named once, in the title; the query's ">" is the prompt.
	assertContains(t, view, "─commands · analytics.daily_orders─", "│ >", "│ ▸ Run  ", "Run with dependencies  ",
		"Run option: full refresh", "off  f ", "Check: compile")
	assertNotContains(t, view, "> >")
	// What this xf can't do isn't offered, nor what doesn't apply now.
	for _, c := range pm.palette.commands {
		switch c.title {
		case "Open: in editor", "Run changed actions", "Run: cancel", "Go to: log", "View: incremental SQL":
			t.Errorf("offers %q", c.title)
		}
	}
	decl := query(t, startOn(t, m, "raw-project.shop.customers"), ">run")
	// A run of tags doesn't depend on the selected action.
	if c := decl.palette.selectedCommand(); c == nil || c.title != "Run tags" {
		t.Errorf("first run command on a declaration = %+v", c)
	}

	// Words match in any order; the key is shown beside the title.
	pm = query(t, m, ">lineage view")
	assertContains(t, paletteView(pm), "1/", "View: lineage", "off  O ")
	open := m.lineageOpen
	pm = drive(t, pm, nil, press("enter")...)
	if pm.palette.open || pm.lineageOpen == open {
		t.Errorf("enter: palette open %v, lineage open %v", pm.palette.open, pm.lineageOpen)
	}

	// A run with dependents sets what the run includes, as d and D do.
	pm = query(t, m, ">run dependents")
	if c := pm.palette.selectedCommand(); c == nil || c.title != "Run with dependents" {
		t.Fatalf("first match = %+v", c)
	}
	pm = drive(t, pm, nil, press("enter")...)
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.analytics.daily_orders",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0"}, IncludeDependents: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}

	// The full refresh option applies to the runs, which still ask first.
	pm = drive(t, query(t, m, ">full refresh"), nil, press("enter")...)
	assertContains(t, paletteView(query(t, pm, ">full refresh")), "on   f ")
	pm = drive(t, query(t, pm, ">run"), nil, press("enter")...)
	if pm.confirmFull == nil || len(fr.calls()) != 1 {
		t.Errorf("full refresh ran without asking")
	}

	// Deleting the ">" lists actions again.
	pm = query(t, m, ">")
	pm = drive(t, pm, nil, press("backspace")...)
	assertContains(t, paletteView(pm), "─find action", "10/10")
	assertContains(t, screen(pm), "> commands")
}

func TestPaletteCommandsClick(t *testing.T) {
	m, fc := newTestModel(t, "basic.json")
	m = query(t, m, ">compile")
	b := m.palette.box(m.width, m.geometry().bodyH)
	before := fc.callCount()
	m = drive(t, m, nil, tea.MouseClickMsg{X: b.x + 4, Y: b.y + 2, Button: tea.MouseLeft})
	if m.palette.open || fc.callCount() != before+1 {
		t.Errorf("click: palette open %v, compiles %d → %d", m.palette.open, before, fc.callCount())
	}
}

func TestPaletteListsMostUsedCommandsFirst(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	uses := commanduse.Open(filepath.Join(t.TempDir(), "commands.json"))
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow, CommandUses: uses})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40}))
	first := func(m Model, q string) string {
		t.Helper()
		pm := query(t, m, q)
		if c := pm.palette.selectedCommand(); c != nil {
			return c.title
		}
		return ""
	}
	if got := first(m, ">"); got != "Check: compile" {
		t.Fatalf("first command before any use = %q", got)
	}
	// Done twice from the palette, the lineage view comes first, and
	// first of what a query matches; once, the zoom comes after it.
	for _, q := range []string{">lineage view", ">lineage view", ">zoom pane"} {
		m = drive(t, query(t, m, q), nil, press("enter")...)
	}
	if got := first(m, ">"); got != "View: lineage" {
		t.Errorf("first command = %q, want View: lineage", got)
	}
	if got := first(m, ">view"); got != "View: lineage" {
		t.Errorf("first of >view = %q, want View: lineage", got)
	}
	pm := query(t, m, ">")
	if got := pm.palette.list[1].command.title; got != "View: zoom pane" {
		t.Errorf("second command = %q, want View: zoom pane", got)
	}
	if n := uses.Load()["View: lineage"].Count; n != 2 {
		t.Errorf("View: lineage counted %d times, want 2", n)
	}
}

func TestThemeCommandCyclesAndSaves(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	var saved []string
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow,
		SaveTheme: func(s string) error { saved = append(saved, s); return nil }})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40}))
	if !m.styles.isDark {
		t.Fatal("auto starts dark before the terminal answers")
	}
	run := func() {
		for _, c := range m.commands() {
			if strings.HasPrefix(c.title, "View: theme") {
				m = drive(t, m, c.run(&m))
				return
			}
		}
		t.Fatal("no theme command")
	}
	run() // auto -> light
	if m.theme != "light" || m.styles.isDark {
		t.Errorf("theme %q dark %v", m.theme, m.styles.isDark)
	}
	// A dark terminal doesn't override a forced light theme.
	m = drive(t, m, nil, tea.BackgroundColorMsg{Color: color.Black})
	if m.styles.isDark {
		t.Error("the terminal's background beat the forced theme")
	}
	run() // light -> dark
	run() // dark -> auto, which follows the terminal again
	if m.theme != "auto" || !m.styles.isDark || strings.Join(saved, " ") != "light dark auto" {
		t.Errorf("theme %q dark %v saved %q", m.theme, m.styles.isDark, saved)
	}
	// Another client's choice arrives as engine state.
	m.deps.Engine.SetTheme("light")
	m = drive(t, m, nil)
	if m.theme != "light" || m.styles.isDark {
		t.Errorf("another client's theme: %q dark %v", m.theme, m.styles.isDark)
	}
}
