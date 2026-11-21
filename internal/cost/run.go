package cost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// dryRunBatch is how many dry runs start at once.
const dryRunBatch = 6

// Deps are what Run needs from the outside world.
type Deps struct {
	// DryRun plans sql without running it.
	DryRun func(ctx context.Context, project, location, sql string) (*warehouse.Check, error)
	// Partition is how a table is partitioned: nil (and no error) when it
	// isn't, or there is no such table. A nil Partition looks for no
	// unfiltered reads.
	Partition func(ctx context.Context, t warehouse.TableRef) (*warehouse.Partition, error)
	// Base compiles the merge-base as the head was compiled, so its
	// actions have the head's IDs; nil estimates no merge-base. It is
	// called once, while the head is dry run.
	Base func(ctx context.Context) (*project.Graph, error)
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// InDev says whether a table the branch adds is built in dev: dev is
	// its table there, built at builtAt (zero when not known). id is its
	// ID in the compile the Changed Actions were worked out from. A dry run
	// that doesn't find the table where it looks then reads dev's copy;
	// nil reads nothing from dev.
	InDev func(ctx context.Context, id project.ActionID) (dev project.Target, builtAt time.Time, ok bool)
}

// Head is the compile whose SQL is dry run.
type Head struct {
	Graph *project.Graph
	// ID is the ID in Graph of an action of the compile the Changed
	// Actions were worked out from, which differs when Graph was compiled
	// with other options; nil when they are the same compile.
	ID func(project.ActionID) project.ActionID
	// Compiled is Report.Compiled.
	Compiled string
}

// inBatches runs run for every item with up to size workers running at once,
// starting each next item as soon as a worker finishes instead of waiting for
// a lockstep batch. Stops starting new items once ctx is done.
func inBatches[T any](ctx context.Context, items []T, size int, run func(T)) {
	if len(items) == 0 {
		return
	}
	workers := min(max(size, 1), len(items))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1) - 1)
				if i >= len(items) {
					return
				}
				run(items[i])
			}
		})
	}
	wg.Wait()
}

func errorText(err error) string {
	var api *warehouse.APIError
	if errors.As(err, &api) {
		return api.Message
	}
	return err.Error()
}

// estimateOf is a dry run's estimate, or why it has none: BigQuery reports
// 0 bytes when it cannot tell, and gives no figure for most scripts.
func estimateOf(d *warehouse.Check) (*Estimate, string) {
	switch {
	case d.StatementType == "SCRIPT" && d.Accuracy != warehouse.AccuracyPrecise && d.Accuracy != warehouse.AccuracyUpperBound:
		return nil, "BigQuery gives no estimate for a script"
	case d.Accuracy == warehouse.AccuracyUnknown && d.Bytes == 0:
		return nil, "BigQuery gave no estimate"
	}
	return &Estimate{Bytes: d.Bytes, Accuracy: d.Accuracy}, ""
}

// counted is the query of a whose bytes count, an incremental table's
// incremental run, and the query of its full rebuild when that is another.
func counted(a *project.Action) (run, full *project.DryRunQuery) {
	queries := a.DryRunQueries()
	switch len(queries) {
	case 0:
		return nil, nil
	case 1:
		return &queries[0], nil
	}
	return &queries[len(queries)-1], &queries[0]
}

// partitions looks tables' partitioning up once each.
type partitions struct {
	lookup func(ctx context.Context, t warehouse.TableRef) (*warehouse.Partition, error)

	mu   sync.Mutex
	seen map[warehouse.TableRef]*partitionLookup
}

type partitionLookup struct {
	once sync.Once
	p    *warehouse.Partition
}

func (ps *partitions) of(ctx context.Context, t warehouse.TableRef) *warehouse.Partition {
	ps.mu.Lock()
	l := ps.seen[t]
	if l == nil {
		l = &partitionLookup{}
		ps.seen[t] = l
	}
	ps.mu.Unlock()
	// A table that can't be read is taken as unpartitioned: no warning.
	l.once.Do(func() { l.p, _ = ps.lookup(ctx, t) })
	return l.p
}

