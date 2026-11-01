package ui

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// errorsOverlay is the e overlay's state: the compile and dry run errors,
// wrapped to the box and scrolled.
type errorsOverlay struct {
	offset int  // first visible line
	zoomed bool // the box fills the body (z)
}

// dryRunError is a dry run BigQuery refused.
type dryRunError struct {
	action *project.Action
	label  string // "incremental", "full", or "" for an action's only query
	msg    string
	denied bool // BigQuery refused the account (HTTP 403)
}

// dryRunErrors are the failed dry runs of the graph's actions as compiled
// now, by action ID, and whether any failed for want of credentials,
// which says nothing about the SQL and so isn't listed.
func (m Model) dryRunErrors() (errs []dryRunError, auth bool) {
	if m.deps.DryRun == nil || m.graph == nil {
		return nil, false
	}
	ids := slices.Sorted(maps.Keys(m.graph.Actions))
	for _, id := range ids {
		a := m.graph.Actions[id]
		project, location := m.dryRunTarget(a)
		for _, q := range a.DryRunQueries() {
			e := m.dryRuns[dryRunKey(project, location, q.SQL)]
			switch {
			case e == nil || !e.done || e.err == nil:
			case errors.Is(e.err, bigquery.ErrAuth):
				auth = true
			default:
				msg, denied := e.err.Error(), false
				if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
					msg, denied = apiErr.Message, apiErr.Status == http.StatusForbidden
				}
				errs = append(errs, dryRunError{action: a, label: q.Label, msg: msg, denied: denied})
			}
		}
	}
	return errs, auth
}

// anyErrors reports whether e has anything to show.
func (m Model) anyErrors() bool {
	if m.hasErrors() {
		return true
	}
	errs, auth := m.dryRunErrors()
	return len(errs) > 0 || auth
}

// errorLines are the e overlay's contents, unwrapped: the compile's errors,
// then the dry runs'.
func (m Model) errorLines() []string {
	s := m.styles
	var lines []string
	if m.hasErrors() {
		lines = append(lines, s.title.Render("compile errors"))
		if m.compileErr != nil {
			lines = append(lines, s.errorText.Render(m.tool()+" compile failed"))
			lines = append(lines, strings.Split(m.compileErr.Error(), "\n")...)
		}
		for _, e := range m.errors {
			lines = append(lines, s.errorText.Render(e.FileName))
			for _, l := range strings.Split(e.Message, "\n") {
				lines = append(lines, "  "+l)
			}
		}
		if m.stale {
			lines = append(lines, s.dim.Render("showing the last good compile until the project compiles cleanly"))
		}
	}
	errs, auth := m.dryRunErrors()
	if len(errs) > 0 || auth {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, s.title.Render("dry run errors"))
	}
	if auth {
		lines = append(lines, s.warnText.Render("no credentials: "+bigquery.AuthHint))
		if hint := m.accountHint(); hint != "" {
			lines = append(lines, s.dim.Render(hint))
		}
	} else if hint := m.accountHint(); hint != "" && slices.ContainsFunc(errs, func(e dryRunError) bool { return e.denied }) {
		lines = append(lines, s.dim.Render("access denied? "+hint))
	}
	for _, e := range errs {
		name := e.action.Target.Schema + "." + e.action.Target.Name
		if e.label != "" {
			name += " (" + e.label + ")"
		}
		if e.action.FileName != "" {
			name += s.dim.Render(" · " + e.action.FileName)
		}
		lines = append(lines, s.errorText.Render(name))
		for _, l := range strings.Split(e.msg, "\n") {
			lines = append(lines, "  "+l)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, s.okText.Render("no errors"))
	}
	return lines
}

// copyErrors copies the e overlay's contents as plain text.
func (m *Model) copyErrors() tea.Cmd {
	text := ansi.Strip(strings.Join(m.errorLines(), "\n")) + "\n"
	m.flash = "copied the errors"
	return m.copyText(text)
}

// openErrors shows the compile errors from the top, at the normal size.
func (m *Model) openErrors() {
	m.showErrors = true
	m.errorsView = errorsOverlay{}
}

// handleErrorsKey handles a key while the e overlay is open.
func (m *Model) handleErrorsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.errorsView
	page := max(m.errorsLayout(m.geometry().bodyH).rows/2, 1)
	switch {
	case key.Matches(msg, k.Escape) && o.zoomed:
		o.zoomed = false
	case key.Matches(msg, k.Close, k.Errors):
		m.showErrors = false
	case key.Matches(msg, k.CopyReport):
		return m.copyErrors()
	case key.Matches(msg, k.Zoom):
		o.zoomed = !o.zoomed
	case key.Matches(msg, k.Compile):
		m.deps.Engine.Recompile("")
	case key.Matches(msg, k.Up):
		m.scrollErrors(-1)
	case key.Matches(msg, k.Down):
		m.scrollErrors(1)
	case key.Matches(msg, k.HalfUp):
		m.scrollErrors(-page)
	case key.Matches(msg, k.HalfDown):
		m.scrollErrors(page)
	case key.Matches(msg, k.Top):
		o.offset = 0
	case key.Matches(msg, k.Bottom):
		m.scrollErrors(len(m.errorsLayout(m.geometry().bodyH).list))
	}
	return nil
}

