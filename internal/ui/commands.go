package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/ashish10alex/xf/internal/commanduse"
)

// command is something the palette can do: what a key does, findable by
// name. Related commands share the start of their title ("View: …").
type command struct {
	title string
	// key is the key that does the same, shown beside the title; "" when
	// no single key does.
	key string
	// state is "on" or "off" for a command that switches something, shown
	// beside the key; "" for the rest.
	state string
	run   func(m *Model) tea.Cmd
}

// commands lists what the palette's ">" mode offers, for the project and
// the selected action as they are now: the commands the keys have, and a
// run of the selected action's file with each choice of dependencies and
// dependents (full refresh and the Dataform API are run options, which
// apply to all of them). Like the help overlay, it leaves out what this xf
// can't do; it also leaves out what doesn't apply now, such as a run of a
// declaration.
func (m Model) commands() []command {
	k := m.keys
	var out []command
	add := func(title string, b key.Binding, run func(m *Model) tea.Cmd) {
		out = append(out, command{title: title, key: b.Help().Key, run: run})
	}
	toggle := func(title string, on bool, b key.Binding, f func(m *Model)) {
		state := "off"
		if on {
			state = "on"
		}
		out = append(out, command{title: title, key: b.Help().Key, state: state, run: do(f)})
	}
	none := key.Binding{}
	a := m.selected

	if m.canRun() && len(m.runActions()) > 0 && !m.running {
		// runWith sets what the run includes besides the action, then
		// runs as r does. The choice stays, as the Run pane shows.
		runWith := func(deps, dependents bool) func(m *Model) tea.Cmd {
			return func(m *Model) tea.Cmd {
				m.flags.includeDeps, m.flags.includeDependents = deps, dependents
				return m.startRun()
			}
		}
		add("Run", k.Run, (*Model).startRun)
		add("Run with dependencies", none, runWith(true, false))
		add("Run with dependents", none, runWith(false, true))
		add("Run with dependencies and dependents", none, runWith(true, true))
	}
	if m.running {
		add("Run: cancel", k.Cancel, do((*Model).cancelOrKillRun))
	}
	if m.deps.ChangedActions != nil {
		add("Run changed actions", k.RunChanged, (*Model).openChanged)
	}
	if m.canRun() {
		add("Run tags", k.RunTags, (*Model).openTags)
	}
	if m.canRun() {
		toggle("Run option: full refresh", m.flags.fullRefresh, k.FullRefresh,
			func(m *Model) { m.flags.fullRefresh = !m.flags.fullRefresh })
		if m.deps.StartAPIRun != nil {
			toggle("Run option: via Dataform API", m.flags.api, k.API, (*Model).toggleAPI)
		}
		toggle("Run option: include dependencies", m.flags.includeDeps, k.Deps,
			func(m *Model) { m.flags.includeDeps = !m.flags.includeDeps })
		toggle("Run option: include dependents", m.flags.includeDependents, k.Dependents,
			func(m *Model) { m.flags.includeDependents = !m.flags.includeDependents })
		toggle("Run option: selected only", m.flags.selectedOnly, k.SelectedOnly,
			func(m *Model) { m.flags.selectedOnly = !m.flags.selectedOnly })
	}

	if m.hasTabs() && len(m.history.entries) > 0 {
		add("History: clear kept runs", k.ClearHistory, do((*Model).askClearHistory))
	}

	add("Check: compile", k.Compile, do(func(m *Model) { m.deps.Engine.Recompile("") }))
	add("Check: show errors", k.Errors, do((*Model).showErrorsOrSay))
	if m.deps.ColumnImpact != nil {
		add("Check: column impact of changes", k.Impact, (*Model).openImpact)
	}
	if m.deps.QueryCost != nil {
		add("Check: query cost of changes", k.Cost, (*Model).openCost)
	}

	if a != nil {
		if m.deps.Open != nil && a.FileName != "" {
			add("Open: in editor", k.Open, (*Model).openInEditor)
		}
		if m.deps.OpenURL != nil && hasTable(a) {
			add("Open: in BigQuery", k.OpenTable, func(m *Model) tea.Cmd { return m.openTableURL(m.selected) })
		}
		add("Copy: `table` name", k.CopyTarget, func(m *Model) tea.Cmd { return m.copyTarget(m.selected) })
		if a.FileName != "" {
			add("Copy: file path", k.CopyPath, func(m *Model) tea.Cmd { return m.copyPath(m.selected) })
		}
	}
	if m.deps.OpenURL != nil && m.lastRunURL() != "" {
		add("Open: run in browser", k.OpenURL, (*Model).openRunURL)
	}

	if a != nil && a.HasIncrementalVariant() {
		toggle("View: incremental SQL", m.incremental, k.Incremental, (*Model).toggleIncremental)
	}
	toggle("View: columns", m.cols.shown, k.Columns, (*Model).toggleColumns)
	toggle("View: log", m.showLog, k.Log, (*Model).toggleLog)
	toggle("View: lineage", m.lineageOpen, k.ToggleLineage, (*Model).toggleLineage)
	toggle("View: zoom pane", m.zoomed, k.Zoom, func(m *Model) { m.zoomed = !m.zoomed })
	if m.deps.FullScreen != nil {
		add("View: full screen in nvim", k.FullScreen, (*Model).toggleFullScreen)
	}
	if m.deps.Editor != nil {
		toggle("View: follow editor", m.following, k.Follow, (*Model).toggleFollow)
	}

	out = append(out, command{title: "View: theme (auto, light, dark)", state: m.theme, run: do((*Model).cycleTheme)})

	if m.deps.Info != nil {
		add("View: project info", k.Info, (*Model).openInfo)
	}
	if m.deps.Branches != nil {
		add("Git: switch branch", k.Branch, (*Model).openBranches)
	}

	if a != nil {
		search := k.Search
		search.SetHelp("z /", "")
		add("SQL: search", search, (*Model).searchSQLFromPalette)
	}
	add("Go to: another action", k.Find, (*Model).openPalette)
	if len(m.paneActions()) > 1 {
		add("Go to: next action in file", k.NextInFile, do(func(m *Model) { m.cycleInFile(1) }))
		add("Go to: previous action in file", k.PrevInFile, do(func(m *Model) { m.cycleInFile(-1) }))
	}
	add("Go to: sql", k.FocusSQL, do(func(m *Model) { m.setFocus(paneSQL) }))
	add("Go to: actions", k.FocusActions, do(func(m *Model) { m.setFocus(paneActions) }))
	add("Go to: lineage", k.FocusLineage, do(func(m *Model) { m.setFocus(paneLineage) }))
	if m.canRun() {
		add("Go to: run", k.FocusRun, do(func(m *Model) { m.setFocus(paneRun) }))
	}
	if m.showLog {
		add("Go to: log", k.FocusLog, do((*Model).focusLog))
	}
	add("Help: every key", k.Help, do(func(m *Model) { m.keyHelp.show() }))
	add("Quit", k.Quit, (*Model).quit)
	return out
}

// byUse puts the most used commands first (see commanduse.Compare); the
// rest keep their order.
func byUse(cmds []command, uses map[string]commanduse.Use) {
	slices.SortStableFunc(cmds, func(a, b command) int { return commanduse.Compare(uses[a.title], uses[b.title]) })
}

// do makes a command of something that needs no tea.Cmd.
func do(f func(m *Model)) func(m *Model) tea.Cmd {
	return func(m *Model) tea.Cmd { f(m); return nil }
}

// matchCommands lists the commands whose title holds every word of the
// query (lower case), in any order: those holding each word as written
// first, then those with a word's letters scattered, each in the given
// order.
func matchCommands(cmds []command, terms []string) []paletteMatch {
	var exact, loose []paletteMatch
next:
	for i := range cmds {
		title := strings.ToLower(cmds[i].title)
		written := true
		for _, t := range terms {
			if strings.Contains(title, t) {
				continue
			}
			if len(fuzzy.Find(t, []string{title})) == 0 {
				continue next
			}
			written = false
		}
		if written {
			exact = append(exact, paletteMatch{command: &cmds[i]})
		} else {
			loose = append(loose, paletteMatch{command: &cmds[i]})
		}
	}
	return append(exact, loose...)
}
