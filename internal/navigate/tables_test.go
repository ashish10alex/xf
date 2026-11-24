package navigate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func TestTableRefsAt(t *testing.T) {
	p := navProject(t)
	tests := []struct {
		name       string
		file       string
		src        string // unsaved buffer text; empty reads the file
		needle     string
		wantIDs    []string
		wantSource TableRefSource
		wantAction bool // every result carries its action
		wantText   string
	}{
		{"ref", report, "", `ref("stg_ev|ents")} e`, []string{"demo-project.analytics.dev_stg_events"}, SourceRef, true, `ref("stg_events")`},
		{"ref to a declaration", stgEvents, "", `ref("ev|ents")`, []string{"raw-project.raw.events"}, SourceRef, true, `ref("events")`},
		{"raw id that is a declaration", report, "", "raw-project.raw.ev|ents r", []string{"raw-project.raw.events"}, SourceRaw, true, "raw-project.raw.events"},
		{"raw id outside the project", report, "SELECT * FROM `other-proj.sales.orders`", "sales.or|ders", []string{"other-proj.sales.orders"}, SourceRaw, false, "other-proj.sales.orders"},
		{"self", report, "", "${se|lf()}", []string{"demo-project.analytics.dev_report"}, SourceSelf, true, "self()"},
		{"raw id in a comment", report, "-- see raw-project.raw.events", "raw.ev|ents", nil, 0, false, ""},
		{"raw id inside a placeholder", report, `SELECT ${"a.b.c"}`, `a.|b.c`, nil, 0, false, ""},
		{"plain column", report, "", "USING (us|er_id)\nJOIN ${ref(\"js_view\")}", nil, 0, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(p.Root, tt.file)
			src := []byte(tt.src)
			if tt.src == "" {
				var err error
				if src, err = os.ReadFile(path); err != nil {
					t.Fatal(err)
				}
			}
			got := TableRefsAt(p, path, src, cursorAt(t, src, tt.needle))
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %+v, want %v", got, tt.wantIDs)
			}
			for i, r := range got {
				if string(r.Target.ID()) != tt.wantIDs[i] || r.Source != tt.wantSource || (r.Action != nil) != tt.wantAction {
					t.Errorf("got %+v", r)
				}
				if text := string(src[r.Start:r.End]); text != tt.wantText {
					t.Errorf("range covers %q, want %q", text, tt.wantText)
				}
			}
		})
	}
}

func TestTableRefsAtWithoutGraph(t *testing.T) {
	p := navProject(t)
	p.Graph = nil
	src := []byte("SELECT * FROM ${ref(\"stg_events\")} JOIN a.b.c ${self()}")
	for _, needle := range []string{`stg_|events`, `${se|lf`} {
		if got := TableRefsAt(p, filepath.Join(p.Root, report), src, cursorAt(t, src, needle)); got != nil {
			t.Errorf("%s: got %+v without a graph", needle, got)
		}
	}
	if got := TableRefsAt(p, filepath.Join(p.Root, report), src, cursorAt(t, src, "a.|b.c")); len(got) != 1 || got[0].Action != nil {
		t.Errorf("raw id without a graph: %+v", got)
	}
}

func TestTableRefs(t *testing.T) {
	p := navProject(t)
	path := filepath.Join(p.Root, report)
	src := []byte("SELECT * FROM ${ref(\"stg_events\")} e\n-- raw-project.raw.events\nJOIN raw-project.raw.events r USING (id)\nJOIN ${ref(\"stg_events\")} again\nWHERE x > (SELECT 1 FROM ${self()})")
	got := TableRefs(p, path, src)
	var texts, ids []string
	for _, r := range got {
		texts = append(texts, string(src[r.Start:r.End]))
		ids = append(ids, string(r.Target.ID()))
	}
	wantTexts := []string{`ref("stg_events")`, "raw-project.raw.events", `ref("stg_events")`, "self()"}
	wantIDs := []string{"demo-project.analytics.dev_stg_events", "raw-project.raw.events", "demo-project.analytics.dev_stg_events", "demo-project.analytics.dev_report"}
	if !slices.Equal(texts, wantTexts) || !slices.Equal(ids, wantIDs) {
		t.Errorf("TableRefs = %q %q, want %q %q", texts, ids, wantTexts, wantIDs)
	}
}

func TestTableRefsDbt(t *testing.T) {
	p := dbtProjects(t)["dbt-core"]
	path := filepath.Join(p.Root, "models", "x.sql")
	src := []byte("select * from {{ ref('stg_orders') }} o join {{ source('raw', 'customers') }} c on true\nunion all select * from {{ this }}")
	var texts []string
	for _, r := range TableRefs(p, path, src) {
		texts = append(texts, string(src[r.Start:r.End]))
	}
	if len(texts) < 2 || texts[0] != "ref('stg_orders')" || texts[1] != "source('raw', 'customers')" {
		t.Errorf("TableRefs = %q", texts)
	}
}

// tableRefsByOffset is TableRefs built from TableRefsAt the way TableRefs
// first did it, with the file scanned again for each offset: the reference
// TableRefs must still match.
func tableRefsByOffset(p Project, file string, src []byte) []TableRef {
	var syms []Symbol
	var selves [][]int
	if p.Dbt {
		syms = DbtDocumentSymbols(file, src)
		selves = thisWord.FindAllIndex(src, -1)
	} else {
		syms = DocumentSymbols(file, src)
		selves = selfCall.FindAllIndex(src, -1)
	}
	type key struct {
		start int
		id    project.ActionID
	}
	seen := map[key]bool{}
	var out []TableRef
	add := func(offset int) {
		for _, r := range TableRefsAt(p, file, src, offset) {
			k := key{r.Start, r.Target.ID()}
			if !seen[k] {
				seen[k] = true
				out = append(out, r)
			}
		}
	}
	for _, s := range ReferencesFirst(syms) {
		if s.Kind == SymbolRef || s.Kind == SymbolTable {
			add((s.Start + s.End) / 2)
		}
	}
	for _, m := range selves {
		add(m[0])
	}
	slices.SortStableFunc(out, func(a, b TableRef) int { return a.Start - b.Start })
	return out
}

