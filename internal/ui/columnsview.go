package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// columnsView is the SQL pane's other view: the columns the selected
// action's query returns, from the schema of the dry run the Actions pane
// already made. S swaps it with the SQL, and the choice stays as the
// selection moves. It lists the columns by name, a RECORD's fields under it
// by their path, with a cursor, and / filters them by name and by type, a
// box for each.
type columnsView struct {
	shown bool
	// name and typ are the filters' boxes, drawn under the header while
	// typing or set.
	name, typ textinput.Model
	typing    bool
	cursor    int
	offset    int              // first column shown
	of        project.ActionID // the action the filters and cursor are for
}

func newColumnsView() columnsView {
	box := func() textinput.Model {
		in := textinput.New()
		in.Prompt = "/"
		return in
	}
	return columnsView{name: box(), typ: box()}
}

func (v columnsView) filtered() bool { return v.name.Value() != "" || v.typ.Value() != "" }

// stopTyping returns the keys to the table, keeping the filters.
func (v *columnsView) stopTyping() {
	v.typing = false
	v.name.Blur()
	v.typ.Blur()
}

// clear drops the filters and puts the cursor on the first column.
func (v *columnsView) clear() {
	v.stopTyping()
	v.name.SetValue("")
	v.typ.SetValue("")
	v.cursor, v.offset = 0, 0
}

// setAction clears the filters when another action is selected: a filter
// left set would make its table look as if it had few columns.
func (v *columnsView) setAction(a *project.Action) {
	var id project.ActionID
	if a != nil {
		id = a.ID
	}
	if id != v.of {
		v.of = id
		v.clear()
	}
}

// match keeps the fields whose name holds the name filter and whose type
// holds the type filter, whatever their case.
func (v columnsView) match(fields []bigquery.Field) []bigquery.Field {
	name, typ := strings.ToLower(strings.TrimSpace(v.name.Value())), strings.ToLower(strings.TrimSpace(v.typ.Value()))
	if name == "" && typ == "" {
		return fields
	}
	var out []bigquery.Field
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f.Name), name) && strings.Contains(strings.ToLower(f.Type), typ) {
			out = append(out, f)
		}
	}
	return out
}

// columnRows is how many columns fit in height lines, under the header and
// the filters' boxes.
func (v columnsView) columnRows(height int) int {
	rows := height - 1
	if v.typing || v.filtered() {
		rows--
	}
	return max(rows, 1)
}

// move moves the cursor by delta among n columns in a pane height lines
// tall, scrolling to keep it in view.
func (v *columnsView) move(delta, n, height int) {
	if n == 0 {
		return
	}
	v.cursor = min(max(v.cursor+delta, 0), n-1)
	v.offset = v.window(n, v.columnRows(height))
}

// window is the cursor and the first column to show so that it is among
// rows of n.
func (v columnsView) window(n, rows int) int {
	cur := min(v.cursor, max(n-1, 0))
	off := min(v.offset, max(n-rows, 0))
	return max(min(off, cur), cur-rows+1, 0)
}

// columnsQuery is the dry run whose schema the columns view shows for a:
// the variant the SQL pane shows of an incremental table, else its only one.
func (m Model) columnsQuery(a *project.Action) (project.DryRunQuery, bool) {
	queries := a.DryRunQueries()
	if len(queries) == 0 {
		return project.DryRunQuery{}, false
	}
	for _, q := range queries {
		if q.Label != "" && (q.Label == "incremental") == m.incremental {
			return q, true
		}
	}
	return queries[0], true
}

// columns is the selected action's columns and their nested fields by name
// (see frontend.FlattenFields), or why there are none
// to show; failed is set when that is an error.
func (m Model) columns() (fields []bigquery.Field, reason string, failed bool) {
	a := m.selected
	switch {
	case m.graph == nil && m.compiling:
		return nil, "compiling…", false
	case m.graph == nil:
		return nil, "no compile result — press e for details, c to retry", false
	case a == nil:
		return nil, "no action selected", false
	case m.deps.DryRun == nil:
		return nil, "no dry runs here, so no columns", false
	case m.uncompiled():
		return nil, "not compiled: BigQuery can't plan this project's SQL", false
	}
	q, ok := m.columnsQuery(a)
	if !ok {
		return nil, a.Kind.String() + ": nothing to dry run, so no columns", false
	}
	if m.outdated != "" || m.sqlNote(a) != "" {
		return nil, "waiting for the compile…", false
	}
	proj, location := m.dryRunTarget(a)
	if proj == "" {
		return nil, "no project to dry run in", false
	}
	e := m.dryRuns[dryRunKey(proj, location, q.SQL)]
	switch {
	case e == nil || !e.done:
		return nil, "dry run…", false
	case errors.Is(e.err, bigquery.ErrAuth):
		return nil, "no credentials for the dry run — press e for details", true
	case e.err != nil:
		msg := e.err.Error()
		if apiErr, ok := errors.AsType[*bigquery.APIError](e.err); ok {
			msg = apiErr.Message
		}
		return nil, "✗ " + firstLine(msg) + " — press e for details", true
	}
	fields = frontend.FlattenFields(e.res.Schema)
	switch {
	case len(fields) > 0:
	case a.Kind == project.KindOperation || e.res.StatementType == "SCRIPT":
		return nil, a.Kind.String() + ": a dry run of a script has no schema", false
	default:
		return nil, "the dry run returned no schema", false
	}
	return fields, "", false
}

