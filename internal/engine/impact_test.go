package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeImpact counts checks; each waits for release before reporting.
type fakeImpact struct {
	mu      sync.Mutex
	calls   int
	graphs  []*project.Graph
	reqs    []ImpactRequest
	results []*changes.Result
	release chan struct{}
	err     error
}

func (f *fakeImpact) run(ctx context.Context, req ImpactRequest, res *changes.Result, g *project.Graph, progress func(impact.Progress)) (*impact.Report, error) {
	f.mu.Lock()
	f.calls++
	f.reqs = append(f.reqs, req)
	n := f.calls
	f.graphs = append(f.graphs, g)
	f.results = append(f.results, res)
	release, err := f.release, f.err
	f.mu.Unlock()
	progress(impact.Progress{Phase: "Dry running", Total: 1})
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
	return &impact.Report{ChangedCount: n, Comparison: impact.ComparisonOf(res)}, nil
}

func (f *fakeImpact) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sameChanges always reports the same Changed Action, so the signature
// only moves when its SQL does.
type sameChanges struct{ fakeChanges }

func (s *sameChanges) Changes(ctx context.Context, req changes.Request, head []byte) (*changes.Result, error) {
	res, err := s.fakeChanges.Changes(ctx, req, head)
	if res != nil {
		res.Changed = []changes.Changed{{ID: "proj.analytics.orders"}}
		res.HeadJSON = head
	}
	return res, err
}

func impactEngine(t *testing.T, g *project.Graph, cs ChangesSource, fi *fakeImpact) (*Engine, context.Context) {
	t.Helper()
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return dataform.ParseCompileJSON(g.JSON) }, Changes: cs, Impact: fi.run})
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	_ = e.Start(ctx)
	<-e.FirstCompile()
	return e, ctx
}

var worktree = ImpactRequest{DefaultBranch: "main", Head: changes.HeadWorktree}

func TestColumnImpactJoinsACheckUnderWayAndKeepsItsReport(t *testing.T) {
	fi := &fakeImpact{release: make(chan struct{})}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fi)

	type answer struct {
		r   *impact.Report
		err error
	}
	answers := make(chan answer, 2)
	var heard sync.WaitGroup
	heard.Add(2)
	for range 2 {
		go func() {
			var once sync.Once
			r, err := e.ColumnImpact(ctx, worktree, func(impact.Progress) { once.Do(heard.Done) })
			answers <- answer{r, err}
		}()
	}
	heard.Wait() // both hear the progress of the one check
	close(fi.release)
	for range 2 {
		a := <-answers
		if a.err != nil || a.r.ChangedCount != 1 || a.r.Stale || a.r.Signature == "" {
			t.Fatalf("answer %+v, %v", a.r, a.err)
		}
	}
	if n := fi.count(); n != 1 {
		t.Fatalf("%d checks, want 1", n)
	}

	// Asked again with nothing changed: the kept report, no new check.
	r, err := e.ColumnImpact(ctx, worktree, nil)
	if err != nil || r.ChangedCount != 1 || r.Stale || fi.count() != 1 {
		t.Fatalf("again: %+v, %v, %d checks", r, err, fi.count())
	}
	if st := e.State(); st.ImpactSignature != r.Signature {
		t.Errorf("state signature %q, report %q", st.ImpactSignature, r.Signature)
	}

	// Refresh checks again.
	r, err = e.ColumnImpact(ctx, ImpactRequest{DefaultBranch: "main", Head: changes.HeadWorktree, Refresh: true}, nil)
	if err != nil || r.ChangedCount != 2 {
		t.Fatalf("refresh: %+v, %v", r, err)
	}
}

func TestColumnImpactMarksAReportStaleOnceTheChangesMove(t *testing.T) {
	fi := &fakeImpact{}
	// fakeChanges names a new Changed Action every time it is asked.
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &fakeChanges{}, fi)
	first, err := e.ColumnImpact(ctx, worktree, nil)
	if err != nil || first.Stale {
		t.Fatalf("first: %+v, %v", first, err)
	}
	again, err := e.ColumnImpact(ctx, worktree, nil)
	if err != nil || !again.Stale || again.ChangedCount != 1 || fi.count() != 1 {
		t.Fatalf("again: %+v, %v, %d checks", again, err, fi.count())
	}
	if first.Stale {
		t.Error("marking stale changed the kept report")
	}
}

