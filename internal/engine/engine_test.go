package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	data, err = dataform.ExtractCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestFinishKeepsLastGood(t *testing.T) {
	good := loadGraph(t, "basic.json")
	broken := loadGraph(t, "compile_error.json")
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	boom := errors.New("boom")

	var s State
	s = s.Begin("", at).Finish(broken, nil, time.Second, at)
	if s.Graph != broken || s.Stale || len(s.Errors) == 0 || s.Compiling || s.Compiles != 1 {
		t.Fatalf("a broken first compile should still be used: %+v", s)
	}
	s = s.Finish(good, nil, 2*time.Second, at.Add(time.Minute))
	if s.Graph != good || s.Stale || len(s.Errors) != 0 || s.Took != 2*time.Second || !s.CompiledAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("a good compile should replace it: %+v", s)
	}
	s = s.Finish(broken, nil, time.Second, at.Add(2*time.Minute))
	if s.Graph != good || !s.Stale || len(s.Errors) == 0 || !s.CompiledAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("a broken compile replaced a good one: %+v", s)
	}
	s = s.Finish(nil, boom, time.Second, at)
	if s.Graph != good || !s.Stale || s.Errors != nil || s.Err != boom {
		t.Fatalf("a failed compile: %+v", s)
	}
	s = s.Finish(good, nil, time.Second, at)
	if s.Stale || s.Err != nil || s.Compiles != 5 {
		t.Fatalf("recovering: %+v", s)
	}

	var first State
	if first = first.Finish(nil, boom, 0, at); first.Graph != nil || first.Stale || first.Err != boom {
		t.Fatalf("a failed first compile: %+v", first)
	}
}

// next returns the next state on ch that satisfies ok.
func next(t *testing.T, ch <-chan State, ok func(State) bool) State {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-ch:
			if ok(s) {
				return s
			}
		case <-timeout:
			t.Fatal("no matching state")
		}
	}
}

func compiled(n int) func(State) bool {
	return func(s State) bool { return s.Compiles == n && !s.Compiling }
}

func TestLoopCompilesOnStartAndOnChange(t *testing.T) {
	g := loadGraph(t, "basic.json")
	changes := make(chan struct{})
	var calls atomic.Int32
	e := New(Config{
		Compile: func(context.Context) (*project.Graph, error) { calls.Add(1); return g, nil },
		Watch:   func(context.Context) (<-chan struct{}, error) { return changes, nil },
	})
	ch, cancel := e.Subscribe()
	defer cancel()
	if s := <-ch; !s.Compiling || s.Compiles != 0 {
		t.Fatalf("before Start the first compile is pending: %+v", s)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if s := next(t, ch, compiled(1)); s.Graph != g {
		t.Fatalf("first compile: %+v", s)
	}
	select {
	case <-e.FirstCompile():
	default:
		t.Fatal("FirstCompile not closed after the first compile")
	}

	changes <- struct{}{}
	if s := next(t, ch, func(s State) bool { return s.Compiling }); s.Reason != ReasonFileChanged {
		t.Errorf("reason = %q", s.Reason)
	}
	next(t, ch, compiled(2))
	e.Recompile("")
	next(t, ch, compiled(3))
	if calls.Load() != 3 {
		t.Errorf("compiles = %d, want 3", calls.Load())
	}

	// A late subscriber gets the current state straight away.
	late, cancelLate := e.Subscribe()
	defer cancelLate()
	if s := <-late; s.Compiles != 3 || s.Graph != g {
		t.Errorf("late subscriber got %+v", s)
	}
}

func TestLoopSupersedesCompileInFlight(t *testing.T) {
	g := loadGraph(t, "basic.json")
	started := make(chan struct{}, 4)
	var calls atomic.Int32
	e := New(Config{Compile: func(ctx context.Context) (*project.Graph, error) {
		started <- struct{}{}
		if calls.Add(1) == 1 {
			<-ctx.Done() // cancelled by the Recompile below
			return nil, ctx.Err()
		}
		return g, nil
	}})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	<-started
	e.Recompile("again")
	s := next(t, ch, func(s State) bool { return s.Compiles > 0 })
	if s.Compiles != 1 || s.Err != nil || s.Graph != g {
		t.Fatalf("the cancelled compile leaked into the state: %+v", s)
	}
}

func TestStartWithoutCompile(t *testing.T) {
	e := New(Config{})
	if s := e.State(); s.Compiling {
		t.Fatalf("nothing to compile, but compiling: %+v", s)
	}
	_ = e.Start(context.Background())
	select {
	case <-e.FirstCompile():
	case <-time.After(time.Second):
		t.Fatal("FirstCompile never closed")
	}
	e.Recompile("") // doesn't block
}

func TestStartReportsWatchError(t *testing.T) {
	g := loadGraph(t, "basic.json")
	e := New(Config{
		Compile: func(context.Context) (*project.Graph, error) { return g, nil },
		Watch:   func(context.Context) (<-chan struct{}, error) { return nil, errors.New("too many files") },
	})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := e.Start(ctx); err == nil {
		t.Fatal("want the watch error")
	}
	if s := next(t, ch, compiled(1)); s.WatchErr == nil {
		t.Error("the state doesn't say watching is off")
	}
	e.Recompile("")
	next(t, ch, compiled(2))
}

func TestCancelledSubscriberDoesNotBlock(t *testing.T) {
	g := loadGraph(t, "basic.json")
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }})
	_, cancelIdle := e.Subscribe() // never read
	cancelIdle()
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	next(t, ch, compiled(1))
	for i := 2; i < 40; i++ { // more states than a subscriber's buffer holds
		e.Recompile("")
		next(t, ch, compiled(i))
	}
}

