package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Exit codes of xf impact.
const (
	impactOK     = 0
	impactFailOn = 1 // a change at or above --fail-on was found
	impactFailed = 2 // the check could not be made
)

// runImpact prints the column impact of the project's Changed Actions and
// returns the exit code.
func runImpact(args []string) int {
	fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtProdTarget, config.KeyDbtCompileWithHooks,
		config.KeyDatabricksWarehouse, config.KeyDatabricksProfile, config.KeySnowflakeWarehouse, config.KeySnowflakeConnection,
		config.KeyDefaultBranch, config.KeyDevCompilerOptions, config.KeyProdCompilerOptions, config.KeyLineageProjects)
	pushed := fs.Bool("pushed", false, "compare the branch pushed to origin (what an API run compiles) instead of the working tree; Dataform only")
	failOn := fs.String("fail-on", "", "exit 1 when a change of this `severity` or worse is found: critical, warning or unknown")
	failOnUnchecked := fs.Bool("fail-on-unchecked", false, "exit 1 when any changed model failed to check (dry run or schema error; ignores operations/scripts and tables not yet built)")
	output := fs.String("output", "", "also write the report to `path`: as Markdown, for a pull request comment, or as JSON with --json")
	fs.StringVar(output, "o", "", "")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	color := fs.String("color", "auto", "colour the text: `when` is auto (on a terminal, unless $NO_COLOR is set), always or never")
	web := fs.Bool("web", false, "open xf web on its Checks page instead, where the check runs as I runs it there")
	attention := fs.Bool("attention", false, "list only the models to fix, review or look into, in the text and the --output Markdown; the summary still counts every model")
	commandUsage(fs, "xf impact [flags]", `Prints the column impact of the actions this branch changes, or with --json
as JSON for scripts; --output also writes it as Markdown for a pull request:
every changed table, view and incremental table (in dbt: model) is dry run
and compared with its table in BigQuery, and Dataplex is asked who reads the
columns it drops or retypes and the tables it deletes. A dbt project on
Databricks is checked there instead: each changed model's query is planned
with DESCRIBE QUERY on its SQL warehouse, which starts if it is stopped and
bills while it runs, and Unity Catalog gives the tables and their readers;
the report names the warehouse. It always checks
afresh, and shares the work with xf through xf serve. Progress goes to
stderr. With --web it opens xf web on the column impact tab of its Checks
page instead, where y copies the Markdown, and runs until ctrl+c as xf web
does. Exits 0, 1 with --fail-on when a change that severe is found, or 2
when the check could not be made.`)
	help, err := parseArgs(fs, args)
	if help {
		return impactOK
	}
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %v (see xf impact --help)", fs.Args())
	}
	var threshold impact.Severity
	var gate bool
	if err == nil {
		err = sf.checkFlag(config.KeyLineageProjects)
	}
	if err == nil {
		threshold, gate, err = parseFailOn(*failOn)
	}
	if err == nil && *web && (*pushed || *failOn != "" || *failOnUnchecked || *output != "" || *asJSON || *attention) {
		err = errors.New("--web can't be used with --pushed, --fail-on, --fail-on-unchecked, --output, --json or --attention")
	}
	if err == nil && *attention && *asJSON {
		err = errors.New("--attention can't be used with --json, which has every model: keep those whose severity isn't low")
	}
	useColour := false
	if err == nil {
		useColour, err = useColor(*color, os.Stdout, os.Getenv)
	}
	if err == nil && *web {
		if err = startWeb(pf, sf, webStart{cmdName: "xf impact", check: "impact"}); err == nil {
			return impactOK
		}
	}
	if err == nil {
		var code int
		if code, err = checkImpact(pf, sf, *pushed, gate, threshold, *failOnUnchecked, *output, *asJSON, useColour, *attention); err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "xf impact:", err)
	return impactFailed
}

