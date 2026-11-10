package dataformapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// API is the part of the Dataform API a run uses. *Client implements it;
// tests pass fakes.
type API interface {
	CreateCompilationResult(ctx context.Context, repo Repository, gitCommitish string, cfg CompilationConfig) (*CompilationResult, error)
	CreateWorkflowInvocation(ctx context.Context, repo Repository, compilationResult string, cfg InvocationConfig) (*Invocation, error)
	GetWorkflowInvocation(ctx context.Context, name string) (*Invocation, error)
	QueryWorkflowInvocationActions(ctx context.Context, name string) ([]Action, error)
	CancelWorkflowInvocation(ctx context.Context, name string) error
}

// Options say what an API run runs, and where.
type Options struct {
	// Repo is the Dataform repository. An empty Name is taken from the
	// origin remote of the checkout.
	Repo       Repository
	Invocation InvocationConfig
	// Compile is what the compile adds to the names the project builds,
	// as the local compile does.
	Compile CompilationConfig
	// Git is the checkout's state: its branch is compiled, and how it
	// differs from what was pushed is logged as warnings.
	Git GitState
	// PollInterval is how often the invocation is polled; 0 means 5s.
	PollInterval time.Duration
	// Jobs looks up what the actions' BigQuery jobs did, for the run's
	// report (see report.go); nil leaves the report without job stats.
	Jobs JobReader
	// Deferred are the upstream tables the run reads from prod through
	// Proxy Views, "dev → prod", kept in the run history.
	Deferred []string
	// JobLocation is where the project's BigQuery jobs run (the project's
	// defaultLocation), for jobs whose ID doesn't say.
	JobLocation string
}

// DefaultPollInterval is how often a run polls its invocation by default.
const DefaultPollInterval = 5 * time.Second

// maxPollFailures is how many polls in a row may fail before the run gives
// up watching.
const maxPollFailures = 5

// Start runs opts through api and returns at once. The run's lines say what
// it is doing: warnings about the checkout, the commit Dataform compiled,
// the console URL, and each action's state as it changes.
//
// Cancelling ctx asks Dataform to cancel the invocation (or abandons the
// run if there is none yet) and keeps watching until it is cancelled. Kill
// stops watching; the invocation carries on in GCP.
func Start(ctx context.Context, api API, opts Options) *project.Run {
	lines := make(chan string)
	done := make(chan project.RunResult, 1)
	watch, stopWatching := context.WithCancel(context.Background())
	r := &runner{api: api, opts: opts, lines: lines, watch: watch}
	go func() {
		defer close(lines)
		start := time.Now()
		res := r.run(ctx)
		res.Duration = time.Since(start)
		done <- res
	}()
	return &project.Run{
		Cmdline: "dataform API run",
		Lines:   lines,
		Done:    done,
		Kill:    stopWatching,
		URL: func() string {
			if u := r.url.Load(); u != nil {
				return *u
			}
			return ""
		},
		Report: r.report.Load,
	}
}

type runner struct {
	api   API
	opts  Options
	lines chan<- string
	watch context.Context // cancelled by Kill
	url   atomic.Pointer[string]
	// report is the run's report as of the last poll; each is published
	// whole and never changed.
	report atomic.Pointer[project.RunReport]
	// jobs are the job lookups made so far, by the action's job ID.
	jobs map[string]jobLookup
}

func (r *runner) emit(format string, args ...any) {
	select {
	case r.lines <- fmt.Sprintf(format, args...):
	case <-r.watch.Done():
	}
}

