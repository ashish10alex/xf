package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/check"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/impact"
)

// checkFlags are xf check's own flags.
type checkFlags struct {
	pushed      bool
	opts        check.Options
	output      string
	asJSON      bool
	comment     bool
	codequality string
	// preflight runs the probes alone.
	preflight bool
}

// runCheck checks the branch's column impact and query cost in one report
// (ADR 0040) and returns the exit code.
func runCheck(args []string) int {
	if len(args) > 0 && args[0] == "init" {
		return runCheckInit(args[1:])
	}
	fs := flag.NewFlagSet("xf check", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDbtBin, config.KeyDbtTarget, config.KeyDbtProdTarget, config.KeyDbtCompileWithHooks,
		config.KeyDefaultBranch, config.KeyDevCompilerOptions, config.KeyProdCompilerOptions)
	pushed := fs.Bool("pushed", false, "check the branch pushed to origin (what an API run compiles) instead of the working tree; Dataform only")
	failOn := fs.String("fail-on", "", "exit 1 when a column change of this `severity` or worse is found: critical or warning")
	failOver := fs.String("fail-over", "", "exit 1 when the changed actions scan more than this `size` between them, e.g. 100GB (units are binary)")
	usdPerTiB := fs.Float64("usd-per-tib", bigquery.OnDemandUSDPerTiB, "the `price` in dollars of a TiB scanned")
	quietUnder := fs.String("quiet-under", "1GiB", "a run that grows by less than this `size` isn't worth a comment on cost alone")
	output := fs.String("output", "", "write the report to `path` instead of stdout")
	fs.StringVar(output, "o", "", "")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of Markdown")
	comment := fs.Bool("comment", false, "post the report on the pull request (merge request) as one comment, edited on each push; in GitHub Actions or GitLab CI")
	codequality := fs.String("codequality", "", "also write the findings as a GitLab Code Quality report to `path`")
	preflight := fs.Bool("preflight", false, "only probe what the checks need (tools, git history, prod settings, Google access, the token), print the checklist, and exit 0, or 2 when something stops them")
	commandUsage(fs, "xf check [flags]", `Checks the column impact and the query cost of the actions this branch
changes, as xf impact and xf cost do, in one report for a pull request:
Markdown, or JSON with --json. With --comment it posts the report on the
pull request (GitHub Actions) or merge request (GitLab CI) as one comment,
edited on each push, and only when there is something to say. In GitHub
Actions the findings also go on the changed lines; --codequality writes them
for GitLab. Progress goes to stderr.

It first probes, in seconds, what the checks need, and prints the checklist
on stderr; a probe that makes a check impossible stops it there. Exits 0, 1
when --fail-on or --fail-over trips, or 2 when neither check could be made,
or --comment couldn't comment (the checks run first).

  xf check init gitlab|github   writes the CI job (see xf check init --help)`)
	help, err := parseArgs(fs, args)
	if help {
		return check.OK
	}
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected arguments: %v (see xf check --help)", fs.Args())
	}
	cf := checkFlags{pushed: *pushed, output: *output, asJSON: *asJSON, comment: *comment, codequality: *codequality, preflight: *preflight}
	cf.opts.Cost.USDPerTiB = *usdPerTiB
	if err == nil {
		cf.opts.FailOn, cf.opts.Gate, err = parseFailOn(*failOn)
	}
	if err == nil && *usdPerTiB <= 0 {
		err = fmt.Errorf("--usd-per-tib must be more than 0, not %v", *usdPerTiB)
	}
	if err == nil && *failOver != "" {
		if cf.opts.Cost.Threshold, err = cost.ParseSize(*failOver); err == nil && cf.opts.Cost.Threshold <= 0 {
			err = errors.New("must be more than 0")
		}
		if err != nil {
			err = fmt.Errorf("--fail-over: %w", err)
		}
	}
	if err == nil {
		if cf.opts.QuietUnder, err = cost.ParseSize(*quietUnder); err != nil {
			err = fmt.Errorf("--quiet-under: %w", err)
		}
	}
	if err == nil {
		var code int
		if code, err = checkBranch(pf, sf, cf); err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "xf check:", err)
	return check.Failed
}

// parseFailOn reads --fail-on: critical, warning, or "" for no gate.
func parseFailOn(s string) (impact.Severity, bool, error) {
	switch s {
	case "":
		return 0, false, nil
	case "critical":
		return impact.SeverityCritical, true, nil
	case "warning":
		return impact.SeverityWarning, true, nil
	}
	return 0, false, fmt.Errorf("--fail-on must be critical or warning, not %q", s)
}

