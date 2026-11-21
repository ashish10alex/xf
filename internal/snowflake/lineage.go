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
	// column-level lineage (directSources or DIRECT_OBJECTS_ACCESSED.columns).
	trackedTables map[string]bool
	// tableReaders are all downstream tables/views from ACCESS_HISTORY and
	// OBJECT_DEPENDENCIES.
	tableReaders []string
	// partialErr is set when one of ACCESS_HISTORY or OBJECT_DEPENDENCIES
	// failed while the other succeeded, so callers surface both the known
	// readers and the warning that lineage is incomplete.
	partialErr error
	// noAccessHistory is set when ACCESS_HISTORY failed, so ColumnLinks
	// falls back to returning OBJECT_DEPENDENCIES views as TableOnly.
	noAccessHistory bool
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

// Preload fetches lineage for all uncached tables in 2 batched ACCOUNT_USAGE
// queries total (one on ACCESS_HISTORY, one on OBJECT_DEPENDENCIES) instead of
// running per-table queries. Concurrent load calls for the same tables share
// the in-flight batch.
func (r *Readers) Preload(ctx context.Context, tables []string) {
	if len(tables) == 0 {
		return
	}
	var pendingTables []string
	var pendingCalls []*lineageCall
	r.mu.Lock()
	for _, t := range tables {
		key := strings.ToUpper(strings.TrimSpace(t))
		if key == "" || r.calls[key] != nil {
			continue
		}
		lc := &lineageCall{done: make(chan struct{})}
		r.calls[key] = lc
		pendingTables = append(pendingTables, t)
		pendingCalls = append(pendingCalls, lc)
	}
	r.mu.Unlock()
	if len(pendingTables) == 0 {
		return
	}
	var results map[string]*tableLineage
	var batchErr error
	batchErr = r.call(ctx, func() error {
		var err error
		results, err = r.fetchBatch(ctx, pendingTables)
		return err
	})
	for i, t := range pendingTables {
		key := strings.ToUpper(strings.TrimSpace(t))
		lc := pendingCalls[i]
		if batchErr != nil {
			lc.err = batchErr
		} else if lin := results[key]; lin != nil {
			if lin.partialErr != nil && len(lin.tableReaders) == 0 {
				lc.err = lin.partialErr
			} else {
				lc.lin = lin
			}
		} else {
			lc.lin = emptyLineage()
		}
		if lc.err != nil && ctx.Err() != nil {
			r.mu.Lock()
			if r.calls[key] == lc {
				delete(r.calls, key)
			}
			r.mu.Unlock()
		}
		close(lc.done)
	}
}

func emptyLineage() *tableLineage {
	return &tableLineage{
		byCol:         map[string][]ColumnRef{},
		trackedTables: map[string]bool{},
	}
}

func (r *Readers) load(ctx context.Context, table string) (*tableLineage, error) {
	key := strings.ToUpper(strings.TrimSpace(table))
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

	var results map[string]*tableLineage
	lc.err = r.call(ctx, func() error {
		var err error
		results, err = r.fetchBatch(ctx, []string{table})
		return err
	})
	if lc.err == nil {
		if lin := results[key]; lin != nil {
			if lin.partialErr != nil && len(lin.tableReaders) == 0 {
				lc.err = lin.partialErr
			} else {
				lc.lin = lin
			}
		} else {
			lc.lin = emptyLineage()
		}
	}
	if lc.err != nil {
		r.mu.Lock()
		if r.calls[key] == lc {
			delete(r.calls, key)
		}
		r.mu.Unlock()
	}
	close(lc.done)
	return lc.lin, lc.err
}

