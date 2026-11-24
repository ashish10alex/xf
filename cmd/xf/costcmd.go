package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Exit codes of xf cost.
const (
	costOK       = 0
	costFailOver = 1 // the changed actions scan more than --fail-over
	costFailed   = 2 // the check could not be made
)

// runCost prints what the project's Changed Actions would scan in BigQuery
// and returns the exit code.
func runCost(args []string) int {
	fs := flag.NewFlagSet("xf cost", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtProdTarget, config.KeyDbtCompileWithHooks,
		config.KeyDatabricksWarehouse, config.KeyDatabricksProfile, config.KeySnowflakeWarehouse, config.KeySnowflakeConnection,
		config.KeyDefaultBranch, config.KeyProdCompilerOptions, config.KeyUSDPerTiB)
	pushed := fs.Bool("pushed", false, "estimate the branch pushed to origin (what an API run compiles) instead of the working tree; Dataform only")
	failOver := fs.String("fail-over", "", "exit 1 when the changed actions scan more than this `size` between them, e.g. 100GB (units are binary: 1 GB is 2^30 bytes)")
	failOverDelta := fs.String("fail-over-delta", "", "exit 1 when the branch adds more than this `size` of scan per run compared to the merge-base, e.g. 50GB (units are binary)")
	failOnUnfiltered := fs.Bool("fail-on-unfiltered", false, "exit 1 when any estimated query reads a partitioned table without naming its partition column")
	failOnUnestimated := fs.Bool("fail-on-unestimated", false, "exit 1 when any changed action could not be estimated")
	devStandIns := fs.Bool("dev-stand-ins", false, "size queries that read tables missing in prod using dev's copies when built, while still reporting missing prod prerequisites and prod-reader risks")
	output := fs.String("output", "", "also write the report to `path`: as Markdown, for a pull request comment, or as JSON with --json")
	fs.StringVar(output, "o", "", "")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	color := fs.String("color", "auto", "colour the text: `when` is auto (on a terminal, unless $NO_COLOR is set), always or never")
	web := fs.Bool("web", false, "open xf web on its Checks page instead, where the check runs as C runs it there")
	attention := fs.Bool("attention", false, "list only the actions that need a look, in the text and the --output Markdown: not estimated, missing a partition filter, or scanning 1 GiB or more than at the merge-base; the total still counts every action")
	commandUsage(fs, "xf cost [flags]", `Prints what the actions this branch changes would scan in BigQuery, and what
that costs on demand: every changed action with a query is dry run, as it is
on the branch and as it was at the merge-base with the default branch. An
incremental table counts its incremental run, with its full rebuild beside
it. A read of a partitioned table that never names its partition column is
marked. An action BigQuery gives no estimate for is listed and left out of
the total. Dry runs are free and run nothing. A dbt project on Databricks,
which bills warehouse time rather than bytes, has nothing to estimate: it
gets each changed model's latest run with the prod target instead, from
Query History, found by dbt's query comment, and cost gates don't apply.
It always checks afresh, and
shares the work with xf through xf serve. Progress goes to stderr. With --web
it opens xf web on the query cost tab of its Checks page instead, where y
copies the Markdown, and runs until ctrl+c as xf web does. Exits 0, 1 when a
cost gate trips (--fail-over, --fail-over-delta, --fail-on-unfiltered or
--fail-on-unestimated), or 2 when the check could not be made.`)
	help, err := parseArgs(fs, args)
	if help {
		return costOK
	}
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %v (see xf cost --help)", fs.Args())
	}
	opts := cost.Options{Attention: *attention, FailOnUnfiltered: *failOnUnfiltered, FailOnUnestimated: *failOnUnestimated}
	if err == nil {
		err = sf.checkFlag(config.KeyUSDPerTiB)
	}
	if err == nil && *web && (*pushed || *failOver != "" || *failOverDelta != "" || *failOnUnfiltered || *failOnUnestimated || *devStandIns || *output != "" || *asJSON || *attention) {
		err = errors.New("--web can't be used with --pushed, --fail-over, --fail-over-delta, --fail-on-unfiltered, --fail-on-unestimated, --dev-stand-ins, --output, --json or --attention")
	}
	if err == nil && *attention && *asJSON {
		err = errors.New("--attention can't be used with --json, which has every action")
	}
	if err == nil {
		opts.Color, err = useColor(*color, os.Stdout, os.Getenv)
	}
	if err == nil && *failOver != "" {
		if opts.Threshold, err = cost.ParseSize(*failOver); err == nil && opts.Threshold <= 0 {
			err = errors.New("--fail-over must be more than 0")
		}
		if err != nil {
			err = fmt.Errorf("--fail-over: %w", err)
		}
	}
	if err == nil && *failOverDelta != "" {
		if opts.DeltaThreshold, err = cost.ParseSize(*failOverDelta); err == nil && opts.DeltaThreshold <= 0 {
			err = errors.New("must be more than 0")
		}
		if err != nil {
			err = fmt.Errorf("--fail-over-delta: %w", err)
		}
	}
	if err == nil && *web {
		if err = startWeb(pf, sf, webStart{cmdName: "xf cost", check: "cost"}); err == nil {
			return costOK
		}
	}
	if err == nil {
		var code int
		if code, err = checkCost(pf, sf, *pushed, *devStandIns, opts, *output, *asJSON); err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "xf cost:", err)
	return costFailed
}

