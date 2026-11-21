package snowflake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// Snowflake records downstream lineage in two ACCOUNT_USAGE views:
//   - SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY (Enterprise Edition, last 30 days):
//     column-level lineage in OBJECTS_MODIFIED[*].columns[*].directSources, and
//     table-level lineage in DIRECT_OBJECTS_ACCESSED -> OBJECTS_MODIFIED.
//   - SNOWFLAKE.ACCOUNT_USAGE.OBJECT_DEPENDENCIES (all editions):
//     views, materialized views and dynamic tables referencing a table.
//
// Readers fetches a table's lineage once per impact check and serves
// ColumnLinks, TableOnlyReaders and TableReaders from that cache.

const (
	// lineageDays is how far back ACCESS_HISTORY is read.
	lineageDays     = 30
	maxLineageCalls = 4
)

type tableLineage struct {
	// byCol maps lowercased source column name -> downstream column links.
	byCol map[string][]ColumnRef
	// trackedTables are downstream tables for which ACCESS_HISTORY recorded
	// column-level lineage (directSources).
	trackedTables map[string]bool
	// tableReaders are all downstream tables/views from ACCESS_HISTORY and
	// OBJECT_DEPENDENCIES.
	tableReaders []string
}

// ColumnRef is a column of a downstream table.
type ColumnRef struct{ Table, Column string }

// Readers finds who reads a Snowflake table's columns, implementing
// impact.Readers.
type Readers struct {
	c      *Client
	graph  *project.Graph
	toProd func(string) string
	// byUpper maps UPPER(actionID) -> canonical ActionID in graph so
	// Snowflake's uppercase ACCOUNT_USAGE names line up with dbt's casing.
	byUpper map[string]string
	sem     chan struct{}
	used    atomic.Int64

	mu    sync.Mutex
	calls map[string]*lineageCall
}

// Used reports whether Readers ran any lineage query on the virtual warehouse.
func (r *Readers) Used() bool { return r.used.Load() > 0 }

type lineageCall struct {
	done chan struct{}
	lin  *tableLineage
	err  error
}

// NewReaders reads lineage with c and spots the project's actions and tests in
// graph. toProd, when not nil, maps dev targets to prod targets.
func NewReaders(c *Client, graph *project.Graph, toProd func(string) string) *Readers {
	if toProd == nil {
		toProd = func(t string) string { return t }
	}
	byUpper := map[string]string{}
	if graph != nil {
		for id := range graph.Actions {
			byUpper[strings.ToUpper(string(id))] = string(id)
		}
	}
	return &Readers{
		c:       c,
		graph:   graph,
		toProd:  toProd,
		byUpper: byUpper,
		sem:     make(chan struct{}, maxLineageCalls),
		calls:   map[string]*lineageCall{},
	}
}

func (r *Readers) canonical(table string) string {
	if id, ok := r.byUpper[strings.ToUpper(table)]; ok {
		return id
	}
	return table
}

