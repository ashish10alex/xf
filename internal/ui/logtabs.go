package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/runhistory"
)

// With the project's kept runs (Deps.Runs) the log pane has three tabs,
// which t goes round: the log, the BigQuery jobs of the last run, and the
// history of the runs xf started here, each of which opens to its jobs.

type logTab int

const (
	tabLog logTab = iota
	tabJobs
	tabHistory
)

// historyView is the history tab: the kept runs, or the jobs of the one
// that is open.
type historyView struct {
	entries []runhistory.Entry
	err     string
	cursor  int
	offset  int // first run shown
	// open is the run whose jobs are shown, in table; nil shows the runs.
	open  *runhistory.Entry
	table runTable
	// refreshing are the runs being looked up (see RunHistory.Refresh).
	refreshing map[string]bool
}

type (
	historyLoadedMsg struct {
		entries []runhistory.Entry
		err     error
	}
	runRefreshedMsg   struct{ entry runhistory.Entry }
	historyClearedMsg struct{ err error }
	// jobsTickMsg has the log pane look at the run's report again: its
	// running jobs come and go between its lines.
	jobsTickMsg struct{}
)

const jobsTickInterval = time.Second

// jobsTick schedules the next look at a run's report. Tests replace it so
// drive() never waits on the timer.
var jobsTick = func() tea.Cmd {
	return tea.Tick(jobsTickInterval, func(time.Time) tea.Msg { return jobsTickMsg{} })
}

// hasTabs says the log pane has its jobs and history tabs.
func (m Model) hasTabs() bool { return m.deps.Runs != nil }

// loadHistory reads the kept runs.
func (m Model) loadHistory() tea.Cmd {
	runs := m.deps.Runs
	if runs == nil {
		return nil
	}
	return func() tea.Msg {
		entries, err := runs.List()
		return historyLoadedMsg{entries, err}
	}
}

// setHistory takes the kept runs as read. Until something is run here, the
// jobs tab shows the last of them.
func (m *Model) setHistory(msg historyLoadedMsg) tea.Cmd {
	h := &m.history
	h.entries, h.err = msg.entries, ""
	if msg.err != nil {
		h.err = msg.err.Error()
	}
	h.cursor = min(h.cursor, max(len(h.entries)-1, 0))
	var cmds []tea.Cmd
	if t := &m.runTable; !t.ran && len(h.entries) > 0 {
		e := h.entries[0]
		t.kept, t.report = &e, e.Report
		t.cursor = 0
		cmds = append(cmds, m.refreshIfStale(0))
	}
	if h.open != nil {
		if i := slices.IndexFunc(h.entries, func(e runhistory.Entry) bool { return e.ID == h.open.ID }); i >= 0 {
			m.showKept(h.entries[i])
		}
	}
	return tea.Batch(cmds...)
}

// refreshIfStale looks up the i'th kept run if it was never seen to end
// and no xf follows it, here (the run going here now is the newest) or in
// another window.
func (m *Model) refreshIfStale(i int) tea.Cmd {
	h := &m.history
	e := h.entries[i]
	if !e.Stale(m.deps.Now()) || (m.running && i == 0) || h.refreshing[e.ID] {
		return nil
	}
	if h.refreshing == nil {
		h.refreshing = map[string]bool{}
	}
	h.refreshing[e.ID] = true
	runs := m.deps.Runs
	return func() tea.Msg {
		got, _ := runs.Refresh(context.Background(), e)
		return runRefreshedMsg{got}
	}
}

// setRefreshed takes a kept run as looked up.
func (m *Model) setRefreshed(e runhistory.Entry) {
	h := &m.history
	if i := slices.IndexFunc(h.entries, func(x runhistory.Entry) bool { return x.ID == e.ID }); i >= 0 {
		h.entries[i] = e
	}
	if h.open != nil && h.open.ID == e.ID {
		m.showKept(e)
	}
	if t := &m.runTable; t.kept != nil && t.kept.ID == e.ID {
		t.kept, t.report = &e, e.Report
	}
}

