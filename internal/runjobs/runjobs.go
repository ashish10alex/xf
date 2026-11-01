// Package runjobs follows the BigQuery jobs of a `dataform run`, so that a
// CLI run has the report (project.RunReport) an API run has: a row for each
// job with what it cost, and for each job still running.
//
// The CLI names an action's jobs only once the action has ended, in the
// line after the one that says how it went. Until then a job is found by
// its ID: xf has the CLI prefix the IDs of a run's jobs (--job-prefix), and
// lists the caller's running jobs for the ones that have it.
package runjobs

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/project"
)

// JobReader looks up what a BigQuery job did. *bigquery.Client implements
// it; tests pass fakes.
type JobReader interface {
	Job(ctx context.Context, project, location, jobID string) (*bigquery.Job, error)
}

// JobLister lists the caller's jobs that have not ended. *bigquery.Client
// implements it; tests pass fakes.
type JobLister interface {
	ActiveJobs(ctx context.Context, project string, since time.Time) ([]bigquery.ActiveJob, error)
}

// Options say where a run's jobs are and how to follow them.
type Options struct {
	// Project and Location are where the CLI runs its jobs.
	Project, Location string
	// Prefix is what the IDs of the run's jobs start with, "" when the CLI
	// was not told to prefix them: then no job is shown until it has ended.
	Prefix string
	// Jobs looks up what the jobs did; nil leaves the report without their
	// stats.
	Jobs JobReader
	// Active lists the jobs still running; nil shows none until it ended.
	Active JobLister
	// Match names the action whose job runs query, when it can tell.
	Match func(query string) (project.Target, bool)
	// PollInterval is how often the running jobs are listed; 0 means 2s.
	PollInterval time.Duration
}

// DefaultPollInterval is how often the running jobs are listed by default.
const DefaultPollInterval = 2 * time.Second

const (
	// jobConcurrency is how many jobs are looked up at once.
	jobConcurrency = 5
	// jobTimeout is how long one lookup or listing may take.
	jobTimeout = 15 * time.Second
	// clockSkew is how far BigQuery's clock may be behind this one.
	clockSkew = time.Minute
)

// Prefix is what the CLI starts the IDs of a run's jobs with when it is
// given --job-prefix flag.
func Prefix(flag string) string { return "dataform-" + flag + "-" }

// Track returns run with a report of its BigQuery jobs. Its lines and
// result are run's own, and a line more should the running jobs turn out
// not to be listable.
func Track(run *project.Run, opts Options) *project.Run {
	lines := make(chan string)
	done := make(chan project.RunResult, 1)
	ctx, stop := context.WithCancel(context.Background())
	t := &tracker{opts: opts, started: time.Now(), last: -1, lines: lines, confirmed: map[string]bool{}, sem: make(chan struct{}, jobConcurrency)}
	t.publish("RUNNING")

	polled := make(chan struct{})
	go func() {
		defer close(polled)
		t.poll(ctx)
	}()
	go func() {
		for l := range run.Lines {
			t.line(ctx, l)
			lines <- l
		}
		res := <-run.Done
		stop()
		<-polled
		t.lookups.Wait()
		t.finish(res)
		close(lines)
		done <- res
	}()

	out := *run
	out.Lines, out.Done, out.Report = lines, done, t.report.Load
	out.Kill = func() {
		stop()
		run.Kill()
	}
	return &out
}

type tracker struct {
	opts    Options
	started time.Time
	lines   chan<- string
	sem     chan struct{}
	lookups sync.WaitGroup
	// report is the run's report as of now; each is published whole and
	// never changed.
	report atomic.Pointer[project.RunReport]

	mu sync.Mutex
	// rows are the actions the CLI has told of, in its order: one for each
	// job of an action, or one for an action that ran none.
	rows []project.RunAction
	// last is the row of the action the CLI told of in its last line, whose
	// jobs the next line may name; -1 when that line was no action's.
	last int
	// running are the rows of the jobs still running.
	running []project.RunAction
	// confirmed are the IDs of the jobs the CLI has named.
	confirmed map[string]bool
	state     string
	ended     time.Time
}

