package ui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/project"
)

const testInvocationURL = "https://console.cloud.google.com/bigquery/dataform/locations/EU/repositories/shop/workflows/42?project=demo-project"

// fakeAPIRunner records API runs and returns runs whose output is already
// complete, like fakeRunner.
type fakeAPIRunner struct {
	mu    sync.Mutex
	opts  []dataformapi.Options
	lines []string
	res   project.RunResult
	err   error
	// report is the run's report; nil for a run with no actions.
	report *project.RunReport
}

func (f *fakeAPIRunner) start(_ context.Context, opts dataformapi.Options) (*project.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opts = append(f.opts, opts)
	if f.err != nil {
		return nil, f.err
	}
	lines := make(chan string, len(f.lines))
	for _, l := range f.lines {
		lines <- l
	}
	close(lines)
	done := make(chan project.RunResult, 1)
	done <- f.res
	return &project.Run{Lines: lines, Done: done, URL: func() string { return testInvocationURL },
		Report: func() *project.RunReport { return f.report }}, nil
}

func newAPITestModel(t *testing.T, fa *fakeAPIRunner, runAPI bool) (Model, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{
		Root:        "/work/demo",
		Engine:      newFakeEngine(fc.compile),
		StartRun:    fr.start,
		StartAPIRun: fa.start,
		RunAPI:      runAPI,
		Now:         testNow,
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	return startOn(t, closePalette(t, m), "demo-project.analytics.daily_orders"), fr
}

func TestAPIToggleOnlyWhenAvailable(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertNotContains(t, screen(m), "a api")
	m = drive(t, m, nil, press("a")...)
	assertContains(t, screen(m), "API runs are not available")

	m, _ = newAPITestModel(t, &fakeAPIRunner{}, false)
	assertContains(t, screen(m), "[ ] f full-refresh  [ ] a api  [ ] s selected only", "r ▸ run 3 actions")
	m = drive(t, m, nil, press("3", "a", "d")...)
	assertContains(t, screen(m), "[x] a api", "r ▸ run in GCP 3 actions",
		"Dataform API run --actions demo-project.analytics.daily_orders demo-project.analytics_assertions.")
	m = drive(t, m, nil, press("s")...)
	assertContains(t, screen(m), "[x] s selected only", "r ▸ run in GCP daily_orders",
		"Dataform API run --actions demo-project.analytics.daily_orders --include-deps")
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "run via Dataform API")

	// --run-mode api starts it on.
	m, _ = newAPITestModel(t, &fakeAPIRunner{}, true)
	assertContains(t, screen(m), "[x] a api")
}

func TestAPIRunStreamsIntoLog(t *testing.T) {
	fa := &fakeAPIRunner{
		lines: []string{"⚠ 1 uncommitted file won't run: definitions/daily_orders.sqlx", "compiled commit abc1234", "↗ " + testInvocationURL, "✓ analytics.daily_orders · 3.0s"},
		res:   project.RunResult{State: dataformapi.StateSucceeded, Duration: 12 * time.Second},
	}
	m, fr := newAPITestModel(t, fa, true)
	m = drive(t, m, nil, press("s", "D", "r")...)
	want := []dataformapi.Options{{
		Repo:        dataformapi.Repository{Project: "demo-project", Location: "EU"},
		JobLocation: "EU",
		Invocation: dataformapi.InvocationConfig{
			IncludedTargets:              []dataformapi.Target{{Database: "demo-project", Schema: "analytics", Name: "daily_orders"}},
			TransitiveDependentsIncluded: true,
		},
	}}
	if !reflect.DeepEqual(fa.opts, want) {
		t.Errorf("API runs = %+v, want %+v", fa.opts, want)
	}
	if len(fr.calls()) != 0 {
		t.Error("ran dataform locally too")
	}
	assertContains(t, screen(m), "4 log · analytics.daily_orders · ✓ succeeded · 12.0s",
		"Dataform API run --actions demo-project.analytics.daily_orders --include-dependents",
		"⚠ 1 uncommitted file won't run", "✓ analytics.daily_orders · 3.0s", "✓ succeeded · 12.0s")

	// Turning a off runs locally again.
	_ = drive(t, m, nil, press("a", "r")...)
	if len(fr.calls()) != 1 || len(fa.opts) != 1 {
		t.Errorf("local runs %d, API runs %d; want 1 and 1", len(fr.calls()), len(fa.opts))
	}
}

func TestAPIRunOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name string
		res  project.RunResult
		want string
	}{
		{"failed", project.RunResult{State: dataformapi.StateFailed, ExitCode: 1, Duration: time.Second}, "✗ failed · 1.0s"},
		{"cancelled", project.RunResult{State: dataformapi.StateCancelled, Canceled: true, Duration: time.Second}, "✗ cancelled · 1.0s"},
		{"detached", project.RunResult{Detached: true, Duration: time.Second}, "stopped watching · 1.0s: the invocation carries on in GCP"},
		{"compile errors", project.RunResult{ExitCode: 1, Err: fmt.Errorf("2 compilation errors"), Duration: time.Second}, "✗ 2 compilation errors · 1.0s"},
	} {
		m, _ := newAPITestModel(t, &fakeAPIRunner{res: tt.res}, true)
		m = drive(t, m, nil, press("r")...)
		assertContains(t, screen(m), tt.want)
	}
}

func TestAPIRunStartErrorSaysHowToLogIn(t *testing.T) {
	fa := &fakeAPIRunner{err: fmt.Errorf("%w: could not find default credentials", dataformapi.ErrAuth)}
	m, _ := newAPITestModel(t, fa, true)
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "could not start the API run: no valid Google credentials", "gcloud auth application-default login")
	if m.running {
		t.Error("running after a failed start")
	}

	// With the project info to look in, it says where the account is.
	m, _ = newAPITestModel(t, fa, true)
	m.deps.Info = &ProjectInfo{ADC: func(context.Context) gcpauth.ADC { return gcpauth.ADC{} }}
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "press , to see the account xf uses")
}

// TestAPIRunCtrlCCancelsThenStopsWatching: the first ctrl+c cancels the
// invocation, the second stops watching it.
func TestAPIRunCtrlCCancelsThenStopsWatching(t *testing.T) {
	m, _ := newAPITestModel(t, &fakeAPIRunner{}, true)
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	cancelled, killed := false, false
	inner := m.cancelRun
	m.cancelRun = func() { cancelled = true; inner() }
	lines := make(chan string)
	next, _ = m.Update(runStartedMsg{&project.Run{Lines: lines, Done: make(chan project.RunResult, 1), Kill: func() { killed = true }}})
	m = next.(Model)

	next, _ = m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if !cancelled || killed {
		t.Fatalf("first ctrl+c: cancelled %v, killed %v", cancelled, killed)
	}
	assertContains(t, screen(m), "cancelling the workflow invocation (ctrl+c again stops watching)",
		"cancelling… ctrl+c stops watching", "ctrl+c stop watching")
	next, _ = m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if !killed {
		t.Error("second ctrl+c did not stop watching")
	}
	assertNotContains(t, screen(m), "force-killed")
}

