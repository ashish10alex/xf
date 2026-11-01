package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
)

// The C overlay shows what the Changed Actions would scan in BigQuery (see
// package cost), drawn for the terminal from the report; y copies the
// Markdown a pull request gets. It works as the I overlay does: the engine
// does the work and keeps the report, closing the overlay lets a check
// finish, opening it again shows the report at once, flagged when the
// branch has changed since, until c checks again, and a compares the
// pushed branch. The branch's estimates are shown as soon as they are in;
// what each action scanned at the merge-base follows.

// costOverlay is the C overlay's state.
type costOverlay struct {
	open    bool
	loading bool
	// seq tells a request's answers from an earlier one's.
	seq      int
	head     changes.Head
	base     string // the branch the report compares with
	report   *cost.Report
	markdown string
	// lines is the report drawn linesW wide, redone when the styles or the
	// width change.
	lines    []string
	linesW   int
	err      error
	progress *cost.Progress
	// offset is the first visible line.
	offset int
	cancel context.CancelFunc
}

// costMsg answers the request seq.
type costMsg struct {
	seq    int
	report *cost.Report
	err    error
}

// costProgressMsg is how far the request seq got.
type costProgressMsg struct {
	seq  int
	p    cost.Progress
	ch   <-chan cost.Progress
	done <-chan struct{}
}

// costTimeout bounds waiting for a report: a base compile, then dry runs.
// The engine carries on if the overlay stops waiting.
const costTimeout = 30 * time.Minute

// openCost opens the C overlay and asks for the report.
func (m *Model) openCost() tea.Cmd {
	if m.deps.QueryCost == nil {
		m.flash = "query cost is not available"
		return nil
	}
	if cmd, asking := m.askProd(prodPromptCost); asking {
		return cmd
	}
	m.zoomed = false
	m.cost.open = true
	return m.requestCost(false)
}

// requestCost asks for the report on the head the a toggle picks; with
// refresh, a new one even when the engine has one for the same changes.
func (m *Model) requestCost(refresh bool) tea.Cmd {
	o := &m.cost
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	o.seq++
	o.err, o.progress = nil, nil
	head := changes.HeadWorktree
	if m.flags.api && m.deps.StartAPIRun != nil {
		head = changes.HeadPushed
	}
	if head != o.head || m.deps.DefaultBranch != o.base {
		o.report, o.markdown, o.lines = nil, "", nil // another comparison
	}
	o.head, o.base = head, m.deps.DefaultBranch
	if head == changes.HeadWorktree && m.hasErrors() {
		o.loading, o.report, o.err = false, nil, engine.ErrStale
		return nil
	}
	o.loading = true
	ctx, cancel := context.WithTimeout(context.Background(), costTimeout)
	o.cancel = cancel
	seq, fn := o.seq, m.deps.QueryCost
	req := engine.CostRequest{DefaultBranch: m.deps.DefaultBranch, Head: head, Refresh: refresh, ProdCompilerOptions: m.deps.ProdCompilerOptions, DbtProdTarget: m.deps.DbtProdTarget, Unset: m.deps.Unset}
	ch := make(chan cost.Progress, 1)
	done := make(chan struct{})
	progress := func(p cost.Progress) {
		for { // keep only the newest
			select {
			case ch <- p:
				return
			default:
			}
			select {
			case <-ch:
			default:
			}
		}
	}
	return tea.Batch(m.startAnim(), waitCostProgress(seq, ch, done), func() tea.Msg {
		defer close(done)
		r, err := fn(ctx, req, progress)
		return costMsg{seq: seq, report: r, err: err}
	})
}

func waitCostProgress(seq int, ch <-chan cost.Progress, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case p := <-ch:
			return costProgressMsg{seq: seq, p: p, ch: ch, done: done}
		case <-done:
			return nil
		}
	}
}

