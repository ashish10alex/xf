package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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
	"github.com/ashish10alex/xf/internal/snowflake"
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
	sf  *snowflake.Client
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
	switch kind {
	case project.Databricks:
		return w.databricks(), nil
	case project.Snowflake:
		return w.snowflake(), nil
	}
	return w.bq(ctx)
}

// errOnDemand is what a check xf would run on its own gets on Databricks or
// Snowflake.
var errOnDemand = errors.New("checks on this warehouse run only when asked: xf web's Check")

// autoClient is the client of the checks xf runs on its own (dry runs on a
// save or a compile): none for Databricks or Snowflake.
func (w *projectWarehouse) autoClient(ctx context.Context) (warehouse.Warehouse, error) {
	switch w.kind() {
	case project.Databricks, project.Snowflake:
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
		Chosen:      func() bool { return w.databricks().Warehouse() != "" },
		WarehouseID: func() string { return w.databricks().Warehouse() },
		AuthHint:    databricks.AuthHint,
	}
}

// host is the Databricks workspace's URL, for links.
func (w *projectWarehouse) host() string { return w.databricks().Host() }

// snowflake is the Snowflake client, made on first use. Until a virtual
// warehouse is chosen, each use checks the settings again so a pick saved in
// .xf/config.json takes effect on the next check.
func (w *projectWarehouse) snowflake() *snowflake.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sf == nil {
		w.sf = snowflake.New(w.snowflakeConnection().Config)
	} else if w.sf.Warehouse() == "" {
		w.sf.SetWarehouse(w.snowflakeConnection().Warehouse)
	}
	return w.sf
}

func (w *projectWarehouse) snowflakeConnection() snowflake.Connection {
	var s config.Settings
	if w.load != nil {
		if l, err := w.load(); err == nil {
			s = l.Settings
		}
	}
	t := w.dbtTarget()
	return snowflake.Resolve(snowflake.Setup{
		Warehouse:            s.SnowflakeWarehouse,
		Connection:           s.SnowflakeConnection,
		Account:              t.Account,
		Host:                 t.Host,
		User:                 t.User,
		Role:                 t.Role,
		TargetWarehouse:      t.Warehouse,
		Database:             t.Database,
		Schema:               t.Schema,
		Authenticator:        t.Authenticator,
		Token:                t.Token,
		Password:             t.Password,
		PrivateKeyPath:       t.PrivateKeyPath,
		PrivateKey:           t.PrivateKey,
		PrivateKeyPassphrase: t.PrivateKeyPassphrase,
	})
}

// webSnowflake is what xf web needs of a dbt project that may build in
// Snowflake: its account client, Snowsight links and virtual warehouses.
func (w *projectWarehouse) webSnowflake() *web.Snowflake {
	return &web.Snowflake{
		TargetType: w.dbtTarget().Type,
		Client: func(ctx context.Context) (warehouse.Warehouse, error) {
			return w.clientOf(ctx, project.Snowflake)
		},
		Host: func() string { return w.snowflake().Host() },
		TableURL: func(t project.Target) string {
			c := w.snowflake()
			return snowflake.SnowsightURL(cmp.Or(c.Account(), c.Host()), t.Database, t.Schema, t.Name)
		},
		Warehouses: func(ctx context.Context) ([]web.SQLWarehouse, error) {
			list, err := w.snowflake().Warehouses(ctx)
			out := make([]web.SQLWarehouse, 0, len(list))
			for _, s := range list {
				out = append(out, web.SQLWarehouse{ID: s.Name, Name: s.Name, State: s.State, Size: s.Size, Serverless: true})
			}
			return out, err
		},
		Choose: func(name string) (string, error) {
			file, err := config.SetProjectKey(w.root, config.KeySnowflakeWarehouse, name)
			if err == nil {
				w.snowflake().SetWarehouse(name)
			}
			return file, err
		},
		Chosen:         func() bool { return w.snowflake().Warehouse() != "" },
		WarehouseID:    func() string { return w.snowflake().Warehouse() },
		ConfigKey:      config.KeySnowflakeWarehouse,
		CatalogName:    "Snowflake",
		WarehouseLabel: "virtual warehouse",
		PaletteCommand: "Snowflake: choose the virtual warehouse",
		AuthHint:       snowflake.AuthHint,
	}
}

var errNoSnowflakeCostWarehouse = fmt.Errorf("%w: reading Snowflake Query History runs on a virtual warehouse: set snowflakeWarehouse (xf settings, or xf web asks), or the dbt target's warehouse", warehouse.ErrNoWarehouse)

