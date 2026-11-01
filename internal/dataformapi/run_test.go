package dataformapi

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

const invName = "projects/proj/locations/europe-west2/repositories/shop/workflowInvocations/42"

// fakeAPI replays polls: each GetWorkflowInvocation takes the next state and
// the actions that go with it (the last one repeats).
type fakeAPI struct {
	mu      sync.Mutex
	compile *CompilationResult
	compErr error
	// compileCfg is the CompilationConfig the compile was asked for.
	compileCfg CompilationConfig
	invoked    []InvocationConfig
	polls      []poll
	polled     int
	cancelled  int
	// cancelTo is the state polls report once cancel was called.
	cancelTo string
	// block makes polls wait until their context ends.
	block bool
	// got are the names of the invocations asked for.
	got []string
}

type poll struct {
	state   string
	actions []Action
	err     error
	timing  Timing
}

func (f *fakeAPI) CreateCompilationResult(ctx context.Context, repo Repository, commitish string, cfg CompilationConfig) (*CompilationResult, error) {
	f.compileCfg = cfg
	if f.compErr != nil {
		return nil, f.compErr
	}
	if f.compile != nil {
		return f.compile, nil
	}
	return &CompilationResult{Name: "cr/1", ResolvedGitCommitSHA: "abc1234ffff"}, nil
}

func (f *fakeAPI) CreateWorkflowInvocation(_ context.Context, _ Repository, cr string, cfg InvocationConfig) (*Invocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoked = append(f.invoked, cfg)
	return &Invocation{Name: invName, State: StateRunning}, nil
}

func (f *fakeAPI) current() poll {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelled > 0 && f.cancelTo != "" {
		return poll{state: f.cancelTo}
	}
	p := f.polls[min(f.polled, len(f.polls)-1)]
	return p
}

func (f *fakeAPI) GetWorkflowInvocation(ctx context.Context, name string) (*Invocation, error) {
	f.mu.Lock()
	f.got = append(f.got, name)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := f.current()
	if p.err != nil {
		f.mu.Lock()
		f.polled++
		f.mu.Unlock()
		return nil, p.err
	}
	return &Invocation{Name: invName, State: p.state, Timing: p.timing}, nil
}

func (f *fakeAPI) QueryWorkflowInvocationActions(context.Context, string) ([]Action, error) {
	p := f.current()
	f.mu.Lock()
	f.polled++
	f.mu.Unlock()
	return p.actions, nil
}

func (f *fakeAPI) CancelWorkflowInvocation(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled++
	return nil
}

func act(name, state string) Action {
	return Action{Target: Target{Database: "proj", Schema: "s", Name: name}, State: state}
}

var testOpts = Options{
	Repo:         Repository{Project: "proj", Location: "europe-west2"},
	Git:          GitState{Branch: "feat", Origin: "git@github.com:me/shop.git", RemoteBranch: true},
	Invocation:   InvocationConfig{IncludedTargets: []Target{{Database: "proj", Schema: "s", Name: "a"}}},
	PollInterval: time.Millisecond,
}

// collect reads a run to the end.
func collect(t *testing.T, r *project.Run) ([]string, project.RunResult) {
	t.Helper()
	var lines []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-r.Lines:
			if !ok {
				return lines, <-r.Done
			}
			lines = append(lines, l)
		case <-timeout:
			t.Fatalf("run did not finish; lines so far: %q", lines)
		}
	}
}

func TestRunLogsStateChanges(t *testing.T) {
	failed := act("b", "FAILED")
	failed.FailureReason = "Not found: Table proj:s.missing\n"
	failed.Timing.StartTime, failed.Timing.EndTime = "2026-09-30T10:00:00Z", "2026-09-30T10:00:02.5Z"
	api := &fakeAPI{polls: []poll{
		{state: StateRunning, actions: []Action{act("a", "RUNNING"), act("b", "PENDING")}},
		{state: StateRunning, actions: []Action{act("a", "RUNNING"), act("b", "PENDING")}},
		{state: StateRunning, actions: []Action{act("a", "SUCCEEDED"), act("b", "RUNNING")}},
		{state: StateFailed, actions: []Action{act("a", "SUCCEEDED"), failed}},
	}}
	opts := testOpts
	opts.Git.Dirty = []string{"definitions/a.sqlx"}
	r := Start(context.Background(), api, opts)
	lines, res := collect(t, r)
	want := []string{
		"⚠ 1 uncommitted file won't run: definitions/a.sqlx",
		"compiling origin/feat in shop (proj, europe-west2)…",
		"compiled commit abc1234",
		"↗ https://console.cloud.google.com/bigquery/dataform/locations/europe-west2/repositories/shop/workflows/42?project=proj",
		"▶ running s.a",
		"✓ s.a",
		"▶ running s.b",
		"✗ failed s.b · 2.5s: Not found: Table proj:s.missing",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if res.State != StateFailed || res.ExitCode != 1 || res.Canceled || res.Err != nil {
		t.Errorf("result = %+v", res)
	}
	if !reflect.DeepEqual(api.invoked, []InvocationConfig{opts.Invocation}) {
		t.Errorf("invoked = %+v", api.invoked)
	}
	if !strings.HasSuffix(r.URL(), "/workflows/42?project=proj") {
		t.Errorf("URL() = %q", r.URL())
	}
}

func TestRunStopsOnCompilationErrors(t *testing.T) {
	api := &fakeAPI{compile: &CompilationResult{Name: "cr/1", ResolvedGitCommitSHA: "abc1234",
		Errors: []CompilationError{{Message: "ref not found", Path: "definitions/a.sqlx", ActionTarget: &Target{Schema: "s", Name: "a"}}}}}
	lines, res := collect(t, Start(context.Background(), api, testOpts))
	if last := lines[len(lines)-1]; last != "✗ definitions/a.sqlx (s.a): ref not found" {
		t.Errorf("last line = %q", last)
	}
	if res.Err == nil || res.Err.Error() != "1 compilation error" || len(api.invoked) != 0 {
		t.Errorf("result = %+v, invoked %d", res, len(api.invoked))
	}
}

func TestRunCompilesWithTheNames(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateSucceeded}}}
	opts := testOpts
	opts.Compile = CompilationConfig{SchemaSuffix: "ashish", TablePrefix: "tmp"}
	lines, _ := collect(t, Start(context.Background(), api, opts))
	if api.compileCfg != opts.Compile {
		t.Errorf("compiled with %+v, want %+v", api.compileCfg, opts.Compile)
	}
	if want := "compiling origin/feat in shop (proj, europe-west2) with table prefix tmp, schema suffix ashish…"; !slices.Contains(lines, want) {
		t.Errorf("lines %q have no %q", lines, want)
	}
}

