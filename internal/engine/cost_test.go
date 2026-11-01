package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeCost counts checks; each tells a partial report, then waits for
// release before reporting.
type fakeCost struct {
	mu      sync.Mutex
	calls   int
	graphs  []*project.Graph
	reqs    []CostRequest
	release chan struct{}
	err     error
}

func (f *fakeCost) run(ctx context.Context, req CostRequest, res *changes.Result, g *project.Graph, progress func(cost.Progress)) (*cost.Report, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.reqs = append(f.reqs, req)
	f.graphs = append(f.graphs, g)
	release, err := f.release, f.err
	f.mu.Unlock()
	report := &cost.Report{Comparison: impact.ComparisonOf(res), Actions: []cost.Action{{Name: "proj.analytics.orders", Estimate: &cost.Estimate{Bytes: int64(n)}}}}
	partial := *report
	partial.BasePending = true
	progress(cost.Progress{Phase: "Compiling the merge-base", Partial: &partial})
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return report, nil
}

func (f *fakeCost) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func costEngine(t *testing.T, g *project.Graph, cs ChangesSource, fc *fakeCost) (*Engine, context.Context) {
	t.Helper()
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return dataform.ParseCompileJSON(g.JSON) }, Changes: cs, Cost: fc.run})
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	_ = e.Start(ctx)
	<-e.FirstCompile()
	return e, ctx
}

var worktreeCost = CostRequest{DefaultBranch: "main", Head: changes.HeadWorktree}

// The check hears whether defer to prod is on, and keeps a report for
// each, as tables are read from dev only with it on.
func TestQueryCostFollowsDeferToProd(t *testing.T) {
	fc := &fakeCost{}
	g := loadGraph(t, "basic.json")
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return dataform.ParseCompileJSON(g.JSON) },
		Changes: &sameChanges{}, Cost: fc.run, Defer: &fakeDeferrer{}})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	<-e.FirstCompile()

	req := worktreeCost
	req.Defer = true // a client can't say: the engine does
	if _, err := e.QueryCost(ctx, req, nil); err != nil {
		t.Fatal(err)
	}
	e.SetDefer(true)
	if _, err := e.QueryCost(ctx, worktreeCost, nil); err != nil {
		t.Fatal(err)
	}
	e.SetDefer(false)
	if _, err := e.QueryCost(ctx, worktreeCost, nil); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.reqs) != 2 || fc.reqs[0].Defer || !fc.reqs[1].Defer {
		t.Fatalf("checks %+v, want one with defer off, one with it on, and the first kept for off", fc.reqs)
	}
}

func checked(r *cost.Report) int64 { return r.Actions[0].Estimate.Bytes }

func TestQueryCostJoinsACheckUnderWayAndKeepsItsReport(t *testing.T) {
	fc := &fakeCost{release: make(chan struct{})}
	e, ctx := costEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fc)

	type answer struct {
		r   *cost.Report
		err error
	}
	answers := make(chan answer, 2)
	partials := make(chan *cost.Report, 2)
	for range 2 {
		go func() {
			var once sync.Once
			r, err := e.QueryCost(ctx, worktreeCost, func(p cost.Progress) { once.Do(func() { partials <- p.Partial }) })
			answers <- answer{r, err}
		}()
	}
	// Both hear the one check's partial report, which says what it is of.
	for range 2 {
		if p := <-partials; p == nil || !p.BasePending || p.Signature == "" {
			t.Fatalf("partial %+v", p)
		}
	}
	close(fc.release)
	for range 2 {
		a := <-answers
		if a.err != nil || checked(a.r) != 1 || a.r.Stale || a.r.Signature == "" || a.r.BasePending {
			t.Fatalf("answer %+v, %v", a.r, a.err)
		}
	}
	if n := fc.count(); n != 1 {
		t.Fatalf("%d checks, want 1", n)
	}

	// Asked again with nothing changed: the kept report, no new check.
	r, err := e.QueryCost(ctx, worktreeCost, nil)
	if err != nil || checked(r) != 1 || r.Stale || fc.count() != 1 {
		t.Fatalf("again: %+v, %v, %d checks", r, err, fc.count())
	}
	// The state's signature is the report's, without column impact too.
	if st := e.State(); st.ImpactSignature != r.Signature {
		t.Errorf("state signature %q, report %q", st.ImpactSignature, r.Signature)
	}

	// Refresh checks again, and so do other compiler options.
	refresh := worktreeCost
	refresh.Refresh = true
	if r, err = e.QueryCost(ctx, refresh, nil); err != nil || checked(r) != 2 {
		t.Fatalf("refresh: %+v, %v", r, err)
	}
	prod := worktreeCost
	prod.ProdCompilerOptions = "--default-database=acme-prod"
	if r, err = e.QueryCost(ctx, prod, nil); err != nil || checked(r) != 3 || fc.reqs[2].ProdCompilerOptions == "" {
		t.Fatalf("prod options: %+v, %v", r, err)
	}
}

func TestQueryCostMarksAReportStaleOnceTheChangesMove(t *testing.T) {
	fc := &fakeCost{}
	// fakeChanges names a new Changed Action every time it is asked.
	e, ctx := costEngine(t, loadGraph(t, "basic.json"), &fakeChanges{}, fc)
	first, err := e.QueryCost(ctx, worktreeCost, nil)
	if err != nil || first.Stale {
		t.Fatalf("first: %+v, %v", first, err)
	}
	again, err := e.QueryCost(ctx, worktreeCost, nil)
	if err != nil || !again.Stale || fc.count() != 1 || first.Stale {
		t.Fatalf("again: %+v, %v, %d checks", again, err, fc.count())
	}
}

func TestQueryCostForgetsAFailedCheck(t *testing.T) {
	fc := &fakeCost{err: errors.New("boom")}
	e, ctx := costEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fc)
	if _, err := e.QueryCost(ctx, worktreeCost, nil); err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()
	if r, err := e.QueryCost(ctx, worktreeCost, nil); err != nil || fc.count() != 2 {
		t.Fatalf("retry: %+v, %v, %d checks", r, err, fc.count())
	}
}

func TestQueryCostOfThePushedCommitHasNoGraph(t *testing.T) {
	fc := &fakeCost{}
	e, ctx := costEngine(t, loadGraph(t, "basic.json"), &pushedChanges{json: loadGraph(t, "basic.json").JSON}, fc)
	r, err := e.QueryCost(ctx, CostRequest{DefaultBranch: "main", Head: changes.HeadPushed}, nil)
	if err != nil || r.Comparison.HeadLabel != "pushed commit abc1234" || r.Signature == "" {
		t.Fatalf("got %+v, %v", r, err)
	}
	// The checker parses the pushed commit's compile as its backend does.
	if fc.graphs[0] != nil {
		t.Error("the check got a graph for the pushed commit")
	}
}

func TestQueryCostOffAndRefusedOnCompileErrors(t *testing.T) {
	e := New(Config{})
	if _, err := e.QueryCost(context.Background(), worktreeCost, nil); !errors.Is(err, ErrNoCost) {
		t.Errorf("off: %v", err)
	}
	broken := loadGraph(t, "basic.json")
	broken.Errors = []project.CompilationError{{FileName: "a.sqlx", Message: "boom"}}
	e2 := New(Config{Compile: func(context.Context) (*project.Graph, error) { return broken, nil }, Changes: &sameChanges{}, Cost: (&fakeCost{}).run})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e2.Start(ctx)
	if _, err := e2.QueryCost(ctx, worktreeCost, nil); !errors.Is(err, ErrStale) {
		t.Errorf("compile errors: %v", err)
	}
}