// showKept opens e in the history tab, to its jobs.
func (m *Model) showKept(e runhistory.Entry) {
	h := &m.history
	h.open, h.table.report = &e, e.Report
	if e.Report != nil {
		h.table.cursor = min(h.table.cursor, max(len(e.Report.Actions)-1, 0))
	}
}

// shownTable is the table of jobs the pane shows now, nil when it shows
// none.
func (m *Model) shownTable() *runTable {
	switch {
	case !m.hasTabs():
		if m.runTable.shown() {
			return &m.runTable
		}
	case m.logTab == tabJobs && m.runTable.has():
		return &m.runTable
	case m.logTab == tabHistory && m.history.open != nil && m.history.table.has():
		return &m.history.table
	}
	return nil
}

// jobCount is how many BigQuery jobs the jobs tab has.
func (m Model) jobCount() int {
	n := 0
	if r := m.runTable.report; r != nil {
		for _, a := range r.Actions {
			if a.JobID != "" {
				n++
			}
		}
	}
	return n
}

// tabsTitle is the log pane's title with its tabs: the one shown is in
// brackets, and the jobs tab says how many jobs it has.
func (m Model) tabsTitle() string {
	jobs := "jobs"
	if n := m.jobCount(); n > 0 {
		jobs = fmt.Sprintf("jobs (%d)", n)
	}
	names := []string{"log", jobs, "history"}
	names[m.logTab] = "[" + names[m.logTab] + "]"
	return "4 " + strings.Join(names, " │ ")
}

// keptTitle says which kept run a table of jobs is of: "cli · 6 Oct 17:40 ·
// ✓ succeeded".
func (m Model) keptTitle(e *runhistory.Entry) string { return frontend.KeptTitle(m.deps.Now(), e) }

// when is a time short enough for a column: the time of day today, with
// the day before that.
func (m Model) when(t time.Time) string { return frontend.When(m.deps.Now(), t) }

// tabsBody is what the log pane shows under its tabs.
func (m Model) tabsBody(width int, focused bool) string {
	s, h := m.styles, m.log.height
	switch m.logTab {
	case tabJobs:
		if !m.runTable.has() {
			if m.running {
				return s.dim.Render(" no BigQuery jobs yet: each is listed as it starts")
			}
			return s.dim.Render(" no BigQuery jobs: nothing has been run here")
		}
		var now time.Time
		if m.running && m.runTable.kept == nil {
			now = m.deps.Now()
		}
		return m.runTable.view(s, width, h, focused, now, m.jobsFindView(&m.runTable))
	case tabHistory:
		hv := m.history
		switch {
		case hv.open != nil && hv.table.has():
			return hv.table.view(s, width, h, focused, time.Time{}, m.jobsFindView(&hv.table))
		case hv.open != nil:
			return s.dim.Render(" this run had no actions: backspace goes back")
		case hv.err != "":
			return s.errorText.Render(" could not read the kept runs: " + hv.err)
		case len(hv.entries) == 0:
			return s.dim.Render(" no runs kept yet: the runs xf starts here are listed")
		}
		return m.historyList(width, h, focused)
	}
	return strings.Join(m.log.visible(), "\n")
}

// The history's columns after the command, which takes the room they leave.
const (
	whenColW = 12
	viaColW  = 3
	jobsColW = 4
	minCmdW  = 12
)

