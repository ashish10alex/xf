package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// The R overlay runs the Changed Actions: the actions whose compiled output
// differs from the merge-base with the default branch (see package
// changes), or for dbt what state:modified selects against it. It opens at
// once and asks the engine, which compiles the base
// the first time; it then lists what would run, grouped by file with why
// each changed, and the actions removed on the branch, which don't run.
// Every file starts checked; space unchecks the file under the cursor, x
// all of them, and r (or enter) runs the checked ones. d, D, f and a toggle
// as in the Run pane. With a on, the pushed branch is compared instead of
// the working tree, since that is what an API run compiles.

// changedOverlay is the R overlay's state.
type changedOverlay struct {
	open    bool
	loading bool
	// seq tells a request's answer from an earlier one's.
	seq  int
	head changes.Head
	res  *changes.Result
	err  error
	// skip is the unchecked files, by name. It lasts until the overlay
	// closes, so a file stays unchecked when the list is asked for again.
	skip map[string]bool
	// cursor is the row of the Changed Actions' list it is on, and
	// cursorKey what that row shows, to find it again in a new list.
	cursor    int
	cursorKey string
	offset    int // first visible row of the list
	cancel    context.CancelFunc
}

// changedMsg answers the request seq.
type changedMsg struct {
	seq int
	res *changes.Result
	err error
}

// changedTimeout bounds waiting for Changed Actions. Compiling a base takes
// about as long as compiling the project; the engine keeps compiling if
// the overlay stops waiting.
const changedTimeout = 10 * time.Minute

// primeChanges tells the engine the default branch once, so the markers
// show when a base compiled earlier today is on disk.
func (m Model) primeChanges() tea.Cmd {
	fn := m.deps.ChangedActions
	if fn == nil {
		return nil
	}
	req := changes.Request{DefaultBranch: m.deps.DefaultBranch, Head: changes.HeadWorktree}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), changedTimeout)
		defer cancel()
		_, _ = fn(ctx, req) // the answer arrives with the engine's state
		return nil
	}
}

// openChanged opens the R overlay and asks for the Changed Actions.
func (m *Model) openChanged() tea.Cmd {
	switch {
	case m.deps.ChangedActions == nil:
		m.flash = "run changed is not available"
		return nil
	case m.running:
		m.flash = "a run is already in progress (ctrl+c cancels it)"
		return nil
	}
	m.zoomed = false
	m.changed.open = true
	return m.requestChanged()
}

// requestChanged asks for the Changed Actions against the head the a
// toggle picks: the working tree, or the pushed branch for an API run.
func (m *Model) requestChanged() tea.Cmd {
	c := &m.changed
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.seq++
	// The last list stays up, dimmed, until the answer replaces it, so
	// asking again (a, c) doesn't collapse the overlay and redraw it.
	c.err = nil
	c.head = changes.HeadWorktree
	if m.flags.api {
		c.head = changes.HeadPushed
	}
	if c.head == changes.HeadWorktree && m.hasErrors() {
		// A run would fail on them, and the stale graph hides the edits.
		c.loading, c.res, c.err = false, nil, engine.ErrStale
		return nil
	}
	c.loading = true
	ctx, cancel := context.WithTimeout(context.Background(), changedTimeout)
	c.cancel = cancel
	seq, fn := c.seq, m.deps.ChangedActions
	req := changes.Request{DefaultBranch: m.deps.DefaultBranch, Head: c.head, Compile: true}
	return tea.Batch(m.startAnim(), func() tea.Msg {
		res, err := fn(ctx, req)
		return changedMsg{seq: seq, res: res, err: err}
	})
}

func (m *Model) handleChanged(msg changedMsg) {
	c := &m.changed
	if msg.seq != c.seq || !c.loading {
		return // an answer nobody is waiting for; the engine cached it
	}
	c.loading, c.res, c.err = false, msg.res, msg.err
	if c.err == nil && c.res == nil {
		c.err = errors.New("the base was not compiled")
	}
	// The cursor stays on its row, or goes to the nearest one.
	rows := c.cursorRows()
	i := slices.IndexFunc(rows, func(r frontend.ListRow) bool { return r.Key() == c.cursorKey })
	if i < 0 {
		i = min(c.cursor, len(rows)-1)
	}
	c.setCursor(max(i, 0))
}

