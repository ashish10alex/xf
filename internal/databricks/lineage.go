package databricks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// Unity Catalog records lineage from the queries that build its tables, at
// table and column level, and answers without a SQL warehouse: column
// impact asks it who reads what a branch drops (ADR 0042), as it asks
// Dataplex on BigQuery.

const (
	// maxLineageCalls is how many lineage calls run at once.
	maxLineageCalls = 8
	// probeColumns is how many of a reader's columns are probed to decide
	// whether Unity Catalog tracks its column lineage.
	probeColumns = 3
)

type lineageTable struct {
	Name    string `json:"name"`
	Catalog string `json:"catalog_name"`
	Schema  string `json:"schema_name"`
}

func (t lineageTable) full() string {
	if t.Name == "" || t.Catalog == "" || t.Schema == "" {
		return ""
	}
	return FullName(t.Catalog, t.Schema, t.Name)
}

// lineageGet calls a lineage endpoint with query parameters on GET (no request
// body, which some HTTP proxies reject on GET).
func (c *Client) lineageGet(ctx context.Context, path string, fields map[string]string) ([]byte, error) {
	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	return c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil)
}

// entityPrefixes identify non-table Databricks entity readers returned by
// Unity Catalog entity lineage (notebooks, jobs, dashboards, pipelines, queries).
var entityPrefixes = []string{"notebook:", "job:", "dashboard:", "pipeline:", "query:"}

func isEntityReader(name string) bool {
	for _, p := range entityPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Downstream is the tables and entities (notebooks, jobs, dashboards,
// pipelines, queries) that read table (catalog.schema.table), from Unity
// Catalog's table lineage.
func (c *Client) Downstream(ctx context.Context, table string) ([]string, error) {
	data, err := c.lineageGet(ctx, "/api/2.0/lineage-tracking/table-lineage", map[string]string{"table_name": table, "include_entity_lineage": "true"})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Downstreams []struct {
			Table     *lineageTable `json:"tableInfo"`
			Notebooks []struct {
				ID int64 `json:"notebook_id"`
			} `json:"notebookInfos"`
			Jobs []struct {
				ID int64 `json:"job_id"`
			} `json:"jobInfos"`
			Dashboards []struct {
				ID string `json:"dashboard_id"`
			} `json:"dashboardInfos"`
			LakeviewDashboards []struct {
				ID string `json:"dashboard_id"`
			} `json:"lakeviewDashboardInfos"`
			DBSQLDashboards []struct {
				ID string `json:"dashboard_id"`
			} `json:"dbsqlDashboardInfos"`
			Pipelines []struct {
				ID string `json:"pipeline_id"`
			} `json:"pipelineInfos"`
			Queries []struct {
				ID string `json:"query_id"`
			} `json:"queryInfos"`
		} `json:"downstreams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode table lineage: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && name != table && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, d := range raw.Downstreams {
		if d.Table != nil {
			add(d.Table.full())
		}
		for _, n := range d.Notebooks {
			if n.ID != 0 {
				add("notebook:" + strconv.FormatInt(n.ID, 10))
			}
		}
		for _, j := range d.Jobs {
			if j.ID != 0 {
				add("job:" + strconv.FormatInt(j.ID, 10))
			}
		}
		for _, db := range d.Dashboards {
			if db.ID != "" {
				add("dashboard:" + db.ID)
			}
		}
		for _, db := range d.LakeviewDashboards {
			if db.ID != "" {
				add("dashboard:" + db.ID)
			}
		}
		for _, db := range d.DBSQLDashboards {
			if db.ID != "" {
				add("dashboard:" + db.ID)
			}
		}
		for _, p := range d.Pipelines {
			if p.ID != "" {
				add("pipeline:" + p.ID)
			}
		}
		for _, q := range d.Queries {
			if q.ID != "" {
				add("query:" + q.ID)
			}
		}
	}
	return out, nil
}

// ColumnRef is a column of a table.
type ColumnRef struct{ Table, Column string }

// columnLineage is the columns a column feeds (down) or is fed by (up).
func (c *Client) columnLineage(ctx context.Context, table, column string) (down, up []ColumnRef, err error) {
	data, err := c.lineageGet(ctx, "/api/2.0/lineage-tracking/column-lineage", map[string]string{"table_name": table, "column_name": column})
	if err != nil {
		return nil, nil, err
	}
	type col struct {
		lineageTable
		Column string `json:"name"`
		Table  string `json:"table_name"`
	}
	var raw struct {
		Down []col `json:"downstream_cols"`
		Up   []col `json:"upstream_cols"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("decode column lineage: %w", err)
	}
	refs := func(cols []col) []ColumnRef {
		var out []ColumnRef
		for _, c := range cols {
			if c.Table == "" || c.Catalog == "" || c.Schema == "" || c.Column == "" {
				continue
			}
			out = append(out, ColumnRef{Table: FullName(c.Catalog, c.Schema, c.Table), Column: c.Column})
		}
		return out
	}
	return refs(raw.Down), refs(raw.Up), nil
}

// DownstreamColumns is the columns that read table.column.
func (c *Client) DownstreamColumns(ctx context.Context, table, column string) ([]ColumnRef, error) {
	down, _, err := c.columnLineage(ctx, table, column)
	return down, err
}

// Readers finds who reads a table's columns from Unity Catalog lineage; it
// is column impact's impact.Readers on Databricks. Lookups are kept for the
// life of the Readers, one impact check.
type Readers struct {
	c       *Client
	graph   *project.Graph
	toProd  func(string) string
	extDays int
	now     func() time.Time
	sem     chan struct{}

	mu       sync.Mutex
	tables   map[string]*tableCall
	tracked  map[string]*trackedCall
	ext      *extCall
	warnings []string
	warnSeen map[string]bool
}

// NewReaders reads lineage with c, and spots the project's assertions in
// graph. toProd, when not nil, is the table of the action a reader is a
// run of.
func NewReaders(c *Client, graph *project.Graph, toProd func(string) string) *Readers {
	if toProd == nil {
		toProd = func(t string) string { return t }
	}
	return &Readers{c: c, graph: graph, toProd: toProd, sem: make(chan struct{}, maxLineageCalls),
		tables: map[string]*tableCall{}, tracked: map[string]*trackedCall{}}
}

func (r *Readers) call(ctx context.Context, f func() error) error {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.sem }()
	return f()
}