// columnsFocused reports whether the keys go to the columns view.
func (m Model) columnsFocused() bool { return m.cols.shown && m.focus == paneSQL }

// columnsFilterable reports whether / filters columns rather than finding
// an action: the columns view has the keys and a table to filter.
func (m Model) columnsFilterable() bool {
	if !m.columnsFocused() {
		return false
	}
	fields, _, _ := m.columns()
	return len(fields) > 0
}

// toggleColumns swaps the SQL pane between the SQL and the columns.
func (m *Model) toggleColumns() {
	m.cols.shown = !m.cols.shown
	m.cols.stopTyping()
	m.sql.visual = visualNone
}

// modeColW fits "REQUIRED" and "REPEATED".
const modeColW = 8

// columnsLayout sizes the table's columns for width cells: the name and
// the type as wide as their widest (and wide enough for their filters'
// boxes), so a type sits beside its name on a wide pane; the name gives way
// on a narrow one.
func columnsLayout(fields []bigquery.Field, width int) []table.Column {
	nameW, typeW := 24, 12
	for _, f := range fields {
		nameW = max(nameW, ansi.StringWidth(f.Name))
		typeW = max(typeW, ansi.StringWidth(f.Type))
	}
	typeW = min(typeW, max(width/3, 4))
	nameW = max(min(nameW, width-typeW-modeColW-6*cellPad), 4)
	return []table.Column{{Title: "Column", Width: nameW}, {Title: "Type", Width: typeW}, {Title: "Mode", Width: modeColW}}
}

// columnMode is a field's mode as its cell shows it: NULLABLE, which
// BigQuery often leaves out, is blank.
func columnMode(f bigquery.Field) string {
	if strings.EqualFold(f.Mode, "NULLABLE") {
		return ""
	}
	return f.Mode
}

// columnsBody renders the columns view in the SQL pane's cells: the table,
// or a line saying why there is none.
func (m Model) columnsBody() string {
	s := m.styles
	fields, reason, failed := m.columns()
	if reason != "" {
		if failed {
			return s.errorText.Render(reason)
		}
		return s.dim.Render(reason)
	}
	v := m.cols
	width, height := m.sql.width, m.sql.height
	cols := columnsLayout(fields, width)
	shown := v.match(fields)
	rows := v.columnRows(height)
	off := v.window(len(shown), rows)
	end := min(off+rows, len(shown))

	cells := make([]table.Row, 0, end-off)
	for _, f := range shown[off:end] {
		cells = append(cells, table.Row{f.Name, f.Type, columnMode(f)})
	}
	st := table.Styles{
		Header:   s.dim.Bold(true).Padding(0, cellPad),
		Cell:     lipgloss.NewStyle().Padding(0, cellPad),
		Selected: lipgloss.NewStyle(),
	}
	if m.focus == paneSQL {
		st.Selected = s.selected
	}
	tb := table.New(table.WithColumns(cols), table.WithRows(cells), table.WithStyles(st),
		table.WithWidth(width), table.WithHeight(len(cells)+1))
	tb.SetCursor(min(v.cursor, max(len(shown)-1, 0)) - off)
	header, body, _ := strings.Cut(tb.View(), "\n")
	lines := []string{header}
	if v.typing || v.filtered() {
		name, typ := v.name, v.typ
		name.SetWidth(max(cols[0].Width-2, 1))
		typ.SetWidth(max(cols[1].Width-2, 1))
		lines = append(lines, " "+padRight(name.View(), cols[0].Width)+"  "+padRight(typ.View(), cols[1].Width))
	}
	switch {
	case len(cells) > 0:
		lines = append(lines, body)
	default:
		lines = append(lines, s.dim.Render(" no column matches"))
	}
	return strings.Join(lines, "\n")
}

