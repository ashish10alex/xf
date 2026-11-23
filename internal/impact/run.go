package impact

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	// dryRunBatch is how many dry runs start at once.
	dryRunBatch = 6
	// readerConcurrency is how many reader lookups run at once; the
	// source throttles its own calls further.
	readerConcurrency = 4
)

// Readers finds who reads a table's columns; *lineage.Source is one.
type Readers interface {
	// ColumnLinks is the columns that read table.column directly.
	ColumnLinks(ctx context.Context, table, column string) ([]lineage.ColumnLink, error)
	// TableOnlyReaders is the readers of table, other than those in
	// linked, that may read a column without a column link saying so.
	TableOnlyReaders(ctx context.Context, table string, linked map[string]bool) ([]lineage.ColumnLink, error)
	// TableReaders is every reader of table, at table level.
	TableReaders(ctx context.Context, table string) ([]lineage.ColumnLink, error)
}

// Candidate is a changed or deleted table, once its prod schema was read
// and it was dry run.
type Candidate struct {
	Table string
	// Target is the prod table Table is compared with and whose readers
	// are looked up; "" means Table itself.
	Target   string
	FileName string
	Type     string
	Deleted  bool
	// New: the base doesn't have the action.
	New bool
	// NoTable is set on a new action with no table to compare with: there
	// is nothing to lose, so it wasn't dry run.
	NoTable bool
	// RenamedFrom is the deleted action a new one was renamed from (see
	// changes.Changed.RenamedFrom); RenamedTo, the new action a deleted one
	// was renamed to.
	RenamedFrom string
	RenamedTo   string
	// FromOld is set on a renamed action whose own table isn't in BigQuery
	// yet: Prod, and Target, are those of the table it was renamed from.
	FromOld bool
	Prod    []warehouse.Column
	// Dev is the dry run's schema; unused for a deleted table.
	Dev []warehouse.Column
	// Base is the schema of a dry run of the action as it is at the
	// merge-base, when Dev drops or retypes a column of Prod and it could
	// be dry run: a change it makes too is the base branch's.
	Base []warehouse.Column
	// BaseOwnRun, BaseStrictSchema and BaseUniqueKey are OwnRun, StrictSchema
	// and UniqueKey of the action at the merge-base, read with Base.
	BaseOwnRun       bool
	BaseStrictSchema bool
	BaseUniqueKey    []string
	// UncheckedReason says why the table couldn't be compared, e.g. its
	// dry run failed.
	UncheckedReason string
	// OwnRun: the table is an incremental table whose run fails when any
	// existing column is dropped (Dataform default/IGNORE/FAIL/EXTEND, or
	// dbt default/ignore/fail); StrictSchema is set when onSchemaChange=FAIL
	// or on_schema_change=fail (retypes also break the run); UniqueKey is
	// its uniqueKey / unique_key when its incremental strategy uses one.
	OwnRun       bool
	StrictSchema bool
	UniqueKey    []string
	// Dbt is set when the candidate is a dbt model.
	Dbt bool
	// Warehouse is the warehouse whose type system Prod and Dev use
	// (project.BigQuery, project.Databricks, project.Snowflake; empty
	// defaults to BigQuery).
	Warehouse string
}

// target is the table c is compared with.
func (c Candidate) target() string { return cmp.Or(c.Target, c.Table) }

// Result is the tables of a report, sorted.
type Result struct {
	AtRisk    []Table
	Safe      []Table
	New       []Table
	Unchecked []Unchecked
	Warnings  []string
}

// inBatches runs run on every item with up to size workers running at once,
// starting the next item as soon as a worker finishes instead of waiting for a
// lockstep batch. Stops starting new items once ctx is done.
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

// preloader is an optional interface implemented by warehouse lineage readers
// (e.g. Snowflake ACCOUNT_USAGE) that can fetch lineage for many tables in a
// small fixed number of batched queries instead of per-table lookups.
type preloader interface {
	Preload(ctx context.Context, tables []string)
}

// warner is an optional interface implemented by lineage readers that surface
// report-level warnings (e.g. BigQuery query-history permission or scan cap)
// without turning individual columns into SeverityUnknown.
type warner interface {
	Warnings() []string
}

