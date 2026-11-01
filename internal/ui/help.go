package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	helpMaxW     = 72
	helpTwoColsW = 70 // the narrowest box that fits both columns
	helpLeftColW = 34
)

// helpOverlay is the "?" key reference. It scrolls when it doesn't fit the
// screen, and "/" searches it as vim does: every match is highlighted, the
// view goes to the first as the query is typed, and n and N go from one
// matching row to the next.
type helpOverlay struct {
	open   bool
	offset int // first visible line
	input  textinput.Model
	typing bool // "/" was pressed: keys edit the query until enter or esc
	from   int  // the offset when "/" was pressed, which esc goes back to
	cur    int  // the current match, as its row's number (see helpHit)
}

func newHelpOverlay() helpOverlay {
	in := textinput.New()
	in.Prompt = "/"
	return helpOverlay{input: in}
}

func (h *helpOverlay) show() {
	h.open, h.offset = true, 0
	h.clearSearch()
}

// clearSearch drops the query and its highlights.
func (h *helpOverlay) clearSearch() {
	h.typing, h.cur = false, 0
	h.input.SetValue("")
	h.input.Blur()
}

func (h *helpOverlay) query() string { return strings.TrimSpace(h.input.Value()) }

// helpSection is a titled group of bindings in the help overlay.
type helpSection struct {
	title    string
	bindings []key.Binding
}

// helpSections lists every binding for the help overlay, in two columns.
func (m Model) helpSections() [2][]helpSection {
	k := m.keys
	global := []key.Binding{k.Find, k.FindKind, k.Commands, k.NextInFile, k.PrevInFile, k.Compile, k.Incremental, k.Columns, k.Errors, k.Log, k.Follow}
	if m.deps.Open != nil {
		global = append(global, k.Open)
	}
	if m.deps.FullScreen != nil {
		global = append(global, k.FullScreen)
	}
	if m.deps.Info != nil {
		global = append(global, k.Info)
	}
	if m.deps.Branches != nil {
		global = append(global, k.Branch)
	}
	global = append(global, k.Help, k.Quit, k.Cancel)
	targetKeys := []key.Binding{k.CopyTarget, k.CopyPath}
	logKeys := []key.Binding{k.Top, k.Bottom}
	if m.deps.OpenURL != nil {
		targetKeys = append([]key.Binding{k.OpenTable}, targetKeys...)
		logKeys = append(logKeys, k.OpenURL)
	}
	if m.deps.StartAPIRun != nil || m.hasTabs() {
		tabs := k.RunTable
		if m.hasTabs() {
			tabs.SetHelp("t", "next tab: log, jobs, history")
		}
		logKeys = append(logKeys, tabs)
		if m.deps.OpenURL != nil {
			logKeys = append(logKeys, k.OpenJob)
		}
		search := k.Find
		search.SetHelp("/", "search the jobs (esc clears)")
		sort := k.Columns
		sort.SetHelp("S", "sort the jobs by the next column")
		logKeys = append(logKeys, search, sort)
	}
	var run []key.Binding
	if m.canRun() {
		run = append(run, k.Run)
	}
	if m.deps.ChangedActions != nil {
		run = append(run, k.RunChanged)
	}
	if m.canRun() {
		run = append(run, k.RunTags)
	}
	if m.deps.ColumnImpact != nil {
		run = append(run, k.Impact)
	}
	if m.deps.QueryCost != nil {
		run = append(run, k.Cost)
	}
	if m.canRun() {
		run = append(run, k.Deps, k.Dependents, k.FullRefresh)
	}
	if m.deps.StartAPIRun != nil {
		run = append(run, k.API)
	}
	if m.canRun() {
		run = append(run, k.SelectedOnly)
	}
	panes := []key.Binding{k.NextPane, k.PrevPane, k.Zoom, k.Escape, k.ToggleLineage, k.FocusSQL, k.FocusActions, k.FocusLineage}
	if m.canRun() {
		panes = append(panes, k.FocusRun)
	}
	panes = append(panes, k.FocusLog)
	right := []helpSection{
		{"move", []key.Binding{k.Down, k.Up, k.Top, k.Bottom, k.HalfDown, k.HalfUp}},
		{"1 actions", append([]key.Binding{k.ShowSQL}, targetKeys...)},
		{"2 lineage", append([]key.Binding{k.Jump, k.Back}, targetKeys...)},
		{"4 log", logKeys},
	}
	// b, which changes the base R, I and C compare with.
	var base []key.Binding
	if m.deps.Branches != nil && m.deps.ChangedActions != nil {
		base = []key.Binding{k.Base}
	}
	if m.deps.ChangedActions != nil {
		right = append(right, helpSection{"R run changed", append([]key.Binding{k.ToggleFile, k.ToggleAll}, base...)})
	}
	if m.canRun() {
		right = append(right, helpSection{"T run tags", []key.Binding{k.ToggleTag, k.RunChecked, k.TagOptions}})
	}
	if m.deps.ColumnImpact != nil {
		recheck := k.Compile
		recheck.SetHelp("c", "check again")
		right = append(right, helpSection{"I column impact", append([]key.Binding{k.CopyReport, recheck}, base...)})
	}
	if m.deps.QueryCost != nil {
		recheck := k.Compile
		recheck.SetHelp("c", "check again")
		right = append(right, helpSection{"C query cost", append([]key.Binding{k.CopyReport, recheck}, base...)})
	}
	if m.deps.Info != nil {
		search := k.Find
		search.SetHelp("/", "search")
		copyAll := k.CopyReport
		copyAll.SetHelp("y", "copy")
		right = append(right, helpSection{", project info", []key.Binding{search, k.NextMatch, copyAll}})
	}
	filter := k.Find
	filter.SetHelp("/", "filter by name (tab: by type)")
	right = append(right, helpSection{"0 columns", []key.Binding{filter, k.CopyColumn, k.CopyColumnType}})
	right = append(right, helpSection{"0 sql (vim keys)", []key.Binding{k.Left, k.WordNext, k.WordPrev, k.LineStart, k.LineEnd, k.ScrollDown, k.Visual, k.VisualLine, k.Yank, k.YankLine, k.OtherEnd, k.Search, k.NextMatch, k.SearchWord}})
	return [2][]helpSection{
		{
			{"global", global},
			{"run", run},
			{"panes", panes},
		},
		right,
	}
}