// fetchBatch queries ACCESS_HISTORY (projected column lineage via directSources,
// plus WHERE/JOIN/GROUP BY column reads and table-level reads via
// DIRECT_OBJECTS_ACCESSED) and OBJECT_DEPENDENCIES (view/MV/dynamic-table
// dependencies) for all tables in two batched queries total.
func (r *Readers) fetchBatch(ctx context.Context, tables []string) (map[string]*tableLineage, error) {
	bySource := map[string]*tableLineage{}
	readerSets := map[string]map[string]bool{}
	seenCols := map[string]map[string]map[ColumnRef]bool{}
	hasDirectCol := map[string]map[string]bool{}

	var inList []string
	var depPredicates []string
	for _, table := range tables {
		key := strings.ToUpper(strings.TrimSpace(table))
		if key == "" || bySource[key] != nil {
			continue
		}
		bySource[key] = emptyLineage()
		readerSets[key] = map[string]bool{}
		seenCols[key] = map[string]map[ColumnRef]bool{}
		hasDirectCol[key] = map[string]bool{}
		inList = append(inList, "UPPER("+quoteLiteral(table)+")")
		if db, sch, tbl := splitFull(table); db != "" && sch != "" && tbl != "" {
			depPredicates = append(depPredicates, fmt.Sprintf(
				"(UPPER(REFERENCED_DATABASE) = UPPER(%s) AND UPPER(REFERENCED_SCHEMA) = UPPER(%s) AND UPPER(REFERENCED_OBJECT_NAME) = UPPER(%s))",
				quoteLiteral(db), quoteLiteral(sch), quoteLiteral(tbl)))
		}
	}
	if len(inList) == 0 {
		return bySource, nil
	}

	addReader := func(srcKey, t string) {
		lin := bySource[srcKey]
		if lin == nil {
			return
		}
		t = strings.TrimSpace(t)
		if t == "" || strings.EqualFold(t, srcKey) {
			return
		}
		t = r.canonical(t)
		upper := strings.ToUpper(t)
		if !readerSets[srcKey][upper] {
			readerSets[srcKey][upper] = true
			lin.tableReaders = append(lin.tableReaders, t)
		}
	}
	addCol := func(srcKey, target, tcol, scol string) {
		lin := bySource[srcKey]
		if lin == nil || target == "" || scol == "" || strings.EqualFold(target, srcKey) {
			return
		}
		target = r.canonical(target)
		addReader(srcKey, target)
		lin.trackedTables[strings.ToUpper(target)] = true
		colKey := strings.ToLower(scol)
		if seenCols[srcKey][colKey] == nil {
			seenCols[srcKey][colKey] = map[ColumnRef]bool{}
		}
		ref := ColumnRef{Table: target, Column: tcol}
		if !seenCols[srcKey][colKey][ref] {
			seenCols[srcKey][colKey][ref] = true
			lin.byCol[colKey] = append(lin.byCol[colKey], ref)
		}
	}

	// 1. Batched ACCESS_HISTORY query: projected column lineage (directSources)
	// UNION direct column/table accesses (DIRECT_OBJECTS_ACCESSED, catching
	// WHERE/JOIN/GROUP BY/HAVING column reads as well as table-level reads).
	inExpr := strings.Join(inList, ", ")
	accessSQL := fmt.Sprintf(`WITH recent AS (
  SELECT DIRECT_OBJECTS_ACCESSED, OBJECTS_MODIFIED
  FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY
  WHERE QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
    AND ARRAY_SIZE(OBJECTS_MODIFIED) > 0
)
SELECT
  UPPER(ds.value:objectName::STRING) AS source_table,
  m.value:objectName::STRING AS target_table,
  mc.value:columnName::STRING AS target_col,
  ds.value:columnName::STRING AS source_col,
  'DIRECT' AS kind
FROM recent h,
  LATERAL FLATTEN(input => h.OBJECTS_MODIFIED) m,
  LATERAL FLATTEN(input => m.value:columns) mc,
  LATERAL FLATTEN(input => mc.value:directSources) ds
WHERE UPPER(ds.value:objectName::STRING) IN (%s)
  AND m.value:objectName IS NOT NULL
UNION
SELECT
  UPPER(d.value:objectName::STRING) AS source_table,
  m.value:objectName::STRING AS target_table,
  '' AS target_col,
  dc.value:columnName::STRING AS source_col,
  'ACCESSED' AS kind
FROM recent h,
  LATERAL FLATTEN(input => h.DIRECT_OBJECTS_ACCESSED) d,
  LATERAL FLATTEN(input => h.OBJECTS_MODIFIED) m,
  LATERAL FLATTEN(input => d.value:columns, outer => TRUE) dc
WHERE UPPER(d.value:objectName::STRING) IN (%s)
  AND m.value:objectName IS NOT NULL`, lineageDays, inExpr, inExpr)

	r.used.Add(1)
	accessSt, accessErr := r.c.execute(ctx, accessSQL)
	if accessErr == nil {
		idx := accessSt.colIndex()
		// Process DIRECT rows first so ACCESSED rows don't duplicate a
		// column already linked with a specific target_col.
		type accRow struct{ src, target, scol string }
		var accessed []accRow
		for _, row := range accessSt.Data {
			srcKey := strings.ToUpper(strings.TrimSpace(cellStr(row, idx, "source_table")))
			if srcKey == "" && len(bySource) == 1 {
				for k := range bySource {
					srcKey = k
				}
			}
			target := cellStr(row, idx, "target_table")
			tcol := cellStr(row, idx, "target_col")
			scol := cellStr(row, idx, "source_col")
			kind := strings.ToUpper(cellStr(row, idx, "kind"))
			if kind == "ACCESSED" {
				accessed = append(accessed, accRow{src: srcKey, target: target, scol: scol})
				continue
			}
			addCol(srcKey, target, tcol, scol)
			if target != "" && scol != "" && bySource[srcKey] != nil {
				hasDirectCol[srcKey][strings.ToUpper(r.canonical(target))+"|"+strings.ToLower(scol)] = true
			}
		}
		for _, a := range accessed {
			if bySource[a.src] == nil {
				continue
			}
			addReader(a.src, a.target)
			if a.scol == "" {
				continue
			}
			targetCanon := r.canonical(strings.TrimSpace(a.target))
			if hasDirectCol[a.src][strings.ToUpper(targetCanon)+"|"+strings.ToLower(a.scol)] {
				continue
			}
			addCol(a.src, targetCanon, "", a.scol)
		}
	}

	// 2. Batched view / MV / dynamic table dependencies from OBJECT_DEPENDENCIES.
	var depErr error
	if len(depPredicates) > 0 {
		depSQL := fmt.Sprintf(`SELECT DISTINCT
  UPPER(REFERENCED_DATABASE || '.' || REFERENCED_SCHEMA || '.' || REFERENCED_OBJECT_NAME) AS source_table,
  REFERENCING_DATABASE || '.' || REFERENCING_SCHEMA || '.' || REFERENCING_OBJECT_NAME AS reader
FROM SNOWFLAKE.ACCOUNT_USAGE.OBJECT_DEPENDENCIES
WHERE %s`, strings.Join(depPredicates, "\n   OR "))
		r.used.Add(1)
		if st, err := r.c.execute(ctx, depSQL); err == nil {
			idx := st.colIndex()
			for _, row := range st.Data {
				srcKey := strings.ToUpper(strings.TrimSpace(cellStr(row, idx, "source_table")))
				if srcKey == "" && len(bySource) == 1 {
					for k := range bySource {
						srcKey = k
					}
				}
				addReader(srcKey, cellStr(row, idx, "reader"))
			}
		} else {
			depErr = err
		}
	}

	// Surface errors so column impact never silently hides missing readers
	// when either ACCESS_HISTORY or OBJECT_DEPENDENCIES is unavailable.
	if accessErr != nil && depErr != nil {
		return nil, mapStatementError(accessErr)
	}
	var partialErr error
	switch {
	case accessErr != nil:
		partialErr = fmt.Errorf("Snowflake ACCESS_HISTORY unavailable (non-view readers cannot be checked): %w", mapStatementError(accessErr))
	case depErr != nil:
		partialErr = fmt.Errorf("Snowflake OBJECT_DEPENDENCIES unavailable (view readers cannot be checked): %w", mapStatementError(depErr))
	}
	for _, lin := range bySource {
		lin.partialErr = partialErr
		lin.noAccessHistory = accessErr != nil
		slices.Sort(lin.tableReaders)
	}
	return bySource, nil
}

// ColumnLinks returns the downstream columns that directly read table.column.
// When ACCESS_HISTORY failed while OBJECT_DEPENDENCIES found view readers,
// it returns those views as table-only links alongside the partial error so
// callers surface both the known views and the incomplete-lineage warning.
func (r *Readers) ColumnLinks(ctx context.Context, table, column string) ([]lineage.ColumnLink, error) {
	lin, err := r.load(ctx, table)
	if err != nil {
		return nil, err
	}
	if lin.noAccessHistory {
		out := make([]lineage.ColumnLink, len(lin.tableReaders))
		for i, t := range lin.tableReaders {
			out[i] = lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)}
		}
		return out, lin.partialErr
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
	return out, lin.partialErr
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
	return out, lin.partialErr
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
	return out, lin.partialErr
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
