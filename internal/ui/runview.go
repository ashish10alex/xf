package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// runFlags are the toggles shown in the Run pane.
type runFlags struct {
	includeDeps, includeDependents, fullRefresh bool
	api                                         bool // run in GCP through the Dataform API
	// selectedOnly narrows r to the selected action; off, r runs every
	// runnable action the Actions pane lists.
	selectedOnly bool
}

func (f runFlags) options(ids ...project.ActionID) project.RunOptions {
	return project.RunOptions{
		Actions:           ids,
		IncludeDeps:       f.includeDeps,
		IncludeDependents: f.includeDependents,
		FullRefresh:       f.fullRefresh,
	}
}

// with names what a run adds to its actions: "deps", "dependents", "deps
// and dependents", or "" for nothing.
func (f runFlags) with() string {
	switch {
	case f.includeDeps && f.includeDependents:
		return "deps and dependents"
	case f.includeDeps:
		return "deps"
	case f.includeDependents:
		return "dependents"
	}
	return ""
}

// apiOptions is what the UI knows of an API run of targets in the project
// g compiles: main adds the repository and the checkout's state.
func (f runFlags) apiOptions(g *project.Graph, targets ...project.Target) dataformapi.Options {
	return frontend.APIRunOptions(g, f.includeDeps, f.includeDependents, f.fullRefresh, targets...)
}

// runPaneLine renders the one-line Run pane, width cells wide: the toggles
// (a only when API runs are available) and what r runs: run, of the
// selected action's pane. The full command is too long for it; the footer
// shows it while the pane is focused, and the log starts with it.
func runPaneLine(s styles, f runFlags, apiAvail bool, selected *project.Action, run []*project.Action, running, cancelling bool, width int) string {
	line := runToggles(s, f, apiAvail) + "  " + toggleBox(s, f.selectedOnly, "s", "selected only")
	var tail string
	switch {
	case cancelling && f.api:
		tail = s.warnText.Render("cancelling… ctrl+c stops watching")
	case cancelling:
		tail = s.warnText.Render("cancelling… ctrl+c force-kills")
	case running:
		tail = s.warnText.Render("running… ctrl+c cancels")
	case selected == nil:
	case len(run) == 0:
		tail = s.dim.Render(selected.Kind.String() + "s can't be run")
	default:
		verb := "▸ run "
		if f.api {
			verb = "▸ run in GCP "
		}
		name := runLabel(run)
		if len(run) == 1 {
			room := width - ansi.StringWidth(line+"  ·  r "+verb)
			name = fitName(name, run[0].Target.Name, max(room, 8))
		}
		verbStyle := s.dim
		keyStyle := s.footerKey
		if f.fullRefresh {
			verbStyle = s.warnText.Bold(true)
			keyStyle = s.warnText.Bold(true)
		}
		tail = keyStyle.Render("r") + " " + verbStyle.Render(verb) + s.text.Render(name)
	}
	if tail != "" {
		line += s.dim.Render("  ·  ") + tail
	}
	return line
}

// runToggles is the toggles' line: "[ ] d deps  [x] D dependents …", with a
// only when API runs are available.
func runToggles(s styles, f runFlags, apiAvail bool) string {
	box := func(on bool, k, label string) string { return toggleBox(s, on, k, label) }
	line := box(f.includeDeps, "d", "deps") + "  " + box(f.includeDependents, "D", "dependents") + "  " + box(f.fullRefresh, "f", "full-refresh")
	if apiAvail {
		line += "  " + box(f.api, "a", "api")
	}
	return line
}

// toggleBox is one toggle: "[x] d deps".
func toggleBox(s styles, on bool, k, label string) string {
	if !on {
		return s.dim.Render("[ ]") + " " + s.footerKey.Render(k) + " " + s.dim.Render(label)
	}
	tone := s.okText.Bold(true)
	switch label {
	case "full-refresh":
		tone = s.warnText.Bold(true)
	case "api":
		tone = s.accentTitle.Bold(true)
	}
	return tone.Render("[x]") + " " + tone.Render(k) + " " + tone.Render(label)
}

// runActions are the actions r runs: the runnable ones the Actions pane
// lists, in its order, or with selected only on the selected action if it
// is runnable.
func (m Model) runActions() []*project.Action {
	actions := m.paneActions()
	if m.flags.selectedOnly {
		actions = []*project.Action{m.selected}
	}
	var run []*project.Action
	for _, a := range actions {
		if a != nil && a.Runnable() {
			run = append(run, a)
		}
	}
	return run
}