// unfiltered are the partitioned tables sql reads without filtering on their
// partition column, plus any partitioned MERGE target whose ON / WHEN ... AND
// clauses omit its partition column. A table read through a view isn't named in
// sql, and isn't judged: the view may filter it.
func (ps *partitions) unfiltered(ctx context.Context, sql string, tables []warehouse.TableRef) []Unfiltered {
	if ps == nil {
		return nil
	}
	code := strings.ToLower(project.StripSQL(sql, project.StripOptions{BlankSingleStrings: true, BlankDoubleStrings: true}))
	merges := findMergeTargets(code)
	var out []Unfiltered
	seen := map[string]bool{}
	seenRef := map[warehouse.TableRef]bool{}
	for _, t := range tables {
		seenRef[t] = true
		fullName := t.Project + "." + t.Dataset + "." + t.Table
		if matched := matchMergeTargets(merges, t); len(matched) > 0 {
			p := ps.of(ctx, t)
			if p != nil {
				col := p.Column
				if col == "" {
					col = "_PARTITIONTIME"
				}
				for _, m := range matched {
					var filtered bool
					if p.Column == "" {
						filtered = names(m.filterClauses, "_PARTITIONTIME") || names(m.filterClauses, "_PARTITIONDATE")
					} else {
						filtered = names(m.filterClauses, p.Column)
					}
					if !filtered {
						out = append(out, Unfiltered{Table: fullName, Column: col, MergeTarget: true})
						seen[strings.ToLower(fullName)] = true
						break
					}
				}
			}
			// If the table is also read in a USING/FROM subquery (e.g. self-MERGE),
			// fall through when it wasn't already flagged as a MERGE target.
			if seen[strings.ToLower(fullName)] || !readOutsideMergeTarget(code, matched, t) {
				continue
			}
		}
		if !names(code, t.Table) {
			continue
		}
		p := ps.of(ctx, t)
		if p == nil {
			continue
		}
		column, filtered := p.Column, mentionsInFilterClause(code, p.Column)
		if column == "" {
			column = "_PARTITIONTIME"
			filtered = mentionsInFilterClause(code, "_PARTITIONTIME") || mentionsInFilterClause(code, "_PARTITIONDATE")
		}
		if !filtered && !seen[strings.ToLower(fullName)] {
			seen[strings.ToLower(fullName)] = true
			out = append(out, Unfiltered{Table: fullName, Column: column})
		}
	}
	// If BigQuery omitted a 3-part MERGE target from referencedTables, check it
	// directly so MERGE target warnings still fire.
	for _, m := range merges {
		parts := strings.Split(m.target, ".")
		if len(parts) != 3 {
			continue
		}
		ref := warehouse.TableRef{Project: parts[0], Dataset: parts[1], Table: parts[2]}
		if seenRef[ref] || seen[m.target] {
			continue
		}
		// Match case-insensitively against seenRef too.
		already := false
		for sr := range seenRef {
			if strings.EqualFold(sr.Project+"."+sr.Dataset+"."+sr.Table, m.target) {
				already = true
				break
			}
		}
		if already {
			continue
		}
		p := ps.of(ctx, ref)
		if p == nil {
			continue
		}
		col := p.Column
		var filtered bool
		if col == "" {
			col = "_PARTITIONTIME"
			filtered = names(m.filterClauses, "_PARTITIONTIME") || names(m.filterClauses, "_PARTITIONDATE")
		} else {
			filtered = names(m.filterClauses, col)
		}
		if !filtered {
			seen[m.target] = true
			out = append(out, Unfiltered{Table: m.target, Column: col, MergeTarget: true})
		}
	}
	return out
}

type mergeTarget struct {
	target        string // lowercased, backticks stripped (e.g. proj.ds.tbl or ds.tbl)
	targetStart   int    // byte offset in code where target begins
	targetEnd     int    // byte offset in code where target ends
	filterClauses string // lowercased SQL from top-level ON and WHEN ... AND clauses
}

// findMergeTargets extracts every MERGE [INTO] <target> ... USING ... ON <cond>
// (plus any top-level WHEN ... AND <cond> predicates before THEN, which BigQuery
// also uses for target partition pruning, including dbt's insert_overwrite).
func findMergeTargets(code string) []mergeTarget {
	toks := scanSQLTokens(code)
	var out []mergeTarget
	for i := 0; i < len(toks); i++ {
		if toks[i].text != "merge" || toks[i].depth != 0 {
			continue
		}
		j := i + 1
		if j < len(toks) && toks[j].text == "into" {
			j++
		}
		if j >= len(toks) {
			break
		}
		targetStart := toks[j].start
		target, next := readTableRef(toks, j)
		if target == "" {
			continue
		}
		targetEnd := toks[next-1].end
		seenUsing := false
		inOn := false
		onStart := -1
		inWhen := false
		whenAndStart := -1
		var filters strings.Builder
		for k := next; k < len(toks); k++ {
			if toks[k].depth != 0 {
				continue
			}
			if toks[k].text == ";" {
				if inOn && onStart >= 0 {
					filters.WriteString(" ")
					filters.WriteString(code[onStart:toks[k].start])
				}
				if whenAndStart >= 0 {
					filters.WriteString(" ")
					filters.WriteString(code[whenAndStart:toks[k].start])
				}
				i = k
				break
			}
			if !seenUsing {
				if toks[k].text == "using" {
					seenUsing = true
				}
				continue
			}
			if !inOn && onStart < 0 {
				if toks[k].text == "on" {
					inOn = true
					onStart = toks[k].end
				}
				continue
			}
			if toks[k].text == "when" {
				if inOn && onStart >= 0 {
					filters.WriteString(" ")
					filters.WriteString(code[onStart:toks[k].start])
					inOn = false
				}
				if whenAndStart >= 0 {
					filters.WriteString(" ")
					filters.WriteString(code[whenAndStart:toks[k].start])
					whenAndStart = -1
				}
				inWhen = true
				continue
			}
			if inWhen && toks[k].text == "and" && whenAndStart < 0 {
				whenAndStart = toks[k].end
				continue
			}
			if inWhen && toks[k].text == "then" {
				if whenAndStart >= 0 {
					filters.WriteString(" ")
					filters.WriteString(code[whenAndStart:toks[k].start])
					whenAndStart = -1
				}
				inWhen = false
			}
		}
		if inOn && onStart >= 0 {
			filters.WriteString(" ")
			filters.WriteString(code[onStart:])
		}
		if whenAndStart >= 0 {
			filters.WriteString(" ")
			filters.WriteString(code[whenAndStart:])
		}
		if onStart >= 0 {
			out = append(out, mergeTarget{
				target:        target,
				targetStart:   targetStart,
				targetEnd:     targetEnd,
				filterClauses: filters.String(),
			})
		}
	}
	return out
}

