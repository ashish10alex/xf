package dataformapi

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

const watchedID = "1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestParseInvocation(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    InvocationRef
		wantErr string
	}{
		{in: watchedID, want: InvocationRef{ID: watchedID}},
		{in: "  " + watchedID + "\n", want: InvocationRef{ID: watchedID}},
		{
			in:   "https://console.cloud.google.com/bigquery/dataform/locations/europe-west2/repositories/shop/workflows/" + watchedID + "?project=my-project",
			want: InvocationRef{Project: "my-project", Location: "europe-west2", Repo: "shop", ID: watchedID},
		},
		{
			in:   "projects/p/locations/europe-west2/repositories/shop/workflowInvocations/42",
			want: InvocationRef{Project: "p", Location: "europe-west2", Repo: "shop", ID: "42"},
		},
		{in: "", wantErr: "paste a workflow ID"},
		{in: "https://console.cloud.google.com/bigquery?project=p", wantErr: "names no workflow"},
		{in: "bad id", wantErr: "not a workflow ID"},
		{in: "../x", wantErr: "names no workflow"},
		{in: "https://evil.example/bigquery/dataform/locations/europe-west2/repositories/r/workflows/42?project=p", wantErr: "only a page of console.cloud.google.com"},
		{in: "http://console.cloud.google.com/bigquery/dataform/locations/europe-west2/repositories/r/workflows/42", wantErr: "only a page of console.cloud.google.com"},
		{in: "javascript:alert(1)//workflows/42", wantErr: "only a page of console.cloud.google.com"},
		{in: "https://console.cloud.google.com@evil.example/x/workflows/42", wantErr: "only a page of console.cloud.google.com"},
		{in: "$(touch pwned)", wantErr: "not a workflow ID"},
		{in: "42;rm -rf", wantErr: "not a workflow ID"},
		{in: "42\nsecond line", wantErr: "not a workflow ID"},
		{in: "https://console.cloud.google.com/x/locations/..%2f..%2fx/repositories/r/workflows/42", wantErr: "not a location"},
		{in: "projects/p;x/locations/europe-west2/repositories/r/workflowInvocations/42", wantErr: "not a project"},
		{in: "projects/p/locations/europe-west2/repositories/r$x/workflowInvocations/42", wantErr: "not a repository name"},
		{in: strings.Repeat("a", 3000), wantErr: "too long"},
	} {
		got, err := ParseInvocation(tt.in)
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%q: err = %v, want it to mention %q", tt.in, err, tt.wantErr)
			}
		case err != nil || got != tt.want:
			t.Errorf("%q: got %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestInvocationRefResolve(t *testing.T) {
	base := Repository{Project: "proj", Location: "europe-west2", Name: "shop"}
	if got := (InvocationRef{ID: "1"}).Resolve(base); got != base {
		t.Errorf("an ID alone is the project's: %+v", got)
	}
	got := InvocationRef{Project: "other", Location: "us-east1", Repo: "x", ID: "1"}.Resolve(base)
	if want := (Repository{Project: "other", Location: "us-east1", Name: "x"}); got != want {
		t.Errorf("a URL names its own: %+v", got)
	}
}

func watchOpts() WatchOptions {
	return WatchOptions{Repo: Repository{Project: "proj", Location: "europe-west2", Name: "shop"}, ID: "42", PollInterval: time.Millisecond}
}

// TestWatchReportsWhatJobsCost: an invocation that has ended is read once
// and ends with its report, each job looked up, and its own timing.
func TestWatchReportsWhatJobsCost(t *testing.T) {
	a := act("a", "SUCCEEDED")
	a.BigQuery.JobID = "job_a"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, actions: []Action{a}}}}
	jobs := &fakeJobs{jobs: map[string]bigquery.Job{"job_a": {BytesBilled: 10 << 20, Done: true}}}
	opts := watchOpts()
	opts.Jobs = jobs
	r, inv, err := Watch(context.Background(), api, opts)
	if err != nil {
		t.Fatal(err)
	}
	if inv == nil || api.got[0] != "projects/proj/locations/europe-west2/repositories/shop/workflowInvocations/42" {
		t.Errorf("asked for %q", api.got)
	}
	lines, res := collect(t, r)
	if res.State != StateSucceeded || res.ExitCode != 0 || res.Err != nil || res.Detached {
		t.Errorf("result = %+v", res)
	}
	if lines[0] != "watching workflow 42 in shop (proj, europe-west2) · succeeded" || !strings.HasPrefix(lines[1], "↗ https://console.cloud.google.com/") {
		t.Errorf("lines = %q", lines)
	}
	rep := r.Report()
	if rep == nil || len(rep.Actions) != 1 || rep.Actions[0].Job == nil || rep.Actions[0].Job.BytesBilled != 10<<20 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.HasSuffix(r.URL(), "/workflows/42?project=proj") {
		t.Errorf("URL() = %q", r.URL())
	}
}

