package ui

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// A run table (runTable) is searched and sorted as xf web's jobs table is
// (internal/web/assets/log-tabs.js): / keeps the actions whose target,
// state or failure has every word typed, and S orders them by the next
// column. Over the table, a line tells how far the run has got and what
// its jobs have cost so far.

// jobSortCol is a column a run table sorts by.
type jobSortCol struct {
	id, title string
	// num is the action's number in the column, ok false when it has none
	// yet; nil for a text column.
	num func(a project.RunAction) (v float64, ok bool)
}

// jobSortCols are the columns in the order S goes round them. A number
// sorts biggest first, a text A to Z.
var jobSortCols = []jobSortCol{
	{id: "duration", title: "Duration", num: func(a project.RunAction) (float64, bool) { d, _, _, _ := jobSortNumbers(a); return float64(d), d != 0 }},
	{id: "slot", title: "Slot time", num: func(a project.RunAction) (float64, bool) { _, s, _, _ := jobSortNumbers(a); return float64(s), s != 0 }},
	{id: "billed", title: "Billed", num: func(a project.RunAction) (float64, bool) { _, _, b, _ := jobSortNumbers(a); return float64(b), b != 0 }},
	{id: "cost", title: "Est. cost", num: func(a project.RunAction) (float64, bool) { _, _, _, c := jobSortNumbers(a); return c, c != 0 }},
	{id: "target", title: "Target"},
	{id: "state", title: "State"},
}

// stateRank orders by state: what needs looking at first.
var stateRank = map[string]int{"RUNNING": 0, "FAILED": 1, "CANCELLED": 1, "PENDING": 2, "SUCCEEDED": 3, "SKIPPED": 4, "DISABLED": 4}

func jobTarget(a project.RunAction) string {
	return strings.TrimPrefix(a.Target.Schema+"."+a.Target.Name, ".")
}

// order is the indexes in t.report.Actions of the rows t lists: those
// matching its search, in its order.
func (t runTable) order() []int {
	if t.report == nil {
		return nil
	}
	words := strings.Fields(strings.ToLower(t.query))
	out := make([]int, 0, len(t.report.Actions))
	for i, a := range t.report.Actions {
		hay := strings.ToLower(jobTarget(a) + " " + actionState(a.State) + " " + a.FailureReason)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			out = append(out, i)
		}
	}
	col, ok := sortColOf(t.sortCol)
	if !ok {
		return out
	}
	acts := t.report.Actions
	dir := 1
	if t.sortDesc {
		dir = -1
	}
	slices.SortStableFunc(out, func(i, j int) int {
		a, b := acts[i], acts[j]
		var c int
		switch {
		case col.num != nil:
			x, xok := col.num(a)
			y, yok := col.num(b)
			if xok != yok { // those with no number yet stay last
				if xok {
					return -1
				}
				return 1
			}
			c = cmp.Compare(x, y) * dir
		case col.id == "state":
			c = cmp.Compare(rankOf(a.State), rankOf(b.State)) * dir
		default:
			c = cmp.Compare(jobTarget(a), jobTarget(b)) * dir
		}
		return cmp.Or(c, cmp.Compare(jobTarget(a), jobTarget(b)))
	})
	return out
}

func rankOf(state string) int {
	if r, ok := stateRank[state]; ok {
		return r
	}
	return 2
}

func sortColOf(id string) (jobSortCol, bool) {
	i := slices.IndexFunc(jobSortCols, func(c jobSortCol) bool { return c.id == id })
	if i < 0 {
		return jobSortCol{}, false
	}
	return jobSortCols[i], true
}

// setQuery searches for q, with the cursor on the first match.
func (t *runTable) setQuery(q string) {
	t.query = q
	t.cursor, t.offset = 0, 0
}