// snowflakeCost is query cost on Snowflake (ADR 0044), which bills virtual
// warehouse compute time rather than bytes scanned: each changed model's
// latest run with target from Snowflake Query History. Unlike Databricks'
// REST Query History API, reading Snowflake's ACCOUNT_USAGE / INFORMATION_SCHEMA
// executes on the virtual warehouse and may resume a suspended one.
func (w *projectWarehouse) snowflakeCost(ctx context.Context, res *changes.Result, g *project.Graph, target string, progress func(cost.Progress)) (*cost.Report, error) {
	target = cmp.Or(target, w.dbtTarget().Name)
	report := &cost.Report{Comparison: impact.ComparisonOf(res), Actions: []cost.Action{},
		PastRuns: &cost.PastRuns{Source: "Snowflake Query History", Target: target, Days: snowflake.HistoryDays, NoReadRows: true, Runs: []cost.PastRun{}}}
	var models []*project.Action
	var changed []changes.Changed
	var nodeIDs []string
	for _, c := range res.Changed {
		if a := g.Actions[c.ID]; a != nil && a.DbtName != nil && builds(a) {
			models, changed = append(models, a), append(changed, c)
			nodeIDs = append(nodeIDs, dbtModelNodeID(a))
		}
	}
	if len(models) > 0 {
		sf := w.snowflake()
		if sf.Warehouse() == "" {
			return nil, errNoSnowflakeCostWarehouse
		}
		progress(cost.Progress{Phase: "Reading Snowflake Query History"})
		h, err := sf.DbtRunsFor(ctx, time.Now().AddDate(0, 0, -snowflake.HistoryDays), target, nodeIDs)
		if err != nil {
			return nil, err
		}
		report.PastRuns.Days = cmp.Or(h.Days, snowflake.HistoryDays)
		report.PastRuns.Truncated = h.Truncated
		report.PastRuns.WarehouseUsed = sf.Warehouse()
		byNode := map[string]snowflake.Run{}
		for id, r := range h.Runs {
			byNode[nodeKey(id)] = r
		}
		unfilteredByID := pastUnfiltered(ctx, g, models, func(ctx context.Context, t project.Target) []string {
			fields, err := sf.ClusterBy(ctx, t.Database, t.Schema, t.Name)
			if err != nil {
				return nil
			}
			return clusterKeyColumns(fields)
		})
		for i, a := range models {
			c := changed[i]
			pr := cost.PastRun{
				Name:       string(c.ID),
				FileName:   c.FileName,
				Type:       c.Type,
				New:        slices.Contains(c.Reasons, changes.ReasonNew) && c.RenamedFrom == "",
				Unfiltered: unfilteredByID[c.ID],
			}
			n := a.DbtName
			key := n.Package + "." + n.Name
			if n.Version != "" {
				key += ".v" + n.Version
			}
			if r, ok := byNode[key]; ok {
				pr.Ran, pr.Started, pr.Duration, pr.ReadBytes, pr.Warehouse, pr.QueryID =
					true, r.Started, r.Duration, r.ReadBytes, r.Warehouse, r.QueryID
				pr.WarehouseSize, pr.Credits, pr.StatementCount = r.WarehouseSize, r.Credits, r.StatementCount
				pr.RunCount, pr.RunsCapped, pr.MedianDuration, pr.MedianReadBytes = r.RunCount, h.Truncated, r.MedianDuration, r.MedianReadBytes
			}
			report.PastRuns.Runs = append(report.PastRuns.Runs, pr)
		}
		cost.SortPastRuns(report.PastRuns.Runs)
		if len(h.Runs) == 0 {
			report.PastRuns.Why = snowflakeHistoryWhy(h, target)
		}
	}
	report.CheckedAt = time.Now()
	return report, nil
}

func snowflakeHistoryWhy(h *snowflake.History, target string) string {
	days := cmp.Or(h.Days, snowflake.HistoryDays)
	switch {
	case h.Queries == 0:
		return fmt.Sprintf("Query History has no dbt query of the last %d days that you can see.", days)
	case h.Dbt == 0:
		return fmt.Sprintf("No query of the last %d days carries dbt's query comment, which names a query's model: is `query-comment` turned off in dbt_project.yml?", days)
	case h.Targets[target] == 0:
		var seen []string
		for t := range h.Targets {
			seen = append(seen, "`"+cmp.Or(t, "(none)")+"`")
		}
		slices.Sort(seen)
		return fmt.Sprintf("dbt's queries of the last %d days are of target %s, not `%s`.", days, strings.Join(seen, ", "), target)
	}
	return ""
}

var errNoSnowflakeImpactWarehouse = fmt.Errorf("%w: column impact checks the changed models and reads Snowflake lineage on one: set snowflakeWarehouse (xf settings, or xf web asks), or the dbt target's warehouse", warehouse.ErrNoWarehouse)

