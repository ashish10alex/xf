package snowflake

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
	lineageDays             = 30
	maxLineageCalls         = 4
	maxExtConsumersPerTable = 100
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
	// extByCol maps lowercased source column name -> SELECT-only external
	// consumer links confirmed by ACCESS_HISTORY.DIRECT_OBJECTS_ACCESSED.columns.
	extByCol map[string][]lineage.ColumnLink
	// extUntracked holds SELECT-only external consumers for which ACCESS_HISTORY
	// recorded a table access without column detail (TableOnly).
	extUntracked []lineage.ColumnLink
	// extTable holds every SELECT-only external consumer of this table (for
	// deleted or renamed tables).
	extTable []lineage.ColumnLink
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
	extDays int
	sem     chan struct{}
	used    atomic.Int64

	mu       sync.Mutex
	calls    map[string]*lineageCall
	warnings []string
	warnSeen map[string]bool
}

// WithExternalReaders enables querying SELECT-only external/BI consumers from
// Snowflake ACCESS_HISTORY (enriched with QUERY_HISTORY tags/roles) over the
// last days (clamped to [1, lineage.MaxJobHistoryDays]).
func (r *Readers) WithExternalReaders(days int) *Readers {
	if days <= 0 {
		days = lineage.DefaultJobHistoryDays
	}
	if days > lineage.MaxJobHistoryDays {
		days = lineage.MaxJobHistoryDays
	}
	r.extDays = days
	return r
}

// Warnings returns report-level warnings from optional external-consumer
// enrichment (e.g. QUERY_HISTORY denied or row cap hit) without turning clean
// columns into unknown severity.
func (r *Readers) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.warnings)
}

