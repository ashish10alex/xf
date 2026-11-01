package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// A file of declarations alone has the declarations table: its rows in
// file order, and each row's "updated" once the page asks for it.
func TestDeclarationsTable(t *testing.T) {
	root := t.TempDir()
	src := "[\n  \"zeta\"\n, \"alpha\"\n].forEach((name) => declare({ schema: \"raw\", name }));\n" +
		"declare({ schema: \"raw\", name: \"pre_\" + \"built\" });\n"
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "definitions", "sources.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	decl := func(name string) *project.Action {
		tg := project.Target{Database: "p", Schema: "raw", Name: name}
		return &project.Action{ID: tg.ID(), Target: tg, CanonicalTarget: tg, Kind: project.KindDeclaration, FileName: "definitions/sources.js"}
	}
	g := project.NewGraph([]*project.Action{decl("alpha"), decl("zeta"), decl("pre_built")})

	var mu sync.Mutex
	looked := map[string]int{}
	table := func(ctx context.Context, p, d, tb string) (*bigquery.Table, error) {
		mu.Lock()
		looked[tb]++
		mu.Unlock()
		switch tb {
		case "zeta":
			return nil, &bigquery.APIError{Status: http.StatusForbidden, Message: "Access Denied: Table p:raw.zeta"}
		case "pre_built":
			return &bigquery.Table{Type: "VIEW", ViewQuery: "SELECT 1 AS x\n", Schema: []bigquery.Field{{Name: "x", Type: "INTEGER"}}}, nil
		}
		return &bigquery.Table{Type: "TABLE", LastModified: time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local),
			Schema: []bigquery.Field{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "STRING"}}}, nil
	}
	now := func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(g), Table: table, Now: now})
	get := func(id string, done func(v actionView) bool) actionView {
		t.Helper()
		var v actionView
		waitFor(t, func() bool {
			v = actionView{}
			getJSON(t, c, ts.URL+"/api/action?id="+id, &v)
			return done(v)
		})
		return v
	}

	var v actionView
	getJSON(t, c, ts.URL+"/api/action?id=p.raw.alpha", &v)
	var order []string
	for _, r := range v.Externals {
		order = append(order, r.Name)
	}
	if len(order) != 3 || order[0] != "zeta" || order[1] != "alpha" || order[2] != "pre_built" {
		t.Fatalf("rows %v, want zeta, alpha (file order) then pre_built (no line)", order)
	}
	if r := v.Externals[0]; r.At == nil || r.At.Line != 1 || r.At.Col != 3 {
		t.Errorf("zeta at %+v, want line 1 col 3", r.At)
	}
	if v.Externals[2].At != nil {
		t.Errorf("pre_built at %+v, want none: the name is built", v.Externals[2].At)
	}
	if v.Externals[0].Updated != nil {
		t.Errorf("zeta updated %+v: only the selected declaration is looked up", v.Externals[0].Updated)
	}

	// The selected declaration's table gives its row's "updated" and pane
	// 2's columns.
	v = get("p.raw.alpha", func(v actionView) bool { return v.TableType != "" })
	if u := v.Externals[1].Updated; u == nil || u.Tone != "" || u.Value != "2026-10-20 09:00 · 3h ago" {
		t.Errorf("alpha updated %+v", u)
	}
	if v.TableType != "TABLE" || len(v.Columns.Fields) != 2 || v.Columns.Fields[1].Name != "name" || !v.SQL.Empty {
		t.Errorf("alpha: type %q, columns %+v, sql %+v; want the table's columns and no SQL", v.TableType, v.Columns, v.SQL.Plain)
	}

	// A declared view shows its SQL; the row looked up before keeps its
	// "updated".
	v = get("p.raw.pre_built", func(v actionView) bool { return v.TableType != "" })
	if len(v.SQL.Plain) != 2 || v.SQL.Plain[1] != "SELECT 1 AS x" || v.TableType != "VIEW" || len(v.Columns.Fields) != 1 {
		t.Errorf("view: type %q, sql %q, columns %+v", v.TableType, v.SQL.Plain, v.Columns)
	}
	if v.Externals[1].Updated == nil || v.Externals[0].Updated != nil {
		t.Errorf("rows' updated %+v, %+v: want alpha's kept, zeta's never looked up", v.Externals[1].Updated, v.Externals[0].Updated)
	}

	// A table it can't see says why in the columns tab.
	v = get("p.raw.zeta", func(v actionView) bool { return v.Columns.Failed })
	if v.Columns.Reason != "✗ Access Denied: Table p:raw.zeta" {
		t.Errorf("zeta columns %+v", v.Columns)
	}
	mu.Lock()
	if looked["alpha"] != 1 || looked["zeta"] != 1 || looked["pre_built"] != 1 {
		t.Errorf("looked up %v: want each selected table once", looked)
	}
	mu.Unlock()

	// A file of one declaration has no declarations table.
	_, ts2, c2 := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	v = actionView{}
	getJSON(t, c2, ts2.URL+"/api/action?id=p.raw.orders", &v)
	if v.Externals != nil {
		t.Errorf("one declaration: externals %+v, want none", v.Externals)
	}
}

// Preview in the declarations table makes a scratch that selects from the
// table, left to run.
func TestScratchPreview(t *testing.T) {
	bq, _ := fakeScratchBQ(t)
	_, ts, c := newScratchTest(t, frontend.Deps{}, bq)
	var f scratchFile
	postJSON(t, c, ts.URL+"/api/scratch/create", map[string]any{"id": "p.raw.orders", "form": "preview"}, &f)
	if f.SQL != "SELECT *\nFROM `p.raw.orders`\nLIMIT 100\n" || f.From != "raw.orders" {
		t.Errorf("preview scratch = %+v", f)
	}
}