// snowflakeImpact is what column impact on Snowflake works with (ADR 0044):
// built table schemas from DESCRIBE TABLE, changed model schemas from
// SELECT * FROM (<query>) LIMIT 0, and downstream readers from
// SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY and OBJECT_DEPENDENCIES.
func (w *projectWarehouse) snowflakeImpact(ctx context.Context, g *project.Graph, toProd func(string) string) (deps impact.Deps, used func() *impact.WarehouseUse, err error) {
	c := w.snowflake()
	if c.Warehouse() == "" {
		return deps, nil, errNoSnowflakeImpactWarehouse
	}
	vw, err := c.VirtualWarehouse(ctx)
	if err != nil {
		return deps, nil, err
	}
	var checked atomic.Int64
	tables := lineage.NewTables(c.Table)
	readers := snowflake.NewReaders(c, g, toProd)
	deps = impact.Deps{
		Schema: tables.Schema,
		DryRun: func(ctx context.Context, project, location, sql string) (*warehouse.Check, error) {
			checked.Add(1)
			return c.Check(ctx, project, location, sql)
		},
		Readers:   readers,
		OnDemand:  true,
		Warehouse: project.Snowflake,
	}
	used = func() *impact.WarehouseUse {
		if checked.Load() == 0 && !readers.Used() {
			return nil
		}
		return &impact.WarehouseUse{Name: "Snowflake virtual warehouse `" + cmp.Or(vw.Name, c.Warehouse()) + "`", Started: vw.State != "STARTED", Lineage: "Snowflake Access History"}
	}
	return deps, used, nil
}

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
	var nodeIDs []string
	for _, c := range res.Changed {
		if a := g.Actions[c.ID]; a != nil && a.DbtName != nil && builds(a) {
			models, changed = append(models, a), append(changed, c)
			nodeIDs = append(nodeIDs, dbtModelNodeID(a))
		}
	}
	if len(models) > 0 {
		dbx := w.databricks()
		progress(cost.Progress{Phase: "Reading Databricks Query History"})
		h, err := dbx.DbtRunsFor(ctx, time.Now().AddDate(0, 0, -databricks.HistoryDays), target, nodeIDs)
		if err != nil {
			return nil, err
		}
		report.PastRuns.Truncated = h.Truncated
		byNode := map[string]databricks.Run{}
		for id, r := range h.Runs {
			byNode[nodeKey(id)] = r
		}
		unfilteredByID := pastUnfiltered(ctx, g, models, func(ctx context.Context, t project.Target) []string {
			cols, err := dbx.PartitionAndClusterColumns(ctx, t.Database, t.Schema, t.Name)
			if err != nil {
				return nil
			}
			return cols
		})
		for i, a := range models {
			c := changed[i]
			pr := cost.PastRun{
				Name:       string(c.ID),
				FileName:   c.FileName,
				Type:       c.Type,
				New:        slices.Contains(c.Reasons, changes.ReasonNew) && c.RenamedFrom == "",
				Unfiltered: unfilteredByID[c.ID],
			}
			n := a.DbtName
			key := n.Package + "." + n.Name
			if n.Version != "" {
				key += ".v" + n.Version
			}
			if r, ok := byNode[key]; ok {
				pr.Ran, pr.Started, pr.Duration, pr.ReadBytes, pr.ReadRows, pr.Warehouse, pr.QueryID =
					true, r.Started, r.Duration, r.ReadBytes, r.ReadRows, r.Warehouse, r.QueryID
				pr.WarehouseSize, pr.Serverless, pr.StatementCount = r.WarehouseSize, r.Serverless, r.StatementCount
				pr.RunCount, pr.RunsCapped, pr.MedianDuration, pr.MedianReadBytes = r.RunCount, h.Sampled || h.Truncated, r.MedianDuration, r.MedianReadBytes
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

// dbtModelNodeID returns a's dbt node_id as dbt's query comment writes it.
func dbtModelNodeID(a *project.Action) string {
	if a == nil || a.DbtName == nil {
		return ""
	}
	kind := "model"
	if a.Kind == project.KindSnapshot {
		kind = "snapshot"
	}
	id := kind + "." + a.DbtName.Package + "." + a.DbtName.Name
	if a.DbtName.Version != "" {
		id += ".v" + a.DbtName.Version
	}
	return id
}

var (
	bareIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	funcCallRE  = regexp.MustCompile(`(?i)^[A-Za-z_][A-Za-z0-9_]*\((.*)\)$`)
)

// clusterKeyColumns extracts bare column names from Snowflake CLUSTER BY
// expressions (e.g. "EVENT_DATE", "TO_DATE(EVENT_TS)", "DATE_TRUNC('DAY', TS)",
// "SUBSTRING(USER_ID, 1, 5)").
func clusterKeyColumns(fields []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		col := extractClusterCol(f)
		if col != "" && !seen[strings.ToLower(col)] {
			seen[strings.ToLower(col)] = true
			out = append(out, col)
		}
	}
	return out
}

func extractClusterCol(expr string) string {
	expr = strings.TrimSpace(expr)
	unquoted := strings.Trim(expr, `"`)
	if bareIdentRE.MatchString(unquoted) {
		return unquoted
	}
	m := funcCallRE.FindStringSubmatch(expr)
	if m == nil {
		return ""
	}
	for _, arg := range splitTopLevelArgs(m[1]) {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "'") {
			continue
		}
		if col := extractClusterCol(arg); col != "" {
			return col
		}
	}
	return ""
}

func splitTopLevelArgs(s string) []string {
	var out []string
	depth := 0
	inSingle := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if inSingle && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inSingle = !inSingle
		case '(':
			if !inSingle {
				depth++
			}
		case ')':
			if !inSingle && depth > 0 {
				depth--
			}
		case ',':
			if !inSingle && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// pastUnfiltered statically checks each changed model's compiled SQL against
// the partition/clustering columns of its direct upstream dependencies (free
// metadata-only checks; wakes no SQL or virtual warehouse).
func pastUnfiltered(ctx context.Context, g *project.Graph, models []*project.Action, keyCols func(context.Context, project.Target) []string) map[project.ActionID][]cost.Unfiltered {
	if g == nil || keyCols == nil || len(models) == 0 {
		return nil
	}
	cache := map[string][]string{}
	lookup := func(t project.Target) []string {
		key := string(t.ID())
		if cols, ok := cache[key]; ok {
			return cols
		}
		cols := keyCols(ctx, t)
		cache[key] = cols
		return cols
	}
	out := map[project.ActionID][]cost.Unfiltered{}
	for _, a := range models {
		sql := cmp.Or(a.IncrementalQuery, a.Query)
		if strings.TrimSpace(sql) == "" {
			continue
		}
		stripped := project.StripSQL(sql, project.StripOptions{BlankSingleStrings: true})
		var warns []cost.Unfiltered
		for _, dep := range g.Dependencies(a.ID) {
			if dep == nil || dep.Target.Name == "" {
				continue
			}
			for _, col := range lookup(dep.Target) {
				if !mentionsIdent(stripped, col) {
					warns = append(warns, cost.Unfiltered{Table: string(dep.Target.ID()), Column: col})
				}
			}
		}
		if len(warns) > 0 {
			out[a.ID] = warns
		}
	}
	return out
}

func mentionsIdent(sql, ident string) bool {
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(ident) + `($|[^A-Za-z0-9_])`)
	return re.MatchString(sql)
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
		Readers:   databricks.NewReaders(c, g, toProd),
		OnDemand:  true,
		Warehouse: project.Databricks,
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
		if w == nil {
			return nil
		}
		switch g.WarehouseOr(w.dbtTarget().Type) {
		case project.Databricks:
			c := w.databricks()
			return &lsp.TableSource{Name: "Unity Catalog", Tables: c.Table, AuthHint: databricks.AuthHint,
				URL: func(t project.Target) string { return databricks.CatalogURL(c.Host(), t.Database, t.Schema, t.Name) }}
		case project.Snowflake:
			c := w.snowflake()
			return &lsp.TableSource{Name: "Snowflake", Tables: c.Table, AuthHint: snowflake.AuthHint,
				URL: func(t project.Target) string {
					return snowflake.SnowsightURL(cmp.Or(c.Account(), c.Host()), t.Database, t.Schema, t.Name)
				}}
		}
		return nil
	}
}

