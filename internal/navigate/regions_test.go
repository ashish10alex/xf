package navigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/project"
)

// alignedAll reports whether every piece of r lines up with compiled.
func alignedAll(r Region, compiled string) bool {
	return len(r.Align(compiled).spans) == len(r.Pieces)
}

// checkRegions checks, for every sqlx action of a compiled project, that each
// region of its source lines up with what Dataform compiled from it.
func checkRegions(t *testing.T, root string, g *project.Graph) (checked int) {
	t.Helper()
	for _, a := range g.Actions {
		if !strings.HasSuffix(a.FileName, ".sqlx") || a.ParentAction != nil || a.Kind == project.KindDeclaration || a.Kind == project.KindUnknown {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, a.FileName))
		if err != nil {
			t.Fatal(err)
		}
		r := Regions(src)
		check := func(what string, region Region, compiled string) {
			t.Helper()
			checked++
			if !alignedAll(region, compiled) {
				t.Errorf("%s %s: only %d of %d pieces line up", a.FileName, what, len(region.Align(compiled).spans), len(region.Pieces))
			}
		}
		switch a.Kind {
		case project.KindOperation:
			check("queries", r.Main, strings.Join(a.Queries, "---"))
		default:
			check("query", r.Main, a.Query)
		}
		if a.Kind == project.KindIncremental {
			check("incremental query", r.Main, a.IncrementalQuery)
			for i, op := range a.IncrementalPreOps {
				if i < len(r.PreOps) {
					check("incremental pre_operations", r.PreOps[i], op)
				}
			}
		}
		for i, op := range a.PreOps {
			if i < len(r.PreOps) {
				check("pre_operations", r.PreOps[i], op)
			}
		}
		for i, op := range a.PostOps {
			if i < len(r.PostOps) {
				check("post_operations", r.PostOps[i], op)
			}
		}
	}
	return checked
}

func TestRegionsAlignWithCompiledFixtures(t *testing.T) {
	for _, name := range []string{"basic", "incremental", "navigation"} {
		t.Run(name, func(t *testing.T) {
			n := checkRegions(t, testdata(t, "projects", name), loadGraph(t, name+".json"))
			if n == 0 {
				t.Fatal("nothing checked")
			}
		})
	}
}

// TestRegionsAlignWithRealProject runs against a real project when
// XF_REAL_PROJECT names its root and XF_REAL_COMPILE its compile output.
func TestRegionsAlignWithRealProject(t *testing.T) {
	root, compiled := os.Getenv("XF_REAL_PROJECT"), os.Getenv("XF_REAL_COMPILE")
	if root == "" || compiled == "" {
		t.Skip("set XF_REAL_PROJECT and XF_REAL_COMPILE to run")
	}
	data, err := os.ReadFile(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if data, err = dataform.ExtractCompileJSON(data); err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("checked %d compiled parts", checkRegions(t, root, g))
}

func TestRegions(t *testing.T) {
	src := "config { type: \"table\" }\n\npre_operations {\n  DECLARE x INT64 DEFAULT ${n};\n}\n\nSELECT ${ref(\"a\")} -- ${not_substituted}\nFROM t\n\npost_operations { SELECT 1 }\n"
	r := Regions([]byte(src))
	if got := src[r.ConfigStart:r.ConfigEnd]; got != ` type: "table" ` {
		t.Errorf("config body %q", got)
	}
	var main []string
	for _, p := range r.Main.Pieces {
		main = append(main, p.Text)
	}
	want := []string{"\n\n", "\n\nSELECT ", " -- ${not_substituted}\nFROM t\n\n", "\n"}
	if strings.Join(main, "|") != strings.Join(want, "|") {
		t.Errorf("main pieces %q, want %q", main, want)
	}
	if len(r.PreOps) != 1 || r.PreOps[0].Pieces[0].Text != "\n  DECLARE x INT64 DEFAULT " || len(r.PostOps) != 1 || r.PostOps[0].Pieces[0].Text != " SELECT 1 " {
		t.Errorf("ops %+v %+v", r.PreOps, r.PostOps)
	}

	compiled := "\n\n\n\nSELECT `p.d.a` -- ${not_substituted}\nFROM t\n\n\n"
	m := r.Main.Align(compiled)
	for _, tt := range []struct {
		compiled, source string // text at the compiled offset and at the source offset it maps to
	}{
		{"SELECT `p", "SELECT ${"},
		{"`p.d.a`", "${ref"}, // inside a substitution: where it starts
		{"FROM t", "FROM t"},
		{"-- ${not", "-- ${not"},
	} {
		off, ok := m.Source(strings.Index(compiled, tt.compiled))
		if !ok || !strings.HasPrefix(src[off:], tt.source) {
			t.Errorf("%q maps to %q, want %q", tt.compiled, src[off:min(off+10, len(src))], tt.source)
		}
	}
}

func TestRegionsCRLF(t *testing.T) {
	src := "config {\r\n  type: \"view\"\r\n}\r\n\r\nSELECT a\r\nFROM ${ref(\"t\")}\r\nWHERE b\r\n"
	compiled := "\n\nSELECT a\nFROM `p.d.t`\nWHERE b\n"
	m := Regions([]byte(src)).Main.Align(compiled)
	for _, word := range []string{"SELECT", "FROM", "WHERE", "b\n"} {
		off, ok := m.Source(strings.Index(compiled, word))
		want := strings.TrimSuffix(word, "\n")
		if !ok || !strings.HasPrefix(src[off:], want) {
			t.Errorf("%q maps to %q", word, src[off:min(off+8, len(src))])
		}
	}
	// The newline after b maps to the end of its source line, before the CR.
	if off, _ := m.Source(strings.Index(compiled, "b\n") + 1); src[off] != '\r' {
		t.Errorf("line end maps to %q", src[off:])
	}
}

func TestSQLParts(t *testing.T) {
	src := "config {\n  name: \"${x}\"\n}\njs { const a = 1; }\n\npre_operations {\n  select ${ref(\"a\")}\n}\n\n-- ${c}\nselect ${a}, 1 from ${ref(\"b\")}\n"
	parts, ok := SQLParts([]byte(src))
	if !ok {
		t.Fatal("not ok")
	}
	var got []string
	for _, p := range parts {
		var islands []string
		for _, is := range p.Islands {
			islands = append(islands, src[is[0]:is[1]])
		}
		got = append(got, strings.TrimSpace(src[p.Start:p.End])+" | "+strings.Join(islands, " "))
	}
	want := []string{
		`select ${ref("a")} | ${ref("a")}`,
		"-- ${c}\nselect ${a}, 1 from ${ref(\"b\")} | ${a} ${ref(\"b\")}",
	}
	// The whitespace runs between blocks are parts too; they hold no SQL.
	var sql []string
	for _, g := range got {
		if !strings.HasPrefix(g, " |") {
			sql = append(sql, g)
		}
	}
	if len(sql) != len(want) || sql[0] != want[0] || sql[1] != want[1] {
		t.Errorf("got %q want %q", sql, want)
	}
}

func TestSQLPartsRefusesWhatIsNeverClosed(t *testing.T) {
	for _, src := range []string{"config {\n a: 1", "select ${ref(\"a\")", "pre_operations { select 1"} {
		if _, ok := SQLParts([]byte(src)); ok {
			t.Errorf("%q was accepted", src)
		}
	}
}

func TestQueryPartFindsSubstitutions(t *testing.T) {
	p, ok := QueryPart([]byte("select ${a} -- ${b}\n, ${c}"))
	if !ok || len(p.Islands) != 2 {
		t.Errorf("got %+v, %v", p, ok)
	}
}