func (m *Model) closeChanged() {
	c := &m.changed
	if c.cancel != nil {
		c.cancel()
	}
	*c = changedOverlay{seq: c.seq}
}

// listed is whether the overlay lists Changed Actions to check and run:
// not while asking again, after a failure, or when nothing changed.
func (c changedOverlay) listed() bool {
	return !c.loading && c.err == nil && c.res != nil && len(c.res.Changed) > 0
}

// runnable is the Changed Actions in checked files, what enter would run;
// nil when there are none.
func (c changedOverlay) runnable() []changes.Changed {
	if !c.listed() {
		return nil
	}
	var run []changes.Changed
	for _, ch := range c.res.Changed {
		if !c.skip[ch.FileName] {
			run = append(run, ch)
		}
	}
	return run
}

// files is how many files have Changed Actions, and how many are checked.
func (c changedOverlay) files() (checked, total int) {
	seen := map[string]bool{}
	for _, ch := range c.res.Changed {
		if !seen[ch.FileName] {
			seen[ch.FileName] = true
			total++
			if !c.skip[ch.FileName] {
				checked++
			}
		}
	}
	return checked, total
}

// cursorRows is the rows the cursor moves over: the Changed Actions' list,
// while it is listed.
func (c changedOverlay) cursorRows() []frontend.ListRow {
	if !c.listed() {
		return nil
	}
	return frontend.ChangedListRows(c.res)
}

func (c *changedOverlay) setCursor(i int) {
	c.cursor, c.cursorKey = i, ""
	if rows := c.cursorRows(); i < len(rows) {
		c.cursorKey = rows[i].Key()
	}
}

// toggleFile checks or unchecks the file under the cursor.
func (c *changedOverlay) toggleFile() {
	rows := c.cursorRows()
	if c.cursor >= len(rows) {
		return
	}
	file := rows[c.cursor].File
	if c.skip == nil {
		c.skip = map[string]bool{}
	}
	c.skip[file] = !c.skip[file]
}

// toggleAll unchecks every file when any is checked, else checks them all.
func (c *changedOverlay) toggleAll() {
	if !c.listed() {
		return
	}
	if len(c.runnable()) == 0 {
		c.skip = nil
		return
	}
	c.skip = map[string]bool{}
	for _, ch := range c.res.Changed {
		c.skip[ch.FileName] = true
	}
}

// moveChanged moves the cursor by delta rows, keeping it in view. Past the
// last Changed Action, or with no cursor, it scrolls the list instead: the
// removed actions, or the list being asked for again.
func (m *Model) moveChanged(delta int) {
	c := &m.changed
	lay := m.changedLayout(m.geometry().bodyH)
	n := len(c.cursorRows())
	if n == 0 || delta > 0 && c.cursor == n-1 {
		c.offset = lay.clamp(c.offset + delta)
		return
	}
	c.setCursor(min(max(c.cursor+delta, 0), n-1))
	c.offset = lay.clamp(max(min(c.offset, c.cursor), c.cursor-lay.rows+1))
}

// handleChangedKey handles a key while the R overlay is open.
func (m *Model) handleChangedKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.Close):
		m.closeChanged()
	case key.Matches(msg, k.Run, k.Accept, k.Confirm): // r runs, as everywhere
		run := m.changed.runnable()
		if run == nil {
			return nil
		}
		res := m.changed.res
		m.closeChanged()
		return m.launchChangedRun(res, run)
	case key.Matches(msg, k.ToggleFile):
		m.changed.toggleFile()
	case key.Matches(msg, k.ToggleAll):
		m.changed.toggleAll()
	case key.Matches(msg, k.Deps):
		m.flags.includeDeps = !m.flags.includeDeps
	case key.Matches(msg, k.Dependents):
		m.flags.includeDependents = !m.flags.includeDependents
	case key.Matches(msg, k.FullRefresh):
		m.flags.fullRefresh = !m.flags.fullRefresh
	case key.Matches(msg, k.API):
		if m.deps.StartAPIRun == nil {
			m.flash = "API runs are not available"
			return nil
		}
		m.flags.api = !m.flags.api
		return m.requestChanged() // an API run compiles the pushed branch
	case key.Matches(msg, k.Base):
		return m.openBase()
	case key.Matches(msg, k.Compile):
		// Retries a base that failed to compile, as c does everywhere.
		m.deps.Engine.Recompile("")
		return m.requestChanged()
	case key.Matches(msg, k.Errors) && m.anyErrors():
		m.closeChanged()
		m.openErrors()
	case key.Matches(msg, k.Up):
		m.moveChanged(-1)
	case key.Matches(msg, k.Down):
		m.moveChanged(1)
	case key.Matches(msg, k.HalfUp):
		m.moveChanged(-5)
	case key.Matches(msg, k.HalfDown):
		m.moveChanged(5)
	case key.Matches(msg, k.Top):
		m.changed.setCursor(0)
		m.changed.offset = 0
	case key.Matches(msg, k.Bottom):
		// The end of the list, removed actions and all.
		m.changed.setCursor(max(len(m.changed.cursorRows())-1, 0))
		m.changed.offset = m.changedLayout(m.geometry().bodyH).clamp(math.MaxInt)
	}
	return nil
}