// databricksTUINote and snowflakeTUINote are the TUI's line for an on-demand
// warehouse project.
const (
	databricksTUINote = "Databricks project: dry runs, columns and cost are off here; xf web checks models on demand"
	snowflakeTUINote  = "Snowflake project: dry runs, columns and cost are off here; xf web checks models on demand"
)

// hideDatabricks takes the warehouse's features out of the TUI's deps for a
// dbt project whose target is Databricks or Snowflake (ADR 0042, ADR 0044):
// their checks bill a warehouse and run only when asked, which the TUI, in
// maintenance mode, doesn't do.
func hideDatabricks(deps *frontend.Deps, b backend.Backend, root string) {
	d, ok := b.(*dbt.Backend)
	if !ok {
		return
	}
	var note string
	switch t, _ := dbt.TargetOf(root, d.Target, os.Getenv); t.Type {
	case project.Databricks:
		note = databricksTUINote
	case project.Snowflake:
		note = snowflakeTUINote
	default:
		return
	}
	deps.DryRun, deps.ForgetFailedDryRuns, deps.QueryCost, deps.ColumnImpact = nil, nil, nil, nil
	deps.Deferral, deps.SetDefer = nil, nil
	deps.Notice = strings.TrimPrefix(deps.Notice+"; "+note, "; ")
}
