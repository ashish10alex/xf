// Package backend is what xf needs from the tool that compiles a project,
// such as Dataform or dbt. Each tool's package implements Backend; the
// engine, the TUI and the language server only see the project.Graph it
// builds.
package backend

import (
	"context"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/project"
)

// Backend compiles projects of one tool.
type Backend interface {
	// Name is the tool's name, as --backend takes it.
	Name() string
	// Compile compiles the project at root. Compilation errors in the
	// project are returned in Graph.Errors; an error means there is no
	// graph at all.
	Compile(ctx context.Context, root string) (*project.Graph, error)
	// Parse rebuilds a graph from its Graph.JSON, which is how a graph is
	// sent between processes and saved.
	Parse(data []byte) (*project.Graph, error)
	// Watch reports changes to the files under root that affect
	// compilation, debounced. The channel is closed when ctx is done.
	Watch(ctx context.Context, root string, debounce time.Duration) (<-chan struct{}, error)
}

// Runner is a Backend that can run a project's actions with its tool's
// CLI.
type Runner interface {
	// RunCommand is the command line StartRun runs with o in the project
	// at root: shown before a run starts, and copy-pasteable.
	RunCommand(root string, o project.RunOptions) string
	// StartRun starts a run of the project at root. Cancelling ctx cancels
	// it gracefully; Run.Kill forces it.
	StartRun(ctx context.Context, root string, o project.RunOptions) (*project.Run, error)
}

// Stager is a Backend that can show a project before its compile is done:
// parsed at once, then with the SQL of one action at a time compiled ahead
// of the rest (see engine.Config.Stage).
type Stager interface {
	// Stage parses the project at root into a graph whose SQL is still
	// templates (project.SQLRaw), but for the actions prev, the graph on
	// show, has compiled SQL for and whose templates are unchanged, which
	// keep it (project.SQLOutdated). prev may be nil. final says the graph
	// is all a compile would give, so none should follow.
	Stage(ctx context.Context, root string, prev *project.Graph) (g *project.Graph, final bool, err error)
	// Focus compiles the action id of base, a graph Stage or Focus
	// returned, with the actions shown with it (project.Graph.Siblings),
	// and returns base with their SQL compiled; nil when it has nothing to
	// compile. Errors in them are in Graph.Errors.
	Focus(ctx context.Context, root string, base *project.Graph, id project.ActionID) (*project.Graph, error)
	// Suggest picks the action of g to compile first when nobody has asked
	// for one: the one whose file changed last. "" picks none.
	Suggest(ctx context.Context, root string, g *project.Graph) project.ActionID
}

var (
	_ Stager  = (*dbt.Backend)(nil)
	_ Backend = dataform.Backend{}
	_ Backend = (*dbt.Backend)(nil)
	_ Runner  = dataform.Backend{}
	_ Runner  = (*dbt.Backend)(nil)
)

// EngineName names the engine that compiles a project with b: the tool's
// name, then its Variant when it has settings that change what a compile
// produces (the binary, dbt's target). Clients and xf serve meet on it, so a
// client never shares an engine that compiles differently.
func EngineName(b Backend) string {
	if v, ok := b.(interface{ Variant() string }); ok && v.Variant() != "" {
		return b.Name() + "-" + v.Variant()
	}
	return b.Name()
}
