package navigate

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
)

func TestCompletions(t *testing.T) {
	p := navProject(t)
	sqlx := filepath.Join(p.Root, "definitions", "new.sqlx")
	tests := []struct {
		name string
		file string
		// text with | at the cursor
		text      string
		wantKind  CompletionKind
		wantItems []string // label -> insert, in order; nil for no completions
		wantStart string   // the text Start points at
	}{
		{
			name:     "dollar in SQL",
			file:     sqlx,
			text:     "config { type: \"view\" }\nSELECT * FROM $st|",
			wantKind: CompletionRef,
			wantItems: []string{
				`events -> ${ref("events")}`,
				`js_view -> ${ref("js_view")}`,
				`report -> ${ref("report")}`,
				`stg_events -> ${ref("stg_events")}`,
			},
			wantStart: "$st",
		},
		{
			name:      "dependencies",
			file:      sqlx,
			text:      "config {\n  type: \"operations\",\n  dependencies: [\"report\", \"stg|\n}",
			wantKind:  CompletionDependency,
			wantItems: []string{"events -> events", "js_view -> js_view", "report -> report", "stg_events -> stg_events"},
			wantStart: "stg",
		},
		{
			name:      "tags",
			file:      filepath.Join(testdata(t, "projects", "basic"), "definitions", "x.sqlx"),
			text:      "config { tags: ['|'] }",
			wantKind:  CompletionTag,
			wantItems: []string{"daily -> daily", "staging -> staging"},
			wantStart: "",
		},
		{name: "dollar inside a placeholder", file: sqlx, text: "SELECT ${ $|}", wantItems: nil},
		{name: "dollar in a comment", file: sqlx, text: "-- costs $|", wantItems: nil},
		{name: "dollar before a brace", file: sqlx, text: "SELECT $|{", wantItems: nil},
		{
			name:     "a config's type",
			file:     sqlx,
			text:     "config { type: \"in|\" }",
			wantKind: CompletionConfigValue,
			wantItems: []string{
				"table -> table", "view -> view", "incremental -> incremental",
				"operations -> operations", "assertion -> assertion", "declaration -> declaration",
			},
			wantStart: "in",
		},
		{name: "a string of free text", file: sqlx, text: "config { description: \"|\" }", wantItems: nil},
		{
			name:      "bigquery keys of a view",
			file:      sqlx,
			text:      "config {\n  type: \"view\",\n  bigquery: {\n    labels: {},\n    p|\n  }\n}",
			wantKind:  CompletionConfigKey,
			wantItems: []string{"partitionBy -> partitionBy: ", "clusterBy -> clusterBy: ", "additionalOptions -> additionalOptions: "},
			wantStart: "p",
		},
		{name: "javascript", file: filepath.Join(p.Root, "definitions", "x.js"), text: "const a = `$|`", wantItems: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := p
			if strings.Contains(tt.file, "basic") {
				proj = Project{Root: testdata(t, "projects", "basic"), Graph: loadGraph(t, "basic.json")}
			}
			bar := strings.IndexByte(tt.text, '|')
			src := []byte(tt.text[:bar] + tt.text[bar+1:])
			got := Completions(proj, tt.file, src, bar)
			var items []string
			for _, c := range got {
				items = append(items, c.Label+" -> "+c.InsertText)
				if c.Kind != tt.wantKind {
					t.Errorf("%s: kind %v, want %v", c.Label, c.Kind, tt.wantKind)
				}
			}
			if !slices.Equal(items, tt.wantItems) {
				t.Fatalf("got %q\nwant %q", items, tt.wantItems)
			}
			if len(got) > 0 {
				if start := string(src[got[0].Start:bar]); start != tt.wantStart {
					t.Errorf("replaces %q, want %q", start, tt.wantStart)
				}
			}
		})
	}
}

func TestCompletionsAmbiguousName(t *testing.T) {
	g, err := dataform.ParseCompileJSON([]byte(`{"tables": [
		{"type": "table", "target": {"database": "p", "schema": "b", "name": "t"}, "fileName": "definitions/b/t.sqlx"},
		{"type": "view", "target": {"database": "p", "schema": "a", "name": "t"}, "fileName": "definitions/a/t.sqlx"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	src := []byte("SELECT * FROM $")
	var got []string
	for _, c := range Completions(Project{Root: "/p", Graph: g}, "/p/definitions/x.sqlx", src, len(src)) {
		got = append(got, c.Detail+" -> "+c.InsertText+" filter "+c.FilterText)
	}
	want := []string{`a · view -> ${ref("a", "t")} filter $t`, `b · table -> ${ref("b", "t")} filter $t`}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