// scrollErrors moves the errors by delta lines, within them.
func (m *Model) scrollErrors(delta int) {
	lay := m.errorsLayout(m.geometry().bodyH)
	m.errorsView.offset = lay.clamp(m.errorsView.offset + delta)
}

// errorsBoxLayout is the e overlay's lines: the wrapped errors, which
// scroll rows lines at a time, above a fixed footer.
type errorsBoxLayout struct {
	w, inner     int
	list, footer []string
	rows         int
	maxH         int // the box's height when the errors fill it
}

// clamp keeps a first visible line of the errors in range.
func (l errorsBoxLayout) clamp(offset int) int {
	return min(max(offset, 0), max(len(l.list)-l.rows, 0))
}

// errorsLayout lays the e overlay out in bodyH lines.
func (m Model) errorsLayout(bodyH int) errorsBoxLayout {
	return m.scrollBoxLayout(bodyH, m.errorsView.zoomed, func(inner int) []string {
		var list []string
		for _, l := range m.errorLines() {
			list = append(list, wrapIndented(l, inner)...)
		}
		return list
	}, [2]string{"esc", "close"}, [2]string{"y", "copy"}, [2]string{"c", "recompile"})
}

// scrollBoxLayout lays out an overlay of scrolling text in bodyH lines:
// nearly the full width, or all of the body when zoomed. text is the
// overlay's lines, wrapped to the width it is given; keys are the footer's
// keys and what they do, before z and j/k.
func (m Model) scrollBoxLayout(bodyH int, zoomed bool, text func(inner int) []string, keys ...[2]string) errorsBoxLayout {
	s := m.styles
	w, maxH := max(m.width-8, 20), max(bodyH-2, 3)
	if zoomed {
		w, maxH = max(m.width, 20), max(bodyH, 3)
	}
	inner := w - 4 // borders and a column of padding each side
	list := text(inner)

	zoom := "zoom"
	if zoomed {
		zoom = "unzoom"
	}
	keys = append(keys, [2]string{"z", zoom})
	if len(list) > max(maxH-2-2, 1) {
		keys = append(keys, [2]string{"j/k", "scroll"})
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = s.footerKey.Render(k[0]) + " " + s.footerDesc.Render(k[1])
	}
	footer := []string{"", strings.Join(parts, s.dim.Render(" · "))}
	return errorsBoxLayout{w: w, inner: inner, list: list, footer: footer, rows: max(maxH-2-len(footer), 1), maxH: maxH}
}

// errorsBox draws the e overlay.
func (m Model) errorsBox(g geometry) string {
	return m.scrollBox("errors", m.errorsLayout(g.bodyH), m.errorsView.offset, m.errorsView.zoomed)
}

// scrollBox draws an overlay laid out by scrollBoxLayout, from its
// offset-th line.
func (m Model) scrollBox(title string, lay errorsBoxLayout, offset int, zoomed bool) string {
	offset = lay.clamp(offset)
	end := min(offset+lay.rows, len(lay.list))
	lines := append(append([]string{}, lay.list[offset:end]...), lay.footer...)
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, lay.inner, "…")
	}
	h := min(len(lines)+2, lay.maxH)
	if zoomed {
		h = lay.maxH
		title += " · zoom (esc)"
	}
	return m.styles.paneWithFooter(title, moreNote(offset, len(lay.list)-end), strings.Join(lines, "\n"), lay.w, h, true)
}

// wrapIndented wraps l to w cells, indenting the lines it wraps onto past
// l's own indent, so a wrapped stack frame still reads as one.
func wrapIndented(l string, w int) []string {
	body := strings.TrimLeft(l, " \t")
	indent := l[:len(l)-len(body)]
	cont := indent + "  "
	if w-ansi.StringWidth(cont) < 20 {
		indent, cont = "", ""
	}
	if ansi.StringWidth(l) <= w || body == "" {
		return []string{l}
	}
	out := strings.Split(ansi.Wrap(body, w-ansi.StringWidth(cont), "/._,"), "\n")
	for i := range out {
		if i == 0 {
			out[i] = indent + out[i]
		} else {
			out[i] = cont + out[i]
		}
	}
	return out
}