func (r *runner) run(ctx context.Context) project.RunResult {
	// Until there is an invocation, ctrl+c and Kill both just stop.
	setup, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.watch, cancel)
	defer stop()
	stopped := func(err error) (project.RunResult, bool) {
		switch {
		case r.watch.Err() != nil:
			return project.RunResult{Detached: true}, true
		case ctx.Err() != nil:
			return project.RunResult{Canceled: true}, true
		case err != nil:
			return project.RunResult{ExitCode: -1, Err: err}, true
		}
		return project.RunResult{}, false
	}

	g, repo := r.opts.Git, r.opts.Repo
	if b := g.Blocker(); b != "" {
		return project.RunResult{ExitCode: -1, Err: errors.New(b)}
	}
	if repo.Name == "" {
		repo.Name = RepoNameFromURL(g.Origin)
		if repo.Name == "" {
			return project.RunResult{ExitCode: -1, Err: errors.New("no origin remote to name the Dataform repository after: pass --dataform-repo")}
		}
	}
	switch {
	case repo.Project == "":
		return project.RunResult{ExitCode: -1, Err: errors.New("no GCP project: set defaultProject in workflow_settings.yaml")}
	case repo.Location == "":
		return project.RunResult{ExitCode: -1, Err: errors.New("no location: set defaultLocation in workflow_settings.yaml or pass --dataform-location")}
	}

	for _, w := range g.Warnings() {
		r.emit("⚠ %s", w)
	}
	r.emit("compiling origin/%s in %s (%s, %s)%s…", g.Branch, repo.Name, repo.Project, repo.Location, describeConfig(r.opts.Compile))
	cr, err := r.api.CreateCompilationResult(setup, repo, g.Branch, r.opts.Compile)
	if LooksLikeLocationError(err, repo.Location) {
		r.emit("⚠ Dataform repositories live in a region such as europe-west2, not a multi-region like EU.")
		r.emit("  Run `xf init` to look up %s's region and save it to .xf/config.json,", repo.Name)
		r.emit("  or set \"dataformLocation\" there yourself, or pass --dataform-location.")
	}
	if res, ok := stopped(err); ok {
		return res
	}
	r.emit("compiled %s", describeCompile(cr))
	if n := len(cr.Errors); n > 0 {
		for _, e := range cr.Errors {
			r.emit("✗ %s", describeCompileError(e))
		}
		return project.RunResult{ExitCode: 1, Err: fmt.Errorf("%d compilation error%s", n, plural(n))}
	}
	if res, ok := stopped(nil); ok {
		return res
	}
	// Not abandoned on ctrl+c: an invocation the request created anyway
	// would run unwatched. poll cancels it straight away instead.
	inv, err := r.api.CreateWorkflowInvocation(r.watch, repo, cr.Name, r.opts.Invocation)
	switch {
	case r.watch.Err() != nil:
		return project.RunResult{Detached: true}
	case err != nil:
		return project.RunResult{ExitCode: -1, Err: err}
	}
	u := repo.ConsoleURL(inv.Name)
	r.url.Store(&u)
	r.emit("↗ %s", u)
	return r.poll(ctx, inv.Name)
}

// poll watches the invocation until it ends, logging each action whose state
// changes. The first ctrl+c (ctx) asks Dataform to cancel it; Kill stops.
func (r *runner) poll(ctx context.Context, name string) project.RunResult {
	interval := r.opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	seen := map[Target]string{}
	cancelAsked := ctx.Done()
	failures := 0
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		inv, err := r.api.GetWorkflowInvocation(r.watch, name)
		var actions []Action
		if err == nil {
			actions, err = r.api.QueryWorkflowInvocationActions(r.watch, name)
		}
		switch {
		case r.watch.Err() != nil:
			return project.RunResult{Detached: true}
		case err != nil:
			failures++
			r.emit("⚠ could not check the invocation: %v", err)
			if failures >= maxPollFailures {
				return project.RunResult{ExitCode: -1, Err: fmt.Errorf("stopped watching after %d failed checks", failures)}
			}
		default:
			failures = 0
			// Before the lines: a reader looks at the report after each.
			// The actions are shown before their jobs are read, which can
			// take a while for a workflow of many.
			if r.jobsToLookUp(actions) > 0 {
				r.report.Store(r.buildReport(inv, actions))
			}
			r.lookUpJobs(actions)
			r.report.Store(r.buildReport(inv, actions))
			for _, a := range actions {
				if seen[a.Target] != a.State {
					seen[a.Target] = a.State
					if line := describeAction(a); line != "" {
						r.emit("%s", line)
					}
				}
			}
			if inv.Done() {
				return result(inv.State)
			}
		}
		select {
		case <-r.watch.Done():
			return project.RunResult{Detached: true}
		case <-cancelAsked:
			cancelAsked = nil // ask once
			if err := r.api.CancelWorkflowInvocation(r.watch, name); err != nil {
				r.emit("⚠ could not cancel the invocation: %v", err)
			} else {
				r.emit("cancel requested; waiting for Dataform to stop its BigQuery jobs")
			}
		case <-tick.C:
		}
	}
}

