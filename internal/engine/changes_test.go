package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeChanges answers from a base that is "cached" once a request with
// Compile has been made.
type fakeChanges struct {
	mu       sync.Mutex
	compiled bool
	reqs     []changes.Request
	heads    [][]byte
	retries  int
	err      error
}

func (f *fakeChanges) Changes(_ context.Context, req changes.Request, head []byte) (*changes.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	f.heads = append(f.heads, head)
	if f.err != nil {
		return nil, f.err
	}
	if req.Compile {
		f.compiled = true
	}
	if !f.compiled {
		return nil, nil
	}
	return &changes.Result{BaseRef: "origin/" + req.DefaultBranch, Head: req.Head,
		Diff: changes.Diff{Changed: []changes.Changed{{ID: project.ActionID("n" + string(rune('0'+len(f.reqs))))}}}}, nil
}

func (f *fakeChanges) Retry() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries++
}

func (f *fakeChanges) requests() []changes.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]changes.Request(nil), f.reqs...)
}

func TestChangedActionsFollowCompiles(t *testing.T) {
	g := loadGraph(t, "basic.json")
	fc := &fakeChanges{}
	// Every compile makes a new graph, as a real one does.
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return dataform.ParseCompileJSON(g.JSON) }, Changes: fc})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	next(t, ch, compiled(1))

	// Until a client names the branch, nothing is worked out.
	e.Recompile(ReasonFileChanged)
	next(t, ch, compiled(2))
	if reqs := fc.requests(); len(reqs) != 0 {
		t.Fatalf("worked out changes before anyone asked: %v", reqs)
	}

	// A request without Compile names the branch; the base isn't cached.
	res, err := e.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree})
	if err != nil || res != nil {
		t.Fatalf("uncached: %v, %v", res, err)
	}
	// R compiles it; the result shows in the state.
	res, err = e.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree, Compile: true})
	if err != nil || res == nil {
		t.Fatalf("compile: %v, %v", res, err)
	}
	if s := next(t, ch, func(s State) bool { return s.Changes != nil }); s.Changes != res {
		t.Errorf("state changes = %+v, want the result", s.Changes)
	}
	fc.mu.Lock()
	for _, h := range fc.heads {
		if string(h) != string(g.JSON) {
			t.Error("the head compared wasn't the current graph")
		}
	}
	fc.mu.Unlock()

	// Every later compile refreshes it from the cache, never compiling.
	e.Recompile(ReasonFileChanged)
	s := next(t, ch, func(s State) bool { return s.Compiles == 3 && s.Changes != nil && s.Changes != res })
	reqs := fc.requests()
	if last := reqs[len(reqs)-1]; last.Compile || last.DefaultBranch != "main" || last.Head != changes.HeadWorktree {
		t.Errorf("refresh asked %+v", last)
	}
	if s.Changes.BaseRef != "origin/main" {
		t.Errorf("state changes = %+v", s.Changes)
	}

	// The pushed commit is worked out on request only and never shown live.
	pushed, err := e.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadPushed, Compile: true})
	if err != nil || pushed == nil || pushed.Head != changes.HeadPushed {
		t.Fatalf("pushed: %v, %v", pushed, err)
	}
	if live := e.State().Changes; live == pushed || live.Head != changes.HeadWorktree {
		t.Error("the pushed result replaced the live one")
	}
}

func TestChangedActionsRefuseStale(t *testing.T) {
	good := loadGraph(t, "basic.json")
	broken := loadGraph(t, "compile_error.json")
	graphs := []*project.Graph{good, broken}
	var mu sync.Mutex
	fc := &fakeChanges{}
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) {
		mu.Lock()
		defer mu.Unlock()
		g := graphs[0]
		if len(graphs) > 1 {
			graphs = graphs[1:]
		}
		return g, nil
	}, Changes: fc})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	next(t, ch, compiled(1))
	e.Recompile(ReasonFileChanged)
	if s := next(t, ch, compiled(2)); !s.Stale {
		t.Fatalf("want a stale state: %+v", s)
	}
	if _, err := e.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree, Compile: true}); !errors.Is(err, ErrStale) {
		t.Errorf("err = %v, want ErrStale", err)
	}
	// An API run compiles the pushed commit, so local errors don't matter.
	if _, err := e.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadPushed, Compile: true}); err != nil {
		t.Errorf("pushed: err = %v", err)
	}
}

func TestChangedActionsRetryOnlyWhenAsked(t *testing.T) {
	g := loadGraph(t, "basic.json")
	fc := &fakeChanges{}
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }, Changes: fc})
	e.Recompile(ReasonFileChanged)
	if fc.retries != 0 {
		t.Error("a file change retried failed bases")
	}
	e.Recompile("")
	if fc.retries != 1 {
		t.Error("c didn't retry failed bases")
	}
}

func TestChangedActionsOff(t *testing.T) {
	e := New(Config{})
	if _, err := e.ChangedActions(context.Background(), changes.Request{DefaultBranch: "main"}); !errors.Is(err, ErrNoChanges) {
		t.Errorf("err = %v, want ErrNoChanges", err)
	}
}
