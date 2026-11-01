package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
)

// The status line names the checked-out branch, with how the checkout
// differs from origin/<branch> as of the last fetch: "⎇ main ●3 ↑2 ↓1"
// for 3 uncommitted files, 2 unpushed commits and 1 unpulled one. It is
// read at start, when HEAD or another watched git file changes (a switch
// or commit in a terminal), when the terminal gets focus back and when a
// compile starts.
//
// B opens the branch switcher: the local branches and those only origin
// has, newest commit first, with a query that filters them as the palette
// does. enter switches with a plain git switch (see package gitbranch),
// ctrl+r fetches origin first. A local run blocks a switch, since the
// tool reads files as it goes; an API run, which compiles the pushed
// branch in GCP, doesn't.

const (
	// branchTimeout bounds a read of the branches and a switch; a fetch,
	// which goes to the network, gets fetchTimeout.
	branchTimeout = 30 * time.Second
	fetchTimeout  = 2 * time.Minute
	// headDebounce gathers a switch's or commit's writes into one read.
	headDebounce = 150 * time.Millisecond

	branchesMaxRows = 14
	branchesMinW    = 56
	branchesMaxW    = 80
	// branchErrorLines is how many lines of git's refusal the box shows.
	branchErrorLines = 6
)

// branchRow is a branch's row: its name and, right-aligned, "remote" for
// a branch only origin has, "current" for the checked-out one, and its
// age; lit for the checked-out one.
func branchRow(b gitbranch.Branch, now time.Time) (name, right string, lit bool) {
	var tags []string
	if b.Current {
		tags = append(tags, "current")
	}
	if b.Remote {
		tags = append(tags, "remote")
	}
	if age := gitbranch.Age(now, b.Date); age != "" {
		tags = append(tags, age)
	}
	return b.Name, strings.Join(tags, " · "), b.Current
}

// gitStatus is the checkout as the status line shows it.
type gitStatus struct {
	g     dataformapi.GitState
	err   error
	known bool
	seq   int // only the latest read's answer is kept
}

type gitStatusMsg struct {
	seq int
	g   dataformapi.GitState
	err error
}

// headMsg says a watched git file changed.
type headMsg struct{}

// readGit reads the checkout again for the status line.
func (m *Model) readGit() tea.Cmd {
	if m.deps.Git == nil {
		return nil
	}
	m.git.seq++
	seq, read := m.git.seq, m.deps.Git
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiGitTimeout)
		defer cancel()
		g, err := read(ctx)
		return gitStatusMsg{seq: seq, g: g, err: err}
	}
}

func (m *Model) handleGitStatus(msg gitStatusMsg) {
	if msg.seq == m.git.seq {
		m.git.g, m.git.err, m.git.known = msg.g, msg.err, true
	}
}

// waitForHead waits for the next change to a watched git file.
func waitForHead(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return headMsg{}
	}
}

// branchLine is the status line's branch, "" when there is none to show.
func (m Model) branchLine() string {
	return m.styles.gitLine(frontend.BranchParts(m.git.g, m.git.err, m.git.known))
}

// branchOverlay is the B overlay's state.
type branchOverlay struct {
	open    bool
	input   textinput.Model
	all     []gitbranch.Branch // newest first
	list    []gitbranch.Branch // the ones the query matches, best first
	note    string             // BranchRootNote
	cursor  int
	offset  int
	loading bool
	// busy is what is under way, "switching to x" or "fetching origin",
	// during which enter and ctrl+r wait.
	busy string
	err  error // the last list, switch or fetch's
	seq  int   // counts the opens, so an older open's answers are dropped
	// base says it is b's picker of the branch R, I and C compare with
	// (see basepick.go), not B's switcher.
	base bool
}

func newBranchOverlay() branchOverlay {
	in := textinput.New()
	in.Prompt = palettePrompt
	in.Placeholder = "find branch"
	return branchOverlay{input: in}
}

// waiting says the overlay shows a spinner.
func (o *branchOverlay) waiting() bool { return o.open && (o.loading || o.busy != "") }

func (o *branchOverlay) close() {
	o.open = false
	o.input.Blur()
	o.seq++
}

func (o *branchOverlay) filter() {
	o.cursor, o.offset = 0, 0
	o.list = frontend.FindBranches(o.all, o.input.Value())
}

func (o *branchOverlay) move(n, rows int) {
	o.cursor = min(max(o.cursor+n, 0), max(len(o.list)-1, 0))
	if o.cursor < o.offset {
		o.offset = o.cursor
	}
	if o.cursor >= o.offset+rows {
		o.offset = o.cursor - rows + 1
	}
	o.offset = min(max(o.offset, 0), max(len(o.list)-rows, 0))
}

