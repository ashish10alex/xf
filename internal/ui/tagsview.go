package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// The T overlay runs tags: every action with any of the checked ones, as
// the tool selects them (dataform run --tags, the Dataform API's
// includedTags, dbt build --select tag:…). It is a query line over the
// project's tags, each with how many actions it would run; typing filters
// them, space checks the one under the cursor and enter runs the checked
// ones. Nothing starts checked, and a filter hides a checked tag without
// unchecking it: the checked ones are named under the list, wherever they
// are in it.
//
// Every letter belongs to the query, so the run's options (the Run pane's)
// have their own mode: esc leaves the query for it, where d, D, f and a
// toggle as in the Run pane, / goes back to the query and esc closes the
// overlay.

const (
	tagsMaxRows = 12
	// tagsMinW has room for the options with their keys.
	tagsMinW = 64
	tagsMaxW = 80
)

// tagItem is a tag as the overlay lists it.
type tagItem = frontend.Tag

// tagItems lists the tags of g's actions by name.
func tagItems(g *project.Graph) []tagItem { return frontend.Tags(g) }

// tagsOverlay is the T overlay's state.
type tagsOverlay struct {
	open    bool
	input   textinput.Model
	all     []tagItem // every tag, by name
	list    []tagItem // the ones the query matches, best first
	checked map[string]bool
	// options is the mode esc switches to, where letters toggle the run's
	// options rather than type.
	options bool
	cursor  int
	offset  int
}

func newTagsOverlay() tagsOverlay {
	in := textinput.New()
	in.Prompt = palettePrompt
	in.Placeholder = "find tag"
	return tagsOverlay{input: in}
}

func (o *tagsOverlay) show(items []tagItem) tea.Cmd {
	o.open, o.options = true, false
	o.all = items
	o.checked = map[string]bool{}
	o.input.SetValue("")
	o.filter()
	return o.input.Focus()
}

func (o *tagsOverlay) close() {
	o.open = false
	o.input.Blur()
}

// update feeds a message to the query input and re-filters when it changes.
func (o *tagsOverlay) update(msg tea.Msg) tea.Cmd {
	before := o.input.Value()
	var cmd tea.Cmd
	o.input, cmd = o.input.Update(msg)
	if o.input.Value() != before {
		o.filter()
	}
	return cmd
}

// filter lists the tags the query fuzzy-matches, best first; all of them,
// by name, for an empty query.
func (o *tagsOverlay) filter() {
	o.cursor, o.offset = 0, 0
	o.list = frontend.FilterTags(o.all, o.input.Value())
}

func (o *tagsOverlay) move(n, rows int) {
	o.cursor = min(max(o.cursor+n, 0), max(len(o.list)-1, 0))
	if o.cursor < o.offset {
		o.offset = o.cursor
	}
	if o.cursor >= o.offset+rows {
		o.offset = o.cursor - rows + 1
	}
	o.offset = min(max(o.offset, 0), max(len(o.list)-rows, 0))
}

// toggle checks or unchecks the tag under the cursor.
func (o *tagsOverlay) toggle() {
	if o.cursor < len(o.list) {
		name := o.list[o.cursor].Name
		if o.checked[name] {
			delete(o.checked, name)
		} else {
			o.checked[name] = true
		}
	}
}

// run is the checked tags by name, listed or not, and how many actions
// they have between them.
func (o *tagsOverlay) run() (tags []string, actions int) {
	seen := map[project.ActionID]bool{}
	for _, t := range o.all {
		if !o.checked[t.Name] {
			continue
		}
		tags = append(tags, t.Name)
		for _, id := range t.Actions {
			seen[id] = true
		}
	}
	return tags, len(seen)
}

// openTags opens the T overlay on the project's tags.
func (m *Model) openTags() tea.Cmd {
	switch {
	case !m.canRun():
		m.flash = "running is not available"
		return nil
	case m.running:
		m.flash = "a run is already in progress (ctrl+c cancels it)"
		return nil
	case m.graph == nil:
		m.flash = "no actions yet: waiting for a compile"
		return nil
	}
	items := tagItems(m.graph)
	if len(items) == 0 {
		m.flash = "no tags in this project"
		return nil
	}
	m.zoomed = false
	return m.tags.show(items)
}

