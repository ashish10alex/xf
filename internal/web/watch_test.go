package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

const testWorkflowID = "1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestWatchWorkflowShowsItsJobsLikeARun(t *testing.T) {
	type watched struct {
		ref  dataformapi.InvocationRef
		base dataformapi.Repository
		ctx  context.Context
		run  *fakeRun
	}
	got := make(chan watched, 1)
	watch := func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx}
		got <- watched{ref, base, ctx, f}
		rep := &project.RunReport{State: "SUCCEEDED", Actions: []project.RunAction{{
			Target: project.Target{Schema: "mart", Name: "orders"}, State: "SUCCEEDED", JobID: "job_1",
			Job: &project.JobStats{BytesBilled: 1 << 30, CostUSD: 0.006},
		}}}
		return &project.Run{Lines: f.lines, Done: f.done, Report: func() *project.RunReport { return rep }}, &dataformapi.Invocation{}, nil
	}
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), WatchWorkflow: watch})

	var boot bootView
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &boot)
	if !boot.Project.Caps.Watch {
		t.Errorf("caps = %+v, want watch", boot.Project.Caps)
	}

	var ok okView
	postJSON(t, c, ts.URL+"/api/watch", map[string]string{"input": "  not a workflow  "}, &ok)
	if ok.OK || !strings.Contains(ok.Message, "not a workflow ID") {
		t.Errorf("bad ID: %+v", ok)
	}
	postJSON(t, c, ts.URL+"/api/watch", map[string]string{"input": "https://console.cloud.google.com/bigquery/dataform/locations/europe-west2/repositories/shop/workflows/" + testWorkflowID + "?project=my-project"}, &ok)
	if !ok.OK {
		t.Fatalf("watch: %+v", ok)
	}
	w := <-got
	if w.ref.ID != testWorkflowID || w.ref.Repo != "shop" || w.ref.Location != "europe-west2" || w.ref.Project != "my-project" {
		t.Errorf("ref = %+v", w.ref)
	}
	// One run at a time, a watch included.
	postJSON(t, c, ts.URL+"/api/watch", map[string]string{"input": testWorkflowID}, &ok)
	if ok.OK || !strings.Contains(ok.Message, "already in progress") {
		t.Errorf("second watch: %+v, want refused", ok)
	}
	w.run.lines <- "✓ mart.orders"
	close(w.run.lines)
	w.run.done <- project.RunResult{State: "SUCCEEDED", Duration: 210 * time.Second}

	var v runView
	waitFor(t, func() bool {
		getJSON(t, c, ts.URL+"/api/run", &v)
		return !v.Running && v.Jobs != nil && len(v.Jobs.Rows) == 1
	})
	if !v.Watching || !v.API || v.Label != "workflow …eeeeeeeeeeee" {
		t.Errorf("run = %+v", v)
	}
	if v.Title != "✓ succeeded · 210.0s" || v.Jobs.Rows[0].Cost == "" {
		t.Errorf("title %q, jobs %+v", v.Title, v.Jobs)
	}
}

// A workflow that can't be found is answered to the prompt that asked, and
// the run, if there is one, is left alone.
func TestWatchWorkflowSaysWhyItCouldNotStart(t *testing.T) {
	var err error = &dataformapi.APIError{Status: 404, Message: "Resource not found"}
	watch := func(context.Context, dataformapi.InvocationRef, dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
		return nil, nil, err
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), WatchWorkflow: watch})
	var ok okView
	postJSON(t, c, ts.URL+"/api/watch", map[string]string{"input": testWorkflowID}, &ok)
	if ok.OK || !strings.Contains(ok.Message, "Resource not found") {
		t.Errorf("watch: %+v, want the reason", ok)
	}
	err = dataformapi.ErrAuth
	postJSON(t, c, ts.URL+"/api/watch", map[string]string{"input": testWorkflowID}, &ok)
	if ok.OK || !strings.Contains(ok.Message, dataformapi.AuthHint) {
		t.Errorf("without credentials: %+v, want how to log in", ok)
	}
	var v runView
	getJSON(t, c, ts.URL+"/api/run", &v)
	if v.Ran || v.Running || v.Seq != 0 {
		t.Errorf("run = %+v, want it untouched", v)
	}
}

func TestShortWorkflowID(t *testing.T) {
	if got := shortWorkflowID(testWorkflowID); got != "…eeeeeeeeeeee" {
		t.Errorf("long ID: %q", got)
	}
	if got := shortWorkflowID("42"); got != "42" {
		t.Errorf("short ID: %q", got)
	}
}