// launchChangedRun runs run, the checked ones of res's Changed Actions,
// with the current flags.
func (m *Model) launchChangedRun(res *changes.Result, run []changes.Changed) tea.Cmd {
	ids := make([]project.ActionID, len(run))
	targets := make([]project.Target, len(run))
	for i, c := range run {
		ids[i], targets[i] = c.ID, c.Target
	}
	what := countRun(len(ids), len(res.Changed))
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancelRun, m.runAPI, m.runURL = true, cancel, m.flags.api, nil
	m.runLabel, m.runShort = what, what
	m.log.reset(m.styles, what+" "+res.Title())
	m.log.append(m.styles.dim.Render(m.runCommand(ids...)))
	m.runTable.reset()
	if !m.showLog {
		m.showLog = true
		m.layout()
	}
	if m.runAPI {
		return startRunCmd(ctx, m.deps.StartAPIRun, m.flags.apiOptions(m.graph, targets...))
	}
	return startRunCmd(ctx, m.deps.StartRun, m.runOptions(ids...))
}

// stillRun counts the unchecked Changed Actions that d or D runs anyway:
// the deps or dependents, in the local graph, of the checked ones.
func (m Model) stillRun() int {
	if !m.changed.listed() {
		return 0
	}
	return frontend.StillRun(m.graph, m.changed.res, m.changed.skip, m.flags.includeDeps, m.flags.includeDependents)
}

// stillRunNote is the warning that n unchecked actions run anyway.
func stillRunNote(f runFlags, n int) string {
	return frontend.StillRunNote(f.includeDeps, f.includeDependents, n)
}

// changedBoxLayout is the R overlay's lines: a fixed header and footer
// around a list that scrolls, rows lines of it at a time.
type changedBoxLayout struct {
	w, inner             int
	header, list, footer []string
	rows                 int
}

// clamp keeps a first visible row of the list in range.
func (l changedBoxLayout) clamp(offset int) int {
	return min(max(offset, 0), max(len(l.list)-l.rows, 0))
}

