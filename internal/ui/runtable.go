package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
)

// runTable is the table of a run's report (project.RunReport) in the log
// pane: each action of an API run with its state, how long its BigQuery job
// took, the slot time it used, the bytes it was billed and what they cost,
// and their totals, as the extension's workflow run table shows. The pane
// shows it in place of the log once the run has actions; t swaps the two.
type runTable struct {
	report *project.RunReport
	// logInstead is set once t chose the log; it stays for later runs.
	logInstead bool
	cursor     int
	offset     int // first action shown
	// kept is the kept run the report is of, when it is not of a run
	// started here; ran is set once one was.
	kept *runhistory.Entry
	ran  bool
	// query and sortCol are its search and sort (see order); they stay
	// for later runs, as the page's do.
	query    string
	sortCol  string
	sortDesc bool
}

// reset forgets the last run's report, for a new run.
func (t *runTable) reset() { t.report, t.cursor, t.offset, t.kept, t.ran = nil, 0, 0, nil, true }

func (t *runTable) set(r *project.RunReport) {
	t.report = r
	t.cursor = min(t.cursor, max(len(t.order())-1, 0))
}

// has reports whether there is a table to show.
func (t runTable) has() bool { return t.report != nil && len(t.report.Actions) > 0 }

// shown reports whether the log pane shows the table.
func (t runTable) shown() bool { return t.has() && !t.logInstead }

// selected is the action under the cursor, nil when none is listed.
func (t runTable) selected() *project.RunAction {
	if i := t.selectedIndex(); i >= 0 {
		return &t.report.Actions[i]
	}
	return nil
}

// selectedIndex is the selected action's index in t.report.Actions, -1
// when none is listed.
func (t runTable) selectedIndex() int {
	if !t.has() {
		return -1
	}
	o := t.order()
	if t.cursor >= len(o) {
		return -1
	}
	return o[t.cursor]
}

// tableRows is how many actions fit in height lines, under the summary
// and the header.
func tableRows(height int) int { return max(height-2, 1) }

// move moves the cursor n actions in a pane height lines tall, scrolling
// to keep it in view.
func (t *runTable) move(n, height int) {
	if !t.has() {
		return
	}
	t.cursor = min(max(t.cursor+n, 0), max(len(t.order())-1, 0))
	t.offset = t.window(tableRows(height))
}

// window is the first action to show so that the cursor is among rows of
// them.
func (t runTable) window(rows int) int {
	off := min(t.offset, max(len(t.order())-rows, 0))
	return max(min(off, t.cursor), t.cursor-rows+1, 0)
}

// The table's columns after the target, which takes the room they leave.
// Each is as wide as its widest cell: "✗ cancelled", "59m 59s",
// "1023.99 KiB", "$123.4567".
const (
	stateColW    = 11
	durationColW = 8
	slotColW     = 9
	billedColW   = 11
	costColW     = 9
	cellPad      = 1 // blank cells either side of a column
	minTargetW   = 16
)

// runColumns lays the table out in width cells. On a narrow pane it drops
// the slot time, then the duration; on a wide one with failures to tell of,
// it adds their reasons.
type runColumns struct {
	cols                   []table.Column
	slot, duration, reason bool
	// now is the time to tell how long a running job has taken so far;
	// zero leaves that out.
	now time.Time
}

func layoutRunColumns(width int, failures bool) runColumns {
	c := runColumns{slot: true, duration: true}
	fixed := func() int {
		w, n := stateColW+billedColW+costColW, 4
		if c.slot {
			w, n = w+slotColW, n+1
		}
		if c.duration {
			w, n = w+durationColW, n+1
		}
		return w + n*2*cellPad
	}
	if width-fixed() < minTargetW {
		c.slot = false
	}
	if width-fixed() < minTargetW {
		c.duration = false
	}
	targetW := max(width-fixed(), 4)
	reasonW := 0
	if failures && targetW >= 64 {
		c.reason = true
		reasonW = targetW/2 - 2*cellPad
		targetW -= reasonW + 2*cellPad
	}
	c.cols = []table.Column{{Title: "Target", Width: targetW}, {Title: "State", Width: stateColW}}
	right := func(title string, w int) { c.cols = append(c.cols, table.Column{Title: padLeft(title, w), Width: w}) }
	if c.duration {
		right("Duration", durationColW)
	}
	if c.slot {
		right("Slot time", slotColW)
	}
	right("Billed", billedColW)
	right("Est. cost", costColW)
	if c.reason {
		c.cols = append(c.cols, table.Column{Title: "Failure", Width: reasonW})
	}
	return c
}

// row is a's cells. state styles the state cell; nil leaves it plain, as
// the selected row must be for its highlight to cover it.
func (c runColumns) row(a project.RunAction, state func(string) string) table.Row {
	st := actionState(a.State)
	if state != nil {
		st = state(st)
	}
	row := table.Row{fitLabel(a.Target.Schema, a.Target.Name, c.cols[0].Width), st}
	return append(row, c.figures(frontend.JobCellsOf(a, c.now), strings.Join(strings.Fields(a.FailureReason), " "))...)
}

// figures are the cells after the state: the shown figures of f, padded
// to their columns, then the failure when there is a column for it.
func (c runColumns) figures(f frontend.JobCells, failure string) []string {
	var row []string
	if c.duration {
		row = append(row, padLeft(f.Duration, durationColW))
	}
	if c.slot {
		row = append(row, padLeft(f.Slot, slotColW))
	}
	row = append(row, padLeft(f.Billed, billedColW), padLeft(f.Cost, costColW))
	if c.reason {
		row = append(row, failure)
	}
	return row
}