type branchListMsg struct {
	seq int
	l   gitbranch.Listing
	err error
}

type branchSwitchMsg struct {
	seq  int
	name string
	err  error
}

type branchFetchMsg struct {
	seq int
	err error
}

// openBranches opens the B overlay and lists the branches.
func (m *Model) openBranches() tea.Cmd {
	switch {
	case m.deps.Branches == nil:
		m.flash = "switching branch is not available"
		return nil
	case m.running && !m.runAPI:
		m.flash = frontend.BranchRunBlocker
		return nil
	}
	o := &m.branches
	o.open, o.loading, o.busy, o.err, o.base = true, true, "", nil, false
	o.input.SetValue("")
	o.seq++
	m.zoomed = false
	return tea.Batch(o.input.Focus(), m.listBranches())
}

func (m *Model) listBranches() tea.Cmd {
	seq, b := m.branches.seq, m.deps.Branches
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), branchTimeout)
		defer cancel()
		l, err := b.List(ctx)
		return branchListMsg{seq: seq, l: l, err: err}
	}
}

func (m *Model) handleBranchList(msg branchListMsg) {
	o := &m.branches
	if msg.seq != o.seq || !o.open {
		return
	}
	o.loading = false
	if errors.Is(msg.err, gitbranch.ErrNotGit) {
		o.close()
		m.flash = "not a git repository: there are no branches to switch to"
		if o.base {
			m.flash = "not a git repository: there are no branches to compare with"
		}
		return
	}
	o.err = msg.err
	if msg.err == nil {
		o.all, o.note = msg.l.Branches, frontend.BranchRootNote(m.deps.Root, msg.l.Root)
		if o.base {
			o.all, o.note = frontend.BaseBranches(msg.l.Branches, m.deps.DefaultBranch), ""
		}
		cur := ""
		if o.cursor < len(o.list) {
			cur = o.list[o.cursor].Name
		}
		o.filter()
		for i, b := range o.list { // keep the cursor on its branch after a fetch
			if b.Name == cur {
				o.move(i, m.branchesRows(m.geometry().bodyH))
			}
		}
	}
}

// switchBranch switches to the branch under the cursor.
func (m *Model) switchBranch() tea.Cmd {
	o := &m.branches
	if o.busy != "" || o.cursor >= len(o.list) {
		return nil
	}
	b := o.list[o.cursor]
	if b.Current {
		o.close()
		m.flash = "already on " + b.Name
		return nil
	}
	if m.running && !m.runAPI {
		o.err = errors.New(frontend.BranchRunBlocker)
		return nil
	}
	o.busy, o.err = "switching to "+b.Name, nil
	seq, br := o.seq, m.deps.Branches
	return tea.Batch(m.startAnim(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), branchTimeout)
		defer cancel()
		return branchSwitchMsg{seq: seq, name: b.Name, err: br.Switch(ctx, b.Name)}
	})
}

func (m *Model) handleBranchSwitch(msg branchSwitchMsg) tea.Cmd {
	o := &m.branches
	if msg.seq != o.seq || !o.open {
		return nil
	}
	o.busy = ""
	if msg.err != nil {
		o.err = msg.err
		return nil
	}
	o.close()
	m.flash = frontend.BranchSwitched(msg.name)
	if m.watchNoticed { // file watching is off, so nothing else recompiles
		m.deps.Engine.Recompile("switched to " + msg.name)
	}
	return tea.Batch(m.readGit(), m.readAPIGit())
}

// fetchBranches fetches origin, then lists the branches again.
func (m *Model) fetchBranches() tea.Cmd {
	o := &m.branches
	if o.busy != "" {
		return nil
	}
	o.busy, o.err = "fetching origin", nil
	seq, br := o.seq, m.deps.Branches
	return tea.Batch(m.startAnim(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return branchFetchMsg{seq: seq, err: br.Fetch(ctx)}
	})
}

func (m *Model) handleBranchFetch(msg branchFetchMsg) tea.Cmd {
	o := &m.branches
	if msg.seq != o.seq || !o.open {
		return nil
	}
	o.busy, o.err = "", msg.err
	if msg.err != nil {
		return nil
	}
	return tea.Batch(m.listBranches(), m.readGit(), m.readAPIGit())
}