type fakeBigQuery struct {
	mu      sync.Mutex
	calls   int
	release chan struct{} // when set, calls wait for it
	err     error
}

func (f *fakeBigQuery) dryRun(context.Context, string, string, string) (*bigquery.DryRun, error) {
	f.mu.Lock()
	f.calls++
	release, err := f.release, f.err
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise}, nil
}

func (f *fakeBigQuery) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestDryRunCache(t *testing.T) {
	bq := &fakeBigQuery{release: make(chan struct{})}
	e := New(Config{DryRun: bq.dryRun})
	ctx := context.Background()

	// Two callers asking for the same SQL share one call.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil || r.Bytes != 1<<20 {
				t.Errorf("got %v, %v", r, err)
			}
		}()
	}
	// A caller that gives up doesn't cancel the call the others wait on.
	short, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.DryRun(short, "p", "EU", "SELECT 1"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: got %v", err)
	}
	close(bq.release)
	wg.Wait()
	if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if n := bq.callCount(); n != 1 {
		t.Errorf("BigQuery calls = %d, want 1", n)
	}

	// The same SQL in another project or location is another dry run.
	_, _ = e.DryRun(ctx, "p", "US", "SELECT 1")
	_, _ = e.DryRun(ctx, "q", "EU", "SELECT 1")
	if n := bq.callCount(); n != 3 {
		t.Errorf("BigQuery calls = %d, want 3", n)
	}
}

func TestDryRunFailuresRetriedAfterCompile(t *testing.T) {
	g := loadGraph(t, "basic.json")
	bq := &fakeBigQuery{err: bigquery.ErrAuth}
	e := New(Config{
		Compile: func(context.Context) (*project.Graph, error) { return g, nil },
		DryRun:  bq.dryRun,
	})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	next(t, ch, compiled(1))

	for range 2 {
		if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); !errors.Is(err, bigquery.ErrAuth) {
			t.Fatalf("got %v", err)
		}
	}
	if n := bq.callCount(); n != 1 {
		t.Fatalf("a failure was retried before a compile: %d calls", n)
	}
	bq.mu.Lock()
	bq.err = nil // logged in
	bq.mu.Unlock()
	e.Recompile("")
	next(t, ch, compiled(2))
	if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil {
		t.Fatalf("after a compile: %v", err)
	}
	if n := bq.callCount(); n != 2 {
		t.Errorf("BigQuery calls = %d, want 2", n)
	}
}

func TestDryRunAnswersExpire(t *testing.T) {
	bq := &fakeBigQuery{}
	var mu sync.Mutex
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := New(Config{DryRun: bq.dryRun, Now: clock})
	ctx := context.Background()
	ask := func() {
		t.Helper()
		if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil {
			t.Fatal(err)
		}
	}
	ask()
	mu.Lock()
	now = now.Add(DryRunMaxAge - time.Second)
	mu.Unlock()
	ask()
	if n := bq.callCount(); n != 1 {
		t.Fatalf("a fresh answer was asked again: %d calls", n)
	}
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	ask()
	if n := bq.callCount(); n != 2 {
		t.Errorf("an old answer was kept: %d calls, want 2", n)
	}
}

func TestDryRunOff(t *testing.T) {
	if _, err := New(Config{}).DryRun(context.Background(), "p", "EU", "SELECT 1"); !errors.Is(err, ErrNoDryRun) {
		t.Errorf("got %v", err)
	}
}

func TestSetThemeTellsSubscribersOnce(t *testing.T) {
	e := New(Config{})
	states, cancel := e.Subscribe()
	defer cancel()
	<-states
	e.SetTheme("dark")
	if s := <-states; s.Theme != "dark" {
		t.Fatalf("theme %q", s.Theme)
	}
	e.SetTheme("dark") // unchanged: nobody is told
	e.SetTheme("light")
	if s := <-states; s.Theme != "light" {
		t.Errorf("theme %q after light, the unchanged set should say nothing", s.Theme)
	}
}
