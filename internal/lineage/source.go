package lineage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

const (
	// maxTableReads is how many metadata reads run at once; a wide hop
	// would otherwise start one per linked table.
	maxTableReads = 6
	// tableReadTimeout gives up on one slow read, which is tried again
	// next time, unlike a missing table.
	tableReadTimeout = 15 * time.Second
	// maxSearches is how many Data Lineage searches run at once.
	maxSearches = 8
	// probeColumns is how many of a reader's columns are probed to decide
	// whether Dataplex tracks its column lineage at all.
	probeColumns = 3
)

// call is one lookup, shared by everyone who asks for the same key while it
// runs and, once done, kept unless forget says otherwise.
type call[T any] struct {
	done chan struct{}
	v    T
	err  error
}

type memo[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

// get runs f once for key. A result forget reports true for is dropped, so
// the next get runs f again.
func (m *memo[T]) get(key string, f func() (T, error), forget func(error) bool) (T, error) {
	m.mu.Lock()
	if m.calls == nil {
		m.calls = map[string]*call[T]{}
	}
	c := m.calls[key]
	if c == nil {
		c = &call[T]{done: make(chan struct{})}
		m.calls[key] = c
		m.mu.Unlock()
		c.v, c.err = f()
		if forget != nil && forget(c.err) {
			m.mu.Lock()
			delete(m.calls, key)
			m.mu.Unlock()
		}
		close(c.done)
		return c.v, c.err
	}
	m.mu.Unlock()
	<-c.done
	return c.v, c.err
}

// Tables reads BigQuery table metadata, each table once.
type Tables struct {
	get   func(ctx context.Context, project, dataset, table string) (*warehouse.Table, error)
	sem   chan struct{}
	cache memo[*warehouse.Table]
}

// NewTables reads metadata with get.
func NewTables(get func(ctx context.Context, project, dataset, table string) (*warehouse.Table, error)) *Tables {
	return &Tables{get: get, sem: make(chan struct{}, maxTableReads)}
}

// Table is the metadata of project.dataset.table, or nil (and no error)
// when there is no such table.
func (t *Tables) Table(ctx context.Context, table string) (*warehouse.Table, error) {
	return t.cache.get(table, func() (*warehouse.Table, error) {
		parts := strings.Split(table, ".")
		if len(parts) != 3 {
			return nil, nil
		}
		select {
		case t.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-t.sem }()
		rctx, cancel := context.WithTimeout(ctx, tableReadTimeout)
		defer cancel()
		md, err := t.get(rctx, parts[0], parts[1], parts[2])
		var api *warehouse.APIError
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return nil, nil
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("reading %s took longer than %s", table, tableReadTimeout)
		}
		return md, err
	}, func(err error) bool {
		// Unlike a missing table, a slow, cancelled, rate-limited or
		// unauthorised read is worth trying again.
		return err != nil
	})
}

// Schema is the table's columns, or nil when there is no such table.
func (t *Tables) Schema(ctx context.Context, table string) ([]warehouse.Column, error) {
	md, err := t.Table(ctx, table)
	if md == nil {
		return nil, err
	}
	return md.Schema, err
}

// Casing is column as the table's schema spells it, e.g. PLAYER_ID for the
// lowercased player_id; column itself when the table can't be read.
func (t *Tables) Casing(ctx context.Context, table, column string) string {
	fields, _ := t.Schema(ctx, table)
	for _, f := range fields {
		if strings.EqualFold(f.Name, column) {
			return f.Name
		}
	}
	return column
}

// Searcher searches Data Lineage links; *Client is one.
type Searcher interface {
	SearchLinks(ctx context.Context, req SearchRequest) ([]Link, error)
}

// Source finds the readers of a table's columns. Lineage is searched in the
// table's own project and region, plus any extra projects configured via
// WithProjects (for cross-project BigQuery jobs whose Dataplex links are
// recorded in the consumer's project). Lookups are cached for the life of the
// Source, which is meant to last one impact check.
type Source struct {
	search        Searcher
	tables        *Tables
	graph         *project.Graph
	toProd        func(string) string
	extraProjects []string
	sem           chan struct{}
	// tracked is whether Dataplex tracks a table's column lineage; readers
	// are a table's table-level readers cached per (parent, table) so a failing
	// extra project does not force repeated searches on healthy projects.
	tracked       memo[bool]
	readers       memo[[]Link]
	deniedParents sync.Map // parent -> permanent error (401/403)
}

