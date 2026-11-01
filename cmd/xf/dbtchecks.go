package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
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
}

func newDbtTargets(b *dbt.Backend, root string, logger *log.Logger) *dbtTargets {
	return &dbtTargets{b: b, root: root, logger: logger, backends: map[string]*dbt.Backend{}, sources: map[string]*changes.Source{}}
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
// xf's. A compile with errors, or one that only parsed the SQL, is an
// error: neither has SQL to dry run.
func (t *dbtTargets) worktree(ctx context.Context, target string) (*project.Graph, error) {
	g, err := t.backend(target).Compile(ctx, t.root)
	if err != nil {
		return nil, fmt.Errorf("compiling with %s %q: %w", config.KeyDbtProdTarget, target, err)
	}
	if g.HasErrors() {
		return nil, fmt.Errorf("compiling with %s %q: %s", config.KeyDbtProdTarget, target, g.Errors[0].Message)
	}
	if err := compiledSQL(g); err != nil {
		return nil, fmt.Errorf("compiling with %s %q: %w", config.KeyDbtProdTarget, target, err)
	}
	return g, nil
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
func dbtImpact(bq func(context.Context) (*bigquery.Client, error), dryRun dryRunFunc, targets *dbtTargets) engine.ImpactFunc {
	lc := lazyLineage()
	return func(ctx context.Context, req engine.ImpactRequest, res *changes.Result, g *project.Graph, progress func(impact.Progress)) (*impact.Report, error) {
		if g == nil {
			return nil, changes.ErrWorktreeOnly
		}
		client, err := lc(ctx)
		if err != nil {
			return nil, err
		}
		target := req.DbtProdTarget
		var toProd func(string) string
		if targets.other(target) {
			progress(impact.Progress{Phase: "Compiling with " + config.KeyDbtProdTarget})
			prod, err := targets.worktree(ctx, target)
			if err != nil {
				return nil, err
			}
			var runs map[string]string
			res, runs = impact.Retarget(res, g, prod)
			g, toProd = prod, impact.ToProdFunc(runs)
		} else if err := compiledSQL(g); err != nil {
			return nil, err
		}
		tables := lineage.NewTables(lazyTables(bq))
		return impact.Run(ctx, res, g, impact.Deps{
			Schema:  tables.Schema,
			DryRun:  dryRun,
			Readers: lineage.NewSource(client, tables, g, toProd),
			ToProd:  toProd,
			Base: func(ctx context.Context) (*project.Graph, error) {
				return targets.commit(ctx, target, res.MergeBase)
			},
		}, progress)
	}
}
