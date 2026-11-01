package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
)

// The I overlay shows the column impact of the Changed Actions (see package
// impact), drawn for the terminal from the report; y copies the Markdown a
// pull request gets, which folds and shortens its lists. The engine does the work and keeps the report:
// closing the overlay while it runs lets it finish, and opening it again
// shows it at once, flagged when the branch has changed since, until c
// checks again. As with R, a compares the pushed branch instead of the
// working tree.

// impactOverlay is the I overlay's state.
type impactOverlay struct {
	open    bool
	loading bool
	// seq tells a request's answers from an earlier one's.
	seq      int
	head     changes.Head
	base     string // the branch the report compares with
	report   *impact.Report
	markdown string
	// lines is the report drawn linesW wide, redone when the styles or the
	// width change.
	lines    []string
	linesW   int
	err      error
	progress *impact.Progress
	// offset is the first visible line.
	offset int
	cancel context.CancelFunc
}

// impactMsg answers the request seq.
type impactMsg struct {
	seq    int
	report *impact.Report
	err    error
}

// impactProgressMsg is how far the request seq got.
type impactProgressMsg struct {
	seq  int
	p    impact.Progress
	ch   <-chan impact.Progress
	done <-chan struct{}
}

// impactTimeout bounds waiting for a report: a base compile, then dry runs
// and lineage searches. The engine carries on if the overlay stops waiting.
const impactTimeout = 30 * time.Minute

// openImpact opens the I overlay and asks for the report.
func (m *Model) openImpact() tea.Cmd {
	if m.deps.ColumnImpact == nil {
		m.flash = "column impact is not available"
		return nil
	}
	if cmd, asking := m.askProd(prodPromptImpact); asking {
		return cmd
	}
	m.zoomed = false
	m.impact.open = true
	return m.requestImpact(false)
}

// requestImpact asks for the report on the head the a toggle picks; with
// refresh, a new one even when the engine has one for the same changes.
func (m *Model) requestImpact(refresh bool) tea.Cmd {
	o := &m.impact
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	o.seq++
	o.err, o.progress = nil, nil
	head := changes.HeadWorktree
	if m.flags.api {
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
	ctx, cancel := context.WithTimeout(context.Background(), impactTimeout)
	o.cancel = cancel
	seq, fn := o.seq, m.deps.ColumnImpact
	req := engine.ImpactRequest{DefaultBranch: m.deps.DefaultBranch, Head: head, Refresh: refresh,
		DevCompilerOptions: m.deps.DevCompilerOptions, ProdCompilerOptions: m.deps.ProdCompilerOptions, Unset: m.deps.Unset}
	ch := make(chan impact.Progress, 1)
	done := make(chan struct{})
	progress := func(p impact.Progress) {
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
	return tea.Batch(m.startAnim(), waitImpactProgress(seq, ch, done), func() tea.Msg {
		defer close(done)
		r, err := fn(ctx, req, progress)
		return impactMsg{seq: seq, report: r, err: err}
	})
}

func waitImpactProgress(seq int, ch <-chan impact.Progress, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case p := <-ch:
			return impactProgressMsg{seq: seq, p: p, ch: ch, done: done}
		case <-done:
			return nil
		}
	}
}

func (m *Model) handleImpactProgress(msg impactProgressMsg) tea.Cmd {
	o := &m.impact
	if msg.seq != o.seq || !o.loading {
		return nil
	}
	o.progress = &msg.p
	return waitImpactProgress(msg.seq, msg.ch, msg.done)
}

func (m *Model) handleImpact(msg impactMsg) {
	o := &m.impact
	if msg.seq != o.seq || !o.loading {
		return // an answer nobody is waiting for; the engine kept it
	}
	o.loading, o.progress, o.err = false, nil, msg.err
	if msg.err != nil {
		return
	}
	o.report, o.markdown = msg.report, impact.Markdown(msg.report)
	o.offset = 0
	m.drawImpact()
}

// impactInner is how wide the I overlay's lines are.
func (m Model) impactInner() int { return max(m.width-4, 20) - 4 }

// drawImpact draws the report for the current styles and width.
func (m *Model) drawImpact() {
	if o := &m.impact; o.report != nil {
		o.linesW = m.impactInner()
		o.lines = m.styles.impactReportLines(o.report, o.linesW)
	}
}

func (m *Model) closeImpact() {
	o := &m.impact
	if o.cancel != nil {
		o.cancel()
	}
	*o = impactOverlay{seq: o.seq}
}

// impactStale says whether the branch has changed since the report on
// show was checked: the engine said so, or, for the working tree, its
// Changed Actions have moved on since.
func (m Model) impactStale() bool {
	r := m.impact.report
	if r == nil {
		return false
	}
	return r.Stale || m.impact.head == changes.HeadWorktree && m.impactSig != "" && m.impactSig != r.Signature
}

// copyImpact copies the report's Markdown.
func (m *Model) copyImpact() tea.Cmd {
	text := m.impact.markdown
	if text == "" || m.impact.loading {
		m.flash = "no report to copy yet"
		return nil
	}
	m.flash = "copied the column impact report as Markdown"
	return m.copyText(text)
}