// changedLayout lays the R overlay out in bodyH lines.
func (m Model) changedLayout(bodyH int) changedBoxLayout {
	s := m.styles
	c := m.changed
	w := min(m.width-4, 100)
	inner := w - 4

	what := "the working tree"
	if c.head == changes.HeadPushed {
		what = "the pushed branch"
	}
	comparing := m.spinner() + " " + s.warnText.Render("comparing "+what+" with "+m.deps.DefaultBranch+"…")
	var header, list []string
	switch {
	case c.loading && c.res != nil:
		// Asked again: the last answer, dimmed, until the new one comes.
		header = append(header, comparing, s.dim.Render(ansi.Strip(changesSummary(c.res))))
		for _, l := range append(m.changedLines(c.res, inner), m.deletedLines(c.res, inner)...) {
			list = append(list, s.dim.Render(ansi.Strip(l)))
		}
	case c.loading:
		first := "The first time today, this compiles the merge-base, which takes about as long as a compile."
		if m.tool() == "dbt" {
			first = "The first time today, this parses the merge-base, then dbt lists what state:modified selects."
		}
		header = append(header, comparing, s.dim.Render(first))
	case c.err != nil:
		for _, l := range strings.Split(c.err.Error(), "\n") {
			header = append(header, s.errorText.Render(l))
		}
		switch {
		case c.err.Error() == engine.ErrStale.Error(): // also when it came from the engine
			header = append(header, "", s.dim.Render("e shows the compile errors"))
		case strings.Contains(c.err.Error(), "could not compile"):
			header = append(header, "", s.dim.Render("c tries again"))
		}
	case len(c.res.Changed) == 0:
		header = append(header, s.text.Render(c.res.NoChangesMessage()))
		list = m.deletedLines(c.res, inner)
	default:
		header = append(header, s.footerKey.Render(countChanged(len(c.res.Changed)))+" "+s.dim.Render(c.res.Title()),
			s.text.Render(changesSummary(c.res)))
		if checked, total := c.files(); checked < total {
			header = append(header, s.text.Render(fmt.Sprintf("%d of %d files checked", checked, total)))
		}
		list = append(m.changedLines(c.res, inner), m.deletedLines(c.res, inner)...)
	}

	run := c.runnable()
	footer := []string{"", runToggles(s, m.flags, m.deps.StartAPIRun != nil)}
	if m.flags.api {
		footer = append(footer, m.apiGitLine("run"))
	}
	if m.flags.fullRefresh && run != nil {
		footer = append(footer, s.warnText.Render("⚠ full refresh: incremental tables lose their rows and are rebuilt"))
	}
	if n := m.stillRun(); n > 0 {
		footer = append(footer, s.warnText.Render(stillRunNote(m.flags, n)))
	}
	if c.listed() && run == nil {
		footer = append(footer, s.warnText.Render("nothing checked · space includes a file, x all"))
	}
	sep := s.dim.Render(" · ")
	keys := s.footerKey.Render("esc") + " " + s.footerDesc.Render("close")
	if run != nil {
		verb := "run "
		if m.flags.fullRefresh {
			verb = "run full refresh of "
		}
		if m.flags.api {
			verb = strings.TrimSuffix(verb, " ") + " in GCP "
		}
		keys = s.footerKey.Render("r/enter") + " " + s.footerDesc.Render(verb+countRun(len(run), len(c.res.Changed))) + sep + keys
	}
	if !c.loading { // before the rest, which a narrow box cuts off
		keys += m.baseKey(sep)
	}
	if c.listed() {
		keys += sep + s.footerKey.Render("space") + " " + s.footerDesc.Render("toggle file") +
			sep + s.footerKey.Render("x") + " " + s.footerDesc.Render("all")
	}
	if len(list) > 0 {
		move := "scroll"
		if c.listed() {
			move = "move"
		}
		keys += sep + s.footerKey.Render("j/k") + " " + s.footerDesc.Render(move)
	}
	footer = append(footer, keys)

	if len(list) > 0 {
		header = append(header, "")
	}
	rows := max(bodyH-2-len(header)-len(footer), 1)
	return changedBoxLayout{w: w, inner: inner, header: header, list: list, footer: footer, rows: rows}
}

// changedBox draws the R overlay, the cursor's row highlighted.
func (m Model) changedBox(bodyH int) string {
	s := m.styles
	c := m.changed
	lay := m.changedLayout(bodyH)
	offset := lay.clamp(c.offset)
	cursor := -1
	if c.listed() {
		cursor = c.cursor
		if cursor >= offset+lay.rows { // the window shrank
			offset = cursor - lay.rows + 1
		}
	}
	end := min(offset+lay.rows, len(lay.list))
	visible := slices.Clone(lay.list[offset:end])
	if i := cursor - offset; i >= 0 && i < len(visible) {
		plain := ansi.Truncate(ansi.Strip(visible[i]), lay.inner, "…")
		visible[i] = s.selected.Render(plain + strings.Repeat(" ", max(lay.inner-ansi.StringWidth(plain), 0)))
	}
	if note := moreNote(offset, len(lay.list)-end); note != "" && len(visible) > 0 {
		visible[len(visible)-1] = ansi.Truncate(visible[len(visible)-1], lay.inner-ansi.StringWidth(note)-2, "…") + "  " + s.dim.Render(note)
	}

	lines := append(append(append([]string{}, lay.header...), visible...), lay.footer...)
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, lay.inner, "…")
	}
	return s.apiPane("R run changed", "", strings.Join(lines, "\n"), lay.w, min(len(lines)+2, bodyH), true, m.flags.api)
}

// fitRow fits an action's "dataset.name" and its tail in width cells: the
// name, shortened as fitName does, and the gap that right-aligns the tail.
func fitRow(id project.ActionID, tail string, width int) (label, gap string) {
	schema, name := splitID(id)
	label = fitName(schema+"."+name, name, max(width-ansi.StringWidth(tail)-2, 8))
	return label, strings.Repeat(" ", max(width-ansi.StringWidth(label)-ansi.StringWidth(tail), 2))
}