// TestQuitDuringAPIRunLeavesItRunning: q asks, saying the run carries on, and
// the invocation's URL is printed on exit.
func TestQuitDuringAPIRunLeavesItRunning(t *testing.T) {
	m, _ := newAPITestModel(t, &fakeAPIRunner{}, true)
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	if got := m.ExitNotice(); got == "" {
		t.Error("no exit notice while the API run was starting")
	}
	next, _ = m.Update(runStartedMsg{&project.Run{Lines: make(chan string), Done: make(chan project.RunResult, 1), URL: func() string { return testInvocationURL }}})
	m = next.(Model)
	next, _ = m.Update(keyMsg("q"))
	m = next.(Model)
	assertContains(t, screen(m), "carries on in GCP: q again or y quits")
	next, cmd := m.Update(keyMsg("q"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("q q did not quit during an API run")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not quit")
	}
	if got, want := m.ExitNotice(), "the workflow invocation is still running in GCP: "+testInvocationURL; got != want {
		t.Errorf("ExitNotice = %q, want %q", got, want)
	}

	// A local run has no notice.
	m, _ = newAPITestModel(t, &fakeAPIRunner{}, false)
	next, _ = m.Update(keyMsg("r"))
	if got := next.(Model).ExitNotice(); got != "" {
		t.Errorf("ExitNotice for a local run = %q", got)
	}
}

// TestLogPaneOpensRunURL: u in the log pane opens the API run's workflow
// invocation, and says why not when there is none.
func TestLogPaneOpensRunURL(t *testing.T) {
	fa := &fakeAPIRunner{lines: []string{"↗ " + testInvocationURL}, res: project.RunResult{State: dataformapi.StateSucceeded}}
	m, _ := newAPITestModel(t, fa, true)
	var opened []string
	m.deps.OpenURL = func(u string) error { opened = append(opened, u); return nil }

	m = drive(t, m, nil, press("r", "4")...)
	assertContains(t, screen(m), "u open run in browser")
	m = drive(t, m, nil, press("u")...)
	if len(opened) != 1 || opened[0] != testInvocationURL {
		t.Fatalf("opened %q, want the invocation URL", opened)
	}

	// u elsewhere does nothing; a local run has no URL.
	m = drive(t, m, nil, press("0", "u")...)
	if len(opened) != 1 {
		t.Error("u opened the URL outside the log pane")
	}
	m = drive(t, m, nil, press("a", "r", "4", "u")...)
	if len(opened) != 1 {
		t.Error("u opened a URL after a local run")
	}
	assertContains(t, screen(m), "this run has none")
	assertNotContains(t, screen(m), "u open run in browser")

	// Help lists it only when a browser can be opened.
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "open run in browser")
	m.deps.OpenURL = nil
	assertNotContains(t, screen(m), "open run in browser")
}

func TestOpenRunURLWhileCompiling(t *testing.T) {
	m, _ := newAPITestModel(t, &fakeAPIRunner{}, true)
	m.deps.OpenURL = func(string) error { t.Error("opened a URL before there was one"); return nil }
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	next, _ = m.Update(runStartedMsg{&project.Run{Lines: make(chan string), Done: make(chan project.RunResult, 1), URL: func() string { return "" }}})
	m = next.(Model)
	m = drive(t, m, nil, press("4", "u")...)
	assertContains(t, screen(m), "no workflow invocation yet")
}

// testRunReport is a finished invocation of three actions: one that ran a
// job, one that failed in its job, and one that ran none.
func testRunReport() *project.RunReport {
	at := func(sec float64) time.Time {
		return time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC).Add(time.Duration(sec * float64(time.Second)))
	}
	target := func(name string) project.Target {
		return project.Target{Database: "demo-project", Schema: "analytics", Name: name}
	}
	return &project.RunReport{
		State: dataformapi.StateFailed, URL: testInvocationURL, Started: at(0), Ended: at(75),
		Actions: []project.RunAction{
			{Target: target("daily_orders"), State: "SUCCEEDED", Started: at(1), Ended: at(9),
				JobID: "job_1", JobURL: "https://console.cloud.google.com/bigquery?project=demo-project&j=bq:EU:job_1&page=queryresults",
				Job: &project.JobStats{Location: "EU", BytesBilled: 3 << 30, BytesProcessed: 3 << 30, CostUSD: 0.0183, SlotMs: 64000, Started: at(2), Ended: at(5.2)}},
			{Target: target("daily_orders_check"), State: "FAILED", FailureReason: "Not found: Table\n demo-project:analytics.missing", Started: at(9), Ended: at(11),
				JobID: "job_2", JobURL: "https://console.cloud.google.com/bigquery?project=demo-project&j=bq:EU:job_2&page=queryresults",
				Job: &project.JobStats{Location: "EU", BytesBilled: 10 << 20, CostUSD: 0.0001, SlotMs: 500, Started: at(9), Ended: at(10.5)}},
			{Target: target("report"), State: "SKIPPED"},
		},
	}
}

