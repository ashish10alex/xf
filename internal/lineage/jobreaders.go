package lineage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	// DefaultJobHistoryDays is the default lookback window for scanning
	// BigQuery INFORMATION_SCHEMA.JOBS for SELECT-only external readers.
	DefaultJobHistoryDays = 14
	// MaxJobHistoryDays caps the lookback window to bound scan volume.
	MaxJobHistoryDays = 90
	// DefaultJobHistoryCacheTTL caches query-history results per table inside
	// a long-lived xf engine so local re-checks don't re-scan JOBS.
	DefaultJobHistoryCacheTTL = 15 * time.Minute

	maxJobHistoryQueries   = 4
	maxJobHistoryPerTable  = 100
	maxJobHistoryBatchRows = 5000
	maxJobHistoryCacheSize = 512
	jobHistoryTimeout      = 90 * time.Second
)

// QueryExecutor is the subset of warehouse.Warehouse needed to query
// BigQuery INFORMATION_SCHEMA.JOBS.
type QueryExecutor interface {
	StartQuery(ctx context.Context, o warehouse.QueryOptions) (warehouse.JobRef, error)
	QueryJob(ctx context.Context, ref warehouse.JobRef) (*warehouse.QueryStatus, error)
	Rows(ctx context.Context, ref warehouse.JobRef, token string, size int) (*warehouse.Page, error)
	CancelQuery(ctx context.Context, ref warehouse.JobRef) error
	DefaultProject() string
}

// JobHistoryConfig controls scanning BigQuery INFORMATION_SCHEMA.JOBS for
// SELECT-only external/BI consumers alongside Dataplex table lineage.
type JobHistoryConfig struct {
	// Projects lists the GCP billing projects to scan JOBS_BY_PROJECT in.
	// Special tokens:
	//   - "auto": each table's own project plus Source's extraProjects
	//   - "org":  region-<loc>.INFORMATION_SCHEMA.JOBS_BY_ORGANIZATION
	//             (takes precedence over project entries so counts never double)
	Projects []string
	// Days is the lookback window in days (defaults to DefaultJobHistoryDays,
	// clamped to [1, MaxJobHistoryDays]).
	Days int
	// MaxBytesBilled sets BigQuery maximumBytesBilled per query (0 = no cap).
	MaxBytesBilled int64
	// CacheTTL overrides DefaultJobHistoryCacheTTL when > 0.
	CacheTTL time.Duration
	// Now overrides time.Now for tests.
	Now func() time.Time
}

type bucketQuery struct {
	stripped StrippedQuery
	count    int
	lastSeen string
	jobID    string
	jobURL   string
}

// consumerBucket holds one aggregated external consumer of a specific table
// along with the distinct normalized SQL queries it ran.
type consumerBucket struct {
	consumer Consumer
	queries  []bucketQuery
}

type tableConsumers struct {
	buckets   []consumerBucket
	warnings  []string
	truncated bool
	err       error
}

type cacheEntry struct {
	at   time.Time
	data tableConsumers
}

// JobHistoryCache caches per-table external consumer lookups across checks in
// the same engine process so repeated local checks don't re-scan BigQuery JOBS.
type JobHistoryCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

// NewJobHistoryCache creates an empty cache.
func NewJobHistoryCache() *JobHistoryCache {
	return &JobHistoryCache{entries: map[string]cacheEntry{}}
}

