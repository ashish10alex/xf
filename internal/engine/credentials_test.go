package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ashish10alex/xf/internal/bigquery"
)

// A new login (Config.Credentials moving on) has the failed dry runs
// asked again, with no compile.
func TestDryRunFailuresRetriedAfterLogin(t *testing.T) {
	bq := &fakeBigQuery{err: bigquery.ErrAuth}
	var gen atomic.Uint64
	e := New(Config{DryRun: bq.dryRun, Credentials: gen.Load})
	ctx := context.Background()
	for range 2 {
		if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); !errors.Is(err, bigquery.ErrAuth) {
			t.Fatalf("got %v", err)
		}
	}
	if n := bq.callCount(); n != 1 {
		t.Fatalf("a failure was retried with the same login: %d calls", n)
	}
	bq.mu.Lock()
	bq.err = nil
	bq.mu.Unlock()
	gen.Add(1)
	if _, err := e.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil {
		t.Fatalf("after a new login: %v", err)
	}
	if n := bq.callCount(); n != 2 {
		t.Errorf("BigQuery calls = %d, want 2", n)
	}
}