func hasNonConsumerLinks(links []lineage.ColumnLink) bool {
	for _, l := range links {
		if l.Consumer == nil {
			return true
		}
	}
	return false
}

// columnReaders is the direct readers of a column: its column links, then
// readers Dataplex knows only at table level. For a nested STRUCT field
// ("user.address.zip"), if table lineage has no nested-field links it falls
// back to the top-level STRUCT column ("user") tagged lineage.Other (keeping
// external SELECT* consumers at their original DependencyType).
func columnReaders(ctx context.Context, src Readers, table, column string) ([]lineage.ColumnLink, error) {
	var errs []error
	addErr := func(err error) {
		if err == nil {
			return
		}
		for _, prev := range errs {
			if prev.Error() == err.Error() {
				return
			}
		}
		errs = append(errs, err)
	}
	links, err := src.ColumnLinks(ctx, table, column)
	addErr(err)
	if !hasNonConsumerLinks(links) {
		if top, _, nested := strings.Cut(column, "."); nested {
			parentLinks, pErr := src.ColumnLinks(ctx, table, top)
			addErr(pErr)
			for _, l := range parentLinks {
				if l.Consumer == nil {
					l.DependencyType = lineage.Other
				}
				links = append(links, l)
			}
		}
	}
	linked := map[string]bool{}
	for _, l := range links {
		if l.Consumer == nil {
			linked[l.Table] = true
		}
	}
	only, onlyErr := src.TableOnlyReaders(ctx, table, linked)
	addErr(onlyErr)
	return append(links, only...), errors.Join(errs...)
}

