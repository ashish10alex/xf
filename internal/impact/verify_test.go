package impact

import (
	"context"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

func TestSwapTable(t *testing.T) {
	target := project.Target{Database: "p", Schema: "marts", Name: "orders"}
	got, ok := swapTable("SELECT a FROM `p.marts.orders` o JOIN `p`.`marts`.`orders` b USING (a) JOIN `p.marts.orders_old` c USING (a)", target, "SELECT 1 AS a;\n")
	want := "SELECT a FROM (\nSELECT 1 AS a\n) o JOIN (\nSELECT 1 AS a\n) b USING (a) JOIN `p.marts.orders_old` c USING (a)"
	if !ok || got != want {
		t.Errorf("got %q, %v\nwant %q", got, ok, want)
	}
	if _, ok := swapTable("SELECT a FROM `p.marts.customers`", target, "SELECT 1"); ok {
		t.Error("swapped a table the SQL doesn't read")
	}
}

func TestMissingColumn(t *testing.T) {
	for msg, want := range map[string]bool{
		"Unrecognized name: discount at [3:5]":              true,
		"Unrecognized name: DISCOUNT; Did you mean amount?": true,
		"Name discount not found inside o at [1:10]":        true,
		"Unrecognized name: discount_rate at [3:5]":         false, // another column
		"Syntax error: Unexpected keyword FROM at [2:1]":    false,
		"Unrecognized name: amount at [3:5]":                false,
		"Table p:marts.orders was not found in location EU": false,
	} {
		if got := missingColumn(msg, "discount"); got != want {
			t.Errorf("missingColumn(%q) = %v, want %v", msg, got, want)
		}
	}
}

// A changed reader that still names a dropped column is dry run against the
// changed table as the branch builds it: an alias of a new column passes,
// a read of the dropped one doesn't.
func TestRunDryRunsReadersThatStillNameADroppedColumn(t *testing.T) {
	for _, tc := range []struct {
		name, dailySQL, dryRunErr, checked, note string
		sev                                      Severity
	}{
		{"alias", "SELECT amount AS discount FROM `p.marts.orders`", "", CheckedClear, "no longer reads it (dry run passes)", SeverityLow},
		{"read", "SELECT discount FROM `p.marts.orders`", "Unrecognized name: discount at [1:8]", CheckedReads, "changed here, still reads it (dry run fails)", SeverityCritical},
		{"other failure", "SELECT discount FROM `p.marts.orders`", "Access Denied: Table p:src.x", "", "changed here, still names it", SeverityCritical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graph(t, "SELECT order_id, amount FROM src", tc.dailySQL)
			res := &changes.Result{BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
				Diff: changes.Diff{Changed: []changes.Changed{
					{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonSQL}},
					{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
				}}}
			prod := map[string][]bigquery.Field{
				"p.marts.orders": {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
				"p.marts.daily":  {f("discount", "FLOAT")},
			}
			var swapped []string
			deps := Deps{
				Schema: func(_ context.Context, table string) ([]bigquery.Field, error) { return prod[table], nil },
				DryRun: func(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
					switch {
					case strings.Contains(sql, "FROM (\nSELECT order_id, amount FROM src\n)"):
						swapped = append(swapped, sql)
						if tc.dryRunErr != "" {
							return nil, &bigquery.APIError{Status: 400, Message: tc.dryRunErr}
						}
						return &bigquery.DryRun{Schema: []bigquery.Field{f("discount", "FLOAT")}}, nil
					case strings.Contains(sql, "p.marts.orders"):
						return &bigquery.DryRun{Schema: []bigquery.Field{f("discount", "FLOAT")}}, nil
					}
					return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
				},
				Readers: &fakeSource{columnLinks: map[string][]lineage.ColumnLink{
					"p.marts.orders#discount": {{Table: "p.marts.daily", Column: "discount", DependencyType: lineage.Other}},
				}},
			}
			var phases []string
			report, err := Run(context.Background(), res, g, deps, func(p Progress) { phases = append(phases, p.Phase) })
			if err != nil {
				t.Fatal(err)
			}
			if len(swapped) != 1 {
				t.Fatalf("reader dry runs %q, want one with orders swapped for its query", swapped)
			}
			if !strings.Contains(strings.Join(phases, ","), "Dry running readers") {
				t.Errorf("phases %v", phases)
			}
			if len(report.AtRisk) != 1 || len(report.AtRisk[0].Columns) != 1 || len(report.AtRisk[0].Columns[0].Readers) != 1 {
				t.Fatalf("at risk %+v", report.AtRisk)
			}
			col := report.AtRisk[0].Columns[0]
			r := col.Readers[0]
			if r.Checked != tc.checked || ReaderNote(r) != tc.note {
				t.Errorf("reader %+v, note %q; want checked %q, note %q", r, ReaderNote(r), tc.checked, tc.note)
			}
			if sev := ImpactSeverity(col.Change, col.Readers, col.ReadersError); sev != tc.sev {
				t.Errorf("severity %v, want %v", sev, tc.sev)
			}
		})
	}
}

func TestReaderNoteOfARenamedReaderSaysWhatTheDryRunFound(t *testing.T) {
	r := Reader{Table: "p.inventory.new", RenamedFrom: "p.inventory.old", ChangedOnBranch: true, ProbablyUpdated: true, Checked: CheckedClear}
	if got := ReaderNote(r); got != "renamed here, no longer reads it (dry run passes)" {
		t.Error(got)
	}
}
