package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/charmbracelet/x/ansi"
)

const (
	paletteMaxRows = 15
	// paletteMinW is wide enough for most action names, so in a narrow
	// terminal (a split beside an editor) the palette fills it.
	paletteMinW = 70
	paletteMaxW = 90
	// paletteFullKindWidth is the narrowest row that spells kinds out
	// ("declaration") rather than abbreviating them ("decl").
	paletteFullKindWidth = 60
)

const palettePrompt = "> "

// paletteItem is an action as the palette lists it.
type paletteItem = frontend.ActionItem

// paletteItems labels every action by its target, sorted by label.
func paletteItems(g *project.Graph) []paletteItem { return frontend.ActionItems(g) }

// kindFilter is a "$name" word of the palette's query (see
// frontend.KindFilters).
type kindFilter = frontend.KindFilter

// paletteMatch is a listed row: an action, or in command mode a command.
type paletteMatch struct {
	item    paletteItem
	command *command
}

// palette is the "/" overlay for finding an action, like VS Code's quick
// open. With an empty query it lists the current action, then the actions of
// the file open in the editor, then recently selected ones, then the rest by
// name; typing filters them as filter describes. A query that starts with ">"
// lists commands instead, as VS Code's does.
type palette struct {
	open bool
	// changed marks the Changed Actions.
	changed map[project.ActionID]bool
	input   textinput.Model
	base    []paletteItem // every action in empty-query order
	// commands is what a query starting with ">" lists, in order, and
	// subject the action they are about: nil when none is selected.
	commands []command
	subject  *project.Action
	kinds    []kindFilter // what a "$" word of the query can name
	// badKind is a "$" word of the query that names no kind.
	badKind string
	list    []paletteMatch
	cursor  int
	offset  int
}

// newPalette makes the palette of a tool's project: "dataform" or "dbt".
func newPalette(tool string) palette {
	in := textinput.New()
	in.Prompt = palettePrompt
	in.Placeholder = "find action"
	return palette{input: in, kinds: frontend.KindFilters(tool)}
}

// show opens the palette. first lists actions to put ahead of the rest, in
// order. When current is set, first starts with the current action, so the
// cursor starts on the second row and enter switches back to the previous
// action.
func (p *palette) show(items []paletteItem, first []*project.Action, current bool) tea.Cmd {
	p.base = frontend.FirstItems(items, first)
	p.open = true
	p.open = true
	p.input.SetValue("")
	p.filter()
	if current && len(p.list) > 1 {
		p.cursor = 1
	}
	return p.input.Focus()
}

func (p *palette) close() {
	p.open = false
	p.input.Blur()
}

// update feeds a message to the query input and re-filters when it changes.
func (p *palette) update(msg tea.Msg) tea.Cmd {
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.filter()
	}
	return cmd
}

// filter lists the items matching the query (see frontend.FilterActions),
// or in command mode the commands.
func (p *palette) filter() {
	p.cursor, p.offset = 0, 0
	p.badKind = ""
	// In command mode the query's own ">" stands for the prompt.
	p.input.Prompt = palettePrompt
	if q, ok := p.commandQuery(); ok {
		p.input.Prompt = ""
		p.list = matchCommands(p.commands, strings.Fields(strings.ToLower(q)))
		return
	}
	var found []paletteItem
	found, p.badKind = frontend.FilterActions(p.base, p.kinds, p.input.Value())
	p.list = p.list[:0]
	for _, it := range found {
		p.list = append(p.list, paletteMatch{item: it})
	}
}

// commandQuery is the query after its ">", and whether it has one: whether
// the palette lists commands.
func (p *palette) commandQuery() (string, bool) {
	return strings.CutPrefix(strings.TrimLeft(p.input.Value(), " "), ">")
}

func (p *palette) move(n, rows int) {
	p.cursor = min(max(p.cursor+n, 0), max(len(p.list)-1, 0))
	p.scrollIntoView(rows)
}

func (p *palette) scrollIntoView(rows int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	p.offset = min(max(p.offset, 0), max(len(p.list)-rows, 0))
}

// selected is the highlighted action; nil in command mode.
func (p *palette) selected() *project.Action {
	if p.cursor < 0 || p.cursor >= len(p.list) {
		return nil
	}
	return p.list[p.cursor].item.Action
}

// selectedCommand is the highlighted command; nil outside command mode.
func (p *palette) selectedCommand() *command {
	if p.cursor < 0 || p.cursor >= len(p.list) {
		return nil
	}
	return p.list[p.cursor].command
}

// paletteBox is where the palette is drawn: centred in the body, with room
// for the query line and up to paletteMaxRows results.
type paletteBox struct {
	x, y, w, h int
	rows       int // result rows
}

func (p *palette) box(width, bodyH int) paletteBox {
	w := min(max(width*6/10, paletteMinW), paletteMaxW, width-2)
	// As tall as the longer list needs, so the box doesn't jump when ">"
	// switches between them.
	rows := max(min(paletteMaxRows, max(len(p.base), len(p.commands)), bodyH-5), 1)
	h := rows + 3 // borders and the query line
	return paletteBox{x: max((width-w)/2, 0), y: max((bodyH-h)/2, 0), w: w, h: h, rows: rows}
}

