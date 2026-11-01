package main

import (
	"cmp"
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
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

// Defer to prod in a dbt project (ADR 0031): dbt's own --defer. Prod's
// manifest is a parse of the merge-base with dbtProdTarget, kept by
// commit; while defer is on the engine's compiles defer to it, and a
// Deferral reads back from the compiled SQL what dbt read from prod.

// dbtDeferTimeout bounds parsing the merge-base with dbtProdTarget.
const dbtDeferTimeout = 5 * time.Minute

type dbtDeferrer struct {
	b      *dbt.Backend
	root   string
	logger *log.Logger
	// load reads the settings as the config files have them now.
	load func() (config.Loaded, error)
	// recompile has the engine compile again, once it is made.
	recompile func()

	mu      sync.Mutex
	sources map[string]*changes.Source
	// state is the directory compiles defer to, and prod its graph, for
	// key (target and merge-base).
	key   string
	state string
	prod  *project.Graph
	err   error
}

func newDbtDeferrer(b *dbt.Backend, root string, load func() (config.Loaded, error), logger *log.Logger) *dbtDeferrer {
	return &dbtDeferrer{b: b, root: root, load: load, logger: logger, sources: map[string]*changes.Source{}}
}

func (d *dbtDeferrer) Forget() {}

func (d *dbtDeferrer) Unreadable([]project.Target) bool { return false }

// source parses commits with target, keeping the manifests by commit.
func (d *dbtDeferrer) source(target string) *changes.Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sources[target]
	if s == nil {
		pb := dbt.New(d.b.Bin, target, d.b.ArtifactDir, d.b.CompileWithHooks)
		s = &changes.Source{
			Root:     d.root,
			Key:      backend.EngineName(pb) + "\ndefer",
			CacheDir: changesCacheDir(d.logger),
			Mirror:   dbt.Mirror,
			Base: func(ctx context.Context, dir string) ([]byte, error) {
				d.logger.Printf("defer to prod: parsing a snapshot in %s with target %q", dir, target)
				return pb.Base(ctx, d.root, dir)
			},
		}
		d.sources[target] = s
	}
	return s
}

// prodState is prod's manifest, written where --defer-state reads it, and
// its graph: a parse of the merge-base with dbtProdTarget.
func (d *dbtDeferrer) prodState(ctx context.Context) (string, *project.Graph, error) {
	settings, err := d.load()
	if err != nil {
		return "", nil, err
	}
	target := settings.DbtProdTarget
	if target == "" {
		return "", nil, fmt.Errorf("set %s to the profile target that builds prod, e.g. prod", config.KeyDbtProdTarget)
	}
	if target == d.b.Target {
		return "", nil, fmt.Errorf("%s is %q, the target xf compiles with, so there is nothing to read from prod", config.KeyDbtProdTarget, target)
	}
	base, err := changes.ResolveBase(ctx, d.root, cmp.Or(settings.DefaultBranch, config.DefaultBranch), changes.HeadWorktree)
	if err != nil {
		return "", nil, fmt.Errorf("finding the merge-base to defer to: %w", err)
	}
	key := target + "\x00" + base.MergeBase
	d.mu.Lock()
	if d.key == key && d.state != "" {
		state, prod := d.state, d.prod
		d.mu.Unlock()
		return state, prod, nil
	}
	d.mu.Unlock()
	manifest, err := d.source(target).Commit(ctx, base.MergeBase)
	if err != nil {
		return "", nil, fmt.Errorf("parsing %s with %s %q: %w", base.BaseRef, config.KeyDbtProdTarget, target, err)
	}
	prod, err := dbt.ParseManifest(manifest)
	if err != nil {
		return "", nil, err
	}
	state, err := d.b.WriteDeferState(d.root, manifest)
	if err != nil {
		return "", nil, err
	}
	d.mu.Lock()
	d.key, d.state, d.prod = key, state, prod
	d.mu.Unlock()
	return state, prod, nil
}

// switched follows the switch: on, the project compiles deferring to
// prod's manifest once it is parsed; off, it compiles as it was.
func (d *dbtDeferrer) switched(on bool) {
	go func() {
		state := ""
		if on {
			ctx, cancel := context.WithTimeout(context.Background(), dbtDeferTimeout)
			defer cancel()
			var err error
			state, _, err = d.prodState(ctx)
			d.mu.Lock()
			d.err = err
			d.mu.Unlock()
			if err != nil {
				d.logger.Printf("defer to prod is not applied: %v", err)
				return
			}
		}
		if d.b.DeferState() == state {
			return
		}
		d.b.SetDeferState(state)
		if d.recompile != nil {
			d.recompile()
		}
	}()
}

func (d *dbtDeferrer) Deferral(ctx context.Context, req engine.DeferralRequest, g *project.Graph, changed func(project.ActionID) bool, on bool) (*engine.DeferralResult, error) {
	if !on {
		return &engine.DeferralResult{}, nil
	}
	state, prod, err := d.prodState(ctx)
	if err != nil {
		return nil, err
	}
	if d.b.DeferState() != state {
		// Turned on before the manifest was ready, or the merge-base moved:
		// compile again with it, and the clients ask again.
		d.switched(true)
		return nil, errors.New("compiling with dbt --defer…")
	}
	out := &deferral.Deferral{}
	for _, r := range dbt.Deferred(g, prod, req.Actions) {
		if r.Prod == nil {
			out.BuiltInDev = append(out.BuiltInDev, deferral.BuiltInDev{Dev: r.Dev})
			continue
		}
		e := deferral.Entry{Dev: r.Dev, Prod: r.Prod, Status: deferral.Deferred}
		e.Stale = changed != nil && changed(r.Dev.ID())
		out.Entries = append(out.Entries, e)
	}
	return &engine.DeferralResult{Deferral: out, DbtState: state}, nil
}