// NewSource searches with search, reads tables with tables, and spots this
// project's assertions and incremental tables in graph. toProd, when not
// nil, is the prod table of a dev run of one of its actions, so a dev run
// is known for the action it runs.
func NewSource(search Searcher, tables *Tables, graph *project.Graph, toProd func(string) string) *Source {
	if toProd == nil {
		toProd = func(t string) string { return t }
	}
	return &Source{search: search, tables: tables, graph: graph, toProd: toProd, sem: make(chan struct{}, maxSearches)}
}

// WithProjects configures additional GCP projects to search in Dataplex Data
// Lineage alongside each table's own project (same region), catching readers
// whose BigQuery jobs ran in a different consumer/BI project.
func (s *Source) WithProjects(projects ...string) *Source {
	seen := map[string]bool{}
	var out []string
	for _, p := range projects {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	s.extraProjects = out
	return s
}

// kind is the kind of the project's action table is a run of.
func (s *Source) kind(table string) (project.ActionKind, bool) {
	if s.graph == nil {
		return 0, false
	}
	a := s.graph.Actions[project.ActionID(s.toProd(table))]
	if a == nil {
		return 0, false
	}
	return a.Kind, true
}

func (s *Source) isAssertion(table string) bool {
	k, ok := s.kind(table)
	return ok && k == project.KindAssertion
}

func isPermanentLineageError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		if len(parts) == 0 {
			return false
		}
		for _, p := range parts {
			if !isPermanentLineageError(p) {
				return false
			}
		}
		return true
	}
	if errors.Is(err, ErrAuth) {
		return true
	}
	return strings.Contains(err.Error(), "no access to Data Lineage")
}

func (s *Source) searchLinks(ctx context.Context, req SearchRequest) ([]Link, error) {
	if cached, ok := s.deniedParents.Load(req.Parent); ok {
		return nil, cached.(error)
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.sem }()
	links, err := s.search.SearchLinks(ctx, req)
	if isPermanentLineageError(err) {
		s.deniedParents.Store(req.Parent, err)
	}
	return links, err
}

// parents are the Dataplex search parents for table: its own project and
// region first, followed by any configured extraProjects in the same region.
func (s *Source) parents(ctx context.Context, table string) ([]string, error) {
	md, err := s.tables.Table(ctx, table)
	if err != nil || md == nil || md.Location == "" {
		return nil, fmt.Errorf("couldn't read %s to find its region", table)
	}
	loc := strings.ToLower(md.Location)
	own := strings.SplitN(table, ".", 2)[0]
	out := []string{"projects/" + own + "/locations/" + loc}
	for _, p := range s.extraProjects {
		if p != own {
			out = append(out, "projects/"+p+"/locations/"+loc)
		}
	}
	return out, nil
}

