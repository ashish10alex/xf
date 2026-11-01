package ui

import (
	"errors"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// The Actions pane lists every action the selected action's file defines
// (a table and the assertions generated from its config, say), like the
// cards at the top of the extension's Compiled Query panel. Each is a row
// with its kind and full target, and under it a line for each dry run and
// one for its partitioning. The selected row is the action the other panes
// show; j/k in the pane, or ] and [ anywhere, move it.

// detailLabelW is the width of the labels in an action's detail lines
// ("incremental" is the longest).
const detailLabelW = len("incremental")

// actionsLines is the Actions pane's content: the lines, the line each
// action's row starts at (starts[i] for fileActions()[i]) and the index of
// the selected action.
func (m Model) actionsLines(width int, focused bool) (lines []string, starts []int, sel int) {
	s := m.styles
	switch {
	case m.graph == nil && m.compiling:
		return strings.Split(m.compilingBody(width), "\n"), nil, -1
	case m.graph == nil:
		return []string{s.dim.Render("no compile result — press e for details, c to retry")}, nil, -1
	case m.selected == nil:
		return []string{s.dim.Render("press ") + s.footerKey.Render("/") + s.dim.Render(" to find an action")}, nil, -1
	}
	actions := m.paneActions()
	kindW := 0
	for _, a := range actions {
		kindW = max(kindW, len(a.Kind.String()))
	}
	sel = -1
	authFailed := false
	for i, a := range actions {
		if a == m.selected {
			sel = i
		}
		starts = append(starts, len(lines))
		lines = append(lines, m.actionRow(a, kindW, width, a == m.selected, focused)...)
		details, auth := m.actionDetails(a)
		authFailed = authFailed || auth
		for _, d := range details {
			lines = append(lines, detailLines(s, d, width)...)
		}
	}
	if authFailed {
		lines = append(lines, s.warnText.Render("dry runs need Google credentials: "+bigquery.AuthHint+", then c"))
		if hint := m.accountHint(); hint != "" {
			lines = append(lines, s.dim.Render(hint))
		}
	}
	return lines, starts, sel
}

// paneActions is what the Actions pane lists: the siblings of its anchor
// (see paneAnchor), the selected action among them: the actions in its
// file, with a dbt model's tests.
func (m Model) paneActions() []*project.Action {
	switch {
	case m.selected == nil:
		return nil
	case m.graph == nil:
		return []*project.Action{m.selected}
	}
	return m.graph.Siblings(m.paneAnchor())
}

// paneAnchor is the action whose siblings the Actions pane lists: the one
// the pane stayed on (paneOf) while they still include the selected
// action, else the selected action.
func (m Model) paneAnchor() *project.Action {
	if m.graph == nil || m.selected == nil {
		return m.selected
	}
	if a := m.graph.Actions[m.paneOf]; a != nil && m.paneOf != "" && slices.Contains(m.graph.Siblings(a), m.selected) {
		return a
	}
	return m.selected
}

// actionRow is an action's first line, "▸ TABLE  project.dataset.name", the
// name wrapped under itself when it is wider than the pane.
func (m Model) actionRow(a *project.Action, kindW, width int, sel, focused bool) []string {
	s := m.styles
	st := func(x lipgloss.Style) lipgloss.Style {
		if sel && focused {
			return x.Background(s.selBg)
		}
		return x
	}
	marker := "  "
	if sel {
		marker = st(s.okText).Render("▸ ")
	}
	kind := strings.ToUpper(a.Kind.String())
	kind += strings.Repeat(" ", kindW-len(kind)+2)
	nameCol := 2 + len(kind)

	nameStyle := s.text
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	text := string(a.ID)
	if a.Disabled {
		text += "  disabled"
	}
	chunks := wrapValue(text, max(width-nameCol, 8))
	var lines []string
	used := 0 // bytes of text already on earlier lines
	for i, c := range chunks {
		lead := marker + st(s.kind(a.Kind.Short()).Bold(true)).Render(kind)
		if i > 0 {
			lead = st(s.text).Render(strings.Repeat(" ", nameCol))
		}
		// "disabled" is a warning, whichever line it lands on.
		name := min(max(len(a.ID)-used, 0), len(c))
		body := st(nameStyle).Render(c[:name]) + st(s.warnText).Render(c[name:])
		used += len(c)
		for used < len(text) && text[used] == ' ' { // the space wrapValue broke at
			used++
		}
		gap := max(width-nameCol-ansi.StringWidth(c), 0)
		lines = append(lines, lead+body+st(s.text).Render(strings.Repeat(" ", gap)))
	}
	return lines
}

// detail is one line under an action's row: a label and its value.
type detail struct {
	label, value string
	style        lipgloss.Style
}

// detailLines draws d as "    label        value", the value wrapped under
// itself when it is wider than the pane.
func detailLines(s styles, d detail, width int) []string {
	lead := "    " + s.dim.Render(d.label+strings.Repeat(" ", detailLabelW-len(d.label)+2))
	indent := strings.Repeat(" ", 4+detailLabelW+2)
	var lines []string
	for i, c := range wrapValue(d.value, max(width-len(indent), 8)) {
		if i > 0 {
			lead = indent
		}
		lines = append(lines, lead+d.style.Render(c))
	}
	return lines
}

// wrapValue breaks s into lines of at most width cells, at spaces, dots and
// underscores where it can.
func wrapValue(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, width, "._"), "\n")
}

