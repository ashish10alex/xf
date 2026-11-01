package ui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/frontend"
)

// The first I or C in a Dataform project whose prodCompilerOptions nothing
// sets asks for them before it checks: without them the changes are
// compared with the default targets, which are dev tables in most projects,
// and a dev run of the branch then makes the check pass. The answer goes in
// the project's config file, for the whole team: the options, once the
// project compiles with them, or "" when the default targets are prod. esc
// checks against the default targets and asks again the next time xf
// starts.

// prodPromptKind is which check the prompt was opened for.
type prodPromptKind int

const (
	prodPromptImpact prodPromptKind = iota
	prodPromptCost
)

// prodPrompt is the prompt's state.
type prodPrompt struct {
	open  bool
	input textinput.Model
	// onDefault: "default targets are prod" is picked, not the options.
	onDefault bool
	// saving: the project is compiling with the options.
	saving bool
	err    error
	// after is the check to open once answered.
	after prodPromptKind
	// asked is set once the prompt was answered or dismissed: it isn't
	// shown again while this xf runs.
	asked bool
	// seq tells a save's answer from an earlier one's.
	seq    int
	cancel context.CancelFunc
}

// prodSavedMsg answers the save seq.
type prodSavedMsg struct {
	seq     int
	options string
	err     error
}

// prodSaveTimeout bounds compiling with the options and saving them.
const prodSaveTimeout = 5 * time.Minute

func newProdPrompt() prodPrompt {
	in := textinput.New()
	in.Prompt = palettePrompt
	in.Placeholder = "--default-database=acme-prod"
	return prodPrompt{input: in}
}

// askProd opens the prompt before the check after, and reports whether it
// did: only where prodCompilerOptions is set nowhere, and once per xf.
func (m *Model) askProd(after prodPromptKind) (tea.Cmd, bool) {
	p := &m.prod
	if p.asked || m.deps.SaveProdOptions == nil || m.deps.Unset != config.KeyProdCompilerOptions {
		return nil, false
	}
	m.zoomed = false
	p.open, p.after, p.onDefault, p.saving, p.err = true, after, false, false, nil
	p.input.SetValue("")
	return p.input.Focus(), true
}

// closeProd closes the prompt and opens the check it was asked for.
func (m *Model) closeProd() tea.Cmd {
	p := &m.prod
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.open, p.saving, p.asked = false, false, true
	p.seq++
	p.input.Blur()
	if p.after == prodPromptCost {
		return m.openCost()
	}
	return m.openImpact()
}

// saveProd compiles with options and saves them; "" saves that the
// default targets are prod.
func (m *Model) saveProd(options string) tea.Cmd {
	p := &m.prod
	p.seq++
	p.saving, p.err = true, nil
	ctx, cancel := context.WithTimeout(context.Background(), prodSaveTimeout)
	p.cancel = cancel
	seq, save := p.seq, m.deps.SaveProdOptions
	return tea.Batch(m.startAnim(), func() tea.Msg {
		defer cancel()
		return prodSavedMsg{seq: seq, options: options, err: save(ctx, options)}
	})
}

func (m *Model) handleProdSaved(msg prodSavedMsg) tea.Cmd {
	p := &m.prod
	if msg.seq != p.seq || !p.open {
		return nil
	}
	p.saving, p.cancel = false, nil
	if msg.err != nil {
		p.err = msg.err
		return nil
	}
	m.deps.ProdCompilerOptions, m.deps.Unset = msg.options, ""
	m.setInfoSetting(config.KeyProdCompilerOptions, msg.options, config.ProjectFile)
	cmd := m.closeProd()
	m.flash = "saved " + config.KeyProdCompilerOptions + " to " + config.ProjectFile
	return cmd
}

// setInfoSetting shows key as set to value in from in the , overlay.
func (m *Model) setInfoSetting(key, value, from string) {
	frontend.SetInfoSetting(m.deps.Info, key, value, from)
}

// handleProdKey handles a key while the prompt is open.
func (m *Model) handleProdKey(msg tea.KeyPressMsg) tea.Cmd {
	p := &m.prod
	switch msg.String() {
	case "esc":
		return m.closeProd()
	}
	if p.saving {
		return nil
	}
	switch msg.String() {
	case "tab", "shift+tab", "up", "down":
		p.onDefault = !p.onDefault
		if p.onDefault {
			p.input.Blur()
			return nil
		}
		return p.input.Focus()
	case "enter":
		if p.onDefault {
			return m.saveProd("")
		}
		options := strings.TrimSpace(p.input.Value())
		if options == "" {
			return nil // an empty box is not an answer: tab picks the other one
		}
		return m.saveProd(options)
	}
	if p.onDefault {
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.err = nil
	return cmd
}

// prodBox draws the prompt.
func (m Model) prodBox(bodyH int) string {
	s := m.styles
	p := m.prod
	w := min(max(m.width*2/3, 50), 90, m.width-2)
	inner := w - 4

	wrap := func(text string) []string { return strings.Split(ansi.Wrap(text, inner, ""), "\n") }
	var lines []string
	for _, l := range wrap("Without the compiler options of this project's prod runs, the changes are compared with its " +
		"default targets. If those are dev tables, a run of the branch there hides what it drops.") {
		lines = append(lines, s.dim.Render(l))
	}
	lines = append(lines, "", s.text.Render("Compiler options of this project's prod runs:"))
	p.input.SetWidth(max(inner-ansi.StringWidth(palettePrompt)-2, 1))
	lines = append(lines, p.input.View(), "")
	mark, label := "( ) ", s.text
	if p.onDefault {
		mark, label = "(•) ", s.selected
	}
	lines = append(lines, label.Render(mark+"the default targets are prod"))

	switch {
	case p.saving && p.onDefault:
		lines = append(lines, "", m.spinner()+" "+s.warnText.Render("saving…"))
	case p.saving:
		lines = append(lines, "", m.spinner()+" "+s.warnText.Render("compiling with these options…"))
	case p.err != nil:
		lines = append(lines, "")
		// The first lines say what is wrong; a stack trace follows them.
		text := strings.Split(strings.TrimSpace(p.err.Error()), "\n")
		for _, l := range text[:min(len(text), 2)] {
			for _, wl := range wrap(l) {
				lines = append(lines, s.errorText.Render(wl))
			}
		}
		lines = append(lines, s.dim.Render("The options are flags of dataform compile, such as "+p.input.Placeholder+"."))
	}

	sep := s.dim.Render(" · ")
	keys := s.footerKey.Render("enter") + " " + s.footerDesc.Render("save to "+config.ProjectFile) + sep +
		s.footerKey.Render("tab") + " " + s.footerDesc.Render("switch") + sep +
		s.footerKey.Render("esc") + " " + s.footerDesc.Render("not now")
	lines = append(lines, "", keys)
	for i, l := range lines {
		lines[i] = " " + ansi.Truncate(l, inner, "…")
	}
	return s.pane("compare with prod", strings.Join(lines, "\n"), w, min(len(lines)+2, bodyH), true)
}