func TestWatchFollowsARunningInvocationToItsEnd(t *testing.T) {
	api := &fakeAPI{polls: []poll{
		{state: StateRunning, actions: []Action{act("a", "RUNNING")}},
		{state: StateFailed, actions: []Action{act("a", "FAILED")}},
	}}
	lines, res := collect(t, mustWatch(t, api, watchOpts()))
	if res.State != StateFailed || res.ExitCode != 1 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "▶ running s.a") {
		t.Errorf("lines = %q", lines)
	}
}

// TestWatchNeverCancelsTheInvocation: the invocation is not xf's, so
// stopping ends the watch and nothing else.
func TestWatchNeverCancelsTheInvocation(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateRunning, actions: []Action{act("a", "RUNNING")}}}}
	ctx, cancel := context.WithCancel(context.Background())
	r := mustWatch(t, api, watchOpts(), ctx)
	for l := range r.Lines {
		if strings.HasPrefix(l, "▶ running") {
			break
		}
	}
	cancel()
	_, res := collect(t, r)
	if !res.Detached || api.cancelled != 0 {
		t.Errorf("result = %+v, cancelled %d times", res, api.cancelled)
	}
}

func TestWatchTimesTheInvocationByItsOwnClock(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, timing: Timing{StartTime: "2026-09-30T10:00:00Z", EndTime: "2026-09-30T10:03:30Z"}}}}
	_, res := collect(t, mustWatch(t, api, watchOpts()))
	if res.Duration != 210*time.Second {
		t.Errorf("Duration = %v", res.Duration)
	}
}

func TestWatchRefusesWhatItCannotFind(t *testing.T) {
	api := &fakeAPI{polls: []poll{{err: &APIError{Status: 404, Message: "Resource not found"}}}}
	if _, _, err := Watch(context.Background(), api, watchOpts()); err == nil || !strings.Contains(err.Error(), "Resource not found") {
		t.Errorf("err = %v", err)
	}
	for _, tt := range []struct {
		edit func(*WatchOptions)
		want string
	}{
		{func(o *WatchOptions) { o.Repo.Name = "" }, "--dataform-repo"},
		{func(o *WatchOptions) { o.Repo.Project = "" }, "defaultProject"},
		{func(o *WatchOptions) { o.Repo.Location = "" }, "--dataform-location"},
	} {
		o := watchOpts()
		tt.edit(&o)
		if _, _, err := Watch(context.Background(), &fakeAPI{}, o); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err = %v, want it to mention %q", err, tt.want)
		}
	}
	// A multi-region is explained, as a run's is.
	api = &fakeAPI{polls: []poll{{err: &APIError{Status: 403, Message: "Location EU is not found or access is unauthorized."}}}}
	o := watchOpts()
	o.Repo.Location = "EU"
	if _, _, err := Watch(context.Background(), api, o); err == nil || !strings.Contains(err.Error(), "not a multi-region like EU") {
		t.Errorf("err = %v", err)
	}
}

func mustWatch(t *testing.T, api API, opts WatchOptions, ctx ...context.Context) *project.Run {
	t.Helper()
	c := context.Background()
	if len(ctx) > 0 {
		c = ctx[0]
	}
	r, _, err := Watch(c, api, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestWatchRefusesAnotherWorkflowThanAsked: whatever Dataform answers with
// has to be the invocation, repository and location asked for.
func TestWatchRefusesAnotherWorkflowThanAsked(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateSucceeded}}}
	for _, edit := range []func(*WatchOptions){
		func(o *WatchOptions) { o.ID = "43" },
		func(o *WatchOptions) { o.Repo.Name = "other" },
		func(o *WatchOptions) { o.Repo.Location = "us-east1" },
	} {
		o := watchOpts()
		edit(&o)
		if _, _, err := Watch(context.Background(), api, o); err == nil || !strings.Contains(err.Error(), "not the workflow asked for") {
			t.Errorf("%+v: err = %v", o, err)
		}
	}
	// The project may be its number.
	o := watchOpts()
	o.Repo.Project = "123456"
	if _, _, err := Watch(context.Background(), api, o); err != nil {
		t.Errorf("project by number: %v", err)
	}
}