// The CLI says how each action went ("Table created:  analytics.orders
// [table]") and, in the next line, which jobs it ran.
var (
	actionLine = regexp.MustCompile(`^\s*(Table created|Property graph created|Operation completed successfully|Assertion passed|` +
		`Dataset creation failed|Property graph creation failed|Operation failed|Assertion failed|` +
		`Skipping dataset creation|Skipping operation execution|Skipping assertion execution|` +
		`Dataset creation disabled|Property graph creation disabled|Operation execution disabled|Assertion execution disabled):\s+([^\s.]+)\.(\S+)`)
	jobLine = regexp.MustCompile(`^\s*jobId:\s*(.+?),?\s*$`)
)

// actionState is the state an action's line tells of.
func actionState(said string) string {
	switch {
	case strings.Contains(said, "failed"):
		return "FAILED"
	case strings.HasPrefix(said, "Skipping"):
		return "SKIPPED"
	case strings.Contains(said, "disabled"):
		return "DISABLED"
	}
	return "SUCCEEDED"
}

// line takes a line of the run's output.
func (t *tracker) line(ctx context.Context, l string) {
	l = ansi.Strip(l) // a run with Color keeps its colour codes
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := actionLine.FindStringSubmatch(l); m != nil {
		t.rows = append(t.rows, project.RunAction{
			Target: project.Target{Database: t.opts.Project, Schema: m[2], Name: m[3]},
			State:  actionState(m[1]),
		})
		t.last = len(t.rows) - 1
		t.publishLocked()
		return
	}
	m := jobLine.FindStringSubmatch(l)
	if m == nil || t.last < 0 {
		if strings.TrimSpace(l) != "" {
			t.last = -1
		}
		return
	}
	action := t.rows[t.last]
	var rows []project.RunAction
	for id := range strings.SplitSeq(m[1], ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		row := action
		row.JobID, row.JobURL = id, t.jobRef(id, "").ConsoleURL()
		rows = append(rows, row)
		t.confirmed[id] = true
		t.lookUp(ctx, id)
	}
	if len(rows) > 0 {
		t.rows = slices.Replace(t.rows, t.last, t.last+1, rows...)
		t.running = slices.DeleteFunc(t.running, func(r project.RunAction) bool { return t.confirmed[r.JobID] })
	}
	t.last = -1
	t.publishLocked()
}

// jobRef names the job with the ID the CLI gave, where the run's jobs are
// unless location says otherwise.
func (t *tracker) jobRef(id, location string) dataformapi.JobRef {
	ref := dataformapi.ParseJobID(id, t.opts.Project)
	ref.Location = cmp.Or(location, ref.Location, t.opts.Location)
	return ref
}

// lookUp looks up what the job did, once, and puts it in the job's row.
func (t *tracker) lookUp(ctx context.Context, id string) {
	if t.opts.Jobs == nil {
		return
	}
	t.lookups.Go(func() {
		t.sem <- struct{}{}
		defer func() { <-t.sem }()
		// The lookup outlives the run's context: its last jobs are named
		// as it ends.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobTimeout)
		defer cancel()
		ref := t.jobRef(id, "")
		j, err := t.opts.Jobs.Job(ctx, ref.Project, ref.Location, ref.ID)

		t.mu.Lock()
		defer t.mu.Unlock()
		i := slices.IndexFunc(t.rows, func(r project.RunAction) bool { return r.JobID == id })
		if i < 0 {
			return
		}
		// A row is replaced whole: published reports share the old one.
		row := t.rows[i]
		if err != nil {
			row.JobErr = err.Error()
		} else {
			row.Job = Stats(j)
			row.JobURL = t.jobRef(id, j.Location).ConsoleURL()
		}
		t.rows[i] = row
		t.publishLocked()
	})
}

