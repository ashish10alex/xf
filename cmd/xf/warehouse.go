package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/databricks"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/lsp"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
	"github.com/ashish10alex/xf/internal/web"
)

// The warehouse xf talks to is made here, and only here: everything else
// takes a warehouse.Warehouse and type-asserts what only some warehouses do
// (ADR 0042). A Dataform project builds in BigQuery; a dbt project in
// BigQuery or Databricks, as projectWarehouse says.

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

// projectWarehouse is the warehouse of the project at root (ADR 0042): its
// graph's, else the type of the dbt target it compiles with, else
// BigQuery. A Databricks project's SQL warehouse bills while it runs, so
// only what a user asks for checks on it: autoClient refuses.
type projectWarehouse struct {
	root, target string
	// load reads the project's settings afresh, for databricksWarehouse
	// and databricksProfile.
	load func() (config.Loaded, error)
	// graph is the project's latest compile, nil before one; nil for a
	// process that has none (xf web, which asks with kind).
	graph func() *project.Graph
	bq    func(ctx context.Context) (warehouse.Warehouse, error)

	once sync.Once
	dbt  dbt.Target

	mu  sync.Mutex
	dbx *databricks.Client
}

func newProjectWarehouse(root, target string, load func() (config.Loaded, error)) *projectWarehouse {
	return &projectWarehouse{root: root, target: target, load: load, bq: lazyClient()}
}

// dbtTarget is the dbt target the project compiles with, read once; empty
// for a Dataform project.
func (w *projectWarehouse) dbtTarget() dbt.Target {
	w.once.Do(func() {
		w.dbt, _ = dbt.TargetOf(w.root, w.target, os.Getenv)
	})
	return w.dbt
}

// kind is the warehouse the project builds in now.
func (w *projectWarehouse) kind() string {
	var g *project.Graph
	if w.graph != nil {
		g = w.graph()
	}
	return g.WarehouseOr(w.dbtTarget().Type)
}

// client is the project's warehouse client.
func (w *projectWarehouse) client(ctx context.Context) (warehouse.Warehouse, error) {
	return w.clientOf(ctx, w.kind())
}

// clientOf is the client of the warehouse kind.
func (w *projectWarehouse) clientOf(ctx context.Context, kind string) (warehouse.Warehouse, error) {
	if kind == project.Databricks {
		return w.databricks(), nil
	}
	return w.bq(ctx)
}

// errOnDemand is what a check xf would run on its own gets on Databricks.
var errOnDemand = errors.New("checks on Databricks run only when asked: xf web's Check")

// autoClient is the client of the checks xf runs on its own (dry runs on a
// save or a compile): none for Databricks.
func (w *projectWarehouse) autoClient(ctx context.Context) (warehouse.Warehouse, error) {
	if w.kind() == project.Databricks {
		return nil, errOnDemand
	}
	return w.bq(ctx)
}

// databricks is the Databricks client, made on first use. Until a SQL
// warehouse is chosen, each use looks for one again, so a pick saved in
// the settings is used from the next check.
func (w *projectWarehouse) databricks() *databricks.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dbx == nil {
		w.dbx = databricks.New(w.connection().Config)
	} else if w.dbx.Warehouse() == "" {
		w.dbx.SetWarehouse(w.connection().Warehouse)
	}
	return w.dbx
}

// connection is where the project's settings and dbt target say Databricks
// is.
func (w *projectWarehouse) connection() databricks.Connection {
	var s config.Settings
	if w.load != nil {
		if l, err := w.load(); err == nil {
			s = l.Settings
		}
	}
	t := w.dbtTarget()
	return databricks.Resolve(databricks.Setup{Warehouse: s.DatabricksWarehouse, Profile: s.DatabricksProfile,
		Host: t.Host, HTTPPath: t.HTTPPath, Token: t.Token, Catalog: t.Database})
}

// dbtTargetName is the target a dbt backend compiles with, "" for the
// profile's default or a Dataform backend.
func dbtTargetName(b backend.Backend) string {
	if d, ok := b.(*dbt.Backend); ok {
		return d.Target
	}
	return ""
}