// nextSort is S: the next column, its natural way, and after the last the
// run's order again. The cursor stays on its action. It says what it did.
func (t *runTable) nextSort() string {
	i := slices.IndexFunc(jobSortCols, func(c jobSortCol) bool { return c.id == t.sortCol })
	was := t.selectedIndex()
	if i+1 < len(jobSortCols) {
		c := jobSortCols[i+1]
		t.sortCol, t.sortDesc = c.id, c.num != nil
	} else {
		t.sortCol, t.sortDesc = "", false
	}
	if was >= 0 {
		t.cursor = max(slices.Index(t.order(), was), 0)
	}
	return "jobs " + t.sortSaid()
}

// sortSaid is the order the table lists its actions in: "by slot time,
// biggest first".
func (t runTable) sortSaid() string {
	c, ok := sortColOf(t.sortCol)
	switch {
	case !ok:
		return "in the run's order"
	case c.num == nil && t.sortDesc:
		return "by " + strings.ToLower(c.title) + ", reversed"
	case c.num == nil:
		return "by " + strings.ToLower(c.title)
	case t.sortDesc:
		return "by " + strings.ToLower(c.title) + ", biggest first"
	}
	return "by " + strings.ToLower(c.title) + ", smallest first"
}

// sortedTitles writes the title of the column t sorts by in capitals, which
// keeps its width.
func (t runTable) sortedTitles(c *runColumns) {
	col, ok := sortColOf(t.sortCol)
	if !ok {
		return
	}
	for i := range c.cols {
		if strings.TrimSpace(c.cols[i].Title) == col.title {
			c.cols[i].Title = strings.ToUpper(c.cols[i].Title)
		}
	}
}

// summary is the line over the table: the search, how far the run has got
// and what its jobs have cost so far, in width cells. Where it doesn't
// all fit, the parts that matter least go first: the order, the count of
// jobs, then the times.
func (t runTable) summary(s styles, width int, now time.Time, find string) string {
	r := t.report
	sum := frontend.SummarizeJobs(r, now)
	type part struct {
		text string
		keep int // the higher, the later it goes
	}
	var parts []part
	add := func(keep int, text string) { parts = append(parts, part{text, keep}) }
	if find != "" {
		if n := len(t.order()); n != sum.Actions {
			find += s.dim.Render(fmt.Sprintf(" %d of %d", n, sum.Actions))
		}
		add(9, find)
	}
	done := sum.Succeeded + sum.Failed + sum.Skipped
	add(8, fmt.Sprintf("%s/%d done %s", s.text.Bold(true).Render(fmt.Sprint(done)), sum.Actions, progressBar(s, sum, 10)))
	count := func(keep, n int, render func(...string) string, text string) {
		if n > 0 {
			add(keep, render(fmt.Sprintf(text, n)))
		}
	}
	count(6, sum.Running, s.warnText.Render, "▶ %d running")
	count(4, sum.Pending, s.dim.Render, "· %d pending")
	count(6, sum.Failed, s.errorText.Render, "✗ %d failed")
	count(4, sum.Skipped, s.dim.Render, "– %d skipped")
	stat := func(keep int, label, v string) {
		if v != "" {
			add(keep, s.dim.Render(label)+" "+v)
		}
	}
	stat(3, "elapsed", sum.Elapsed)
	if tot := r.Totals(); tot.Jobs > 0 {
		add(1, fmt.Sprintf("%d BigQuery job%s", tot.Jobs, plural(tot.Jobs)))
		stat(3, "slot", formatDuration(time.Duration(tot.SlotMs)*time.Millisecond))
		stat(5, "billed", formatBytes(tot.BytesBilled))
		stat(7, "est.", s.text.Bold(true).Render(formatCost(tot.CostUSD)))
	}
	if t.sortCol != "" {
		add(2, s.dim.Render(t.sortSaid()))
	}
	line := func() string {
		texts := make([]string, len(parts))
		for i, p := range parts {
			texts[i] = p.text
		}
		return " " + strings.Join(texts, "  ")
	}
	for len(parts) > 1 && ansi.StringWidth(line()) > width {
		drop := 0
		for i, p := range parts {
			if p.keep < parts[drop].keep {
				drop = i
			}
		}
		parts = slices.Delete(parts, drop, drop+1)
	}
	return ansi.Truncate(line(), width, "…")
}