func (m *Model) handleBranchesKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.branches
	rows := m.branchesRows(m.geometry().bodyH)
	switch {
	case key.Matches(msg, k.Escape):
		o.close()
	case key.Matches(msg, k.Accept) && o.base:
		return m.pickBase()
	case key.Matches(msg, k.Accept):
		return m.switchBranch()
	case key.Matches(msg, k.FetchBranches):
		return m.fetchBranches()
	case msg.String() == "up" || msg.String() == "ctrl+p":
		o.move(-1, rows)
	case msg.String() == "down" || msg.String() == "ctrl+n":
		o.move(1, rows)
	case msg.String() == "pgup":
		o.move(-rows, rows)
	case msg.String() == "pgdown":
		o.move(rows, rows)
	default:
		before := o.input.Value()
		var cmd tea.Cmd
		o.input, cmd = o.input.Update(msg)
		if o.input.Value() != before {
			o.filter()
		}
		return cmd
	}
	return nil
}

// branchesRows is how many branches the overlay lists at once.
func (m Model) branchesRows(bodyH int) int {
	// borders, the query, the note, a blank line and the state, plus
	// room for git's refusal
	fixed := 6 + branchErrorLines
	return max(min(branchesMaxRows, max(len(m.branches.all), 1), bodyH-fixed), 1)
}

// branchesBox draws the B overlay.
func (m Model) branchesBox(bodyH int) string {
	s := m.styles
	o := m.branches
	w := min(max(m.width/2, branchesMinW), branchesMaxW, m.width-2)
	inner := w - 4
	rows := m.branchesRows(bodyH)

	count := fmt.Sprintf("%d/%d", len(o.list), len(o.all))
	o.input.SetWidth(max(inner-ansi.StringWidth(count)-4, 1))
	query := o.input.View()
	gap := strings.Repeat(" ", max(inner-ansi.StringWidth(query)-ansi.StringWidth(count), 1))
	lines := []string{query + gap + s.dim.Render(count)}
	if o.note != "" {
		lines = append(lines, s.warnText.Render(o.note))
	}

	switch {
	case o.loading:
		lines = append(lines, "  "+m.spinner()+s.dim.Render(" listing branches…"))
	case len(o.list) == 0:
		lines = append(lines, s.dim.Render("  no matches"))
	}
	now := m.deps.Now()
	end := min(o.offset+rows, len(o.list))
	for i := o.offset; i < end; i++ {
		b := o.list[i]
		name, right, lit := branchRow(b, now)
		if o.base {
			name, right, lit = b.Name, frontend.BaseTags(b, m.deps.DefaultBranch, now), b.Name == m.deps.DefaultBranch
		}
		lines = append(lines, renderBranchRow(s, name, right, lit, inner, i == o.cursor))
	}
	lines = append(lines, "")
	switch {
	case o.busy != "":
		lines = append(lines, m.spinner()+" "+s.warnText.Render(o.busy+"…"))
	case o.err != nil:
		// git's refusal is wrapped, so the files it names show.
		msg := strings.ReplaceAll(strings.TrimSpace(o.err.Error()), "\t", "  ")
		errLines := strings.Split(ansi.Wrap(msg, inner, ""), "\n")
		if len(errLines) > branchErrorLines {
			errLines = append(errLines[:branchErrorLines-1], "…")
		}
		for _, l := range errLines {
			lines = append(lines, s.errorText.Render(l))
		}
	case o.base:
		lines = append(lines, s.dim.Render("ages are origin's, as of the last fetch · ctrl+r fetches"))
	default:
		lines = append(lines, s.dim.Render("remote branches as of the last fetch · ctrl+r fetches"))
	}
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, inner, "…")
	}
	title := "B switch branch"
	if o.base {
		title = "b compare with"
	}
	return s.apiPane(title, "", strings.Join(lines, "\n"), w, min(len(lines)+2, bodyH), true, false)
}

// renderBranchRow draws "▸ name" with its tags right-aligned, the name
// lit for the checked-out branch, or the base.
func renderBranchRow(s styles, name, right string, lit bool, width int, sel bool) string {
	lead := "  "
	if sel {
		lead = "▸ "
	}
	room := max(width-ansi.StringWidth(lead)-ansi.StringWidth(right)-1, 1)
	name = ansi.Truncate(name, room, "…")
	gap := strings.Repeat(" ", max(width-ansi.StringWidth(lead+name)-ansi.StringWidth(right), 1))
	if sel {
		return s.selected.Render(lead + name + gap + right)
	}
	nameStyle := s.text
	if lit {
		nameStyle = s.okText
	}
	return s.dim.Render(lead) + nameStyle.Render(name) + s.dim.Render(gap+right)
}
