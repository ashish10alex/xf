package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/dataformapi"
)

// While the a toggle is on, a line under the toggles says what an API run
// runs: origin/<branch>, as of the last fetch, and how the checkout
// differs from it. Dataform compiles the pushed branch, so uncommitted and
// unpushed changes don't run and unpulled commits do.

// apiGitTimeout bounds one read of the checkout.
const apiGitTimeout = 10 * time.Second

// apiGitState is the checkout as an API run would see it, read while the a
// toggle is on.
type apiGitState struct {
	g     dataformapi.GitState
	err   error
	known bool
	// seq counts the reads asked for: only the latest's answer is kept.
	seq int
}

type apiGitMsg struct {
	seq int
	g   dataformapi.GitState
	err error
}

// readAPIGit reads the checkout again while the a toggle is on: when it
// turns on, when a compile starts (a file changed), when the terminal
// gets focus back (a commit or push elsewhere) and when an API run starts.
func (m *Model) readAPIGit() tea.Cmd {
	if !m.flags.api || m.deps.APIGit == nil {
		return nil
	}
	m.apiGit.seq++
	seq, read := m.apiGit.seq, m.deps.APIGit
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiGitTimeout)
		defer cancel()
		g, err := read(ctx)
		return apiGitMsg{seq: seq, g: g, err: err}
	}
}

func (m *Model) handleAPIGit(msg apiGitMsg) {
	if msg.seq != m.apiGit.seq {
		return
	}
	m.apiGit.g, m.apiGit.err, m.apiGit.known = msg.g, msg.err, true
}

// apiGitLine is the git line for the views of verb (see apiGitParts),
// "" while the a toggle is off.
func (m Model) apiGitLine(verb string) string {
	if !m.flags.api {
		return ""
	}
	a := m.apiGit
	return m.styles.gitLine(apiGitParts(a.g, a.err, a.known, verb))
}

// gitLine draws a git line's parts in their tones.
func (s styles) gitLine(parts []GitPart) string {
	var b strings.Builder
	for _, p := range parts {
		st := s.text
		switch p.Tone {
		case "dim":
			st = s.dim
		case "warn":
			st = s.warnText
		case "error":
			st = s.errorText
		}
		b.WriteString(st.Render(p.Text))
	}
	return b.String()
}