// progressBar is w cells of the run's actions: succeeded, failed, skipped,
// running, then those still to run.
func progressBar(s styles, sum frontend.JobsSummary, w int) string {
	if sum.Actions == 0 {
		return ""
	}
	var b strings.Builder
	at, cum := 0, 0
	for _, seg := range []struct {
		n      int
		render func(...string) string
		glyph  string
	}{
		{sum.Succeeded, s.okText.Render, "█"}, {sum.Failed, s.errorText.Render, "█"},
		{sum.Skipped, s.dim.Render, "█"}, {sum.Running, s.warnText.Render, "▓"}, {sum.Pending, s.dim.Render, "░"},
	} {
		cum += seg.n
		end := int(math.Round(float64(w*cum) / float64(sum.Actions)))
		if end > at {
			b.WriteString(seg.render(strings.Repeat(seg.glyph, end-at)))
			at = end
		}
	}
	return b.String()
}

// newJobsFind is the box / types a run table's search in.
func newJobsFind() textinput.Model {
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "target, state or failure"
	return in
}

// handleJobsKey handles a key in the log pane over a table of jobs. It
// runs before the global keys, as the columns view's does: typing a search
// takes every key (ctrl+n, ctrl+p and the arrows still move the cursor),
// and over the table / searches it and S sorts it, where elsewhere they
// find an action and show the columns. Unhandled keys fall through.
func (m *Model) handleJobsKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	k := m.keys
	t := m.shownTable()
	if t == nil {
		m.stopJobsFind()
		return false, nil
	}
	if m.jobsTyping {
		switch {
		case key.Matches(msg, k.Escape):
			m.stopJobsFind()
			t.setQuery("")
		case key.Matches(msg, k.Accept):
			m.stopJobsFind()
		case slices.Contains([]string{"ctrl+n", "down"}, msg.String()):
			t.move(1, m.log.height)
		case slices.Contains([]string{"ctrl+p", "up"}, msg.String()):
			t.move(-1, m.log.height)
		default:
			var cmd tea.Cmd
			m.jobsFind, cmd = m.jobsFind.Update(msg)
			if q := m.jobsFind.Value(); q != t.query {
				t.setQuery(q)
			}
			return true, cmd
		}
		return true, nil
	}
	switch {
	case key.Matches(msg, k.Find):
		m.jobsTyping = true
		m.jobsFind.SetValue(t.query)
		m.jobsFind.CursorEnd()
		return true, m.jobsFind.Focus()
	case key.Matches(msg, k.Columns):
		m.flash = t.nextSort()
	case key.Matches(msg, k.Escape) && t.query != "":
		t.setQuery("")
	default:
		return false, nil
	}
	return true, nil
}

func (m *Model) stopJobsFind() {
	m.jobsTyping = false
	m.jobsFind.Blur()
}

// jobsFindView is the search as the summary line starts with it: the box
// while typing, else what was searched for.
func (m Model) jobsFindView(t *runTable) string {
	switch {
	case m.jobsTyping:
		return m.jobsFind.View()
	case t.query != "":
		return m.styles.warnText.Render("/" + t.query)
	}
	return ""
}

// jobsFindKeys are the footer's keys while a search is typed.
func (m Model) jobsFindKeys() []key.Binding {
	k := m.keys
	move := k.Down
	move.SetHelp("ctrl+n/p", "move")
	keep := k.Accept
	keep.SetHelp("enter", "keep")
	clear := k.Escape
	clear.SetHelp("esc", "clear")
	return []key.Binding{move, keep, clear}
}
