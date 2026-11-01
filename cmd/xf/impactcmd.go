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
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// Exit codes of xf impact.
const (
	impactOK     = 0
	impactFailOn = 1 // a change at or above --fail-on was found
	impactFailed = 2 // the check could not be made
)

// runImpact prints the column impact of the project's Changed Actions as
// Markdown and returns the exit code.
func runImpact(args []string) int {
	fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtProdTarget, config.KeyDbtCompileWithHooks,
		config.KeyDefaultBranch, config.KeyDevCompilerOptions, config.KeyProdCompilerOptions)
	pushed := fs.Bool("pushed", false, "compare the branch pushed to origin (what an API run compiles) instead of the working tree; Dataform only")
	failOn := fs.String("fail-on", "", "exit 1 when a change of this `severity` or worse is found: critical or warning")
	output := fs.String("output", "", "write the report to `path` instead of stdout")
	fs.StringVar(output, "o", "", "")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of Markdown")
	web := fs.Bool("web", false, "open xf web on its Checks page instead, where the check runs as I runs it there")
	commandUsage(fs, "xf impact [flags]", `Prints the column impact of the actions this branch changes, as Markdown for
a pull request, or with --json as JSON for scripts: every changed table, view
and incremental table (in dbt: model) is dry run and compared with its table
in BigQuery, and Dataplex is asked who reads the columns it drops or retypes
and the tables it deletes. It always checks afresh, and shares the work with
xf through xf serve. Progress goes to stderr. With --web it opens xf web on
the column impact tab of its Checks page instead, where y copies the
Markdown, and runs until ctrl+c as xf web does. Exits 0, 1 with --fail-on
when a change that severe is found, or 2 when the check could not be made.`)
	help, err := parseArgs(fs, args)
	if help {
		return impactOK
	}
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %v (see xf impact --help)", fs.Args())
	}
	threshold, ok := map[string]impact.Severity{"": 0, "critical": impact.SeverityCritical, "warning": impact.SeverityWarning}[*failOn]
	if err == nil && !ok {
		err = fmt.Errorf("--fail-on must be critical or warning, not %q", *failOn)
	}
	if err == nil && *web && (*pushed || *failOn != "" || *output != "" || *asJSON) {
		err = errors.New("--web can't be used with --pushed, --fail-on, --output or --json")
	}
	if err == nil && *web {
		if err = startWeb(pf, sf, webStart{cmdName: "xf impact", check: "impact"}); err == nil {
			return impactOK
		}
	}
	if err == nil {
		var code int
		if code, err = checkImpact(pf, sf, *pushed, *failOn != "", threshold, *output, *asJSON); err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "xf impact:", err)
	return impactFailed
}

func checkImpact(pf projectFlags, sf *settingFlags, pushed, gate bool, threshold impact.Severity, output string, asJSON bool) (int, error) {
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
		DevCompilerOptions: settings.DevCompilerOptions, ProdCompilerOptions: settings.ProdCompilerOptions, Unset: unsetProdOptions(settings)}
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
		return 0, err
	}
	if err := writeReport(os.Stdout, output, report, asJSON); err != nil {
		return 0, err
	}

	if hint := impact.UnsetHint(report); hint != "" {
		fmt.Fprintln(os.Stderr, "xf impact:", strings.ReplaceAll(hint, "`", ""))
	}
	worst, found := impact.Worst(report)
	if unknown := countUnknown(report); unknown == 1 {
		fmt.Fprintln(os.Stderr, "xf impact: the readers of 1 change could not be looked up; --fail-on doesn't count it")
	} else if unknown > 1 {
		fmt.Fprintf(os.Stderr, "xf impact: the readers of %d changes could not be looked up; --fail-on doesn't count them\n", unknown)
	}
	if gate && found && worst >= threshold {
		return impactFailOn, nil
	}
	return impactOK, nil
}

// writeReport writes the impact report as Markdown, or with asJSON as
// JSON: to output when given, else to w.
func writeReport(w io.Writer, output string, report *impact.Report, asJSON bool) error {
	text := impact.Markdown(report) + "\n"
	if asJSON {
		data, err := impact.JSON(report)
		if err != nil {
			return err
		}
		text = string(data)
	}
	if output != "" && output != "-" {
		if dir := filepath.Dir(output); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		return os.WriteFile(output, []byte(text), 0o644)
	}
	_, err := io.WriteString(w, text)
	return err
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
	w        io.Writer
	terminal bool
	phase    string
	shown    bool
}

func newProgressPrinter(f *os.File) *progressPrinter {
	st, err := f.Stat()
	return &progressPrinter{w: f, terminal: err == nil && st.Mode()&os.ModeCharDevice != 0}
}

func (p *progressPrinter) say(text string) {
	if p.terminal {
		_, _ = fmt.Fprintf(p.w, "\r\x1b[K%s…", text)
		p.shown = true
		return
	}
	phase := text
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == ' ' {
			phase = text[:i]
			break
		}
	}
	if phase != p.phase {
		p.phase = phase
		_, _ = fmt.Fprintln(p.w, text+"…")
	}
}

func (p *progressPrinter) done() {
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
func impactChecker(bq func(context.Context) (*bigquery.Client, error), dryRun func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error), bin, root string, plain *changes.Source) engine.ImpactFunc {
	lc := lazyLineage()
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
		devRuns, err := runsToActions(ctx, bin, root, config.KeyDevCompilerOptions, req.DevCompilerOptions, g, res, progress)
		if err != nil {
			return nil, err
		}
		prodRuns, err := runsToActions(ctx, bin, root, config.KeyProdCompilerOptions, req.ProdCompilerOptions, g, res, progress)
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
			Readers:    lineage.NewSource(client, tables, g, toAction),
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
func runsToActions(ctx context.Context, bin, root, key, options string, g *project.Graph, res *changes.Result, progress func(impact.Progress)) (map[string]string, error) {
	if options == "" {
		return nil, nil
	}
	args, err := dataform.SplitOptions(options)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	progress(impact.Progress{Phase: "Compiling with " + key})
	other, err := dataform.CompileWith(ctx, bin, root, args)
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
