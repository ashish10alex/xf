package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/session"
)

// Defer to prod in xf serve (ADR 0031): the switch starts as the project
// was last left, else as deferToProd says, and a Dataform project's
// Deferrals compile it once more with prodCompilerOptions.

// addDefer gives the engine defer to prod, starting on as the session or
// settings say, and keeping the switch in the project's session.
func addDefer(cfg *engine.Config, d engine.Deferrer, root string, settings config.Loaded) {
	cfg.Defer = d
	cfg.DeferOn = settings.DeferToProd == "true"
	dir := sessionDir()
	if dir == "" {
		return
	}
	path := session.Path(dir, root)
	if s := session.Load(path); s.DeferToProd != nil {
		cfg.DeferOn = *s.DeferToProd
	}
	cfg.SaveDefer = func(on bool) {
		s := session.Load(path)
		s.Root, s.DeferToProd = root, &on
		_ = session.Save(path, s)
	}
}

// deferLookups makes the table lookups of bq's client.
func deferLookups(bq func(context.Context) (*bigquery.Client, error)) *deferral.Lookups {
	return deferral.NewLookups(func(ctx context.Context) (deferral.BigQuery, error) {
		c, err := bq(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}, nil)
}

// dataformDeferrer works out a Dataform project's Deferrals.
type dataformDeferrer struct {
	bin, root string
	lookups   *deferral.Lookups
	logger    *log.Logger
	// compile compiles the project with args; dataform.CompileWith but in
	// tests.
	compile func(ctx context.Context, args []string) (*project.Graph, error)
	// options are the prod compiler options the config files set now, for
	// a request that doesn't say.
	options func() string

	mu   sync.Mutex
	prod map[string]*prodCompile
}

// prodCompile is a compile with prod options: each action's Prod Target,
// and the actions it lacked when compiled again, which don't make it
// compile again.
type prodCompile struct {
	targets deferral.ProdTargets
	missing map[project.ActionID]bool
}

func newDataformDeferrer(bin, root string, bq func(context.Context) (*bigquery.Client, error), load func() (config.Loaded, error), logger *log.Logger) *dataformDeferrer {
	return &dataformDeferrer{bin: bin, root: root, lookups: deferLookups(bq), logger: logger, prod: map[string]*prodCompile{},
		options: func() string {
			s, err := load()
			if err != nil {
				return ""
			}
			return s.ProdCompilerOptions
		},
		compile: func(ctx context.Context, args []string) (*project.Graph, error) {
			return dataform.CompileWith(ctx, bin, root, args)
		}}
}

func (d *dataformDeferrer) Forget() { d.lookups.Clear(false) }

func (d *dataformDeferrer) Unreadable(ts []project.Target) bool { return d.lookups.MarkUnreadable(ts) }

func (d *dataformDeferrer) Deferral(ctx context.Context, req engine.DeferralRequest, g *project.Graph, changed func(project.ActionID) bool, on bool) (*engine.DeferralResult, error) {
	out := &engine.DeferralResult{}
	if req.Proxies {
		ps, err := deferral.Proxies(ctx, g, req.Actions, d.lookups)
		if err != nil {
			return nil, err
		}
		out.Proxies = ps
	}
	if !on {
		return out, nil
	}
	options := ""
	if req.ProdCompilerOptions != nil {
		options = *req.ProdCompilerOptions
	} else if d.options != nil {
		options = d.options()
	}
	p, err := d.prodTargets(ctx, options, g, req.Actions)
	if err != nil {
		return nil, err
	}
	dfr, err := deferral.Resolve(ctx, g, req.Actions, p, d.lookups, changed)
	if err != nil {
		return nil, err
	}
	out.Deferral = dfr
	return out, nil
}

// prodKey keeps a prod compile per options, project settings and day, as
// includes often read the date.
func (d *dataformDeferrer) prodKey(options string) string {
	var settings []byte
	for _, f := range []string{"workflow_settings.yaml", "dataform.json"} {
		if b, err := os.ReadFile(filepath.Join(d.root, f)); err == nil {
			settings = b
			break
		}
	}
	return options + "\x00" + string(settings) + "\x00" + time.Now().Format(time.DateOnly)
}

// prodTargets is the Prod Targets with options, compiling the project
// with them the first time, and again when an upstream action of selected
// is missing from them: one added since, say.
func (d *dataformDeferrer) prodTargets(ctx context.Context, options string, g *project.Graph, selected []project.ActionID) (deferral.ProdTargets, error) {
	args, err := dataform.SplitOptions(options)
	if err != nil {
		return deferral.ProdTargets{}, fmt.Errorf("%s: %w", config.KeyProdCompilerOptions, err)
	}
	key := d.prodKey(options)
	d.mu.Lock()
	defer d.mu.Unlock()
	pc := d.prod[key]
	if pc != nil {
		var missing []project.ActionID
		for _, id := range selected {
			a := g.Actions[id]
			if a == nil {
				continue
			}
			for _, t := range a.DependencyTargets {
				up := g.Actions[t.ID()]
				if up == nil || up.Kind.External() || pc.missing[up.ID] {
					continue
				}
				if _, ok := pc.targets.Of(up); !ok {
					missing = append(missing, up.ID)
				}
			}
		}
		if len(missing) == 0 {
			return pc.targets, nil
		}
		d.logger.Printf("defer to prod: %d upstream actions aren't in the prod compile; compiling again", len(missing))
		fresh, err := d.compileProd(ctx, options, args)
		if err != nil {
			return deferral.ProdTargets{}, err
		}
		for _, id := range missing {
			if _, ok := fresh.targets.Of(g.Actions[id]); !ok {
				fresh.missing[id] = true
			}
		}
		d.prod[key] = fresh
		return fresh.targets, nil
	}
	pc, err = d.compileProd(ctx, options, args)
	if err != nil {
		return deferral.ProdTargets{}, err
	}
	d.prod[key] = pc
	return pc.targets, nil
}

func (d *dataformDeferrer) compileProd(ctx context.Context, options string, args []string) (*prodCompile, error) {
	d.logger.Printf("defer to prod: compiling with %s %q", config.KeyProdCompilerOptions, options)
	g, err := d.compile(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("compiling with %s: %w", config.KeyProdCompilerOptions, err)
	}
	if g.HasErrors() {
		return nil, fmt.Errorf("compiling with %s %q: %s", config.KeyProdCompilerOptions, options, g.Errors[0].Message)
	}
	return &prodCompile{targets: deferral.NewProdTargets(g), missing: map[project.ActionID]bool{}}, nil
}

// addDeferDeps gives a front end defer to prod through the shared engine,
// and a BigQuery client of its own for Proxy Views.
func addDeferDeps(deps *frontend.Deps, client *remote.Client) {
	deps.Deferral = client.Deferral
	deps.SetDefer = client.SetDefer
	deps.DeferUnreadable = client.DeferUnreadable
	deps.DeferWriter = func(ctx context.Context) (deferral.Writer, error) {
		c, err := bigquery.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}
