package ui

import (
	"fmt"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/charmbracelet/x/ansi"
)

// lineageRow is a line of the Lineage pane: a section header ("upstream",
// "downstream"), an action, or a "none" placeholder.
type lineageRow struct {
	header string
	action *project.Action
	last   bool // last entry of its section (drawn with └)
}

// lineagePane shows the direct dependencies and dependents of the selected
// action. enter jumps to an entry; the jump history lets backspace go back.
type lineagePane struct {
	rows     []lineageRow
	up, down int // entry counts, for the collapsed title
	cursor   int
	offset   int
	height   int
	history  []project.ActionID
}

func (p *lineagePane) setAction(g *project.Graph, a *project.Action) {
	p.rows = p.rows[:0]
	p.up, p.down = 0, 0
	if g == nil || a == nil {
		p.cursor, p.offset = 0, 0
		return
	}
	section := func(title string, actions []*project.Action) {
		p.rows = append(p.rows, lineageRow{header: fmt.Sprintf("%s (%d)", title, len(actions))})
		if len(actions) == 0 {
			p.rows = append(p.rows, lineageRow{})
		}
		for i, dep := range actions {
			p.rows = append(p.rows, lineageRow{action: dep, last: i == len(actions)-1})
		}
	}
	up, down := g.Dependencies(a.ID), g.Dependents(a.ID)
	p.up, p.down = len(up), len(down)
	section("upstream", up)
	section("downstream", down)
	p.cursor = -1
	p.move(1)
	p.offset = 0
	p.scrollIntoView()
}

// move steps the cursor over action rows only.
func (p *lineagePane) move(dir int) {
	for i := p.cursor + dir; i >= 0 && i < len(p.rows); i += dir {
		if p.rows[i].action != nil {
			p.cursor = i
			p.scrollIntoView()
			return
		}
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *lineagePane) moveToEnd(dir int) {
	if dir < 0 {
		p.cursor = -1
		p.move(1)
	} else {
		p.cursor = len(p.rows)
		p.move(-1)
	}
	// Keep headers visible when the first entry is selected.
	if dir < 0 {
		p.offset = 0
	}
}

func (p *lineagePane) selected() *project.Action {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return nil
	}
	return p.rows[p.cursor].action
}

func (p *lineagePane) setHeight(h int) {
	p.height = max(h, 1)
	p.scrollIntoView()
}

func (p *lineagePane) scrollIntoView() {
	if p.cursor < p.offset {
		p.offset = p.cursor
		// Show the section header above the first entry.
		if p.offset > 0 && p.rows[p.offset-1].header != "" {
			p.offset--
		}
	}
	if p.cursor >= p.offset+p.height {
		p.offset = p.cursor - p.height + 1
	}
	p.offset = min(max(p.offset, 0), max(len(p.rows)-p.height, 0))
}

func (p *lineagePane) view(s styles, width int, focused bool) string {
	if len(p.rows) == 0 {
		return s.dim.Render("no action selected")
	}
	var lines []string
	end := min(p.offset+p.height, len(p.rows))
	for i := p.offset; i < end; i++ {
		r := p.rows[i]
		switch {
		case r.header != "":
			lines = append(lines, s.dim.Render(r.header))
		case r.action == nil:
			lines = append(lines, s.dim.Render("  none"))
		default:
			lines = append(lines, p.renderEntry(s, r, width, focused && i == p.cursor))
		}
	}
	return strings.Join(lines, "\n")
}

// lineageFullKindWidth is the narrowest pane that shows kinds spelled out
// ("assertion") rather than abbreviated ("asrt").
const lineageFullKindWidth = 40

func (p *lineagePane) renderEntry(s styles, r lineageRow, width int, sel bool) string {
	branch := "├ "
	if r.last {
		branch = "└ "
	}
	a := r.action
	name := a.Target.Schema + "." + a.Target.Name
	// Spell the kind out unless the pane is too narrow, where the
	// abbreviation leaves the name more room.
	kind := a.Kind.String()
	if width < lineageFullKindWidth {
		kind = a.Kind.Short()
	}
	// Right-align the kind label; truncate the name to make room for it.
	branchW, kindW := ansi.StringWidth(branch), ansi.StringWidth(kind)
	room := max(width-branchW-kindW-1, 4)
	if len([]rune(name)) > room {
		name = string([]rune(name)[:room-1]) + "…"
	}
	gap := strings.Repeat(" ", max(width-branchW-len([]rune(name))-kindW, 1))

	nameStyle, kindStyle := s.text, s.kind(a.Kind.Short())
	if a.Kind.External() || a.Kind == project.KindUnknown {
		nameStyle = s.dim
	}
	if sel {
		return s.selectedDim.Render(branch) + s.selected.Render(name+gap) + s.selected.Render(kind)
	}
	return s.dim.Render(branch) + nameStyle.Render(name) + s.dim.Render(gap) + kindStyle.Render(kind)
}

// hiddenEntries counts the entries scrolled out of view above and below.
func (p *lineagePane) hiddenEntries() (above, below int) {
	for i, r := range p.rows {
		if r.action == nil {
			continue
		}
		switch {
		case i < p.offset:
			above++
		case i >= p.offset+p.height:
			below++
		}
	}
	return above, below
}

// push records the action being left, for backspace.
func (p *lineagePane) push(id project.ActionID) {
	p.history = append(p.history, id)
}

func (p *lineagePane) pop() (project.ActionID, bool) {
	if len(p.history) == 0 {
		return "", false
	}
	id := p.history[len(p.history)-1]
	p.history = p.history[:len(p.history)-1]
	return id, true
}