func (r *Readers) isAssertion(table string) bool {
	if r.graph == nil {
		return false
	}
	a := r.graph.Actions[project.ActionID(r.toProd(table))]
	return a != nil && (a.Kind == project.KindAssertion || a.Kind == project.KindTest)
}

// tableCall is one table's lineage lookup, shared by everyone who asks
// while it runs; a failed one is asked again next time.
type tableCall struct {
	done    chan struct{}
	readers []string
	err     error
}

// downstream is table's table-level readers, asked once.
func (r *Readers) downstream(ctx context.Context, table string) ([]string, error) {
	r.mu.Lock()
	c := r.tables[table]
	if c != nil {
		r.mu.Unlock()
		select {
		case <-c.done:
			return c.readers, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c = &tableCall{done: make(chan struct{})}
	r.tables[table] = c
	r.mu.Unlock()
	c.err = r.call(ctx, func() error {
		var err error
		c.readers, err = r.c.Downstream(ctx, table)
		return err
	})
	if c.err != nil {
		r.mu.Lock()
		delete(r.tables, table)
		r.mu.Unlock()
	}
	close(c.done)
	return c.readers, c.err
}

// ColumnLinks is the columns that read table.column directly. Unity
// Catalog doesn't say whether a column is copied or computed, so every
// link is lineage.Other.
func (r *Readers) ColumnLinks(ctx context.Context, table, column string) ([]lineage.ColumnLink, error) {
	var refs []ColumnRef
	var err error
	if e := r.call(ctx, func() error { refs, err = r.c.DownstreamColumns(ctx, table, column); return err }); e != nil {
		return nil, e
	}
	var out []lineage.ColumnLink
	seen := map[ColumnRef]bool{}
	for _, ref := range refs {
		if seen[ref] || ref.Table == table {
			continue
		}
		seen[ref] = true
		out = append(out, lineage.ColumnLink{Table: ref.Table, Column: ref.Column, DependencyType: lineage.Other, Assertion: r.isAssertion(ref.Table)})
	}
	out = append(out, r.extColumnLinks(ctx, table, column)...)
	return out, nil
}

// TableOnlyReaders is the table/view readers of table, other than those in
// linked, whose columns Unity Catalog doesn't track, so they may read a column
// without a column link saying so. Non-table entities (notebooks, jobs,
// dashboards, pipelines, queries) are omitted here because Unity Catalog only
// tracks them at table level, so treating them as column readers would mark
// every dropped column critical; TableReaders still includes them for deleted
// tables.
func (r *Readers) TableOnlyReaders(ctx context.Context, table string, linked map[string]bool) ([]lineage.ColumnLink, error) {
	readers, err := r.downstream(ctx, table)
	if err != nil {
		return nil, err
	}
	tableReaders := make([]string, 0, len(readers))
	for _, t := range readers {
		if !isEntityReader(t) {
			tableReaders = append(tableReaders, t)
		}
	}
	untracked := lineage.UntrackedReaders(ctx, tableReaders, linked, func(ctx context.Context, reader string) bool {
		return r.hasColumnLineage(ctx, reader, table)
	}, r.toProd)
	out := make([]lineage.ColumnLink, len(untracked))
	for i, t := range untracked {
		out[i] = lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)}
	}
	return out, nil
}

