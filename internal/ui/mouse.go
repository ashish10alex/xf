package ui

import tea "charm.land/bubbletea/v2"

// paneRect is where a pane is drawn, in screen cells, borders included.
type paneRect struct {
	id         paneID
	x, y, w, h int
}

func (r paneRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// paneRects mirrors render: the panes and where they are, top to bottom
// from the top of the screen.
func (m Model) paneRects() []paneRect {
	g := m.geometry()
	if m.zoomed || g.solo {
		return []paneRect{{m.focus, 0, 0, m.width, g.bodyH}}
	}
	var rects []paneRect
	y := 0
	add := func(id paneID, h int) {
		rects = append(rects, paneRect{id, 0, y, m.width, h})
		y += h
	}
	add(paneActions, g.cardH)
	add(paneLineage, g.lineageH)
	if g.runH > 0 {
		add(paneRun, g.runH)
	}
	add(paneSQL, g.sqlH)
	if m.showLog {
		add(paneLog, g.logH)
	}
	return rects
}

func (m Model) paneAt(x, y int) (paneRect, bool) {
	for _, r := range m.paneRects() {
		if r.contains(x, y) {
			return r, true
		}
	}
	return paneRect{}, false
}

// handleClick focuses the clicked pane (focusing a collapsed Lineage pane
// expands it). In Lineage it also selects the entry, and clicking the
// selected entry again jumps to it. With the palette open, clicking a result
// picks it and clicking outside closes it.
func (m *Model) handleClick(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || m.showErrors || m.info.open || m.keyHelp.open || m.prod.open || m.changed.open || m.impact.open || m.cost.open || m.tags.open || m.branches.open {
		return nil
	}
	if m.palette.open {
		return m.clickPalette(mouse)
	}
	m.clickPane(mouse)
	return nil
}

func (m *Model) clickPane(mouse tea.Mouse) {
	r, ok := m.paneAt(mouse.X, mouse.Y)
	if !ok {
		return
	}
	m.flash = ""
	m.cols.stopTyping()
	wasOpen := m.lineageOpen
	m.setFocus(r.id)
	row := mouse.Y - r.y - 1 // first line inside the border
	if row < 0 || row >= r.h-2 {
		return
	}
	switch r.id {
	case paneActions:
		if a := m.actionAtLine(m.actionsOffset + row); a != nil && a != m.selected {
			m.selectInPane(a)
		}
	case paneLineage:
		if !wasOpen {
			return // the click only expanded it
		}
		p := &m.lineage
		i := p.offset + row
		if i >= len(p.rows) || p.rows[i].action == nil {
			return
		}
		if i == p.cursor {
			m.handleLineageKey(keyPress("enter"))
			return
		}
		p.cursor = i
	case paneSQL:
		if m.cols.shown {
			m.clickColumns(row)
			return
		}
		b := &m.sql.buffer
		if len(b.text) == 0 {
			return
		}
		b.visual = visualNone
		b.moveTo(b.posAt(mouse.X-r.x-1, row))
		m.sqlDrag = true
	}
}

func (m *Model) clickPalette(mouse tea.Mouse) tea.Cmd {
	b := m.palette.box(m.width, m.geometry().bodyH)
	if mouse.X < b.x || mouse.X >= b.x+b.w || mouse.Y < b.y || mouse.Y >= b.y+b.h {
		m.palette.close()
		return nil
	}
	row := mouse.Y - b.y - 2 // below the border and the query line
	if i := m.palette.offset + row; row >= 0 && row < b.rows && i < len(m.palette.list) {
		m.palette.cursor = i
		return m.pickFromPalette()
	}
	return nil
}

// handleDrag selects text in the SQL pane while the button is held after a
// click there (charwise visual mode, which y copies), scrolling when the
// pointer goes above or below the pane.
func (m *Model) handleDrag(mouse tea.Mouse) {
	if !m.sqlDrag || mouse.Button != tea.MouseLeft {
		return
	}
	var r paneRect
	for _, pr := range m.paneRects() {
		if pr.id == paneSQL {
			r = pr
		}
	}
	b := &m.sql.buffer
	row := mouse.Y - r.y - 1
	switch {
	case row < 0:
		b.scroll(-1)
		row = 0
	case row >= b.height:
		b.scroll(1)
		row = b.height - 1
	}
	if b.visual == visualNone {
		b.visual = visualChar
		b.vrow, b.vcol = b.row, b.col
	}
	b.moveTo(b.posAt(mouse.X-r.x-1, row))
}

// handleWheel scrolls the pane under the pointer without changing focus:
// Lineage and the palette move their selection, SQL and log scroll their text.
func (m *Model) handleWheel(mouse tea.Mouse) {
	var dir int
	switch mouse.Button {
	case tea.MouseWheelUp:
		dir = -1
	case tea.MouseWheelDown:
		dir = 1
	default:
		return
	}
	if m.showErrors {
		m.scrollErrors(3 * dir)
		return
	}
	if m.info.open {
		m.scrollInfo(3 * dir)
		return
	}
	if m.keyHelp.open {
		m.scrollHelp(3 * dir)
		return
	}
	if m.changed.open {
		m.changed.offset = max(m.changed.offset+dir, 0)
		return
	}
	if m.impact.open {
		m.scrollImpact(3 * dir)
		return
	}
	if m.cost.open {
		m.scrollCost(3 * dir)
		return
	}
	if m.palette.open {
		m.palette.move(dir, m.palette.box(m.width, m.geometry().bodyH).rows)
		return
	}
	if m.tags.open {
		m.tags.move(dir, m.tagsRows(m.geometry().bodyH))
		return
	}
	if m.branches.open {
		m.branches.move(dir, m.branchesRows(m.geometry().bodyH))
		return
	}
	r, ok := m.paneAt(mouse.X, mouse.Y)
	if !ok {
		return
	}
	switch r.id {
	case paneActions:
		m.moveInActions(dir)
	case paneLineage:
		m.lineage.move(dir)
	case paneSQL:
		if m.cols.shown {
			fields, _, _ := m.columns()
			m.cols.move(dir, len(m.cols.match(fields)), m.sql.height)
			return
		}
		m.sql.scrollView(3 * dir)
	case paneLog:
		if t := m.shownTable(); t != nil {
			t.move(dir, m.log.height)
			return
		}
		if m.hasTabs() && m.logTab == tabHistory {
			m.moveHistory(dir)
			return
		}
		if m.hasTabs() && m.logTab != tabLog {
			return
		}
		m.log.scrollBy(3 * dir)
	}
}

// keyPress builds a key message for reusing key handlers from mouse actions.
func keyPress(k string) tea.KeyPressMsg {
	if k == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}
