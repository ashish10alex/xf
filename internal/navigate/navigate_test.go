package navigate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/project"
)

func testdata(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(testdata(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	data, err = dataform.ExtractCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// navProject is the navigation fixture project with its compiled graph.
func navProject(t *testing.T) Project {
	t.Helper()
	return Project{Root: testdata(t, "projects", "navigation"), Graph: loadGraph(t, "navigation.json")}
}

// cursorAt finds needle (with a | marking the cursor) in src and returns the
// cursor's byte offset.
func cursorAt(t *testing.T, src []byte, needle string) int {
	t.Helper()
	bar := strings.IndexByte(needle, '|')
	if bar < 0 {
		t.Fatalf("needle %q has no | cursor", needle)
	}
	plain := needle[:bar] + needle[bar+1:]
	i := strings.Index(string(src), plain)
	if i < 0 {
		t.Fatalf("needle %q not found", plain)
	}
	if strings.Count(string(src), plain) > 1 {
		t.Fatalf("needle %q is ambiguous", plain)
	}
	return i + bar
}

// define runs Definition on a project file with the cursor at needle and
// renders each result as "relative/path:line:col".
func define(t *testing.T, p Project, file string, src []byte, needle string) []string {
	t.Helper()
	path := filepath.Join(p.Root, file)
	if src == nil {
		var err error
		if src, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for _, l := range Definition(p, path, src, cursorAt(t, src, needle)) {
		rel, err := filepath.Rel(p.Root, l.File)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), l.Line, l.Col))
	}
	return out
}

const (
	stgEvents = "definitions/staging/stg_events.sqlx"
	report    = "definitions/marts/report.sqlx"
	sourcesJS = "definitions/sources.js"
)

func TestDefinitionRefs(t *testing.T) {
	p := navProject(t)
	tests := []struct {
		name   string
		file   string
		src    string // unsaved buffer text; empty reads the file
		needle string
		want   []string
	}{
		{"name only, prefixed target", report, "", `ref("stg_ev|ents")} e`, []string{stgEvents + ":0:0"}},
		{"cursor on ref keyword", report, "", `r|ef("stg_events")} e`, []string{stgEvents + ":0:0"}},
		{"schema and name", report, "", `ref("analytics", "stg_|events")`, []string{stgEvents + ":0:0"}},
		{"object form", report, "", `ref({schema: "analytics", name: "stg_|events"})`, []string{stgEvents + ":0:0"}},
		{"action defined in js lands on its publish line", report, "", `ref("js_v|iew")`, []string{sourcesJS + ":2:9"}},
		{"declaration in js lands on its declare line", stgEvents, "", `ref("ev|ents")`, []string{sourcesJS + ":0:57"}},
		{"ctx.ref in a js file", sourcesJS, "", `ctx.ref("ev|ents")`, []string{sourcesJS + ":0:57"}},
		{"unsaved text", report, `SELECT 1 FROM ${ref("report")}`, `ref("rep|ort")`, []string{report + ":0:0"}},
		{"wrong schema finds nothing", report, `SELECT 1 FROM ${ref("other", "stg_events")}`, `ref("other", "stg_|events")`, nil},
		{"cursor outside the call", report, "", `ref("stg_events")} |e`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var src []byte
			if tt.src != "" {
				src = []byte(tt.src)
			}
			got := define(t, p, tt.file, src, tt.needle)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefinitionRefWithoutGraph(t *testing.T) {
	p := navProject(t)
	p.Graph = nil
	if got := define(t, p, report, nil, `ref("stg_ev|ents")} e`); got != nil {
		t.Fatalf("got %v, want nothing without a graph", got)
	}
}

func TestDefinitionRefAmbiguousName(t *testing.T) {
	g, err := dataform.ParseCompileJSON([]byte(`{"tables": [
		{"type": "table", "target": {"database": "p", "schema": "b", "name": "t"}, "fileName": "definitions/b/t.sqlx"},
		{"type": "table", "target": {"database": "p", "schema": "a", "name": "t"}, "fileName": "definitions/a/t.sqlx"},
		{"type": "table", "target": {"database": "p", "schema": "a", "name": "u"}, "fileName": "definitions/a/u.sqlx"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := Project{Root: "/proj", Graph: g}
	src := []byte(`SELECT * FROM ${ref("t")}`)
	got := Definition(p, "/proj/definitions/x.sqlx", src, strings.Index(string(src), `"t"`))
	want := []Location{{File: filepath.FromSlash("/proj/definitions/a/t.sqlx")}, {File: filepath.FromSlash("/proj/definitions/b/t.sqlx")}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseRefArgs(t *testing.T) {
	tests := []struct {
		in   string
		want refTarget
		ok   bool
	}{
		{`"a"`, refTarget{Name: "a"}, true},
		{`'a'`, refTarget{Name: "a"}, true},
		{"`a`", refTarget{Name: "a"}, true},
		{`"s", "a"`, refTarget{Schema: "s", Name: "a"}, true},
		{`"d", "s", "a"`, refTarget{Database: "d", Schema: "s", Name: "a"}, true},
		{`{ name: "a", schema: 's' }`, refTarget{Schema: "s", Name: "a"}, true},
		{`{ schema: "s" }`, refTarget{}, false},
		{`tableName`, refTarget{}, false},
		{``, refTarget{}, false},
	}
	for _, tt := range tests {
		got, ok := parseRefArgs(tt.in)
		if ok != tt.ok || ok && got != tt.want {
			t.Errorf("parseRefArgs(%s) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestJSSpans(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // the text of each span
	}{
		{"placeholders", `SELECT ${a} FROM ${ref("t")}`, []string{"a", `ref("t")`}},
		{"config and js blocks", "config { type: \"view\" }\njs { const x = 1; }\nSELECT ${x}", []string{` type: "view" `, ` const x = 1; `, "x"}},
		{"brace inside a string", `config { name: "a}b" } SELECT 1`, []string{` name: "a}b" `}},
		{"nested template substitution", "SELECT ${`a${b}c`} x", []string{"`a${b}c`"}},
		{"sql comments are skipped", "-- ${nope}\n/* ${nope} */ SELECT ${yes}", []string{"yes"}},
		{"keyword inside an identifier", "SELECT myjs {x} FROM t", nil},
		{"unterminated placeholder runs to the end", "SELECT ${ref(", []string{"ref("}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, s := range jsSpans([]byte(tt.src)) {
				got = append(got, tt.src[s.start:s.end])
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefinitionCTEsAndRequire(t *testing.T) {
	p := navProject(t)
	tests := []struct {
		name   string
		file   string
		src    string // unsaved buffer text; empty reads the file
		needle string
		want   []string
	}{
		{"cte used in a later cte", stgEvents, "", "FROM filt|ered GROUP", []string{stgEvents + ":7:5"}},
		{"cte used in the main query", stgEvents, "", "FROM coun|ted", []string{stgEvents + ":11:0"}},
		{"backtick quoted cte", report, "WITH `my cte` AS (SELECT 1) SELECT * FROM `my cte`", "FROM `my c|te`", []string{report + ":0:6"}},
		{"a column is not a cte", stgEvents, "", "SELECT user_|id", nil},
		{"require from the project root", stgEvents, "", `require("includes/li|b/extra")`, []string{"includes/lib/extra.js:0:0"}},
		{"relative require", "includes/x.js", `const h = require("./helpers");`, `require("./hel|pers")`, []string{"includes/helpers.js:0:0"}},
		{"require of a missing file", "includes/x.js", `const h = require("./nope");`, `require("./no|pe")`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var src []byte
			if tt.src != "" {
				src = []byte(tt.src)
			}
			got := define(t, p, tt.file, src, tt.needle)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
