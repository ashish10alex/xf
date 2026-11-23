package databricks

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	// maxExtHistoryPages caps REST Query History pagination for external
	// consumer discovery (up to 10,000 recent queries, zero warehouse wake-up).
	maxExtHistoryPages = 10
	// maxExtQueriesPerConsumer caps distinct SQL shapes kept per consumer.
	maxExtQueriesPerConsumer = 50
)

type extQuerySample struct {
	stripped lineage.StrippedQuery
	count    int
	lastSeen string
	jobID    string
	jobURL   string
}

type extConsumerBucket struct {
	consumer lineage.Consumer
	queries  []extQuerySample
	seenIdx  map[string]int
}

type extCall struct {
	done    chan struct{}
	tables  map[string]bool
	byTable map[string][]extConsumerBucket
}

// WithExternalReaders enables scanning Databricks SQL Query History
// (/api/2.0/sql/history/queries, which needs no SQL warehouse) for external
// SELECT-only BI consumers (Tableau, Power BI, Looker, JDBC/ODBC, etc.) over
// the last days (clamped to [1, lineage.MaxJobHistoryDays]).
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

// Warnings returns report-level warnings from optional external Query History
// lookups without downgrading Unity Catalog severities.
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

// Preload starts one asynchronous REST Query History scan covering all at-risk
// tables so external BI discovery overlaps Unity Catalog lineage calls.
func (r *Readers) Preload(ctx context.Context, tables []string) {
	if r.extDays <= 0 || len(tables) == 0 {
		return
	}
	want := map[string]bool{}
	for _, t := range tables {
		if k := strings.ToLower(strings.TrimSpace(t)); k != "" {
			want[k] = true
		}
	}
	if len(want) == 0 {
		return
	}
	r.mu.Lock()
	if r.ext != nil && coversAll(r.ext.tables, want) {
		r.mu.Unlock()
		return
	}
	call := &extCall{done: make(chan struct{}), tables: want}
	r.ext = call
	r.mu.Unlock()

	go func() {
		byTable, cancelled := r.scanExternalHistory(ctx, tables)
		if cancelled {
			r.mu.Lock()
			if r.ext == call {
				r.ext = nil
			}
			r.mu.Unlock()
		} else {
			call.byTable = byTable
		}
		close(call.done)
	}()
}

func coversAll(have, want map[string]bool) bool {
	for k := range want {
		if !have[k] {
			return false
		}
	}
	return true
}

func (r *Readers) extBuckets(ctx context.Context, table string) []extConsumerBucket {
	if r.extDays <= 0 {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(table))
	if key == "" {
		return nil
	}
	r.mu.Lock()
	call := r.ext
	if call == nil || !call.tables[key] {
		merged := map[string]bool{key: true}
		var list []string
		if call != nil {
			for k := range call.tables {
				merged[k] = true
			}
		}
		for k := range merged {
			list = append(list, k)
		}
		call = &extCall{done: make(chan struct{}), tables: merged}
		r.ext = call
		r.mu.Unlock()
		go func() {
			byTable, cancelled := r.scanExternalHistory(ctx, list)
			if cancelled {
				r.mu.Lock()
				if r.ext == call {
					r.ext = nil
				}
				r.mu.Unlock()
			} else {
				call.byTable = byTable
			}
			close(call.done)
		}()
	} else {
		r.mu.Unlock()
	}
	select {
	case <-call.done:
		return call.byTable[key]
	case <-ctx.Done():
		return nil
	}
}