func checkBranch(pf projectFlags, sf *settingFlags, cf checkFlags) (int, error) {
	root, name, err := pf.find()
	if err != nil {
		return 0, err
	}
	settings, err := sf.load(root)
	if err != nil {
		return 0, err
	}
	for _, w := range settings.Warnings {
		fmt.Fprintln(os.Stderr, "xf check:", w)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Find the pull request before the checks. A missing token waits for
	// them: the job fails after it has a report (ADR 0041).
	var pr *check.PR
	var prErr, tokenErr error
	if cf.comment {
		pr, prErr = check.FindPR(os.Getenv, os.ReadFile)
		switch {
		case errors.Is(prErr, check.ErrNoPR):
			fmt.Fprintln(os.Stderr, "xf check: not commenting:", prErr)
		case errors.Is(prErr, check.ErrNoToken):
			tokenErr = prErr
		case prErr != nil:
			return 0, fmt.Errorf("--comment: %w", prErr)
		}
	}
	commenting := pr != nil && pr.Token != ""

	base := cmp.Or(settings.DefaultBranch, config.DefaultBranch)
	prefix := gitPrefix(root)
	r := &check.Report{Project: cmp.Or(prefix, ".")}
	r.Preflight = runPreflight(ctx, preflightProbes(name, root, base, settings, cf.comment, pr, prErr))
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	fmt.Fprint(os.Stderr, r.Preflight.Text())
	if cf.preflight {
		if cf.asJSON {
			data, err := json.MarshalIndent(r.Preflight, "", "  ")
			if err != nil {
				return 0, err
			}
			if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
				return 0, err
			}
		}
		if r.Preflight.Stops() {
			return check.Failed, nil
		}
		return check.OK, nil
	}
	if r.Preflight.Stops() {
		if cf.output != "" {
			if err := writeCheck(os.Stdout, cf.output, r, cf.opts, cf.asJSON); err != nil {
				return 0, err
			}
		}
		if commenting {
			if err := postComment(ctx, pr, r, cf.opts, cmp.Or(pr.Commit, gitHead(root))); err != nil {
				fmt.Fprintln(os.Stderr, "xf check: --comment:", err)
			}
		}
		return 0, errors.New(r.Preflight.Reason())
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

	head := changes.HeadWorktree
	if cf.pushed {
		head = changes.HeadPushed
	}

	pr0 := newProgressPrinter(os.Stderr)
	pr0.say("Working out the changed actions against " + base)
	ireq := engine.ImpactRequest{DefaultBranch: base, Head: head, Refresh: true,
		DevCompilerOptions: settings.DevCompilerOptions, ProdCompilerOptions: settings.ProdCompilerOptions, Unset: unsetProdOptions(settings)}
	creq := engine.CostRequest{DefaultBranch: base, Head: head, Refresh: true,
		ProdCompilerOptions: settings.ProdCompilerOptions, Unset: unsetProdOptions(settings)}
	if name == backend.DBT {
		ireq.DevCompilerOptions, ireq.ProdCompilerOptions = "", ""
		ireq.DbtProdTarget, ireq.Unset = settings.DbtProdTarget, unsetDbtProdTarget(settings, os.Getenv)
		creq.DbtProdTarget, creq.Unset = settings.DbtProdTarget, unsetDbtProdTarget(settings, os.Getenv)
	}
	r.Impact, err = client.ColumnImpact(ctx, ireq, func(p impact.Progress) { pr0.say(progressText("Column impact", p.Phase, p.Done, p.Total)) })
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		r.ImpactError = explainCompile(ctx, client, err).Error()
	}
	r.Cost, err = client.QueryCost(ctx, creq, func(p cost.Progress) { pr0.say(progressText("Query cost", p.Phase, p.Done, p.Total)) })
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		r.CostError = explainCompile(ctx, client, err).Error()
	}
	pr0.done()

	if r.ImpactError != "" {
		fmt.Fprintln(os.Stderr, "xf check: column impact couldn't be checked:", r.ImpactError)
	} else if hint := impact.UnsetHint(r.Impact); hint != "" {
		fmt.Fprintln(os.Stderr, "xf check:", strings.ReplaceAll(hint, "`", ""))
	}
	if r.CostError != "" {
		fmt.Fprintln(os.Stderr, "xf check: query cost couldn't be checked:", r.CostError)
	} else if r.Cost.BaseError != "" {
		fmt.Fprintln(os.Stderr, "xf check: no change in cost against the merge-base is shown, as it could not be compiled:", r.Cost.BaseError)
	}

	if !r.Neither() {
		if err := writeCheck(os.Stdout, cf.output, r, cf.opts, cf.asJSON); err != nil {
			return 0, err
		}
		notes := check.Notes(r, cf.opts, gitSource{root: root, mergeBase: mergeBase(r)}, prefix)
		if check.GitHubActions(os.Getenv) {
			fmt.Fprint(os.Stderr, check.GitHub(notes))
		}
		if cf.codequality != "" {
			data, err := check.CodeQuality(notes)
			if err == nil {
				err = writeFile(cf.codequality, data)
			}
			if err != nil {
				return 0, fmt.Errorf("--codequality: %w", err)
			}
		}
	}

	if commenting {
		commit := cmp.Or(pr.Commit, gitHead(root))
		if err := postComment(ctx, pr, r, cf.opts, commit); err != nil {
			return 0, fmt.Errorf("--comment: %w", err)
		}
	}
	switch {
	case r.Neither():
		return 0, errors.New("neither check could be made")
	case tokenErr != nil:
		return 0, fmt.Errorf("--comment: the checks ran, but %w", tokenErr)
	}
	return r.Code(cf.opts), nil
}

