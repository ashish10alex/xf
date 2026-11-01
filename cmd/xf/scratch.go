package main

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/atotto/clipboard"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/web"
)

// noEngine is the engine of xf scratch outside a project: it compiles
// nothing, and reports a project with no actions.
type noEngine struct{}

func (noEngine) Subscribe() (<-chan engine.State, func()) {
	states := make(chan engine.State, 1)
	states <- engine.State{Graph: project.NewGraph(nil), Compiles: 1, CompiledAt: time.Now()}
	return states, func() {}
}
func (noEngine) Recompile(string)       {}
func (noEngine) Focus(project.ActionID) {}
func (noEngine) SetTheme(string)        {}

// scratchHome is where scratches live outside a project: tmp in xf's user
// config folder, where the user's config.json is (~/.config/xf, or
// %AppData%\xf on Windows).
func scratchHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config.UserDir(os.Getenv, home), "tmp"), nil
}

// standaloneScratch is the project xf scratch opens outside any: no models
// and no engine, only BigQuery and a folder for scratches.
func standaloneScratch(sf *settingFlags, projectID, location string) (*web.Project, error) {
	settings, err := sf.load("")
	if err != nil {
		return nil, err
	}
	dir, err := scratchHome()
	if err != nil {
		return nil, err
	}
	bq := lazyClient()
	deps := frontend.Deps{
		Root:        filepath.Dir(dir),
		Engine:      noEngine{},
		Copy:        clipboard.WriteAll,
		OpenURL:     browserOpener(),
		Theme:       settings.Theme,
		SaveTheme:   saveTheme,
		CommandUses: commandUses(),
		DryRun:      dryRunner(bq),
		Table:       lazyTables(bq),
	}
	p := &web.Project{Deps: deps, BigQuery: bq, ScratchDir: dir, ScratchProject: projectID, ScratchLocation: location, Standalone: true, StartScratch: true}
	p.ScratchWarnGB, _ = strconv.ParseFloat(settings.ScratchWarnGB, 64)
	p.ScratchConfirmGB, _ = strconv.ParseFloat(settings.ScratchConfirmGB, 64)
	return p, nil
}