func TestRunRefusesWhatItCannotName(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Options)
		want string
	}{
		{"detached", func(o *Options) { o.Git.Branch = "" }, "HEAD is detached"},
		{"not pushed", func(o *Options) { o.Git.RemoteBranch = false }, "origin/feat doesn't exist"},
		{"no origin", func(o *Options) { o.Git.Origin = "" }, "pass --dataform-repo"},
		{"no location", func(o *Options) { o.Repo.Location = "" }, "--dataform-location"},
	} {
		opts := testOpts
		tt.edit(&opts)
		_, res := collect(t, Start(context.Background(), &fakeAPI{}, opts))
		if res.Err == nil || !strings.Contains(res.Err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tt.name, res.Err, tt.want)
		}
	}
	// An explicit repository name needs no origin.
	opts := testOpts
	opts.Git.Origin, opts.Repo.Name = "", "other"
	api := &fakeAPI{polls: []poll{{state: StateSucceeded}}}
	lines, res := collect(t, Start(context.Background(), api, opts))
	if res.Err != nil || !strings.Contains(strings.Join(lines, "\n"), "compiling origin/feat in other") {
		t.Errorf("result = %+v, lines %q", res, lines)
	}
}

func TestRunCancelThenDetach(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateRunning, actions: []Action{act("a", "RUNNING")}}}, cancelTo: StateCanceling}
	ctx, cancel := context.WithCancel(context.Background())
	r := Start(ctx, api, testOpts)
	for l := range r.Lines {
		if strings.HasPrefix(l, "▶ running") {
			break
		}
	}
	cancel()
	for l := range r.Lines {
		if strings.HasPrefix(l, "cancel requested") {
			break
		}
	}
	// Still CANCELING: the run keeps watching until Kill.
	r.Kill()
	_, res := collect(t, r)
	if !res.Detached || api.cancelled != 1 {
		t.Errorf("result = %+v, cancelled %d times", res, api.cancelled)
	}
}

func TestRunCancelledEnds(t *testing.T) {
	api := &fakeAPI{polls: []poll{{state: StateRunning}}, cancelTo: StateCancelled}
	ctx, cancel := context.WithCancel(context.Background())
	r := Start(ctx, api, testOpts)
	<-r.Lines // first warning-free line: compiling…
	cancel()
	_, res := collect(t, r)
	if !res.Canceled || res.Detached {
		t.Errorf("result = %+v", res)
	}
}

func TestRunGivesUpAfterFailedPolls(t *testing.T) {
	api := &fakeAPI{polls: []poll{{err: errors.New("503 unavailable")}}}
	lines, res := collect(t, Start(context.Background(), api, testOpts))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "5 failed checks") {
		t.Errorf("result = %+v", res)
	}
	if n := strings.Count(strings.Join(lines, "\n"), "could not check the invocation: 503 unavailable"); n != maxPollFailures {
		t.Errorf("%d poll warnings, want %d", n, maxPollFailures)
	}
}

func TestKillWhilePolling(t *testing.T) {
	api := &fakeAPI{block: true}
	r := Start(context.Background(), api, testOpts)
	for l := range r.Lines {
		if strings.HasPrefix(l, "↗") {
			break
		}
	}
	r.Kill()
	_, res := collect(t, r)
	if !res.Detached {
		t.Errorf("result = %+v", res)
	}
}

func TestRunExplainsAWrongLocation(t *testing.T) {
	api := &fakeAPI{compErr: &APIError{Status: 403, Message: "Location EU is not found or access is unauthorized."}}
	opts := testOpts
	opts.Repo.Location = "EU"
	lines, res := collect(t, Start(context.Background(), api, opts))
	all := strings.Join(lines, "\n")
	for _, want := range []string{"not a multi-region like EU", "xf init", "dataformLocation", "--dataform-location"} {
		if !strings.Contains(all, want) {
			t.Errorf("lines don't mention %q:\n%s", want, all)
		}
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "Location EU is not found") {
		t.Errorf("result = %+v", res)
	}
}
