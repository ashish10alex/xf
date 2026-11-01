package deferral

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// BigQuery is what the lookups ask of BigQuery; *bigquery.Client is one.
type BigQuery interface {
	ListTablesLabelled(ctx context.Context, project, dataset string) ([]bigquery.LabelledTable, error)
	ListRoutines(ctx context.Context, project, dataset string) ([]string, error)
	Exists(ctx context.Context, project, dataset, name string, routine bool) (bool, error)
	View(ctx context.Context, project, dataset, table string) (*bigquery.ViewInfo, error)
}

// lookupTTL is how long a listing or a prod lookup is kept, so a dry run
// on every save adds no BigQuery calls.
const lookupTTL = 2 * time.Minute

// Lookups says which Dev Targets and Prod Targets exist, keeping the
// answers for a while. Dev datasets are listed whole, one call each; Prod
// Targets are looked up one at a time, as only candidates need them.
type Lookups struct {
	bq  func(context.Context) (BigQuery, error)
	now func() time.Time

	mu       sync.Mutex
	datasets map[string]*listing
	prod     map[project.ActionID]*prodLookup
	// unreadable are Prod Targets a query was denied: kept until the
	// lookups are cleared with them, as access rarely changes.
	unreadable map[project.ActionID]bool
	modified   map[project.ActionID]int64
	// generation goes up whenever an answer is dropped.
	generation uint64
}

type listing struct {
	at      time.Time
	missing bool
	tables  map[string]bool
	proxies map[string]bool
}

type prodLookup struct {
	at     time.Time
	status ProdStatus
}

// NewLookups makes lookups that ask the client bq returns; now is
// time.Now when nil.
func NewLookups(bq func(context.Context) (BigQuery, error), now func() time.Time) *Lookups {
	if now == nil {
		now = time.Now
	}
	return &Lookups{bq: bq, now: now, datasets: map[string]*listing{}, prod: map[project.ActionID]*prodLookup{},
		unreadable: map[project.ActionID]bool{}, modified: map[project.ActionID]int64{}}
}

// Clear drops what is known of which tables exist, as a run may just have
// built some; with unreadable, also which prod tables may not be read.
func (l *Lookups) Clear(unreadable bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.datasets = map[string]*listing{}
	l.prod = map[project.ActionID]*prodLookup{}
	l.modified = map[project.ActionID]int64{}
	if unreadable {
		l.unreadable = map[project.ActionID]bool{}
	}
	l.generation++
}

// Generation goes up whenever an answer is dropped, so a Deferral worked
// out before then is out of date.
func (l *Lookups) Generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generation
}

// MarkUnreadable remembers Prod Targets a query was denied, so the next
// Deferral reads them from dev. It says whether any is new.
func (l *Lookups) MarkUnreadable(ts []project.Target) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	added := false
	for _, t := range ts {
		if !l.unreadable[t.ID()] {
			l.unreadable[t.ID()] = true
			added = true
		}
	}
	if added {
		l.generation++
	}
	return added
}

func (l *Lookups) fresh(at time.Time) bool { return l.now().Sub(at) < lookupTTL }