// searchAcross searches req across all parents concurrently and merges links.
// If any parent fails, returns the merged links alongside the joined error so
// callers don't silently miss readers in an unreachable configured project.
func (s *Source) searchAcross(ctx context.Context, parents []string, req SearchRequest) ([]Link, error) {
	if len(parents) == 1 {
		r := req
		r.Parent = parents[0]
		return s.searchLinks(ctx, r)
	}
	type res struct {
		links []Link
		err   error
	}
	results := make([]res, len(parents))
	var wg sync.WaitGroup
	for i, parent := range parents {
		wg.Go(func() {
			r := req
			r.Parent = parent
			results[i].links, results[i].err = s.searchLinks(ctx, r)
		})
	}
	wg.Wait()
	var merged []Link
	var errs []error
	for _, r := range results {
		merged = append(merged, r.links...)
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	return merged, errors.Join(errs...)
}

// readerLinks is the table-level links from table, searched once per parent
// project so a failing extra project never retries healthy parents.
func (s *Source) readerLinks(ctx context.Context, parents []string, table string) ([]Link, error) {
	type res struct {
		links []Link
		err   error
	}
	results := make([]res, len(parents))
	var wg sync.WaitGroup
	for i, parent := range parents {
		wg.Go(func() {
			key := parent + "\x00" + table
			results[i].links, results[i].err = s.readers.get(key, func() ([]Link, error) {
				return s.searchLinks(ctx, SearchRequest{Parent: parent, Source: &EntityReference{FullyQualifiedName: FQN(table)}})
			}, func(err error) bool { return err != nil && !isPermanentLineageError(err) })
		})
	}
	wg.Wait()
	var merged []Link
	var errs []error
	for _, r := range results {
		merged = append(merged, r.links...)
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	return merged, errors.Join(errs...)
}

// hasColumnLineage is whether any of the table's first few columns has
// upstream column links. A table that can't be read or searched counts as
// untracked, so its readers are listed as ones that may read the column.
func (s *Source) hasColumnLineage(ctx context.Context, table string) bool {
	tracked, _ := s.tracked.get(table, func() (bool, error) {
		parents, err := s.parents(ctx, table)
		if err != nil {
			return false, nil
		}
		fields, _ := s.tables.Schema(ctx, table)
		fields = fields[:min(len(fields), probeColumns)]
		type probe struct {
			hit bool
			err error
		}
		found := make(chan probe, len(fields))
		for _, f := range fields {
			go func() {
				links, err := s.searchAcross(ctx, parents, SearchRequest{Target: &EntityReference{FullyQualifiedName: FQN(table), Field: Field(f.Name)}})
				found <- probe{len(links) > 0, err}
			}()
		}
		var hit bool
		var errs []error
		for range fields {
			p := <-found
			hit = hit || p.hit
			errs = append(errs, p.err)
		}
		if hit {
			return true, nil
		}
		return false, errors.Join(errs...)
	}, func(err error) bool {
		// A transient failed probe, e.g. rate limited, is asked again next time
		// rather than kept; permanent 401/403 errors are not retried.
		return err != nil && !isPermanentLineageError(err)
	})
	return tracked
}

// ColumnLinks is the columns that read table.column directly, spelled as
// each reader's schema spells them.
func (s *Source) ColumnLinks(ctx context.Context, table, column string) ([]ColumnLink, error) {
	parents, err := s.parents(ctx, table)
	if err != nil {
		return nil, err
	}
	// Started now so it overlaps the column search; TableOnlyReaders picks
	// it up from the cache.
	go func() { _, _ = s.readerLinks(ctx, parents, table) }()
	links, searchErr := s.searchAcross(ctx, parents, SearchRequest{Source: &EntityReference{FullyQualifiedName: FQN(table), Field: Field(column)}})
	if searchErr != nil && len(links) == 0 {
		return nil, searchErr
	}
	found := ColumnLinksFromAPI(links)
	var wg sync.WaitGroup
	for i := range found {
		wg.Go(func() {
			// Dataplex returns lowercase names
			found[i].Column = s.tables.Casing(ctx, found[i].Table, found[i].Column)
			found[i].Assertion = s.isAssertion(found[i].Table)
		})
	}
	wg.Wait()
	return found, searchErr
}

func (s *Source) tableLink(reader string) ColumnLink {
	return ColumnLink{Table: reader, DependencyType: TableOnly, Assertion: s.isAssertion(reader)}
}

// TableOnlyReaders is the readers of table, other than those in linked,
// that may read a column without Dataplex saying so: incremental tables
// never get column lineage, and other readers are probed. Operations are
// probed too: some get column lineage (CREATE TABLE … CLONE), others, such
// as procedures, don't.
func (s *Source) TableOnlyReaders(ctx context.Context, table string, linked map[string]bool) ([]ColumnLink, error) {
	parents, err := s.parents(ctx, table)
	if err != nil {
		return nil, err
	}
	links, searchErr := s.readerLinks(ctx, parents, table)
	if searchErr != nil && len(links) == 0 {
		return nil, searchErr
	}
	untracked := UntrackedReaders(ctx, TablesFromAPI(links), linked, func(ctx context.Context, reader string) bool {
		if k, ok := s.kind(reader); ok && k == project.KindIncremental {
			return false
		}
		return s.hasColumnLineage(ctx, reader)
	}, s.toProd)
	out := make([]ColumnLink, len(untracked))
	for i, r := range untracked {
		out[i] = s.tableLink(r)
	}
	return out, searchErr
}

// TableReaders is every reader of table, as table-level links: who loses a
// table the branch deletes.
func (s *Source) TableReaders(ctx context.Context, table string) ([]ColumnLink, error) {
	parents, err := s.parents(ctx, table)
	if err != nil {
		return nil, err
	}
	links, searchErr := s.readerLinks(ctx, parents, table)
	if searchErr != nil && len(links) == 0 {
		return nil, searchErr
	}
	readers := TablesFromAPI(links)
	out := make([]ColumnLink, len(readers))
	for i, r := range readers {
		out[i] = s.tableLink(r)
	}
	return out, searchErr
}