// trackedCall is one reader's column-lineage probe, shared by everyone who
// asks while it runs; a failed probe is not cached, so transient errors
// don't turn a tracked reader into a permanent TableOnly false positive.
type trackedCall struct {
	done    chan struct{}
	tracked bool
}

// hasColumnLineage is whether any of reader's first few columns has
// upstream column lineage. A reader that can't be read counts as
// untracked for this call, so it is listed as one that may read the column,
// without caching the failure for later calls.
func (r *Readers) hasColumnLineage(ctx context.Context, reader, from string) bool {
	// Non-table entities (notebooks, jobs, dashboards, pipelines, queries)
	// never have Unity Catalog column lineage, so skip probing tables/{name}.
	if isEntityReader(reader) {
		return false
	}
	r.mu.Lock()
	c := r.tracked[reader]
	if c != nil {
		r.mu.Unlock()
		select {
		case <-c.done:
			return c.tracked
		case <-ctx.Done():
			return false
		}
	}
	c = &trackedCall{done: make(chan struct{})}
	r.tracked[reader] = c
	r.mu.Unlock()

	var cols []string
	catalog, schema, name := splitFull(reader)
	var probeErr error
	t, err := r.c.Table(ctx, catalog, schema, name)
	if err != nil {
		probeErr = err
	} else {
		for _, f := range t.Schema[:min(len(t.Schema), probeColumns)] {
			cols = append(cols, f.Name)
		}
	}
	for _, col := range cols {
		var up []ColumnRef
		err := r.call(ctx, func() error {
			var err error
			_, up, err = r.c.columnLineage(ctx, reader, col)
			return err
		})
		if err != nil {
			probeErr = err
			continue
		}
		if len(up) > 0 {
			c.tracked = true
			break
		}
	}
	if !c.tracked && probeErr != nil {
		r.mu.Lock()
		delete(r.tracked, reader)
		r.mu.Unlock()
	}
	close(c.done)
	return c.tracked
}

// splitFull is catalog.schema.table's three parts.
func splitFull(table string) (catalog, schema, name string) {
	parts := strings.SplitN(table, ".", 3)
	for len(parts) < 3 {
		parts = append([]string{""}, parts...)
	}
	return parts[0], parts[1], parts[2]
}

// TableReaders is every reader of table, as table-level links: who loses a
// table the branch deletes.
func (r *Readers) TableReaders(ctx context.Context, table string) ([]lineage.ColumnLink, error) {
	readers, err := r.downstream(ctx, table)
	if err != nil {
		return nil, err
	}
	out := make([]lineage.ColumnLink, 0, len(readers))
	for _, t := range readers {
		out = append(out, lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)})
	}
	out = append(out, r.extTableLinks(ctx, table)...)
	return out, nil
}