func (m *Model) handleTagsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.tags
	rows := m.tagsRows(m.geometry().bodyH)
	if o.options {
		switch {
		case key.Matches(msg, k.Escape):
			o.close()
		case key.Matches(msg, k.Find):
			o.options = false
			return o.input.Focus()
		case key.Matches(msg, k.Deps):
			m.flags.includeDeps = !m.flags.includeDeps
		case key.Matches(msg, k.Dependents):
			m.flags.includeDependents = !m.flags.includeDependents
		case key.Matches(msg, k.FullRefresh):
			m.flags.fullRefresh = !m.flags.fullRefresh
		case key.Matches(msg, k.API):
			m.toggleAPI()
		case key.Matches(msg, k.RunChecked):
			return m.startTagRun()
		case key.Matches(msg, k.ToggleTag):
			o.toggle()
		case key.Matches(msg, k.Up):
			o.move(-1, rows)
		case key.Matches(msg, k.Down):
			o.move(1, rows)
		case key.Matches(msg, k.HalfUp):
			o.move(-rows, rows)
		case key.Matches(msg, k.HalfDown):
			o.move(rows, rows)
		}
		return nil
	}
	switch {
	case key.Matches(msg, k.Escape):
		o.options = true
		o.input.Blur()
	case key.Matches(msg, k.RunChecked):
		return m.startTagRun()
	case key.Matches(msg, k.ToggleTag):
		o.toggle()
	case msg.String() == "up" || msg.String() == "ctrl+p":
		o.move(-1, rows)
	case msg.String() == "down" || msg.String() == "ctrl+n":
		o.move(1, rows)
	case msg.String() == "pgup":
		o.move(-rows, rows)
	case msg.String() == "pgdown":
		o.move(rows, rows)
	default:
		return o.update(msg)
	}
	return nil
}

// startTagRun runs the checked tags with the current flags. Like r, it asks
// first for a full refresh.
func (m *Model) startTagRun() tea.Cmd {
	tags, _ := m.tags.run()
	if len(tags) == 0 {
		m.flash = "check a tag with space, then enter runs the checked tags"
		return nil
	}
	m.tags.close()
	if m.flags.fullRefresh {
		m.confirmTags = tags
		return nil
	}
	return m.launchTagRun(tags)
}

// launchTagRun runs tags with the current flags.
func (m *Model) launchTagRun(tags []string) tea.Cmd {
	opts := m.flags.options()
	opts.Tags = tags
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancelRun, m.runAPI, m.runURL = true, cancel, m.flags.api, nil
	m.runLabel, m.runShort = tagsLabel(tags), tagsLabel(tags)
	m.log.reset(m.styles, m.commandFor(opts))
	m.runTable.reset()
	if !m.showLog {
		m.showLog = true
		m.layout()
	}
	if m.runAPI {
		api := m.flags.apiOptions(m.graph)
		api.Invocation.IncludedTargets, api.Invocation.IncludedTags = nil, tags
		return startRunCmd(ctx, m.deps.StartAPIRun, api)
	}
	return startRunCmd(ctx, m.deps.StartRun, opts)
}

// tagsRows is how many tags the overlay lists at once.
func (m Model) tagsRows(bodyH int) int {
	// borders, the query, a blank line, the summary and the options
	fixed := 6
	if m.flags.api {
		fixed += 2 // what an API run compiles
	}
	return max(min(tagsMaxRows, len(m.tags.all), bodyH-fixed), 1)
}