// helpHit is a row of the help overlay that the query matches: a section's
// title or a binding. Rows are numbered down the left column, then down the
// right one, which is the order n goes through them.
type helpHit struct {
	row  int // the row's number
	line int // the body line it is on
}

// helpColumn renders sections as lines, a blank line between sections, with
// q (lower case) highlighted, and returns the rows it is in. The rows are
// numbered from *n, which is left at the number after the last.
func (m Model) helpColumn(sections []helpSection, q string, n *int) (lines []string, hits []helpHit) {
	s := m.styles
	// row notes the row about to be added and returns its highlight style.
	row := func(texts ...string) lipgloss.Style {
		hit := s.match
		for _, t := range texts {
			if q != "" && strings.Contains(strings.ToLower(t), q) {
				hits = append(hits, helpHit{row: *n, line: len(lines)})
				if *n == m.helpCur() {
					hit = s.matchCurrent
				}
				break
			}
		}
		*n++
		return hit
	}
	for _, sec := range sections {
		if len(sec.bindings) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		hit := row(sec.title)
		lines = append(lines, highlight(sec.title, q, s.focusTitle, hit))
		for _, b := range sec.bindings {
			h := b.Help()
			hit := row(h.Key, h.Desc)
			lines = append(lines, "  "+highlight(fmt.Sprintf("%-10s", h.Key), q, s.footerKey, hit)+" "+highlight(h.Desc, q, s.footerDesc, hit))
		}
	}
	return lines, hits
}

// highlight renders text in base with every case-insensitive occurrence of
// q (lower case) in hit.
func highlight(text, q string, base, hit lipgloss.Style) string {
	lower := strings.ToLower(text)
	if q == "" || len(lower) != len(text) { // byte offsets must line up
		return base.Render(text)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, q)
		if i < 0 {
			break
		}
		b.WriteString(base.Render(text[:i]) + hit.Render(text[i:i+len(q)]))
		text, lower = text[i+len(q):], lower[i+len(q):]
	}
	b.WriteString(base.Render(text))
	return b.String()
}

// helpLayout is the help overlay's width, every line of its body, before
// scrolling, and the rows the query matches: two columns side by side when
// they fit, else one after the other.
func (m Model) helpLayout() (w int, lines []string, hits []helpHit) {
	q := strings.ToLower(m.keyHelp.query())
	cols := m.helpSections()
	w = min(m.width-4, helpMaxW)
	n := 0
	if w < helpTwoColsW {
		lines, hits = m.helpColumn(append(cols[0], cols[1]...), q, &n)
		return w, lines, hits
	}
	left, hits := m.helpColumn(cols[0], q, &n)
	right, rightHits := m.helpColumn(cols[1], q, &n)
	for i := range max(len(left), len(right)) {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, l+strings.Repeat(" ", max(helpLeftColW-ansi.StringWidth(l), 0))+r)
	}
	return w, lines, append(hits, rightHits...)
}

// helpCur is the number of the row that is the current match, or -1 while
// nothing is searched for.
func (m Model) helpCur() int {
	if m.keyHelp.query() == "" {
		return -1
	}
	return m.keyHelp.cur
}

// seekHelp makes the first match at or below where the search started the
// current one and shows it; with no match the view stays where it started.
func (m *Model) seekHelp() {
	h := &m.keyHelp
	h.offset, h.cur = h.from, 0
	_, _, hits := m.helpLayout()
	if len(hits) == 0 {
		return
	}
	first := hits[0]
	for _, hit := range hits {
		if hit.line >= h.from {
			first = hit
			break
		}
	}
	h.cur = first.row
	m.revealHelp(first.line)
}

