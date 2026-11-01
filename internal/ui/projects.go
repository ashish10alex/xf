package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// PickerDeps is what the project picker needs from outside.
type PickerDeps struct {
	// Projects are the projects to pick from, most recently visited first.
	Projects []session.Project
	// Current is the root of the project xf was run in, or "".
	Current string
	// Home is the user's home directory, shown as ~; "" shows whole paths.
	Home string
	// Open opens a project's folder in the editor its settings name; nil
	// hides the o key.
	Open func(session.Project) (Opening, error)
	// Forget removes a project from the list for good.
	Forget func(session.Project) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Theme is the saved colour theme (auto, light or dark).
	Theme string
	// Branch reads a project's checked-out branch (gitbranch.Repo.Current);
	// nil leaves the branches out.
	Branch func(ctx context.Context, root string) (string, error)
}

// pickerBranchTimeout bounds the read of one project's branch.
const pickerBranchTimeout = 3 * time.Second

// pickerBranchMax is how wide a branch name the picker shows.
const pickerBranchMax = 28

// projectBranchMsg is a project's branch, "" outside a repository.
type projectBranchMsg struct{ root, branch string }

// ReadProjectBranches reads the branch of each of projects at once, each
// answer a message as it arrives.
func ReadProjectBranches(read func(ctx context.Context, root string) (string, error), projects []session.Project) tea.Cmd {
	if read == nil {
		return nil
	}
	var cmds []tea.Cmd
	seen := map[string]bool{}
	for _, it := range projects {
		if seen[it.Root] {
			continue // a root with both tools
		}
		seen[it.Root] = true
		root := it.Root
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), pickerBranchTimeout)
			defer cancel()
			b, _ := read(ctx, root)
			return projectBranchMsg{root: root, branch: b}
		})
	}
	return tea.Batch(cmds...)
}

// ProjectPicker lists the projects xf has opened before or xf search found,
// when xf is run outside any project. It is a program of its own, run before
// the TUI opens the project picked.
type ProjectPicker struct {
	deps     PickerDeps
	items    []session.Project
	shown    []int // the items matching the filter, as indexes into items
	cursor   int   // into shown
	current  string
	filter   string
	typing   bool // keys go to the filter
	forget   bool // waiting for y to forget the selected project
	flash    string
	chosen   *session.Project
	inEditor bool // chosen is to open in the editor, with xf beside it
	width    int
	height   int
	styles   styles
	quitting bool
	// branches are the projects' branches by root, as they arrive.
	branches map[string]string
}

// NewProjectPicker returns the picker for deps. In a project, its cursor
// starts on the most recent other project, so enter goes back to it.
func NewProjectPicker(deps PickerDeps) ProjectPicker {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	p := ProjectPicker{deps: deps, items: deps.Projects, styles: themeStyles(validTheme(deps.Theme), true), width: 80, height: 24,
		branches: map[string]string{}}
	if deps.Current != "" {
		p.current = project.RealPath(deps.Current)
	}
	p.applyFilter()
	if len(p.shown) > 1 && p.isCurrent(p.items[p.shown[0]]) {
		p.cursor = 1
	}
	return p
}

// Chosen is the project picked, if one was.
func (p ProjectPicker) Chosen() (session.Project, bool) {
	if p.chosen == nil {
		return session.Project{}, false
	}
	return *p.chosen, true
}

// InEditor says the project chosen is to open in the editor with xf beside
// it (o with neovim as the editor) rather than in xf alone.
func (p ProjectPicker) InEditor() bool { return p.inEditor }

// projectOpenedMsg reports asking the editor to open a project.
type projectOpenedMsg struct {
	project session.Project
	opening Opening
	err     error
}

func (p ProjectPicker) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, ReadProjectBranches(p.deps.Branch, p.items))
}

