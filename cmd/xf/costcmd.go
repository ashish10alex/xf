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
	"sync"
	"syscall"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
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
		config.KeyDefaultBranch, config.KeyProdCompilerOptions)
	pushed := fs.Bool("pushed", false, "estimate the branch pushed to origin (what an API run compiles) instead of the working tree; Dataform only")
	failOver := fs.String("fail-over", "", "exit 1 when the changed actions scan more than this `size` between them, e.g. 100GB (units are binary: 1 GB is 2^30 bytes)")
	usdPerTiB := fs.Float64("usd-per-tib", bigquery.OnDemandUSDPerTiB, "the `price` in dollars of a TiB scanned")
	output := fs.String("output", "", "also write the report to `path`: as Markdown, for a pull request comment, or as JSON with --json")
	fs.StringVar(output, "o", "", "")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	color := fs.String("color", "auto", "colour the text: `when` is auto (on a terminal, unless $NO_COLOR is set), always or never")
	commandUsage(fs, "xf cost [flags]", `Prints what the actions this branch changes would scan in BigQuery, and what
that costs on demand: every changed action with a query is dry run, as it is
on the branch and as it was at the merge-base with the default branch. An
incremental table counts its incremental run, with its full rebuild beside
it. A read of a partitioned table that never names its partition column is
marked. An action BigQuery gives no estimate for is listed and left out of
the total. Dry runs are free and run nothing. It always checks afresh, and
shares the work with xf through xf serve. Progress goes to stderr. Exits 0, 1
with --fail-over when the total is over the limit, or 2 when the check could
not be made.`)
	help, err := parseArgs(fs, args)
	if help {
		return costOK
	}
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %v (see xf cost --help)", fs.Args())
	}
	opts := cost.Options{USDPerTiB: *usdPerTiB}
	if err == nil && *usdPerTiB <= 0 {
		err = fmt.Errorf("--usd-per-tib must be more than 0, not %v", *usdPerTiB)
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
	if err == nil {
		var code int
		if code, err = checkCost(pf, sf, *pushed, opts, *output, *asJSON); err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "xf cost:", err)
	return costFailed
}

func checkCost(pf projectFlags, sf *settingFlags, pushed bool, opts cost.Options, output string, asJSON bool) (int, error) {
	root, name, err := pf.find()
	if err != nil {
		return 0, err
	}
	settings, err := sf.load(root)
	if err != nil {
		return 0, err
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
		ProdCompilerOptions: settings.ProdCompilerOptions, Unset: unsetProdOptions(settings)}
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

	if n := report.Unestimated(); n == 1 {
		fmt.Fprintln(os.Stderr, "xf cost: 1 action could not be estimated; it is not in the total, and --fail-over doesn't count it")
	} else if n > 1 {
		fmt.Fprintf(os.Stderr, "xf cost: %d actions could not be estimated; they are not in the total, and --fail-over doesn't count them\n", n)
	}
	if report.BaseError != "" {
		fmt.Fprintln(os.Stderr, "xf cost: no change against the merge-base is shown, as it could not be compiled:", report.BaseError)
	}
	if report.Exceeds(opts.Threshold) {
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
func partitionLookup(bq func(context.Context) (*bigquery.Client, error)) func(ctx context.Context, t bigquery.TableRef) (*bigquery.Partition, error) {
	return func(ctx context.Context, t bigquery.TableRef) (*bigquery.Partition, error) {
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

type dryRunFunc = func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)

// dataformCost checks query cost for xf serve in a Dataform project: dry
// runs through dryRun (the engine's cached ones) and tables' partitioning
// with the client bq makes. The merge-base is the one plain compiled to
// work out the Changed Actions. With prod compiler options, the project at
// root and its merge-base are compiled once more with them, using b, and
// those compiles are what is dry run, so the estimates are of the tables
// prod runs read. With defer to prod on, a table the branch adds that prod
// doesn't have is read from dev when lookups find it built there.
func dataformCost(bq func(context.Context) (*bigquery.Client, error), dryRun dryRunFunc, b dataform.Backend, root string, plain *changes.Source, lookups *deferral.Lookups, logger *log.Logger) engine.CostFunc {
	var mu sync.Mutex
	prodSources := map[string]*changes.Source{}
	prodSource := func(options string, args []string) *changes.Source {
		mu.Lock()
		defer mu.Unlock()
		if s := prodSources[options]; s != nil {
			return s
		}
		s := &changes.Source{
			Root:     root,
			Key:      b.Bin + "\n" + options,
			CacheDir: costCacheDir(logger),
			Compile: func(ctx context.Context, dir string) (*project.Graph, error) {
				logger.Printf("compiling a snapshot in %s with %s", dir, config.KeyProdCompilerOptions)
				return dataform.CompileWith(ctx, b.Bin, dir, args)
			},
		}
		prodSources[options] = s
		return s
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
		src := prodSource(req.ProdCompilerOptions, args)
		progress(cost.Progress{Phase: "Compiling with " + config.KeyProdCompilerOptions})
		var prod *project.Graph
		if res.Head == changes.HeadPushed {
			prod, err = parse(src.Commit(ctx, res.HeadSHA))
		} else {
			prod, err = dataform.CompileWith(ctx, b.Bin, root, args)
		}
		if err != nil {
			return nil, fmt.Errorf("compiling with %s: %w", config.KeyProdCompilerOptions, err)
		}
		if prod.HasErrors() {
			return nil, fmt.Errorf("compiling with %s %q: %s", config.KeyProdCompilerOptions, req.ProdCompilerOptions, prod.Errors[0].Message)
		}
		// Each action's ID in the prod compile, by its ID in xf's.
		prodID := impact.ProdTargetFunc(impact.DevToProd(prod, g, nil))
		deps.Base = func(ctx context.Context) (*project.Graph, error) { return parse(src.Commit(ctx, res.MergeBase)) }
		head := cost.Head{Graph: prod, Compiled: config.KeyProdCompilerOptions,
			ID: func(id project.ActionID) project.ActionID { return project.ActionID(prodID(string(id))) }}
		if req.Defer && lookups != nil {
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
// compiled with it instead, so the dry runs read prod's tables.
func dbtCost(bq func(context.Context) (*bigquery.Client, error), dryRun dryRunFunc, targets *dbtTargets) engine.CostFunc {
	return func(ctx context.Context, req engine.CostRequest, res *changes.Result, g *project.Graph, progress func(cost.Progress)) (*cost.Report, error) {
		if g == nil {
			return nil, changes.ErrWorktreeOnly
		}
		target := req.DbtProdTarget
		deps := cost.Deps{DryRun: dryRun, Partition: partitionLookup(bq)}
		deps.Base = func(ctx context.Context) (*project.Graph, error) { return targets.commit(ctx, target, res.MergeBase) }
		if !targets.other(target) {
			return cost.Run(ctx, res, cost.Head{Graph: g}, deps, progress)
		}
		progress(cost.Progress{Phase: "Compiling with " + config.KeyDbtProdTarget})
		prod, err := targets.worktree(ctx, target)
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
		return cost.Run(ctx, res, head, deps, progress)
	}
}
