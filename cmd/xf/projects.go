package main

import (
	"context"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/commanduse"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/ui"
)

// sessionDir is where sessions are kept, or "" when the user has no home
// directory to keep them in.
func sessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return session.Dir(os.Getenv, home)
}

// commandUses keeps how often the palette's commands are done, beside the
// sessions; nil, keeping nothing, when the user has no home directory.
func commandUses() *commanduse.Store {
	dir := sessionDir()
	if dir == "" {
		return nil
	}
	return commanduse.Open(commanduse.Path(dir))
}

// rememberedProjects are the projects xf has opened before that are still
// projects, most recent first.
func rememberedProjects() []session.Project {
	dir := sessionDir()
	if dir == "" {
		return nil
	}
	return session.Projects(dir, backend.Detect)
}

// interactive says someone is at a terminal to pick a project.
func interactive() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// pickProject shows the picker and returns the project picked; ok is false
// when it was closed without picking. inEditor says it is to open in neovim
// with xf beside it: o opens a project in the editor of its settings, read
// with sf.
func pickProject(projects []session.Project, sf *settingFlags) (p session.Project, ok, inEditor bool, err error) {
	home, _ := os.UserHomeDir()
	final, err := tea.NewProgram(ui.NewProjectPicker(ui.PickerDeps{
		Projects: projects,
		Home:     home,
		Forget:   session.Forget,
		Theme:    pickerTheme(sf),
		Branch: func(ctx context.Context, root string) (string, error) {
			return gitbranch.Repo{Dir: root}.Current(ctx)
		},
		Open: func(p session.Project) (frontend.Opening, error) {
			settings, err := sf.load(p.Root)
			if err != nil {
				return frontend.Opening{}, err
			}
			return openWith(editorOf(settings), p.Root, "", 0)
		},
	}), colorOptions()...).Run()
	if err != nil {
		return session.Project{}, false, false, err
	}
	if m, isPicker := final.(ui.ProjectPicker); isPicker {
		p, ok = m.Chosen()
		inEditor = m.InEditor()
	}
	return p, ok, inEditor, nil
}
