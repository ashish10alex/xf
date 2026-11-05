package navigate

import (
	"slices"
	"strings"
	"testing"
)

// configLabels completes the config text with | at the cursor, the
// project not compiled.
func configLabels(t *testing.T, text string) (labels []string, replaced string) {
	t.Helper()
	bar := strings.IndexByte(text, '|')
	src := []byte(text[:bar] + text[bar+1:])
	got := Completions(Project{Root: "/p"}, "/p/definitions/x.sqlx", src, bar)
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	if len(got) > 0 {
		replaced = string(src[got[0].Start:bar])
	}
	return labels, replaced
}

func TestConfigKeys(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string // nil for none; a leading "+" lists only some labels that must be there
	}{
		{
			name: "an incremental table's own keys, those written left out",
			text: "config {\n  type: \"incremental\",\n  |\n  schema: \"x\",\n}",
			want: []string{"type?", "name", "database", "description", "tags", "dependencies", "columns", "bigquery", "assertions", "uniqueKey",
				"protected", "onSchemaChange", "incrementalStrategy", "disabled", "dependOnDependencyAssertions", "metadata", "reservation", "hermetic"},
		},
		{
			name: "a declaration's",
			text: "config { type: \"declaration\", | }",
			want: []string{"schema", "name", "database", "description", "columns"},
		},
		{
			name: "the first key, type unknown, every key",
			text: "config {\n  |\n}",
			want: []string{"+", "type", "materialized", "hasOutput", "uniqueKey"},
		},
		{
			name: "a table's bigquery keys",
			text: "config { type: \"table\", bigquery: { partitionBy: \"DATE(ts)\", | } }",
			want: []string{"clusterBy", "requirePartitionFilter", "partitionExpirationDays", "labels", "additionalOptions", "iceberg"},
		},
		{
			name: "assertions",
			text: "config { type: \"table\", assertions: {\n  u| } }",
			want: []string{"uniqueKey", "uniqueKeys", "nonNull", "rowConditions"},
		},
		{
			name: "iceberg, in bigquery",
			text: "config { type: \"table\", bigquery: { iceberg: { | } } }",
			want: []string{"connection", "bucketName", "tableFolderRoot", "tableFolderSubpath", "fileFormat"},
		},
		{
			name: "a quoted key counts as written",
			text: "config { \"type\": \"view\", \"bigquery\": { | } }",
			want: []string{"partitionBy", "clusterBy", "labels", "additionalOptions"},
		},
		{
			name: "on the line after the last value, its comma not written yet",
			text: "config {\n  type: \"view\",\n  bigquery: {\n    partitionBy: \"d\"\n    c|\n  }\n}",
			want: []string{"clusterBy", "labels", "additionalOptions"},
		},
		{name: "a value", text: "config { type: \"table\", schema: x| }", want: nil},
		{name: "a value on the line after its colon", text: "config { type: \"table\", schema:\n  x| }", want: nil},
		{name: "after a value on its line", text: "config { type: \"table\" s| }", want: nil},
		{name: "after a key's colon", text: "config { type: | }", want: nil},
		{name: "in an array", text: "config { tags: [ | ] }", want: nil},
		{name: "in a comment", text: "config { // |\n }", want: nil},
		{name: "in a labels object", text: "config { bigquery: { labels: { | } } }", want: nil},
		{name: "in an object no key holds", text: "config { x: [{ | }] }", want: nil},
		{name: "in SQL", text: "config { type: \"view\" }\nSELECT |", want: nil},
		{
			name: "onSchemaChange's values",
			text: "config { type: \"incremental\", onSchemaChange: \"|\" }",
			want: []string{"IGNORE", "FAIL", "EXTEND", "SYNCHRONIZE"},
		},
		{
			name: "iceberg's fileFormat",
			text: "config { bigquery: { iceberg: { fileFormat: '|' } } }",
			want: []string{"PARQUET"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := configLabels(t, tt.text)
			want := tt.want
			if len(want) > 0 && want[0] == "+" {
				for _, w := range want[1:] {
					if !slices.Contains(got, w) {
						t.Errorf("%s missing from %q", w, got)
					}
				}
				return
			}
			want = slices.DeleteFunc(slices.Clone(want), func(s string) bool { return strings.HasSuffix(s, "?") })
			if !slices.Equal(got, want) {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}

func TestConfigKeyInsert(t *testing.T) {
	bar := "config {\n  type: \"table\",\n  bigq|\n}"
	src := []byte(strings.Replace(bar, "|", "", 1))
	got := Completions(Project{Root: "/p"}, "/p/definitions/x.sqlx", src, strings.IndexByte(bar, '|'))
	i := slices.IndexFunc(got, func(c Completion) bool { return c.Label == "bigquery" })
	if i < 0 || got[i].InsertText != "bigquery: " || string(src[got[i].Start:strings.IndexByte(bar, '|')]) != "bigq" {
		t.Fatalf("got %+v", got)
	}
	// Renaming a key keeps its colon.
	bar = "config { sch|: \"x\" }"
	src = []byte(strings.Replace(bar, "|", "", 1))
	got = Completions(Project{Root: "/p"}, "/p/definitions/x.sqlx", src, strings.IndexByte(bar, '|'))
	if len(got) == 0 || got[0].InsertText != got[0].Label {
		t.Fatalf("got %+v", got)
	}
}
