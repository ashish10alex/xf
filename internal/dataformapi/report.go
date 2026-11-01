package dataformapi

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// A run's report (project.RunReport) is its actions and what the BigQuery
// job of each did: bytes billed, slot time and timing, as the extension's
// workflow run table shows. It is built here, apart from any UI, so that
// whatever starts a run can show it.

// JobReader looks up what a BigQuery job did. *bigquery.Client implements
// it; tests pass fakes.
type JobReader interface {
	Job(ctx context.Context, project, location, jobID string) (*bigquery.Job, error)
}

const (
	// jobConcurrency is how many jobs are looked up at once.
	jobConcurrency = 5
	// jobTimeout is how long one lookup may take: it holds up the poll
	// that waits for it.
	jobTimeout = 15 * time.Second
)

// JobRef names a BigQuery job.
type JobRef struct {
	Project, Location, ID string
}

var qualifiedJobID = regexp.MustCompile(`^([^:]+):([^.]+)\.(.+)$`)

// ParseJobID reads a job ID as Dataform reports it: bare, a job of
// defaultProject, or fully qualified as project:location.id.
func ParseJobID(raw, defaultProject string) JobRef {
	if m := qualifiedJobID.FindStringSubmatch(raw); m != nil {
		return JobRef{Project: m[1], Location: m[2], ID: m[3]}
	}
	return JobRef{Project: defaultProject, ID: raw}
}

// ConsoleURL is the job's page in the Cloud console.
func (j JobRef) ConsoleURL() string {
	return fmt.Sprintf("https://console.cloud.google.com/bigquery?project=%s&j=bq:%s:%s&page=queryresults",
		url.QueryEscape(j.Project), url.QueryEscape(cmp.Or(j.Location, "US")), url.QueryEscape(j.ID))
}

// jobLookup is how looking up a job went.
type jobLookup struct {
	stats *project.JobStats
	err   string
}

// pendingJobs are the IDs of the jobs lookUpJobs would look up now.
func (r *runner) pendingJobs(actions []Action) []string {
	if r.opts.Jobs == nil {
		return nil
	}
	var pending []string
	for _, a := range actions {
		id := a.BigQuery.JobID
		if _, done := r.jobs[id]; id == "" || done || (a.State != "SUCCEEDED" && a.State != "FAILED") {
			continue
		}
		pending = append(pending, id)
	}
	return pending
}

func (r *runner) jobsToLookUp(actions []Action) int { return len(r.pendingJobs(actions)) }

// lookUpJobs looks up the BigQuery job of every action that has finished
// one and has not been looked up. Each is tried once: a job that can't be
// read is reported as such.
func (r *runner) lookUpJobs(actions []Action) {
	pending := r.pendingJobs(actions)
	if len(pending) == 0 {
		return
	}
	if r.jobs == nil {
		r.jobs = map[string]jobLookup{}
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		sem = make(chan struct{}, jobConcurrency)
	)
	for _, id := range pending {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.watch, jobTimeout)
			defer cancel()
			ref := r.jobRef(id)
			var l jobLookup
			if j, err := r.readJob(ctx, id, ref); err != nil {
				l.err = err.Error()
			} else {
				l.stats = &project.JobStats{
					Location:       j.Location,
					BytesBilled:    j.BytesBilled,
					BytesProcessed: j.BytesProcessed,
					CostUSD:        j.CostUSD(),
					SlotMs:         j.SlotMs,
					Started:        j.Started,
					Ended:          j.Ended,
				}
			}
			mu.Lock()
			r.jobs[id] = l
			mu.Unlock()
		})
	}
	wg.Wait()
	r.warnUnreadJobs(pending)
}

// warnUnreadJobs says in the log that jobs could not be read, once for the
// batch just looked up: without them the table has no costs, and the
// reason (a wrong location, no access) is not in the table.
func (r *runner) warnUnreadJobs(ids []string) {
	n, first := 0, ""
	for _, id := range ids {
		if l := r.jobs[id]; l.err != "" {
			if n == 0 {
				first = id + ": " + l.err
			}
			n++
		}
	}
	if n == 0 {
		return
	}
	where := cmp.Or(r.opts.JobLocation, "no location")
	if loc := r.opts.Repo.Location; loc != "" && loc != r.opts.JobLocation {
		where += ", " + loc + " and with no location"
	}
	r.emit("⚠ could not read %d BigQuery job%s, so their costs are missing (looked in %s; first: %s)", n, plural(n), where, first)
}

// readJob looks the job up where its ID says, else in the project's job
// location, and if it is not found there in the repository's location and
// then with none: a workflow of another project may have run its jobs
// elsewhere. The first error is the one kept.
func (r *runner) readJob(ctx context.Context, id string, ref JobRef) (*bigquery.Job, error) {
	j, err := r.opts.Jobs.Job(ctx, ref.Project, ref.Location, ref.ID)
	if err == nil || ParseJobID(id, "").Location != "" {
		return j, err
	}
	tried := ref.Location
	for _, loc := range []string{r.opts.Repo.Location, ""} {
		if loc == tried {
			continue
		}
		tried = loc
		if j, e := r.opts.Jobs.Job(ctx, ref.Project, loc, ref.ID); e == nil {
			return j, nil
		}
	}
	return nil, err
}

// jobRef names the job with the ID an action reports, in the run's project
// and the project's job location unless the ID says otherwise.
func (r *runner) jobRef(id string) JobRef {
	ref := ParseJobID(id, r.opts.Repo.Project)
	ref.Location = cmp.Or(ref.Location, r.opts.JobLocation)
	return ref
}

// buildReport is the report of the invocation and its actions as polled,
// with the jobs looked up so far. Actions are in name order.
func (r *runner) buildReport(inv *Invocation, actions []Action) *project.RunReport {
	rep := &project.RunReport{
		State:   inv.State,
		Started: parseTime(inv.Timing.StartTime),
		Ended:   parseTime(inv.Timing.EndTime),
		Actions: make([]project.RunAction, 0, len(actions)),
	}
	if u := r.url.Load(); u != nil {
		rep.URL = *u
	}
	for _, a := range actions {
		ra := project.RunAction{
			Target:        project.Target{Database: a.Target.Database, Schema: a.Target.Schema, Name: a.Target.Name},
			State:         a.State,
			FailureReason: a.FailureReason,
			Started:       parseTime(a.Timing.StartTime),
			Ended:         parseTime(a.Timing.EndTime),
			JobID:         a.BigQuery.JobID,
		}
		if ra.JobID != "" {
			l := r.jobs[ra.JobID]
			ra.Job, ra.JobErr = l.stats, l.err
			ref := r.jobRef(ra.JobID)
			if l.stats != nil {
				ref.Location = cmp.Or(l.stats.Location, ref.Location)
			}
			ra.JobURL = ref.ConsoleURL()
		}
		rep.Actions = append(rep.Actions, ra)
	}
	slices.SortStableFunc(rep.Actions, func(x, y project.RunAction) int {
		return cmp.Or(cmp.Compare(x.Target.Schema, y.Target.Schema), cmp.Compare(x.Target.Name, y.Target.Name))
	})
	return rep
}

// parseTime reads an API timestamp; the zero time for "" or one it can't.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
