package runhistory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

var t0 = time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)

// TestPutKeepsTheLastRuns: runs come back newest first, a run put again
// replaces itself, and only the last Keep are kept.
func TestPutKeepsTheLastRuns(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if runs, err := s.Load("/work/demo"); runs != nil || err != nil {
		t.Fatalf("no file: %v %v", runs, err)
	}
	for i := range Keep + 5 {
		if err := s.Put("/work/demo", Entry{ID: fmt.Sprint(i), Via: ViaCLI, Started: t0.Add(time.Duration(i) * time.Minute), State: "RUNNING"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("/work/demo", Entry{ID: "54", Via: ViaCLI, Started: t0.Add(54 * time.Minute), State: "SUCCEEDED"}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Load("/work/demo")
	if err != nil || len(runs) != Keep || runs[0].ID != "54" || runs[0].State != "SUCCEEDED" || runs[Keep-1].ID != "5" {
		t.Errorf("%d runs, first %+v, last %+v (%v)", len(runs), runs[0], runs[len(runs)-1], err)
	}
	if other, _ := s.Load("/work/other"); other != nil {
		t.Errorf("another project's runs: %v", other)
	}
}

// TestRecord: a run is kept as it starts, as its report changes and as it
// ends, and its lines and result come through.
func TestRecord(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	lines, done := make(chan string), make(chan project.RunResult, 1)
	var report *project.RunReport
	run := &project.Run{Cmdline: "dataform run --actions a", Lines: lines, Done: done, Report: func() *project.RunReport { return report }}
	rec := s.Record("/work/demo", Entry{ID: "ab12", Via: ViaCLI, Started: t0}, run)

	kept := func() Entry {
		t.Helper()
		runs, err := s.Load("/work/demo")
		if err != nil || len(runs) != 1 {
			t.Fatalf("kept %v (%v)", runs, err)
		}
		return runs[0]
	}
	if e := kept(); e.State != "RUNNING" || e.Cmdline != "dataform run --actions a" || e.Report != nil {
		t.Errorf("at the start: %+v", e)
	}
	report = &project.RunReport{State: "RUNNING", Actions: []project.RunAction{{State: "SUCCEEDED", JobID: "j1"}}}
	lines <- ""
	if l := <-rec.Lines; l != "" {
		t.Errorf("line %q", l)
	}
	if e := kept(); e.Jobs() != 1 {
		t.Errorf("after a line: %+v", e)
	}
	close(lines)
	done <- project.RunResult{ExitCode: 1, Duration: 90 * time.Second}
	if _, open := <-rec.Lines; open {
		t.Error("lines left open")
	}
	if res := <-rec.Done; res.ExitCode != 1 {
		t.Errorf("result %+v", res)
	}
	e := kept()
	if d, ok := e.Duration(); e.State != "FAILED" || !ok || d != 90*time.Second {
		t.Errorf("at the end: %+v", e)
	}
}

func TestResultState(t *testing.T) {
	for want, res := range map[string]project.RunResult{
		"SUCCEEDED":      {},
		"FAILED":         {ExitCode: 1},
		"CANCELLED":      {Canceled: true, ExitCode: 130},
		"CANCELING":      {State: "CANCELING"},
		StateInterrupted: {Detached: true, State: "RUNNING"},
	} {
		if got := ResultState(res); got != want {
			t.Errorf("%+v: %s, want %s", res, got, want)
		}
	}
}

type jobs map[string]*bigquery.Job

func (j jobs) Job(_ context.Context, _, _, id string) (*bigquery.Job, error) {
	if job := j[id]; job != nil {
		return job, nil
	}
	return nil, errors.New("Not found: Job")
}

// TestRefresh: the jobs of a run xf stopped following are looked up; those
// that ended since have what they did, and the run is interrupted once none
// is running.
func TestRefresh(t *testing.T) {
	e := Entry{ID: "ab12", Via: ViaCLI, Started: t0, State: "RUNNING", Project: "demo", Report: &project.RunReport{State: "RUNNING", Actions: []project.RunAction{
		{State: "SUCCEEDED", JobID: "done", Job: &project.JobStats{BytesBilled: 1}},
		{State: "RUNNING", JobID: "ok"},
		{State: "RUNNING", JobID: "bad"},
		{State: "RUNNING", JobID: "still"},
		{State: "SKIPPED"},
	}}}
	j := jobs{
		"ok":    {Done: true, BytesBilled: 10 << 20, Ended: t0.Add(time.Minute)},
		"bad":   {Done: true, Failed: true, Ended: t0.Add(2 * time.Minute)},
		"still": {},
	}
	got := Refresh(context.Background(), j, e)
	a := got.Report.Actions
	if got.State != "RUNNING" || a[0].Job.BytesBilled != 1 || a[1].State != "SUCCEEDED" || a[1].Job.BytesBilled != 10<<20 || a[2].State != "FAILED" || a[3].State != "RUNNING" {
		t.Errorf("with a job still running: %s %+v", got.State, a)
	}
	if e.Report.Actions[1].State != "RUNNING" {
		t.Error("the run given was changed")
	}

	delete(j, "still")
	got = Refresh(context.Background(), j, got)
	if a := got.Report.Actions[3]; got.State != StateInterrupted || a.State != StateInterrupted || a.JobErr == "" || !got.Ended.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("with none running: %+v %+v", got, a)
	}
	e.Updated = t0
	if !e.Stale(t0.Add(time.Minute)) || e.Stale(t0.Add(time.Second)) || got.Stale(t0.Add(time.Hour)) {
		t.Error("stale")
	}

	api := Entry{Via: ViaAPI, State: "RUNNING"}
	if got := Refresh(context.Background(), j, api); got.State != "RUNNING" {
		t.Errorf("an API run: %+v", got)
	}
}

// TestPutRefreshedLeavesAFollowedRun: a run looked up as stale is not kept
// over what the xf that follows it has kept since.
func TestPutRefreshedLeavesAFollowedRun(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	was := Entry{ID: "ab12", Via: ViaCLI, Started: t0, State: "RUNNING", Updated: t0}
	if err := s.Put("/work/demo", was); err != nil {
		t.Fatal(err)
	}
	now := was
	now.State = StateInterrupted
	if got, err := s.PutRefreshed("/work/demo", was, now); err != nil || got.State != StateInterrupted {
		t.Errorf("nobody follows it: %+v (%v)", got, err)
	}

	// Its xf ends it while it is being looked up.
	ended := was
	ended.State, ended.Updated = "SUCCEEDED", t0.Add(time.Minute)
	if err := s.Put("/work/demo", was); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("/work/demo", ended); err != nil {
		t.Fatal(err)
	}
	got, err := s.PutRefreshed("/work/demo", was, now)
	if runs, _ := s.Load("/work/demo"); err != nil || got.State != "SUCCEEDED" || runs[0].State != "SUCCEEDED" {
		t.Errorf("followed: %+v, kept %+v (%v)", got, runs[0], err)
	}
}

// TestClear: the kept runs are forgotten, but for one an xf still follows.
func TestClear(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := t0.Add(time.Hour)
	for _, e := range []Entry{
		{ID: "done", Started: t0, State: "SUCCEEDED"},
		{ID: "left", Started: t0.Add(time.Minute), State: "RUNNING", Updated: t0},
		{ID: "going", Started: t0.Add(2 * time.Minute), State: "RUNNING", Updated: now.Add(-time.Second)},
	} {
		if err := s.Put("/work/demo", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Clear("/work/demo", now); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.Load("/work/demo"); len(runs) != 1 || runs[0].ID != "going" {
		t.Errorf("kept %+v", runs)
	}
	// With none going the file goes too, and clearing nothing is no error.
	for range 2 {
		if err := s.Clear("/work/demo", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if runs, err := s.Load("/work/demo"); runs != nil || err != nil {
		t.Errorf("kept %+v (%v)", runs, err)
	}
}