func TestColumnImpactCarriesOnWhenTheCallerStopsWaiting(t *testing.T) {
	fi := &fakeImpact{release: make(chan struct{})}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fi)
	wctx, cancel := context.WithCancel(ctx)
	started := make(chan struct{})
	var once sync.Once
	go func() {
		<-started
		cancel()
	}()
	if _, err := e.ColumnImpact(wctx, worktree, func(impact.Progress) { once.Do(func() { close(started) }) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	close(fi.release)
	r, err := e.ColumnImpact(ctx, worktree, nil)
	if err != nil || r.ChangedCount != 1 || fi.count() != 1 {
		t.Fatalf("after: %+v, %v, %d checks", r, err, fi.count())
	}
}

func TestColumnImpactForgetsAFailedCheck(t *testing.T) {
	fi := &fakeImpact{err: errors.New("boom")}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fi)
	if _, err := e.ColumnImpact(ctx, worktree, nil); err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
	fi.mu.Lock()
	fi.err = nil
	fi.mu.Unlock()
	if r, err := e.ColumnImpact(ctx, worktree, nil); err != nil || fi.count() != 2 {
		t.Fatalf("retry: %+v, %v, %d checks", r, err, fi.count())
	}
}

func TestColumnImpactOfThePushedCommitPassesItsCompile(t *testing.T) {
	fi := &fakeImpact{}
	pushed := loadGraph(t, "basic.json")
	cs := &pushedChanges{json: pushed.JSON}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), cs, fi)
	r, err := e.ColumnImpact(ctx, ImpactRequest{DefaultBranch: "main", Head: changes.HeadPushed}, nil)
	if err != nil || r.Comparison.HeadLabel != "pushed commit abc1234" {
		t.Fatalf("got %+v, %v", r, err)
	}
	fi.mu.Lock()
	defer fi.mu.Unlock()
	// The check is left to parse the pushed commit's compile, which only
	// its backend can read.
	if fi.graphs[0] != nil || string(fi.results[0].HeadJSON) != string(pushed.JSON) {
		t.Error("the check didn't get the pushed commit's compile")
	}
}

type pushedChanges struct{ json []byte }

func (p *pushedChanges) Changes(_ context.Context, req changes.Request, _ []byte) (*changes.Result, error) {
	return &changes.Result{Head: req.Head, HeadSHA: "abc1234def", HeadJSON: p.json}, nil
}

func (p *pushedChanges) Retry() {}

func TestColumnImpactOffAndRefusedOnCompileErrors(t *testing.T) {
	e := New(Config{})
	if _, err := e.ColumnImpact(context.Background(), worktree, nil); !errors.Is(err, ErrNoImpact) {
		t.Errorf("off: %v", err)
	}
	broken := loadGraph(t, "basic.json")
	broken.Errors = []project.CompilationError{{FileName: "a.sqlx", Message: "boom"}}
	e2 := New(Config{Compile: func(context.Context) (*project.Graph, error) { return broken, nil }, Changes: &sameChanges{}, Impact: (&fakeImpact{}).run})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e2.Start(ctx)
	if _, err := e2.ColumnImpact(ctx, worktree, nil); !errors.Is(err, ErrStale) {
		t.Errorf("compile errors: %v", err)
	}
}

func TestColumnImpactKeepsAReportPerCompilerOptions(t *testing.T) {
	fi := &fakeImpact{}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fi)
	if _, err := e.ColumnImpact(ctx, worktree, nil); err != nil {
		t.Fatal(err)
	}
	dev := worktree
	dev.DevCompilerOptions = "--schema-suffix=ashish"
	r, err := e.ColumnImpact(ctx, dev, nil)
	if err != nil || r.Stale || fi.count() != 2 {
		t.Fatalf("other options: %+v, %v, %d checks", r, err, fi.count())
	}
	prod := worktree
	prod.ProdCompilerOptions = "--default-database=acme-prod"
	r, err = e.ColumnImpact(ctx, prod, nil)
	if err != nil || r.Stale || fi.count() != 3 {
		t.Fatalf("prod options: %+v, %v, %d checks", r, err, fi.count())
	}
	fi.mu.Lock()
	defer fi.mu.Unlock()
	if fi.reqs[1].DevCompilerOptions != "--schema-suffix=ashish" || fi.reqs[2].ProdCompilerOptions != "--default-database=acme-prod" {
		t.Errorf("the checks got %+v", fi.reqs)
	}
}

// Whether prodCompilerOptions is set is the caller's to say, and no part of
// what was checked: the kept report serves callers either way.
func TestColumnImpactSaysTheSettingIsUnsetOnlyToTheCallerWhoSaidSo(t *testing.T) {
	fi := &fakeImpact{}
	e, ctx := impactEngine(t, loadGraph(t, "basic.json"), &sameChanges{}, fi)
	unset := worktree
	unset.Unset = "prodCompilerOptions"
	r, err := e.ColumnImpact(ctx, unset, nil)
	if err != nil || r.Unset != "prodCompilerOptions" {
		t.Fatalf("%+v, %v", r, err)
	}
	r, err = e.ColumnImpact(ctx, worktree, nil)
	if err != nil || r.Unset != "" || fi.count() != 1 {
		t.Fatalf("set: %+v, %v, %d checks", r, err, fi.count())
	}
}
