package diagnose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/ashish10alex/xf/internal/project"
)

func TestSkippedGraphs(t *testing.T) {
	fsys := fstest.MapFS{
		"definitions/graphs/graph.yaml":       {Data: []byte("version: 0.2.0.dev0/google\n")},
		"definitions/other/graph.yaml":        {Data: []byte("version: 0.2.0.dev0/google\n")},
		"definitions/node_modules/graph.yaml": {Data: []byte("x\n")},
		"definitions/graph.yml":               {Data: []byte("not a graph file\n")},
		"graph.yaml":                          {Data: []byte("outside definitions/\n")},
		"definitions/a.sqlx":                  {Data: []byte("SELECT 1\n")},
	}
	graph := func(errs ...project.CompilationError) *project.Graph {
		g := project.NewGraph([]*project.Action{
			{ID: "p.d.a", Kind: project.KindView, FileName: "definitions/a.sqlx"},
			{ID: "p.d.g", Kind: project.KindPropertyGraph, FileName: "definitions/other/graph.yaml"},
		})
		g.Errors = errs
		return g
	}
	got := SkippedGraphs(fsys, graph())
	if len(got) != 1 || got[0].FileName != "definitions/graphs/graph.yaml" || got[0].Message != skippedGraph {
		t.Errorf("got %+v, want the graph.yaml the compile has no action from", got)
	}
	if got := SkippedGraphs(fsys, graph(project.CompilationError{FileName: "definitions/a.sqlx", Message: "x"})); got != nil {
		t.Errorf("a compile with errors: got %+v, want none", got)
	}
	if got := SkippedGraphs(fstest.MapFS{"workflow_settings.yaml": {}}, graph()); got != nil {
		t.Errorf("no definitions/: got %+v", got)
	}
	if got := SkippedGraphs(fsys, nil); got != nil {
		t.Errorf("no graph: got %+v", got)
	}
}

func TestWarnings(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "definitions", "graph.yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("version: 0.2.0.dev0/google\nsemantic_model: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Warnings(root, "dataform", []project.CompilationError{{FileName: "definitions/graph.yaml", Message: "m"}}, os.ReadFile)
	want := map[string][]Diagnostic{file: {{File: file, End: len("version: 0.2.0.dev0/google"), Severity: Warning, Source: "dataform compile", Message: "m"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// The example compiled by a CLI that compiles graph.yaml has nothing to
// warn of; with its graph gone, as an older CLI compiles it, it has.
func TestSkippedGraphsExample(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "dataform")
	g := loadGraph(t, "examples/dataform.json")
	if got := SkippedGraphs(os.DirFS(root), g); got != nil {
		t.Errorf("got %+v, want none", got)
	}
	delete(g.Actions, "alex-personal-dev-01.xf_example.commerce_graph")
	if got := SkippedGraphs(os.DirFS(root), g); len(got) != 1 || got[0].FileName != "definitions/graphs/graph.yaml" {
		t.Errorf("got %+v, want definitions/graphs/graph.yaml", got)
	}
}
