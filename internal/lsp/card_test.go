package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// fixtureTable is testdata/bigquery/table.json as the client decodes it.
func fixtureTable(t *testing.T) *bigquery.Table {
	t.Helper()
	data, err := os.ReadFile(testdata(t, "bigquery", "table.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Type, Description, Location string
		Schema                      struct{ Fields []bigquery.Field }
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return &bigquery.Table{
		Type: raw.Type, Description: raw.Description, Location: raw.Location,
		LastModified: time.Date(2026, 9, 30, 8, 54, 57, 0, time.UTC), Schema: raw.Schema.Fields,
	}
}

func reportRef(t *testing.T) navigate.TableRef {
	t.Helper()
	a := loadGraph(t, "navigation.json").Actions["demo-project.analytics.dev_report"]
	return navigate.TableRef{Source: navigate.SourceRef, Target: a.Target, Action: a}
}

const reportTitle = "#### [demo-project.analytics.dev_report](https://console.cloud.google.com/bigquery?project=demo-project&ws=!1m5!1m4!4m3!1sdemo-project!2sanalytics!3sdev_report)"

func TestCardFull(t *testing.T) {
	got := card(reportRef(t), fixtureTable(t), nil, 5*time.Second)
	want := reportTitle + `

---

**Description:** Daily report of user events (from BigQuery)

**Type:** table

**Location:** EU

**Partition:** ` + "`DATE(created_at)`" + `

**Last Modified Time:** Sep 30, 2026, 08:54:57 AM UTC

---

| name       | type            | description               |
|------------|-----------------|---------------------------|
| user_id    | STRING REQUIRED | The user who acted        |
| created_at | TIMESTAMP       |                           |
| payload    | RECORD          | Raw event payload         |
| └─ kind    | STRING          | What kind of event it was |
| └─ tags    | STRING REPEATED | Free-form tags \| labels  |`
	if got != want {
		t.Fatalf("got\n%s\n\nwant\n%s", got, want)
	}
}

func TestCardWithoutBigQuery(t *testing.T) {
	ref := reportRef(t)
	tests := []struct {
		name string
		err  error
		tail string
	}{
		{"auth", fmt.Errorf("%w: invalid_rapt", bigquery.ErrAuth), "⚠ **BigQuery metadata unavailable:** no valid Google credentials: invalid_rapt; run `gcloud auth application-default login`"},
		{"timeout", context.DeadlineExceeded, "⚠ **BigQuery metadata unavailable:** timed out after 5s"},
		{"not found", errors.New("Not found: Table demo-project:analytics.dev_report (HTTP 404)"), "⚠ **BigQuery metadata unavailable:** Not found: Table demo-project:analytics.dev_report (HTTP 404)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := card(ref, nil, tt.err, 5*time.Second)
			want := reportTitle + `

---

**Description:** Daily report of user events

**Type:** table

**Partition:** ` + "`DATE(created_at)`" + `

---

| name    | type | description               |
|---------|------|---------------------------|
| user_id |      | The user who acted        |
| payload |      | Raw event payload         |
| └─ kind |      | What kind of event it was |

` + tt.tail
			if got != want {
				t.Fatalf("got\n%s\n\nwant\n%s", got, want)
			}
		})
	}
}

func TestCardRawAndSelf(t *testing.T) {
	raw := navigate.TableRef{Source: navigate.SourceRaw, Target: project.Target{Database: "p", Schema: "d", Name: "t"}}
	title := "#### [p.d.t](https://console.cloud.google.com/bigquery?project=p&ws=!1m5!1m4!4m3!1sp!2sd!3st)"
	if got, want := card(raw, nil, errors.New("Access Denied (HTTP 403)"), time.Second), title+"\n\n---\n\n⚠ **BigQuery metadata unavailable:** Access Denied (HTTP 403)"; got != want {
		t.Errorf("raw id without metadata:\ngot  %q\nwant %q", got, want)
	}
	got := card(raw, &bigquery.Table{Type: "VIEW", Schema: []bigquery.Field{{Name: "a", Type: "INT64"}}}, nil, time.Second)
	if !strings.Contains(got, "**Type:** view") || !strings.Contains(got, "| a    | INT64 |             |") {
		t.Errorf("raw id with metadata:\n%s", got)
	}
	self := reportRef(t)
	self.Source = navigate.SourceSelf
	if got := card(self, nil, nil, time.Second); got != reportTitle {
		t.Errorf("self: %q", got)
	}
}

func TestCardNoSchema(t *testing.T) {
	target := project.Target{Database: "p", Schema: "d", Name: "t"}
	title := "#### [p.d.t](https://console.cloud.google.com/bigquery?project=p&ws=!1m5!1m4!4m3!1sp!2sd!3st)"
	action := func(kind project.ActionKind, hasOutput bool) *project.Action {
		return &project.Action{ID: target.ID(), Target: target, Kind: kind, HasOutput: hasOutput}
	}
	tests := []struct {
		name string
		ref  navigate.TableRef
		tbl  *bigquery.Table
		note string
	}{
		{"operation", navigate.TableRef{Target: target, Action: action(project.KindOperation, false)}, nil,
			"**Type:** operation\n\n_no schema: this operation has no output table (set `hasOutput: true` to give it one)_"},
		{"operation without a project", navigate.TableRef{Target: project.Target{Schema: "d", Name: "t"}, Action: &project.Action{Kind: project.KindOperation, HasOutput: true}}, nil,
			"_no schema: this operation's target names no project to look it up in_"},
		{"ephemeral", navigate.TableRef{Target: target, Action: action(project.KindEphemeral, false)}, nil,
			"**Type:** ephemeral\n\n_no schema: an ephemeral model has no table of its own (it is inlined where it is used)_"},
		{"test", navigate.TableRef{Target: target, Action: action(project.KindTest, false)}, nil,
			"_no schema: a test has no table of its own_"},
		{"no columns", navigate.TableRef{Target: target, Action: action(project.KindOperation, true)}, &bigquery.Table{Type: "TABLE"},
			"**Type:** operation\n\n_no schema: BigQuery has no columns for the table_"},
		{"raw id without columns", navigate.TableRef{Source: navigate.SourceRaw, Target: target}, &bigquery.Table{Type: "VIEW"},
			title + "\n\n---\n\n**Type:** view\n\n_no schema: BigQuery has no columns for the table_"},
		{"no BigQuery", navigate.TableRef{Target: target, Action: action(project.KindTable, false)}, nil,
			"_no schema: no BigQuery access here, and the config documents no columns_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := card(tt.ref, tt.tbl, nil, time.Second); !strings.HasSuffix(got, tt.note) {
				t.Errorf("got\n%s\n\nwant it to end\n%s", got, tt.note)
			}
		})
	}
	// A BigQuery error already says why: no second note.
	if got := card(navigate.TableRef{Target: target, Action: action(project.KindTable, false)}, nil, errors.New("Access Denied (HTTP 403)"), time.Second); strings.Contains(got, "no schema") {
		t.Errorf("note beside an error:\n%s", got)
	}
}