// Build compares each candidate with prod and looks up who reads what it
// drops, retypes or deletes. progress is told after each lookup. Once ctx
// is done no more lookups start, and Build returns ctx's error.
func Build(ctx context.Context, candidates []Candidate, src Readers, rctx ReaderContext, progress func(done, total int)) (*Result, error) {
	var unchecked []Unchecked
	var safe, atRisk, added []*Table
	var lookups []func()
	var preloadTables []string
	preloadSeen := map[string]bool{}
	addPreload := func(t string) {
		if t != "" && !preloadSeen[t] {
			preloadSeen[t] = true
			preloadTables = append(preloadTables, t)
		}
	}
	for _, c := range candidates {
		if c.UncheckedReason != "" {
			unchecked = append(unchecked, Unchecked{Table: c.Table, FileName: c.FileName, Reason: c.UncheckedReason, RenamedFrom: c.RenamedFrom, RenamedTo: c.RenamedTo})
			continue
		}
		if c.NoTable {
			added = append(added, &Table{Table: c.Table, FileName: c.FileName, Type: c.Type, Columns: []Column{}, New: true, RenamedFrom: c.RenamedFrom})
			continue
		}
		if c.Deleted {
			addPreload(c.target())
			entry := &Table{Table: c.Table, FileName: c.FileName, Type: c.Type, Deleted: true, Columns: []Column{}, RenamedTo: c.RenamedTo}
			atRisk = append(atRisk, entry)
			// As the compile names it, and as a reader that names its prod
			// table outright does
			name, prod := TablePath(c.Table), TablePath(c.target())
			lookups = append(lookups, func() {
				links, err := src.TableReaders(ctx, c.target())
				if err != nil {
					entry.ReadersError = err.Error()
				}
				links = append(links, rctx.projectReaders(entry.Table, "")...)
				if len(links) > 0 || err == nil {
					entry.Readers = ImpactReaders(links, entry.Table, name, rctx, prod)
				}
			})
			continue
		}
		prodNames := map[string]bool{}
		for _, f := range c.Prod {
			prodNames[strings.ToLower(f.Name)] = true
		}
		var only []string
		for _, f := range c.Dev {
			if !prodNames[strings.ToLower(f.Name)] {
				only = append(only, f.Name)
			}
		}
		entry := &Table{Table: c.Table, FileName: c.FileName, Type: c.Type, Columns: []Column{}, Added: only, New: c.New, Exists: c.New && !c.FromOld,
			RenamedFrom: c.RenamedFrom, FromOld: c.FromOld, Dbt: c.Dbt}
		var onBase []ColumnImpact
		if len(c.Base) > 0 {
			onBase = DiffSchemasFor(c.Warehouse, c.Base, c.Prod)
		}
		var key, baseKey []string
		if !c.FromOld {
			key, baseKey = c.UniqueKey, c.BaseUniqueKey
		}
		ownRun := c.OwnRun && !c.FromOld
		strict := c.StrictSchema && !c.FromOld
		for _, d := range DiffSchemasFor(c.Warehouse, c.Dev, c.Prod) {
			d.Change.OnBase = slices.Contains(onBase, d)
			// Inserting a widened type (e.g. INT -> BIGINT) into an existing
			// incremental table can fail or overflow, so don't downgrade it to low.
			if ownRun && d.Change.Widening {
				d.Change.Widening = false
			}
			// A renamed table compared with its old one is built anew.
			d.Change.BreaksRun = breaksRun(d.Change, d.Column, ownRun, strict, key)
			if d.Change.OnBase {
				d.Change.BaseBreaksRun = breaksRun(d.Change, d.Column, c.BaseOwnRun && !c.FromOld, c.BaseStrictSchema && !c.FromOld, baseKey)
			}
			entry.Columns = append(entry.Columns, Column{Column: d.Column, Change: d.Change, Readers: []Reader{}})
		}
		switch {
		case len(entry.Columns) > 0:
		case c.New:
			added = append(added, entry)
			continue
		default:
			safe = append(safe, entry)
			continue
		}
		atRisk = append(atRisk, entry)
		addPreload(c.target())
		for i := range entry.Columns {
			col := &entry.Columns[i]
			lookups = append(lookups, func() {
				links, err := columnReaders(ctx, src, c.target(), col.Column)
				if err != nil {
					col.ReadersError = err.Error()
				}
				warehouseTables := map[string]bool{}
				for _, l := range links {
					t := l.Table
					if rctx.ToProd != nil {
						t = rctx.ToProd(t)
					}
					warehouseTables[t] = true
				}
				for _, pl := range rctx.projectReaders(entry.Table, col.Column) {
					t := pl.Table
					if rctx.ToProd != nil {
						t = rctx.ToProd(t)
					}
					if !warehouseTables[t] {
						links = append(links, pl)
					}
				}
				if len(links) > 0 || err == nil {
					var also []string
					if dot := strings.LastIndexByte(col.Column, '.'); dot >= 0 {
						also = append(also, col.Column[dot+1:])
					}
					col.Readers = ImpactReaders(links, entry.Table, col.Column, rctx, also...)
				}
			})
		}
	}

	var mu sync.Mutex
	done := 0
	progress(0, len(lookups))
	if p, ok := src.(preloader); ok && len(preloadTables) > 0 {
		p.Preload(ctx, preloadTables)
	}
	inBatches(ctx, lookups, readerConcurrency, func(lookup func()) {
		lookup()
		mu.Lock()
		done++
		progress(done, len(lookups))
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{AtRisk: []Table{}, Safe: []Table{}, New: []Table{}, Unchecked: []Unchecked{}}
	for _, t := range atRisk {
		t.Columns = SortColumns(t.Columns)
		res.AtRisk = append(res.AtRisk, *t)
	}
	res.AtRisk = SortTables(res.AtRisk)
	for _, t := range safe {
		res.Safe = append(res.Safe, *t)
	}
	slices.SortFunc(res.Safe, func(a, b Table) int { return localeCompare(a.Table, b.Table) })
	for _, t := range added {
		res.New = append(res.New, *t)
	}
	slices.SortFunc(res.New, func(a, b Table) int { return localeCompare(a.Table, b.Table) })
	res.Unchecked = append(res.Unchecked, unchecked...)
	slices.SortFunc(res.Unchecked, func(a, b Unchecked) int { return localeCompare(a.Table, b.Table) })
	if w, ok := src.(warner); ok {
		res.Warnings = w.Warnings()
	}
	return res, nil
}

// Deps are what Run needs from the outside world.
type Deps struct {
	// Schema is a table's columns in BigQuery, or nil (and no error) when
	// there is no such table.
	Schema func(ctx context.Context, table string) ([]warehouse.Column, error)
	// DryRun plans sql without running it, returning its schema.
	DryRun func(ctx context.Context, project, location, sql string) (*warehouse.Check, error)
	// Readers finds who reads what the branch changes.
	Readers Readers
	// ToProd is the table of the action a reader is a run of, when it is
	// a dev or prod run of an action of this project, or of one the branch
	// deletes (see DevToProd); nil when there are no runs to fold.
	ToProd func(table string) string
	// ProdTarget is the prod table of an action, or of one the branch
	// deletes: what it is compared with and whose readers are looked up.
	// It is "" when the action has none; nil compares each action with
	// its own table, as xf compiles it.
	ProdTarget func(table string) string
	// Base is the project compiled at the merge-base; nil, or an error,
	// leaves every change the branch's own. It is asked for only when a
	// changed table drops or retypes a column.
	Base func(ctx context.Context) (*project.Graph, error)
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// OnDemand is set for a warehouse that bills while it runs, a
	// Databricks SQL warehouse or Snowflake virtual warehouse (ADR 0042,
	// ADR 0044): DryRun plans one query, not a script, so each changed
	// model is checked by its query alone, without its hooks, and the
	// readers of a dropped column aren't dry run, so only the changed
	// models wake the warehouse.
	OnDemand bool
	// Warehouse names the warehouse type system to diff schemas under
	// (project.BigQuery, project.Databricks, project.Snowflake); empty
	// falls back to g.Warehouse, then BigQuery.
	Warehouse string
}

// checkSQL is what a's dry run plans: its first dry run query, or with
// deps.OnDemand its query alone.
func checkSQL(a *project.Action, queries []project.DryRunQuery, deps Deps) string {
	if deps.OnDemand && a.Kind != project.KindOperation && strings.TrimSpace(a.Query) != "" {
		return a.Query
	}
	return queries[0].SQL
}

// producesTable says whether an action builds a table whose columns can be
// compared: tables, views, incremental tables, dbt's materialized views and
// operations with hasOutput. Assertions generated from a table's config don't change its
// columns.
func producesTable(a *project.Action) bool {
	switch a.Kind {
	case project.KindTable, project.KindView, project.KindIncremental, project.KindMaterializedView:
		return true
	case project.KindOperation:
		return a.HasOutput
	}
	return false
}

// tableTypes are the types of deleted actions that had a table to lose:
// the compile output's, and dbt's action kinds.
var tableTypes = map[string]bool{"table": true, "view": true, "incremental": true, project.KindMaterializedView.String(): true}

// actionSQL is everything the action runs, incremental runs included, for
// checking whether it still names a column.
func actionSQL(a *project.Action) string {
	parts := slices.Concat(a.PreOps, []string{a.Query}, a.PostOps,
		a.IncrementalPreOps, []string{a.IncrementalQuery}, a.IncrementalPostOps, a.Queries)
	if a.GraphBody != "" {
		parts = append(parts, a.GraphBody) // left out when empty, so Signature stays as it was
	}
	return strings.Join(parts, "\n")
}

// Signature changes when the branch's Changed Actions, or their compiled
// SQL, do, so a report can tell it is out of date.
func Signature(res *changes.Result, g *project.Graph) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n", res.Head, res.MergeBase, res.HeadSHA)
	for _, c := range res.Changed {
		sql := ""
		if a := g.Actions[c.ID]; a != nil {
			sql = actionSQL(a)
		}
		_, _ = fmt.Fprintf(h, "%s\n%s\n%v\n", c.ID, sql, c.Reasons)
	}
	for _, d := range res.Deleted {
		_, _ = fmt.Fprintf(h, "-%s\n", d.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComparisonOf is what res compared.
func ComparisonOf(res *changes.Result) *Comparison {
	label := "working tree"
	if res.Head == changes.HeadPushed {
		label = "pushed commit " + res.HeadSHA[:min(len(res.HeadSHA), 7)]
	}
	return &Comparison{HeadRef: res.HeadRef, BaseRef: res.BaseRef, MergeBase: res.MergeBase, HeadLabel: label}
}

func errorText(err error) string {
	var api *warehouse.APIError
	if errors.As(err, &api) {
		return api.Message
	}
	return err.Error()
}

// Run works out the column impact of res, a diff of g against the
// merge-base. progress is told how far it got. Once ctx is done nothing
// more starts and ctx's error is returned.
func Run(ctx context.Context, res *changes.Result, g *project.Graph, deps Deps, progress func(Progress)) (*Report, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if base := deps.Base; base != nil {
		// Asked for by every table at risk, at once: compiled once.
		once := sync.OnceValues(func() (*project.Graph, error) { return base(ctx) })
		deps.Base = func(context.Context) (*project.Graph, error) { return once() }
	}
	var changed []changes.Changed
	branchChanged := map[project.ActionID]bool{}
	for _, c := range res.Changed {
		if a := g.Actions[c.ID]; a != nil && producesTable(a) {
			changed = append(changed, c)
			branchChanged[c.ID] = true
		}
	}
	var deleted []changes.Deleted
	for _, d := range res.Deleted {
		if tableTypes[d.Type] {
			deleted = append(deleted, d)
		}
	}

	candidates := make([]Candidate, len(changed))
	var mu sync.Mutex
	done := 0
	phase := "Dry running"
	if deps.OnDemand {
		phase = "Checking on the SQL warehouse (it starts if stopped)"
	}
	progress(Progress{Phase: phase, Total: len(changed)})
	indexes := make([]int, len(changed))
	for i := range indexes {
		indexes[i] = i
	}
	inBatches(ctx, indexes, dryRunBatch, func(i int) {
		candidates[i] = check(ctx, changed[i], g.Actions[changed[i].ID], g, branchChanged, deps)
		mu.Lock()
		done++
		progress(Progress{Phase: phase, Done: done, Total: len(changed)})
		mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(deleted) > 0 {
		deletedCands := make([]Candidate, len(deleted))
		delIdx := make([]int, len(deleted))
		for i := range delIdx {
			delIdx[i] = i
		}
		inBatches(ctx, delIdx, dryRunBatch, func(i int) {
			d := deleted[i]
			cand := Candidate{Table: string(d.ID), FileName: d.FileName, Type: d.Type, RenamedTo: string(d.RenamedTo)}
			gone := "deleted"
			if d.RenamedTo != "" {
				gone = "renamed to " + ShortTable(string(d.RenamedTo))
			}
			if !deps.prodTarget(&cand) {
				cand.UncheckedReason = gone + ", and " + cand.UncheckedReason
				deletedCands[i] = cand
				return
			}
			prod, err := deps.Schema(ctx, cand.target())
			switch {
			case err != nil:
				cand.UncheckedReason = gone + ", and " + cand.target() + " could not be read: " + errorText(err)
			case len(prod) == 0:
				cand.UncheckedReason = gone + ", and no table in " + tableProject(cand.target())
			default:
				cand.Deleted = true
			}
			deletedCands[i] = cand
		})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidates = append(candidates, deletedCands...)
	}

	changedTables := map[string]bool{}
	for _, c := range changed {
		changedTables[string(c.ID)] = true
	}
	// Every deleted action, assertions and operations too: readers may be
	// any of them.
	deletedTables := map[string]bool{}
	for _, d := range res.Deleted {
		deletedTables[string(d.ID)] = true
	}
	renamed := map[string]string{}
	for _, d := range res.Deleted {
		if d.RenamedTo != "" && changedTables[string(d.RenamedTo)] {
			renamed[string(d.ID)] = string(d.RenamedTo)
		}
	}
	rctx := ReaderContext{
		Renamed: renamed,
		ProjectReaders: func(table, column string) []lineage.ColumnLink {
			links := GraphReaders(g, table, column)
			links = append(links, StaticDAGReaders(g, table, column)...)
			return links
		},
		ResolveFile: func(table string) string {
			if a := g.Actions[project.ActionID(table)]; a != nil {
				return a.FileName
			}
			return ""
		},
		Changed: changedTables,
		Deleted: deletedTables,
		ToProd:  deps.ToProd,
		IsAssertion: func(table string) bool {
			a := g.Actions[project.ActionID(table)]
			return a != nil && a.Kind == project.KindAssertion
		},
		SQLOf: func(table string) (string, bool) {
			a := g.Actions[project.ActionID(table)]
			if !changedTables[table] || a == nil {
				return "", false
			}
			return actionSQL(a), true
		},
	}
	result, err := Build(ctx, candidates, deps.Readers, rctx, func(done, total int) {
		progress(Progress{Phase: "Looking up readers", Done: done, Total: total})
	})
	if err != nil {
		return nil, err
	}
	for i := range result.New {
		result.New[i].ReadBy = readBy(g, result.New[i].Table)
	}
	markGraphs(result.AtRisk, g)
	verifyReaders(ctx, result.AtRisk, g, branchChanged, deps, func(done, total int) {
		progress(Progress{Phase: "Dry running readers", Done: done, Total: total})
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.AtRisk = SortTables(result.AtRisk)
	// A renamed action and the one it was renamed from are one change.
	pairs := 0
	for _, d := range deleted {
		if d.RenamedTo != "" && changedTables[string(d.RenamedTo)] {
			pairs++
		}
	}
	report := &Report{
		Comparison:   ComparisonOf(res),
		ChangedCount: len(changed) + len(deleted) - pairs,
		AtRisk:       result.AtRisk,
		Safe:         result.Safe,
		New:          result.New,
		Unchecked:    result.Unchecked,
		Warnings:     result.Warnings,
		Against:      projectsOf(candidates, deps.ProdTarget != nil),
		DryRunIn:     dryRunIn(g, changed),
		CheckedAt:    deps.Now(),
		Signature:    Signature(res, g),
	}
	if deps.ProdTarget != nil {
		toProdTables(report, deps.ProdTarget)
	}
	return report, nil
}

// markGraphs sets Graph on the readers that are property graphs of g.
func markGraphs(tables []Table, g *project.Graph) {
	mark := func(rs []Reader) {
		for i := range rs {
			if a := g.Actions[project.ActionID(rs[i].Table)]; a != nil && a.Kind == project.KindPropertyGraph {
				rs[i].Graph = true
			}
		}
	}
	for i := range tables {
		mark(tables[i].Readers)
		for j := range tables[i].Columns {
			mark(tables[i].Columns[j].Readers)
		}
	}
}

// readBy is the actions of g that read table, assertions aside.
func readBy(g *project.Graph, table string) []string {
	var out []string
	for _, a := range g.Dependents(project.ActionID(table)) {
		if a != nil && a.Kind != project.KindAssertion {
			out = append(out, string(a.ID))
		}
	}
	return out
}

// prodTarget sets c's Target, or says why it has none.
func (deps Deps) prodTarget(c *Candidate) bool {
	if deps.ProdTarget == nil {
		return true
	}
	c.Target = deps.ProdTarget(c.Table)
	if c.Target == "" {
		c.UncheckedReason = "its prod table is unknown: nothing in the compile with prodCompilerOptions matches it"
		return false
	}
	return true
}

// dryRunIn is the one project the changed actions dry run in, "" for none
// or more than one.
func dryRunIn(g *project.Graph, changed []changes.Changed) string {
	in := ""
	for _, c := range changed {
		proj, _ := g.DryRunTarget(g.Actions[c.ID])
		if proj == "" || (in != "" && in != proj) {
			return ""
		}
		in = proj
	}
	return in
}

// projectsOf is the projects of the tables the candidates were compared
// with, sorted. With prod targets, a candidate without one wasn't.
func projectsOf(candidates []Candidate, prodTargets bool) []string {
	var projects []string
	for _, c := range candidates {
		if prodTargets && c.Target == "" {
			continue
		}
		if p := tableProject(c.target()); p != "" && !slices.Contains(projects, p) {
			projects = append(projects, p)
		}
	}
	slices.Sort(projects)
	return projects
}

// tableProject is the project of a project.dataset.table.
func tableProject(table string) string {
	p, _, _ := strings.Cut(table, ".")
	return p
}

// toProdTables names the report's tables, and the readers that are
// actions of the project, by their prod tables, which is what was
// compared and what the readers read.
func toProdTables(r *Report, prodTarget func(string) string) {
	rename := func(table *string) {
		if p := prodTarget(*table); p != "" {
			*table = p
		}
	}
	readers := func(rs []Reader) {
		for i := range rs {
			rename(&rs[i].Table)
			if rs[i].RenamedFrom != "" {
				rename(&rs[i].RenamedFrom)
			}
		}
	}
	for _, ts := range [][]Table{r.AtRisk, r.Safe, r.New} {
		for i := range ts {
			rename(&ts[i].Table)
			if ts[i].RenamedFrom != "" {
				rename(&ts[i].RenamedFrom)
			}
			if ts[i].RenamedTo != "" {
				rename(&ts[i].RenamedTo)
			}
			readers(ts[i].Readers)
			for j := range ts[i].ReadBy {
				rename(&ts[i].ReadBy[j])
			}
			for j := range ts[i].Columns {
				readers(ts[i].Columns[j].Readers)
			}
		}
	}
	for i := range r.Unchecked {
		rename(&r.Unchecked[i].Table)
		if r.Unchecked[i].RenamedFrom != "" {
			rename(&r.Unchecked[i].RenamedFrom)
		}
		if r.Unchecked[i].RenamedTo != "" {
			rename(&r.Unchecked[i].RenamedTo)
		}
	}
}

// check reads a changed table's prod schema and dry runs it. A new action
// with no table has nothing to compare, and isn't dry run.
func check(ctx context.Context, c changes.Changed, a *project.Action, g *project.Graph, branchChanged map[project.ActionID]bool, deps Deps) Candidate {
	wh := cmp.Or(deps.Warehouse, g.Warehouse, project.BigQuery)
	cand := Candidate{Table: string(c.ID), FileName: c.FileName, Type: c.Type, New: c.IsNew(), RenamedFrom: string(c.RenamedFrom), Dbt: a != nil && a.DbtName != nil, Warehouse: wh}
	cand.OwnRun, cand.StrictSchema, cand.UniqueKey = incrementalRun(a)
	if !deps.prodTarget(&cand) {
		return cand
	}
	prod, err := deps.Schema(ctx, cand.target())
	if err != nil {
		cand.UncheckedReason = cand.target() + " could not be read: " + errorText(err)
		return cand
	}
	if len(prod) == 0 && cand.New && cand.RenamedFrom != "" {
		// Its table is yet to be built: what it replaces is the table of
		// the action it was renamed from.
		old := Candidate{Table: cand.RenamedFrom}
		if deps.prodTarget(&old) {
			if prod, err = deps.Schema(ctx, old.target()); err != nil {
				cand.UncheckedReason = "renamed from " + ShortTable(cand.RenamedFrom) + ", and " + old.target() + " could not be read: " + errorText(err)
				return cand
			}
			if len(prod) > 0 {
				cand.Target, cand.FromOld = old.target(), true
			}
		}
	}
	if len(prod) == 0 {
		if cand.New {
			cand.NoTable = true
			return cand
		}
		cand.UncheckedReason = "no table in " + tableProject(cand.target()) + " yet"
		return cand
	}
	if a.Kind == project.KindOperation {
		cand.UncheckedReason = "operation: a dry run of a script has no schema"
		return cand
	}
	queries := a.DryRunQueries() // an incremental table's first is its full rebuild, whose schema the table gets
	proj, location := g.DryRunTarget(a)
	switch {
	case len(queries) == 0:
		cand.UncheckedReason = "nothing to dry run"
		return cand
	case proj == "":
		cand.UncheckedReason = "no project to dry run in: set defaultProject"
		return cand
	}
	dr, err := dryRunCheck(ctx, proj, location, a, queries, g, branchChanged, deps)
	if err != nil {
		cand.UncheckedReason = "dry run failed: " + errorText(err)
		return cand
	}
	var dev []warehouse.Column
	for _, f := range dr.Schema {
		if f.Name != "" {
			dev = append(dev, f)
		}
	}
	if len(dev) == 0 {
		cand.UncheckedReason = "the dry run returned no schema"
		return cand
	}
	cand.Prod, cand.Dev = prod, dev
	switch {
	case len(DiffSchemasFor(wh, dev, prod)) == 0:
	case cand.FromOld:
		cand.Base, cand.BaseOwnRun, cand.BaseStrictSchema, cand.BaseUniqueKey = baseSchema(ctx, c.RenamedFrom, deps)
	case !cand.New:
		cand.Base, cand.BaseOwnRun, cand.BaseStrictSchema, cand.BaseUniqueKey = baseSchema(ctx, c.ID, deps)
	}
	return cand
}

// incrementalRun says whether a's incremental run fails on a dropped column
// (ownRun), fails on any schema change including retypes (strict), and
// returns its merge uniqueKey / unique_key.
//
//   - Dataform: default / IGNORE / EXTEND / FAIL all query the existing
//     table's columns on incremental runs, so dropping a column breaks the
//     run; only SYNCHRONIZE alters the target table to drop removed columns.
//     FAIL also fails on retypes.
//   - dbt: default / ignore / fail use existing_relation's columns or abort,
//     so dropping a column breaks the run; append_new_columns and
//     sync_all_columns do not. fail also fails on retypes. unique_key is
//     ignored for append and insert_overwrite strategies.
func incrementalRun(a *project.Action) (ownRun, strict bool, uniqueKey []string) {
	if a == nil || a.Kind != project.KindIncremental {
		return false, false, nil
	}
	if a.DbtName != nil {
		osc := strings.ToLower(strings.TrimSpace(a.OnSchemaChange))
		ownRun = osc != "append_new_columns" && osc != "sync_all_columns"
		strict = osc == "fail"
		strat := strings.ToLower(strings.TrimSpace(a.IncrementalStrategy))
		if strat != "append" && strat != "insert_overwrite" {
			uniqueKey = a.UniqueKey
		}
		return ownRun, strict, uniqueKey
	}
	osc := strings.ToUpper(strings.TrimSpace(a.OnSchemaChange))
	ownRun = osc != "SYNCHRONIZE"
	strict = osc == "FAIL"
	return ownRun, strict, a.UniqueKey
}

// baseSchema is the schema of a dry run of action id as it is at the
// merge-base, with its incrementalRun; nil when there is no telling: no base, an
// action it doesn't have or can't dry run, or a dry run that fails.
func baseSchema(ctx context.Context, id project.ActionID, deps Deps) ([]warehouse.Column, bool, bool, []string) {
	if deps.Base == nil {
		return nil, false, false, nil
	}
	g, err := deps.Base(ctx)
	if err != nil || g == nil {
		return nil, false, false, nil
	}
	a := g.Actions[id]
	if a == nil || a.Kind == project.KindOperation {
		return nil, false, false, nil
	}
	queries := a.DryRunQueries()
	proj, location := g.DryRunTarget(a)
	if len(queries) == 0 || proj == "" {
		return nil, false, false, nil
	}
	dr, err := dryRunCheck(ctx, proj, location, a, queries, nil, nil, deps)
	if err != nil {
		return nil, false, false, nil
	}
	var base []warehouse.Column
	for _, f := range dr.Schema {
		if f.Name != "" {
			base = append(base, f)
		}
	}
	own, strict, key := incrementalRun(a)
	return base, own, strict, key
}

// dryRunCheck plans a's schema query; on BigQuery, when BigQuery refuses a
// DML statement in pre_operations because the caller has read-only access,
// it retries without those DML statements (as cost.Run does). When a dry run
// fails because an upstream table or column added/changed on this branch does
// not exist in prod yet, it retries with changed in-branch upstream tables
// substituted as subqueries.
func dryRunCheck(ctx context.Context, proj, location string, a *project.Action, queries []project.DryRunQuery, g *project.Graph, branchChanged map[project.ActionID]bool, deps Deps) (*warehouse.Check, error) {
	baseSQL := checkSQL(a, queries, deps)
	dr, err := deps.DryRun(ctx, proj, location, baseSQL)
	if err != nil && !deps.OnDemand {
		if _, _, denied := project.WriteDenied(errorText(err)); denied {
			if lean, ok := queries[0].WithoutDML(); ok {
				if leanDR, leanErr := deps.DryRun(ctx, proj, location, lean.SQL); leanErr == nil {
					return leanDR, nil
				}
			}
		}
		if len(branchChanged) > 1 && isMissingTableOrColumn(errorText(err)) {
			if swapped, ok := swapBranchTables(baseSQL, a.ID, g, branchChanged, 3); ok {
				if subDR, subErr := deps.DryRun(ctx, proj, location, swapped); subErr == nil {
					return subDR, nil
				}
			}
		}
	}
	return dr, err
}

func isMissingTableOrColumn(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "not found: table") ||
		strings.Contains(low, "not found: view") ||
		strings.Contains(low, "unrecognized name:") ||
		strings.Contains(low, "not found inside") ||
		strings.Contains(low, "does not exist in") ||
		strings.Contains(low, "in using clause not found")
}