// TestAPIRunTable: the log pane shows an API run's actions as a table with
// what each cost and their totals; t swaps it with the log, and enter opens
// the BigQuery job of the action under the cursor.
func TestAPIRunTable(t *testing.T) {
	fa := &fakeAPIRunner{
		lines:  []string{"↗ " + testInvocationURL, "✓ analytics.daily_orders · 8.0s", "✗ failed analytics.daily_orders_check · 2.0s: Not found"},
		res:    project.RunResult{State: dataformapi.StateFailed, ExitCode: 1, Duration: 80 * time.Second},
		report: testRunReport(),
	}
	m, _ := newAPITestModel(t, fa, true)
	var opened []string
	m.deps.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	m = drive(t, m, nil, press("s", "r", "4", "z")...)
	view := screen(m)
	assertContains(t, view, "4 log · analytics.daily_orders · ✗ failed · 80.0s", "t log",
		"Target", "State", "Duration", "Slot time", "Billed", "Est. cost",
		"✓ succeeded", "3.2s", "1m 04s", "3.00 GiB", "$0.0183",
		"✗ failed", "1.5s", "0.5s", "10.00 MiB", "$0.0001",
		"analytics.report", "– skipped",
		"3/3 done", "✗ 1 failed", "– 1 skipped", "elapsed 1m 15s", "slot 1m 05s", "billed 3.01 GiB", "est. $0.0184",
		"t log", "enter open BigQuery job")
	assertNotContains(t, view, "↗ "+testInvocationURL)

	// enter opens the job under the cursor; an action with no job has none.
	m = drive(t, m, nil, press("j", "enter")...)
	if len(opened) != 1 || !strings.Contains(opened[0], "j=bq:EU:job_2") {
		t.Fatalf("opened %q, want job_2", opened)
	}
	m = drive(t, m, nil, press("G", "enter")...)
	if len(opened) != 1 {
		t.Errorf("opened %q for an action with no job", opened)
	}
	assertContains(t, screen(m), "this action ran no BigQuery job")
	assertNotContains(t, screen(m), "enter open BigQuery job")

	// t shows the log instead, and the next run's table starts hidden too.
	m = drive(t, m, nil, press("t")...)
	assertContains(t, screen(m), "↗ "+testInvocationURL, "t table")
	assertNotContains(t, screen(m), "Slot time")
	m = drive(t, m, nil, press("t")...)
	assertContains(t, screen(m), "Slot time")

	// Wide enough, the table says why an action failed.
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 170, Height: 40})
	assertContains(t, screen(m), "Failure", "Not found: Table demo-project:analytics.missing")

	// Narrow, it keeps the target, state, bytes and cost.
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 70, Height: 40})
	assertContains(t, screen(m), "daily_orders_check", "Billed", "Est. cost", "$0.0184")
	assertNotContains(t, screen(m), "Slot time")
}

