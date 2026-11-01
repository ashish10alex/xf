package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

type engineMsg struct{ state engine.State }

// waitForEngine waits for the engine's next state. It is re-armed after
// every engineMsg; a nil or closed channel ends the loop.
func waitForEngine(ch <-chan engine.State) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		st, ok := <-ch
		if !ok {
			return nil
		}
		return engineMsg{st}
	}
}

type editorMsg struct{ ev project.EditorEvent }

// openedMsg reports asking the editor to open file.
type openedMsg struct {
	file string // relative to the project, as shown
	err  error
	// asked is set for the answer to o itself, not for a terminal editor
	// closing: an editor in another window then has the file, which the
	// footer says, as nothing else here shows it.
	asked bool
	// opening is what is left for xf to do; path and line are what was
	// asked for.
	opening Opening
	path    string
	line    int
}

// fullScreenMsg reports asking the editor to toggle xf full screen.
type fullScreenMsg struct{ err error }

// urlOpenedMsg reports opening an API run's URL in the browser.
type urlOpenedMsg struct{ err error }

// waitForEditor waits for the next editor event. Like waitForFileChange it is
// re-armed after every event; a closed channel ends the loop.
func waitForEditor(ch <-chan project.EditorEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return editorMsg{ev}
	}
}

type (
	runStartedMsg  struct{ run *project.Run }
	runStartErrMsg struct{ err error }
	runLineMsg     struct{ line string }
	runFinishedMsg struct{ res project.RunResult }
)

func startRunCmd[O any](ctx context.Context, start func(context.Context, O) (*project.Run, error), opts O) tea.Cmd {
	return func() tea.Msg {
		r, err := start(ctx, opts)
		if err != nil {
			return runStartErrMsg{err}
		}
		return runStartedMsg{r}
	}
}

// waitForRunEvent reads the next line of a run, or its result once the
// output ends. It is re-issued after every line, so the UI never blocks.
func waitForRunEvent(r *project.Run) tea.Cmd {
	return func() tea.Msg {
		if line, ok := <-r.Lines; ok {
			return runLineMsg{line}
		}
		return runFinishedMsg{<-r.Done}
	}
}