func matchMergeTargets(merges []mergeTarget, t warehouse.TableRef) []mergeTarget {
	full := strings.ToLower(t.Project + "." + t.Dataset + "." + t.Table)
	short := strings.ToLower(t.Dataset + "." + t.Table)
	bare := strings.ToLower(t.Table)
	var out []mergeTarget
	for _, m := range merges {
		dots := strings.Count(m.target, ".")
		switch {
		case dots == 2 && m.target == full:
			out = append(out, m)
		case dots == 1 && m.target == short:
			out = append(out, m)
		case dots == 0 && m.target == bare:
			out = append(out, m)
		}
	}
	return out
}

// readOutsideMergeTarget reports whether t's table name appears in code outside
// the MERGE target position(s) of matched (e.g. a self-MERGE reading t in USING).
func readOutsideMergeTarget(code string, matched []mergeTarget, t warehouse.TableRef) bool {
	buf := []byte(code)
	for _, m := range matched {
		for i := m.targetStart; i < m.targetEnd && i < len(buf); i++ {
			buf[i] = ' '
		}
	}
	return names(string(buf), t.Table)
}

type sqlToken struct {
	text       string
	start, end int
	depth      int
	quoted     bool
}

func scanSQLTokens(code string) []sqlToken {
	var toks []sqlToken
	depth := 0
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, sqlToken{text: "(", start: i, end: i + 1, depth: depth})
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			toks = append(toks, sqlToken{text: ")", start: i, end: i + 1, depth: depth})
			i++
		case c == ';' || c == '.' || c == ',':
			toks = append(toks, sqlToken{text: code[i : i+1], start: i, end: i + 1, depth: depth})
			i++
		case c == '`':
			j := i + 1
			for j < len(code) && code[j] != '`' {
				j++
			}
			end := min(len(code), j+1)
			toks = append(toks, sqlToken{text: code[i+1 : min(j, len(code))], start: i, end: end, depth: depth, quoted: true})
			i = end
		case identChar(c):
			j := i + 1
			for j < len(code) && identChar(code[j]) {
				j++
			}
			toks = append(toks, sqlToken{text: code[i:j], start: i, end: j, depth: depth})
			i = j
		default:
			i++
		}
	}
	return toks
}

func readTableRef(toks []sqlToken, i int) (string, int) {
	if i >= len(toks) || toks[i].text == "(" || toks[i].text == ")" || toks[i].text == ";" || toks[i].text == "." {
		return "", i
	}
	var parts []string
	parts = append(parts, strings.ReplaceAll(toks[i].text, "`", ""))
	j := i + 1
	for j+1 < len(toks) && toks[j].text == "." {
		parts = append(parts, strings.ReplaceAll(toks[j+1].text, "`", ""))
		j += 2
	}
	return strings.ToLower(strings.Join(parts, ".")), j
}

// filterEnterKeywords begin a clause where referencing a partition column can
// prune partitions (WHERE, JOIN ON, USING). HAVING and QUALIFY run after
// aggregation/windowing and do not prune partitions.
var filterEnterKeywords = map[string]bool{
	"where": true, "on": true, "using": true,
}

// filterExitKeywords leave a filter clause so columns appearing only in
// SELECT, GROUP BY, ORDER BY, etc. do not count as partition filters.
var filterExitKeywords = map[string]bool{
	"select": true, "from": true, "group": true, "order": true, "limit": true,
	"having": true, "qualify": true, "union": true, "intersect": true, "except": true,
	"window": true, "into": true, "set": true, "values": true, "update": true,
	"insert": true, "delete": true, "merge": true, "returning": true, "join": true,
	";": true,
}

var joinModifiers = map[string]bool{
	"left": true, "right": true, "inner": true, "full": true, "cross": true, "outer": true,
}

// nonFuncKeywords are SQL keywords that may precede '(' without making it a
// function call (so keywords like FROM inside a subquery still apply).
var nonFuncKeywords = map[string]bool{
	"where": true, "and": true, "or": true, "not": true, "in": true, "exists": true,
	"on": true, "using": true, "as": true, "select": true, "from": true, "join": true,
	"when": true, "then": true, "else": true, "case": true, "having": true, "qualify": true,
	"over": true, "values": true, "union": true, "intersect": true, "except": true,
	"(": true, ",": true, ";": true,
}

