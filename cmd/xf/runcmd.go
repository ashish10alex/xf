package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/remote"
)

// xf run (ADR 0038): `dataform run` or `dbt build`, with the tool's own
// flags, and defer to prod with --defer.

// Exit codes of xf run's own; a run exits with its tool's.
const (
	runOK            = 0
	runDryRunFailed  = 1 // a dry run of the plan failed, as the tool's own --dry-run exits
	runNeedsDecision = 3 // stale reads, leftover Proxy Views or tables that can't be deferred
	runCouldntCheck  = 4 // a bad flag, a compile error, a BigQuery error
	runInterrupted   = 130
)

const runUsage = `usage: xf run [project-dir] [flags] [the tool's run flags]

Runs dataform run, or dbt build in a dbt project, with the tool's own flags
as given (dataform help run, dbt build --help), and keeps the run in xf's run
history. xf adds what its own runs add: the name settings (tablePrefix,
schemaSuffix, databaseSuffix) or the dbt target, unless the flags give them.
The tool's output goes to stdout; xf's own lines go to stderr, after "xf:".
Settings come from xf's config files (xf config).

xf's flags:
  --defer                 read upstream tables that the run doesn't build and
                          that aren't built in dev from prod: in Dataform
                          through proxy views that xf makes in dev, in dbt
                          with dbt's --defer and a manifest of prod. Needs
                          prodCompilerOptions or dbtProdTarget. Flags that
                          change the compile (--vars, --schema-suffix,
                          --target …) can't be given with it.
  --defer-build-changed   when a table read from prod changes on this branch,
                          build it in dev first, in this run
  --defer-allow-stale     run all the same when a table read from prod changes
                          on this branch
  --allow-proxy-views     without --defer, run all the same when proxy views
                          left by an earlier deferred run would read prod
  --defer-remove-views    remove the project's proxy views (with a selection,
                          those upstream of it) and exit; with --dry-run, list
                          them
  --dry-run               run nothing and write nothing: dry run the SQL of
                          what the run builds (with --defer, reading prod),
                          and print the plan; --json prints it as JSON
  --dir dir               the project's root (default: the working directory)
  --backend tool          dataform or dbt, for a dir that is both

Exits with the tool's exit code, or 0, or 1 when a dry run failed; 3 when the
run needs a decision first (stderr names the flag that makes it); 4 when xf
couldn't check the run.
`

func runRun(args []string) int {
	ra, err := parseRunArgs(args)
	if ra.help {
		fmt.Print(runUsage)
		return runOK
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "xf run: %v (see xf run --help)\n", err)
		return runCouldntCheck
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := xfRun(ctx, ra)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "xf run: interrupted")
			return runInterrupted
		}
		fmt.Fprintln(os.Stderr, "xf run:", err)
		return runCouldntCheck
	}
	return code
}

// runCtx is what a run is checked and started with.
type runCtx struct {
	ra       runArgs
	ta       toolArgs
	root     string
	tool     string
	settings config.Loaded
	b        backend.Backend
	// engine is the shared engine's client (client), or a fake in tests.
	engine runEngine
	client *remote.Client
	g      *project.Graph
	writer func(context.Context) (deferral.Writer, error)
	// out is where a dry run's plan goes: stdout.
	out io.Writer
}

// runEngine is what xf run asks of the engine; *remote.Client is one.
type runEngine interface {
	Deferral(ctx context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error)
	DryRun(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)
	ChangedActions(ctx context.Context, req changes.Request) (*changes.Result, error)
}

