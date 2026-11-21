package impact

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// TestWorkerPoolDoesNotBlockOnSlowHeadItem verifies that inBatches uses a
// bounded worker pool (not lockstep batches): item 0 blocking does not stop
// workers from progressing through items past batch size.
func TestWorkerPoolDoesNotBlockOnSlowHeadItem(t *testing.T) {
	releaseZero := make(chan struct{})
	doneTail := make(chan struct{})
	items := []int{0, 1, 2, 3}
	var finishedTail atomic.Int32
	var doneAll = make(chan struct{})

	go func() {
		inBatches(context.Background(), items, 2, func(i int) {
			if i == 0 {
				<-releaseZero
				return
			}
			if finishedTail.Add(1) == 3 {
				close(doneTail)
			}
		})
		close(doneAll)
	}()

	// With lockstep batches of 2, items 2 and 3 would never start while item 0
	// is blocked; with a worker pool, worker 2 completes items 1, 2 and 3.
	<-doneTail
	close(releaseZero)
	<-doneAll
}

// TestDeletedTableSchemaLookupsRunConcurrently verifies that deleted tables'
// schema checks run concurrently in the bounded worker pool.
func TestDeletedTableSchemaLookupsRunConcurrently(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	bothIn := make(chan struct{})
	res := &changes.Result{
		HeadRef: "feat", BaseRef: "main", MergeBase: "abc1234",
		Diff: changes.Diff{
			Deleted: []changes.Deleted{
				{ID: "p.marts.old_a", Type: "table", FileName: "a.sqlx"},
				{ID: "p.marts.old_b", Type: "table", FileName: "b.sqlx"},
			},
		},
	}
	deps := Deps{
		Schema: func(ctx context.Context, table string) ([]warehouse.Column, error) {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur >= 2 {
				select {
				case <-bothIn:
				default:
					close(bothIn)
				}
			} else {
				<-bothIn
			}
			inFlight.Add(-1)
			return []warehouse.Column{{Name: "id", Type: "INT64"}}, nil
		},
		Readers: emptyReaders{},
	}
	rep, err := Run(context.Background(), res, project.NewGraph(nil), deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("maxInFlight = %d, want >= 2 (concurrent deleted-table schema lookups)", maxInFlight.Load())
	}
	if len(rep.AtRisk) != 2 {
		t.Fatalf("AtRisk = %+v, want 2 deleted tables", rep.AtRisk)
	}
}

type emptyReaders struct{}

func (emptyReaders) ColumnLinks(context.Context, string, string) ([]lineage.ColumnLink, error) {
	return nil, nil
}
func (emptyReaders) TableOnlyReaders(context.Context, string, map[string]bool) ([]lineage.ColumnLink, error) {
	return nil, nil
}
func (emptyReaders) TableReaders(context.Context, string) ([]lineage.ColumnLink, error) {
	return nil, nil
}