func TestSchemaTableLimits(t *testing.T) {
	var fields []bigquery.Field
	for i := range maxSchemaRows + 3 {
		fields = append(fields, bigquery.Field{Name: fmt.Sprintf("c%03d", i), Type: "STRING"})
	}
	fields[0].Description = strings.Repeat("x", 100)
	fields[0].Fields = []bigquery.Field{{Name: "mid", Type: "RECORD", Fields: []bigquery.Field{{Name: "leaf", Type: "INT64"}}}}
	got := schemaTable(schemaRows(fields, nil))
	lines := strings.Split(got, "\n")
	tableLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "|") {
			tableLines++
		}
	}
	if tableLines != maxSchemaRows+2 {
		t.Errorf("%d table lines, want %d rows plus header and separator", tableLines, maxSchemaRows)
	}
	if !strings.HasSuffix(got, "_… 5 more fields not shown_") {
		t.Errorf("tail %q", lines[len(lines)-1])
	}
	if !strings.Contains(lines[2], strings.Repeat("x", maxCellRunes-1)+"… |") {
		t.Errorf("long description not cut: %q", lines[2])
	}
	if !strings.Contains(lines[4], "|    └─ leaf ") {
		t.Errorf("second level not indented: %q", lines[4])
	}
	if got := schemaTable(schemaRows(fields[:maxSchemaRows+1], nil)); !strings.HasSuffix(got, "_… 3 more fields not shown_") {
		t.Errorf("singular/plural tail: %q", got[len(got)-40:])
	}
}