// webDatabricks is what xf web needs of a dbt project that may build in
// Databricks: its workspace's client, links and SQL warehouses.
func (w *projectWarehouse) webDatabricks() *web.Databricks {
	return &web.Databricks{
		TargetType: w.dbtTarget().Type,
		Client: func(ctx context.Context) (warehouse.Warehouse, error) {
			return w.clientOf(ctx, project.Databricks)
		},
		Host: w.host,
		Warehouses: func(ctx context.Context) ([]web.SQLWarehouse, error) {
			list, err := w.databricks().Warehouses(ctx)
			out := make([]web.SQLWarehouse, 0, len(list))
			for _, s := range list {
				out = append(out, web.SQLWarehouse{ID: s.ID, Name: s.Name, State: s.State, Size: s.Size, Serverless: s.Serverless})
			}
			return out, err
		},
		Choose: func(id string) (string, error) {
			file, err := config.SetProjectKey(w.root, config.KeyDatabricksWarehouse, id)
			if err == nil {
				w.databricks().SetWarehouse(id)
			}
			return file, err
		},
		Chosen:   func() bool { return w.databricks().Warehouse() != "" },
		AuthHint: databricks.AuthHint,
	}
}

// host is the Databricks workspace's URL, for links.
func (w *projectWarehouse) host() string { return w.databricks().Host() }

// databricksCost is query cost on Databricks (ADR 0042), which bills SQL
// warehouse time, not bytes scanned, so nothing is estimated before a run:
// each changed model's latest run with target (the prod target, else the
// one xf compiles with), from Query History, which wakes no warehouse. dbt
// names the node and target of each query it runs in a comment.
func (w *projectWarehouse) databricksCost(ctx context.Context, res *changes.Result, g *project.Graph, target string, progress func(cost.Progress)) (*cost.Report, error) {
	target = cmp.Or(target, w.dbtTarget().Name)
	report := &cost.Report{Comparison: impact.ComparisonOf(res), Actions: []cost.Action{},
		PastRuns: &cost.PastRuns{Source: "Databricks Query History", Target: target, Days: databricks.HistoryDays, Runs: []cost.PastRun{}}}
	var models []*project.Action
	var changed []changes.Changed
	for _, c := range res.Changed {
		if a := g.Actions[c.ID]; a != nil && a.DbtName != nil && builds(a) {
			models, changed = append(models, a), append(changed, c)
		}
	}
	if len(models) > 0 {
		progress(cost.Progress{Phase: "Reading Databricks Query History"})
		h, err := w.databricks().DbtRuns(ctx, time.Now().AddDate(0, 0, -databricks.HistoryDays), target)
		if err != nil {
			return nil, err
		}
		report.PastRuns.Truncated = h.Truncated
		byNode := map[string]databricks.Run{}
		for id, r := range h.Runs {
			byNode[nodeKey(id)] = r
		}
		for i, a := range models {
			c := changed[i]
			pr := cost.PastRun{Name: string(c.ID), FileName: c.FileName, Type: c.Type, New: slices.Contains(c.Reasons, changes.ReasonNew)}
			n := a.DbtName
			key := n.Package + "." + n.Name
			if n.Version != "" {
				key += ".v" + n.Version
			}
			if r, ok := byNode[key]; ok {
				pr.Ran, pr.Started, pr.Duration, pr.ReadBytes, pr.ReadRows, pr.Rows, pr.Warehouse, pr.QueryID =
					true, r.Started, r.Duration, r.ReadBytes, r.ReadRows, r.ProducedRows, r.Warehouse, r.QueryID
			}
			report.PastRuns.Runs = append(report.PastRuns.Runs, pr)
		}
		cost.SortPastRuns(report.PastRuns.Runs)
		if len(h.Runs) == 0 {
			report.PastRuns.Why = historyWhy(h, target)
		}
	}
	report.CheckedAt = time.Now()
	return report, nil
}

// builds says a builds a table or view whose runs Query History has.
func builds(a *project.Action) bool {
	switch a.Kind {
	case project.KindTable, project.KindView, project.KindIncremental, project.KindMaterializedView, project.KindSnapshot:
		return true
	}
	return false
}

// nodeKey is a dbt node ID without its resource type: model.shop.orders is
// shop.orders, and a version's model.shop.orders.v2 shop.orders.v2.
func nodeKey(id string) string {
	_, rest, _ := strings.Cut(id, ".")
	return rest
}

// historyWhy says why Query History has no run of any model, when it
// isn't only that none ran.
func historyWhy(h *databricks.History, target string) string {
	switch {
	case h.Queries == 0:
		return fmt.Sprintf("Query History has no query of the last %d days that you can see.", databricks.HistoryDays)
	case h.Dbt == 0:
		return fmt.Sprintf("No query of the last %d days carries dbt's query comment, which names a query's model: is `query-comment` turned off in dbt_project.yml?", databricks.HistoryDays)
	case h.Targets[target] == 0:
		var seen []string
		for t := range h.Targets {
			seen = append(seen, "`"+cmp.Or(t, "(none)")+"`")
		}
		slices.Sort(seen)
		return fmt.Sprintf("dbt's queries of the last %d days are of target %s, not `%s`.", databricks.HistoryDays, strings.Join(seen, ", "), target)
	}
	return ""
}

