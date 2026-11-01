package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// helpView is the help overlay alone, stripped of styling.
func helpView(m Model) string {
	return ansi.Strip(m.helpBox())
}

func TestHelpScrollsWhenClipped(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 35}, keyMsg("?"))
	view := helpView(m)
	assertContains(t, view, "global", "more ─╯")
	assertNotContains(t, view, "backspace")

	m = drive(t, m, nil, press("G")...)
	view = helpView(m)
	assertContains(t, view, "4 log", "0 sql (vim keys)", "search word under cursor", "↑")
	assertNotContains(t, view, "global")

	m = drive(t, m, nil, press("g")...)
	assertContains(t, helpView(m), "global")

	// ctrl+d pages down, the wheel scrolls, and scrolling stops at the end.
	m = drive(t, m, nil, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	assertNotContains(t, helpView(m), "find action")
	for range 20 {
		m = drive(t, m, nil, wheel(10, 10, true))
	}
	bottom := helpView(m)
	m = drive(t, m, nil, press("j")...)
	if helpView(m) != bottom {
		t.Error("j scrolled past the end")
	}
	assertContains(t, bottom, "other end")
}

func TestHelpStackedColumnsAreSeparated(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 80}, keyMsg("?"))
	lines := strings.Split(helpView(m), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "│move") {
			if strings.Trim(lines[i-1], "│ ") != "" {
				t.Errorf("no blank line above move: %q", lines[i-1])
			}
			return
		}
	}
	t.Errorf("no move section:\n%s", strings.Join(lines, "\n"))
}

func TestHelpSearch(t *testing.T) {
	m, fc := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("?", "/")...)
	m = drive(t, m, nil, typeText("lineage")...)
	assertContains(t, screen(m), "/lineage")
	// Nothing is filtered out: the matches are highlighted where they are.
	view := helpView(m)
	assertContains(t, view, "help · /lineage · 1 of 3", "show/hide lineage", "2 lineage", "compile", "global", "move")
	assertContains(t, m.helpBox(), m.styles.matchCurrent.Render("lineage"), m.styles.match.Render("lineage"))

	// enter keeps the search and goes back to scrolling; keys are not typed.
	m = drive(t, m, nil, press("enter", "c")...)
	if fc.callCount() != 1 {
		t.Error("c recompiled while help was open")
	}
	assertContains(t, helpView(m), "help · /lineage · 1 of 3")
	assertContains(t, screen(m), "clear search", "next/prev match")

	// n and N go through the matches, around the ends.
	m = drive(t, m, nil, press("n")...)
	assertContains(t, helpView(m), "2 of 3")
	m = drive(t, m, nil, press("N", "N")...)
	assertContains(t, helpView(m), "3 of 3")
	m = drive(t, m, nil, press("n")...)
	assertContains(t, helpView(m), "1 of 3")

	// esc clears the search, and a second esc closes the overlay.
	m = drive(t, m, nil, press("esc")...)
	assertContains(t, helpView(m), "help · esc closes", "compile")
	assertNotContains(t, m.helpBox(), m.styles.match.Render("lineage"))
	m = drive(t, m, nil, press("esc")...)
	assertNotContains(t, screen(m), "help · esc closes")
}

func TestHelpSearchScrollsToMatches(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 35}, keyMsg("?"))
	assertNotContains(t, helpView(m), "other end")

	// The view follows the query as it is typed, and n wraps to the top.
	m = drive(t, m, nil, press("/")...)
	m = drive(t, m, nil, typeText("other end")...)
	assertContains(t, helpView(m), "other end", "1 of 1")
	assertNotContains(t, helpView(m), "global")

	// esc while typing drops the query and goes back to where it started.
	m = drive(t, m, nil, press("esc")...)
	assertContains(t, helpView(m), "help · esc closes", "global")

	m = drive(t, m, nil, press("/")...)
	m = drive(t, m, nil, typeText("page")...)
	m = drive(t, m, nil, press("enter")...)
	assertContains(t, helpView(m), "page down", "1 of 2")
	m = drive(t, m, nil, press("n")...)
	assertContains(t, helpView(m), "2 of 2")
	m = drive(t, m, nil, press("n")...)
	assertContains(t, helpView(m), "1 of 2")
}

func TestHelpSearchNoMatch(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("?", "/")...)
	m = drive(t, m, nil, typeText("zzz")...)
	assertContains(t, helpView(m), "help · /zzz · no match", "global")
	m = drive(t, m, nil, press("enter", "n")...)
	assertContains(t, helpView(m), "no match")
}
