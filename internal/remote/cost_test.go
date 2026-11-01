package remote

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

type oneChange struct{}

func (oneChange) Changes(_ context.Context, req changes.Request, head []byte) (*changes.Result, error) {
	return &changes.Result{BaseRef: "origin/" + req.DefaultBranch, MergeBase: "abc1234def", HeadRef: "feat", Head: req.Head,
		Diff: changes.Diff{Changed: []changes.Changed{{ID: "proj.analytics.orders"}}}, HeadJSON: head}, nil
}

func (oneChange) Retry() {}

func TestQueryCostOverTheWire(t *testing.T) {
	path := socketPath(t)
	var fail error
	var mu sync.Mutex
	startServer(t, path, "/proj", engine.Config{
		Compile: fixed(loadGraph(t, "basic.json")),
		Changes: oneChange{},
		Cost: func(_ context.Context, req engine.CostRequest, _ *changes.Result, _ *project.Graph, progress func(cost.Progress)) (*cost.Report, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail != nil {
				return nil, fail
			}
			r := &cost.Report{Compiled: req.ProdCompilerOptions, Actions: []cost.Action{{Name: "proj.analytics.orders", Type: "table",
				Estimate: &cost.Estimate{Bytes: 42 << 30, Accuracy: "UPPER_BOUND"}, Unfiltered: []cost.Unfiltered{{Table: "proj.raw.events", Column: "day"}}}}}
			partial := *r
			partial.BasePending = true
			progress(cost.Progress{Phase: "Compiling the merge-base", Partial: &partial})
			r.Actions[0].Base = &cost.Estimate{Bytes: 2 << 30}
			return r, nil
		},
	}, time.Minute)
	c := connect(t, path, noStart)
	ctx := context.Background()

	var heard []cost.Progress
	r, err := c.QueryCost(ctx, engine.CostRequest{DefaultBranch: "main", Head: changes.HeadWorktree, ProdCompilerOptions: "--vars=env=prod"}, func(p cost.Progress) {
		mu.Lock() // the answer comes after its progress
		heard = append(heard, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	a := r.Actions[0]
	if d, ok := a.Delta(); r.Compiled != "--vars=env=prod" || a.Estimate.Bytes != 42<<30 || a.Estimate.Bound() != "≤ " || !ok || d != 40<<30 ||
		len(a.Unfiltered) != 1 || a.Unfiltered[0].Column != "day" || r.Signature == "" || r.BasePending {
		t.Errorf("report %+v", r)
	}
	mu.Lock()
	if len(heard) != 1 || heard[0].Partial == nil || !heard[0].Partial.BasePending || heard[0].Partial.Actions[0].Base != nil {
		t.Errorf("progress %+v", heard)
	}
	fail = errors.New("no valid Google credentials")
	mu.Unlock()

	if _, err := c.QueryCost(ctx, engine.CostRequest{DefaultBranch: "main", Head: changes.HeadWorktree, Refresh: true}, nil); err == nil || err.Error() != "no valid Google credentials" {
		t.Errorf("a failed check: %v", err)
	}
}