// groupLines draws rows: a file's from fileLine; an action's from row (its
// marker, already styled, the style of its name, and its tail); and a
// summary of the assertions generated from an action. Tails line up on the
// right.
func (m Model) groupLines(rows []frontend.ListRow, width int, fileLine func(file string) string, row func(g *frontend.ListGroup) (mark string, name lipgloss.Style, tail string)) []string {
	s := m.styles
	lines := make([]string, len(rows))
	for i, r := range rows {
		switch r.Kind {
		case frontend.FileRow:
			lines[i] = fileLine(cmp.Or(r.File, "(no file)"))
		case frontend.ActionRow:
			mark, nameStyle, tail := row(r.Group)
			label, gap := fitRow(r.Group.ID, tail, width-4)
			lines[i] = "  " + mark + " " + nameStyle.Render(label) + gap + s.dim.Render(tail)
		case frontend.AssertsRow:
			summary := "↳ " + frontend.AssertionsSummary(r.Group.Asserts)
			tail := frontend.ReasonsOf(r.Group.Asserts)
			if room := width - 4 - ansi.StringWidth(tail) - 2; ansi.StringWidth(summary) > room {
				summary = ansi.Truncate(summary, max(room, 8), "…")
			}
			gap := strings.Repeat(" ", max(width-4-ansi.StringWidth(summary)-ansi.StringWidth(tail), 2))
			lines[i] = "    " + s.dim.Render(summary+gap+tail)
		}
	}
	return lines
}

// changedLines lists the Changed Actions under their files, each checked
// or not: "  ● dataset.name     table · sql, config", with the assertions
// generated from a table summarised under it. An unchecked file's actions
// are dimmed.
func (m Model) changedLines(res *changes.Result, width int) []string {
	s := m.styles
	skip := m.changed.skip
	rows := frontend.ChangedListRows(res)
	renames := renamedFrom(res)
	lines := m.groupLines(rows, width, func(file string) string {
		label := file
		if from := renames[file]; from != "" {
			label += "  " + from
		}
		if skip[file] {
			return s.dim.Render("[ ] " + label)
		}
		return s.text.Render("[x]") + " " + s.dim.Render(label)
	}, func(g *frontend.ListGroup) (string, lipgloss.Style, string) {
		if !g.Listed {
			return s.dim.Render("○"), s.dim, "unchanged"
		}
		return s.warnText.Render("●"), s.text, g.Type + " · " + frontend.ReasonsOf([]frontend.ListItem{g.ListItem})
	})
	for i, r := range rows {
		if r.Kind != frontend.FileRow && skip[r.File] {
			lines[i] = s.dim.Render(ansi.Strip(lines[i]))
		}
	}
	return lines
}

// deletedLines lists the actions removed on the branch, which don't run.
func (m Model) deletedLines(res *changes.Result, width int) []string {
	deleted := removed(res)
	if len(deleted) == 0 {
		return nil
	}
	s := m.styles
	items := make([]frontend.ListItem, len(deleted))
	for i, d := range deleted {
		items[i] = frontend.ListItem{ID: d.ID, Parent: d.Parent, FileName: d.FileName, Type: d.Type}
	}
	renamedTo := map[project.ActionID]bool{} // their generated assertions are still listed
	for _, d := range res.Deleted {
		if d.RenamedTo != "" {
			renamedTo[d.ID] = true
		}
	}
	lines := []string{s.dim.Render("removed on this branch, not run")}
	return append(lines, m.groupLines(frontend.GroupRows(frontend.GroupItems(items)), width, func(file string) string {
		return s.dim.Render(file)
	}, func(g *frontend.ListGroup) (string, lipgloss.Style, string) {
		if !g.Listed {
			if renamedTo[g.ID] {
				return s.dim.Render("○"), s.dim, renamedReason
			}
			return s.dim.Render("○"), s.dim, "kept"
		}
		return s.dim.Render("✗"), s.dim, g.Type
	})...)
}

// changedSet is res's Changed Actions by ID, for the palette's markers.
func changedSet(res *changes.Result) map[project.ActionID]bool {
	if res == nil {
		return nil
	}
	set := make(map[project.ActionID]bool, len(res.Changed))
	for _, c := range res.Changed {
		set[c.ID] = true
	}
	return set
}