func (m *Model) handleCostProgress(msg costProgressMsg) tea.Cmd {
	o := &m.cost
	if msg.seq != o.seq || !o.loading {
		return nil
	}
	o.progress = &msg.p
	if msg.p.Partial != nil {
		// The branch's estimates, ahead of the merge-base's.
		m.showCost(msg.p.Partial)
	}
	return waitCostProgress(msg.seq, msg.ch, msg.done)
}

func (m *Model) handleCost(msg costMsg) {
	o := &m.cost
	if msg.seq != o.seq || !o.loading {
		return // an answer nobody is waiting for; the engine kept it
	}
	o.loading, o.progress, o.err = false, nil, msg.err
	if msg.err != nil {
		return
	}
	m.showCost(msg.report)
}

// showCost puts r on show, keeping the place when it replaces the same
// report without its merge-base.
func (m *Model) showCost(r *cost.Report) {
	o := &m.cost
	if o.report == nil || !o.report.BasePending {
		o.offset = 0
	}
	o.report, o.markdown = r, cost.Markdown(r, cost.Options{})
	m.drawCost()
}

// drawCost draws the report for the current styles and width.
func (m *Model) drawCost() {
	if o := &m.cost; o.report != nil {
		o.linesW = m.impactInner()
		o.lines = m.styles.costReportLines(o.report, o.linesW)
	}
}

func (m *Model) closeCost() {
	o := &m.cost
	if o.cancel != nil {
		o.cancel()
	}
	*o = costOverlay{seq: o.seq}
}

// costPartial says whether the report on show still waits for its
// merge-base.
func (m Model) costPartial() bool {
	return m.cost.loading && m.cost.report != nil && m.cost.report.BasePending
}

// costStale says whether the branch has changed since the report on show
// was checked, as impactStale does.
func (m Model) costStale() bool {
	r := m.cost.report
	if r == nil {
		return false
	}
	return r.Stale || m.cost.head == changes.HeadWorktree && m.impactSig != "" && m.impactSig != r.Signature
}

// copyCost copies the report's Markdown.
func (m *Model) copyCost() tea.Cmd {
	if m.cost.markdown == "" || m.cost.loading {
		m.flash = "no report to copy yet"
		return nil
	}
	m.flash = "copied the query cost report as Markdown"
	return m.copyText(m.cost.markdown)
}

// scrollCost moves the report by delta lines, within it.
func (m *Model) scrollCost(delta int) {
	lay := m.costLayout(m.geometry().bodyH)
	m.cost.offset = lay.clamp(m.cost.offset + delta)
}

// handleCostKey handles a key while the C overlay is open.
func (m *Model) handleCostKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.cost
	page := max(m.costLayout(m.geometry().bodyH).rows/2, 1)
	switch {
	case key.Matches(msg, k.Close):
		m.closeCost()
	case key.Matches(msg, k.CopyReport):
		return m.copyCost()
	case key.Matches(msg, k.Compile):
		if o.err != nil && strings.Contains(o.err.Error(), "could not compile") {
			m.deps.Engine.Recompile("") // retries a base that failed to compile
		}
		return m.requestCost(true)
	case key.Matches(msg, k.API):
		if m.deps.StartAPIRun == nil {
			m.flash = "only the working tree can be estimated"
			return nil
		}
		m.flags.api = !m.flags.api
		return m.requestCost(false) // an API run compiles the pushed branch
	case key.Matches(msg, k.Base):
		return m.openBase()
	case key.Matches(msg, k.Errors) && m.anyErrors():
		m.closeCost()
		m.openErrors()
	case key.Matches(msg, k.Up):
		m.scrollCost(-1)
	case key.Matches(msg, k.Down):
		m.scrollCost(1)
	case key.Matches(msg, k.HalfUp):
		m.scrollCost(-page)
	case key.Matches(msg, k.HalfDown):
		m.scrollCost(page)
	case key.Matches(msg, k.Top):
		o.offset = 0
	case key.Matches(msg, k.Bottom):
		m.scrollCost(len(o.lines))
	}
	return nil
}

