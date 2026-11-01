package navigate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