func (c *JobHistoryCache) get(key string, now time.Time, ttl time.Duration) (tableConsumers, bool) {
	if c == nil || ttl <= 0 {
		return tableConsumers{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) > ttl {
		return tableConsumers{}, false
	}
	return e.data, true
}

func (c *JobHistoryCache) set(key string, now time.Time, ttl time.Duration, data tableConsumers) {
	if c == nil || data.err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	if len(c.entries) >= maxJobHistoryCacheSize {
		for k, e := range c.entries {
			if now.Sub(e.at) > ttl {
				delete(c.entries, k)
			}
		}
		// If still full, evict oldest entries.
		for len(c.entries) >= maxJobHistoryCacheSize {
			var oldestKey string
			var oldestAt time.Time
			for k, e := range c.entries {
				if oldestKey == "" || e.at.Before(oldestAt) {
					oldestKey, oldestAt = k, e.at
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = cacheEntry{at: now, data: data}
}

type jobHistoryCall struct {
	done chan struct{}
	res  tableConsumers
}

// jobHistorySource queries BigQuery INFORMATION_SCHEMA.JOBS in batched queries
// per (billingProject, region) and maps rows to external Consumers.
type jobHistorySource struct {
	exec     QueryExecutor
	tables   *Tables
	projects []string
	extra    []string
	days     int
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time
	cache    *JobHistoryCache
	sem      chan struct{}

	mu       sync.Mutex
	calls    map[string]*jobHistoryCall
	warnings []string
	warnSeen map[string]bool
}

func newJobHistorySource(exec QueryExecutor, tables *Tables, extraProjects []string, cfg JobHistoryConfig, cache *JobHistoryCache) *jobHistorySource {
	if exec == nil || len(cfg.Projects) == 0 {
		return nil
	}
	days := cfg.Days
	if days <= 0 {
		days = DefaultJobHistoryDays
	}
	if days > MaxJobHistoryDays {
		days = MaxJobHistoryDays
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultJobHistoryCacheTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	var cleaned []string
	seen := map[string]bool{}
	for _, p := range cfg.Projects {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return &jobHistorySource{
		exec:     exec,
		tables:   tables,
		projects: cleaned,
		extra:    extraProjects,
		days:     days,
		maxBytes: cfg.MaxBytesBilled,
		ttl:      ttl,
		now:      nowFn,
		cache:    cache,
		sem:      make(chan struct{}, maxJobHistoryQueries),
		calls:    map[string]*jobHistoryCall{},
		warnSeen: map[string]bool{},
	}
}

func (j *jobHistorySource) addWarning(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.warnSeen[msg] {
		j.warnSeen[msg] = true
		j.warnings = append(j.warnings, msg)
	}
}

func (j *jobHistorySource) Warnings() []string {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.warnings)
}

func (j *jobHistorySource) cacheKey(table string) string {
	own, _, _ := strings.Cut(table, ".")
	bks := j.batchKeysFor(own, "r", "R")
	parts := make([]string, len(bks))
	for i, bk := range bks {
		if bk.orgView {
			parts[i] = "org:" + bk.billingProject
		} else {
			parts[i] = bk.viewProject
		}
	}
	slices.Sort(parts)
	return strings.ToLower(table) + "|" + strings.Join(parts, ",") + "|" + strconv.Itoa(j.days) + "|" + strconv.FormatInt(j.maxBytes, 10)
}

// preload starts batched INFORMATION_SCHEMA.JOBS lookups for all tables not
// already in flight or cached, returning immediately once requests are
// registered so Dataplex searches can run in parallel.
func (j *jobHistorySource) preload(ctx context.Context, tables []string) {
	if j == nil || len(tables) == 0 {
		return
	}
	now := j.now()
	var pending []string
	var pendingCalls []*jobHistoryCall

	j.mu.Lock()
	for _, t := range tables {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" || j.calls[key] != nil {
			continue
		}
		if cached, ok := j.cache.get(j.cacheKey(t), now, j.ttl); ok {
			c := &jobHistoryCall{done: make(chan struct{}), res: cached}
			close(c.done)
			j.calls[key] = c
			for _, w := range cached.warnings {
				if !j.warnSeen[w] {
					j.warnSeen[w] = true
					j.warnings = append(j.warnings, w)
				}
			}
			continue
		}
		c := &jobHistoryCall{done: make(chan struct{})}
		j.calls[key] = c
		pending = append(pending, t)
		pendingCalls = append(pendingCalls, c)
	}
	j.mu.Unlock()

	if len(pending) == 0 {
		return
	}
	go j.fetchTables(ctx, pending, pendingCalls)
}

func (j *jobHistorySource) forTable(ctx context.Context, table string) tableConsumers {
	if j == nil {
		return tableConsumers{}
	}
	key := strings.ToLower(strings.TrimSpace(table))
	if key == "" {
		return tableConsumers{}
	}
	j.mu.Lock()
	c := j.calls[key]
	if c == nil {
		if cached, ok := j.cache.get(j.cacheKey(table), j.now(), j.ttl); ok {
			c = &jobHistoryCall{done: make(chan struct{}), res: cached}
			close(c.done)
			j.calls[key] = c
			for _, w := range cached.warnings {
				if !j.warnSeen[w] {
					j.warnSeen[w] = true
					j.warnings = append(j.warnings, w)
				}
			}
			j.mu.Unlock()
			return cached
		}
		c = &jobHistoryCall{done: make(chan struct{})}
		j.calls[key] = c
		j.mu.Unlock()
		go j.fetchTables(ctx, []string{table}, []*jobHistoryCall{c})
	} else {
		j.mu.Unlock()
	}

	select {
	case <-c.done:
		return c.res
	case <-ctx.Done():
		return tableConsumers{err: ctx.Err()}
	}
}

type queryBatchKey struct {
	billingProject string // GCP project billed for the query
	viewProject    string // project prefix on JOBS_BY_PROJECT, or "" for JOBS_BY_ORGANIZATION
	region         string // lowercased BigQuery region for region-<loc>.INFORMATION_SCHEMA
	location       string // original table Location passed to BigQuery Jobs API
	orgView        bool
}

func (j *jobHistorySource) fetchTables(ctx context.Context, tables []string, calls []*jobHistoryCall) {
	// Resolve each table's BigQuery location first (cached by s.tables).
	type tableMeta struct {
		raw   string
		lower string
		own   string
		loc   string // original BigQuery Location
		reg   string // lowercased region
		err   error
	}
	metas := make([]tableMeta, len(tables))
	var locWG sync.WaitGroup
	for i, t := range tables {
		locWG.Go(func() {
			own, _, _ := strings.Cut(t, ".")
			md, err := j.tables.Table(ctx, t)
			if err != nil {
				metas[i] = tableMeta{raw: t, lower: strings.ToLower(t), own: own, err: fmt.Errorf("couldn't read %s to find its region for query history: %w", t, err)}
				return
			}
			if md == nil || strings.TrimSpace(md.Location) == "" {
				metas[i] = tableMeta{raw: t, lower: strings.ToLower(t), own: own, err: fmt.Errorf("couldn't read %s to find its region for query history", t)}
				return
			}
			loc := strings.TrimSpace(md.Location)
			metas[i] = tableMeta{raw: t, lower: strings.ToLower(t), own: own, loc: loc, reg: strings.ToLower(loc)}
		})
	}
	locWG.Wait()

	// Group tables by (billingProject/view, region).
	batches := map[queryBatchKey][]string{}
	tableBatches := make([][]queryBatchKey, len(tables))
	for i, m := range metas {
		if m.err != nil {
			continue
		}
		bks := j.batchKeysFor(m.own, m.reg, m.loc)
		tableBatches[i] = bks
		for _, bk := range bks {
			if !slices.Contains(batches[bk], m.lower) {
				batches[bk] = append(batches[bk], m.lower)
			}
		}
	}

	type batchResult struct {
		byTable   map[string][]consumerBucket
		truncated map[string]bool
		err       error
	}
	results := map[queryBatchKey]batchResult{}
	var resMu sync.Mutex
	var batchWG sync.WaitGroup
	for bk, tbls := range batches {
		slices.Sort(tbls)
		batchWG.Go(func() {
			byTable, trunc, err := j.runBatch(ctx, bk, tbls)
			resMu.Lock()
			results[bk] = batchResult{byTable: byTable, truncated: trunc, err: err}
			resMu.Unlock()
		})
	}
	batchWG.Wait()

	now := j.now()
	for i, m := range metas {
		var out tableConsumers
		if m.err != nil {
			out.err = m.err
			j.addWarning(m.err.Error())
		} else {
			merged := map[string]*consumerBucket{}
			var order []string
			var errs []error
			for _, bk := range tableBatches[i] {
				br := results[bk]
				if br.err != nil {
					errs = append(errs, br.err)
					j.addWarning(br.err.Error())
				}
				if br.truncated[m.lower] {
					out.truncated = true
					w := fmt.Sprintf("BigQuery query history for %s hit the %d-query-group cap; some low-frequency readers may not be shown", m.raw, maxJobHistoryPerTable)
					out.warnings = append(out.warnings, w)
					j.addWarning(w)
				}
				for _, b := range br.byTable[m.lower] {
					id := b.consumer.ID()
					dst := merged[id]
					if dst == nil {
						cp := b.consumer
						dst = &consumerBucket{consumer: cp}
						merged[id] = dst
						order = append(order, id)
					} else {
						dst.consumer.Queries += b.consumer.Queries
						if b.consumer.LastSeen > dst.consumer.LastSeen || (b.consumer.LastSeen == dst.consumer.LastSeen && dst.consumer.SampleJobURL == "" && b.consumer.SampleJobURL != "") {
							dst.consumer.LastSeen = b.consumer.LastSeen
							dst.consumer.SampleJobID = b.consumer.SampleJobID
							dst.consumer.SampleJobURL = b.consumer.SampleJobURL
						}
						if dst.consumer.User == "" {
							dst.consumer.User = b.consumer.User
						}
					}
					dst.queries = append(dst.queries, b.queries...)
				}
			}
			for _, id := range order {
				out.buckets = append(out.buckets, *merged[id])
			}
			out.err = errors.Join(errs...)
		}
		if out.err == nil {
			j.cache.set(j.cacheKey(m.raw), now, j.ttl, out)
		} else if ctx.Err() != nil {
			j.mu.Lock()
			if j.calls[m.lower] == calls[i] {
				delete(j.calls, m.lower)
			}
			j.mu.Unlock()
		}
		calls[i].res = out
		close(calls[i].done)
	}
}

func (j *jobHistorySource) batchKeysFor(ownProject, region, location string) []queryBatchKey {
	// If "org" is configured alongside project entries, let JOBS_BY_ORGANIZATION
	// take precedence alone so jobs aren't double-counted across both views.
	for _, p := range j.projects {
		if strings.EqualFold(p, "org") {
			billing := cmp.Or(ownProject, j.exec.DefaultProject())
			if billing == "" || region == "" {
				return nil
			}
			return []queryBatchKey{{billingProject: billing, region: region, location: location, orgView: true}}
		}
	}

	var out []queryBatchKey
	seen := map[queryBatchKey]bool{}
	add := func(bk queryBatchKey) {
		if bk.billingProject == "" || bk.region == "" || seen[bk] {
			return
		}
		seen[bk] = true
		out = append(out, bk)
	}
	for _, p := range j.projects {
		if strings.EqualFold(p, "auto") {
			add(queryBatchKey{billingProject: ownProject, viewProject: ownProject, region: region, location: location})
			for _, ep := range j.extra {
				add(queryBatchKey{billingProject: ep, viewProject: ep, region: region, location: location})
			}
			continue
		}
		add(queryBatchKey{billingProject: p, viewProject: p, region: region, location: location})
	}
	return out
}

var (
	validBQProjectRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	validBQRegionRE  = regexp.MustCompile(`^[a-z0-9-]+$`)
)

func buildJobHistorySQL(bk queryBatchKey, lowerTables []string, days int) (string, error) {
	if !validBQRegionRE.MatchString(bk.region) {
		return "", fmt.Errorf("invalid BigQuery region %q", bk.region)
	}
	var view string
	if bk.orgView {
		view = fmt.Sprintf("`region-%s.INFORMATION_SCHEMA.JOBS_BY_ORGANIZATION`", bk.region)
	} else {
		if !validBQProjectRE.MatchString(bk.viewProject) {
			return "", fmt.Errorf("invalid BigQuery project %q", bk.viewProject)
		}
		view = fmt.Sprintf("`%s.region-%s.INFORMATION_SCHEMA.JOBS_BY_PROJECT`", bk.viewProject, bk.region)
	}
	inList := make([]string, len(lowerTables))
	for i, t := range lowerTables {
		inList[i] = bqStringLiteral(t)
	}
	// Per-table cap (maxJobHistoryPerTable+1) via QUALIFY ensures one busy table
	// can never starve another table in the same batch and lets us detect truncation.
	// Grouping by FARM_FINGERPRINT of the normalized full query preserves distinct
	// WHERE/GROUP BY columns on long BI queries sharing a SELECT prefix.
	return fmt.Sprintf(`/* xf:query-history */
WITH matched AS (
  SELECT
    LOWER(CONCAT(rt.project_id, '.', rt.dataset_id, '.', rt.table_id)) AS ref_table,
    j.project_id,
    j.job_id,
    j.user_email,
    j.creation_time,
    j.query,
    j.labels
  FROM %s AS j,
    UNNEST(j.referenced_tables) AS rt
  WHERE j.creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL %d DAY)
    AND j.job_type = 'QUERY'
    AND j.statement_type = 'SELECT'
    AND j.state = 'DONE'
    AND j.error_result IS NULL
    AND (j.destination_table IS NULL OR STARTS_WITH(j.destination_table.dataset_id, '_'))
    AND NOT STARTS_WITH(j.job_id, 'xf_scratch_')
    AND NOT STARTS_WITH(j.job_id, 'xf_impact_')
    AND LOWER(CONCAT(rt.project_id, '.', rt.dataset_id, '.', rt.table_id)) IN (%s)
),
grouped AS (
  SELECT
    ref_table,
    user_email,
    COUNT(*) AS query_count,
    FORMAT_TIMESTAMP('%%Y-%%m-%%dT%%H:%%M:%%SZ', MAX(creation_time)) AS last_seen,
    ANY_VALUE(query) AS sample_query,
    TO_JSON_STRING(ANY_VALUE(labels)) AS sample_labels,
    ARRAY_AGG(STRUCT(job_id, project_id) ORDER BY creation_time DESC, job_id DESC LIMIT 1)[SAFE_OFFSET(0)] AS latest_job
  FROM matched
  GROUP BY
    ref_table,
    user_email,
    REGEXP_EXTRACT(query, r"(?i)(Looker Query Context\s*\x27[^\x27\n]*\x27|[\"\x27]tableau-query-origins[\"\x27]\s*:\s*(?:\{[^}\n]*\}|\"[^\"\n]*\")|--\s*Metabase::[^\n]*)"),
    TO_JSON_STRING(labels),
    FARM_FINGERPRINT(REGEXP_REPLACE(query, r"\x27[^\x27]*\x27|\b\d+\b", "?"))
)
SELECT
  ref_table,
  user_email,
  query_count,
  last_seen,
  sample_query,
  sample_labels,
  latest_job.job_id AS sample_job_id,
  latest_job.project_id AS sample_project_id
FROM grouped
QUALIFY ROW_NUMBER() OVER (PARTITION BY ref_table ORDER BY query_count DESC) <= %d`, view, days, strings.Join(inList, ", "), maxJobHistoryPerTable+1), nil
}

func bqStringLiteral(s string) string {
	esc := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`).Replace(s)
	return "'" + esc + "'"
}

func (j *jobHistorySource) runBatch(ctx context.Context, bk queryBatchKey, lowerTables []string) (map[string][]consumerBucket, map[string]bool, error) {
	sql, err := buildJobHistorySQL(bk, lowerTables, j.days)
	if err != nil {
		return nil, nil, err
	}
	select {
	case j.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, j.formatQueryError(bk, ctx.Err())
	}
	defer func() { <-j.sem }()

	qctx, cancel := context.WithTimeout(ctx, jobHistoryTimeout)
	defer cancel()

	ref, err := j.exec.StartQuery(qctx, warehouse.QueryOptions{
		Project:            bk.billingProject,
		Location:           bk.location,
		SQL:                sql,
		Labels:             map[string]string{"app": "xf"},
		JobPrefix:          "xf_impact_",
		MaximumBytesBilled: j.maxBytes,
	})
	if err != nil {
		return nil, nil, j.formatQueryError(bk, err)
	}

	finished := false
	defer func() {
		if !finished {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = j.exec.CancelQuery(cctx, ref)
			ccancel()
		}
	}()

	delay := 150 * time.Millisecond
	for {
		st, err := j.exec.QueryJob(qctx, ref)
		if err != nil {
			return nil, nil, j.formatQueryError(bk, err)
		}
		if st.State == "DONE" {
			finished = true
			if st.Error != "" {
				return nil, nil, j.formatQueryError(bk, errors.New(st.Error))
			}
			break
		}
		select {
		case <-qctx.Done():
			return nil, nil, j.formatQueryError(bk, qctx.Err())
		case <-time.After(delay):
			if delay < time.Second {
				delay = min(delay*2, time.Second)
			}
		}
	}

	maxTotal := min(len(lowerTables)*(maxJobHistoryPerTable+1), maxJobHistoryBatchRows)
	var rows [][]any
	token := ""
	for len(rows) < maxTotal {
		page, err := j.exec.Rows(qctx, ref, token, maxTotal-len(rows))
		if err != nil {
			return nil, nil, j.formatQueryError(bk, err)
		}
		rows = append(rows, page.Rows...)
		if page.NextToken == "" || len(page.Rows) == 0 {
			break
		}
		token = page.NextToken
	}

	byTable, trunc := decodeJobHistoryRows(rows, j.days, cmp.Or(bk.viewProject, bk.billingProject), bk.location)
	return byTable, trunc, nil
}

func (j *jobHistorySource) formatQueryError(bk queryBatchKey, err error) error {
	if err == nil {
		return nil
	}
	scope := bk.viewProject
	if bk.orgView {
		scope = "organization (" + bk.region + ")"
	}
	var api *warehouse.APIError
	if errors.As(err, &api) && (api.Status == http.StatusForbidden || api.Status == http.StatusUnauthorized) {
		role := "roles/bigquery.resourceViewer"
		if bk.orgView {
			role = "roles/bigquery.resourceViewer at the organization level"
		}
		return fmt.Errorf("no access to BigQuery query history in %s (needs %s)", scope, role)
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "bytesbilledlimitexceeded") || strings.Contains(low, "maximum bytes billed") || strings.Contains(low, "exceeded limit for bytes billed") {
		capText := warehouse.FormatBytes(j.maxBytes)
		return fmt.Errorf("BigQuery query history in %s exceeded the %s scan cap (raise queryHistoryMaxGB, set it to 0 on slot reservations, or lower queryHistoryDays)", scope, capText)
	}
	if strings.Contains(low, "accessdenied") || strings.Contains(low, "access denied") {
		return fmt.Errorf("no access to BigQuery query history in %s (needs roles/bigquery.resourceViewer): %s", scope, msg)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("BigQuery query history in %s timed out after %s", scope, jobHistoryTimeout)
	}
	return fmt.Errorf("BigQuery query history in %s: %w", scope, err)
}

func decodeJobHistoryRows(rows [][]any, days int, defaultProject, location string) (map[string][]consumerBucket, map[string]bool) {
	byTable := map[string]map[string]*consumerBucket{}
	orderByTable := map[string][]string{}
	rawCount := map[string]int{}
	truncated := map[string]bool{}

	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		refTable := strings.ToLower(strings.TrimSpace(warehouse.CellText(row[0])))
		if refTable == "" {
			continue
		}
		rawCount[refTable]++
		if rawCount[refTable] > maxJobHistoryPerTable {
			truncated[refTable] = true
			continue
		}
		userEmail := strings.TrimSpace(warehouse.CellText(row[1]))
		count, _ := strconv.Atoi(strings.TrimSpace(warehouse.CellText(row[2])))
		if count <= 0 {
			count = 1
		}
		lastSeen := strings.TrimSpace(warehouse.CellText(row[3]))
		sampleQuery := warehouse.CellText(row[4])
		labels := parseBQLabelsJSON(warehouse.CellText(row[5]))
		jobID := strings.TrimSpace(warehouse.CellText(row[6]))
		jobProject := defaultProject
		if len(row) >= 8 {
			if p := strings.TrimSpace(warehouse.CellText(row[7])); p != "" {
				jobProject = p
			}
		}

		if IsOwnXFQuery(jobID, sampleQuery, labels) || IsTransformToolQuery(sampleQuery, "", labels) {
			continue
		}
		jobURL := BigQueryJobURL(jobProject, location, jobID)
		cons := ParseConsumer(QuerySignal{
			User:   userEmail,
			Query:  sampleQuery,
			Labels: labels,
		})
		cons.Queries = count
		cons.Days = days
		cons.LastSeen = lastSeen
		cons.SampleJobID = jobID
		cons.SampleJobURL = jobURL

		id := cons.ID()
		if byTable[refTable] == nil {
			byTable[refTable] = map[string]*consumerBucket{}
		}
		b := byTable[refTable][id]
		if b == nil {
			b = &consumerBucket{consumer: cons}
			byTable[refTable][id] = b
			orderByTable[refTable] = append(orderByTable[refTable], id)
		} else {
			b.consumer.Queries += count
			if lastSeen > b.consumer.LastSeen || (lastSeen == b.consumer.LastSeen && b.consumer.SampleJobURL == "" && jobURL != "") {
				b.consumer.LastSeen = lastSeen
				b.consumer.SampleJobID = jobID
				b.consumer.SampleJobURL = jobURL
			}
			if b.consumer.User == "" {
				b.consumer.User = cons.User
			}
		}
		if strings.TrimSpace(sampleQuery) != "" {
			b.queries = append(b.queries, bucketQuery{
				stripped: StripQueryForMatch(sampleQuery, true),
				count:    count,
				lastSeen: lastSeen,
				jobID:    jobID,
				jobURL:   jobURL,
			})
		}
	}

	out := make(map[string][]consumerBucket, len(byTable))
	for tbl, ids := range orderByTable {
		buckets := make([]consumerBucket, 0, len(ids))
		for _, id := range ids {
			buckets = append(buckets, *byTable[tbl][id])
		}
		out[tbl] = buckets
	}
	return out, truncated
}

func parseBQLabelsJSON(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "[]" {
		return nil
	}
	var pairs []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil || len(pairs) == 0 {
		return nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if p.Key != "" {
			out[p.Key] = p.Value
		}
	}
	return out
}

// columnLinks returns external consumers of table whose SQL queries reference
// column (DependencyType: Other) or use SELECT * (DependencyType: TableOnly).
// Query-history errors are recorded as report-level warnings rather than
// returned as per-column lookup errors so Dataplex severities stay intact.
func (j *jobHistorySource) columnLinks(ctx context.Context, table, column string) []ColumnLink {
	tc := j.forTable(ctx, table)
	if len(tc.buckets) == 0 {
		return nil
	}
	matcher := NewColumnMatcher(column)
	var out []ColumnLink
	for _, b := range tc.buckets {
		directCount := 0
		directLast, directJobID, directJobURL := "", "", ""
		probableCount := 0
		probableLast, probableJobID, probableJobURL := "", "", ""
		for _, q := range b.queries {
			r, s := matcher.Match(q.stripped)
			if r {
				// On single-table queries an explicit column mention is a confirmed
				// read (Other); on multi-table JOINs a bare column name could belong
				// to another joined table, so classify as TableOnly (Warning).
				if !q.stripped.HasJoin() {
					directCount += q.count
					if q.lastSeen > directLast || (q.lastSeen == directLast && directJobURL == "" && q.jobURL != "") {
						directLast, directJobID, directJobURL = q.lastSeen, q.jobID, q.jobURL
					}
					continue
				}
				probableCount += q.count
				if q.lastSeen > probableLast || (q.lastSeen == probableLast && probableJobURL == "" && q.jobURL != "") {
					probableLast, probableJobID, probableJobURL = q.lastSeen, q.jobID, q.jobURL
				}
			} else if s {
				probableCount += q.count
				if q.lastSeen > probableLast || (q.lastSeen == probableLast && probableJobURL == "" && q.jobURL != "") {
					probableLast, probableJobID, probableJobURL = q.lastSeen, q.jobID, q.jobURL
				}
			}
		}
		if directCount == 0 && probableCount == 0 {
			continue
		}
		dep := Other
		c := b.consumer
		if directCount > 0 {
			c.Queries = directCount + probableCount
			c.LastSeen = directLast
			c.SampleJobID = directJobID
			c.SampleJobURL = directJobURL
		} else {
			dep = TableOnly
			c.Queries = probableCount
			c.LastSeen = probableLast
			c.SampleJobID = probableJobID
			c.SampleJobURL = probableJobURL
		}
		out = append(out, ColumnLink{
			Table:          c.ID(),
			DependencyType: dep,
			Consumer:       &c,
		})
	}
	return out
}

// tableLinks returns every external consumer that queried table (for deleted
// or renamed tables, where any SELECT reader breaks).
func (j *jobHistorySource) tableLinks(ctx context.Context, table string) []ColumnLink {
	tc := j.forTable(ctx, table)
	if len(tc.buckets) == 0 {
		return nil
	}
	out := make([]ColumnLink, 0, len(tc.buckets))
	for _, b := range tc.buckets {
		c := b.consumer
		out = append(out, ColumnLink{
			Table:          c.ID(),
			DependencyType: TableOnly,
			Consumer:       &c,
		})
	}
	return out
}