// costProgressText is "Dry running 3/7".
func costProgressText(p *cost.Progress) string {
	if p == nil {
		return "Working out the changed actions"
	}
	if p.Total == 0 {
		return p.Phase
	}
	return fmt.Sprintf("%s %d/%d", p.Phase, p.Done, p.Total)
}

// costLayout lays the C overlay out in bodyH lines.
func (m Model) costLayout(bodyH int) impactBoxLayout {
	s := m.styles
	o := m.cost
	inner := m.impactInner()
	w := inner + 4

	what := "the working tree"
	if o.head == changes.HeadPushed {
		what = "the pushed branch"
	}
	checking := m.spinner() + " " + s.warnText.Render("estimating what "+what+" changes against "+m.deps.DefaultBranch+" would scan…") +
		"  " + s.dim.Render(costProgressText(o.progress))
	var header, list []string
	switch {
	case m.costPartial():
		header = append(header, m.spinner()+" "+s.warnText.Render("estimating the same actions at the merge-base…")+"  "+s.dim.Render(costProgressText(o.progress)))
		list = o.lines
	case o.loading && o.report != nil:
		// Asked again: the last report, dimmed, until the new one comes.
		header = append(header, checking)
		for _, l := range o.lines {
			list = append(list, s.dim.Render(ansi.Strip(l)))
		}
	case o.loading:
		header = append(header, checking,
			s.dim.Render("Dry runs every changed action with a query, as it is on the branch and as it was at the"),
			s.dim.Render("merge-base. Dry runs are free and run nothing. The first time today, this also compiles the"),
			s.dim.Render("merge-base."))
	case o.err != nil:
		for _, l := range strings.Split(o.err.Error(), "\n") {
			header = append(header, s.errorText.Render(l))
		}
		switch {
		case o.err.Error() == engine.ErrStale.Error(): // also when it came from the engine
			header = append(header, "", s.dim.Render("e shows the compile errors"))
		default:
			header = append(header, "", s.dim.Render("c tries again"))
		}
	case o.report != nil:
		when := o.report.CheckedAt.Local().Format("15:04")
		if m.costStale() {
			header = append(header, s.warnText.Render("⚠ the branch has changed since this was checked at "+when+" · c checks again"))
		} else {
			label := ""
			if c := o.report.Comparison; c != nil {
				label = " · " + c.HeadLabel
			}
			header = append(header, s.dim.Render("checked at "+when+label))
		}
		list = o.lines
	}

	sep := s.dim.Render(" · ")
	keys := s.footerKey.Render("esc") + " " + s.footerDesc.Render("close")
	if o.report != nil && !o.loading {
		keys = s.footerKey.Render("y") + " " + s.footerDesc.Render("copy Markdown") + sep + keys
	}
	if !o.loading {
		keys += sep + s.footerKey.Render("c") + " " + s.footerDesc.Render("check again")
	}
	keys += m.baseKey(sep)
	if m.deps.StartAPIRun != nil {
		against := "pushed branch"
		if o.head == changes.HeadPushed {
			against = "working tree"
		}
		keys += sep + s.footerKey.Render("a") + " " + s.footerDesc.Render(against)
	}
	if len(list) > 0 {
		keys += sep + s.footerKey.Render("j/k") + " " + s.footerDesc.Render("scroll")
	}
	footer := []string{"", keys}
	if m.flags.api {
		footer = []string{"", m.apiGitLine("check"), keys}
	}

	if len(list) > 0 {
		header = append(header, "")
	}
	rows := max(bodyH-2-len(header)-len(footer), 1)
	return impactBoxLayout{w: w, inner: inner, header: header, list: list, footer: footer, rows: rows}
}