// Stats is what a looked-up job did, as a report has it.
func Stats(j *bigquery.Job) *project.JobStats {
	return &project.JobStats{
		Location:       j.Location,
		BytesBilled:    j.BytesBilled,
		BytesProcessed: j.BytesProcessed,
		CostUSD:        j.CostUSD(),
		SlotMs:         j.SlotMs,
		Started:        j.Started,
		Ended:          j.Ended,
	}
}

// poll lists the run's running jobs until ctx ends, or they can't be
// listed: the run's lines then say so, once, and its jobs are shown as each
// action ends.
func (t *tracker) poll(ctx context.Context) {
	if t.opts.Active == nil || t.opts.Prefix == "" {
		return
	}
	tick := time.NewTicker(cmp.Or(t.opts.PollInterval, DefaultPollInterval))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		lctx, cancel := context.WithTimeout(ctx, jobTimeout)
		jobs, err := t.opts.Active.ActiveJobs(lctx, t.opts.Project, t.started.Add(-clockSkew))
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			select {
			case t.lines <- "xf: can't list the running BigQuery jobs (" + err.Error() + "); each is shown once its action ends":
			case <-ctx.Done():
			}
			return
		}
		t.setRunning(jobs)
	}
}

// setRunning takes the caller's running jobs, of which the run's are those
// with its prefix that the CLI has not named yet.
func (t *tracker) setRunning(jobs []bigquery.ActiveJob) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var rows []project.RunAction
	for _, j := range jobs {
		if !strings.HasPrefix(j.ID, t.opts.Prefix) || t.confirmed[j.ID] {
			continue
		}
		row := project.RunAction{
			// A job no action can be told for goes by the end of its ID.
			Target:  project.Target{Name: "job …" + strings.TrimPrefix(j.ID, t.opts.Prefix)},
			State:   "RUNNING",
			Started: j.Started,
			JobID:   j.ID,
			JobURL:  t.jobRef(j.ID, j.Location).ConsoleURL(),
		}
		if t.opts.Match != nil {
			if target, ok := t.opts.Match(j.Query); ok {
				row.Target = target
			}
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(x, y project.RunAction) int { return x.Started.Compare(y.Started) })
	t.running = rows
	t.publishLocked()
}

// finish takes the run's result: nothing of it is running now.
func (t *tracker) finish(res project.RunResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running, t.ended = nil, t.started.Add(res.Duration)
	switch {
	case res.Canceled:
		t.state = "CANCELLED"
	case res.ExitCode == 0 && res.Err == nil:
		t.state = "SUCCEEDED"
	default:
		t.state = "FAILED"
	}
	t.publishLocked()
}

func (t *tracker) publish(state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = state
	t.publishLocked()
}

func (t *tracker) publishLocked() {
	t.report.Store(&project.RunReport{
		State:   t.state,
		Started: t.started,
		Ended:   t.ended,
		Actions: slices.Concat(t.rows, t.running),
	})
}

// A job's query tells its action when it opens by writing to the action's
// table: "create or replace table `p.d.t` as …", "insert into `p.d.t` …".
var writesTo = regexp.MustCompile("(?is)^(?:\\s+|--[^\\n]*\\n|/\\*.*?\\*/)*(?:create|insert|merge|truncate|delete|update)\\b[a-z\\s]*`([^`]+)`")

// Matcher names the action, of those with targets, whose job runs a query:
// the one the query opens by writing to. It is a guess, good until the CLI
// names the action's jobs.
func Matcher(targets []project.Target) func(query string) (project.Target, bool) {
	byName := make(map[string]project.Target, 2*len(targets))
	for _, t := range targets {
		byName[t.Schema+"."+t.Name] = t
		byName[t.Database+"."+t.Schema+"."+t.Name] = t
	}
	return func(query string) (project.Target, bool) {
		m := writesTo.FindStringSubmatch(query)
		if m == nil {
			return project.Target{}, false
		}
		t, ok := byName[m[1]]
		return t, ok
	}
}