// historyList renders the kept runs in width×height cells: when each ran,
// how it was started, its jobs, how it went, how long it took and what its
// jobs cost.
func (m Model) historyList(width, height int, focused bool) string {
	s, hv := m.styles, m.history
	cmd := true
	fixed := whenColW + viaColW + jobsColW + stateColW + durationColW + billedColW + costColW
	if width-fixed-8*2*cellPad < minCmdW {
		cmd = false
	}
	cols := []table.Column{{Title: "When", Width: whenColW}, {Title: "Via", Width: viaColW},
		{Title: padLeft("Jobs", jobsColW), Width: jobsColW}, {Title: "State", Width: stateColW},
		{Title: padLeft("Duration", durationColW), Width: durationColW},
		{Title: padLeft("Billed", billedColW), Width: billedColW}, {Title: padLeft("Est. cost", costColW), Width: costColW}}
	if cmd {
		cols = append(cols, table.Column{Title: "Command", Width: width - fixed - 8*2*cellPad})
	}
	rows := max(height-1, 1)
	off := max(min(hv.offset, hv.cursor, max(len(hv.entries)-rows, 0)), hv.cursor-rows+1, 0)
	end := min(off+rows, len(hv.entries))
	cells := make([]table.Row, 0, end-off)
	for i := off; i < end; i++ {
		e := hv.entries[i]
		state := actionState(e.State)
		if !(focused && i == hv.cursor) {
			state = s.stateText(state)
		}
		var duration, billed, cost string
		if d, ok := e.Duration(); ok {
			duration = formatDuration(d)
		}
		if e.Report != nil {
			if t := e.Report.Totals(); t.Jobs > 0 {
				billed, cost = formatBytes(t.BytesBilled), formatCost(t.CostUSD)
			}
		}
		row := table.Row{m.when(e.Started), e.Via, padLeft(fmt.Sprint(e.Jobs()), jobsColW), state,
			padLeft(duration, durationColW), padLeft(billed, billedColW), padLeft(cost, costColW)}
		if cmd {
			row = append(row, e.Cmdline)
		}
		cells = append(cells, row)
	}
	st := table.Styles{
		Header:   s.dim.Bold(true).Padding(0, cellPad),
		Cell:     lipgloss.NewStyle().Padding(0, cellPad),
		Selected: lipgloss.NewStyle(),
	}
	if focused {
		st.Selected = s.selected
	}
	tb := table.New(table.WithColumns(cols), table.WithRows(cells), table.WithStyles(st),
		table.WithWidth(width), table.WithHeight(len(cells)+1))
	tb.SetCursor(hv.cursor - off)
	return tb.View()
}

// moveHistory moves the history's cursor n runs, scrolling to keep it in
// view.
func (m *Model) moveHistory(n int) {
	h := &m.history
	if len(h.entries) == 0 {
		return
	}
	rows := max(m.log.height-1, 1)
	h.cursor = min(max(h.cursor+n, 0), len(h.entries)-1)
	h.offset = max(min(h.offset, h.cursor, max(len(h.entries)-rows, 0)), h.cursor-rows+1, 0)
}

// tabsFooter is the note in the log pane's border: where t goes next.
func (m Model) tabsFooter() string {
	switch m.logTab {
	case tabJobs:
		return "t history"
	case tabHistory:
		return "t log"
	}
	if n := m.jobCount(); n > 0 {
		return fmt.Sprintf("t jobs (%d)", n)
	}
	return "t jobs"
}

// nextTab shows the log pane's next tab.
func (m *Model) nextTab() tea.Cmd {
	m.logTab = (m.logTab + 1) % (tabHistory + 1)
	// What t chose stays for later runs: an API run's table shows itself
	// unless the log was chosen over it.
	m.runTable.logInstead = m.logTab == tabLog
	m.history.open = nil
	return m.loadHistory()
}

// handleTabsKey handles a key in the log pane that is its tabs': t shows
// the next, and over a table or the history the move keys move the cursor,
// enter opens what is under it and u opens its page in the browser. ok is
// false for a key that is the log's.
func (m *Model) handleTabsKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	k := m.keys
	if key.Matches(msg, k.RunTable) {
		return m.nextTab(), true
	}
	if m.logTab == tabLog {
		return nil, false
	}
	h := &m.history
	if m.logTab == tabHistory && key.Matches(msg, k.Back) && h.open != nil {
		h.open = nil
		return nil, true
	}
	if t := m.shownTable(); t != nil {
		switch {
		case key.Matches(msg, k.OpenJob):
			return m.openJobOf(t), true
		case key.Matches(msg, k.OpenURL):
			if t.report.URL != "" {
				return m.openRun(t.report.URL), true
			}
			return m.openJobOf(t), true
		}
		return nil, t.handleMove(msg, k, m.log.height)
	}
	if m.logTab == tabHistory && key.Matches(msg, k.ClearHistory) {
		m.askClearHistory()
		return nil, true
	}
	if m.logTab != tabHistory || h.open != nil || len(h.entries) == 0 {
		return nil, true // nothing under these tabs to move in
	}
	page := max((m.log.height-1)/2, 1)
	switch {
	case key.Matches(msg, k.Accept):
		// The run going here is the jobs tab's, where it is followed.
		if m.running && h.cursor == 0 && h.entries[0].State == "RUNNING" {
			m.logTab = tabJobs
			return nil, true
		}
		h.table = runTable{}
		m.showKept(h.entries[h.cursor])
		return m.refreshIfStale(h.cursor), true
	case key.Matches(msg, k.OpenURL):
		if e := h.entries[h.cursor]; e.Report != nil && e.Report.URL != "" {
			return m.openRun(e.Report.URL), true
		}
		m.flash = "a CLI run has no page of its own: enter lists its jobs"
	case key.Matches(msg, k.Up):
		m.moveHistory(-1)
	case key.Matches(msg, k.Down):
		m.moveHistory(1)
	case key.Matches(msg, k.HalfDown):
		m.moveHistory(page)
	case key.Matches(msg, k.HalfUp):
		m.moveHistory(-page)
	case key.Matches(msg, k.Top):
		m.moveHistory(-len(h.entries))
	case key.Matches(msg, k.Bottom):
		m.moveHistory(len(h.entries))
	}
	return nil, true
}