func (p ProjectPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		p.styles = themeStyles(validTheme(p.deps.Theme), msg.IsDark())
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	case projectBranchMsg:
		p.branches[msg.root] = msg.branch
	case projectOpenedMsg:
		name := filepath.Base(msg.project.Root)
		switch {
		case msg.err != nil:
			p.flash = "could not open " + name + ": " + msg.err.Error()
		case msg.opening.Run != nil:
			it := msg.project
			return p, tea.ExecProcess(msg.opening.Run, func(err error) tea.Msg {
				if err == nil {
					return nil
				}
				return projectOpenedMsg{project: it, err: err}
			})
		case msg.opening.Leave:
			p.chosen, p.inEditor = &msg.project, true
			return p.quit()
		default:
			p.flash = "opened " + name + " in the editor"
		}
	}
	return p, nil
}

func (p ProjectPicker) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p.flash = ""
	key := msg.String()
	if p.forget {
		p.forget = false
		if key == "y" {
			p.forgetSelected()
		}
		return p, nil
	}
	switch key {
	case "ctrl+c":
		return p.quit()
	case "enter":
		if it, ok := p.selected(); ok {
			p.chosen = &it
			return p.quit()
		}
		return p, nil
	case "up", "ctrl+p":
		p.move(-1)
		return p, nil
	case "down", "ctrl+n":
		p.move(1)
		return p, nil
	}
	if p.typing {
		switch key {
		case "esc":
			p.typing, p.filter = false, ""
			p.applyFilter()
		case "backspace":
			if r := []rune(p.filter); len(r) > 0 {
				p.filter = string(r[:len(r)-1])
				p.applyFilter()
			} else {
				p.typing = false
			}
		default:
			if msg.Text != "" {
				p.filter += msg.Text
				p.applyFilter()
			}
		}
		return p, nil
	}
	switch key {
	case "q":
		return p.quit()
	case "esc":
		if p.filter == "" {
			return p.quit()
		}
		p.filter = ""
		p.applyFilter()
	case "/":
		p.typing = true
	case "j":
		p.move(1)
	case "k":
		p.move(-1)
	case "g":
		p.cursor = 0
	case "G":
		p.cursor = max(len(p.shown)-1, 0)
	case "o":
		if it, ok := p.selected(); ok && p.deps.Open != nil {
			open := p.deps.Open
			return p, func() tea.Msg {
				opening, err := open(it)
				return projectOpenedMsg{project: it, opening: opening, err: err}
			}
		}
	case "x":
		if _, ok := p.selected(); ok && p.deps.Forget != nil {
			p.forget = true
		}
	}
	return p, nil
}

func (p ProjectPicker) quit() (tea.Model, tea.Cmd) {
	p.quitting = true
	return p, tea.Quit
}

func (p *ProjectPicker) move(by int) {
	p.cursor = max(min(p.cursor+by, len(p.shown)-1), 0)
}

func (p ProjectPicker) selected() (session.Project, bool) {
	if p.cursor >= len(p.shown) {
		return session.Project{}, false
	}
	return p.items[p.shown[p.cursor]], true
}

func (p ProjectPicker) isCurrent(it session.Project) bool {
	return p.current != "" && project.RealPath(it.Root) == p.current
}

// forgetSelected removes the selected project from the list and from disk.
func (p *ProjectPicker) forgetSelected() {
	it, ok := p.selected()
	if !ok {
		return
	}
	if err := p.deps.Forget(it); err != nil {
		p.flash = "could not forget " + filepath.Base(it.Root) + ": " + err.Error()
		return
	}
	i := p.shown[p.cursor]
	p.items = append(p.items[:i:i], p.items[i+1:]...)
	p.applyFilter()
	p.flash = "forgot " + filepath.Base(it.Root)
}

// applyFilter shows the items with every word of the filter in their name,
// tool or path, still most recent first.
func (p *ProjectPicker) applyFilter() {
	p.shown = p.shown[:0:0]
	for i, it := range p.items {
		if frontend.ProjectMatches(p.filter, it) {
			p.shown = append(p.shown, i)
		}
	}
	p.cursor = max(min(p.cursor, len(p.shown)-1), 0)
}

func (p ProjectPicker) View() tea.View {
	v := tea.NewView(p.render())
	v.AltScreen = true
	v.WindowTitle = "xf · projects"
	return v
}

