package remote

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeChanges has a compiled base once a request with Compile arrives.
type fakeChanges struct {
	mu       sync.Mutex
	compiled bool
	err      error
}

func (f *fakeChanges) Changes(_ context.Context, req changes.Request, _ []byte) (*changes.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.compiled = f.compiled || req.Compile
	if !f.compiled {
		return nil, nil
	}
	return &changes.Result{BaseRef: "origin/" + req.DefaultBranch, MergeBase: "abc1234def", HeadRef: "feat", Head: req.Head,
		Diff: changes.Diff{
			Changed: []changes.Changed{{ID: "demo-project.analytics.daily_orders", Target: project.Target{Database: "demo-project", Schema: "analytics", Name: "daily_orders"},
				FileName: "definitions/daily_orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}}},
			Deleted: []changes.Deleted{{ID: "demo-project.analytics.legacy", FileName: "definitions/legacy.sqlx", Type: "view"}},
		}}, nil
}

func (f *fakeChanges) Retry() {}

func TestClientChangedActions(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	fc := &fakeChanges{}
	startServer(t, path, "/proj", engine.Config{Compile: fixed(g), Changes: fc}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()
	next(t, states, compiled(1))
	ctx := context.Background()

	res, err := c.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree})
	if err != nil || res != nil {
		t.Fatalf("uncompiled base: %v, %v", res, err)
	}
	res, err = c.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree, Compile: true})
	if err != nil || res == nil {
		t.Fatalf("compiled: %v, %v", res, err)
	}
	if len(res.Changed) != 1 || res.Changed[0].Target.Name != "daily_orders" || res.Changed[0].Reasons[0] != changes.ReasonSQL ||
		len(res.Deleted) != 1 || res.BaseRef != "origin/main" {
		t.Errorf("result did not come across: %+v", res)
	}
	// The live set follows in the state.
	st := next(t, states, func(s engine.State) bool { return s.Changes != nil })
	if _, ok := st.Changes.Find("demo-project.analytics.daily_orders"); !ok {
		t.Errorf("state changes = %+v", st.Changes)
	}

	fc.mu.Lock()
	fc.err = errors.New("could not compile abc1234:\ndefinitions/x.sqlx: boom")
	fc.mu.Unlock()
	if _, err := c.ChangedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadPushed, Compile: true}); err == nil || err.Error() != "could not compile abc1234:\ndefinitions/x.sqlx: boom" {
		t.Errorf("err = %v", err)
	}
}

func TestClientColumnImpact(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	release := make(chan struct{})
	check := func(_ context.Context, _ engine.ImpactRequest, res *changes.Result, _ *project.Graph, progress func(impact.Progress)) (*impact.Report, error) {
		progress(impact.Progress{Phase: "Dry running", Done: 0, Total: 1})
		<-release
		return &impact.Report{Comparison: impact.ComparisonOf(res), ChangedCount: 1,
			AtRisk: []impact.Table{{Table: "demo-project.analytics.daily_orders", Type: "table",
				Columns: []impact.Column{{Column: "total", Change: impact.ColumnChange{Kind: impact.Dropped}, Readers: []impact.Reader{}}}}}}, nil
	}
	startServer(t, path, "/proj", engine.Config{Compile: fixed(g), Changes: &fakeChanges{}, Impact: check}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()
	next(t, states, compiled(1))

	heard := make(chan impact.Progress, 1)
	var once sync.Once
	go func() {
		p := <-heard
		if p.Phase != "Dry running" || p.Total != 1 {
			t.Errorf("progress %+v", p)
		}
		close(release)
	}()
	r, err := c.ColumnImpact(context.Background(), engine.ImpactRequest{DefaultBranch: "main", Head: changes.HeadWorktree},
		func(p impact.Progress) { once.Do(func() { heard <- p }) })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.AtRisk) != 1 || r.AtRisk[0].Columns[0].Change.Kind != impact.Dropped || r.Comparison.MergeBase != "abc1234def" || r.Signature == "" {
		t.Errorf("report did not come across: %+v", r)
	}
	// The signature of the live changes follows in the state.
	if st := next(t, states, func(s engine.State) bool { return s.ImpactSignature != "" }); st.ImpactSignature != r.Signature {
		t.Errorf("state signature %q, report %q", st.ImpactSignature, r.Signature)
	}
}