func (r *Readers) extColumnLinks(ctx context.Context, table, column string) []lineage.ColumnLink {
	buckets := r.extBuckets(ctx, table)
	if len(buckets) == 0 {
		return nil
	}
	matcher := lineage.NewColumnMatcher(column)
	var out []lineage.ColumnLink
	for _, b := range buckets {
		directCount := 0
		directLast, directJobID, directJobURL := "", "", ""
		probableCount := 0
		probableLast, probableJobID, probableJobURL := "", "", ""
		for _, q := range b.queries {
			reads, star := matcher.Match(q.stripped)
			if reads {
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
				continue
			}
			if star {
				probableCount += q.count
				if q.lastSeen > probableLast || (q.lastSeen == probableLast && probableJobURL == "" && q.jobURL != "") {
					probableLast, probableJobID, probableJobURL = q.lastSeen, q.jobID, q.jobURL
				}
			}
		}
		if directCount == 0 && probableCount == 0 {
			continue
		}
		dep := lineage.Other
		c := b.consumer
		if directCount > 0 {
			c.Queries = directCount + probableCount
			c.LastSeen = directLast
			c.SampleJobID = directJobID
			c.SampleJobURL = directJobURL
		} else {
			dep = lineage.TableOnly
			c.Queries = probableCount
			c.LastSeen = probableLast
			c.SampleJobID = probableJobID
			c.SampleJobURL = probableJobURL
		}
		out = append(out, lineage.ColumnLink{
			Table:          c.ID(),
			DependencyType: dep,
			Consumer:       &c,
		})
	}
	return out
}

func (r *Readers) extTableLinks(ctx context.Context, table string) []lineage.ColumnLink {
	buckets := r.extBuckets(ctx, table)
	if len(buckets) == 0 {
		return nil
	}
	out := make([]lineage.ColumnLink, 0, len(buckets))
	for _, b := range buckets {
		c := b.consumer
		out = append(out, lineage.ColumnLink{
			Table:          c.ID(),
			DependencyType: lineage.TableOnly,
			Consumer:       &c,
		})
	}
	return out
}

type tableTargetMatcher struct {
	lower string
	full  lineage.ColumnMatcher // exact 3-part catalog.schema.table (no leaf fallback)
	short lineage.ColumnMatcher // exact 2-part schema.table (no leaf fallback)
}