// mentionsInFilterClause reports whether code (stripped, lowercased SQL) names
// ident inside a partition-pruning filter clause (WHERE, ON, USING) rather than
// only in SELECT/GROUP BY/ORDER BY. Function calls like LEFT(dt, 4) and
// EXTRACT(DATE FROM ts) inside WHERE stay in filter context.
func mentionsInFilterClause(code, ident string) bool {
	ident = strings.ToLower(ident)
	if ident == "" {
		return false
	}
	toks := scanSQLTokens(code)
	type frame struct {
		inFilter bool
		funcCall bool
	}
	stack := []frame{{}}
	for i, tok := range toks {
		switch tok.text {
		case "(":
			cur := stack[len(stack)-1]
			isFunc := i > 0 && !toks[i-1].quoted && identChar(toks[i-1].text[0]) && !nonFuncKeywords[toks[i-1].text]
			stack = append(stack, frame{inFilter: cur.inFilter, funcCall: isFunc})
			continue
		case ")":
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		cur := &stack[len(stack)-1]
		if !tok.quoted {
			if tok.text == "select" {
				cur.inFilter = false
				cur.funcCall = false
				continue
			}
			if !cur.funcCall {
				if filterEnterKeywords[tok.text] {
					cur.inFilter = true
					continue
				}
				if joinModifiers[tok.text] {
					if i+1 < len(toks) && (toks[i+1].text == "join" || toks[i+1].text == "outer") {
						cur.inFilter = false
						continue
					}
				} else if filterExitKeywords[tok.text] {
					cur.inFilter = false
					continue
				}
			}
		}
		if cur.inFilter && tok.text == ident {
			return true
		}
	}
	return false
}

func identChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// names reports whether code, lower-cased SQL, has ident as a whole word.
func names(code, ident string) bool {
	ident = strings.ToLower(ident)
	if ident == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(code[from:], ident)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(ident)
		if (start == 0 || !identChar(code[start-1])) && (end == len(code) || !identChar(code[end])) {
			return true
		}
		from = start + 1
	}
}

type builtLookup struct {
	once sync.Once
	ok   bool
}

type runner struct {
	deps Deps
	ps   *partitions
	// added are the tables the branch adds, by their lower-cased
	// project.dataset.name in the compile dry run.
	added map[string]addedTable
	// built says whether an added table is where the dry runs look, by
	// its key in added; guarded by mu.
	mu    sync.Mutex
	built map[string]*builtLookup
}

// addedTable is a table the branch adds: target in the compile dry run, id
// in the one the Changed Actions were worked out from, and re matching its
// table reference in Dataform or dbt-bigquery SQL.
type addedTable struct {
	target project.Target
	id     project.ActionID
	re     *regexp.Regexp
}

// missingRE is BigQuery's message for a query that reads a table, view or
// dataset it doesn't have, as dryRun words it.
var missingRE = regexp.MustCompile(`^dry run failed: Not found: (Table|View|Dataset) (\S+) was not found`)

// missing is what a dry run didn't find, lower-cased: a table's
// project.dataset.name, or a dataset's project.dataset.
type missing struct {
	name    string
	dataset bool
}

// missingIn is what reason says a dry run didn't find; ok is false when
// it failed for another reason.
func missingIn(reason string) (m missing, ok bool) {
	sub := missingRE.FindStringSubmatch(reason)
	if sub == nil {
		return missing{}, false
	}
	return missing{name: strings.ToLower(strings.Replace(sub[2], ":", ".", 1)), dataset: sub[1] == "Dataset"}, true
}

// covers says t is m, or is in it.
func (m missing) covers(t project.Target) bool {
	id := strings.ToLower(string(t.ID()))
	if m.dataset {
		return strings.HasPrefix(id, m.name+".")
	}
	return id == m.name
}

// quoted is how a Dataform compile names a table in its SQL.
func quoted(id string) string { return "`" + id + "`" }

// targetRefRE matches a table reference in either Dataform's single-backtick
// form (`project.dataset.table`) or dbt-bigquery's per-part-backtick form
// (`project`.`dataset`.`table`), preserving BigQuery's case sensitivity.
func targetRefRE(id string) *regexp.Regexp {
	parts := strings.Split(id, ".")
	quotedParts := make([]string, len(parts))
	for i, p := range parts {
		quotedParts[i] = regexp.QuoteMeta(p)
	}
	whole := "`" + strings.Join(quotedParts, `\.`) + "`"
	if len(parts) == 1 {
		return regexp.MustCompile(whole)
	}
	each := "`" + strings.Join(quotedParts, "`\\.`") + "`"
	return regexp.MustCompile(whole + `|` + each)
}

func replaceTargetRef(sql, fromID, toID string) string {
	return targetRefRE(fromID).ReplaceAllLiteralString(sql, quoted(toID))
}

