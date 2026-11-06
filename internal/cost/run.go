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
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// dryRunBatch is how many dry runs start at once.
const dryRunBatch = 6

// Deps are what Run needs from the outside world.
type Deps struct {
	// DryRun plans sql without running it.
	DryRun func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error)
	// Partition is how a table is partitioned: nil (and no error) when it
	// isn't, or there is no such table. A nil Partition looks for no
	// unfiltered reads.
	Partition func(ctx context.Context, t bigquery.TableRef) (*bigquery.Partition, error)
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

// inBatches runs run for every item, size at a time. Once ctx is done no
// further batch starts.
func inBatches[T any](ctx context.Context, items []T, size int, run func(T)) {
	for i := 0; i < len(items); i += size {
		if ctx.Err() != nil {
			return
		}
		var wg sync.WaitGroup
		for _, it := range items[i:min(i+size, len(items))] {
			wg.Go(func() { run(it) })
		}
		wg.Wait()
	}
}

func errorText(err error) string {
	var api *bigquery.APIError
	if errors.As(err, &api) {
		return api.Message
	}
	return err.Error()
}

// estimateOf is a dry run's estimate, or why it has none: BigQuery reports
// 0 bytes when it cannot tell, and gives no figure for most scripts.
func estimateOf(d *bigquery.DryRun) (*Estimate, string) {
	switch {
	case d.StatementType == "SCRIPT" && d.Accuracy != bigquery.AccuracyPrecise && d.Accuracy != bigquery.AccuracyUpperBound:
		return nil, "BigQuery gives no estimate for a script"
	case d.Accuracy == bigquery.AccuracyUnknown && d.Bytes == 0:
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
	lookup func(ctx context.Context, t bigquery.TableRef) (*bigquery.Partition, error)

	mu   sync.Mutex
	seen map[bigquery.TableRef]*partitionLookup
}

type partitionLookup struct {
	once sync.Once
	p    *bigquery.Partition
}

func (ps *partitions) of(ctx context.Context, t bigquery.TableRef) *bigquery.Partition {
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

// unfiltered are the partitioned tables sql reads by name without naming
// their partition column. A table read through a view isn't named in sql,
// and isn't judged: the view may filter it.
func (ps *partitions) unfiltered(ctx context.Context, sql string, tables []bigquery.TableRef) []Unfiltered {
	if ps == nil {
		return nil
	}
	code := strings.ToLower(stripComments(sql))
	var out []Unfiltered
	for _, t := range tables {
		if !names(code, t.Table) {
			continue
		}
		p := ps.of(ctx, t)
		if p == nil {
			continue
		}
		column, filtered := p.Column, names(code, p.Column)
		if column == "" {
			column = "_PARTITIONTIME"
			filtered = names(code, "_PARTITIONTIME") || names(code, "_PARTITIONDATE")
		}
		if !filtered {
			out = append(out, Unfiltered{Table: t.Project + "." + t.Dataset + "." + t.Table, Column: column})
		}
	}
	return out
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

// stripComments blanks sql's comments (--, # and /* */), leaving its
// strings and quoted names alone.
func stripComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(sql) && sql[j] != c {
				if sql[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(sql))
			b.WriteString(sql[i:j])
			i = j
		case c == '#' || c == '-' && strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			if end := strings.Index(sql[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(sql)
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

type runner struct {
	deps Deps
	ps   *partitions
	// added are the tables the branch adds, by their lower-cased
	// project.dataset.name in the compile dry run.
	added map[string]addedTable
}

// addedTable is a table the branch adds: target in the compile dry run, id
// in the one the Changed Actions were worked out from.
type addedTable struct {
	target project.Target
	id     project.ActionID
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

// quoted is how a compile names a table in its SQL.
func quoted(id string) string { return "`" + id + "`" }

// readFromDev adds to reads dev's copy of the tables the branch adds that
// m is, or that sql reads in m, a dataset, when they are built there. It
// is false when none is, or each is in reads already.
func (r *runner) readFromDev(ctx context.Context, m missing, sql string, reads *[]DevRead) bool {
	if r.deps.InDev == nil {
		return false
	}
	var keys []string
	for k, t := range r.added {
		if m.covers(t.target) && (!m.dataset || strings.Contains(sql, quoted(string(t.target.ID())))) {
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

// fromDev is q reading the tables of reads from dev.
func fromDev(q *project.DryRunQuery, reads []DevRead) *project.DryRunQuery {
	if len(reads) == 0 {
		return q
	}
	out := *q
	for _, d := range reads {
		out.SQL = strings.ReplaceAll(out.SQL, quoted(d.Table), quoted(d.Dev))
	}
	return &out
}

// dryRun dry runs q as an action of g, returning its estimate, or why it
// has none, and the tables it reads. err is the dry run's own failure.
func (r *runner) dryRun(ctx context.Context, g *project.Graph, a *project.Action, q *project.DryRunQuery) (est *Estimate, tables []bigquery.TableRef, reason string, err error) {
	proj, location := g.DryRunTarget(a)
	if proj == "" {
		return nil, nil, "no project to dry run in: set defaultProject", nil
	}
	d, err := r.deps.DryRun(ctx, proj, location, q.SQL)
	if err != nil {
		return nil, nil, "dry run failed: " + errorText(err), err
	}
	est, reason = estimateOf(d)
	return est, d.Tables, reason, nil
}

// denied reports a dry run BigQuery refused whatever the SQL: no usable
// credentials, or no permission.
func denied(err error) bool {
	var api *bigquery.APIError
	return errors.Is(err, bigquery.ErrAuth) || errors.As(err, &api) && api.Status == http.StatusForbidden
}

// estimate is the estimate of c, whose action in head is a, and the
// failure of its dry run, if it failed.
func (r *runner) estimate(ctx context.Context, c changes.Changed, a *project.Action, head *project.Graph) (Action, error) {
	out := Action{Name: string(c.ID), FileName: c.FileName, Type: c.Type, New: isNew(c)}
	if a.ID != c.ID {
		out.DryRunAs = string(a.ID)
	}
	run, full := counted(a)
	q := run
	var tables []bigquery.TableRef
	var err error
	for {
		sql := fromDev(q, out.FromDev)
		out.Estimate, tables, out.Reason, err = r.dryRun(ctx, head, a, sql)
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
			break
		}
	}
	if out.Estimate != nil {
		out.Unfiltered = r.ps.unfiltered(ctx, fromDev(q, out.FromDev).SQL, tables)
	}
	if full != nil && err == nil {
		out.Full, _, _, _ = r.dryRun(ctx, head, a, fromDev(full, out.FromDev))
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
		est, _, reason, err := r.dryRun(ctx, head, a, sql)
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
	r := &runner{deps: deps, added: map[string]addedTable{}}
	if deps.Partition != nil {
		r.ps = &partitions{lookup: deps.Partition, seen: map[bigquery.TableRef]*partitionLookup{}}
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
			report.Actions = append(report.Actions, Action{Name: string(c.ID), FileName: c.FileName, Type: c.Type, New: isNew(c),
				Reason: "nothing in the compile with " + head.Compiled + " matches it"})
		case a == nil:
		default:
			if isNew(c) {
				r.added[strings.ToLower(string(a.Target.ID()))] = addedTable{a.Target, c.ID}
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
		if a := b.g.Actions[items[i].a.ID]; a != nil {
			if run, _ := counted(a); run != nil {
				estimated[i].Base, _, _, _ = r.dryRun(ctx, b.g, a, run)
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
