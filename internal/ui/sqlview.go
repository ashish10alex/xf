package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/ashish10alex/xf/internal/project"
)

// sqlPane shows the compiled SQL of the selected action: every section in one
// scroll, each under a `-- title` comment, highlighted and line-numbered. It
// behaves like a read-only vim buffer (see buffer and handleSQLKey).
type sqlPane struct {
	buffer
	key string // what the rendered lines were built from
}

// setAction renders the action's SQL, keeping the scroll position when the
// same content is shown again (e.g. after a recompile that didn't change it).
func (p *sqlPane) setAction(a *project.Action, incremental bool, s styles) {
	if a == nil {
		p.key = ""
		p.setText(nil, nil, false)
		return
	}
	sections := a.SQLSections(incremental)
	var b strings.Builder
	for i, sec := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("-- " + sec.Title + "\n" + sec.SQL)
	}
	// Tabs would throw off the cursor's cell arithmetic, and a project saved
	// with CRLF line endings compiles to SQL with \r in it: show and copy
	// plain lines.
	text := strings.NewReplacer("\t", "    ", "\r", "").Replace(b.String())
	key := fmt.Sprintf("%s|%t|%t|%s", a.ID, incremental, s.isDark, text)
	if key == p.key {
		return
	}
	sameAction := strings.HasPrefix(p.key, string(a.ID)+"|")
	p.key = key
	if text == "" {
		msg := noSQLMessage(a)
		p.setText([]string{s.dim.Render(msg)}, [][]rune{[]rune(msg)}, false)
		p.reset()
		return
	}
	lines := strings.Split(text, "\n")
	plain := make([][]rune, len(lines))
	for i, l := range lines {
		plain[i] = []rune(l)
	}
	styled := highlightSQL(text, s.chromaStyle)
	for len(styled) < len(plain) {
		styled = append(styled, "")
	}
	p.setText(styled[:len(plain)], plain, true)
	if !sameAction {
		p.reset()
	}
}

// handleSQLKey handles a key in the focused SQL pane. It runs before the
// global keys so that vim keys win where they clash (v, 0, esc in visual
// mode, and / when the pane is zoomed, which searches the SQL rather than
// finding an action); unhandled keys fall through to the global bindings.
func (m *Model) handleSQLKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	k := m.keys
	b := &m.sql.buffer
	if len(b.text) == 0 {
		return false, nil
	}
	if b.pendingY {
		b.pendingY = false
		if msg.String() == "y" {
			return true, m.yank(b.yankLine(), 1, true)
		}
	}
	switch {
	case key.Matches(msg, k.Up):
		b.moveRow(-1)
	case key.Matches(msg, k.Down):
		b.moveRow(1)
	case key.Matches(msg, k.Left):
		b.setCol(b.col - 1)
	case key.Matches(msg, k.Right):
		b.setCol(b.col + 1)
	case key.Matches(msg, k.WordNext):
		b.wordForward(msg.String() == "W")
	case key.Matches(msg, k.WordPrev):
		b.wordBackward(msg.String() == "B")
	case msg.String() == "0":
		b.setCol(0)
	case msg.String() == "^":
		b.setCol(firstNonBlank(b.line()))
	case key.Matches(msg, k.LineEnd):
		b.setCol(len(b.line()))
		b.want = -1
	case key.Matches(msg, k.Top):
		b.gotoRow(0)
	case key.Matches(msg, k.Bottom):
		b.gotoRow(len(b.text) - 1)
	case key.Matches(msg, k.HalfDown):
		b.halfPage(1)
	case key.Matches(msg, k.HalfUp):
		b.halfPage(-1)
	case key.Matches(msg, k.ScrollDown):
		b.scrollView(1)
	case key.Matches(msg, k.ScrollUp):
		b.scrollView(-1)
	case key.Matches(msg, k.Visual):
		b.toggleVisual(visualChar)
	case key.Matches(msg, k.VisualLine):
		b.toggleVisual(visualLine)
	case key.Matches(msg, k.Escape) && b.visual != visualNone:
		b.visual = visualNone
	case key.Matches(msg, k.Escape) && b.hlHits:
		b.hlHits = false
	case key.Matches(msg, k.Search) && m.zoomed:
		return true, m.startSQLSearch()
	case key.Matches(msg, k.NextMatch):
		m.stepSQLSearch(1)
	case key.Matches(msg, k.PrevMatch):
		m.stepSQLSearch(-1)
	case key.Matches(msg, k.SearchWord):
		m.searchSQLWord(false)
	case key.Matches(msg, k.SearchWordBack):
		m.searchSQLWord(true)
	case key.Matches(msg, k.OtherEnd) && b.visual != visualNone:
		b.swapVisualEnds()
	case key.Matches(msg, k.Yank) && b.visual != visualNone:
		linewise := b.visual == visualLine
		text, lines := b.yankVisual()
		return true, m.yank(text, lines, linewise)
	case key.Matches(msg, k.Yank):
		b.pendingY = true
	case key.Matches(msg, k.YankLine):
		return true, m.yank(b.yankLine(), 1, true)
	default:
		return false, nil
	}
	return true, nil
}

// yank copies text to the clipboard: natively when main provides a way, and
// with OSC 52 so it also works over ssh.
func (m *Model) yank(text string, lines int, linewise bool) tea.Cmd {
	switch {
	case linewise || lines > 1:
		m.flash = fmt.Sprintf("copied %d line%s", lines, plural(lines))
	default:
		n := len([]rune(text))
		m.flash = fmt.Sprintf("copied %d character%s", n, plural(n))
	}
	cmds := []tea.Cmd{tea.SetClipboard(text)}
	if c := m.deps.Copy; c != nil {
		cmds = append(cmds, func() tea.Msg {
			_ = c(text) // OSC 52 may still have worked
			return nil
		})
	}
	return tea.Batch(cmds...)
}

// highlightSQL colours SQL with chroma and returns one string per line. Tokens
// are styled per line (rather than with chroma's terminal formatter) so a
// multi-line token such as a block comment never leaves an escape sequence
// open across a line break.
func highlightSQL(text, styleName string) []string {
	return highlightCode("sql", text, styleName)
}

// highlightCode is highlightSQL for the language chroma calls lang.
func highlightCode(lang, text, styleName string) []string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		return strings.Split(text, "\n")
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	style := chromastyles.Get(styleName)

	var lines []string
	var cur strings.Builder
	cache := map[chroma.TokenType]lipgloss.Style{}
	for tok := it(); tok != chroma.EOF; tok = it() {
		st, ok := cache[tok.Type]
		if !ok {
			st = tokenStyle(style.Get(tok.Type))
			cache[tok.Type] = st
		}
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
			}
			if part != "" {
				cur.WriteString(st.Render(part))
			}
		}
	}
	lines = append(lines, cur.String())
	// Tokenise adds a trailing newline to the input.
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func tokenStyle(e chroma.StyleEntry) lipgloss.Style {
	st := lipgloss.NewStyle()
	if e.Colour.IsSet() {
		st = st.Foreground(lipgloss.Color(e.Colour.String()))
	}
	if e.Bold == chroma.Yes {
		st = st.Bold(true)
	}
	if e.Italic == chroma.Yes {
		st = st.Italic(true)
	}
	return st
}