// errNoImpactWarehouse is column impact on Databricks without a SQL
// warehouse to plan the changed models on.
var errNoImpactWarehouse = fmt.Errorf("%w: column impact plans the changed models on one: set databricksWarehouse (xf settings, or xf web asks), or the dbt target's http_path", warehouse.ErrNoWarehouse)

// databricksImpact is what column impact on Databricks works with (ADR
// 0042): schemas and readers from Unity Catalog, which wake nothing, and
// DESCRIBE QUERY of the changed models alone on the SQL warehouse, which
// running the check asks for. used says afterwards which warehouse the
// check used, and whether it started it; nil when no model needed it.
func (w *projectWarehouse) databricksImpact(ctx context.Context, g *project.Graph, toProd func(string) string) (deps impact.Deps, used func() *impact.WarehouseUse, err error) {
	c := w.databricks()
	if c.Warehouse() == "" {
		return deps, nil, errNoImpactWarehouse
	}
	// Its state now, before a check may start it; asking starts nothing.
	sw, err := c.SQLWarehouse(ctx)
	if err != nil {
		return deps, nil, err
	}
	var checked atomic.Int64
	tables := lineage.NewTables(c.Table)
	deps = impact.Deps{
		Schema: tables.Schema,
		DryRun: func(ctx context.Context, project, location, sql string) (*warehouse.Check, error) {
			checked.Add(1)
			return c.Check(ctx, project, location, sql)
		},
		Readers:  databricks.NewReaders(c, g, toProd),
		OnDemand: true,
	}
	used = func() *impact.WarehouseUse {
		if checked.Load() == 0 {
			return nil
		}
		return &impact.WarehouseUse{Name: "Databricks SQL warehouse `" + cmp.Or(sw.Name, sw.ID) + "`", Started: sw.State != "RUNNING", Lineage: "Unity Catalog"}
	}
	return deps, used, nil
}

// catalogTables is the language server's TablesFor: a Databricks project's
// hover cards read Unity Catalog, which wakes no SQL warehouse, and link to
// Catalog Explorer. open makes the project's warehouse once per root (nil
// when the project can't be opened); any other project's cards read
// BigQuery.
func catalogTables(open func(root string) *projectWarehouse) func(root string, g *project.Graph) *lsp.TableSource {
	var mu sync.Mutex
	byRoot := map[string]*projectWarehouse{}
	return func(root string, g *project.Graph) *lsp.TableSource {
		mu.Lock()
		w, seen := byRoot[root]
		if !seen {
			w = open(root)
			byRoot[root] = w
		}
		mu.Unlock()
		if w == nil || g.WarehouseOr(w.dbtTarget().Type) != project.Databricks {
			return nil
		}
		c := w.databricks()
		return &lsp.TableSource{Name: "Unity Catalog", Tables: c.Table, AuthHint: databricks.AuthHint,
			URL: func(t project.Target) string { return databricks.CatalogURL(c.Host(), t.Database, t.Schema, t.Name) }}
	}
}

// databricksTUINote is the TUI's line for a Databricks project.
const databricksTUINote = "Databricks project: dry runs, columns and cost are off here; xf web checks models on demand"

// hideDatabricks takes the warehouse's features out of the TUI's deps for a
// dbt project whose target is Databricks (ADR 0042): its checks bill a SQL
// warehouse and run only when asked, which the TUI, in maintenance mode,
// doesn't do. A project whose manifest says Databricks where its target
// didn't gets the engine's refusal on its dry run lines instead.
func hideDatabricks(deps *frontend.Deps, b backend.Backend, root string) {
	d, ok := b.(*dbt.Backend)
	if !ok {
		return
	}
	if t, _ := dbt.TargetOf(root, d.Target, os.Getenv); t.Type != project.Databricks {
		return
	}
	deps.DryRun, deps.ForgetFailedDryRuns, deps.QueryCost, deps.ColumnImpact = nil, nil, nil, nil
	deps.Deferral, deps.SetDefer = nil, nil
	deps.Notice = strings.TrimPrefix(deps.Notice+"; "+databricksTUINote, "; ")
}