func result(state string) project.RunResult {
	res := project.RunResult{State: state}
	switch state {
	case StateFailed:
		res.ExitCode = 1
	case StateCancelled:
		res.Canceled = true
	}
	return res
}

// describeConfig is what cfg adds to the names, for the compiling line:
// " with schema suffix ashish"; "" when it adds nothing.
func describeConfig(cfg CompilationConfig) string {
	var parts []string
	if cfg.TablePrefix != "" {
		parts = append(parts, "table prefix "+cfg.TablePrefix)
	}
	if cfg.SchemaSuffix != "" {
		parts = append(parts, "schema suffix "+cfg.SchemaSuffix)
	}
	if cfg.DatabaseSuffix != "" {
		parts = append(parts, "database suffix "+cfg.DatabaseSuffix)
	}
	if len(parts) == 0 {
		return ""
	}
	return " with " + strings.Join(parts, ", ")
}

func describeCompile(cr *CompilationResult) string {
	s := "commit " + short(cr.ResolvedGitCommitSHA)
	if cr.ResolvedGitCommitSHA == "" {
		s = "the branch"
	}
	if cr.DataformCoreVersion != "" {
		s += " · @dataform/core " + cr.DataformCoreVersion
	}
	return s
}

func describeCompileError(e CompilationError) string {
	where := e.Path
	if e.ActionTarget != nil && e.ActionTarget.Name != "" {
		t := targetName(*e.ActionTarget)
		if where != "" {
			where += " (" + t + ")"
		} else {
			where = t
		}
	}
	if where == "" {
		return e.Message
	}
	return where + ": " + e.Message
}

// IsActionLine says line is one describeAction makes: an action's state,
// which a run's report also has.
func IsActionLine(line string) bool {
	for _, p := range []string{"▶ running ", "✓ ", "✗ failed ", "✗ cancelled ", "– skipped ", "– disabled "} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// describeAction is the log line for an action's state; "" for states not
// worth a line (PENDING).
func describeAction(a Action) string {
	name := targetName(a.Target)
	took := ""
	if d, ok := duration(a); ok {
		took = " · " + d
	}
	switch a.State {
	case "RUNNING":
		return "▶ running " + name
	case "SUCCEEDED":
		return "✓ " + name + took
	case "FAILED":
		line := "✗ failed " + name + took
		if a.FailureReason != "" {
			line += ": " + strings.TrimSpace(a.FailureReason)
		}
		return line
	case "CANCELLED":
		return "✗ cancelled " + name
	case "SKIPPED", "DISABLED":
		return "– " + strings.ToLower(a.State) + " " + name
	}
	return ""
}

func targetName(t Target) string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

func duration(a Action) (string, bool) {
	start, err1 := time.Parse(time.RFC3339Nano, a.Timing.StartTime)
	end, err2 := time.Parse(time.RFC3339Nano, a.Timing.EndTime)
	if err1 != nil || err2 != nil || end.Before(start) {
		return "", false
	}
	return fmt.Sprintf("%.1fs", end.Sub(start).Seconds()), true
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