// TestRunTableSearchAndSort: over the table / searches the actions by
// target, state or failure and S sorts them, as xf web's jobs table does;
// elsewhere those keys find an action and show the columns.
func TestRunTableSearchAndSort(t *testing.T) {
	fa := &fakeAPIRunner{res: project.RunResult{State: dataformapi.StateFailed, ExitCode: 1}, report: testRunReport()}
	m, _ := newAPITestModel(t, fa, true)
	var opened []string
	m.deps.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	m = drive(t, m, nil, press("s", "r", "4", "z")...)
	assertContains(t, screen(m), "/ search", "S sort")

	// Every word must match: "found" is in the failure.
	m = drive(t, m, nil, append(press("/"), typeText("orders found")...)...)
	view := screen(m)
	assertContains(t, view, "/orders found", "1 of 3", "analytics.daily_orders_check", "enter keep", "esc clear")
	assertNotContains(t, view, "S sort", "esc clear search")
	assertNotContains(t, view, "analytics.report", "✓ succeeded")
	// enter keeps the search, and the next opens the one job listed.
	m = drive(t, m, nil, press("enter", "enter")...)
	if len(opened) != 1 || !strings.Contains(opened[0], "j=bq:EU:job_2") {
		t.Fatalf("opened %q, want job_2", opened)
	}
	m = drive(t, m, nil, press("j")...) // the flash goes, and the footer is back
	assertContains(t, screen(m), "/orders found", "esc clear search")
	m = drive(t, m, nil, append(press("/"), typeText("zzz")...)...)
	assertContains(t, screen(m), "no job matches “orders foundzzz”")
	m = drive(t, m, nil, press("esc")...)
	assertContains(t, screen(m), "analytics.report", "✓ succeeded")
	if m.zoomed != true || m.focus != paneLog {
		t.Fatalf("esc left the table: zoomed %v, focus %v", m.zoomed, m.focus)
	}

	// S goes round the columns; by state, failures come first, and the
	// cursor stays on its action.
	m = drive(t, m, nil, press("S")...)
	assertContains(t, screen(m), "jobs by duration, biggest first", "DURATION")
	m = drive(t, m, nil, press("S", "S", "S", "S", "S")...)
	view = screen(m)
	assertContains(t, view, "jobs by state", "STATE")
	check, orders, report := strings.Index(view, "daily_orders_check"), strings.Index(view, "analytics.daily_orders  "), strings.Index(view, "analytics.report")
	if !(check < orders && orders < report) {
		t.Errorf("by state, rows at %d %d %d:\n%s", check, orders, report, view)
	}
	m = drive(t, m, nil, press("enter")...)
	if len(opened) != 2 || !strings.Contains(opened[1], "j=bq:EU:job_1") {
		t.Fatalf("opened %q, want job_1: the cursor follows its action", opened)
	}
	m = drive(t, m, nil, press("S")...)
	assertContains(t, screen(m), "jobs in the run's order")
	assertNotContains(t, screen(m), "STATE")

	// Out of the log pane, S is the columns again.
	m = drive(t, m, nil, press("z", "esc", "1", "S")...)
	if !m.cols.shown {
		t.Error("S in the Actions pane didn't show the columns")
	}
}

// TestRunTableOnlyForRunsWithActions: a local run, and an API run that
// never got as far as actions, show the log; t says there is no table.
func TestRunTableOnlyForRunsWithActions(t *testing.T) {
	fa := &fakeAPIRunner{lines: []string{"✗ definitions/a.sqlx: ref not found"},
		res: project.RunResult{ExitCode: 1, Err: fmt.Errorf("1 compilation error")}}
	m, _ := newAPITestModel(t, fa, true)
	m = drive(t, m, nil, press("r", "4", "t")...)
	assertContains(t, screen(m), "ref not found", "no run table yet")
	assertNotContains(t, screen(m), "t table", "t log")

	// A run after one with a table starts with none.
	fa.report = testRunReport()
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "Est. cost")
	fa.report = nil
	m = drive(t, m, nil, press("r")...)
	assertNotContains(t, screen(m), "Est. cost")
	assertContains(t, screen(m), "ref not found")
}