// readFromDev adds to reads dev's copy of the tables the branch adds that
// m is, or that sql reads in m, a dataset, when they are built there. It
// is false when none is, or each is in reads already.
func (r *runner) readFromDev(ctx context.Context, m missing, sql string, reads *[]DevRead) bool {
	if r.deps.InDev == nil {
		return false
	}
	var keys []string
	for k, t := range r.added {
		if m.covers(t.target) && (!m.dataset || t.re.MatchString(sql)) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	read := false
	for _, k := range keys {
		t := r.added[k]
		if slices.ContainsFunc(*reads, func(d DevRead) bool { return d.Table == string(t.target.ID()) }) {
			continue
		}
		dev, builtAt, ok := r.deps.InDev(ctx, t.id)
		if !ok || dev == t.target {
			continue
		}
		*reads = append(*reads, DevRead{Table: string(t.target.ID()), Dev: string(dev.ID()), BuiltAt: builtAt})
		read = true
	}
	return read
}

// waits are the tables the branch adds that sql, a dry run of a that
// didn't find m, reads and BigQuery doesn't have where a's dry runs look:
// those m is, and each other one a dry run of its own doesn't find. It is
// nil when m is none of them.
func (r *runner) waits(ctx context.Context, g *project.Graph, a *project.Action, m missing, sql string) []string {
	var keys []string
	found := false
	for k, t := range r.added {
		if t.target == a.Target || !t.re.MatchString(sql) {
			continue
		}
		keys = append(keys, k)
		found = found || m.covers(t.target)
	}
	if !found {
		return nil
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		t := r.added[k]
		if m.covers(t.target) || !r.isBuilt(ctx, g, a, k) {
			out = append(out, string(t.target.ID()))
		}
	}
	return out
}

// isBuilt says whether the table the branch adds of key k is where a's
// dry runs look, by a dry run that reads it alone; it is true when that
// dry run fails for another reason, which says nothing of the table.
func (r *runner) isBuilt(ctx context.Context, g *project.Graph, a *project.Action, k string) bool {
	r.mu.Lock()
	l := r.built[k]
	if l == nil {
		l = &builtLookup{}
		r.built[k] = l
	}
	r.mu.Unlock()
	l.once.Do(func() {
		proj, location := g.DryRunTarget(a)
		_, err := r.deps.DryRun(ctx, proj, location, "SELECT 1 FROM "+quoted(string(r.added[k].target.ID()))+" WHERE FALSE")
		l.ok = true
		if err != nil {
			_, notFound := missingIn("dry run failed: " + errorText(err))
			l.ok = !notFound
		}
	})
	return l.ok
}

// fromDev is q reading the tables of reads from dev.
func fromDev(q *project.DryRunQuery, reads []DevRead) *project.DryRunQuery {
	if len(reads) == 0 {
		return q
	}
	out := *q
	out.PreOps = slices.Clone(q.PreOps)
	for _, d := range reads {
		out.SQL = replaceTargetRef(out.SQL, d.Table, d.Dev)
		out.Query = replaceTargetRef(out.Query, d.Table, d.Dev)
		for i := range out.PreOps {
			out.PreOps[i] = replaceTargetRef(out.PreOps[i], d.Table, d.Dev)
		}
	}
	return &out
}

// writeDenied is the write reason says BigQuery refused to plan.
func writeDenied(reason string) (w Write, ok bool) {
	table, perm, ok := project.WriteDenied(reason)
	if !ok {
		return Write{}, false
	}
	return Write{Table: table, Permission: perm}, true
}

// deniedReason is why an action whose dry run was refused w has no
// estimate.
func deniedReason(a *project.Action, w Write) string {
	table := impact.ShortTable(w.Table)
	if strings.EqualFold(w.Table, string(a.Target.ID())) {
		table = "its own table"
	}
	return "dry run failed: it writes to " + table + ", and BigQuery checks " + w.Permission + " on it even in a dry run"
}

// dryRun dry runs q as an action of g, returning its estimate, the dry run,
// the SQL that actually ran, why it has no estimate, and any pre_operations
// write left out. err is the dry run's own failure. BigQuery checks that
// the caller may write to the table a DML statement writes to even when it
// only plans it: refused that for DML in q's pre_operations, it dry runs q
// without them, and left is the write left out.
func (r *runner) dryRun(ctx context.Context, g *project.Graph, a *project.Action, q *project.DryRunQuery) (est *Estimate, d *warehouse.Check, ranSQL, reason string, left *Write, err error) {
	proj, location := g.DryRunTarget(a)
	if proj == "" {
		return nil, nil, q.SQL, "no project to dry run in: set defaultProject", nil, nil
	}
	d, err = r.deps.DryRun(ctx, proj, location, q.SQL)
	if err != nil {
		reason = "dry run failed: " + errorText(err)
		w, ok := writeDenied(reason)
		if !ok {
			return nil, nil, q.SQL, reason, nil, err
		}
		if lean, ok := q.WithoutDML(); ok {
			if d, leanErr := r.deps.DryRun(ctx, proj, location, lean.SQL); leanErr == nil {
				est, reason = estimateOf(d)
				return est, d, lean.SQL, reason, &w, nil
			}
		}
		return nil, nil, q.SQL, deniedReason(a, w), nil, err
	}
	est, reason = estimateOf(d)
	if est == nil && d.StatementType == "SCRIPT" {
		if recEst, recD, recSQL := r.recoverScript(ctx, proj, location, q); recEst != nil {
			return recEst, recD, recSQL, "", nil, nil
		}
	}
	return est, d, q.SQL, reason, nil, nil
}

// recoverScript tries to recover a lower-bound byte estimate when BigQuery
// returns StatementType == "SCRIPT" with no byte count: first by dry-running
// q.Query alone (when q.SQL combined pre_operations + query), else by
// dry-running standalone SELECT/DML statements split from q.SQL.
func (r *runner) recoverScript(ctx context.Context, proj, location string, q *project.DryRunQuery) (*Estimate, *warehouse.Check, string) {
	if strings.TrimSpace(q.Query) != "" {
		if subD, err := r.deps.DryRun(ctx, proj, location, q.Query); err == nil {
			if subEst, _ := estimateOf(subD); subEst != nil {
				subEst.Accuracy = warehouse.AccuracyLowerBound
				return subEst, subD, q.Query
			}
		}
	}
	stmts := splitSQLStatements(q.SQL)
	if len(stmts) <= 1 {
		return nil, nil, ""
	}
	var totalBytes int64
	var accuracy string
	var tables []warehouse.TableRef
	seenTables := map[warehouse.TableRef]bool{}
	var ranParts []string
	recovered := 0
	partial := false
	for _, stmt := range stmts {
		if !isEstimableStatement(stmt) {
			partial = true
			continue
		}
		subD, err := r.deps.DryRun(ctx, proj, location, stmt)
		if err != nil {
			partial = true
			continue
		}
		subEst, _ := estimateOf(subD)
		if subEst == nil {
			partial = true
			continue
		}
		recovered++
		totalBytes += subEst.Bytes
		ranParts = append(ranParts, stmt)
		if subEst.Accuracy == warehouse.AccuracyLowerBound || accuracy == "" {
			accuracy = subEst.Accuracy
		} else if subEst.Accuracy == warehouse.AccuracyUpperBound && accuracy != warehouse.AccuracyLowerBound {
			accuracy = warehouse.AccuracyUpperBound
		}
		for _, t := range subD.Tables {
			if !seenTables[t] {
				seenTables[t] = true
				tables = append(tables, t)
			}
		}
	}
	if recovered == 0 {
		return nil, nil, ""
	}
	if partial {
		accuracy = warehouse.AccuracyLowerBound
	}
	est := &Estimate{Bytes: totalBytes, Accuracy: accuracy}
	check := &warehouse.Check{Bytes: totalBytes, Accuracy: accuracy, Tables: tables}
	return est, check, strings.Join(ranParts, ";\n")
}

// splitSQLStatements splits sql on top-level semicolons directly over sql's
// byte offsets, skipping comments, string literals, backticked identifiers,
// parenthesized expressions, and procedural blocks (BEGIN ... END, IF ... END IF,
// LOOP ... END LOOP, WHILE ... END WHILE, FOR ... END FOR, REPEAT ... END REPEAT)
// so conditional branches are never split and double-counted.
func splitSQLStatements(sql string) []string {
	var out []string
	parenDepth := 0
	blockDepth := 0
	start := 0
	var prevWord string
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case strings.HasPrefix(sql[i:], "--") || c == '#':
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(sql)
			}
		case strings.HasPrefix(sql[i:], "/*"):
			if j := strings.Index(sql[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(sql)
			}
		case c == '`':
			if j := strings.IndexByte(sql[i+1:], '`'); j >= 0 {
				i += j + 2
			} else {
				i = len(sql)
			}
		case c == '\'' || c == '"':
			quote := sql[i : i+1]
			if strings.HasPrefix(sql[i:], strings.Repeat(quote, 3)) {
				quote = strings.Repeat(quote, 3)
			}
			j := i + len(quote)
			for j < len(sql) && !strings.HasPrefix(sql[j:], quote) {
				if sql[j] == '\\' {
					j++
				}
				j++
			}
			i = min(len(sql), j+len(quote))
		case c == '(':
			parenDepth++
			i++
		case c == ')':
			if parenDepth > 0 {
				parenDepth--
			}
			i++
		case c == ';':
			if parenDepth == 0 && blockDepth == 0 {
				if s := strings.TrimSpace(sql[start:i]); s != "" {
					out = append(out, s)
				}
				start = i + 1
				prevWord = ""
			}
			i++
		case identChar(c):
			j := i + 1
			for j < len(sql) && identChar(sql[j]) {
				j++
			}
			word := strings.ToLower(sql[i:j])
			if parenDepth == 0 {
				// Look ahead past spaces for '(' (function calls like IF(...), REPEAT(...))
				// or next identifier (e.g. BEGIN TRANSACTION).
				k := j
				for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t' || sql[k] == '\n' || sql[k] == '\r') {
					k++
				}
				followedByParen := k < len(sql) && sql[k] == '('
				nextWord := ""
				if k < len(sql) && identChar(sql[k]) {
					m := k + 1
					for m < len(sql) && identChar(sql[m]) {
						m++
					}
					nextWord = strings.ToLower(sql[k:m])
				}
				switch word {
				case "begin":
					if !followedByParen && nextWord != "transaction" {
						blockDepth++
					}
				case "case":
					if prevWord != "end" && !followedByParen {
						blockDepth++
					}
				case "if", "loop", "while", "repeat", "for":
					if !followedByParen && prevWord != "end" &&
						(prevWord == "" || prevWord == "then" || prevWord == "else" || prevWord == "do" || prevWord == "begin") {
						blockDepth++
					}
				case "end":
					if blockDepth > 0 {
						blockDepth--
					}
				}
			}
			prevWord = word
			i = j
		default:
			i++
		}
	}
	if s := strings.TrimSpace(sql[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

func isEstimableStatement(stmt string) bool {
	code := strings.ToLower(project.StripSQL(stmt, project.StripOptions{BlankSingleStrings: true, BlankDoubleStrings: true}))
	toks := scanSQLTokens(code)
	if len(toks) == 0 {
		return false
	}
	switch toks[0].text {
	case "select", "with", "create", "insert", "merge", "update", "delete":
		return true
	}
	return false
}

// denied reports a dry run BigQuery refused whatever the SQL: no usable
// credentials, or no permission.
func denied(err error) bool {
	var api *warehouse.APIError
	return errors.Is(err, warehouse.ErrAuth) || errors.As(err, &api) && api.Status == http.StatusForbidden
}

// estimate is the estimate of c, whose action in head is a, and the
// failure of its dry run, if it failed.
func (r *runner) estimate(ctx context.Context, c changes.Changed, a *project.Action, head *project.Graph) (Action, error) {
	out := Action{Name: string(c.ID), FileName: c.FileName, Type: c.Type, New: isNew(c) && c.RenamedFrom == "", RenamedFrom: string(c.RenamedFrom)}
	if a.ID != c.ID {
		out.DryRunAs = string(a.ID)
	}
	run, full := counted(a)
	q := run
	var d *warehouse.Check
	var ranSQL string
	var err error
	for {
		sql := fromDev(q, out.FromDev)
		out.Estimate, d, ranSQL, out.Reason, out.LeftOutDML, err = r.dryRun(ctx, head, a, sql)
		m, ok := missingIn(out.Reason)
		if err == nil || !ok {
			break
		}
		// An incremental table that isn't built yet is built whole.
		if q == run && full != nil && m.covers(a.Target) {
			q, full, out.FirstBuild = full, nil, true
			continue
		}
		if !r.readFromDev(ctx, m, sql.SQL, &out.FromDev) {
			out.Waits = r.waits(ctx, head, a, m, sql.SQL)
			break
		}
	}
	if d != nil {
		out.schema = d.Schema
	}
	if out.Estimate != nil {
		out.Unfiltered = r.ps.unfiltered(ctx, ranSQL, d.Tables)
	}
	if full != nil && err == nil {
		out.Full, _, _, _, _, _ = r.dryRun(ctx, head, a, fromDev(full, out.FromDev))
	}
	if out.FirstBuild && out.Estimate != nil {
		out.Incremental = r.later(ctx, head, a, run, out.FromDev)
	}
	return out, err
}

// later is what each run of a, a new incremental table, scans once its
// first has built it: its incremental run q, reading dev's copy of its own
// table, and of the other tables the branch adds, as reads does. It is nil
// when dev hasn't built them.
func (r *runner) later(ctx context.Context, head *project.Graph, a *project.Action, q *project.DryRunQuery, reads []DevRead) *Estimate {
	reads = slices.Clone(reads)
	for {
		sql := fromDev(q, reads)
		est, _, _, reason, _, err := r.dryRun(ctx, head, a, sql)
		if err == nil {
			return est
		}
		m, ok := missingIn(reason)
		if !ok || !r.readFromDev(ctx, m, sql.SQL, &reads) {
			return nil
		}
	}
}

func isNew(c changes.Changed) bool {
	for _, r := range c.Reasons {
		if r == changes.ReasonNew {
			return true
		}
	}
	return false
}

// Run estimates what the Changed Actions of res scan, dry running their
// SQL in head. progress is told how far it got, and is handed the report
// once the branch is estimated and only the merge-base is left. Once ctx is
// done nothing more starts and ctx's error is returned.
func Run(ctx context.Context, res *changes.Result, head Head, deps Deps, progress func(Progress)) (*Report, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if progress == nil {
		progress = func(Progress) {}
	}
	r := &runner{deps: deps, added: map[string]addedTable{}, built: map[string]*builtLookup{}}
	if deps.Partition != nil {
		r.ps = &partitions{lookup: deps.Partition, seen: map[warehouse.TableRef]*partitionLookup{}}
	}
	headID := func(id project.ActionID) project.ActionID {
		if head.ID == nil {
			return id
		}
		return head.ID(id)
	}

	type item struct {
		c changes.Changed
		a *project.Action
	}
	var items []item
	report := &Report{Comparison: impact.ComparisonOf(res), Compiled: head.Compiled, Actions: []Action{}}
	for _, c := range res.Changed {
		a := head.Graph.Actions[headID(c.ID)]
		switch {
		case a == nil && head.ID != nil:
			report.Actions = append(report.Actions, Action{Name: string(c.ID), FileName: c.FileName, Type: c.Type, New: isNew(c) && c.RenamedFrom == "", RenamedFrom: string(c.RenamedFrom),
				Reason: "nothing in the compile with " + head.Compiled + " matches it"})
		case a == nil:
		default:
			if isNew(c) {
				r.added[strings.ToLower(string(a.Target.ID()))] = addedTable{a.Target, c.ID, targetRefRE(string(a.Target.ID()))}
			}
			if run, _ := counted(a); run != nil {
				items = append(items, item{c, a})
			}
		}
	}

	acts := make([]*project.Action, len(items))
	for i, it := range items {
		acts[i] = it.a
	}
	report.DryRunIn = dryRunIn(head.Graph, acts)

	// The merge-base compiles while the branch is dry run.
	type compiled struct {
		g   *project.Graph
		err error
	}
	var base chan compiled
	if deps.Base != nil && len(items) > 0 {
		base = make(chan compiled, 1)
		go func() {
			g, err := deps.Base(ctx)
			base <- compiled{g, err}
		}()
	}

	estimated := make([]Action, len(items))
	failures := make([]error, len(items))
	indexes := make([]int, len(items))
	for i := range indexes {
		indexes[i] = i
	}
	var mu sync.Mutex
	done := 0
	if len(items) > 0 {
		progress(Progress{Phase: "Dry running", Total: len(items)})
	}
	inBatches(ctx, indexes, dryRunBatch, func(i int) {
		estimated[i], failures[i] = r.estimate(ctx, items[i].c, items[i].a, head.Graph)
		mu.Lock()
		done++
		progress(Progress{Phase: "Dry running", Done: done, Total: len(items)})
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	heads := make([]*project.Action, len(items))
	for i, it := range items {
		heads[i] = it.a
	}
	addedColumns(head.Graph, heads, estimated)

	// Refused every time, the check wasn't made: a report of nothing but
	// failures would pass any limit.
	if len(items) > 0 && !slices.ContainsFunc(failures, func(err error) bool { return !denied(err) }) {
		return nil, fmt.Errorf("BigQuery refused every dry run: %w", failures[0])
	}

	// compare are the actions with a merge-base to estimate.
	var compare []int
	for i, a := range estimated {
		if a.Estimate != nil && !a.New {
			compare = append(compare, i)
		}
	}
	finish := func(pending bool) *Report {
		out := *report
		out.Actions = append(append([]Action{}, report.Actions...), estimated...)
		sortActions(out.Actions)
		out.BasePending = pending
		out.CheckedAt = deps.Now()
		return &out
	}
	if base == nil || len(compare) == 0 {
		return finish(false), nil
	}

	progress(Progress{Phase: "Compiling the merge-base", Partial: finish(true)})
	var b compiled
	select {
	case b = <-base:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if b.err != nil {
		report.BaseError = b.err.Error()
		return finish(false), nil
	}
	done = 0
	progress(Progress{Phase: "Dry running the merge-base", Total: len(compare)})
	inBatches(ctx, compare, dryRunBatch, func(i int) {
		baseID := items[i].a.ID
		if items[i].c.RenamedFrom != "" {
			if id := headID(items[i].c.RenamedFrom); id != "" {
				baseID = id
			} else {
				baseID = items[i].c.RenamedFrom
			}
		}
		if a := b.g.Actions[baseID]; a != nil {
			if run, _ := counted(a); run != nil {
				estimated[i].Base, _, _, _, _, _ = r.dryRun(ctx, b.g, a, run)
			}
		}
		mu.Lock()
		done++
		progress(Progress{Phase: "Dry running the merge-base", Done: done, Total: len(compare)})
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return finish(false), nil
}

// dryRunIn is the one project, and location when there is one, where
// actions are dry run: "acme-prod" or "acme-prod (EU)", or "" when there
// are more.
func dryRunIn(g *project.Graph, actions []*project.Action) string {
	in := ""
	for _, a := range actions {
		proj, location := g.DryRunTarget(a)
		where := proj
		if location != "" {
			where += " (" + location + ")"
		}
		if in != "" && in != where {
			return ""
		}
		in = where
	}
	return in
}

// unrecognizedRE is BigQuery's message for a query that names a column
// none of the tables it reads has: "Unrecognized name: X", or "Name X not
// found inside Y" for a qualified one.
var unrecognizedRE = regexp.MustCompile("^dry run failed: (?:Unrecognized name: `?([A-Za-z_][A-Za-z0-9_]*)`?|Name `?([A-Za-z_][A-Za-z0-9_]*)`? not found inside)")

// addedColumns sets AddedColumn on the actions whose dry run failed on a
// column a changed action they read makes on this branch: its table in
// BigQuery has the column only once a run of the branch builds it.
// heads[i] is the action of estimated[i] in g, the compile dry run. A
// changed action makes the column when its dry run returns it, or, when it
// has no schema (its own dry run failed, or is a script), when its SQL
// names it. The search goes on up through the actions whose output isn't
// known, as a SELECT * passes a column on unnamed: unchanged ones, and
// changed ones without a schema that don't name it.
func addedColumns(g *project.Graph, heads []*project.Action, estimated []Action) {
	at := map[project.ActionID]int{}
	for i, a := range heads {
		at[a.ID] = i
	}
	// maker is the changed action above a that makes column, or -1.
	maker := func(a *project.Action, column string) int {
		seen := map[project.ActionID]bool{a.ID: true}
		queue := slices.Clone(a.DependencyTargets)
		for len(queue) > 0 {
			id := queue[0].ID()
			queue = queue[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			dep := g.Actions[id]
			if dep == nil {
				continue
			}
			if j, changed := at[id]; changed {
				if schema := estimated[j].schema; len(schema) > 0 {
					if slices.ContainsFunc(schema, func(f warehouse.Column) bool { return strings.EqualFold(f.Name, column) }) {
						return j
					}
					continue // its output is known, and hasn't got it
				}
				if run, _ := counted(dep); run != nil && impact.Mentions(run.SQL, column) {
					return j
				}
			}
			queue = append(queue, dep.DependencyTargets...)
		}
		return -1
	}
	for i := range estimated {
		m := unrecognizedRE.FindStringSubmatch(firstLine(estimated[i].Reason))
		if m == nil {
			continue
		}
		column := m[1] + m[2]
		if j := maker(heads[i], column); j >= 0 {
			estimated[i].AddedColumn = &AddedColumn{Table: estimated[j].Name, Column: column}
		}
	}
	for i := range estimated {
		estimated[i].schema = nil
	}
}
