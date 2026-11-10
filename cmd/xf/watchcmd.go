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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
)

// xf watch follows a Dataform workflow invocation xf did not start (a
// scheduled one, a colleague's, one started from the console), as xf web's
// "Run: watch a workflow" does, and prints what each action's BigQuery job
// cost.

// Exit codes of xf watch.
const (
	watchOK          = 0
	watchFailed      = 1 // the workflow failed or was cancelled
	watchCouldntRead = 4 // a bad ID or URL, no credentials, an API error
)

const watchSummary = `Follows a Dataform workflow invocation, whoever started it (a schedule, a
colleague, the Cloud console), until it ends, then prints its jobs table:
each action's state and time, and the slot time, bytes billed and on-demand
cost of its BigQuery job. A workflow that has ended is read once.

The workflow is its ID (1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee), the
URL of its page in the Cloud console, or its resource name. A bare ID is in
the project's Dataform repository (dataformRepo, else the origin remote's
name) in dataformLocation, else defaultLocation; a URL names its own.

The log goes to stderr as the workflow goes; the table (or --json) to stdout.
ctrl+c stops watching and prints the table so far: it never cancels the
workflow. In a Dataform project the workflow is kept in xf's run history, as
xf web's palette keeps it.

Exits 0 when the workflow succeeded, 1 when it failed or was cancelled, 4
when xf couldn't read it, 130 when watching was stopped.`