// progressText is a check's progress, for the progress line.
func progressText(check, phase string, done, total int) string {
	if total == 0 {
		return check + ": " + phase
	}
	return fmt.Sprintf("%s: %s %d/%d", check, phase, done, total)
}

// postComment posts, edits or leaves the project's comment on pr.
func postComment(ctx context.Context, pr *check.PR, r *check.Report, o check.Options, commit string) error {
	cs := forge.NewComments(pr.Repo, pr.Number, pr.Token, nil)
	marker := check.Marker(r.Project)
	existing, err := cs.Find(ctx, func(body string) bool { return check.IsOurs(body, marker) })
	if err != nil {
		return err
	}
	prev := ""
	if existing != nil {
		prev = existing.Body
	}
	action, body := check.Plan(r, o, commit, prev)
	switch action {
	case check.Post:
		err = cs.Post(ctx, body)
		fmt.Fprintln(os.Stderr, "xf check: commented on", what(pr))
	case check.Edit:
		err = cs.Edit(ctx, existing.ID, body)
		fmt.Fprintln(os.Stderr, "xf check: edited the comment on", what(pr))
	default:
		fmt.Fprintln(os.Stderr, "xf check: nothing to comment on", what(pr))
	}
	return err
}

func what(pr *check.PR) string {
	if pr.Repo.Kind == forge.GitHub {
		return fmt.Sprintf("pull request #%d", pr.Number)
	}
	return fmt.Sprintf("merge request !%d", pr.Number)
}

// writeCheck writes the report as Markdown, or with asJSON as JSON: to
// output when given, else to w.
func writeCheck(w io.Writer, output string, r *check.Report, o check.Options, asJSON bool) error {
	data := []byte(check.Markdown(r, o))
	if asJSON {
		var err error
		if data, err = check.JSON(r, o); err != nil {
			return err
		}
	}
	if output != "" && output != "-" {
		return writeFile(output, data)
	}
	_, err := w.Write(data)
	return err
}

// writeFile writes data to path, making its folders.
func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// mergeBase is the merge-base a report compared with.
func mergeBase(r *check.Report) string {
	if r.Impact != nil && r.Impact.Comparison != nil {
		return r.Impact.Comparison.MergeBase
	}
	if r.Cost != nil && r.Cost.Comparison != nil {
		return r.Cost.Comparison.MergeBase
	}
	return ""
}

// gitPrefix is root's folder from its repository's root, "" at the root.
func gitPrefix(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
}

// gitHead is the commit checked out at root, "" when unknown.
func gitHead(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitSource is check.Source for the project at root: its files on disk and
// their diff against mergeBase.
type gitSource struct{ root, mergeBase string }

func (s gitSource) Text(file string) string {
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(file)))
	if err != nil {
		return ""
	}
	return string(data)
}

func (s gitSource) Diff(file string) string {
	if s.mergeBase == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", s.root, "diff", "-U0", "--no-color", "--no-ext-diff", s.mergeBase, "--", file).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