// list lists a dev dataset; missing when it doesn't exist. A listing that
// fails otherwise is an error: guessing would read dev for a table that
// isn't built, or prod for one that is.
func (l *Lookups) list(ctx context.Context, database, schema string) (*listing, error) {
	key := database + "." + schema
	l.mu.Lock()
	if c := l.datasets[key]; c != nil && l.fresh(c.at) {
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	bq, err := l.bq(ctx)
	if err != nil {
		return nil, err
	}
	out := &listing{at: l.now(), tables: map[string]bool{}, proxies: map[string]bool{}}
	tables, err := bq.ListTablesLabelled(ctx, database, schema)
	switch {
	case bigquery.IsNotFound(err):
		out.missing = true
	case bigquery.IsForbidden(err):
		return nil, fmt.Errorf("no permission to list the dev dataset %s, so it isn't known which upstream tables are built in dev: %w", key, err)
	case err != nil:
		return nil, fmt.Errorf("listing the dev dataset %s: %w", key, err)
	}
	for _, t := range tables {
		if t.Labels[ProxyLabel] == "true" {
			out.proxies[t.ID] = true
		} else {
			out.tables[t.ID] = true
		}
	}
	if !out.missing {
		// Functions an operation builds are routines, which the table
		// listing leaves out. A dataset whose routines can't be listed is
		// taken as having none.
		if rs, err := bq.ListRoutines(ctx, database, schema); err == nil {
			for _, r := range rs {
				out.tables[r] = true
			}
		}
	}
	l.mu.Lock()
	l.datasets[key] = out
	l.mu.Unlock()
	return out, nil
}

// listAll lists the datasets of ts.
func (l *Lookups) listAll(ctx context.Context, ts []project.Target) (map[string]*listing, error) {
	out := map[string]*listing{}
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for _, t := range ts {
		key := t.Database + "." + t.Schema
		mu.Lock()
		_, done := out[key]
		if !done {
			out[key] = nil
		}
		mu.Unlock()
		if done {
			continue
		}
		wg.Add(1)
		go func(t project.Target) {
			defer wg.Done()
			ls, err := l.list(ctx, t.Database, t.Schema)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			out[t.Database+"."+t.Schema] = ls
		}(t)
	}
	wg.Wait()
	return out, firstErr
}

func (ls *listing) has(name string) bool   { return ls != nil && ls.tables[name] }
func (ls *listing) proxy(name string) bool { return ls != nil && ls.proxies[name] }

// prodStatus looks a Prod Target up.
func (l *Lookups) prodStatus(ctx context.Context, t project.Target, routine bool) ProdStatus {
	l.mu.Lock()
	if l.unreadable[t.ID()] {
		l.mu.Unlock()
		return ProdUnreadable
	}
	if c := l.prod[t.ID()]; c != nil && l.fresh(c.at) {
		l.mu.Unlock()
		return c.status
	}
	l.mu.Unlock()
	status := ProdMissing
	bq, err := l.bq(ctx)
	if err == nil {
		var ok bool
		ok, err = bq.Exists(ctx, t.Database, t.Schema, t.Name, routine)
		switch {
		case bigquery.IsForbidden(err):
			status = ProdUnreadable
		case err != nil:
			return ProdMissing // not kept: it may pass next time
		case ok:
			status = ProdExists
		}
	} else {
		return ProdMissing
	}
	l.mu.Lock()
	l.prod[t.ID()] = &prodLookup{at: l.now(), status: status}
	l.mu.Unlock()
	return status
}

// lastModified is when a dev table was last built, 0 when unknown.
func (l *Lookups) lastModified(ctx context.Context, t project.Target) int64 {
	l.mu.Lock()
	ms, ok := l.modified[t.ID()]
	l.mu.Unlock()
	if ok {
		return ms
	}
	bq, err := l.bq(ctx)
	if err != nil {
		return 0
	}
	v, err := bq.View(ctx, t.Database, t.Schema, t.Name)
	if err != nil || v == nil || v.LastModified.IsZero() {
		return 0
	}
	ms = v.LastModified.UnixMilli()
	l.mu.Lock()
	l.modified[t.ID()] = ms
	l.mu.Unlock()
	return ms
}

// BuiltInDev says whether dev, a Dev Target, is built (a Proxy View isn't),
// and when it was last built, 0 when not known. A dataset that can't be
// listed is an error.
func (l *Lookups) BuiltInDev(ctx context.Context, dev project.Target) (ok bool, lastModified int64, err error) {
	ls, err := l.list(ctx, dev.Database, dev.Schema)
	if err != nil || !ls.has(dev.Name) {
		return false, 0, err
	}
	return true, l.lastModified(ctx, dev), nil
}

// ErrSameAsDev says the prod compile names the same tables as dev.
var ErrSameAsDev = errors.New("prod names the same tables as dev, so there is nothing to read from prod: set prodCompilerOptions to your prod project's, e.g. --default-database=my-prod-project")

// Resolve works out the Deferral of the upstream actions of selected, in
// g, the dev compile; p is the prod compile's targets and changed says
// which actions are Changed Actions (nil when not known).
func Resolve(ctx context.Context, g *project.Graph, selected []project.ActionID, p ProdTargets, l *Lookups, changed func(project.ActionID) bool) (*Deferral, error) {
	if SameAsDev(g, p) {
		return nil, ErrSameAsDev
	}
	cands := Candidates(g, selected, p)
	if len(cands) == 0 {
		return &Deferral{}, nil
	}
	devs := make([]project.Target, len(cands))
	for i, c := range cands {
		devs[i] = c.Dev
	}
	listings, err := l.listAll(ctx, devs)
	if err != nil {
		return nil, err
	}
	devExists := func(t project.Target) bool { return listings[t.Database+"."+t.Schema].has(t.Name) }

	type result struct {
		status ProdStatus
		ms     int64
	}
	results := make([]result, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		if c.Prod == nil {
			continue
		}
		wg.Add(1)
		go func(i int, c Candidate) {
			defer wg.Done()
			if devExists(c.Dev) {
				if !c.Routine {
					results[i].ms = l.lastModified(ctx, c.Dev)
				}
				return
			}
			results[i].status = l.prodStatus(ctx, *c.Prod, c.Routine)
		}(i, c)
	}
	wg.Wait()
	status := map[project.ActionID]ProdStatus{}
	d := &Deferral{}
	for i, c := range cands {
		if c.Prod == nil {
			continue
		}
		if devExists(c.Dev) {
			d.BuiltInDev = append(d.BuiltInDev, BuiltInDev{Dev: c.Dev, LastModified: results[i].ms})
			continue
		}
		status[c.Prod.ID()] = results[i].status
	}
	d.Entries = Decide(cands, devExists, func(t project.Target) ProdStatus { return status[t.ID()] })
	if changed != nil {
		for i, e := range d.Entries {
			if e.Read() && changed(e.Dev.ID()) {
				d.Entries[i].Stale = true
			}
		}
	}
	return d, nil
}

// Proxies are the upstream Dev Targets of selected in g that are Proxy
// Views: they read prod whether or not defer to prod is on.
func Proxies(ctx context.Context, g *project.Graph, selected []project.ActionID, l *Lookups) ([]project.Target, error) {
	isSelected := map[project.ActionID]bool{}
	for _, id := range selected {
		isSelected[id] = true
	}
	var ups []project.Target
	seen := map[project.ActionID]bool{}
	for _, id := range selected {
		a := g.Actions[id]
		if a == nil {
			continue
		}
		for _, t := range a.DependencyTargets {
			if dep := g.Actions[t.ID()]; isSelected[t.ID()] || seen[t.ID()] || dep != nil && dep.Kind.External() {
				continue
			}
			seen[t.ID()] = true
			ups = append(ups, t)
		}
	}
	if len(ups) == 0 {
		return nil, nil
	}
	listings, err := l.listAll(ctx, ups)
	if err != nil {
		return nil, err
	}
	var out []project.Target
	for _, t := range ups {
		if listings[t.Database+"."+t.Schema].proxy(t.Name) {
			out = append(out, t)
		}
	}
	return out, nil
}

// MissingDatasets are the dev datasets of ts that don't exist; xf never
// makes one, so a Proxy View can't go there.
func (l *Lookups) MissingDatasets(ctx context.Context, ts []project.Target) (map[string]bool, error) {
	listings, err := l.listAll(ctx, ts)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for key, ls := range listings {
		if ls != nil && ls.missing {
			out[key] = true
		}
	}
	return out, nil
}
