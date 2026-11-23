package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// dbtTargets compiles a dbt project for column impact and query cost with
// a target of their choosing, dbtProdTarget, as well as the one xf browses
// with: the working tree, and commits as snapshots, cached per commit and
// day. Each target gets its own backend, and so its own artifacts.
type dbtTargets struct {
	b      *dbt.Backend
	root   string
	logger *log.Logger

	mu       sync.Mutex
	backends map[string]*dbt.Backend
	sources  map[string]*changes.Source
	wt       map[string]*wtCompile
	// compileWT overrides working-tree compilation for tests.
	compileWT func(ctx context.Context, target string) (*project.Graph, error)
}

func newDbtTargets(b *dbt.Backend, root string, logger *log.Logger) *dbtTargets {
	return &dbtTargets{
		b:        b,
		root:     root,
		logger:   logger,
		backends: map[string]*dbt.Backend{},
		sources:  map[string]*changes.Source{},
		wt:       map[string]*wtCompile{},
	}
}

// other says whether target is another than the one xf compiles with, so
// it needs a compile of its own.
func (t *dbtTargets) other(target string) bool { return target != "" && target != t.b.Target }

// backend is the backend that compiles with target; xf's own for "" or
// xf's target.
func (t *dbtTargets) backend(target string) *dbt.Backend {
	if !t.other(target) {
		return t.b
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.backends[target]
	if b == nil {
		b = dbt.New(t.b.Bin, target, t.b.ArtifactDir, t.b.CompileWithHooks)
		t.backends[target] = b
	}
	return b
}

// source compiles commits with target, as snapshots (see
// dbt.Backend.CompiledBase). Changed Actions only parse theirs.
func (t *dbtTargets) source(target string) *changes.Source {
	b := t.backend(target)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sources[target]
	if s == nil {
		s = &changes.Source{
			Root:     t.root,
			Key:      backend.EngineName(b) + "\ncompiled",
			CacheDir: costCacheDir(t.logger),
			Mirror:   dbt.Mirror,
			Base: func(ctx context.Context, dir string) ([]byte, error) {
				t.logger.Printf("compiling a base snapshot in %s with target %q", dir, b.Target)
				return b.CompiledBase(ctx, t.root, dir)
			},
		}
		t.sources[target] = s
	}
	return s
}

// commit is the project compiled with target at commit sha.
func (t *dbtTargets) commit(ctx context.Context, target, sha string) (*project.Graph, error) {
	data, err := t.source(target).Commit(ctx, sha)
	if err != nil {
		return nil, err
	}
	return t.backend(target).Parse(data)
}

// worktree compiles the working tree with target, which is another than
// xf's, coalescing concurrent compiles and caching the graph for sig so
// parallel ColumnImpact and QueryCost never run two dbt compiles at once.
// A compile with errors, or one that only parsed the SQL, is an error:
// neither has SQL to dry run.
func (t *dbtTargets) worktree(ctx context.Context, target, sig string) (*project.Graph, error) {
	t.mu.Lock()
	if wc := t.wt[target]; wc.fresh(sig, time.Now()) {
		t.mu.Unlock()
		select {
		case <-wc.done:
			return wc.graph, wc.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	wc := &wtCompile{sig: sig, done: make(chan struct{})}
	t.wt[target] = wc
	compileFn := t.compileWT
	t.mu.Unlock()

	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changes.DefaultCompileTimeout)
		defer cancel()
		var g *project.Graph
		var err error
		if compileFn != nil {
			g, err = compileFn(cctx, target)
		} else {
			g, err = t.backend(target).Compile(cctx, t.root)
		}
		switch {
		case err != nil:
			err = fmt.Errorf("compiling with %s %q: %w", config.KeyDbtProdTarget, target, err)
		case g.HasErrors():
			err = fmt.Errorf("compiling with %s %q: %s", config.KeyDbtProdTarget, target, g.Errors[0].Message)
		default:
			if cerr := compiledSQL(g); cerr != nil {
				err = fmt.Errorf("compiling with %s %q: %w", config.KeyDbtProdTarget, target, cerr)
			}
		}
		if err != nil {
			wc.err = err
			t.mu.Lock()
			if t.wt[target] == wc {
				delete(t.wt, target)
			}
			t.mu.Unlock()
		} else {
			wc.graph = g
			t.mu.Lock()
			wc.finishedAt = time.Now()
			t.mu.Unlock()
		}
		close(wc.done)
	}()

	select {
	case <-wc.done:
		return wc.graph, wc.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// compiledSQL refuses a graph whose SQL dbt only parsed (dbt v2 with
// hooks, see dbt.Backend.Compile): its Jinja can't be dry run.
func compiledSQL(g *project.Graph) error {
	if g.Notice != "" {
		return errors.New(g.Notice)
	}
	return nil
}

// dbtImpact checks column impact for xf serve in a dbt project: schemas
// from BigQuery with the client bq makes, dry runs through dryRun (the
// engine's cached ones) and readers from Dataplex. With dbtProdTarget set
// to another target than xf's, the working tree is compiled with it and
// the check works on that compile alone: it dry runs what prod would build
// and compares it with prod's tables. The Changed Actions are named as
// that compile names them (impact.Retarget), and readers that are runs of
// xf's target fold into the actions they run. The merge-base is compiled
// with the same target, to tell the base branch's changes from the
// branch's.
//
// A project that builds in Databricks (ADR 0042) is checked there: schemas
// and readers from Unity Catalog, and the changed models planned with
// DESCRIBE QUERY on the SQL warehouse, which the report names.
func dbtImpact(pw *projectWarehouse, dryRun dryRunFunc, targets *dbtTargets) engine.ImpactFunc {
	lc := lazyLineage()
	jobCache := lineage.NewJobHistoryCache()
	bq := pw.bq
	return func(ctx context.Context, req engine.ImpactRequest, res *changes.Result, g *project.Graph, progress func(impact.Progress)) (*impact.Report, error) {
		if g == nil {
			return nil, changes.ErrWorktreeOnly
		}
		whKind := g.WarehouseOr(pw.dbtTarget().Type)
		onDemand := whKind == project.Databricks || whKind == project.Snowflake
		var client *lineage.Client
		if !onDemand {
			var err error
			if client, err = lc(ctx); err != nil {
				return nil, err
			}
		}
		target := req.DbtProdTarget
		var toProd func(string) string
		if targets.other(target) {
			progress(impact.Progress{Phase: "Compiling with " + config.KeyDbtProdTarget})
			prod, err := targets.worktree(ctx, target, impact.Signature(res, g))
			if err != nil {
				return nil, err
			}
			var runs map[string]string
			res, runs = impact.Retarget(res, g, prod)
			g, toProd = prod, impact.ToProdFunc(runs)
		} else if err := compiledSQL(g); err != nil {
			return nil, err
		}
		base := func(ctx context.Context) (*project.Graph, error) {
			return targets.commit(ctx, target, res.MergeBase)
		}
		if onDemand {
			extDays, _ := config.ParseQueryHistoryDays(req.QueryHistoryDays)
			var deps impact.Deps
			var used func() *impact.WarehouseUse
			var err error
			if whKind == project.Snowflake {
				deps, used, err = pw.snowflakeImpact(ctx, g, toProd, extDays)
			} else {
				deps, used, err = pw.databricksImpact(ctx, g, toProd, extDays)
			}
			if err != nil {
				return nil, err
			}
			deps.ToProd, deps.Base = toProd, base
			report, err := impact.Run(ctx, res, g, deps, progress)
			if report != nil {
				report.Warehouse = used()
			}
			return report, err
		}
		tables := lineage.NewTables(lazyTables(bq))
		readers := lineage.NewSource(client, tables, g, toProd).WithProjects(config.ParseLineageProjects(req.LineageProjects)...)
		configureJobHistory(readers, bq, req, jobCache)
		return impact.Run(ctx, res, g, impact.Deps{
			Schema:  tables.Schema,
			DryRun:  dryRun,
			Readers: readers,
			ToProd:  toProd,
			Base:    base,
		}, progress)
	}
}