// view renders the table in width×height cells: the summary when there is
// a line for it, the header and the listed actions around the cursor. now
// is the time, for a run that is going; zero for one that is not. find is
// the search as the summary starts with it.
func (t runTable) view(s styles, width, height int, focused bool, now time.Time, find string) string {
	r := t.report
	failures := false
	for _, a := range r.Actions {
		failures = failures || a.FailureReason != ""
	}
	c := layoutRunColumns(width, failures)
	c.now = now
	t.sortedTitles(&c)
	rows := tableRows(height)
	order := t.order()
	off := t.window(rows)
	end := min(off+rows, len(order))

	cells := make([]table.Row, 0, max(end-off, 0))
	for i := off; i < end; i++ {
		state := s.stateText
		if focused && i == t.cursor {
			state = nil
		}
		cells = append(cells, c.row(r.Actions[order[i]], state))
	}
	st := table.Styles{
		Header:   s.dim.Bold(true).Padding(0, cellPad),
		Cell:     lipgloss.NewStyle().Padding(0, cellPad),
		Selected: lipgloss.NewStyle(),
	}
	if focused {
		st.Selected = s.selected
	}
	tb := table.New(table.WithColumns(c.cols), table.WithRows(cells), table.WithStyles(st),
		table.WithWidth(width), table.WithHeight(len(cells)+1))
	tb.SetCursor(t.cursor - off)
	out := tb.View()
	if len(order) == 0 {
		out += "\n" + s.dim.Render(" no job matches “"+t.query+"”: esc clears the search")
	}
	if height < 3 {
		return out
	}
	return t.summary(s, width, now, find) + "\n" + out
}

// stateText colours an action's state cell.
func (s styles) stateText(state string) string {
	switch {
	case strings.HasPrefix(state, "✓"):
		return s.okText.Render(state)
	case strings.HasPrefix(state, "✗"):
		return s.errorText.Render(state)
	case strings.HasPrefix(state, "▶"):
		return s.warnText.Render(state)
	}
	return s.dim.Render(state)
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-ansi.StringWidth(s), 0)) + s
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// syncRunTable takes the run's report as it is now. A run's lines follow
// the changes they tell of, so this is called after each, and when the run
// ends.
func (m *Model) syncRunTable() {
	if m.run == nil || m.run.Report == nil {
		return
	}
	r := m.run.Report()
	if r == nil {
		return
	}
	had, jobs := m.runTable.has(), m.jobCount()
	m.runTable.set(r)
	if !m.hasTabs() || m.logTab != tabLog {
		return
	}
	switch {
	case m.runAPI && !had && m.runTable.has() && !m.runTable.logInstead:
		// An API run's table shows itself, unless the log was chosen.
		m.logTab = tabJobs
	case !m.runAPI && jobs == 0 && m.jobCount() > 0:
		m.flash = "t lists this run's BigQuery jobs and what they cost"
	}
}

// logBody is what the log pane shows in width cells: the run's table, or
// its log.
func (m Model) logBody(width int, focused bool) string {
	if m.hasTabs() {
		return m.tabsBody(width, focused)
	}
	if m.runTable.shown() {
		return m.runTable.view(m.styles, width, m.log.height, focused, time.Time{}, m.jobsFindView(&m.runTable))
	}
	return strings.Join(m.log.visible(), "\n")
}

// logFooter is the note in the log pane's border: how to swap the table
// and the log, when there is a table.
func (m Model) logFooter() string {
	if m.hasTabs() {
		return m.tabsFooter()
	}
	switch {
	case !m.runTable.has():
		return ""
	case m.runTable.shown():
		return "t log"
	}
	return "t table"
}

// handleRunTableKey handles a key in the log pane that is the run table's:
// t swaps the table and the log and, over the table, the move keys move its
// cursor and enter opens the BigQuery job of the action under it. ok is
// false for a key that is the log's.
func (m *Model) handleRunTableKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	if m.hasTabs() {
		return m.handleTabsKey(msg)
	}
	k := m.keys
	t := &m.runTable
	if key.Matches(msg, k.RunTable) {
		switch {
		case !t.has() && m.runAPI:
			m.flash = "no run table yet: the run has no actions"
		case !t.has():
			m.flash = "t shows an API run's table; this run has none"
		default:
			t.logInstead = !t.logInstead
		}
		return nil, true
	}
	if !t.shown() {
		return nil, false
	}
	if key.Matches(msg, k.OpenJob) {
		return m.openJobOf(t), true
	}
	return nil, t.handleMove(msg, k, m.log.height)
}

// handleMove moves the cursor for a move key, in a pane h lines tall; it
// is false for any other key.
func (t *runTable) handleMove(msg tea.KeyPressMsg, k keyMap, h int) bool {
	switch {
	case key.Matches(msg, k.Up):
		t.move(-1, h)
	case key.Matches(msg, k.Down):
		t.move(1, h)
	case key.Matches(msg, k.HalfDown):
		t.move(max(tableRows(h)/2, 1), h)
	case key.Matches(msg, k.HalfUp):
		t.move(-max(tableRows(h)/2, 1), h)
	case key.Matches(msg, k.Top):
		t.move(-len(t.report.Actions), h)
	case key.Matches(msg, k.Bottom):
		t.move(len(t.report.Actions), h)
	default:
		return false
	}
	return true
}

// openJobOf opens the BigQuery job of the action under t's cursor in the
// browser.
func (m *Model) openJobOf(t *runTable) tea.Cmd {
	a := t.selected()
	switch {
	case m.deps.OpenURL == nil:
		m.flash = "can't open a browser here"
		return nil
	case a == nil || a.JobURL == "":
		m.flash = "this action ran no BigQuery job"
		return nil
	}
	m.flash = "opening the BigQuery job in the browser"
	open, u := m.deps.OpenURL, a.JobURL
	return func() tea.Msg { return urlOpenedMsg{open(u)} }
}
