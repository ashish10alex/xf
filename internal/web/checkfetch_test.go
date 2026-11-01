package web

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
)

// A check fetches origin first, so its base is the default branch as it is
// now: always when it asks to refresh, else at most once a minute.
func TestChecksFetchBase(t *testing.T) {
	fb := &fakeBranches{}
	deps := branchDeps(fb)
	var now atomic.Int64
	start := deps.Now()
	deps.Now = func() time.Time { return start.Add(time.Duration(now.Load())) }
	var checked atomic.Int32
	deps.ColumnImpact = func(context.Context, engine.ImpactRequest, func(impact.Progress)) (*impact.Report, error) {
		checked.Add(1)
		return &impact.Report{CheckedAt: start}, nil
	}
	_, ts, c := newTest(t, deps)
	check := func(refresh bool) {
		t.Helper()
		var v impactView
		postJSON(t, c, ts.URL+"/api/impact", map[string]any{"refresh": refresh, "token": "t"}, &v)
	}
	fetched := func() int {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.fetched
	}

	check(false)
	if fetched() != 1 || checked.Load() != 1 {
		t.Fatalf("first check: fetched %d, checked %d; want 1, 1", fetched(), checked.Load())
	}
	check(false)
	if fetched() != 1 {
		t.Errorf("a check within a minute fetched again: %d", fetched())
	}
	check(true)
	if fetched() != 2 {
		t.Errorf("a refresh fetched %d times in all, want 2", fetched())
	}
	now.Store(int64(2 * time.Minute))
	check(false)
	if fetched() != 3 {
		t.Errorf("a check after a minute fetched %d times in all, want 3", fetched())
	}
}