// columnsFooter is the note in the pane's border: "180 columns", or
// "3 of 180 columns" under a filter.
func (m Model) columnsFooter() string {
	fields, reason, _ := m.columns()
	if reason != "" {
		return ""
	}
	n := len(fields)
	if m.cols.filtered() {
		return fmt.Sprintf("%d of %d column%s", len(m.cols.match(fields)), n, plural(n))
	}
	return fmt.Sprintf("%d column%s", n, plural(n))
}

// handleColumnsKey handles a key in the focused columns view. It runs
// before the global keys: typing a filter takes every key (ctrl+n, ctrl+p
// and the arrows still move the cursor, as in the palette), and over the
// table / filters where it finds an action elsewhere. Unhandled keys fall
// through to the global bindings.
func (m *Model) handleColumnsKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	k := m.keys
	v := &m.cols
	fields, _, _ := m.columns()
	if len(fields) == 0 {
		v.stopTyping()
		return false, nil
	}
	if v.typing {
		switch {
		case key.Matches(msg, k.Escape):
			v.clear()
		case key.Matches(msg, k.Accept):
			v.stopTyping()
		case key.Matches(msg, k.NextPane, k.PrevPane):
			if v.name.Focused() {
				v.name.Blur()
				return true, v.typ.Focus()
			}
			v.typ.Blur()
			return true, v.name.Focus()
		case slices.Contains([]string{"ctrl+n", "down"}, msg.String()):
			v.move(1, len(v.match(fields)), m.sql.height)
		case slices.Contains([]string{"ctrl+p", "up"}, msg.String()):
			v.move(-1, len(v.match(fields)), m.sql.height)
		default:
			before := v.name.Value() + "\x00" + v.typ.Value()
			var cmd tea.Cmd
			if v.name.Focused() {
				v.name, cmd = v.name.Update(msg)
			} else {
				v.typ, cmd = v.typ.Update(msg)
			}
			if v.name.Value()+"\x00"+v.typ.Value() != before {
				v.cursor, v.offset = 0, 0
			}
			return true, cmd
		}
		return true, nil
	}
	shown := v.match(fields)
	n, h := len(shown), m.sql.height
	page := max(v.columnRows(h)/2, 1)
	copyColumn := func(text func(f bigquery.Field) string) tea.Cmd {
		if n == 0 {
			return nil
		}
		t := text(shown[min(v.cursor, n-1)])
		m.flash = "copied " + t
		return m.copyText(t)
	}
	switch {
	case key.Matches(msg, k.Find):
		v.typing = true
		v.typ.Blur()
		return true, v.name.Focus()
	case key.Matches(msg, k.Escape) && v.filtered():
		v.clear()
	case key.Matches(msg, k.Up):
		v.move(-1, n, h)
	case key.Matches(msg, k.Down):
		v.move(1, n, h)
	case key.Matches(msg, k.HalfDown):
		v.move(page, n, h)
	case key.Matches(msg, k.HalfUp):
		v.move(-page, n, h)
	case key.Matches(msg, k.Top):
		v.move(-n, n, h)
	case key.Matches(msg, k.Bottom):
		v.move(n, n, h)
	case key.Matches(msg, k.CopyColumn):
		return true, copyColumn(func(f bigquery.Field) string { return f.Name })
	case key.Matches(msg, k.CopyColumnType):
		return true, copyColumn(func(f bigquery.Field) string { return f.Name + " " + f.Type })
	default:
		return false, nil
	}
	return true, nil
}

// clickColumns puts the cursor on the column drawn on the pane's line row.
func (m *Model) clickColumns(row int) {
	v := &m.cols
	fields, _, _ := m.columns()
	shown := v.match(fields)
	row-- // the header
	if v.typing || v.filtered() {
		row-- // the filters' boxes
	}
	v.stopTyping()
	if i := v.window(len(shown), v.columnRows(m.sql.height)) + row; row >= 0 && i < len(shown) {
		v.cursor = i
	}
}

// columnsKeys are the footer's keys for the focused columns view.
func (m Model) columnsKeys() []key.Binding {
	k := m.keys
	if m.cols.typing {
		box := k.NextPane
		box.SetHelp("tab", "name/type")
		move := k.Down
		move.SetHelp("ctrl+n/p", "move")
		keep := k.Accept
		keep.SetHelp("enter", "keep")
		clear := k.Escape
		clear.SetHelp("esc", "clear")
		return []key.Binding{box, move, keep, clear}
	}
	swap := k.Columns
	swap.SetHelp("S", "sql")
	if !m.columnsFilterable() {
		return []key.Binding{swap}
	}
	move := k.Down
	move.SetHelp("j/k", "move")
	b := []key.Binding{move, k.CopyColumn, k.CopyColumnType, swap}
	if m.cols.filtered() {
		clear := k.Escape
		clear.SetHelp("esc", "clear filter")
		b = append(b, clear)
	}
	return b
}