// TestWatchLooksJobsUpWhereTheyRan: a job is read in the project's BigQuery
// location, not the Dataform repository's, and when jobs can't be read the
// log says so, since the table then has no costs.
func TestWatchLooksJobsUpWhereTheyRan(t *testing.T) {
	a, b := act("a", "SUCCEEDED"), act("b", "SUCCEEDED")
	a.BigQuery.JobID, b.BigQuery.JobID = "job_a", "job_b"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, actions: []Action{a, b}}}}
	jobs := &fakeJobs{jobs: map[string]bigquery.Job{"job_a": {BytesBilled: 1 << 40, SlotMs: 1000, Done: true}}}
	opts := watchOpts()
	opts.Jobs, opts.JobLocation = jobs, "EU"
	r := mustWatch(t, api, opts)
	lines, _ := collect(t, r)
	// job_b is not found anywhere: the repository's location is tried too, and none.
	if want := []string{"proj/EU/job_a", "proj/EU/job_b", "proj/europe-west2/job_b", "proj//job_b"}; !slices.Equal(sortedCopy(jobs.asked), sortedCopy(want)) {
		t.Errorf("looked up %v, want %v", jobs.asked, want)
	}
	if got := r.Report().Totals(); got.Jobs != 1 || got.BytesBilled != 1<<40 {
		t.Errorf("totals %+v", got)
	}
	var warned string
	for _, l := range lines {
		if strings.HasPrefix(l, "⚠ could not read") {
			warned = l
		}
	}
	if want := "⚠ could not read 1 BigQuery job, so their costs are missing (looked in EU, europe-west2 and with no location; first: job_b: Not found: Job job_b)"; warned != want {
		t.Errorf("warning %q, want %q", warned, want)
	}
}

func sortedCopy(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// TestWatchFindsJobsOfAnotherRepository: a workflow of another project may
// have run its jobs in another location than this project's; a job not
// found there is looked for in the repository's own, then anywhere.
func TestWatchFindsJobsOfAnotherRepository(t *testing.T) {
	a := act("a", "SUCCEEDED")
	a.BigQuery.JobID = "job_a"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, actions: []Action{a}}}}
	jobs := &fakeJobs{jobs: map[string]bigquery.Job{"job_a": {BytesBilled: 1 << 30, SlotMs: 1000, Done: true}}, onlyIn: "europe-west2"}
	opts := watchOpts()
	opts.Jobs, opts.JobLocation = jobs, "EU"
	r := mustWatch(t, api, opts)
	lines, _ := collect(t, r)
	if got := r.Report().Totals(); got.Jobs != 1 {
		t.Errorf("totals %+v, lines %q", got, lines)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "⚠") {
			t.Errorf("warned: %q", l)
		}
	}
}

// blockedJobs holds every lookup until release is closed.
type blockedJobs struct{ release chan struct{} }

func (b blockedJobs) Job(ctx context.Context, _, _, _ string) (*bigquery.Job, error) {
	select {
	case <-b.release:
		return &bigquery.Job{BytesBilled: 1 << 30, Done: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestWatchShowsActionsBeforeTheirJobsAreRead: reading the jobs of a big
// workflow takes a while, and the table has its rows meanwhile.
func TestWatchShowsActionsBeforeTheirJobsAreRead(t *testing.T) {
	a := act("a", "SUCCEEDED")
	a.BigQuery.JobID = "job_a"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded, actions: []Action{a}}}}
	jobs := blockedJobs{release: make(chan struct{})}
	opts := watchOpts()
	opts.Jobs = jobs
	r := mustWatch(t, api, opts)
	go func() {
		for range r.Lines {
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for rep := r.Report(); rep == nil; rep = r.Report() {
		if time.Now().After(deadline) {
			t.Fatal("no report while the job is being read")
		}
		time.Sleep(time.Millisecond)
	}
	if rep := r.Report(); len(rep.Actions) != 1 || rep.Actions[0].Job != nil {
		t.Errorf("report while reading: %+v", rep)
	}
	close(jobs.release)
	if res := <-r.Done; res.State != StateSucceeded {
		t.Errorf("result = %+v", res)
	}
	if rep := r.Report(); rep.Actions[0].Job == nil || rep.Actions[0].Job.BytesBilled != 1<<30 {
		t.Errorf("report after: %+v", rep.Actions[0])
	}
}