func checkCost(pf projectFlags, sf *settingFlags, pushed, standIns bool, opts cost.Options, output string, asJSON bool) (int, error) {
	root, name, err := pf.find()
	if err != nil {
		return 0, err
	}
	settings, err := sf.load(root)
	if err != nil {
		return 0, err
	}
	if settings.USDPerTiB != "" {
		opts.USDPerTiB, _ = strconv.ParseFloat(settings.USDPerTiB, 64)
	}
	for _, w := range settings.Warnings {
		fmt.Fprintln(os.Stderr, "xf cost:", w)
	}
	b, err := openBackend(name, root, settings)
	if err != nil {
		return 0, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := connect(ctx, root, b, nil)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	req := engine.CostRequest{DefaultBranch: cmp.Or(settings.DefaultBranch, config.DefaultBranch), Head: changes.HeadWorktree, Refresh: true,
		ProdCompilerOptions: settings.ProdCompilerOptions, Unset: unsetProdOptions(settings), StandIns: standIns}
	if name == backend.DBT {
		req.DbtProdTarget, req.Unset = settings.DbtProdTarget, unsetDbtProdTarget(settings, os.Getenv)
	}
	if pushed {
		req.Head = changes.HeadPushed
	}
	pr := newProgressPrinter(os.Stderr)
	pr.say("Working out the changed actions against " + req.DefaultBranch)
	report, err := client.QueryCost(ctx, req, func(p cost.Progress) {
		if p.Total == 0 {
			pr.say(p.Phase)
			return
		}
		pr.say(fmt.Sprintf("%s %d/%d", p.Phase, p.Done, p.Total))
	})
	pr.done()
	if err != nil {
		return 0, explainCompile(ctx, client, err)
	}

	if asJSON {
		data, err := cost.JSON(report, opts)
		if err != nil {
			return 0, err
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return 0, err
		}
	} else if _, err := fmt.Fprint(os.Stdout, cost.Text(report, opts)); err != nil {
		return 0, err
	}
	if output != "" {
		if err := writeCostReport(output, report, opts, asJSON); err != nil {
			return 0, err
		}
	}

	if !opts.FailOnUnestimated {
		if n := report.Unestimated(); n == 1 {
			fmt.Fprintln(os.Stderr, "xf cost: 1 action could not be estimated; it is not in the total, and --fail-over doesn't count it")
		} else if n > 1 {
			fmt.Fprintf(os.Stderr, "xf cost: %d actions could not be estimated; they are not in the total, and --fail-over doesn't count them\n", n)
		}
	}
	if standIns {
		for _, risk := range report.ProdRisks {
			fmt.Fprintln(os.Stderr, "xf cost: warning:", cost.ProdRiskText(risk))
		}
		for _, p := range report.Prerequisites {
			fmt.Fprintln(os.Stderr, "xf cost: warning:", cost.PrerequisiteText(p))
		}
	}
	if report.BaseError != "" {
		suffix := ""
		if opts.DeltaThreshold > 0 {
			suffix = " (--fail-over-delta counted the total as growth)"
		}
		fmt.Fprintf(os.Stderr, "xf cost: no change against the merge-base is shown, as it could not be compiled: %s%s\n", report.BaseError, suffix)
	}
	if report.GateTripped(opts) {
		return costFailOver, nil
	}
	return costOK, nil
}

// writeCostReport writes the report to path, making its folders: as JSON
// with asJSON, else as Markdown for a pull request comment.
func writeCostReport(path string, report *cost.Report, opts cost.Options, asJSON bool) error {
	data := []byte(cost.Markdown(report, opts))
	if asJSON {
		var err error
		if data, err = cost.JSON(report, opts); err != nil {
			return err
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// useColor says whether to colour text written to out, as --color when
// asks: always, never, or auto, which colours a terminal unless $NO_COLOR is
// set or $TERM is dumb.
func useColor(when string, out *os.File, getenv func(string) string) (bool, error) {
	switch when {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
			return false, nil
		}
		st, err := out.Stat()
		return err == nil && st.Mode()&os.ModeCharDevice != 0, nil
	}
	return false, fmt.Errorf("--color must be auto, always or never, not %q", when)
}

// costCacheDir is where the compiles a cost check makes of commits are
// cached; "" keeps them in memory.
func costCacheDir(logger *log.Logger) string {
	dir := changesCacheDir(logger)
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), "costed")
}

// partitionLookup reads how a table is partitioned with the client bq
// makes.
func partitionLookup(bq func(context.Context) (warehouse.Warehouse, error)) func(ctx context.Context, t warehouse.TableRef) (*warehouse.Partition, error) {
	return func(ctx context.Context, t warehouse.TableRef) (*warehouse.Partition, error) {
		c, err := bq(ctx)
		if err != nil {
			return nil, err
		}
		table, err := c.Table(ctx, t.Project, t.Dataset, t.Table)
		if err != nil {
			return nil, err
		}
		return table.Partition, nil
	}
}

type dryRunFunc = func(ctx context.Context, project, location, sql string) (*warehouse.Check, error)

// worktreeShareTTL keeps a finished prod worktree compile reusable only briefly
// so concurrent ColumnImpact and QueryCost share one compile while subsequent
// refreshes (e.g. after editing profiles.yml or workflow settings) always
// recompile fresh.
const worktreeShareTTL = 5 * time.Second

// wtCompile coalesces concurrent working-tree compiles for the same signature
// (e.g. when xf check runs ColumnImpact and QueryCost in parallel) and shares
// the finished graph briefly before expiring.
type wtCompile struct {
	sig        string
	done       chan struct{}
	finishedAt time.Time
	graph      *project.Graph
	err        error
}

func (w *wtCompile) fresh(sig string, now time.Time) bool {
	if w == nil || w.sig != sig {
		return false
	}
	select {
	case <-w.done:
		return !w.finishedAt.IsZero() && now.Sub(w.finishedAt) < worktreeShareTTL
	default:
		return true
	}
}

// optionSources keeps one cached changes.Source per Dataform compiler-options
// string, so xf impact and xf cost share the same in-memory and disk cache
// when compiling commits (e.g. merge-base or --pushed HeadSHA) and working-tree
// prod compiles with options.
type optionSources struct {
	bin, root, cacheDir string
	logger              *log.Logger
	compileWT           func(ctx context.Context, args []string) (*project.Graph, error)
	mu                  sync.Mutex
	byOpts              map[string]*changes.Source
	wt                  map[string]*wtCompile
}

func newOptionSources(bin, root string, logger *log.Logger) *optionSources {
	return &optionSources{
		bin:      bin,
		root:     root,
		cacheDir: costCacheDir(logger),
		logger:   logger,
		byOpts:   map[string]*changes.Source{},
		wt:       map[string]*wtCompile{},
	}
}

func (o *optionSources) worktree(ctx context.Context, options string, args []string, sig string) (*project.Graph, error) {
	o.mu.Lock()
	if wc := o.wt[options]; wc.fresh(sig, time.Now()) {
		o.mu.Unlock()
		select {
		case <-wc.done:
			return wc.graph, wc.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	wc := &wtCompile{sig: sig, done: make(chan struct{})}
	o.wt[options] = wc
	compileFn := o.compileWT
	if compileFn == nil {
		bin, root := o.bin, o.root
		compileFn = func(ctx context.Context, args []string) (*project.Graph, error) {
			return dataform.CompileWith(ctx, bin, root, args)
		}
	}
	o.mu.Unlock()

	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changes.DefaultCompileTimeout)
		defer cancel()
		g, err := compileFn(cctx, args)
		wc.graph, wc.err = g, err
		if err != nil || (g != nil && g.HasErrors()) {
			o.mu.Lock()
			if o.wt[options] == wc {
				delete(o.wt, options)
			}
			o.mu.Unlock()
		} else {
			o.mu.Lock()
			wc.finishedAt = time.Now()
			o.mu.Unlock()
		}
		close(wc.done)
	}()

	select {
	case <-wc.done:
		return wc.graph, wc.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (o *optionSources) get(key, options string, args []string) *changes.Source {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.byOpts[options]; s != nil {
		return s
	}
	s := &changes.Source{
		Root:     o.root,
		Key:      o.bin + "\n" + options,
		CacheDir: o.cacheDir,
		Compile: func(ctx context.Context, dir string) (*project.Graph, error) {
			if o.logger != nil {
				o.logger.Printf("compiling a snapshot in %s with %s", dir, options)
			}
			return dataform.CompileWith(ctx, o.bin, dir, args)
		},
	}
	o.byOpts[options] = s
	return s
}

// dataformCost checks query cost for xf serve in a Dataform project: dry
// runs through dryRun (the engine's cached ones) and tables' partitioning
// with the client bq makes. The merge-base is the one plain compiled to
// work out the Changed Actions. With prod compiler options, the project at
// root and its merge-base are compiled once more with them, using b, and
// those compiles are what is dry run, so the estimates are of the tables
// prod runs read. With defer to prod on, a table the branch adds that prod
// doesn't have is read from dev when lookups find it built there.
func dataformCost(bq func(context.Context) (warehouse.Warehouse, error), dryRun dryRunFunc, b dataform.Backend, root string, plain *changes.Source, opts *optionSources, lookups *deferral.Lookups, logger *log.Logger) engine.CostFunc {
	if opts == nil {
		opts = newOptionSources(b.Bin, root, logger)
	}
	parse := func(data []byte, err error) (*project.Graph, error) {
		if err != nil {
			return nil, err
		}
		return dataform.ParseCompileJSON(data)
	}
	return func(ctx context.Context, req engine.CostRequest, res *changes.Result, g *project.Graph, progress func(cost.Progress)) (*cost.Report, error) {
		if g == nil {
			var err error
			if g, err = dataform.ParseCompileJSON(res.HeadJSON); err != nil {
				return nil, fmt.Errorf("the compile of %s: %w", res.HeadRef, err)
			}
		}
		deps := cost.Deps{DryRun: dryRun, Partition: partitionLookup(bq)}
		if req.ProdCompilerOptions == "" {
			deps.Base = func(ctx context.Context) (*project.Graph, error) { return parse(plain.Commit(ctx, res.MergeBase)) }
			return cost.Run(ctx, res, cost.Head{Graph: g}, deps, progress)
		}

		args, err := dataform.SplitOptions(req.ProdCompilerOptions)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", config.KeyProdCompilerOptions, err)
		}
		src := opts.get(config.KeyProdCompilerOptions, req.ProdCompilerOptions, args)
		progress(cost.Progress{Phase: "Compiling with " + config.KeyProdCompilerOptions})
		var prod *project.Graph
		if res.Head == changes.HeadPushed {
			prod, err = parse(src.Commit(ctx, res.HeadSHA))
		} else {
			prod, err = opts.worktree(ctx, req.ProdCompilerOptions, args, impact.Signature(res, g))
		}
		if err != nil {
			return nil, fmt.Errorf("compiling with %s: %w", config.KeyProdCompilerOptions, err)
		}
		if prod.HasErrors() {
			return nil, fmt.Errorf("compiling with %s %q: %s", config.KeyProdCompilerOptions, req.ProdCompilerOptions, prod.Errors[0].Message)
		}
		// Each action's ID in the prod compile, by its ID in xf's (deleted
		// actions included, so a renamed action pairs with its merge-base).
		prodID := impact.ProdTargetFunc(impact.DevToProd(prod, g, res.Deleted))
		deps.Base = func(ctx context.Context) (*project.Graph, error) { return parse(src.Commit(ctx, res.MergeBase)) }
		head := cost.Head{Graph: prod, Compiled: config.KeyProdCompilerOptions,
			ID: func(id project.ActionID) project.ActionID { return project.ActionID(prodID(string(id))) }}
		if req.StandIns && lookups != nil {
			deps.InDev = inDev(g, lookups, logger)
		}
		return cost.Run(ctx, res, head, deps, progress)
	}
}

// inDev is cost.Deps.InDev for g, xf's compile: whether an action's table
// in g is built, as lookups find it.
func inDev(g *project.Graph, lookups *deferral.Lookups, logger *log.Logger) func(context.Context, project.ActionID) (project.Target, time.Time, bool) {
	return func(ctx context.Context, id project.ActionID) (project.Target, time.Time, bool) {
		a := g.Actions[id]
		if a == nil {
			return project.Target{}, time.Time{}, false
		}
		ok, ms, err := lookups.BuiltInDev(ctx, a.Target)
		if err != nil {
			logger.Printf("query cost: %v", err)
		}
		var builtAt time.Time
		if ms > 0 {
			builtAt = time.UnixMilli(ms)
		}
		return a.Target, builtAt, ok
	}
}

// dbtCost checks query cost for xf serve in a dbt project: the working
// tree's compile is dry run, which reads what b's target resolves to, and
// the merge-base is a snapshot compiled for the purpose, as Changed Actions
// only parse theirs. With dbtProdTarget set to another target, both are
// compiled with it instead, so the dry runs read prod's tables. A project
// that builds in Databricks has nothing to estimate, so its report is each
// changed model's last run instead (ADR 0042, databricksCost).
func dbtCost(pw *projectWarehouse, dryRun dryRunFunc, targets *dbtTargets) engine.CostFunc {
	bq := pw.bq
	return func(ctx context.Context, req engine.CostRequest, res *changes.Result, g *project.Graph, progress func(cost.Progress)) (*cost.Report, error) {
		if g == nil {
			return nil, changes.ErrWorktreeOnly
		}
		switch g.WarehouseOr(pw.dbtTarget().Type) {
		case project.Databricks:
			return pw.databricksCost(ctx, res, g, req.DbtProdTarget, progress)
		case project.Snowflake:
			return pw.snowflakeCost(ctx, res, g, req.DbtProdTarget, progress)
		}
		target := req.DbtProdTarget
		deps := cost.Deps{DryRun: dryRun, Partition: partitionLookup(bq)}
		deps.Base = func(ctx context.Context) (*project.Graph, error) { return targets.commit(ctx, target, res.MergeBase) }
		if !targets.other(target) {
			return cost.Run(ctx, res, cost.Head{Graph: g}, deps, progress)
		}
		progress(cost.Progress{Phase: "Compiling with " + config.KeyDbtProdTarget})
		prod, err := targets.worktree(ctx, target, impact.Signature(res, g))
		if err != nil {
			return nil, err
		}
		_, toProd := impact.Retarget(res, g, prod)
		head := cost.Head{Graph: prod, Compiled: config.KeyDbtProdTarget, ID: func(id project.ActionID) project.ActionID {
			if p, ok := toProd[string(id)]; ok {
				return project.ActionID(p)
			}
			return id
		}}
		if req.StandIns {
			deps.InDev = dbtInDev(g, bq)
		}
		return cost.Run(ctx, res, head, deps, progress)
	}
}

// dbtInDev is cost.Deps.InDev for a dbt BigQuery compile g: checks whether an
// action's dev relation exists in BigQuery.
func dbtInDev(g *project.Graph, bq func(context.Context) (warehouse.Warehouse, error)) func(context.Context, project.ActionID) (project.Target, time.Time, bool) {
	return func(ctx context.Context, id project.ActionID) (project.Target, time.Time, bool) {
		a := g.Actions[id]
		if a == nil {
			return project.Target{}, time.Time{}, false
		}
		c, err := bq(ctx)
		if err != nil {
			return project.Target{}, time.Time{}, false
		}
		tbl, err := c.Table(ctx, a.Target.Database, a.Target.Schema, a.Target.Name)
		if err != nil || tbl == nil {
			return project.Target{}, time.Time{}, false
		}
		return a.Target, time.Time{}, true
	}
}
