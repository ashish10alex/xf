package ui

import (
	"reflect"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/project"
)

// tagsView is the T overlay alone.
func tagsView(m Model) string { return ansi.Strip(m.tagsBox(m.geometry().bodyH)) }

func TestTagsOverlayListsTagsUnchecked(t *testing.T) {
	fr := &fakeRunner{}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = drive(t, m, nil, press("T")...)
	if !m.tags.open {
		t.Fatal("T did not open the overlay")
	}
	// daily is a table and its two assertions, staging two tables.
	assertContains(t, tagsView(m), "T run tags", "2/2", "▸ [ ] daily", "3", "[ ] staging", "2", "no tag checked",
		"[ ] deps  [ ] dependents  [ ] full-refresh")
	assertNotContains(t, tagsView(m), "api", "[x]")
	assertContains(t, screen(m), "space check", "enter run", "esc run options")

	// Nothing runs until a tag is checked: the cursor is no selection.
	m = drive(t, m, nil, press("enter")...)
	if len(fr.calls()) != 0 || !m.tags.open {
		t.Fatal("enter ran with no tag checked")
	}
	assertContains(t, screen(m), "check a tag with space")
}

func TestTagsOverlaySearchAndCheck(t *testing.T) {
	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	// Letters are the query, the Run pane's toggles included.
	m = drive(t, m, nil, append(press("T"), typeText("dly")...)...)
	if m.flags.includeDeps {
		t.Fatal("d toggled deps in the overlay")
	}
	assertContains(t, tagsView(m), "> dly", "1/2", "▸ [ ] daily")
	assertNotContains(t, tagsView(m), "staging")

	// space checks, and the query stays.
	m = drive(t, m, nil, press("space")...)
	assertContains(t, tagsView(m), "> dly", "▸ [x] daily", "1 checked · 3 actions")

	// The check survives a filter that hides it.
	m = drive(t, m, nil, append(press("backspace", "backspace", "backspace"), typeText("stag")...)...)
	// The checked tags are named under the list, listed or not.
	assertContains(t, tagsView(m), "1/2", "▸ [ ] staging", "[x] daily", "1 checked · 3 actions")
	if len(m.tags.list) != 1 {
		t.Errorf("the filter lists %d tags, want 1", len(m.tags.list))
	}
	m = drive(t, m, nil, press("space")...)
	assertContains(t, tagsView(m), "[x] daily, staging", "2 checked · 5 actions")

	m = drive(t, m, nil, press("enter")...)
	want := project.RunOptions{Tags: []string{"daily", "staging"}}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	if m.tags.open {
		t.Error("the overlay stayed open over the run")
	}
	assertContains(t, screen(m), "$ dataform run --tags daily staging")

	// Checks are forgotten once the overlay closes.
	m = drive(t, m, nil, press("T")...)
	assertContains(t, tagsView(m), "no tag checked")
	m = drive(t, m, nil, press("esc", "esc")...)
	if m.tags.open {
		t.Error("esc twice did not close the overlay")
	}
}