// stepHelp makes the match d matches on the current one, around the ends,
// and shows it.
func (m *Model) stepHelp(d int) {
	_, _, hits := m.helpLayout()
	if len(hits) == 0 {
		return
	}
	cur := 0
	for i, hit := range hits {
		if hit.row == m.keyHelp.cur {
			cur = i
		}
	}
	next := hits[(cur+d+len(hits))%len(hits)]
	m.keyHelp.cur = next.row
	m.revealHelp(next.line)
}

// revealHelp scrolls the body's line into view, if it isn't in it.
func (m *Model) revealHelp(line int) {
	_, lines, _ := m.helpLayout()
	rows := m.helpRows(len(lines))
	last := max(len(lines)-rows, 0)
	off := min(m.keyHelp.offset, last)
	switch {
	case line < off:
		off = line - infoContext
	case line >= off+rows:
		off = line + 1 + infoContext - rows
	}
	m.keyHelp.offset = min(max(off, 0), last)
}

// helpRows is how many body lines the help overlay shows at once.
func (m Model) helpRows(total int) int {
	return max(min(total, m.geometry().bodyH-2), 1)
}

// scrollHelp moves the help overlay's view by n lines, within its body.
func (m *Model) scrollHelp(n int) {
	_, lines, _ := m.helpLayout()
	last := max(len(lines)-m.helpRows(len(lines)), 0)
	m.keyHelp.offset = min(max(m.keyHelp.offset+n, 0), last)
}

// handleHelpKey scrolls the help overlay (j/k, ctrl+d/u, g/G, the wheel),
// searches it (/, then enter keeps the search and esc drops it; n and N go
// to the next and the previous match) and closes it (esc with no search, q
// or ?).
func (m *Model) handleHelpKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	h := &m.keyHelp
	if h.typing {
		switch {
		case key.Matches(msg, k.Escape):
			h.clearSearch()
			h.offset = h.from
		case msg.String() == "enter":
			h.typing = false
			h.input.Blur()
			if h.query() == "" {
				h.clearSearch()
			}
		default:
			before := h.query()
			var cmd tea.Cmd
			h.input, cmd = h.input.Update(msg)
			if h.query() != before {
				m.seekHelp()
			}
			return cmd
		}
		return nil
	}
	_, lines, _ := m.helpLayout()
	page := m.helpRows(len(lines))
	switch {
	case key.Matches(msg, k.Escape) && h.query() != "":
		h.clearSearch()
	case key.Matches(msg, k.Close, k.Help):
		h.open = false
	case key.Matches(msg, k.Find):
		h.clearSearch()
		h.typing, h.from = true, min(h.offset, max(len(lines)-page, 0))
		return h.input.Focus()
	case key.Matches(msg, k.NextMatch):
		m.stepHelp(1)
	case key.Matches(msg, k.PrevMatch):
		m.stepHelp(-1)
	case key.Matches(msg, k.Down):
		m.scrollHelp(1)
	case key.Matches(msg, k.Up):
		m.scrollHelp(-1)
	case key.Matches(msg, k.HalfDown):
		m.scrollHelp(max(page/2, 1))
	case key.Matches(msg, k.HalfUp):
		m.scrollHelp(-max(page/2, 1))
	case key.Matches(msg, k.Top):
		h.offset = 0
	case key.Matches(msg, k.Bottom):
		m.scrollHelp(len(lines))
	}
	return nil
}

// helpBox renders the key help overlay, its title saying what is searched
// for and which of the matching rows is the current one.
func (m Model) helpBox() string {
	w, lines, hits := m.helpLayout()
	rows := m.helpRows(len(lines))
	off := min(m.keyHelp.offset, max(len(lines)-rows, 0))
	title := "help · esc closes"
	if q := m.keyHelp.query(); q != "" {
		found := "no match"
		for i, hit := range hits {
			if hit.row == m.keyHelp.cur {
				found = fmt.Sprintf("%d of %d", i+1, len(hits))
			}
		}
		title = "help · /" + q + " · " + found
	}
	footer := moreNote(off, len(lines)-off-rows)
	return m.styles.paneWithFooter(title, footer, strings.Join(lines[off:off+rows], "\n"), w, rows+2, true)
}

// helpFooter is the footer's keys while the help overlay is open.
func (m Model) helpFooter() []key.Binding {
	k := m.keys
	search := k.Find
	search.SetHelp("/", "search")
	scroll := k.Down
	scroll.SetHelp("j/k", "scroll")
	if m.keyHelp.query() == "" {
		return []key.Binding{search, scroll, k.Close}
	}
	done := k.Close
	done.SetHelp("esc", "clear search")
	return []key.Binding{done, k.NextMatch, search, scroll}
}
