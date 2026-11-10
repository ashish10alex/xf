package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeWatch is a workflow that has ended in state, with rep as its report.
func fakeWatch(state string, rep *project.RunReport, res project.RunResult) watchFunc {
	return func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
		lines := make(chan string, 2)
		lines <- "watching workflow " + ref.ID
		lines <- "✓ ds.orders"
		close(lines)
		done := make(chan project.RunResult, 1)
		done <- res
		return &project.Run{Lines: lines, Done: done, Report: func() *project.RunReport { return rep }}, &dataformapi.Invocation{State: state}, nil
	}
}

func watchedReport() *project.RunReport {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	return &project.RunReport{
		State: "FAILED", Started: t0, Ended: t0.Add(90 * time.Second),
		Actions: []project.RunAction{
			{Target: project.Target{Schema: "ds", Name: "orders"}, State: "SUCCEEDED", Started: t0, Ended: t0.Add(30 * time.Second), JobID: "j1",
				Job: &project.JobStats{BytesBilled: 2 << 30, CostUSD: 0.0122, SlotMs: 45000}},
			{Target: project.Target{Schema: "ds", Name: "customers"}, State: "FAILED", Started: t0, Ended: t0.Add(5 * time.Second), JobID: "j2"},
			{Target: project.Target{Schema: "ds", Name: "unused"}, State: "SKIPPED"},
		},
	}
}

func TestFollowWorkflowPrintsTheJobsTableAndExitsWithTheWorkflow(t *testing.T) {
	var out, log strings.Builder
	code, err := followWorkflow(context.Background(), dataformapi.InvocationRef{ID: "1700-abc"}, dataformapi.Repository{},
		fakeWatch("FAILED", watchedReport(), project.RunResult{State: "FAILED", ExitCode: 1}), false, &out, &log, false)
	if err != nil {
		t.Fatal(err)
	}
	if code != watchFailed {
		t.Errorf("exit %d for a failed workflow, want %d", code, watchFailed)
	}
	got := out.String()
	for _, want := range []string{
		"failed · 3 actions · 1 succeeded · 1 failed · 1 skipped · 1m 30s",
		"ACTION                  STATE          TIME  SLOT TIME    BILLED     COST\n",
		"ds.orders               ✓ succeeded     30s        45s  2.00 GiB  $0.0122\n",
		"ds.unused               – skipped\n",
		"Total (1 BigQuery job)               1m 30s        45s  2.00 GiB  $0.0122\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the table has no %q:\n%s", want, got)
		}
	}
	if !strings.Contains(log.String(), "watching workflow 1700-abc") || !strings.Contains(log.String(), "couldn't read 1 BigQuery job, so its cost is unknown") {
		t.Errorf("the log: %q", log.String())
	}
	if strings.Contains(got, "watching workflow") {
		t.Error("the log went to stdout")
	}
}

func TestFollowWorkflowExitCodes(t *testing.T) {
	for _, c := range []struct {
		name string
		res  project.RunResult
		want int
	}{
		{"succeeded", project.RunResult{State: "SUCCEEDED"}, watchOK},
		{"cancelled", project.RunResult{State: "CANCELLED", Canceled: true}, watchFailed},
		{"stopped watching", project.RunResult{Detached: true}, runInterrupted},
	} {
		var out, log strings.Builder
		code, err := followWorkflow(context.Background(), dataformapi.InvocationRef{ID: "x"}, dataformapi.Repository{},
			fakeWatch(c.res.State, &project.RunReport{State: c.res.State}, c.res), false, &out, &log, false)
		if err != nil || code != c.want {
			t.Errorf("%s: exit %d, %v; want %d", c.name, code, err, c.want)
		}
	}
	failing := func(ctx context.Context, ref dataformapi.InvocationRef, base dataformapi.Repository) (*project.Run, *dataformapi.Invocation, error) {
		return nil, nil, errors.New("not found")
	}
	if _, err := followWorkflow(context.Background(), dataformapi.InvocationRef{ID: "x"}, dataformapi.Repository{}, failing, false, &strings.Builder{}, &strings.Builder{}, false); err == nil {
		t.Error("a workflow not found is no error")
	}
}

