package dataform

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/project"
)

// RunArgs are the dataform CLI arguments for a run: of o's actions, or of
// its tags when it has any.
func RunArgs(o project.RunOptions) []string {
	args := []string{"run"}
	if len(o.Tags) > 0 {
		args = append(append(args, "--tags"), o.Tags...)
	} else {
		args = append(args, "--actions")
		for _, id := range o.Actions {
			args = append(args, string(id))
		}
	}
	if o.IncludeDeps {
		args = append(args, "--include-deps")
	}
	if o.IncludeDependents {
		args = append(args, "--include-dependents")
	}
	if o.FullRefresh {
		args = append(args, "--full-refresh")
	}
	if o.JobPrefix != "" {
		args = append(args, "--job-prefix", o.JobPrefix)
	}
	return args
}

// StartRun starts `dataform run` in root (see cmdrun.Start).
func StartRun(ctx context.Context, bin, root string, opts project.RunOptions) (*project.Run, error) {
	return startRun(ctx, bin, root, RunArgs(opts), opts.Color)
}

func startRun(ctx context.Context, bin, root string, args []string, color bool) (*project.Run, error) {
	prog, prefix, err := command(bin)
	if err != nil {
		return nil, err
	}
	r, err := cmdrun.Start(ctx, prog, "dataform", root, append(prefix, args...), color)
	if err == nil {
		r.Cmdline = cmdrun.ShellJoin(append([]string{"dataform"}, args...))
	}
	return r, err
}

// RunCommand is the command line StartRun runs, to show and copy.
func (b Backend) RunCommand(root string, o project.RunOptions) string {
	name := "dataform" // a Backend that looks it up on PATH
	if b.Bin != "" {
		name = cmdrun.CommandName(b.Bin, "dataform", root)
	}
	return cmdrun.ShellJoin(append([]string{name}, b.runArgs(o)...))
}

// runArgs are RunArgs with b's Names, so a run builds the tables its
// compile names.
func (b Backend) runArgs(o project.RunOptions) []string {
	return append(RunArgs(o), b.Names.Args()...)
}

// StartRun runs `dataform run` in root with b's Names (see StartRun).
func (b Backend) StartRun(ctx context.Context, root string, o project.RunOptions) (*project.Run, error) {
	return startRun(ctx, b.Bin, root, b.runArgs(o), o.Color)
}

// PrefixesJobs asks the CLI whether its run takes --job-prefix, which older
// ones don't have.
func (b Backend) PrefixesJobs(ctx context.Context) bool {
	prog, prefix, err := command(b.Bin)
	if err != nil {
		return false
	}
	out, _ := cmdrun.Command(ctx, prog, append(prefix, "help", "run")...).CombinedOutput()
	return strings.Contains(string(out), "--job-prefix")
}

// JobTarget is where the CLI runs the BigQuery jobs of the project in
// root: the projectId and location of its .df-credentials.json, each ""
// when the file doesn't say.
func JobTarget(root string) (projectID, location string) {
	data, err := os.ReadFile(filepath.Join(root, ".df-credentials.json"))
	if err != nil {
		return "", ""
	}
	var c struct {
		ProjectID string `json:"projectId"`
		Location  string `json:"location"`
	}
	_ = json.Unmarshal(data, &c)
	return c.ProjectID, c.Location
}