// tagsBox draws the T overlay.
func (m Model) tagsBox(bodyH int) string {
	s := m.styles
	o := m.tags
	w := min(max(m.width/2, tagsMinW), tagsMaxW, m.width-2)
	inner := w - 4
	rows := m.tagsRows(bodyH)

	count := fmt.Sprintf("%d/%d", len(o.list), len(o.all))
	o.input.SetWidth(max(inner-ansi.StringWidth(count)-4, 1))
	query := o.input.View()
	gap := strings.Repeat(" ", max(inner-ansi.StringWidth(query)-ansi.StringWidth(count), 1))
	lines := []string{query + gap + s.dim.Render(count)}

	if len(o.list) == 0 {
		lines = append(lines, s.dim.Render("  no matches"))
	}
	end := min(o.offset+rows, len(o.list))
	for i := o.offset; i < end; i++ {
		lines = append(lines, o.renderRow(s, o.list[i], inner, i == o.cursor))
	}
	for len(lines) < rows+1 { // the box keeps its height as the list narrows
		lines = append(lines, "")
	}

	tags, actions := o.run()
	summary := s.dim.Render("no tag checked")
	if len(tags) > 0 {
		summary = s.text.Render(fmt.Sprintf("%d checked", len(tags))) + s.dim.Render(" · ") +
			s.text.Render(fmt.Sprintf("%d action%s", actions, plural(actions)))
		if with := m.flags.with(); with != "" {
			summary += s.dim.Render(" + their " + with)
		}
	}
	// The checked tags' names take the blank line under the list, so the
	// list never moves as tags are checked.
	lines = append(lines, checkedTags(s, tags, inner), summary, m.tagsOptions())
	if m.flags.api {
		lines = append(lines, m.apiGitLine("run"), s.dim.Render("the counts are your working tree's"))
	}
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, inner, "…")
	}
	return s.apiPane("T run tags", "", strings.Join(lines, "\n"), w, min(len(lines)+2, bodyH), true, m.flags.api)
}

// checkedTags names the checked tags on one line, width cells wide: as
// many as fit, then how many more. It is empty when none is checked.
func checkedTags(s styles, tags []string, width int) string {
	if len(tags) == 0 {
		return ""
	}
	line := ""
	for i, t := range tags {
		next := line + t
		if i > 0 {
			next = line + ", " + t
		}
		more := fmt.Sprintf(" +%d more", len(tags)-i-1)
		if i == len(tags)-1 {
			more = ""
		}
		if i > 0 && ansi.StringWidth("[x] "+next+more) > width {
			return s.okText.Render("[x] ") + s.text.Render(line) + s.dim.Render(fmt.Sprintf(" +%d more", len(tags)-i))
		}
		line = next
	}
	return s.okText.Render("[x] ") + s.text.Render(line)
}

// tagsOptions is the Run pane's toggles as they are: with their keys in
// the options mode, where they work, and without while typing.
func (m Model) tagsOptions() string {
	s := m.styles
	if m.tags.options {
		return runToggles(s, m.flags, m.deps.StartAPIRun != nil)
	}
	box := func(on bool, label string) string {
		if on {
			tone := s.okText.Bold(true)
			switch label {
			case "full-refresh":
				tone = s.warnText.Bold(true)
			case "api":
				tone = s.accentTitle.Bold(true)
			}
			return tone.Render("[x]") + " " + tone.Render(label)
		}
		return s.dim.Render("[ ] " + label)
	}
	f := m.flags
	line := box(f.includeDeps, "deps") + "  " + box(f.includeDependents, "dependents") + "  " + box(f.fullRefresh, "full-refresh")
	if m.deps.StartAPIRun != nil {
		line += "  " + box(f.api, "api")
	}
	return line
}

// renderRow draws "▸ [x] tag" with its count of actions right-aligned.
func (o *tagsOverlay) renderRow(s styles, t tagItem, width int, sel bool) string {
	lead, box := "  ", "[ ] "
	if sel {
		lead = "▸ "
	}
	if o.checked[t.Name] {
		box = "[x] "
	}
	count := fmt.Sprintf("%d", len(t.Actions))
	room := max(width-ansi.StringWidth(lead+box)-ansi.StringWidth(count)-1, 1)
	name := ansi.Truncate(t.Name, room, "…")
	gap := strings.Repeat(" ", max(width-ansi.StringWidth(lead+box+name)-ansi.StringWidth(count), 1))
	if sel {
		return s.selected.Render(lead + box + name + gap + count)
	}
	boxStyle := s.dim
	if o.checked[t.Name] {
		boxStyle = s.okText
	}
	return s.dim.Render(lead) + boxStyle.Render(box) + s.text.Render(name) + s.dim.Render(gap+count)
}