// askClearHistory asks whether to forget the kept runs; y does (see
// clearHistory).
func (m *Model) askClearHistory() {
	switch {
	case m.deps.Runs == nil:
		m.flash = "this project keeps no history of its runs"
	case len(m.history.entries) == 0:
		m.flash = "no runs are kept: there is nothing to clear"
	default:
		m.confirmClear = true
	}
}

// clearHistory forgets the kept runs.
func (m *Model) clearHistory() tea.Cmd {
	runs := m.deps.Runs
	return func() tea.Msg { return historyClearedMsg{runs.Clear()} }
}

// openRun opens a run's page in the browser.
func (m *Model) openRun(u string) tea.Cmd {
	if m.deps.OpenURL == nil {
		m.flash = "can't open a browser here"
		return nil
	}
	m.flash = "opening the workflow invocation in the browser"
	open := m.deps.OpenURL
	return func() tea.Msg { return urlOpenedMsg{open(u)} }
}

// tabsKeys are the log pane's keys for the footer, with its tabs.
func (m Model) tabsKeys() []key.Binding {
	k := m.keys
	next := k.RunTable
	b := []key.Binding{k.Down, k.HalfDown, k.Top, k.Bottom}
	canOpen := m.deps.OpenURL != nil
	switch m.logTab {
	case tabLog:
		next.SetHelp("t", "jobs")
		if canOpen && m.lastRunURL() != "" {
			b = append([]key.Binding{k.OpenURL}, b...)
		}
	case tabJobs:
		next.SetHelp("t", "history")
	case tabHistory:
		next.SetHelp("t", "log")
		if h := m.history; h.open != nil {
			b = append([]key.Binding{k.Back}, b...)
		} else if len(h.entries) > 0 {
			jobs := k.Accept
			jobs.SetHelp("enter", "its jobs")
			b = append([]key.Binding{jobs}, b...)
			b = append(b, k.ClearHistory)
			if e := h.entries[h.cursor]; canOpen && e.Report != nil && e.Report.URL != "" {
				b = append([]key.Binding{k.OpenURL}, b...)
			}
		}
	}
	if t := (&m).shownTable(); t != nil && canOpen {
		open := k.OpenURL
		if t.report.URL == "" {
			open.SetHelp("u", "open BigQuery job")
		}
		if a := t.selected(); t.report.URL != "" {
			if a != nil && a.JobURL != "" {
				b = append([]key.Binding{k.OpenJob}, b...)
			}
			b = append([]key.Binding{open}, b...)
		} else if a != nil && a.JobURL != "" {
			b = append([]key.Binding{open}, b...)
		}
	}
	return append([]key.Binding{next}, b...)
}

// fitLabel is a job's row label: its action's name, or what stands in for
// one.
func fitLabel(schema, name string, width int) string {
	if schema == "" {
		return ansi.Truncate(name, width, "…")
	}
	return fitName(schema+"."+name, name, width)
}