func TestFollowWorkflowJSON(t *testing.T) {
	var out strings.Builder
	if _, err := followWorkflow(context.Background(), dataformapi.InvocationRef{ID: "1700-abc"}, dataformapi.Repository{},
		fakeWatch("FAILED", watchedReport(), project.RunResult{ExitCode: 1}), true, &out, &strings.Builder{}, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID      string              `json:"id"`
		State   string              `json:"state"`
		Actions []project.RunAction `json:"actions"`
		Totals  project.RunTotals   `json:"totals"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if got.ID != "1700-abc" || got.State != "FAILED" || len(got.Actions) != 3 || got.Totals.Jobs != 1 || got.Totals.CostUSD != 0.0122 {
		t.Errorf("JSON: %+v", got)
	}
}

func TestWorkflowDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workflow_settings.yaml"), []byte("defaultProject: acme-dev\ndefaultLocation: EU\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, l := workflowDefaults(dir); p != "acme-dev" || l != "EU" {
		t.Errorf("workflow_settings.yaml: %q, %q", p, l)
	}
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "dataform.json"), []byte(`{"defaultDatabase": "acme-old", "defaultLocation": "US"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, l := workflowDefaults(old); p != "acme-old" || l != "US" {
		t.Errorf("dataform.json: %q, %q", p, l)
	}
	if p, l := workflowDefaults(""); p != "" || l != "" {
		t.Errorf("no project: %q, %q", p, l)
	}
}

func TestWatchRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workflow_settings.yaml"), []byte("defaultProject: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, err := watchRoot(dir); err != nil || root == "" {
		t.Errorf("a Dataform project: %q, %v", root, err)
	}
	if _, err := watchRoot(t.TempDir()); err == nil {
		t.Error("a dir given that is no project")
	}
	t.Chdir(t.TempDir())
	if root, err := watchRoot(""); err != nil || root != "" {
		t.Errorf("outside a project: %q, %v", root, err)
	}
}

func TestFollowWorkflowOnATerminalDropsTheActionLines(t *testing.T) {
	var out, log strings.Builder
	if _, err := followWorkflow(context.Background(), dataformapi.InvocationRef{ID: "1700-abc"}, dataformapi.Repository{},
		fakeWatch("FAILED", watchedReport(), project.RunResult{ExitCode: 1}), false, &out, &log, true); err != nil {
		t.Fatal(err)
	}
	got := log.String()
	if !strings.Contains(got, "watching workflow 1700-abc\n") {
		t.Errorf("the header is gone: %q", got)
	}
	if strings.Contains(got, "✓ ds.orders") {
		t.Errorf("an action line on a terminal: %q", got)
	}
	if !strings.Contains(got, "3/3 actions done · 1 failed · $0.0122 · 1m 30s…") {
		t.Errorf("no progress line: %q", got)
	}
	if !strings.HasSuffix(strings.TrimSuffix(got, "xf: couldn't read 1 BigQuery job, so its cost is unknown (BigQuery credentials?)\n"), "\r\x1b[K") {
		t.Errorf("the progress line is left before the table: %q", got)
	}
}

func TestWatchProgress(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	r := &project.RunReport{State: "RUNNING", Started: t0, Actions: []project.RunAction{
		{State: "SUCCEEDED"}, {State: "RUNNING"}, {State: "RUNNING"}, {State: "PENDING"},
	}}
	if got, want := watchProgress(r, t0.Add(65*time.Second)), "1/4 actions done · 2 running · 1m 05s"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if got := watchProgress(&project.RunReport{}, t0); got != "waiting for the workflow's actions" {
		t.Errorf("no actions yet: %q", got)
	}
}

func TestJobsTableListsFailures(t *testing.T) {
	r := watchedReport()
	r.Actions[1].FailureReason = "Query error:\n  Unrecognized name: x"
	var out strings.Builder
	if err := writeJobsTable(&out, r, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\nfailures:\n  ds.customers: Query error: Unrecognized name: x\n") {
		t.Errorf("no failures under the table:\n%s", out.String())
	}
}