func checkImpact(pf projectFlags, sf *settingFlags, pushed, gate bool, threshold impact.Severity, failOnUnchecked bool, output string, asJSON, color, attention bool) (int, error) {
	root, name, err := pf.find()
	if err != nil {
		return 0, err
	}
	settings, err := sf.load(root)
	if err != nil {
		return 0, err
	}
	for _, w := range settings.Warnings {
		fmt.Fprintln(os.Stderr, "xf impact:", w)
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

	req := engine.ImpactRequest{DefaultBranch: cmp.Or(settings.DefaultBranch, config.DefaultBranch), Head: changes.HeadWorktree, Refresh: true,
		DevCompilerOptions: settings.DevCompilerOptions, ProdCompilerOptions: settings.ProdCompilerOptions, LineageProjects: settings.LineageProjects, Unset: unsetProdOptions(settings)}
	if name == backend.DBT {
		req.DevCompilerOptions, req.ProdCompilerOptions = "", ""
		req.DbtProdTarget, req.Unset = settings.DbtProdTarget, unsetDbtProdTarget(settings, os.Getenv)
	}
	if pushed {
		req.Head = changes.HeadPushed
	}
	pr := newProgressPrinter(os.Stderr)
	pr.say("Working out the changed actions against " + req.DefaultBranch)
	report, err := client.ColumnImpact(ctx, req, func(p impact.Progress) {
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
	if err := writeReport(os.Stdout, output, report, asJSON, color, attention); err != nil {
		return 0, err
	}

	if hint := impact.UnsetHint(report); hint != "" && asJSON { // the text says it under its summary
		fmt.Fprintln(os.Stderr, "xf impact:", strings.ReplaceAll(hint, "`", ""))
	}
	worst, found := impact.Worst(report)
	if !(gate && threshold <= impact.SeverityUnknown) {
		if unknown := countUnknown(report); unknown == 1 {
			fmt.Fprintln(os.Stderr, "xf impact: the readers of 1 change could not be looked up; --fail-on doesn't count it (use --fail-on unknown to fail on it)")
		} else if unknown > 1 {
			fmt.Fprintf(os.Stderr, "xf impact: the readers of %d changes could not be looked up; --fail-on doesn't count them (use --fail-on unknown to fail on them)\n", unknown)
		}
	}
	if (gate && found && worst >= threshold) || (failOnUnchecked && len(impact.UncheckedFailures(report)) > 0) {
		return impactFailOn, nil
	}
	return impactOK, nil
}

// writeReport writes the impact report to w as text, coloured with color,
// and to output when given as Markdown for a pull request comment; with
// asJSON, as JSON to both. attention lists only the models that need it.
func writeReport(w io.Writer, output string, report *impact.Report, asJSON, color, attention bool) error {
	text, file := impact.Text(report, color, attention), impact.Markdown(report)+"\n"
	if attention {
		file = impact.AttentionMarkdown(report) + "\n"
	}
	if asJSON {
		data, err := impact.JSON(report)
		if err != nil {
			return err
		}
		text, file = string(data), string(data)
	}
	if _, err := io.WriteString(w, text); err != nil {
		return err
	}
	if output == "" {
		return nil
	}
	if dir := filepath.Dir(output); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(output, []byte(file), 0o644)
}

// countUnknown counts the changes whose readers couldn't be looked up.
func countUnknown(r *impact.Report) int {
	n := 0
	for _, t := range r.AtRisk {
		if t.Deleted && t.ReadersError != "" {
			n++
		}
		for _, c := range t.Columns {
			if c.ReadersError != "" && !c.Change.OnBase {
				n++
			}
		}
	}
	return n
}

// progressPrinter shows progress on one line rewritten in place on a
// terminal, else a line per phase, so CI logs stay short.
type progressPrinter struct {
	mu       sync.Mutex
	w        io.Writer
	terminal bool
	phases   map[string]string
	shown    bool
}

func newProgressPrinter(f *os.File) *progressPrinter {
	st, err := f.Stat()
	return &progressPrinter{w: f, terminal: err == nil && st.Mode()&os.ModeCharDevice != 0, phases: map[string]string{}}
}

func (p *progressPrinter) say(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal {
		_, _ = fmt.Fprintf(p.w, "\r\x1b[K%s…", text)
		p.shown = true
		return
	}
	key := ""
	if prefix, _, ok := strings.Cut(text, ":"); ok {
		key = prefix
	}
	phase := text
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == ' ' {
			phase = text[:i]
			break
		}
	}
	if p.phases == nil {
		p.phases = map[string]string{}
	}
	if phase != p.phases[key] {
		p.phases[key] = phase
		_, _ = fmt.Fprintln(p.w, text+"…")
	}
}

func (p *progressPrinter) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminal && p.shown {
		_, _ = fmt.Fprint(p.w, "\r\x1b[K")
	}
}

// impactChecker checks column impact for xf serve: the tables' schemas from
// BigQuery with the client bq makes, dry runs through dryRun (the engine's
// cached ones) and readers from Dataplex. With dev or prod compiler options
// it compiles the project at root once more with each, using bin: readers
// that are dev or prod runs fold into the actions they run, and with prod
// options the changes are compared with the tables prod runs build. plain
// compiles the merge-base, as it did for the Changed Actions.
func impactChecker(bq func(context.Context) (warehouse.Warehouse, error), dryRun func(ctx context.Context, project, location, sql string) (*warehouse.Check, error), bin, root string, plain *changes.Source, opts *optionSources) engine.ImpactFunc {
	lc := lazyLineage()
	if opts == nil {
		opts = newOptionSources(bin, root, nil)
	}
	return func(ctx context.Context, req engine.ImpactRequest, res *changes.Result, g *project.Graph, progress func(impact.Progress)) (*impact.Report, error) {
		if g == nil {
			var err error
			if g, err = dataform.ParseCompileJSON(res.HeadJSON); err != nil {
				return nil, fmt.Errorf("the compile of %s: %w", res.HeadRef, err)
			}
		}
		client, err := lc(ctx)
		if err != nil {
			return nil, err
		}
		devRuns, err := runsToActionsWith(ctx, bin, root, config.KeyDevCompilerOptions, req.DevCompilerOptions, g, res, opts, progress)
		if err != nil {
			return nil, err
		}
		prodRuns, err := runsToActionsWith(ctx, bin, root, config.KeyProdCompilerOptions, req.ProdCompilerOptions, g, res, opts, progress)
		if err != nil {
			return nil, err
		}
		toAction := impact.ToProdFunc(merge(devRuns, prodRuns))
		var prodTarget func(string) string
		if req.ProdCompilerOptions != "" {
			prodTarget = impact.ProdTargetFunc(prodRuns)
		}
		tables := lineage.NewTables(lazyTables(bq))
		return impact.Run(ctx, res, g, impact.Deps{
			Schema:     tables.Schema,
			DryRun:     dryRun,
			Readers:    lineage.NewSource(client, tables, g, toAction).WithProjects(config.ParseLineageProjects(req.LineageProjects)...),
			ToProd:     toAction,
			ProdTarget: prodTarget,
			// The merge-base as Changed Actions compiled it, to tell the
			// base branch's changes from the branch's.
			Base: func(ctx context.Context) (*project.Graph, error) {
				data, err := plain.Commit(ctx, res.MergeBase)
				if err != nil {
					return nil, err
				}
				return dataform.ParseCompileJSON(data)
			},
		}, progress)
	}
}

// runsToActions maps the targets of a compile with the compiler options of
// key to the project's actions (see impact.DevToProd); nil without options.
// With res.Head == changes.HeadPushed, it compiles the pushed commit
// res.HeadSHA rather than the working tree.
func runsToActions(ctx context.Context, bin, root, key, options string, g *project.Graph, res *changes.Result, optSource func(key, options string, args []string) *changes.Source, progress func(impact.Progress)) (map[string]string, error) {
	return runsToActionsCore(ctx, bin, root, key, options, g, res, optSource, nil, progress)
}

func runsToActionsWith(ctx context.Context, bin, root, key, options string, g *project.Graph, res *changes.Result, opts *optionSources, progress func(impact.Progress)) (map[string]string, error) {
	var optSource func(key, options string, args []string) *changes.Source
	var wt func(ctx context.Context, options string, args []string, sig string) (*project.Graph, error)
	if opts != nil {
		optSource = opts.get
		wt = opts.worktree
	}
	return runsToActionsCore(ctx, bin, root, key, options, g, res, optSource, wt, progress)
}

func runsToActionsCore(ctx context.Context, bin, root, key, options string, g *project.Graph, res *changes.Result, optSource func(key, options string, args []string) *changes.Source, wt func(ctx context.Context, options string, args []string, sig string) (*project.Graph, error), progress func(impact.Progress)) (map[string]string, error) {
	if options == "" {
		return nil, nil
	}
	args, err := dataform.SplitOptions(options)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	progress(impact.Progress{Phase: "Compiling with " + key})
	var other *project.Graph
	if res.Head == changes.HeadPushed {
		if res.HeadSHA == "" || optSource == nil {
			return nil, fmt.Errorf("compiling with %s: pushed head commit is unknown", key)
		}
		var data []byte
		data, err = optSource(key, options, args).Commit(ctx, res.HeadSHA)
		if err == nil {
			other, err = dataform.ParseCompileJSON(data)
		}
	} else if wt != nil {
		other, err = wt(ctx, options, args, impact.Signature(res, g))
	} else {
		other, err = dataform.CompileWith(ctx, bin, root, args)
	}
	if err != nil {
		return nil, fmt.Errorf("compiling with %s: %w", key, err)
	}
	if other.HasErrors() {
		return nil, fmt.Errorf("compiling with %s %q: %s", key, options, other.Errors[0].Message)
	}
	return impact.DevToProd(other, g, res.Deleted), nil
}

// merge is the union of maps, the later winning.
func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		maps.Copy(out, m)
	}
	return out
}

// lazyLineage makes a Data Lineage client on first use, as lazyClient does
// for BigQuery.
func lazyLineage() func(ctx context.Context) (*lineage.Client, error) {
	var mu sync.Mutex
	var client *lineage.Client
	return func(ctx context.Context) (*lineage.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if client == nil {
			c, err := lineage.NewClient(ctx)
			if err != nil {
				return nil, err
			}
			client = c
		}
		return client, nil
	}
}

// errNoEngine is what an impact check reports before the engine is made.
var errNoEngine = errors.New("the engine is not running")