func TestTagsOverlayOptionsMode(t *testing.T) {
	fa := &fakeAPIRunner{res: project.RunResult{State: dataformapi.StateSucceeded}}
	m, fr := newAPITestModel(t, fa, false)
	// esc leaves the query: the letters are the Run pane's toggles now.
	m = drive(t, m, nil, append(append(press("T"), typeText("da")...), press("space", "esc")...)...)
	if !m.tags.open || !m.tags.options {
		t.Fatal("esc did not switch to the options")
	}
	assertContains(t, tagsView(m), "> da", "[ ] d deps  [ ] D dependents  [ ] f full-refresh  [ ] a api")
	assertContains(t, screen(m), "d deps", "/ search", "esc close")
	m = drive(t, m, nil, press("d", "D", "a")...)
	if m.tags.input.Value() != "da" {
		t.Errorf("query = %q: the options' keys typed", m.tags.input.Value())
	}
	assertContains(t, tagsView(m), "[x] d deps  [x] D dependents  [ ] f full-refresh  [x] a api",
		"+ their deps and dependents", "runs the pushed branch")

	// / goes back to the query, where they are letters again.
	m = drive(t, m, nil, append(press("D", "a", "/"), typeText("d")...)...)
	if m.tags.options || m.tags.input.Value() != "dad" || !m.flags.includeDeps {
		t.Fatalf("after /: options %v, query %q, deps %v", m.tags.options, m.tags.input.Value(), m.flags.includeDeps)
	}

	// The list's keys work in both modes, and the options stay for the run.
	m = drive(t, m, nil, press("esc", "enter")...)
	want := project.RunOptions{Tags: []string{"daily"}, IncludeDeps: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	if len(fa.opts) != 0 {
		t.Error("ran through the API with a off")
	}
}

func TestTagsOverlayMoveAndUncheck(t *testing.T) {
	fr := &fakeRunner{}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = drive(t, m, nil, press("d", "T", "ctrl+n", "space")...)
	assertContains(t, tagsView(m), "▸ [x] staging", "1 checked · 2 actions + their deps", "[x] deps")
	m = drive(t, m, nil, press("space", "ctrl+p", "space", "enter")...)
	want := project.RunOptions{Tags: []string{"daily"}, IncludeDeps: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}

	// ctrl+c closes the overlay rather than quitting.
	m, _, _ = newTestModelWithRunner(t, fr, "basic.json")
	m = drive(t, m, nil, press("T")...)
	next, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd != nil || next.(Model).tags.open {
		t.Error("ctrl+c did not just close the overlay")
	}
}

func TestTagsOverlayNeedsTags(t *testing.T) {
	m, _ := newTestModel(t, "navigation.json")
	m = drive(t, m, nil, press("T")...)
	if m.tags.open {
		t.Fatal("the overlay opened with no tags to list")
	}
	assertContains(t, screen(m), "no tags in this project")
}

func TestTagRunFullRefreshAsksFirst(t *testing.T) {
	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = drive(t, m, nil, press("f", "D", "T", "space", "enter")...)
	if len(fr.calls()) != 0 || m.running {
		t.Fatal("a full refresh of a tag ran without asking")
	}
	assertContains(t, screen(m), "confirm full refresh", "tag daily", "+ their dependents", "y run full refresh")
	m = drive(t, m, nil, press("n")...)
	if len(fr.calls()) != 0 || m.confirmTags != nil {
		t.Fatal("n did not cancel the full refresh prompt")
	}
	m = drive(t, m, nil, press("T", "space", "enter", "y")...)
	want := project.RunOptions{Tags: []string{"daily"}, IncludeDependents: true, FullRefresh: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
}

func TestTagRunThroughAPI(t *testing.T) {
	fa := &fakeAPIRunner{res: project.RunResult{State: dataformapi.StateSucceeded}}
	m, fr := newAPITestModel(t, fa, true)
	m = drive(t, m, nil, press("d", "T")...)
	assertContains(t, tagsView(m), "[x] api", "runs the pushed branch")
	m = drive(t, m, nil, press("space", "enter")...)
	want := []dataformapi.Options{{
		Repo:        dataformapi.Repository{Project: "demo-project", Location: "EU"},
		JobLocation: "EU",
		Invocation:  dataformapi.InvocationConfig{IncludedTags: []string{"daily"}, TransitiveDependenciesIncluded: true},
	}}
	if !reflect.DeepEqual(fa.opts, want) {
		t.Errorf("API runs = %+v, want %+v", fa.opts, want)
	}
	if len(fr.calls()) != 0 {
		t.Error("ran dataform locally too")
	}
	assertContains(t, screen(m), "Dataform API run --tags daily --include-deps")
}

func TestTagsInPaletteAndHelp(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	pm := drive(t, m, nil, append(append(press("/"), typeText(">run tags")...), press("enter")...)...)
	if !pm.tags.open {
		t.Error("the Run tags command did not open the overlay")
	}
	assertContains(t, screen(drive(t, m, nil, press("?")...)), "run tags", "check/uncheck tag", "run checked tags")
}

func TestCheckedTagsFit(t *testing.T) {
	s := newStyles(true)
	for _, tt := range []struct {
		tags  []string
		width int
		want  string
	}{
		{nil, 40, ""},
		{[]string{"daily"}, 40, "[x] daily"},
		{[]string{"daily", "hourly", "weekly"}, 40, "[x] daily, hourly, weekly"},
		{[]string{"daily", "hourly", "weekly_long"}, 26, "[x] daily, hourly +1 more"},
		{[]string{"daily", "hourly", "weekly"}, 12, "[x] daily +2 more"},
	} {
		if got := ansi.Strip(checkedTags(s, tt.tags, tt.width)); got != tt.want {
			t.Errorf("%v in %d = %q, want %q", tt.tags, tt.width, got, tt.want)
		}
	}
}