// copyText puts text on the clipboard: by OSC 52, and by the system's
// clipboard too when xf has one.
func (m *Model) copyText(text string) tea.Cmd {
	cmds := []tea.Cmd{tea.SetClipboard(text)}
	if c := m.deps.Copy; c != nil {
		cmds = append(cmds, func() tea.Msg {
			_ = c(text) // OSC 52 may still have worked
			return nil
		})
	}
	return tea.Batch(cmds...)
}

// scrollImpact moves the report by delta lines, within it.
func (m *Model) scrollImpact(delta int) {
	lay := m.impactLayout(m.geometry().bodyH)
	m.impact.offset = lay.clamp(m.impact.offset + delta)
}

// handleImpactKey handles a key while the I overlay is open.
func (m *Model) handleImpactKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.impact
	page := max(m.impactLayout(m.geometry().bodyH).rows/2, 1)
	switch {
	case key.Matches(msg, k.Close):
		m.closeImpact()
	case key.Matches(msg, k.CopyReport):
		return m.copyImpact()
	case key.Matches(msg, k.Compile):
		if o.err != nil && strings.Contains(o.err.Error(), "could not compile") {
			m.deps.Engine.Recompile("") // retries a base that failed to compile
		}
		return m.requestImpact(true)
	case key.Matches(msg, k.API):
		if m.deps.StartAPIRun == nil {
			m.flash = "API runs are not available"
			return nil
		}
		m.flags.api = !m.flags.api
		return m.requestImpact(false) // an API run compiles the pushed branch
	case key.Matches(msg, k.Base):
		return m.openBase()
	case key.Matches(msg, k.Errors) && m.anyErrors():
		m.closeImpact()
		m.openErrors()
	case key.Matches(msg, k.Up):
		m.scrollImpact(-1)
	case key.Matches(msg, k.Down):
		m.scrollImpact(1)
	case key.Matches(msg, k.HalfUp):
		m.scrollImpact(-page)
	case key.Matches(msg, k.HalfDown):
		m.scrollImpact(page)
	case key.Matches(msg, k.Top):
		o.offset = 0
	case key.Matches(msg, k.Bottom):
		m.scrollImpact(len(o.lines))
	}
	return nil
}

// impactBoxLayout is the I overlay's lines: a fixed header and footer
// around the report, which scrolls rows lines at a time.
type impactBoxLayout struct {
	w, inner             int
	header, list, footer []string
	rows                 int
}

// clamp keeps a first visible line of the report in range.
func (l impactBoxLayout) clamp(offset int) int {
	return min(max(offset, 0), max(len(l.list)-l.rows, 0))
}

// impactProgressText is "Dry running 3/7".
func impactProgressText(p *impact.Progress) string {
	if p == nil {
		return "Working out the changed actions"
	}
	if p.Total == 0 {
		return p.Phase
	}
	return fmt.Sprintf("%s %d/%d", p.Phase, p.Done, p.Total)
}

// impactLayout lays the I overlay out in bodyH lines.
func (m Model) impactLayout(bodyH int) impactBoxLayout {
	s := m.styles
	o := m.impact
	inner := m.impactInner()
	w := inner + 4

	what := "the working tree"
	if o.head == changes.HeadPushed {
		what = "the pushed branch"
	}
	checking := m.spinner() + " " + s.warnText.Render("checking the column impact of "+what+" against "+m.deps.DefaultBranch+"…") +
		"  " + s.dim.Render(impactProgressText(o.progress))
	var header, list []string
	switch {
	case o.loading && o.report != nil:
		// Asked again: the last report, dimmed, until the new one comes.
		header = append(header, checking)
		for _, l := range o.lines {
			list = append(list, s.dim.Render(ansi.Strip(l)))
		}
	case o.loading:
		header = append(header, checking,
			s.dim.Render("Dry runs every changed table, view and incremental table, compares it with the table in BigQuery,"),
			s.dim.Render("then asks Dataplex who reads the columns it drops or retypes. The first time today, this also"),
			s.dim.Render("compiles the merge-base."))
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
		if m.impactStale() {
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

// impactBox draws the I overlay.
func (m Model) impactBox(bodyH int) string {
	s := m.styles
	lay := m.impactLayout(bodyH)
	offset := lay.clamp(m.impact.offset)
	end := min(offset+lay.rows, len(lay.list))
	visible := make([]string, 0, end-offset)
	for _, l := range lay.list[offset:end] {
		visible = append(visible, l)
	}
	if note := moreNote(offset, len(lay.list)-end); note != "" && len(visible) > 0 {
		visible[len(visible)-1] = ansi.Truncate(visible[len(visible)-1], lay.inner-ansi.StringWidth(note)-2, "…") + "  " + s.dim.Render(note)
	}
	lines := append(append(append([]string{}, lay.header...), visible...), lay.footer...)
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, lay.inner, "…")
	}
	return s.apiPane("I column impact", "", strings.Join(lines, "\n"), lay.w, min(len(lines)+2, bodyH), true, m.flags.api)
}
