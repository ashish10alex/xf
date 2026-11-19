package impact

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

const libraryGraph = "my-gcp-project.dataform.LibraryGraph"

// propertyGraph is a real compile of definitions/graph.yaml: Author with
// fields (UPPER(name), address.country), Book with fields and metrics,
// Authorship and an external Shelf with ALL COLUMNS, and two edges on
// wrote.
func propertyGraph(t *testing.T) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "property_graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGraphReaders(t *testing.T) {
	g := propertyGraph(t)
	link := func(column string, dep lineage.DependencyType) lineage.ColumnLink {
		return lineage.ColumnLink{Table: libraryGraph, Column: column, DependencyType: dep}
	}
	tests := []struct {
		table, column string
		want          []lineage.ColumnLink
	}{
		{"my-gcp-project.dataform.authors", "name", []lineage.ColumnLink{link("name", lineage.ExactCopy), link("upper_name", lineage.Other)}},
		{"my-gcp-project.dataform.authors", "NAME", []lineage.ColumnLink{link("name", lineage.ExactCopy), link("upper_name", lineage.Other)}},
		{"my-gcp-project.dataform.authors", "address", []lineage.ColumnLink{link("country", lineage.Other)}},
		{"my-gcp-project.dataform.authors", "nickname", nil},
		{"my-gcp-project.dataform.books", "pages", []lineage.ColumnLink{link("pages", lineage.ExactCopy), link("total_pages", lineage.Other)}},
		// ALL COLUMNS exposes it; the edges' keys name it too
		{"my-gcp-project.dataform.wrote", "book_id", []lineage.ColumnLink{link("book_id", lineage.ExactCopy)}},
		{"my-gcp-project.dataform.wrote", "note", []lineage.ColumnLink{link("note", lineage.ExactCopy)}},
		// an external source: no dependency, still read
		{"my-gcp-project.raw.shelves", "shelf_id", []lineage.ColumnLink{link("shelf_id", lineage.ExactCopy)}},
		{"my-gcp-project.dataform.authors", "", []lineage.ColumnLink{link("", lineage.Other)}},
		{"my-gcp-project.dataform.author_books", "", nil},
	}
	for _, tt := range tests {
		got := GraphReaders(g, tt.table, tt.column)
		slices.SortFunc(got, func(a, b lineage.ColumnLink) int { return cmp.Compare(a.Column, b.Column) })
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s#%s: got %+v, want %+v", tt.table, tt.column, got, tt.want)
		}
	}
}

// A key, or an edge's column, that no property exposes is still read.
func TestGraphReadersKeysAndEdgeColumns(t *testing.T) {
	a := &project.Action{ID: "p.g.G", Kind: project.KindPropertyGraph, GraphTables: []project.GraphTable{
		{Name: "Book", Source: project.Target{Database: "p", Schema: "d", Name: "books"}, Columns: []string{"id"}, Properties: []project.GraphProperty{{Name: "title", Expression: "title"}}},
		{Name: "Wrote", Edge: true, Source: project.Target{Database: "p", Schema: "d", Name: "wrote"}, Columns: []string{"author_id", "book_id", "book_id"}},
	}}
	g := project.NewGraph([]*project.Action{a})
	for _, c := range []struct{ table, column string }{{"p.d.books", "id"}, {"p.d.wrote", "book_id"}} {
		want := []lineage.ColumnLink{{Table: "p.g.G", DependencyType: lineage.Other}}
		if got := GraphReaders(g, c.table, c.column); !reflect.DeepEqual(got, want) {
			t.Errorf("%s#%s: got %+v, want %+v", c.table, c.column, got, want)
		}
	}
	if got := GraphReaders(g, "p.d.wrote", "note"); got != nil {
		t.Errorf("an edge has no properties: got %+v", got)
	}
}

// Dataplex has no lineage for CREATE PROPERTY GRAPH, so a dropped column
// that a graph reads is found in the compile, and is critical. When the
// lineage lookup fails, the graph is still listed.
func TestRunFlagsAPropertyGraphThatReadsADroppedColumn(t *testing.T) {
	g := propertyGraph(t)
	books, authors := "my-gcp-project.dataform.books", "my-gcp-project.dataform.authors"
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{Changed: []changes.Changed{
			{ID: project.ActionID(books), FileName: "definitions/books.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
			{ID: project.ActionID(authors), FileName: "definitions/authors.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
		}},
	}
	prod := map[string][]warehouse.Column{
		books:   {f("id", "INT64"), f("title", "STRING"), f("pages", "INT64"), f("isbn", "STRING")},
		authors: {f("id", "INT64"), f("name", "STRING")},
	}
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]warehouse.Column, error) { return prod[table], nil },
		DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
			if strings.Contains(sql, "Graph Systems") { // books, without title and isbn
				return &warehouse.Check{Schema: []warehouse.Column{f("id", "INT64"), f("pages", "INT64")}}, nil
			}
			return &warehouse.Check{Schema: []warehouse.Column{f("id", "INT64")}}, nil // authors, without name
		},
		Readers: &fakeSource{failing: map[string]bool{authors: true}},
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string]Table{}
	for _, tb := range report.AtRisk {
		byTable[tb.Table] = tb
	}
	cols := map[string]Column{}
	for _, c := range byTable[books].Columns {
		cols[c.Column] = c
	}
	want := []Reader{{Table: libraryGraph, Column: "title", DependencyType: lineage.ExactCopy, FilePath: "definitions/graph.yaml", Graph: true}}
	if got := cols["title"].Readers; !reflect.DeepEqual(got, want) {
		t.Errorf("title's readers %+v, want %+v", got, want)
	}
	if got := cols["isbn"].Readers; len(got) != 0 {
		t.Errorf("isbn's readers %+v, want none: the graph doesn't name it", got)
	}
	if s := ImpactSeverity(cols["title"].Change, cols["title"].Readers, ""); s != SeverityCritical {
		t.Errorf("title's severity %v", s)
	}
	name := byTable[authors].Columns
	if len(name) != 1 || name[0].ReadersError == "" || len(name[0].Readers) != 2 || name[0].Readers[0].Table != libraryGraph {
		t.Errorf("authors' columns %+v, want name read by the graph despite the failed lookup", name)
	}
	md := Markdown(report)
	if !strings.Contains(md, "| 🚨 | `books` | drops `title`, `isbn` | **`LibraryGraph` reads `title` · exact copy · not changed here** |") ||
		!strings.Contains(md, "Property graphs come from the compile") {
		t.Error(md)
	}
}