func xfRun(ctx context.Context, ra runArgs) (int, error) {
	root, name, err := projectFlags{dir: &ra.dir, backend: &ra.backend}.find()
	if err != nil {
		return 0, err
	}
	if name == backend.DBT {
		// dbt build has no --json: it is xf's, for the plan.
		ra.tool = slices.DeleteFunc(slices.Clone(ra.tool), func(a string) bool { return a == "--json" || strings.HasPrefix(a, "--json=") })
	}
	ta, err := readToolArgs(name, ra.tool)
	if err != nil {
		return 0, err
	}
	if ra.deferOn && len(ta.compile) > 0 {
		return 0, fmt.Errorf("--defer can't be given with %s: it changes the compile, so the run would write other tables than the ones xf checked; set it in xf's config instead (xf config), or run without --defer", strings.Join(ta.compile, ", "))
	}
	if ra.removeViews && name == backend.DBT {
		return 0, errors.New("--defer-remove-views: dbt defers itself and makes no proxy views")
	}
	// A flag set never parsed: settings come from the config files alone.
	settings, err := addSettingFlags(flag.NewFlagSet("xf run", flag.ContinueOnError)).load(root)
	if err != nil {
		return 0, err
	}
	for _, w := range settings.Warnings {
		fmt.Fprintln(os.Stderr, "xf:", w)
	}
	if ra.deferOn {
		switch {
		case name == backend.Dataform && !settings.IsSet(config.KeyProdCompilerOptions):
			return 0, fmt.Errorf("--defer needs %s, the compile flags that name prod's tables (e.g. --default-database=acme-prod): xf config --set %s=…, or \"\" when the default targets are prod", config.KeyProdCompilerOptions, config.KeyProdCompilerOptions)
		case name == backend.DBT && settings.DbtProdTarget == "":
			return 0, fmt.Errorf("--defer needs %s, the profile target that builds prod (e.g. prod): xf config --set %s=prod", config.KeyDbtProdTarget, config.KeyDbtProdTarget)
		}
	}
	b, err := openBackend(name, root, settings)
	if err != nil {
		return 0, err
	}
	client, err := connect(ctx, root, b, nil)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	g, err := freshCompile(ctx, client)
	if err != nil {
		return 0, err
	}
	rc := &runCtx{ra: ra, ta: ta, root: root, tool: name, settings: settings, b: b, engine: client, client: client, g: g, out: os.Stdout,
		writer: func(ctx context.Context) (deferral.Writer, error) {
			c, err := bigquery.NewClient(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		}}
	if ra.removeViews {
		return rc.removeViews(ctx)
	}
	plan, err := rc.check(ctx)
	if err != nil {
		return 0, err
	}
	if ra.dryRun {
		return rc.dryRun(ctx, plan)
	}
	if plan.refusal != nil {
		plan.refusal.print()
		return runNeedsDecision, nil
	}
	return rc.run(ctx, plan)
}

// freshCompile has the engine compile the project as it is now, so what
// xf checks is what the tool will run, and returns that compile.
func freshCompile(ctx context.Context, client *remote.Client) (*project.Graph, error) {
	states, unsub := client.Subscribe()
	defer unsub()
	before := client.State().Compiles
	client.Recompile("xf run")
	pr := newProgressPrinter(os.Stderr)
	defer pr.done()
	pr.say("Compiling")
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case st := <-states:
			if st.Compiles <= before || st.Compiling || st.Partial {
				continue
			}
			switch {
			case st.Err != nil:
				return nil, fmt.Errorf("compiling: %w", st.Err)
			case len(st.Errors) > 0:
				e := st.Errors[0]
				where := e.FileName
				if where != "" {
					where += ": "
				}
				return nil, fmt.Errorf("the project has compile errors (%d), so xf can't check the run: %s%s", len(st.Errors), where, e.Message)
			case st.Graph == nil:
				return nil, errors.New("compiling: no compile")
			}
			return st.Graph, nil
		}
	}
}

// runPlan is a checked run: what it builds and reads.
type runPlan struct {
	// runSet are the actions it builds, sorted.
	runSet []project.ActionID
	// args are the tool's arguments, with what the checks added.
	args []string
	// graph is the compile whose SQL is dry run: dbt's with --defer when
	// it defers.
	graph *project.Graph
	// deferral is what it reads from prod, nil when nothing.
	deferral *deferral.Deferral
	// entries are the tables read from prod, through Proxy Views in
	// Dataform.
	entries []deferral.Entry
	// added are the actions the checks added to the run.
	added []project.ActionID
	// dbtState is a deferred dbt run's --defer-state.
	dbtState string
	// proxies are leftover Proxy Views a run without --defer reads.
	proxies []project.Target
	refusal *runRefusal
	// note says why something wasn't checked.
	note string
}