func runWatch(args []string) int {
	fs := flag.NewFlagSet("xf watch", flag.ContinueOnError)
	dir := fs.String("dir", "", "the project's root `dir` (default: the working directory)")
	asJSON := fs.Bool("json", false, "print the run's report as JSON: each action's job, with its figures as numbers")
	sf := addSettingFlags(fs, config.KeyDataformRepo, config.KeyDataformLocation)
	commandUsage(fs, "xf watch [flags] <workflow ID or URL>", watchSummary)
	// Flags may come after the workflow too.
	var input []string
	for {
		help, err := parseArgs(fs, args)
		if help {
			return watchOK
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "xf watch:", err)
			return watchCouldntRead
		}
		if fs.NArg() == 0 {
			break
		}
		input = append(input, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(input) != 1 {
		fmt.Fprintln(os.Stderr, "xf watch: give one workflow: its ID or the URL of its page in the console (see xf watch --help)")
		return watchCouldntRead
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := watchWorkflow(ctx, input[0], *dir, sf, *asJSON, os.Stdout, os.Stderr)
	if err != nil {
		if errors.Is(err, dataformapi.ErrAuth) {
			err = fmt.Errorf("%w: to log in, %s", err, dataformapi.AuthHint)
		}
		fmt.Fprintln(os.Stderr, "xf watch:", err)
		return watchCouldntRead
	}
	return code
}

// watchWorkflow watches the workflow input names, from the project in dir
// if there is one there.
func watchWorkflow(ctx context.Context, input, dir string, sf *settingFlags, asJSON bool, out, log io.Writer) (int, error) {
	ref, err := dataformapi.ParseInvocation(input)
	if err != nil {
		return 0, err
	}
	root, err := watchRoot(dir)
	if err != nil {
		return 0, err
	}
	settings, err := sf.load(root)
	if err != nil {
		return 0, err
	}
	for _, w := range settings.Warnings {
		fmt.Fprintln(log, "xf:", w)
	}
	var base dataformapi.Repository
	base.Project, base.Location = workflowDefaults(root)
	watch := workflowWatcher(root, settings)
	if d := sessionDir(); d != "" && root != "" {
		store := &runhistory.Store{Dir: filepath.Join(filepath.Dir(d), "runs")}
		watch = keepWatched(watch, func(e runhistory.Entry, run *project.Run) *project.Run { return store.Record(root, e, run) })
	}
	return followWorkflow(ctx, ref, base, watch, asJSON, out, log)
}

// followWorkflow watches ref with watch until it ends or ctx is done, the
// log to log, then writes its report to out.
func followWorkflow(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository, watch watchFunc, asJSON bool, out, log io.Writer) (int, error) {
	run, _, err := watch(ctx, ref, base)
	if err != nil {
		if ctx.Err() != nil {
			return runInterrupted, nil
		}
		return 0, err
	}
	for line := range run.Lines {
		fmt.Fprintln(log, line)
	}
	res := <-run.Done
	var rep *project.RunReport
	if run.Report != nil {
		rep = run.Report()
	}
	if rep == nil {
		rep = &project.RunReport{}
	}
	if asJSON {
		err = writeWatchJSON(out, ref.ID, rep)
	} else {
		err = writeJobsTable(out, rep, time.Now())
	}
	if err != nil {
		return 0, err
	}
	if n := unreadJobs(rep); n > 0 {
		fmt.Fprintf(log, "xf: couldn't read %s, so %s cost is unknown (BigQuery credentials?)\n", plural(n, "BigQuery job"), map[bool]string{true: "its", false: "their"}[n == 1])
	}
	switch {
	case res.Detached:
		fmt.Fprintln(log, "xf: stopped watching; the workflow goes on")
		return runInterrupted, nil
	case res.Err != nil:
		return 0, res.Err
	case res.Canceled || res.ExitCode != 0:
		return watchFailed, nil
	}
	return watchOK, nil
}

// watchRoot is the Dataform project in dir (the working directory when
// ""), or "" when there is none: a workflow's URL needs none. A dir given
// must be a Dataform project.
func watchRoot(dir string) (string, error) {
	given := dir != ""
	if !given {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	name, err := backend.Detect(root, backend.Dataform)
	switch {
	case err == nil && name == backend.Dataform:
		return root, nil
	case given:
		return "", fmt.Errorf("%s is not a Dataform project: Dataform workflows are watched from one, or by their URL", dir)
	}
	return "", nil
}

// workflowDefaults are the project's defaultProject and defaultLocation, of
// workflow_settings.yaml or (defaultDatabase) dataform.json; "" for none.
func workflowDefaults(root string) (projectID, location string) {
	if root == "" {
		return "", ""
	}
	if p, l := settingsValue(root, "defaultProject"), settingsValue(root, "defaultLocation"); p != "" || l != "" {
		return p, l
	}
	data, err := os.ReadFile(filepath.Join(root, "dataform.json"))
	if err != nil {
		return "", ""
	}
	var c struct {
		DefaultDatabase string `json:"defaultDatabase"`
		DefaultLocation string `json:"defaultLocation"`
	}
	_ = json.Unmarshal(data, &c)
	return c.DefaultDatabase, c.DefaultLocation
}

// unreadJobs counts the actions that ran a BigQuery job whose figures
// couldn't be read.
func unreadJobs(r *project.RunReport) int {
	n := 0
	for _, a := range r.Actions {
		if a.JobID != "" && a.Job == nil && a.Done() {
			n++
		}
	}
	return n
}

// writeWatchJSON writes the workflow's report, with the sums of its jobs.
func writeWatchJSON(w io.Writer, id string, r *project.RunReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		ID string `json:"id"`
		*project.RunReport
		Totals project.RunTotals `json:"totals"`
	}{id, r, r.Totals()})
}

// writeJobsTable writes r as xf web's jobs table shows it: a summary line,
// then a row per action and the totals.
func writeJobsTable(w io.Writer, r *project.RunReport, now time.Time) error {
	s := frontend.SummarizeJobs(r, now)
	head := []string{cmp.Or(strings.ToLower(r.State), "unknown"), plural(s.Actions, "action")}
	for _, c := range []struct {
		n    int
		what string
	}{{s.Succeeded, "succeeded"}, {s.Failed, "failed"}, {s.Running, "running"}, {s.Pending, "pending"}, {s.Skipped, "skipped"}} {
		if c.n > 0 {
			head = append(head, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	if s.Elapsed != "" {
		head = append(head, s.Elapsed)
	}
	if _, err := fmt.Fprintln(w, strings.Join(head, " · ")); err != nil {
		return err
	}
	if len(r.Actions) == 0 {
		return nil
	}
	rows, total := frontend.JobsTable(r, now)
	cells := [][]string{{"ACTION", "STATE", "TIME", "SLOT TIME", "BILLED", "COST"}}
	for _, row := range rows {
		cells = append(cells, []string{row.Target, row.State, row.Duration, row.Slot, row.Billed, row.Cost})
	}
	cells = append(cells, []string{total.Target, "", total.Duration, total.Slot, total.Billed, total.Cost})
	widths := make([]int, len(cells[0]))
	for _, row := range cells {
		for i, c := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, row := range cells {
		var line strings.Builder
		for i, c := range row {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
			if i > 0 {
				line.WriteString("  ")
			}
			if i < 2 { // names to the left, figures to the right
				line.WriteString(c + pad)
			} else {
				line.WriteString(pad + c)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
