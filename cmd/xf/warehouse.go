package main

import (
	"context"
	"errors"
	"sync"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// The warehouse xf talks to is made here, and only here: everything else
// takes a warehouse.Warehouse and type-asserts what only some warehouses do
// (ADR 0042). BigQuery is the only one.

// newWarehouse makes a client of the warehouse, failing with
// warehouse.ErrAuth when there are no credentials.
func newWarehouse(ctx context.Context) (warehouse.Warehouse, error) {
	c, err := bigquery.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// lazyClient makes a warehouse client on first use. A failed attempt (no
// credentials yet, say) is retried on the next call, so logging in takes
// effect without restarting; once made, the client follows later logins.
func lazyClient() func(ctx context.Context) (warehouse.Warehouse, error) {
	var mu sync.Mutex
	var client warehouse.Warehouse
	return func(ctx context.Context) (warehouse.Warehouse, error) {
		mu.Lock()
		defer mu.Unlock()
		if client == nil {
			c, err := newWarehouse(ctx)
			if err != nil {
				return nil, err
			}
			client = c
		}
		return client, nil
	}
}

// dryRunner checks queries with the client client makes.
func dryRunner(client func(context.Context) (warehouse.Warehouse, error)) func(ctx context.Context, project, location, sql string) (*warehouse.Check, error) {
	return func(ctx context.Context, project, location, sql string) (*warehouse.Check, error) {
		c, err := client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Check(ctx, project, location, sql)
	}
}

// lazyTables looks up table metadata with the client client makes.
func lazyTables(client func(context.Context) (warehouse.Warehouse, error)) func(ctx context.Context, project, dataset, table string) (*warehouse.Table, error) {
	return func(ctx context.Context, project, dataset, table string) (*warehouse.Table, error) {
		c, err := client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Table(ctx, project, dataset, table)
	}
}

// errNoViews is what defer to prod gets from a warehouse that can't hold
// Proxy Views.
var errNoViews = errors.New("this warehouse can't hold the views defer to prod makes")

// viewWriter is w's Proxy View side, for defer to prod.
func viewWriter(w warehouse.Warehouse) (warehouse.ViewWriter, error) {
	if v, ok := w.(warehouse.ViewWriter); ok {
		return v, nil
	}
	return nil, errNoViews
}

// newViewWriter makes a client of the warehouse for defer to prod's Proxy
// Views.
func newViewWriter(ctx context.Context) (deferral.Writer, error) {
	w, err := newWarehouse(ctx)
	if err != nil {
		return nil, err
	}
	return viewWriter(w)
}

// newJobLister makes a client of the warehouse that looks up jobs; nil when
// there are no credentials or the warehouse lists no jobs.
func newJobLister(ctx context.Context) warehouse.JobLister {
	w, err := newWarehouse(ctx)
	if err != nil {
		return nil
	}
	jobs, _ := w.(warehouse.JobLister)
	return jobs
}