func (p ProjectPicker) render() string {
	if p.quitting {
		return ""
	}
	s := p.styles
	title := "Open a project"
	if p.typing || p.filter != "" {
		title += " /" + p.filter
		if p.typing {
			title += "█"
		}
	}
	h := max(p.height-1, 3)
	return s.pane(title, p.body(p.width-2, h-2), p.width, h, true) + "\n" + ansi.Truncate(p.footer(), p.width, "…")
}

// body is the list, scrolled to keep the cursor in its h lines.
func (p ProjectPicker) body(w, h int) string {
	s := p.styles
	if len(p.shown) == 0 {
		if len(p.items) == 0 {
			return s.dim.Render(" no projects left")
		}
		return s.dim.Render(" no project matches " + p.filter)
	}
	now := p.deps.Now()
	var nameW, branchW, toolW, pathW, whenW int
	paths, whens, branches := make([]string, len(p.shown)), make([]string, len(p.shown)), make([]string, len(p.shown))
	for n, i := range p.shown {
		it := p.items[i]
		if p.deps.Branch != nil {
			b, ok := p.branches[it.Root]
			if !ok {
				b = "…"
			}
			branches[n] = ansi.Truncate(b, pickerBranchMax, "…")
			branchW = max(branchW, ansi.StringWidth(branches[n]))
		}
		paths[n] = shortPath(p.deps.Home, it.Root)
		whens[n] = relTime(now, it.LastVisited)
		if p.isCurrent(it) {
			whens[n] += " · here"
		}
		nameW = max(nameW, ansi.StringWidth(filepath.Base(it.Root)))
		toolW = max(toolW, ansi.StringWidth(it.Backend))
		pathW = max(pathW, ansi.StringWidth(paths[n]))
		whenW = max(whenW, ansi.StringWidth(whens[n]))
	}
	// A long path loses its start, not the time beside it: its end is what
	// tells two checkouts apart.
	gapB := 0
	if branchW > 0 {
		gapB = 2
	}
	pathW = max(min(pathW, w-nameW-branchW-gapB-toolW-whenW-8), 8)
	first := max(p.cursor-h+1, 0)
	var lines []string
	for n := first; n < len(p.shown) && n < first+h; n++ {
		it := p.items[p.shown[n]]
		path, when := paths[n], whens[n]
		if r := []rune(path); len(r) > pathW {
			path = "…" + string(r[len(r)-pathW+1:])
		}
		name := fmt.Sprintf("%-*s", nameW, filepath.Base(it.Root))
		branch := ""
		if branchW > 0 {
			branch = "  " + branches[n] + strings.Repeat(" ", branchW-ansi.StringWidth(branches[n]))
		}
		rest := fmt.Sprintf("  %-*s  %-*s  %s", toolW, it.Backend, pathW, path, when)
		if n != p.cursor {
			lines = append(lines, "  "+s.text.Render(name+branch)+s.dim.Render(rest))
			continue
		}
		line := ansi.Truncate("> "+name+branch+rest, w, "…")
		lines = append(lines, s.selected.Render(line+strings.Repeat(" ", max(w-ansi.StringWidth(line), 0))))
	}
	return strings.Join(lines, "\n")
}

func (p ProjectPicker) footer() string {
	s := p.styles
	if p.forget {
		it, _ := p.selected()
		return s.warnText.Render("forget "+filepath.Base(it.Root)+"? ") + s.footerKey.Render("y") + " " + s.footerDesc.Render("forget") +
			s.footerDesc.Render(" · any other key cancels")
	}
	if p.flash != "" {
		return s.warnText.Render(p.flash)
	}
	keys := [][2]string{{"enter", "open"}, {"o", "open in editor"}, {"j/k", "select"}, {"/", "filter"}, {"x", "forget"}, {"q", "quit"}}
	if p.deps.Open == nil {
		keys = slices.Delete(keys, 1, 2)
	}
	if p.typing {
		keys = [][2]string{{"enter", "open"}, {"↑/↓", "select"}, {"esc", "clear"}}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, s.footerKey.Render(k[0])+" "+s.footerDesc.Render(k[1]))
	}
	return strings.Join(parts, s.footerDesc.Render(" · "))
}