// check works out what the run builds and reads, and what defer to prod
// makes of it, adding to the run what the flags ask.
func (rc *runCtx) check(ctx context.Context) (*runPlan, error) {
	plan := &runPlan{args: slices.Clone(rc.ta.args), graph: rc.g}
	answers := map[string]bool{
		frontend.DeferAnswerChanged:  rc.ra.buildChanged,
		frontend.DeferAnswerStale:    rc.ra.allowStale,
		frontend.DeferAnswerLeftover: rc.ra.allowProxies,
	}
	on := rc.ra.deferOn
	for round := 0; ; round++ {
		runSet, err := rc.runSet(ctx, plan.args)
		if err != nil {
			return nil, err
		}
		plan.runSet = runSet
		if len(runSet) == 0 {
			return plan, nil
		}
		var run frontend.DeferRun
		if rc.tool == backend.DBT {
			if !on {
				return plan, nil
			}
			if err := rc.dbtDeferral(ctx, plan); err != nil {
				return nil, err
			}
			run = frontend.DeferRun{On: true, Deferral: plan.deferral, Answers: answers}
		} else {
			res, err := rc.engine.Deferral(ctx, engine.DeferralRequest{
				Actions: runSet, ProdCompilerOptions: &rc.settings.ProdCompilerOptions,
				DefaultBranch: rc.defaultBranch(), Proxies: true, Fresh: true, On: &on,
			})
			if err != nil {
				if !on {
					plan.note = "couldn't look for proxy views left by an earlier deferred run: " + err.Error()
					fmt.Fprintln(os.Stderr, "xf:", plan.note)
					return plan, nil
				}
				return nil, fmt.Errorf("defer to prod: %w", err)
			}
			plan.deferral, plan.proxies = res.Deferral, res.Proxies
			run = frontend.DeferRun{On: on, Deferral: res.Deferral, Proxies: res.Proxies, Answers: answers,
				MissingDatasets: func(entries []deferral.Entry) map[string]bool {
					w, err := rc.writer(ctx)
					if err != nil {
						return nil
					}
					return deferral.MissingDevDatasets(ctx, w, entries)
				}}
		}
		c := frontend.CheckDeferredRun(run)
		if len(c.Add) > 0 && round < 3 {
			plan.added = append(plan.added, c.Add...)
			plan.args = rc.addToRun(plan.args, c.Add)
			continue
		}
		plan.entries = c.Entries
		plan.refusal = refusalOf(c, rc.tool)
		return plan, nil
	}
}

func (rc *runCtx) defaultBranch() string {
	return cmp.Or(rc.settings.DefaultBranch, config.DefaultBranch)
}

// runSet are the actions a run with args builds, as its tool selects them.
func (rc *runCtx) runSet(ctx context.Context, args []string) ([]project.ActionID, error) {
	ta, err := readToolArgs(rc.tool, args)
	if err != nil {
		return nil, err
	}
	if rc.tool == backend.DBT {
		b, ok := rc.b.(*dbt.Backend)
		if !ok {
			return nil, errors.New("not a dbt project")
		}
		ids, err := b.Selected(ctx, rc.root, ta.selection)
		if err != nil {
			return nil, fmt.Errorf("dbt ls with the selection: %w", err)
		}
		slices.Sort(ids)
		return ids, nil
	}
	if !ta.selects {
		var all []project.ActionID
		for id, a := range rc.g.Actions {
			if a.Runnable() {
				all = append(all, id)
			}
		}
		slices.Sort(all)
		return all, nil
	}
	ids, err := dataformMatch(rc.g, ta.actions)
	if err != nil {
		return nil, err
	}
	return deferral.RunSet(rc.g, project.RunOptions{Actions: ids, Tags: ta.tags, IncludeDeps: ta.deps, IncludeDependents: ta.dependents}), nil
}

