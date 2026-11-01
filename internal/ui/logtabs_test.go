package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
)

// fakeRuns is a project's kept runs.
type fakeRuns struct {
	entries   []runhistory.Entry
	refreshed []string
}

func (f *fakeRuns) List() ([]runhistory.Entry, error) { return f.entries, nil }

func (f *fakeRuns) Clear() error {
	f.entries = nil
	return nil
}

func (f *fakeRuns) Refresh(_ context.Context, e runhistory.Entry) (runhistory.Entry, error) {
	f.refreshed = append(f.refreshed, e.ID)
	e.State = runhistory.StateInterrupted
	return e, nil
}

// cliRunReport is the report of a CLI run: a row for each job, the last of
// them still running, under the ID of its job.
func cliRunReport() *project.RunReport {
	r := testRunReport()
	r.URL, r.State = "", "RUNNING"
	r.Actions = append(r.Actions[:2:2], project.RunAction{Target: project.Target{Name: "job …9f2c"}, State: "RUNNING",
		Started: testNow().Add(-48 * time.Second), JobID: "dataform-xf-ab12-9f2c",
		JobURL: "https://console.cloud.google.com/bigquery?project=demo-project&j=bq:EU:dataform-xf-ab12-9f2c&page=queryresults"})
	return r
}

func keptRuns() []runhistory.Entry {
	api := testRunReport()
	return []runhistory.Entry{
		{ID: "r3", Via: runhistory.ViaCLI, Cmdline: "dataform run --actions x", Started: testNow().Add(-time.Hour), Ended: testNow().Add(-time.Hour + 92*time.Second),
			State: "FAILED", Report: cliRunReport()},
		{ID: "r2", Via: runhistory.ViaAPI, Cmdline: "dataform API run", Started: testNow().Add(-26 * time.Hour), Ended: testNow().Add(-26*time.Hour + 75*time.Second),
			State: "SUCCEEDED", Report: api},
		{ID: "r1", Via: runhistory.ViaCLI, Cmdline: "dataform run --tags daily", Started: testNow().Add(-50 * time.Hour), State: "RUNNING"},
	}
}

func newTabsTestModel(t *testing.T, runs *fakeRuns, report func() *project.RunReport) (Model, *[]string) {
	t.Helper()
	fa := &fakeAPIRunner{lines: []string{"↗ " + testInvocationURL}, res: project.RunResult{State: "SUCCEEDED"}, report: testRunReport()}
	m, _ := newAPITestModel(t, fa, false)
	m.deps.Runs = runs
	m.deps.StartRun = func(context.Context, project.RunOptions) (*project.Run, error) {
		lines := make(chan string, 2)
		lines <- "Running..."
		lines <- "Table created:  analytics.daily_orders [table] "
		close(lines)
		done := make(chan project.RunResult, 1)
		done <- project.RunResult{Duration: 80 * time.Second}
		return &project.Run{Lines: lines, Done: done, Report: report}, nil
	}
	opened := &[]string{}
	m.deps.OpenURL = func(u string) error { *opened = append(*opened, u); return nil }
	return drive(t, m, m.loadHistory(), tea.WindowSizeMsg{Width: 150, Height: 40}), opened
}