func (r *Readers) addWarning(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warnSeen == nil {
		r.warnSeen = map[string]bool{}
	}
	if !r.warnSeen[msg] {
		r.warnSeen[msg] = true
		r.warnings = append(r.warnings, msg)
	}
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
			if lin.partialErr != nil && len(lin.tableReaders) == 0 && len(lin.extTable) == 0 {
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
		extByCol:      map[string][]lineage.ColumnLink{},
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
			if lin.partialErr != nil && len(lin.tableReaders) == 0 && len(lin.extTable) == 0 {
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

	// 3. Optional batched SELECT-only external/BI consumers from ACCESS_HISTORY
	// (ARRAY_SIZE(OBJECTS_MODIFIED) = 0), enriched with QUERY_HISTORY for BI
	// QUERY_TAG / ROLE_NAME / QUERY_TEXT. Failures here stay report-level
	// warnings so table-to-table lineage is never degraded.
	if r.extDays > 0 && accessErr == nil {
		r.fetchExternalConsumers(ctx, inExpr, bySource)
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
	extCol := lin.extByCol[strings.ToLower(column)]
	out := make([]lineage.ColumnLink, 0, len(refs)+len(extCol)+len(lin.extUntracked))
	for _, ref := range refs {
		out = append(out, lineage.ColumnLink{
			Table:          ref.Table,
			Column:         ref.Column,
			DependencyType: lineage.Other,
			Assertion:      r.isAssertion(ref.Table),
		})
	}
	out = append(out, extCol...)
	out = append(out, lin.extUntracked...)
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
	out := make([]lineage.ColumnLink, 0, len(lin.tableReaders)+len(lin.extTable))
	for _, t := range lin.tableReaders {
		out = append(out, lineage.ColumnLink{Table: t, DependencyType: lineage.TableOnly, Assertion: r.isAssertion(t)})
	}
	out = append(out, lin.extTable...)
	return out, lin.partialErr
}

func isSnowflakeAuthOrMissingViewErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "002003") ||
		strings.Contains(low, "does not exist") ||
		strings.Contains(low, "not authorized") ||
		strings.Contains(low, "insufficient privileges")
}

// fetchExternalConsumers queries SELECT-only reads (OBJECTS_MODIFIED empty)
// from ACCESS_HISTORY (covering both DIRECT_OBJECTS_ACCESSED and
// BASE_OBJECTS_ACCESSED so reads through secure views are caught), pre-filtering
// to matching QUERY_IDs in a CTE before joining QUERY_HISTORY for BI tags/roles
// and aggregating one row per consumer with ARRAY_AGG of accessed columns.
func (r *Readers) fetchExternalConsumers(ctx context.Context, inExpr string, bySource map[string]*tableLineage) {
	enrichedSQL := fmt.Sprintf(`/* xf:query-history */
WITH accessed AS (
  SELECT
    h.QUERY_ID,
    h.QUERY_START_TIME,
    COALESCE(h.USER_NAME, '') AS access_user,
    UPPER(d.value:objectName::STRING) AS source_table,
    COALESCE(dc.value:columnName::STRING, '') AS source_col
  FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
    LATERAL FLATTEN(input => h.DIRECT_OBJECTS_ACCESSED) d,
    LATERAL FLATTEN(input => d.value:columns, outer => TRUE) dc
  WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
    AND ARRAY_SIZE(h.OBJECTS_MODIFIED) = 0
    AND UPPER(d.value:objectName::STRING) IN (%s)
  UNION ALL
  SELECT
    h.QUERY_ID,
    h.QUERY_START_TIME,
    COALESCE(h.USER_NAME, '') AS access_user,
    UPPER(b.value:objectName::STRING) AS source_table,
    COALESCE(bc.value:columnName::STRING, '') AS source_col
  FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
    LATERAL FLATTEN(input => h.BASE_OBJECTS_ACCESSED) b,
    LATERAL FLATTEN(input => b.value:columns, outer => TRUE) bc
  WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
    AND ARRAY_SIZE(h.OBJECTS_MODIFIED) = 0
    AND UPPER(b.value:objectName::STRING) IN (%s)
),
qids AS (
  SELECT DISTINCT QUERY_ID FROM accessed
),
qh AS (
  SELECT q.QUERY_ID, q.ROLE_NAME, q.QUERY_TAG, q.QUERY_TEXT
  FROM SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY q
  JOIN qids ON qids.QUERY_ID = q.QUERY_ID
  WHERE q.START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
),
filtered AS (
  SELECT
    a.source_table,
    a.access_user AS user_name,
    COALESCE(qh.ROLE_NAME, '') AS role_name,
    COALESCE(qh.QUERY_TAG, '') AS query_tag,
    a.QUERY_ID,
    a.QUERY_START_TIME,
    a.source_col,
    qh.QUERY_TEXT
  FROM accessed a
  LEFT JOIN qh ON qh.QUERY_ID = a.QUERY_ID
  WHERE COALESCE(qh.QUERY_TAG, '') NOT LIKE 'xf:%%'
    AND COALESCE(qh.QUERY_TEXT, '') NOT ILIKE '%%"app": "dbt"%%'
    AND COALESCE(qh.QUERY_TEXT, '') NOT ILIKE '%%"app":"dbt"%%'
    AND COALESCE(qh.QUERY_TEXT, '') NOT ILIKE '%%/* xf:%%'
),
per_col AS (
  SELECT
    source_table,
    user_name,
    role_name,
    query_tag,
    source_col,
    COUNT(DISTINCT QUERY_ID) AS col_queries,
    TO_CHAR(MAX(QUERY_START_TIME), 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS col_last_seen
  FROM filtered
  WHERE source_col <> ''
  GROUP BY 1, 2, 3, 4, 5
),
cols_agg AS (
  SELECT
    source_table,
    user_name,
    role_name,
    query_tag,
    TO_JSON(ARRAY_AGG(OBJECT_CONSTRUCT('col', source_col, 'n', col_queries, 'last', col_last_seen))) AS cols_json
  FROM per_col
  GROUP BY 1, 2, 3, 4
),
per_consumer AS (
  SELECT
    source_table,
    user_name,
    role_name,
    query_tag,
    COUNT(DISTINCT QUERY_ID) AS query_count,
    TO_CHAR(MAX(QUERY_START_TIME), 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS last_seen,
    BOOLOR_AGG(source_col = '') AS has_untracked,
    ANY_VALUE(QUERY_TEXT) AS sample_query
  FROM filtered
  GROUP BY 1, 2, 3, 4
  QUALIFY ROW_NUMBER() OVER (PARTITION BY source_table ORDER BY query_count DESC) <= %d
)
SELECT
  c.source_table,
  c.user_name,
  c.role_name,
  c.query_tag,
  c.query_count,
  c.last_seen,
  COALESCE(ca.cols_json, '[]') AS cols_json,
  c.has_untracked,
  c.sample_query
FROM per_consumer c
LEFT JOIN cols_agg ca
  ON ca.source_table = c.source_table
 AND ca.user_name = c.user_name
 AND ca.role_name = c.role_name
 AND ca.query_tag = c.query_tag`, r.extDays, inExpr, r.extDays, inExpr, r.extDays, maxExtConsumersPerTable+1)

	r.used.Add(1)
	st, err := r.c.execute(ctx, enrichedSQL)
	if err != nil {
		if ctx.Err() != nil || !isSnowflakeAuthOrMissingViewErr(err) {
			r.addWarning(fmt.Sprintf("Snowflake external SELECT readers unavailable: %v", mapStatementError(err)))
			return
		}
		// Only fall back to ACCESS_HISTORY alone when QUERY_HISTORY isn't authorized or doesn't exist.
		fallbackSQL := fmt.Sprintf(`/* xf:query-history */
WITH accessed AS (
  SELECT
    h.QUERY_ID,
    h.QUERY_START_TIME,
    COALESCE(h.USER_NAME, '') AS access_user,
    UPPER(d.value:objectName::STRING) AS source_table,
    COALESCE(dc.value:columnName::STRING, '') AS source_col
  FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
    LATERAL FLATTEN(input => h.DIRECT_OBJECTS_ACCESSED) d,
    LATERAL FLATTEN(input => d.value:columns, outer => TRUE) dc
  WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
    AND ARRAY_SIZE(h.OBJECTS_MODIFIED) = 0
    AND UPPER(d.value:objectName::STRING) IN (%s)
  UNION ALL
  SELECT
    h.QUERY_ID,
    h.QUERY_START_TIME,
    COALESCE(h.USER_NAME, '') AS access_user,
    UPPER(b.value:objectName::STRING) AS source_table,
    COALESCE(bc.value:columnName::STRING, '') AS source_col
  FROM SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY h,
    LATERAL FLATTEN(input => h.BASE_OBJECTS_ACCESSED) b,
    LATERAL FLATTEN(input => b.value:columns, outer => TRUE) bc
  WHERE h.QUERY_START_TIME >= DATEADD('day', -%d, CURRENT_TIMESTAMP())
    AND ARRAY_SIZE(h.OBJECTS_MODIFIED) = 0
    AND UPPER(b.value:objectName::STRING) IN (%s)
),
per_col AS (
  SELECT
    source_table,
    access_user AS user_name,
    source_col,
    COUNT(DISTINCT QUERY_ID) AS col_queries,
    TO_CHAR(MAX(QUERY_START_TIME), 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS col_last_seen
  FROM accessed
  WHERE source_col <> ''
  GROUP BY 1, 2, 3
),
cols_agg AS (
  SELECT
    source_table,
    user_name,
    TO_JSON(ARRAY_AGG(OBJECT_CONSTRUCT('col', source_col, 'n', col_queries, 'last', col_last_seen))) AS cols_json
  FROM per_col
  GROUP BY 1, 2
),
per_consumer AS (
  SELECT
    source_table,
    access_user AS user_name,
    COUNT(DISTINCT QUERY_ID) AS query_count,
    TO_CHAR(MAX(QUERY_START_TIME), 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS last_seen,
    BOOLOR_AGG(source_col = '') AS has_untracked
  FROM accessed
  GROUP BY 1, 2
  QUALIFY ROW_NUMBER() OVER (PARTITION BY source_table ORDER BY query_count DESC) <= %d
)
SELECT
  c.source_table,
  c.user_name,
  '' AS role_name,
  '' AS query_tag,
  c.query_count,
  c.last_seen,
  COALESCE(ca.cols_json, '[]') AS cols_json,
  c.has_untracked,
  '' AS sample_query
FROM per_consumer c
LEFT JOIN cols_agg ca
  ON ca.source_table = c.source_table
 AND ca.user_name = c.user_name`, r.extDays, inExpr, r.extDays, inExpr, maxExtConsumersPerTable+1)
		r.used.Add(1)
		var fbErr error
		st, fbErr = r.c.execute(ctx, fallbackSQL)
		if fbErr != nil {
			r.addWarning(fmt.Sprintf("Snowflake external SELECT readers unavailable: %v", mapStatementError(fbErr)))
			return
		}
		r.addWarning(fmt.Sprintf("Snowflake QUERY_HISTORY unavailable for BI tag enrichment (%v); using ACCESS_HISTORY user names only", mapStatementError(err)))
	}

	idx := st.colIndex()
	rawCount := map[string]int{}
	type colStat struct {
		queries  int
		lastSeen string
	}
	type consumerAgg struct {
		consumer  lineage.Consumer
		cols      map[string]colStat
		untracked bool
	}
	byTable := map[string]map[string]*consumerAgg{}
	tableOrder := map[string][]string{}

	for _, row := range st.Data {
		srcKey := strings.ToUpper(strings.TrimSpace(cellStr(row, idx, "source_table")))
		if bySource[srcKey] == nil {
			continue
		}
		rawCount[srcKey]++
		if rawCount[srcKey] > maxExtConsumersPerTable {
			r.addWarning(fmt.Sprintf("Snowflake external reader lookup for %s hit the %d-consumer cap; some low-frequency readers may not be shown", r.canonical(srcKey), maxExtConsumersPerTable))
			continue
		}
		userName := strings.TrimSpace(cellStr(row, idx, "user_name"))
		roleName := strings.TrimSpace(cellStr(row, idx, "role_name"))
		queryTag := strings.TrimSpace(cellStr(row, idx, "query_tag"))
		sampleQuery := cellStr(row, idx, "sample_query")
		lastSeen := strings.TrimSpace(cellStr(row, idx, "last_seen"))
		colsJSON := strings.TrimSpace(cellStr(row, idx, "cols_json"))
		hasUntracked := strings.EqualFold(strings.TrimSpace(cellStr(row, idx, "has_untracked")), "true")
		cnt := 1
		if n := strings.TrimSpace(cellStr(row, idx, "query_count")); n != "" {
			if parsed, perr := strconv.Atoi(n); perr == nil && parsed > 0 {
				cnt = parsed
			}
		}

		if strings.HasPrefix(strings.ToLower(queryTag), "xf:") ||
			lineage.IsOwnXFQuery("", sampleQuery, nil) ||
			lineage.IsTransformToolQuery(sampleQuery, queryTag, nil) {
			continue
		}

		cons := lineage.ParseConsumer(lineage.QuerySignal{
			User:     userName,
			Role:     roleName,
			Query:    sampleQuery,
			QueryTag: queryTag,
		})
		cons.Queries = cnt
		cons.Days = r.extDays
		cons.LastSeen = lastSeen
		id := cons.ID()

		if byTable[srcKey] == nil {
			byTable[srcKey] = map[string]*consumerAgg{}
		}
		agg := byTable[srcKey][id]
		if agg == nil {
			agg = &consumerAgg{consumer: cons, cols: map[string]colStat{}}
			byTable[srcKey][id] = agg
			tableOrder[srcKey] = append(tableOrder[srcKey], id)
		} else {
			agg.consumer.Queries += cnt
			if lastSeen > agg.consumer.LastSeen {
				agg.consumer.LastSeen = lastSeen
			}
		}
		colStats := decodeSnowflakeColsJSON(colsJSON, cnt, lastSeen)
		for _, cs := range colStats {
			cur := agg.cols[cs.name]
			cur.queries += cs.queries
			if cs.lastSeen > cur.lastSeen {
				cur.lastSeen = cs.lastSeen
			}
			agg.cols[cs.name] = cur
		}
		if hasUntracked || len(colStats) == 0 {
			agg.untracked = true
		}
	}

	for srcKey, lin := range bySource {
		for _, id := range tableOrder[srcKey] {
			agg := byTable[srcKey][id]
			c := agg.consumer
			lin.extTable = append(lin.extTable, lineage.ColumnLink{
				Table:          c.ID(),
				DependencyType: lineage.TableOnly,
				Consumer:       &c,
			})
			for col, cs := range agg.cols {
				cCopy := c
				if cs.queries > 0 {
					cCopy.Queries = cs.queries
				}
				if cs.lastSeen != "" {
					cCopy.LastSeen = cs.lastSeen
				}
				lin.extByCol[col] = append(lin.extByCol[col], lineage.ColumnLink{
					Table:          cCopy.ID(),
					DependencyType: lineage.Other,
					Consumer:       &cCopy,
				})
			}
			if agg.untracked && len(agg.cols) == 0 {
				cCopy := c
				lin.extUntracked = append(lin.extUntracked, lineage.ColumnLink{
					Table:          cCopy.ID(),
					DependencyType: lineage.TableOnly,
					Consumer:       &cCopy,
				})
			}
		}
	}
}

type decodedColStat struct {
	name     string
	queries  int
	lastSeen string
}

// decodeSnowflakeColsJSON parses either the per-column stat object array
// [{"col":"X","n":5,"last":"..."}] or a plain string array ["X","Y"].
func decodeSnowflakeColsJSON(raw string, fallbackCount int, fallbackLast string) []decodedColStat {
	if raw == "" || raw == "null" || raw == "[]" {
		return nil
	}
	var objs []struct {
		Col  string `json:"col"`
		N    int    `json:"n"`
		Last string `json:"last"`
	}
	if err := json.Unmarshal([]byte(raw), &objs); err == nil {
		out := make([]decodedColStat, 0, len(objs))
		for _, o := range objs {
			name := strings.ToLower(strings.TrimSpace(o.Col))
			if name == "" {
				continue
			}
			n := o.N
			if n <= 0 {
				n = fallbackCount
			}
			last := strings.TrimSpace(o.Last)
			if last == "" {
				last = fallbackLast
			}
			out = append(out, decodedColStat{name: name, queries: n, lastSeen: last})
		}
		if len(out) > 0 {
			return out
		}
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	out := make([]decodedColStat, 0, len(names))
	for _, col := range names {
		name := strings.ToLower(strings.TrimSpace(col))
		if name != "" {
			out = append(out, decodedColStat{name: name, queries: fallbackCount, lastSeen: fallbackLast})
		}
	}
	return out
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