// actionDetails are the lines under a's row: how it changed on the branch,
// if it is a Changed Action, one per dry run (full and
// incremental for an incremental table, the one the SQL pane shows in full
// colour), when its table was last updated, then its partitioning, if it
// has any.
// auth reports a dry run that failed for want of credentials.
func (m Model) actionDetails(a *project.Action) (details []detail, auth bool) {
	s := m.styles
	if c, ok := m.changes.Find(a.ID); ok {
		details = append(details, detail{label: "changed", value: c.ReasonsText() + " " + m.changes.Comparison(), style: s.warnText})
	}
	if m.deps.DryRun != nil {
		project, location := m.dryRunTarget(a)
		for _, q := range a.DryRunQueries() {
			e := m.dryRuns[dryRunKey(project, location, q.SQL)]
			d := detail{label: q.Label, style: s.text}
			if d.label == "" {
				d.label = "dry run"
			} else if (q.Label == "incremental") != m.incremental {
				d.style = s.dim
			}
			switch {
			case m.uncompiled():
				d.value, d.style = "not compiled", s.dim
			case m.sqlNote(a) != "":
				d.value, d.style = m.sqlNote(a), s.dim
			case e == nil || !e.done:
				d.value, d.style = "running…", s.dim
			case errors.Is(e.err, bigquery.ErrAuth):
				auth = true
				d.value, d.style = "no credentials", s.warnText
			case e.err != nil:
				msg := e.err.Error()
				if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
					msg = apiErr.Message
				}
				d.value, d.style = "✗ "+firstLine(msg), s.errorText
			default:
				text, warn := formatDryRun(e.res)
				if warn {
					d.style = s.warnText
				}
				d.value = text
			}
			details = append(details, d)
		}
	}
	if d, ok := m.updatedDetail(a); ok {
		details = append(details, d)
	}
	if a.PartitionBy != "" {
		details = append(details, detail{label: "partition", value: a.PartitionBy, style: s.text})
	}
	return details, auth
}

// fitName fits an action's qualified name ("dataset.name") in width cells:
// whole if it fits, else the bare name, else the end of the name after an
// ellipsis. The name tells actions apart more than their dataset, and
// assertions generated from one table differ only at the end.
func fitName(qualified, name string, width int) string {
	switch {
	case ansi.StringWidth(qualified) <= width:
		return qualified
	case ansi.StringWidth(name) <= width:
		return name
	case width <= 1:
		return "…"
	}
	return ansi.TruncateLeft(name, ansi.StringWidth(name)-width+1, "…")
}

// actionsTitle is "1 actions · definitions/orders.sqlx".
func (m Model) actionsTitle() string {
	t := "1 actions"
	if m.selected != nil && m.selected.FileName != "" {
		t += " · " + m.selected.FileName
	}
	return t
}

// actionsBody is the visible part of the Actions pane and the note for its
// bottom border when rows are scrolled out of view.
func (m Model) actionsBody(width, height int) (body, footer string) {
	lines, starts, _ := m.actionsLines(width, m.focus == paneActions)
	off := min(m.actionsOffset, max(len(lines)-height, 0))
	end := min(off+height, len(lines))
	var above, below int
	for _, st := range starts {
		switch {
		case st < off:
			above++
		case st >= end:
			below++
		}
	}
	return strings.Join(lines[off:end], "\n"), moreNote(above, below)
}

// scrollActions keeps the selected action's row, and the error lines under
// it, in view.
func (m *Model) scrollActions(width, height int) {
	lines, starts, sel := m.actionsLines(width, false)
	if sel < 0 {
		m.actionsOffset = 0
		return
	}
	first, last := starts[sel], len(lines)-1
	if sel+1 < len(starts) {
		last = starts[sel+1] - 1
	}
	if last >= m.actionsOffset+height {
		m.actionsOffset = last - height + 1
	}
	if first < m.actionsOffset {
		m.actionsOffset = first
	}
	m.actionsOffset = min(max(m.actionsOffset, 0), max(len(lines)-height, 0))
}

// moveInActions selects the previous (dir -1) or next (dir 1) action in the
// pane, stopping at the ends; dir ±2 jumps to the first or last.
func (m *Model) moveInActions(dir int) {
	actions := m.paneActions()
	if len(actions) == 0 {
		return
	}
	i := 0
	for j, a := range actions {
		if a == m.selected {
			i = j
		}
	}
	switch dir {
	case -2:
		i = 0
	case 2:
		i = len(actions) - 1
	default:
		i = min(max(i+dir, 0), len(actions)-1)
	}
	if actions[i] != m.selected {
		m.selectInPane(actions[i])
	}
}

// actionAtLine is the action whose row (or error lines) is at line i of the
// pane's content.
func (m Model) actionAtLine(i int) *project.Action {
	_, starts, _ := m.actionsLines(m.width-2, false)
	actions := m.paneActions()
	for j := len(starts) - 1; j >= 0; j-- {
		if i >= starts[j] {
			return actions[j]
		}
	}
	return nil
}
