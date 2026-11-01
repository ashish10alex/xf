package runjobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

type fakeJobs struct {
	mu     sync.Mutex
	asked  []string
	active [][]bigquery.ActiveJob // one listing per call; the last repeats
	calls  int
	err    error
	listed chan struct{}
}

func (f *fakeJobs) Job(_ context.Context, project, location, id string) (*bigquery.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, project+":"+location+"."+id)
	if strings.HasSuffix(id, "-gone") {
		return nil, errors.New("Not found: Job")
	}
	return &bigquery.Job{Location: "europe-west2", BytesBilled: 10 << 20, SlotMs: 1500, Done: true}, nil
}

func (f *fakeJobs) ActiveJobs(context.Context, string, time.Time) ([]bigquery.ActiveJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		select {
		case f.listed <- struct{}{}:
		default:
		}
	}()
	if f.err != nil {
		return nil, f.err
	}
	if len(f.active) == 0 {
		return nil, nil
	}
	l := f.active[min(f.calls, len(f.active)-1)]
	f.calls++
	return l, nil
}

// start is a run whose lines the test sends.
func start() (*project.Run, chan<- string, chan<- project.RunResult) {
	lines, done := make(chan string), make(chan project.RunResult, 1)
	return &project.Run{Cmdline: "dataform run", Lines: lines, Done: done, Kill: func() {}}, lines, done
}

// The CLI's own lines, as `dataform run` 3.0.71 prints them.
const (
	created   = "Table created:  analytics.orders [table] "
	createdBy = " \t jobId: dataform-xf-ab12-1, dataform-xf-ab12-2,"
	billed    = " \t Bytes billed: 10.00 MiB, 0 B"
	failed    = "Dataset creation failed:  analytics.daily [table] "
	failedBy  = " \t jobId: dataform-xf-ab12-gone"
	skipped   = "Skipping assertion execution:  analytics_assertions.daily_not_null"
	disabled  = "Operation execution disabled:  analytics.cleanup [disabled]"
)

// TestTrackNamesJobsFromTheCLI: each action the CLI tells of is a row for
// each of its jobs, with what the job did, and a row of its own when it ran
// none; the run's lines come through as they were.
func TestTrackNamesJobsFromTheCLI(t *testing.T) {
	fj := &fakeJobs{}
	run, lines, done := start()
	tr := Track(run, Options{Project: "demo", Location: "EU", Jobs: fj})
	sent := []string{"Running...", "", created, createdBy, billed, failed, failedBy, "  some error", skipped, disabled}
	go func() {
		for _, l := range sent {
			lines <- l
		}
		close(lines)
		done <- project.RunResult{ExitCode: 1, Duration: 3 * time.Second}
	}()
	var got []string
	for l := range tr.Lines {
		got = append(got, l)
	}
	if res := <-tr.Done; res.ExitCode != 1 {
		t.Errorf("result %+v", res)
	}
	if strings.Join(got, "\n") != strings.Join(sent, "\n") {
		t.Errorf("lines %q", got)
	}
	r := tr.Report()
	if r.State != "FAILED" || r.Ended.Sub(r.Started) != 3*time.Second || len(r.Actions) != 5 {
		t.Fatalf("report %+v", r)
	}
	type row struct{ name, state, job string }
	want := []row{{"orders", "SUCCEEDED", "dataform-xf-ab12-1"}, {"orders", "SUCCEEDED", "dataform-xf-ab12-2"},
		{"daily", "FAILED", "dataform-xf-ab12-gone"}, {"daily_not_null", "SKIPPED", ""}, {"cleanup", "DISABLED", ""}}
	for i, a := range r.Actions {
		if got := (row{a.Target.Name, a.State, a.JobID}); got != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, got, want[i])
		}
	}
	a := r.Actions[0]
	if a.Target.Schema != "analytics" || a.Job == nil || a.Job.BytesBilled != 10<<20 || !strings.Contains(a.JobURL, "j=bq:europe-west2:dataform-xf-ab12-1") {
		t.Errorf("first job %+v %+v", a, a.Job)
	}
	if a := r.Actions[2]; a.Job != nil || a.JobErr != "Not found: Job" || !strings.Contains(a.JobURL, "j=bq:EU:") {
		t.Errorf("job that can't be read: %+v", a)
	}
	if tot := r.Totals(); tot.Jobs != 2 {
		t.Errorf("totals %+v", tot)
	}
	if len(fj.asked) != 3 || !strings.HasPrefix(fj.asked[0], "demo:EU.") {
		t.Errorf("asked for %q", fj.asked)
	}
}

// TestTrackReadsColouredLines: a run asked for colour (RunOptions.Color)
// is read as well, and its lines come through with their colour.
func TestTrackReadsColouredLines(t *testing.T) {
	run, lines, done := start()
	tr := Track(run, Options{Project: "demo", Location: "EU", Jobs: &fakeJobs{}})
	sent := []string{"\x1b[32m" + created + "\x1b[0m", "\x1b[2m" + createdBy + "\x1b[0m"}
	go func() {
		for _, l := range sent {
			lines <- l
		}
		close(lines)
		done <- project.RunResult{Duration: time.Second}
	}()
	var got []string
	for l := range tr.Lines {
		got = append(got, l)
	}
	<-tr.Done
	if strings.Join(got, "\n") != strings.Join(sent, "\n") {
		t.Errorf("lines %q", got)
	}
	if r := tr.Report(); len(r.Actions) != 2 || r.Actions[0].Target.Name != "orders" || r.Actions[1].JobID != "dataform-xf-ab12-2" {
		t.Errorf("report %+v", r)
	}
}

