package dataformapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeJobs answers job lookups from jobs, and fails the rest.
type fakeJobs struct {
	mu    sync.Mutex
	jobs  map[string]bigquery.Job
	asked []string // "project/location/id"
	// onlyIn, when set, is the one location the jobs are found in.
	onlyIn string
}

func (f *fakeJobs) Job(_ context.Context, proj, location, id string) (*bigquery.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, proj+"/"+location+"/"+id)
	if j, ok := f.jobs[id]; ok && (f.onlyIn == "" || location == f.onlyIn) {
		return &j, nil
	}
	return nil, errors.New("Not found: Job " + id)
}

func TestParseJobID(t *testing.T) {
	if got, want := ParseJobID("job_1", "proj"), (JobRef{Project: "proj", ID: "job_1"}); got != want {
		t.Errorf("bare: %+v", got)
	}
	got := ParseJobID("other-proj:europe-west2.job_1.x", "proj")
	if want := (JobRef{Project: "other-proj", Location: "europe-west2", ID: "job_1.x"}); got != want {
		t.Errorf("qualified: %+v", got)
	}
	if u := got.ConsoleURL(); u != "https://console.cloud.google.com/bigquery?project=other-proj&j=bq:europe-west2:job_1.x&page=queryresults" {
		t.Errorf("URL %s", u)
	}
	if u := (JobRef{Project: "proj", ID: "job_1"}).ConsoleURL(); !strings.Contains(u, "j=bq:US:job_1") {
		t.Errorf("URL with no location %s", u)
	}
}

// TestRunReport: the report has every action with what its BigQuery job
// did, each job looked up once it has finished and only once.
func TestRunReport(t *testing.T) {
	withJob := func(a Action, id string) Action {
		a.BigQuery.JobID = id
		return a
	}
	timed := func(a Action, start, end string) Action {
		a.Timing = Timing{StartTime: start, EndTime: end}
		return a
	}
	failed := timed(withJob(act("b", "FAILED"), "proj:US.job_b"), "2026-10-05T10:00:10Z", "2026-10-05T10:00:12Z")
	failed.FailureReason = "Not found: Table proj:s.missing"
	done := timed(withJob(act("a", "SUCCEEDED"), "job_a"), "2026-10-05T10:00:00Z", "2026-10-05T10:00:09Z")
	api := &fakeAPI{polls: []poll{
		{state: StateRunning, actions: []Action{act("c", "PENDING"), withJob(act("a", "RUNNING"), "job_a"), act("b", "PENDING")}},
		{state: StateRunning, actions: []Action{act("c", "PENDING"), done, withJob(act("b", "RUNNING"), "proj:US.job_b")}},
		{state: StateFailed, actions: []Action{act("c", "SKIPPED"), done, failed}},
	}}
	start := time.Date(2026, 10, 5, 10, 0, 1, 0, time.UTC)
	jobs := &fakeJobs{jobs: map[string]bigquery.Job{
		"job_a": {Location: "EU", BytesBilled: 1 << 40, BytesProcessed: 1 << 39, SlotMs: 64000, Started: start, Ended: start.Add(3 * time.Second)},
	}}
	opts := testOpts
	opts.Jobs, opts.JobLocation = jobs, "EU"
	r := Start(context.Background(), api, opts)

	// Each line follows the report it tells of.
	var atRunning *project.RunReport
	timeout := time.After(5 * time.Second)
read:
	for {
		select {
		case l, ok := <-r.Lines:
			if !ok {
				break read
			}
			if l == "▶ running s.a" {
				atRunning = r.Report()
			}
		case <-timeout:
			t.Fatal("run did not finish")
		}
	}
	<-r.Done
	if atRunning == nil || len(atRunning.Actions) != 3 || atRunning.Actions[0].State != "RUNNING" || atRunning.Actions[0].Job != nil {
		t.Errorf("report at the first line: %+v", atRunning)
	}

	rep := r.Report()
	if rep.State != StateFailed || !strings.HasSuffix(rep.URL, "/workflows/42?project=proj") || len(rep.Actions) != 3 {
		t.Fatalf("report %+v", rep)
	}
	a, b, c := rep.Actions[0], rep.Actions[1], rep.Actions[2]
	wantJob := project.JobStats{Location: "EU", BytesBilled: 1 << 40, BytesProcessed: 1 << 39, CostUSD: 6.25, SlotMs: 64000, Started: start, Ended: start.Add(3 * time.Second)}
	if a.Target.Name != "a" || a.State != "SUCCEEDED" || a.Job == nil || *a.Job != wantJob || !strings.Contains(a.JobURL, "j=bq:EU:job_a") {
		t.Errorf("a = %+v, job %+v", a, a.Job)
	}
	if d, ok := a.Duration(); !ok || d != 3*time.Second {
		t.Errorf("a took %v, want its job's 3s", d)
	}
	if b.Target.Name != "b" || b.Job != nil || b.JobErr != "Not found: Job job_b" || b.FailureReason == "" || !strings.Contains(b.JobURL, "j=bq:US:job_b") {
		t.Errorf("b = %+v", b)
	}
	if d, ok := b.Duration(); !ok || d != 2*time.Second {
		t.Errorf("b took %v, want its own 2s", d)
	}
	if c.Target.Name != "c" || c.State != "SKIPPED" || c.JobID != "" || c.JobURL != "" {
		t.Errorf("c = %+v", c)
	}
	if got, want := rep.Totals(), (project.RunTotals{Jobs: 1, BytesBilled: 1 << 40, BytesProcessed: 1 << 39, CostUSD: 6.25, SlotMs: 64000}); got != want {
		t.Errorf("totals %+v, want %+v", got, want)
	}
	// A job with no location in its ID is asked for in the project's.
	if want := []string{"proj/EU/job_a", "proj/US/job_b"}; !reflect.DeepEqual(jobs.asked, want) {
		t.Errorf("looked up %v, want %v", jobs.asked, want)
	}
	// It travels as JSON.
	data, err := json.Marshal(rep)
	if err != nil || !strings.Contains(string(data), `"bytesBilled":1099511627776`) || !strings.Contains(string(data), `"costUsd":6.25`) {
		t.Errorf("JSON %s, %v", data, err)
	}
}

// TestRunReportWithoutJobReader: with no BigQuery client the report still
// has the actions.
func TestRunReportWithoutJobReader(t *testing.T) {
	a := act("a", "SUCCEEDED")
	a.BigQuery.JobID = "job_a"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, actions: []Action{a}}}}
	r := Start(context.Background(), api, testOpts)
	collect(t, r)
	rep := r.Report()
	if rep == nil || len(rep.Actions) != 1 || rep.Actions[0].Job != nil || rep.Actions[0].JobErr != "" || rep.Actions[0].JobID != "job_a" {
		t.Errorf("report %+v", rep)
	}
}