// costBox draws the C overlay.
func (m Model) costBox(bodyH int) string {
	s := m.styles
	lay := m.costLayout(bodyH)
	offset := lay.clamp(m.cost.offset)
	end := min(offset+lay.rows, len(lay.list))
	visible := make([]string, 0, end-offset)
	visible = append(visible, lay.list[offset:end]...)
	if note := moreNote(offset, len(lay.list)-end); note != "" && len(visible) > 0 {
		visible[len(visible)-1] = ansi.Truncate(visible[len(visible)-1], lay.inner-ansi.StringWidth(note)-2, "…") + "  " + s.dim.Render(note)
	}
	lines := append(append(append([]string{}, lay.header...), visible...), lay.footer...)
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, lay.inner, "…")
	}
	return s.apiPane("C query cost", "", strings.Join(lines, "\n"), lay.w, min(len(lines)+2, bodyH), true, m.flags.api)
}

// costReportLines is the report drawn for the terminal, width wide: what
// was compared, the total, a table of the estimated actions, most bytes
// first, and the actions that couldn't be estimated.
func (s styles) costReportLines(r *cost.Report, width int) []string {
	var lines []string
	if c := r.Comparison; c != nil {
		lines = append(lines, s.text.Render(c.HeadRef)+s.dim.Render(" vs ")+s.text.Render(c.BaseRef)+
			s.dim.Render(" @ "+c.MergeBase[:min(len(c.MergeBase), 7)]))
	}
	if r.Compiled != "" {
		lines = append(lines, s.dim.Render("compiled with ")+s.text.Render(r.Compiled))
	}
	lines = append(lines, s.unsetHintLines(cost.UnsetHint(r), width)...)
	lines = append(lines, "", s.text.Bold(true).Render(cost.TotalLine(r, cost.Options{})))

	var rows [][]string
	var warned []bool
	var others []cost.Action
	for _, a := range r.Actions {
		if a.Estimate == nil {
			others = append(others, a)
			continue
		}
		var notes []string
		for _, u := range a.Unfiltered {
			notes = append(notes, "⚠ no partition filter: "+u.Column+" of "+impact.ShortTable(u.Table))
		}
		if a.Full != nil {
			notes = append(notes, "full refresh "+cost.Size(*a.Full))
		}
		change := cost.DeltaText(a)
		if change == "" && r.BasePending && !a.New {
			change = "…"
		}
		rows = append(rows, []string{impact.ShortTable(a.Name), a.Type, cost.Size(*a.Estimate),
			cost.FormatUSD(cost.Options{}.USD(a.Estimate.Bytes)), change, strings.Join(notes, " · ")})
		warned = append(warned, len(a.Unfiltered) > 0)
	}
	switch {
	case len(r.Actions) == 0:
		lines = append(lines, s.dim.Render("Nothing to estimate: the branch changes no action with a query to dry run."))
	case len(rows) > 0:
		lines = append(lines, "")
		lines = append(lines, s.impactTable(width, []string{"Action", "Type", "Scans", "Cost", "Change", "Notes"}, rows, func(row, col int) lipgloss.Style {
			switch {
			case col == 5 && row >= 0 && row < len(warned) && warned[row]:
				return s.warnText
			case col == 1 || col >= 4:
				return s.dim
			}
			return s.text
		})...)
	}
	if r.BaseError != "" {
		lines = append(lines, "")
		lines = append(lines, wrapIndented(s.warnText.Render("No change is shown: the merge-base could not be compiled. ")+s.dim.Render(strings.ReplaceAll(r.BaseError, "\n", " ")), width)...)
	}
	if len(others) > 0 {
		lines = append(lines, "", s.groupHeader.Render("Not estimated ")+s.dim.Render(fmt.Sprintf("(%d, not in the total)", len(others))))
		for _, a := range others {
			lines = append(lines, wrapIndented("  "+s.text.Render(impact.ShortTable(a.Name))+s.dim.Render(": "+strings.ReplaceAll(a.Reason, "\n", " ")), width)...)
		}
	}

	lines = append(lines, "")
	for _, l := range strings.Split(ansi.Wrap(costFootnote, width, ""), "\n") {
		lines = append(lines, s.dim.Render(l))
	}
	return lines
}