// view draws the palette box. Its content keeps a cell of margin from the
// border on each side.
func (p *palette) view(s styles, b paletteBox) string {
	inner := b.w - 4
	_, commands := p.commandQuery()
	title, total := "find action", len(p.base)
	keyW := 0
	if commands {
		title, total = "commands", len(p.commands)
		if a := p.subject; a != nil {
			// what "Run" runs and "Copy" copies
			title += " · " + fitName(a.Target.Schema+"."+a.Target.Name, a.Target.Name, b.w-6-ansi.StringWidth(title+" · "))
		}
		for _, c := range p.commands {
			keyW = max(keyW, ansi.StringWidth(c.key))
		}
	}
	count := fmt.Sprintf("%d/%d", len(p.list), total)
	p.input.SetWidth(max(inner-ansi.StringWidth(count)-4, 1))
	query := p.input.View()
	gap := strings.Repeat(" ", max(inner-ansi.StringWidth(query)-ansi.StringWidth(count), 1))
	lines := []string{query + gap + s.dim.Render(count)}
	if len(p.list) == 0 {
		// The kinds there are, on as many rows as they need of the box's.
		msg := []string{"no matches"}
		if p.badKind != "" {
			msg = []string{"no kind " + p.badKind + ", only"}
			if p.badKind == "$" {
				msg = []string{"kinds:"}
			}
			for _, f := range p.kinds {
				word := " $" + f.Name
				if last := &msg[len(msg)-1]; ansi.StringWidth(*last+word) <= inner-2 {
					*last += word
				} else {
					msg = append(msg, word[1:])
				}
			}
		}
		for _, l := range msg[:min(len(msg), b.rows)] {
			lines = append(lines, s.dim.Render("  "+ansi.Truncate(l, max(inner-2, 1), "…")))
		}
	}
	end := min(p.offset+b.rows, len(p.list))
	for i := p.offset; i < end; i++ {
		if c := p.list[i].command; c != nil {
			lines = append(lines, renderCommandRow(s, c, inner, keyW, i == p.cursor))
			continue
		}
		lines = append(lines, p.renderRow(s, p.list[i], inner, i == p.cursor))
	}
	for i, l := range lines {
		lines[i] = " " + l
	}
	return s.pane(title, strings.Join(lines, "\n"), b.w, b.h, true)
}

// renderCommandRow draws "▸ title" with, right-aligned, the command's state
// and its key in a column keyW wide.
func renderCommandRow(s styles, c *command, width, keyW int, sel bool) string {
	lead := "  "
	if sel {
		lead = "▸ "
	}
	nameStyle, dim, stateStyle := s.text, s.dim, s.dim
	if c.state == "on" {
		stateStyle = s.okText
	}
	if sel {
		nameStyle, dim, stateStyle = s.selected, s.selectedDim, s.selected
	}
	// "off" is as wide as a state gets, so the states line up too.
	state, keys := "", c.key+strings.Repeat(" ", keyW-ansi.StringWidth(c.key))
	if c.state != "" {
		state = fmt.Sprintf("%-3s  ", c.state)
	}
	tail := state + keys
	room := max(width-ansi.StringWidth(lead)-ansi.StringWidth(tail)-1, 1)
	title := ansi.Truncate(c.title, room, "…")
	gap := strings.Repeat(" ", max(width-ansi.StringWidth(lead)-ansi.StringWidth(title)-ansi.StringWidth(tail), 1))
	return dim.Render(lead) + nameStyle.Render(title) + dim.Render(gap) + stateStyle.Render(state) + dim.Render(keys)
}

// renderRow draws "▸ schema.name" with the schema dimmed and the kind
// right-aligned, spelled out when the row is wide enough. A label too long
// for the row is shortened as fitName does: the schema goes, then the start
// of the name.
func (p *palette) renderRow(s styles, m paletteMatch, width int, sel bool) string {
	a := m.item.Action
	lead := "  "
	if sel {
		lead = "▸ "
	}
	tail := a.Kind.Short()
	if width >= paletteFullKindWidth {
		tail = a.Kind.String()
	}
	if a.Disabled {
		tail = "off " + tail
	}
	mark := ""
	if p.changed[a.ID] {
		mark = "● "
	}
	nameStyle, dim, kindStyle := s.text, s.dim, s.kind(a.Kind.Short())
	if a.Kind.External() || a.Disabled {
		nameStyle = s.dim
	}
	if sel {
		nameStyle, dim, kindStyle = s.selected, s.selectedDim, s.selected
	}

	var name strings.Builder
	room := max(width-ansi.StringWidth(lead)-ansi.StringWidth(mark)-ansi.StringWidth(tail)-1, 1)
	schemaEnd := len(a.Target.Schema) + 1 // through the dot
	start := 0
	if ansi.StringWidth(m.item.Label) > room {
		start = schemaEnd
	}
	for i, r := range m.item.Label { // fuzzy reports byte offsets
		if i < start {
			continue
		}
		st := nameStyle
		if i < schemaEnd {
			st = dim
		}
		name.WriteString(st.Render(string(r)))
	}
	label := name.String()
	if over := ansi.StringWidth(label) - room; over > 0 {
		label = dim.Render("…") + ansi.TruncateLeft(label, over+1, "")
	}
	gap := strings.Repeat(" ", max(width-ansi.StringWidth(lead)-ansi.StringWidth(label)-ansi.StringWidth(mark)-ansi.StringWidth(tail), 1))
	markStyle := s.warnText
	if sel {
		markStyle = s.selected
	}
	return dim.Render(lead) + label + dim.Render(gap) + markStyle.Render(mark) + kindStyle.Render(tail)
}