// TestTrackShowsRunningJobs: a job with the run's prefix is a running row,
// under its action when its query tells which, until the CLI names it;
// other people's and other runs' jobs are left out.
func TestTrackShowsRunningJobs(t *testing.T) {
	began := time.Now().Add(-40 * time.Second)
	fj := &fakeJobs{listed: make(chan struct{}, 1), active: [][]bigquery.ActiveJob{{
		{ID: "dataform-xf-ab12-1", Location: "EU", Started: began, Query: "-- built by dataform\ncreate or replace table `demo.analytics.orders` as select 1"},
		{ID: "dataform-xf-ab12-9", Started: began.Add(time.Second), Query: "select * from `demo.analytics.orders`"},
		{ID: "dataform-xf-ffff-1", Query: "create or replace table `demo.analytics.orders` as select 1"},
		{ID: "bquxjob_1"},
	}}}
	run, lines, done := start()
	tr := Track(run, Options{Project: "demo", Location: "EU", Prefix: Prefix("xf-ab12"), Jobs: fj, Active: fj, PollInterval: time.Millisecond,
		Match: Matcher([]project.Target{{Database: "demo", Schema: "analytics", Name: "orders"}})})
	go func() {
		for range tr.Lines {
		}
	}()
	<-fj.listed
	<-fj.listed // the first listing has been taken by now
	r := tr.Report()
	if r.State != "RUNNING" || len(r.Actions) != 2 {
		t.Fatalf("report %+v", r)
	}
	if a := r.Actions[0]; a.Target.Name != "orders" || a.State != "RUNNING" || a.JobID != "dataform-xf-ab12-1" || !a.Started.Equal(began) || a.JobURL == "" {
		t.Errorf("matched job %+v", a)
	}
	if a := r.Actions[1]; a.Target.Name != "job …9" || a.Target.Schema != "" {
		t.Errorf("unmatched job %+v", a)
	}

	// The CLI names the first job: its running row gives way.
	lines <- created
	lines <- " \t jobId: dataform-xf-ab12-1,"
	for range 2 {
		<-fj.listed
	}
	r = tr.Report()
	if len(r.Actions) != 2 || r.Actions[0].State != "SUCCEEDED" || r.Actions[0].JobID != "dataform-xf-ab12-1" || r.Actions[1].JobID != "dataform-xf-ab12-9" {
		t.Errorf("after the CLI named it: %+v", r.Actions)
	}

	close(lines)
	done <- project.RunResult{Canceled: true, ExitCode: 130}
	<-tr.Done
	if r = tr.Report(); r.State != "CANCELLED" || len(r.Actions) != 1 {
		t.Errorf("ended: %+v", r)
	}
}

// TestTrackSaysWhenJobsCantBeListed: a listing that fails is told of in the
// run's lines, once, and the run goes on without running rows.
func TestTrackSaysWhenJobsCantBeListed(t *testing.T) {
	fj := &fakeJobs{err: errors.New("Access Denied: bigquery.jobs.list")}
	run, lines, done := start()
	tr := Track(run, Options{Project: "demo", Prefix: Prefix("xf-ab12"), Active: fj, PollInterval: time.Millisecond})
	if l := <-tr.Lines; !strings.Contains(l, "can't list the running BigQuery jobs (Access Denied: bigquery.jobs.list)") {
		t.Errorf("line %q", l)
	}
	lines <- created
	if l := <-tr.Lines; l != created {
		t.Errorf("line %q", l)
	}
	close(lines)
	done <- project.RunResult{}
	for range tr.Lines {
	}
	<-tr.Done
	if r := tr.Report(); r.State != "SUCCEEDED" || len(r.Actions) != 1 || fj.calls != 0 {
		t.Errorf("report %+v", r)
	}
}

func TestMatcher(t *testing.T) {
	match := Matcher([]project.Target{{Database: "demo", Schema: "analytics", Name: "orders"}})
	for q, want := range map[string]bool{
		"create or replace table `demo.analytics.orders` as select 1":                        true,
		"\n  CREATE TABLE IF NOT EXISTS `analytics.orders` (a int64)":                        true,
		"/* a */ -- b\ninsert into `demo.analytics.orders` select 1":                         true,
		"merge `demo.analytics.orders` t using (select 1) s on false":                        true,
		"select * from `demo.analytics.orders`":                                              false,
		"create or replace view `demo.analytics.v` as select * from `demo.analytics.orders`": false,
		"": false,
	} {
		if _, ok := match(q); ok != want {
			t.Errorf("%q: matched %v, want %v", q, ok, want)
		}
	}
}