// TestRunTableScrollsToCursor: more actions than the pane has lines for
// scroll with the cursor.
func TestRunTableScrollsToCursor(t *testing.T) {
	rep := &project.RunReport{State: dataformapi.StateSucceeded}
	for i := range 30 {
		rep.Actions = append(rep.Actions, project.RunAction{
			Target: project.Target{Schema: "s", Name: fmt.Sprintf("action_%02d", i)}, State: "SUCCEEDED"})
	}
	m, _ := newAPITestModel(t, &fakeAPIRunner{res: project.RunResult{State: dataformapi.StateSucceeded}, report: rep}, true)
	m = drive(t, m, nil, press("r", "4")...)
	assertContains(t, screen(m), "s.action_00", "30/30 done")
	assertNotContains(t, screen(m), "s.action_29")
	m = drive(t, m, nil, press("G")...)
	assertContains(t, screen(m), "s.action_29", "30/30 done")
	assertNotContains(t, screen(m), "s.action_00")
	m = drive(t, m, nil, press("g")...)
	assertContains(t, screen(m), "s.action_00")
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		1200 * time.Millisecond: "1.2s", 33 * time.Second: "33s", 64 * time.Second: "1m 04s",
		126 * time.Minute: "2h 06m", -time.Second: "0.0s",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// fakeGit is the checkout an API run would see, as Deps.APIGit reads it.
type fakeGit struct {
	mu    sync.Mutex
	g     dataformapi.GitState
	reads int
}

func (f *fakeGit) read(context.Context) (dataformapi.GitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.g, nil
}

func (f *fakeGit) set(g dataformapi.GitState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.g = g
}

func TestAPIModeShowsWhatRunsInGCP(t *testing.T) {
	fg := &fakeGit{g: dataformapi.GitState{Branch: "feat", RemoteBranch: true, Ahead: 1, Behind: 3,
		Dirty: []string{"definitions/a.sqlx", "definitions/b.sqlx"}}}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{
		Root: "/work/demo", Engine: newFakeEngine(fc.compile), StartRun: (&fakeRunner{}).start,
		StartAPIRun: (&fakeAPIRunner{}).start, APIGit: fg.read, Now: testNow,
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startOn(t, closePalette(t, m), "demo-project.analytics.daily_orders")
	assertContains(t, screen(m), "─3 run─")
	assertNotContains(t, screen(m), "GCP", "origin/")
	if fg.reads != 0 || m.View().ReportFocus {
		t.Errorf("a off: %d git reads, ReportFocus %v", fg.reads, m.View().ReportFocus)
	}

	m = drive(t, m, nil, press("a")...)
	assertContains(t, screen(m), "─3 run · GCP─",
		"runs origin/feat · 2 uncommitted files, 1 unpushed commit won't run · 3 unpulled commits will run")
	if !m.View().ReportFocus {
		t.Error("a on: focus isn't reported, so a commit elsewhere goes unseen")
	}

	// Coming back to the terminal reads the checkout again.
	fg.set(dataformapi.GitState{Branch: "feat", RemoteBranch: true})
	m = drive(t, m, nil, tea.FocusMsg{})
	assertContains(t, screen(m), "runs origin/feat")
	assertNotContains(t, screen(m), "uncommitted", "won't run")

	fg.set(dataformapi.GitState{Branch: "feat"})
	m = drive(t, m, nil, tea.FocusMsg{})
	assertContains(t, screen(m), "origin/feat doesn't exist (as of the last fetch): push the branch first")

	// T says the same, and its title takes the accent.
	m = drive(t, m, nil, press("T")...)
	assertContains(t, screen(m), "T run tags · GCP", "origin/feat doesn't exist")
	m = drive(t, m, nil, press("esc", "a")...)
	assertNotContains(t, screen(m), "GCP", "origin/")
}

func TestAPIGitParts(t *testing.T) {
	text := func(parts []GitPart) string {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	for _, tt := range []struct {
		name  string
		g     dataformapi.GitState
		err   error
		known bool
		verb  string
		want  string
	}{
		{"unread", dataformapi.GitState{}, nil, false, "run", "runs the pushed branch"},
		{"not a repo", dataformapi.GitState{}, fmt.Errorf("/x is not in a git repository"), true, "run", "/x is not in a git repository"},
		{"detached", dataformapi.GitState{}, nil, true, "run", "HEAD is detached: API runs compile a pushed branch, so check one out"},
		{"clean", dataformapi.GitState{Branch: "main", RemoteBranch: true}, nil, true, "run", "runs origin/main"},
		{"unpulled, checked", dataformapi.GitState{Branch: "main", RemoteBranch: true, Behind: 1}, nil, true, "check",
			"checks origin/main · 1 unpulled commit will be checked"},
		{"unpushed", dataformapi.GitState{Branch: "main", RemoteBranch: true, Ahead: 2}, nil, true, "run",
			"runs origin/main · 2 unpushed commits won't run"},
	} {
		if got := text(apiGitParts(tt.g, tt.err, tt.known, tt.verb)); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	// A count lists what it counts, and how many more there are.
	parts := apiGitParts(dataformapi.GitState{Branch: "main", RemoteBranch: true, Ahead: 52,
		Unpushed: []string{"abc1234 one", "def5678 two"}}, nil, true, "run")
	if d := parts[3].Detail; !reflect.DeepEqual(d, []string{"abc1234 one", "def5678 two", "and 50 more"}) {
		t.Errorf("unpushed detail = %q", d)
	}
}