// plainLines is how many lines of plain SQL bigSqlx and bigDbt put after each
// line that may hold a ref.
const plainLines = 9

// bigSqlx is a Dataform file of about n lines with refs, raw ids, comments,
// self() and JavaScript.
func bigSqlx(n int) []byte {
	var b strings.Builder
	b.WriteString("config { type: \"table\", description: \"p.q.r\" }\njs { const t = \"js.only.table\"; }\nWITH\n")
	lines := []string{
		`  a%d AS (SELECT * FROM ${ref("stg_events")} e`,
		`  JOIN raw-project.raw.events r USING (id)`,
		`  -- raw-project.raw.events in a comment %d`,
		`  /* other-proj.sales.orders %d */`,
		`  JOIN ${ref("analytics", "stg_events")} USING (id)`,
		`  JOIN ${ref({name: "events"})} USING (id)`,
		`  WHERE x > (SELECT 1 FROM ${self()}))`,
		`  , b%d AS (SELECT ${"in.a.placeholder"} FROM other-proj.sales.orders`,
		`  JOIN ${resolve("report")} USING (id) // c.d.e`,
		`  JOIN ${ref("missing_%d")} USING (id)),`,
	}
	for i := 0; strings.Count(b.String(), "\n") < n; i++ {
		line := lines[i%len(lines)]
		if strings.Contains(line, "%d") {
			line = fmt.Sprintf(line, i)
		}
		b.WriteString(line + "\n")
		for range plainLines {
			b.WriteString("    , col_a, col_b + 1 AS col_c, CAST(col_d AS STRING) AS col_e\n")
		}
	}
	b.WriteString("  z AS (SELECT 1)\nSELECT * FROM z")
	return []byte(b.String())
}

// bigDbt is a dbt model of about n lines with ref(), source(), {{ this }},
// raw ids, comments and tags.
func bigDbt(n int) []byte {
	var b strings.Builder
	b.WriteString("{{ config(materialized='table') }}\nwith\n")
	lines := []string{
		`  a%d as (select * from {{ ref('stg_orders') }} o`,
		`  join {{ source('raw', 'customers') }} c on true`,
		`  join raw-project.raw.events r using (id)`,
		`  -- other-proj.sales.orders %d`,
		`  {# in.a.comment #}`,
		`  {%% set t = 'in.a.tag' %%}`,
		`  union all select * from {{ this }}`,
		`  join {{ ref('missing_%d') }} using (id)),`,
	}
	for i := 0; strings.Count(b.String(), "\n") < n; i++ {
		line := lines[i%len(lines)]
		if strings.Contains(line, "%") {
			line = fmt.Sprintf(line, i)
		}
		b.WriteString(line + "\n")
		for range plainLines {
			b.WriteString("    , col_a, col_b + 1 as col_c, cast(col_d as string) as col_e\n")
		}
	}
	b.WriteString("  z as (select 1)\nselect * from z")
	return []byte(b.String())
}

func TestTableRefsMatchesTableRefsAt(t *testing.T) {
	nav := navProject(t)
	dbtP := dbtProjects(t)["dbt-core"]
	read := func(rel string) []byte {
		src, err := os.ReadFile(filepath.Join(nav.Root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	tests := []struct {
		name string
		p    Project
		file string
		src  []byte
	}{
		{"TestTableRefs fixture", nav, filepath.Join(nav.Root, report), []byte("SELECT * FROM ${ref(\"stg_events\")} e\n-- raw-project.raw.events\nJOIN raw-project.raw.events r USING (id)\nJOIN ${ref(\"stg_events\")} again\nWHERE x > (SELECT 1 FROM ${self()})")},
		{"report", nav, filepath.Join(nav.Root, report), read(report)},
		{"stg_events", nav, filepath.Join(nav.Root, stgEvents), read(stgEvents)},
		{"generated sqlx", nav, filepath.Join(nav.Root, report), bigSqlx(2000)},
		{"generated sqlx without a graph", Project{Root: nav.Root}, filepath.Join(nav.Root, report), bigSqlx(200)},
		{"TestTableRefsDbt fixture", dbtP, filepath.Join(dbtP.Root, "models", "x.sql"), []byte("select * from {{ ref('stg_orders') }} o join {{ source('raw', 'customers') }} c on true\nunion all select * from {{ this }}")},
		{"generated dbt", dbtP, filepath.Join(dbtP.Root, "models", "x.sql"), bigDbt(2000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TableRefs(tt.p, tt.file, tt.src)
			want := tableRefsByOffset(tt.p, tt.file, tt.src)
			if len(want) == 0 {
				t.Fatal("the fixture finds no refs")
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("TableRefs gave %d refs, TableRefsAt %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
			}
		})
	}
}

func BenchmarkTableRefs(b *testing.B) {
	nav := navProject(b)
	dbtP := dbtProjects(b)["dbt-core"]
	for _, bc := range []struct {
		name string
		p    Project
		file string
		src  []byte
	}{
		{"sqlx", nav, filepath.Join(nav.Root, report), bigSqlx(2000)},
		{"dbt", dbtP, filepath.Join(dbtP.Root, "models", "x.sql"), bigDbt(2000)},
	} {
		b.Run(bc.name, func(b *testing.B) {
			for b.Loop() {
				TableRefs(bc.p, bc.file, bc.src)
			}
		})
	}
}
