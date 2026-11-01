package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/ashish10alex/xf/internal/frontend"
)

// The base is the branch R, I and C compare with (defaultBranch). b in
// their overlays opens the branch overlay as its picker, over them: the
// branches but the checked-out one, which would compare with itself, the
// base first, each aged by origin's tip when origin has the branch, since
// that is the one compared with. enter saves the pick as the project's
// defaultBranch (dropped from the file when the user's file or the
// default already says it) and the overlay under it asks again; ctrl+r
// fetches origin first. The status line says what it is: "⎇ feat vs
// develop". Another xf open on the project reads it when it next starts.

// openBase opens the branch overlay as b's picker.
func (m *Model) openBase() tea.Cmd {
	if m.deps.Branches == nil || m.deps.ChangedActions == nil {
		m.flash = "changing the base is not available"
		return nil
	}
	// Unlike B, a run doesn't stop it: nothing in the checkout changes.
	o := &m.branches
	o.open, o.loading, o.busy, o.err, o.base = true, true, "", nil, true
	o.input.SetValue("")
	o.seq++
	return tea.Batch(o.input.Focus(), m.listBranches())
}

// pickBase makes the branch under the cursor the base, and asks R, I or
// C again, whichever is open.
func (m *Model) pickBase() tea.Cmd {
	o := &m.branches
	if o.busy != "" || o.cursor >= len(o.list) {
		return nil
	}
	name := o.list[o.cursor].Name
	o.close()
	if name == m.deps.DefaultBranch {
		m.flash = "already comparing with " + name
		return nil
	}
	m.flash = frontend.SaveBase(m.deps.SaveBranch, name)
	m.deps.DefaultBranch = name
	switch {
	case m.changed.open:
		return m.requestChanged()
	case m.impact.open:
		return m.requestImpact(false)
	case m.cost.open:
		return m.requestCost(false)
	}
	return m.primeChanges()
}

// baseKey is "b vs develop" for R's, I's and C's keys, after sep; none
// when the base can't change.
func (m Model) baseKey(sep string) string {
	if m.deps.Branches == nil || m.deps.ChangedActions == nil {
		return ""
	}
	s := m.styles
	return sep + s.footerKey.Render("b") + " " + s.footerDesc.Render("vs "+m.deps.DefaultBranch)
}

// baseLine is the status line's " vs develop" after the branch, when R
// can compare.
func (m Model) baseLine() string {
	if m.deps.ChangedActions == nil || m.deps.DefaultBranch == "" {
		return ""
	}
	return m.styles.statusBar.Render(" vs " + m.deps.DefaultBranch)
}