// TestLogTabs: with kept runs the log pane has three tabs, which t goes
// round. A CLI run opens on its log, which says that t lists its jobs; the
// jobs tab has a row for each, and u opens the one under the cursor.
func TestLogTabs(t *testing.T) {
	m, opened := newTabsTestModel(t, &fakeRuns{}, cliRunReport)
	m = drive(t, m, nil, press("s", "r", "4", "z")...)
	view := screen(m)
	assertContains(t, view, "4 [log] │ jobs (3) │ history · analytics.daily_orders", "Table created:", "t jobs (3)",
		"t lists this run's BigQuery jobs and what they cost")
	assertNotContains(t, view, "Est. cost")

	m = drive(t, m, nil, press("t")...)
	view = screen(m)
	assertContains(t, view, "4 log │ [jobs (3)] │ history · analytics.daily_orders", "t history",
		"Target", "Est. cost", "analytics.daily_orders", "✓ succeeded", "$0.0183",
		"job …9f2c", "▶ running", "▶ 1 running", "u open BigQuery job")
	assertNotContains(t, view, "Table created:", ".job …9f2c")

	// ctrl+n and ctrl+p move through the jobs; u opens the one under the cursor.
	m = drive(t, m, nil, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m = drive(t, m, nil, press("u")...)
	if len(*opened) != 1 || !strings.Contains((*opened)[0], "j=bq:EU:job_2") {
		t.Fatalf("opened %q, want job_2", *opened)
	}
	m = drive(t, m, nil, press("enter")...)
	if len(*opened) != 2 {
		t.Errorf("enter opened %q", *opened)
	}

	m = drive(t, m, nil, press("t")...)
	assertContains(t, screen(m), "4 log │ jobs (3) │ [history]", "no runs kept yet", "t log")
	m = drive(t, m, nil, press("t")...)
	assertContains(t, screen(m), "4 [log] │ jobs (3) │ history", "Table created:")
}

// TestRunningJobShowsHowLongSoFar: while the run goes, a running job's row
// says how long it has taken.
func TestRunningJobShowsHowLongSoFar(t *testing.T) {
	m, _ := newTabsTestModel(t, &fakeRuns{}, cliRunReport)
	m.running, m.runTable.ran = true, true
	m.runTable.set(cliRunReport())
	m.logTab, m.showLog = tabJobs, true
	m.layout()
	assertContains(t, m.tabsBody(120, false), "48s")
	m.running = false
	assertNotContains(t, m.tabsBody(120, false), "48s")
}

// TestHistoryTab: the history lists the kept runs with how each was
// started; enter opens one to its jobs and backspace goes back, and u opens
// an API run's page.
func TestHistoryTab(t *testing.T) {
	runs := &fakeRuns{entries: keptRuns()}
	m, opened := newTabsTestModel(t, runs, cliRunReport)
	// Times are told in the local zone, whichever the test runs in.
	at := func(ago time.Duration) string { return m.when(testNow().Add(-ago)) }
	// Nothing was run here yet: the jobs tab has the last kept run.
	m = drive(t, m, nil, press("L", "4", "z", "t")...)
	assertContains(t, screen(m), "4 log │ [jobs (3)] │ history · last run · cli · "+at(time.Hour)+" · ✗ failed", "job …9f2c", "✗ 1 failed")

	m = drive(t, m, nil, press("t")...)
	view := screen(m)
	assertContains(t, view, "4 log │ jobs (3) │ [history]", "When", "Via", "Jobs", "Command",
		at(time.Hour), "cli", "✗ failed", "1m 32s", "dataform run --actions x",
		at(26*time.Hour), "api", "✓ succeeded", "1m 15s", "3.01 GiB", "$0.0184", "dataform API run",
		at(50*time.Hour), "dataform run --tags daily", "enter its jobs")
	if len(runs.refreshed) != 0 {
		t.Errorf("looked up %v before any was opened", runs.refreshed)
	}

	// u has no page for a CLI run; the API run below has one.
	m = drive(t, m, nil, press("u")...)
	assertContains(t, screen(m), "a CLI run has no page of its own")
	m = drive(t, m, nil, press("j", "u")...)
	if len(*opened) != 1 || (*opened)[0] != testInvocationURL {
		t.Fatalf("opened %q", *opened)
	}

	// enter lists the run's jobs: an API run's are opened with enter, its
	// page with u.
	m = drive(t, m, nil, press("enter")...)
	view = screen(m)
	assertContains(t, view, "[history] · api · "+at(26*time.Hour)+" · ✓ succeeded", "analytics.daily_orders_check", "est. $0.0184",
		"backspace back", "enter open BigQuery job")
	m = drive(t, m, nil, press("enter")...)
	if len(*opened) != 2 || !strings.Contains((*opened)[1], "j=bq:EU:job_1") {
		t.Fatalf("opened %q", *opened)
	}
	m = drive(t, m, nil, press("backspace")...)
	assertContains(t, screen(m), "dataform run --tags daily")

	// A run that was never seen to end is looked up when it is opened.
	m = drive(t, m, nil, press("j", "enter")...)
	assertContains(t, screen(m), "[history] · cli · "+at(50*time.Hour)+" · · interrupted", "this run had no actions")
	if len(runs.refreshed) != 1 || runs.refreshed[0] != "r1" {
		t.Errorf("looked up %v", runs.refreshed)
	}

	// A run started here takes the jobs tab over, and leaves the history.
	m = drive(t, m, nil, press("s", "r")...)
	assertContains(t, screen(m), "4 [log] │ jobs (3) │ history · analytics.daily_orders")
	assertNotContains(t, screen(m), "last run")
}

// TestAPIRunShowsItsTableUnderTabs: an API run's table still shows itself,
// as the jobs tab, and u there opens the run's page.
func TestAPIRunShowsItsTableUnderTabs(t *testing.T) {
	m, opened := newTabsTestModel(t, &fakeRuns{}, cliRunReport)
	m = drive(t, m, nil, press("a", "s", "r", "4", "z")...)
	assertContains(t, screen(m), "4 log │ [jobs (2)] │ history", "Est. cost", "analytics.report", "u open run in browser", "enter open BigQuery job")
	m = drive(t, m, nil, press("u")...)
	if len(*opened) != 1 || (*opened)[0] != testInvocationURL {
		t.Fatalf("opened %q", *opened)
	}
	// The log, once chosen, is what the next API run shows.
	m = drive(t, m, nil, press("t", "t")...)
	m = drive(t, m, nil, press("3", "r", "4")...)
	assertContains(t, screen(m), "4 [log] │ jobs (2) │ history", "↗ "+testInvocationURL)
}

// TestClearHistory: X in the history asks first; y forgets the kept runs,
// and the jobs tab no longer has the last of them.
func TestClearHistory(t *testing.T) {
	runs := &fakeRuns{entries: keptRuns()}
	m, _ := newTabsTestModel(t, runs, cliRunReport)
	m = drive(t, m, nil, press("L", "4", "z", "t", "t")...)
	assertContains(t, screen(m), "X clear history")

	m = drive(t, m, nil, press("X")...)
	assertContains(t, screen(m), "forget the 3 kept runs of this project?", "y clear", "any other key keeps them")
	m = drive(t, m, nil, press("n")...)
	assertContains(t, screen(m), "history kept", "dataform run --tags daily")
	if len(runs.entries) != 3 {
		t.Fatalf("cleared without a y: %v", runs.entries)
	}

	m = drive(t, m, nil, press("X", "y")...)
	assertContains(t, screen(m), "history cleared", "no runs kept yet")
	assertNotContains(t, screen(m), "dataform run --tags daily", "jobs (3)")
	m = drive(t, m, nil, press("X")...)
	assertContains(t, screen(m), "there is nothing to clear")
	m = drive(t, m, nil, press("t", "t")...)
	assertContains(t, screen(m), "nothing has been run here")
}
