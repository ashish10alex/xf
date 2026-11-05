package dbt

import (
	"context"
	"errors"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/project"
)

// RunArgs are the dbt CLI arguments for a run: `dbt build`, which builds
// models, seeds and snapshots and runs tests in DAG order, of the actions
// o names by RunName, with + for their parents (IncludeDeps) and children
// (IncludeDependents), with the backend's target. With Tags it selects
// tag:<tag> for each instead, which dbt unions.
func (b *Backend) RunArgs(o project.RunOptions) []string {
	if o.Args != nil {
		args := append([]string{"build"}, o.Args...)
		args = append(args, DeferArgs(o.DeferState)...)
		if b.Target != "" && !cmdrun.GivesFlag(o.Args, "--target", "-t") {
			args = append(args, "--target", b.Target)
		}
		return args
	}
	args := []string{"build", "--select"}
	names := o.Names
	if len(o.Tags) > 0 {
		names = nil
		for _, t := range o.Tags {
			names = append(names, "tag:"+t)
		}
	}
	for _, n := range names {
		if o.IncludeDeps {
			n = "+" + n
		}
		if o.IncludeDependents {
			n += "+"
		}
		args = append(args, n)
	}
	if o.FullRefresh {
		args = append(args, "--full-refresh")
	}
	args = append(args, DeferArgs(o.DeferState)...)
	if b.Target != "" {
		args = append(args, "--target", b.Target)
	}
	return args
}

// RunCommand is the command line StartRun runs in the project root, to
// show and copy.
func (b *Backend) RunCommand(root string, o project.RunOptions) string {
	return cmdrun.ShellJoin(append([]string{cmdrun.CommandName(b.Bin, "dbt", root)}, b.RunArgs(o)...))
}

// StartRun starts `dbt build` in root (see cmdrun.Start). Unlike a
// compile, it writes to the project's own target/ and logs/, as a run
// from the terminal would: run_results.json and --state comparisons want
// it there.
func (b *Backend) StartRun(ctx context.Context, root string, o project.RunOptions) (*project.Run, error) {
	if o.Args == nil && len(o.Tags) == 0 && (len(o.Names) == 0 || len(o.Names) != len(o.Actions)) {
		return nil, errors.New("dbt can't select these actions: they have no dbt name")
	}
	return cmdrun.Start(ctx, b.Bin, "dbt", root, b.RunArgs(o), o.Color)
}