// addToRun adds actions to a run's arguments: --actions in Dataform, which
// adds to the tags and actions given, --select in dbt, which adds to the
// selection.
func (rc *runCtx) addToRun(args []string, ids []project.ActionID) []string {
	out := slices.Clone(args)
	if rc.tool == backend.DBT {
		out = append(out, "--select")
		for _, id := range ids {
			if a := rc.g.Actions[id]; a != nil && a.RunName != "" {
				out = append(out, a.RunName)
			}
		}
		return out
	}
	out = append(out, "--actions")
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// dataformMatch finds the actions --actions names, as dataform run does:
// a pattern with a dot is a whole "database.schema.name", one without the
// name alone, which must be one action's unless it has a '*', which
// matches any characters.
func dataformMatch(g *project.Graph, patterns []string) ([]project.ActionID, error) {
	var out []project.ActionID
	for _, p := range patterns {
		var found []project.ActionID
		for id, a := range g.Actions {
			if !a.Runnable() {
				continue
			}
			subject := string(id)
			if !strings.Contains(p, ".") {
				subject = a.Target.Name
			}
			if ok, _ := path.Match(p, subject); ok {
				found = append(found, id)
			}
		}
		slices.Sort(found)
		if len(found) == 0 {
			// dataform run selects nothing for it, silently: say so.
			hint := ""
			for id, a := range g.Actions {
				if a.Runnable() && strings.HasSuffix(a.Target.Name, "_"+p) {
					hint = fmt.Sprintf(" (names are as compiled, with any table prefix: %s?)", id)
					break
				}
			}
			fmt.Fprintf(os.Stderr, "xf: --actions %s matches no action%s\n", p, hint)
		}
		if len(found) > 1 && !strings.Contains(p, "*") {
			return nil, fmt.Errorf("--actions %s names %d actions: %s; give the whole database.schema.name", p, len(found), strings.Join(idStrings(found), ", "))
		}
		out = append(out, found...)
	}
	return out, nil
}

func idStrings(ids []project.ActionID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// dbtDeferral works out what a deferred dbt run reads from prod. dbt
// decides, from prod's manifest; while the project's switch is off the
// engine's compile doesn't defer, so the project is compiled once with
// --defer here, where xf run keeps its own dbt files.
func (rc *runCtx) dbtDeferral(ctx context.Context, plan *runPlan) error {
	on := true
	res, err := rc.engine.Deferral(ctx, engine.DeferralRequest{Actions: plan.runSet, DefaultBranch: rc.defaultBranch(), Fresh: true, On: &on})
	if err != nil {
		return fmt.Errorf("defer to prod: %w", err)
	}
	if res.DbtState == "" {
		return errors.New("defer to prod: no manifest of prod")
	}
	plan.dbtState = res.DbtState
	if res.Deferral != nil && res.StaleKnown {
		plan.deferral = res.Deferral
		return nil
	}
	b, ok := rc.b.(*dbt.Backend)
	if !ok {
		return errors.New("not a dbt project")
	}
	if plan.graph == rc.g {
		pr := newProgressPrinter(os.Stderr)
		pr.say("Compiling with dbt --defer")
		db := dbt.New(b.Bin, b.Target, func(root string) string { return filepath.Join(b.ArtifactDir(root), "xf-run") }, b.CompileWithHooks)
		db.SetDeferState(res.DbtState)
		dg, err := db.Compile(ctx, rc.root)
		pr.done()
		if err != nil {
			return fmt.Errorf("compiling with dbt --defer: %w", err)
		}
		if dg.HasErrors() {
			return fmt.Errorf("compiling with dbt --defer: %s", dg.Errors[0].Message)
		}
		plan.graph = dg
	}
	data, err := os.ReadFile(filepath.Join(res.DbtState, "manifest.json"))
	if err != nil {
		return err
	}
	prod, err := dbt.ParseManifest(data)
	if err != nil {
		return err
	}
	changed, err := rc.engine.ChangedActions(ctx, changes.Request{DefaultBranch: rc.defaultBranch(), Compile: true})
	if err != nil {
		return fmt.Errorf("working out what changes on this branch: %w", err)
	}
	isChanged := map[project.ActionID]bool{}
	for _, c := range changed.Changed {
		isChanged[c.ID] = true
	}
	d := &deferral.Deferral{}
	for _, r := range dbt.Deferred(plan.graph, prod, plan.runSet) {
		if r.Prod == nil {
			d.BuiltInDev = append(d.BuiltInDev, deferral.BuiltInDev{Dev: r.Dev})
			continue
		}
		d.Entries = append(d.Entries, deferral.Entry{Dev: r.Dev, Prod: r.Prod, Status: deferral.Deferred, Stale: isChanged[r.Dev.ID()]})
	}
	plan.deferral = d
	return nil
}

// runRefusal is why a run needs a decision first, and the flags that make
// it.
type runRefusal struct {
	Reason string   `json:"reason"`
	Title  string   `json:"title"`
	Tables []string `json:"tables"`
	Flags  []string `json:"flags"`
}

func refusalOf(c frontend.DeferCheck, tool string) *runRefusal {
	n := len(c.Detail)
	deps := "--include-deps builds them in dev"
	if tool == backend.DBT {
		deps = "selecting them too (--select +model) builds them in dev"
	}
	switch c.Stop {
	case frontend.DeferStopLeftover:
		return &runRefusal{Reason: "proxyViews", Tables: c.Detail,
			Title: fmt.Sprintf("defer to prod is off, but %s left by an earlier deferred run would make this run read prod:", plural(n, "upstream proxy view")),
			Flags: []string{deps, "--allow-proxy-views runs all the same", "--defer-remove-views removes them"}}
	case frontend.DeferStopBlocked:
		return &runRefusal{Reason: "cannotDefer", Tables: c.Detail,
			Title: fmt.Sprintf("can't read %s from prod, so this run would fail:", plural(n, "upstream table")),
			Flags: []string{deps + " (xf never makes a dev dataset)"}}
	case frontend.DeferStopStale:
		return &runRefusal{Reason: "stale", Tables: c.Detail,
			Title: fmt.Sprintf("%s read from prod %s on this branch, so the run would use prod's version:", plural(n, "upstream table"), map[bool]string{true: "changes", false: "change"}[n == 1]),
			Flags: []string{"--defer-build-changed builds them in dev first, in this run", "--defer-allow-stale runs all the same"}}
	}
	return nil
}

func (r *runRefusal) print() {
	fmt.Fprintln(os.Stderr, "xf: "+r.Title)
	for _, t := range r.Tables {
		fmt.Fprintln(os.Stderr, "xf:   "+t)
	}
	fmt.Fprintln(os.Stderr, "xf: "+strings.Join(r.Flags, "; ")+".")
}

// run makes the Proxy Views and runs the tool, keeping the run in the
// history.
func (rc *runCtx) run(ctx context.Context, plan *runPlan) (int, error) {
	if len(plan.runSet) == 0 && rc.ta.selects {
		fmt.Fprintln(os.Stderr, "xf: the selection names no action to run")
	}
	for _, id := range plan.added {
		fmt.Fprintf(os.Stderr, "xf: added to the run: %s\n", id)
	}
	if rc.ra.deferOn {
		if line := frontend.DeferRunLine(plan.deferral); line != "" {
			fmt.Fprintln(os.Stderr, "xf: defer to prod: "+line)
			for _, p := range frontend.DeferredPairs(plan.deferral) {
				fmt.Fprintln(os.Stderr, "xf:   "+p)
			}
		} else {
			fmt.Fprintln(os.Stderr, "xf: defer to prod: every upstream table is built in dev or built by this run")
		}
	}
	if len(plan.entries) > 0 && rc.tool == backend.Dataform {
		w, err := rc.writer(ctx)
		if err != nil {
			return 0, err
		}
		r, err := deferral.EnsureProxies(ctx, w, plan.entries)
		if err != nil {
			return 0, fmt.Errorf("defer to prod: couldn't make the proxy views, so the run wasn't started: %w", err)
		}
		if s := r.String(); s != "" {
			fmt.Fprintln(os.Stderr, "xf: defer to prod: "+s)
		}
	}
	color, _ := useColor("auto", os.Stdout, os.Getenv)
	opts := project.RunOptions{Actions: plan.runSet, Args: plan.args, DeferState: plan.dbtState, Color: color}
	if rc.ra.deferOn {
		opts.Deferred = frontend.DeferredPairs(plan.deferral)
	}
	runner, ok := rc.b.(backend.Runner)
	if !ok {
		return 0, errors.New("this project's tool can't run")
	}
	deps := frontend.Deps{StartRun: func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		return runner.StartRun(ctx, rc.root, o)
	}}
	if b, ok := rc.b.(dataform.Backend); ok {
		addRunJobs(&deps, rc.root, b, rc.client)
	}
	run, err := deps.StartRun(ctx, opts)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(os.Stderr, "xf: $ "+run.Cmdline)
	for line := range run.Lines {
		fmt.Println(line)
	}
	res := <-run.Done
	switch {
	case res.Canceled:
		return runInterrupted, nil
	case res.Err != nil:
		return 0, res.Err
	}
	return res.ExitCode, nil
}

// removeViews removes the project's Proxy Views, or those upstream of the
// selection, or with --dry-run lists them.
func (rc *runCtx) removeViews(ctx context.Context) (int, error) {
	w, err := rc.writer(ctx)
	if err != nil {
		return 0, err
	}
	var views []project.Target
	if !rc.ta.selects {
		if views, err = deferral.RemoveProxies(ctx, w, rc.g, !rc.ra.dryRun); err != nil {
			return 0, err
		}
	} else {
		runSet, err := rc.runSet(ctx, rc.ta.args)
		if err != nil {
			return 0, err
		}
		off := false
		res, err := rc.engine.Deferral(ctx, engine.DeferralRequest{Actions: runSet, Proxies: true, Fresh: true, On: &off})
		if err != nil {
			return 0, err
		}
		for _, t := range res.Proxies {
			if !rc.ra.dryRun {
				if err := w.DeleteTable(ctx, t.Database, t.Schema, t.Name); err != nil {
					return 0, fmt.Errorf("removing %s: %w", t.ID(), err)
				}
			}
			views = append(views, t)
		}
	}
	slices.SortFunc(views, func(a, b project.Target) int { return strings.Compare(string(a.ID()), string(b.ID())) })
	verb := "removed"
	if rc.ra.dryRun {
		verb = "would remove"
	}
	if len(views) == 0 {
		fmt.Fprintln(os.Stderr, "xf: no proxy views to remove")
		return runOK, nil
	}
	fmt.Fprintf(os.Stderr, "xf: %s %s:\n", verb, plural(len(views), "proxy view"))
	for _, t := range views {
		fmt.Println(t.ID())
	}
	return runOK, nil
}