func (r *Readers) isAssertion(table string) bool {
	if r.graph == nil {
		return false
	}
	a := r.graph.Actions[project.ActionID(r.toProd(r.canonical(table)))]
	return a != nil && (a.Kind == project.KindAssertion || a.Kind == project.KindTest)
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

func (r *Readers) load(ctx context.Context, table string) (*tableLineage, error) {
	key := strings.ToUpper(table)
	r.mu.Lock()
	lc := r.calls[key]
	if lc != nil {
		r.mu.Unlock()
		select {
		case <-lc.done:
			return lc.lin, lc.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	lc = &lineageCall{done: make(chan struct{})}
	r.calls[key] = lc
	r.mu.Unlock()

	lc.err = r.call(ctx, func() error {
		var err error
		lc.lin, err = r.fetch(ctx, table)
		return err
	})
	if lc.err != nil {
		r.mu.Lock()
		delete(r.calls, key)
		r.mu.Unlock()
	}
	close(lc.done)
	return lc.lin, lc.err
}

// fetch queries ACCESS_HISTORY (column + table lineage) and OBJECT_DEPENDENCIES
// (view/MV/dynamic-table dependencies) for table. If ACCESS_HISTORY fails
// (e.g. Standard Edition), OBJECT_DEPENDENCIES alone still succeeds so view
// readers are reported as table-only.
func (r *Readers) fetch(ctx context.Context, table string) (*tableLineage, error) {
	lin := &tableLineage{
		byCol:         map[string][]ColumnRef{},
		trackedTables: map[string]bool{},
	}
	readerSet := map[string]bool{}
	addReader := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || strings.EqualFold(t, table) {
			return
		}
		t = r.canonical(t)
		if !readerSet[strings.ToUpper(t)] {
			readerSet[strings.ToUpper(t)] = true
			lin.tableReaders = append(lin.tableReaders, t)
		}
	}

	// 1. Column-level lineage from ACCESS_HISTORY.
	colSQL := fmt.Sprintf(`SELECT
  m.value:objectName::STRING AS target_table,
  mc.value:columnName::STRING AS target_col,
  ds.value:columnName::STRING AS source_col
FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
  LATERAL FLATTEN(input => h.OBJECTS_MODIFIED) m,
  LATERAL FLATTEN(input => m.value:columns) mc,
  LATERAL FLATTEN(input => mc.value:directSources) ds
WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
  AND UPPER(ds.value:objectName::STRING) = UPPER(%s)
  AND m.value:objectName IS NOT NULL
GROUP BY 1, 2, 3`, lineageDays, quoteLiteral(table))

	_, colErr := r.readColumnLineage(ctx, colSQL, table, lin, addReader)

	// 2. Table-level lineage from ACCESS_HISTORY (catches tables without column tracking).
	var accessTableErr error
	if colErr == nil {
		tblSQL := fmt.Sprintf(`SELECT DISTINCT
  m.value:objectName::STRING AS target_table
FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
  LATERAL FLATTEN(input => h.DIRECT_OBJECTS_ACCESSED) d,
  LATERAL FLATTEN(input => h.OBJECTS_MODIFIED) m
WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
  AND UPPER(d.value:objectName::STRING) = UPPER(%s)
  AND m.value:objectName IS NOT NULL`, lineageDays, quoteLiteral(table))
		r.used.Add(1)
		if st, err := r.c.execute(ctx, tblSQL); err == nil {
			idx := st.colIndex()
			for _, row := range st.Data {
				addReader(cellStr(row, idx, "target_table"))
			}
		} else {
			accessTableErr = err
		}
	}

	// 3. View / MV / dynamic table dependencies from OBJECT_DEPENDENCIES.
	db, sch, tbl := splitFull(table)
	var depErr error
	if db != "" && sch != "" && tbl != "" {
		depSQL := fmt.Sprintf(`SELECT DISTINCT
  REFERENCING_DATABASE || '.' || REFERENCING_SCHEMA || '.' || REFERENCING_OBJECT_NAME AS reader
FROM SNOWFLAKE.ACCOUNT_USAGE.OBJECT_DEPENDENCIES
WHERE UPPER(REFERENCED_DATABASE) = UPPER(%s)
  AND UPPER(REFERENCED_SCHEMA) = UPPER(%s)
  AND UPPER(REFERENCED_OBJECT_NAME) = UPPER(%s)`,
			quoteLiteral(db), quoteLiteral(sch), quoteLiteral(tbl))
		r.used.Add(1)
		if st, err := r.c.execute(ctx, depSQL); err == nil {
			idx := st.colIndex()
			for _, row := range st.Data {
				addReader(cellStr(row, idx, "reader"))
			}
		} else {
			depErr = err
		}
	}

	// Surface errors so column impact marks readers ❔ rather than falsely
	// reporting none when ACCESS_HISTORY could not be checked.
	if colErr != nil && depErr != nil {
		return nil, mapStatementError(colErr)
	}
	if colErr != nil && len(lin.tableReaders) == 0 {
		return nil, fmt.Errorf("Snowflake ACCESS_HISTORY unavailable (non-view readers cannot be checked): %w", mapStatementError(colErr))
	}
	if accessTableErr != nil {
		return nil, mapStatementError(accessTableErr)
	}
	slices.Sort(lin.tableReaders)
	return lin, nil
}

func (r *Readers) readColumnLineage(ctx context.Context, sql, table string, lin *tableLineage, addReader func(string)) (int, error) {
	r.used.Add(1)
	st, err := r.c.execute(ctx, sql)
	if err != nil {
		return 0, err
	}
	idx := st.colIndex()
	seen := map[string]map[ColumnRef]bool{}
	count := 0
	for _, row := range st.Data {
		target := cellStr(row, idx, "target_table")
		tcol := cellStr(row, idx, "target_col")
		scol := cellStr(row, idx, "source_col")
		if target == "" || scol == "" || strings.EqualFold(target, table) {
			continue
		}
		target = r.canonical(target)
		addReader(target)
		lin.trackedTables[strings.ToUpper(target)] = true
		key := strings.ToLower(scol)
		if seen[key] == nil {
			seen[key] = map[ColumnRef]bool{}
		}
		ref := ColumnRef{Table: target, Column: tcol}
		if !seen[key][ref] {
			seen[key][ref] = true
			lin.byCol[key] = append(lin.byCol[key], ref)
			count++
		}
	}
	return count, nil
}

// ColumnLinks returns the downstream columns that directly read table.column.
func (r *Readers) ColumnLinks(ctx context.Context, table, column string) ([]lineage.ColumnLink, error) {
	lin, err := r.load(ctx, table)
	if err != nil {
		return nil, err
	}
	refs := lin.byCol[strings.ToLower(column)]
	out := make([]lineage.ColumnLink, 0, len(refs))
	for _, ref := range refs {
		out = append(out, lineage.ColumnLink{
			Table:          ref.Table,
			Column:         ref.Column,
			DependencyType: lineage.Other,
			Assertion:      r.isAssertion(ref.Table),
		})
	}
	return out, nil
}

// TableOnlyReaders returns downstream readers of table not in linked whose
// column-level lineage ACCESS_HISTORY did not track (such as views from
// OBJECT_DEPENDENCIES).
func (r *Readers) TableOnlyReaders(ctx context.Context, table string, linked map[string]bool) ([]lineage.ColumnLink, error) {
	lin, err := r.load(ctx, table)
	if err != nil {
		return nil, err
	}
	untracked := lineage.UntrackedReaders(ctx, lin.tableReaders, linked, func(_ context.Context, reader string) bool {
		return lin.trackedTables[strings.ToUpper(reader)]
	}, r.toProd)
	out := make([]lineage.ColumnLink, len(untracked))
	for i, t := range untracked {
		out[i] = lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)}
	}
	return out, nil
}

// TableReaders returns every downstream reader of table at table level.
func (r *Readers) TableReaders(ctx context.Context, table string) ([]lineage.ColumnLink, error) {
	lin, err := r.load(ctx, table)
	if err != nil {
		return nil, err
	}
	out := make([]lineage.ColumnLink, len(lin.tableReaders))
	for i, t := range lin.tableReaders {
		out[i] = lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)}
	}
	return out, nil
}

// splitFull splits database.schema.table, respecting double-quoted identifiers
// that contain dots (e.g. "my.db"."my.sch"."my.tbl").
func splitFull(table string) (db, schema, name string) {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(table); i++ {
		c := table[i]
		if inQuote {
			if c == '"' {
				if i+1 < len(table) && table[i+1] == '"' {
					cur.WriteByte('"')
					i++
				} else {
					inQuote = false
				}
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '.':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, cur.String())
	for len(parts) < 3 {
		parts = append([]string{""}, parts...)
	}
	if len(parts) > 3 {
		parts = []string{parts[0], parts[1], strings.Join(parts[2:], ".")}
	}
	return parts[0], parts[1], parts[2]
}