func (r *Readers) scanExternalHistory(ctx context.Context, tables []string) (map[string][]extConsumerBucket, bool) {
	var targets []tableTargetMatcher
	seenTarget := map[string]bool{}
	for _, t := range tables {
		lower := strings.ToLower(strings.TrimSpace(t))
		if lower == "" || seenTarget[lower] {
			continue
		}
		seenTarget[lower] = true
		tm := tableTargetMatcher{
			lower: lower,
			full:  lineage.NewExactIdentMatcher(t),
		}
		_, sch, tbl := splitFull(t)
		if sch != "" && tbl != "" {
			tm.short = lineage.NewExactIdentMatcher(sch + "." + tbl)
		}
		targets = append(targets, tm)
	}
	if len(targets) == 0 {
		return nil, false
	}

	nowFn := r.now
	if nowFn == nil {
		nowFn = time.Now
	}
	since := nowFn().UTC().Add(-time.Duration(r.extDays) * 24 * time.Hour)

	byTable := map[string]map[string]*extConsumerBucket{}
	orderByTable := map[string][]string{}
	var oldestStart int64

	token := ""
	for page := 0; ; page++ {
		if page == maxExtHistoryPages {
			coverage := fmt.Sprintf("last %dd", r.extDays)
			if oldestStart > 0 {
				coverage = "back to " + time.UnixMilli(oldestStart).UTC().Format("2006-01-02")
			}
			r.addWarning(fmt.Sprintf("Databricks Query History hit the %d-page scan cap (covers %s); older external SELECT readers may not be shown", maxExtHistoryPages, coverage))
			break
		}
		queries, next, err := r.c.historyPage(ctx, since, token)
		if err != nil {
			if ctx.Err() != nil {
				return nil, true
			}
			r.addWarning(fmt.Sprintf("Databricks Query History unavailable for external SELECT readers: %v", err))
			break
		}
		for _, q := range queries {
			if q.Start > 0 && (oldestStart == 0 || q.Start < oldestStart) {
				oldestStart = q.Start
			}
			if q.Status != "" && q.Status != "FINISHED" {
				continue
			}
			kind := strings.ToUpper(q.StatementType)
			if kind == "" || kind == "OTHER" {
				kind, _, _ = strings.Cut(warehouse.StatementType(q.Text), "_")
			}
			if kind != "SELECT" {
				continue
			}
			if lineage.IsOwnXFQuery(q.ID, q.Text, nil) || lineage.IsTransformToolQuery(q.Text, "", nil) {
				continue
			}
			stripped := lineage.StripQueryForMatch(q.Text, false)
			var matched []string
			for _, tm := range targets {
				hitFull, _ := tm.full.Match(stripped)
				hitShort := false
				if !hitFull {
					hitShort, _ = tm.short.Match(stripped)
				}
				if hitFull || hitShort {
					matched = append(matched, tm.lower)
				}
			}
			if len(matched) == 0 {
				continue
			}

			clientApp := ""
			if q.ChannelUsed != nil {
				clientApp = q.ChannelUsed.Name
			}
			cons := lineage.ParseConsumer(lineage.QuerySignal{
				User:      cmp.Or(q.UserName, q.ExecutedAs),
				Query:     q.Text,
				ClientApp: clientApp,
			})
			cons.Queries = 1
			cons.Days = r.extDays
			if q.Start > 0 {
				cons.LastSeen = time.UnixMilli(q.Start).UTC().Format(time.RFC3339)
			}
			cons.SampleJobID = strings.TrimSpace(q.ID)
			cons.SampleJobURL = lineage.DatabricksJobURL(r.c.Host(), cons.SampleJobID)
			id := cons.ID()
			normKey := stripped.ShapeKey()

			for _, tbl := range matched {
				if byTable[tbl] == nil {
					byTable[tbl] = map[string]*extConsumerBucket{}
				}
				b := byTable[tbl][id]
				if b == nil {
					b = &extConsumerBucket{consumer: cons, seenIdx: map[string]int{}}
					byTable[tbl][id] = b
					orderByTable[tbl] = append(orderByTable[tbl], id)
				} else {
					b.consumer.Queries++
					if cons.LastSeen > b.consumer.LastSeen || (cons.LastSeen == b.consumer.LastSeen && b.consumer.SampleJobURL == "" && cons.SampleJobURL != "") {
						b.consumer.LastSeen = cons.LastSeen
						b.consumer.SampleJobID = cons.SampleJobID
						b.consumer.SampleJobURL = cons.SampleJobURL
					}
					if b.consumer.User == "" {
						b.consumer.User = cons.User
					}
				}
				if idx, ok := b.seenIdx[normKey]; ok {
					b.queries[idx].count++
					if cons.LastSeen > b.queries[idx].lastSeen || (cons.LastSeen == b.queries[idx].lastSeen && b.queries[idx].jobURL == "" && cons.SampleJobURL != "") {
						b.queries[idx].lastSeen = cons.LastSeen
						b.queries[idx].jobID = cons.SampleJobID
						b.queries[idx].jobURL = cons.SampleJobURL
					}
				} else if len(b.queries) < maxExtQueriesPerConsumer {
					b.seenIdx[normKey] = len(b.queries)
					b.queries = append(b.queries, extQuerySample{
						stripped: stripped,
						count:    1,
						lastSeen: cons.LastSeen,
						jobID:    cons.SampleJobID,
						jobURL:   cons.SampleJobURL,
					})
				}
			}
		}
		if token = next; token == "" {
			break
		}
	}

	out := make(map[string][]extConsumerBucket, len(byTable))
	for tbl, ids := range orderByTable {
		buckets := make([]extConsumerBucket, 0, len(ids))
		for _, id := range ids {
			buckets = append(buckets, *byTable[tbl][id])
		}
		out[tbl] = buckets
	}
	return out, false
}