func actionIDs(actions []*project.Action) []project.ActionID {
	ids := make([]project.ActionID, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	return ids
}

// runOptions are the run options for ids with the current flags, with
// the actions' RunName for a tool that selects by name.
func (m Model) runOptions(ids ...project.ActionID) project.RunOptions {
	return frontend.RunOptionsFor(m.graph, ids, m.flags.includeDeps, m.flags.includeDependents, m.flags.fullRefresh)
}

// runCommand says what a run of ids would run: the command line, or for an
// API run the same arguments sent to the Dataform API. The log starts with
// it and the footer shows it while the Run pane is focused.
func (m Model) runCommand(ids ...project.ActionID) string {
	return m.commandFor(m.runOptions(ids...))
}

// commandFor is runCommand for a run of o.
func (m Model) commandFor(o project.RunOptions) string {
	return frontend.RunCommand(m.deps, m.flags.api, o)
}

// fullRefreshMax is how many actions the full-refresh prompt names before
// it counts the rest.
const fullRefreshMax = 8

// fullRefreshBox renders the modal asking to confirm a full-refresh run of
// actions, or of tags when there are any. It names what gets rebuilt and
// has an amber border so it can't be mistaken for the other overlays.
func fullRefreshBox(s styles, f runFlags, tool string, actions []*project.Action, tags []string, width, bodyH int) string {
	names := make([]string, 0, len(actions)+len(tags))
	for _, a := range actions {
		names = append(names, a.Target.Schema+"."+a.Target.Name)
	}
	for _, t := range tags {
		names = append(names, "tag "+t)
	}
	var scope []string
	for i, n := range names {
		if i == fullRefreshMax && len(names) > fullRefreshMax+1 {
			scope = append(scope, s.text.Render(fmt.Sprintf("… and %d more", len(names)-i)))
			break
		}
		scope = append(scope, s.footerKey.Render(n))
	}
	with := f.with()
	switch {
	case with == "":
	case len(actions) == 1: // a tag is many actions
		scope[0] += s.text.Render(" + its " + with)
	default:
		scope = append(scope, s.text.Render("+ their "+with))
	}
	lost := [2]string{"Incremental tables lose their existing rows and", "are rebuilt from scratch."}
	if tool == "dbt" {
		lost = [2]string{"Incremental models lose their existing rows and", "seeds are reloaded: both rebuilt from scratch."}
	}
	lines := []string{
		"",
		"  " + s.warnText.Bold(true).Render("Full refresh drops and rebuilds:"),
		"",
	}
	for _, l := range scope {
		lines = append(lines, "    "+l)
	}
	lines = append(lines,
		"",
		"  "+s.dim.Render(lost[0]),
		"  "+s.dim.Render(lost[1]),
		"",
		"  "+s.footerKey.Render("y")+" "+s.text.Render("run full refresh")+s.dim.Render("   ·   ")+
			s.footerKey.Render("any other key")+" "+s.text.Render("cancel"),
		"",
	)
	w := 4
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l)+4)
	}
	w = min(w, width-4)
	warn := s
	warn.focusBorder = s.warnText.GetForeground()
	warn.focusTitle = s.warnText.Bold(true)
	return warn.pane("⚠ confirm full refresh", strings.Join(lines, "\n"), w, min(len(lines)+2, bodyH), true)
}

// logPane holds the output of the last run. It follows the tail until the
// user scrolls up, and G resumes following.
type logPane struct {
	scroller
	follow bool
	title  string // status shown in the pane title
}

func (p *logPane) reset(s styles, header string) {
	p.follow = true
	p.title = "running…"
	p.setLines([]string{s.dim.Render(header)})
	p.bottom()
}

func (p *logPane) append(line string) {
	p.setLines(append(p.lines, line))
	if p.follow {
		p.bottom()
	}
}

func (p *logPane) finish(s styles, res project.RunResult) {
	secs := fmt.Sprintf("%.1fs", res.Duration.Seconds())
	var status string
	switch {
	case res.Detached:
		status = s.warnText.Render("◌ stopped watching · " + secs + ": the invocation carries on in GCP")
		p.title = "stopped watching · " + secs
	case res.State != "" && res.Err == nil:
		// A run in GCP: say how the invocation ended.
		st := strings.ToLower(res.State)
		switch {
		case res.Canceled:
			status = s.warnText.Render("✗ " + st + " · " + secs)
			p.title = st + " · " + secs
		case res.ExitCode != 0:
			status = s.errorText.Render("✗ " + st + " · " + secs)
			p.title = "✗ " + st + " · " + secs
		default:
			status = s.okText.Render("✓ " + st + " · " + secs)
			p.title = "✓ " + st + " · " + secs
		}
	case res.Canceled:
		status = s.warnText.Render("✗ cancelled · " + secs)
		p.title = "cancelled · " + secs
	case res.Err != nil:
		status = s.errorText.Render("✗ " + res.Err.Error() + " · " + secs)
		p.title = "failed · " + secs
	case res.ExitCode != 0:
		status = s.errorText.Render(fmt.Sprintf("✗ exit %d · %s", res.ExitCode, secs))
		p.title = fmt.Sprintf("✗ exit %d · %s", res.ExitCode, secs)
	default:
		status = s.okText.Render("✓ exit 0 · " + secs)
		p.title = "✓ exit 0 · " + secs
	}
	p.append(status)
}

// scrollBy moves the view and stops following unless it lands on the tail.
func (p *logPane) scrollBy(n int) {
	p.scroll(n)
	p.follow = p.offset >= p.maxOffset()
}
