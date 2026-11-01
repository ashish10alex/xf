package lineage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
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
	get   func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error)
	sem   chan struct{}
	cache memo[*bigquery.Table]
}

// NewTables reads metadata with get.
func NewTables(get func(ctx context.Context, project, dataset, table string) (*bigquery.Table, error)) *Tables {
	return &Tables{get: get, sem: make(chan struct{}, maxTableReads)}
}

// Table is the metadata of project.dataset.table, or nil (and no error)
// when there is no such table.
func (t *Tables) Table(ctx context.Context, table string) (*bigquery.Table, error) {
	return t.cache.get(table, func() (*bigquery.Table, error) {
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
		var api *bigquery.APIError
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
func (t *Tables) Schema(ctx context.Context, table string) ([]bigquery.Field, error) {
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
// table's own project and region. Lookups are cached for the life of the
// Source, which is meant to last one impact check.
type Source struct {
	search Searcher
	tables *Tables
	graph  *project.Graph
	toProd func(string) string
	sem    chan struct{}
	// tracked is whether Dataplex tracks a table's column lineage; readers
	// are a table's table-level readers, the same for every column of it.
	tracked memo[bool]
	readers memo[[]Link]
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

func (s *Source) searchLinks(ctx context.Context, req SearchRequest) ([]Link, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.sem }()
	return s.search.SearchLinks(ctx, req)
}

// parent is where table's lineage lives: its project and region.
func (s *Source) parent(ctx context.Context, table string) (string, error) {
	md, err := s.tables.Table(ctx, table)
	if err != nil || md == nil || md.Location == "" {
		return "", fmt.Errorf("couldn't read %s to find its region", table)
	}
	return "projects/" + strings.SplitN(table, ".", 2)[0] + "/locations/" + strings.ToLower(md.Location), nil
}

// readerLinks is the table-level links from table, searched once.
func (s *Source) readerLinks(ctx context.Context, parent, table string) ([]Link, error) {
	return s.readers.get(table, func() ([]Link, error) {
		return s.searchLinks(ctx, SearchRequest{Parent: parent, Source: &EntityReference{FullyQualifiedName: FQN(table)}})
	}, func(err error) bool { return err != nil })
}

// hasColumnLineage is whether any of the table's first few columns has
// upstream column links. A table that can't be read or searched counts as
// untracked, so its readers are listed as ones that may read the column.
func (s *Source) hasColumnLineage(ctx context.Context, table string) bool {
	tracked, _ := s.tracked.get(table, func() (bool, error) {
		parent, err := s.parent(ctx, table)
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
				links, err := s.searchLinks(ctx, SearchRequest{Parent: parent, Target: &EntityReference{FullyQualifiedName: FQN(table), Field: Field(f.Name)}})
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
		// A failed probe, e.g. rate limited, is asked again next time
		// rather than kept.
		return err != nil
	})
	return tracked
}

// ColumnLinks is the columns that read table.column directly, spelled as
// each reader's schema spells them.
func (s *Source) ColumnLinks(ctx context.Context, table, column string) ([]ColumnLink, error) {
	parent, err := s.parent(ctx, table)
	if err != nil {
		return nil, err
	}
	// Started now so it overlaps the column search; TableOnlyReaders picks
	// it up from the cache.
	go func() { _, _ = s.readerLinks(ctx, parent, table) }()
	links, err := s.searchLinks(ctx, SearchRequest{Parent: parent, Source: &EntityReference{FullyQualifiedName: FQN(table), Field: Field(column)}})
	if err != nil {
		return nil, err
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
	return found, nil
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
	parent, err := s.parent(ctx, table)
	if err != nil {
		return nil, err
	}
	links, err := s.readerLinks(ctx, parent, table)
	if err != nil {
		return nil, err
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
	return out, nil
}

// TableReaders is every reader of table, as table-level links: who loses a
// table the branch deletes.
func (s *Source) TableReaders(ctx context.Context, table string) ([]ColumnLink, error) {
	parent, err := s.parent(ctx, table)
	if err != nil {
		return nil, err
	}
	links, err := s.readerLinks(ctx, parent, table)
	if err != nil {
		return nil, err
	}
	readers := TablesFromAPI(links)
	out := make([]ColumnLink, len(readers))
	for i, r := range readers {
		out[i] = s.tableLink(r)
	}
	return out, nil
}
