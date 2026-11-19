package impact

import (
	"context"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
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

// A subquery has no implicit alias, so a reference without one gets the
// table's name.
func TestSwapTableKeepsTheImplicitAlias(t *testing.T) {
	target := project.Target{Database: "p", Schema: "marts", Name: "orders"}
	for _, c := range []struct{ sql, want string }{
		{"SELECT orders.id FROM `p.marts.orders` WHERE orders.id > 0", "SELECT orders.id FROM (\nSELECT 1 AS id\n) AS `orders` WHERE orders.id > 0"},
		{"SELECT x.id FROM `p.marts.orders` AS x", "SELECT x.id FROM (\nSELECT 1 AS id\n) AS x"},
		{"SELECT * FROM `p.marts.orders`\nJOIN y USING (id)", "SELECT * FROM (\nSELECT 1 AS id\n) AS `orders`\nJOIN y USING (id)"},
	} {
		if got, ok := swapTable(c.sql, target, "SELECT 1 AS id"); !ok || got != c.want {
			t.Errorf("swapTable(%q) = %q, %v\nwant %q", c.sql, got, ok, c.want)
		}
	}
}

// A reference in a comment or a string isn't swapped, and while one written
// another way is left there is no telling.
func TestSwapTableSwapsCodeOnly(t *testing.T) {
	target := project.Target{Database: "p", Schema: "marts", Name: "orders"}
	if got, ok := swapTable("/* `p.marts.orders` */ SELECT discount FROM p.marts.orders", target, "SELECT 1 AS a"); ok {
		t.Errorf("swapped %q: the table read is unquoted", got)
	}
	sql := "SELECT '`p.marts.orders`' AS t, discount FROM `p.marts.orders` o -- `p.marts.orders`"
	want := "SELECT '`p.marts.orders`' AS t, discount FROM (\nSELECT 1 AS a\n) o -- `p.marts.orders`"
	if got, ok := swapTable(sql, target, "SELECT 1 AS a"); !ok || got != want {
		t.Errorf("got %q, %v\nwant %q", got, ok, want)
	}
	if got, ok := swapTable("SELECT * FROM `p.marts.orders` a JOIN marts.orders b USING (id)", target, "SELECT 1 AS id"); ok {
		t.Errorf("swapped %q: marts.orders is still read", got)
	}
	in := "a -- b\nc /* d */ e 'f\\'g' \"\"\"h\"\"\" `i` # j"
	if got, want := codeOnly(in), "a     \nc"+strings.Repeat(" ", 9)+"e"+strings.Repeat(" ", 16)+"`i`"+strings.Repeat(" ", 4); got != want {
		t.Errorf("codeOnly = %q, want %q", got, want)
	}
}

// BigQuery doesn't check what follows a CALL in a script it dry runs, so a
// reader that CALLs a procedure first is dry run by its query alone.
func TestCheckReaderDryRunsTheQueryAloneAfterACall(t *testing.T) {
	g := graph(t, "SELECT order_id FROM src", "SELECT discount FROM `p.marts.orders`")
	daily := g.Actions["p.marts.daily"]
	for _, c := range []struct {
		preOps []string
		call   bool
	}{
		{[]string{"CALL `p.marts.refresh`()"}, true},
		{[]string{"DECLARE x INT64 DEFAULT 1", "-- CALL nothing"}, false},
	} {
		daily.PreOps = c.preOps
		var ran []string
		deps := Deps{DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
			ran = append(ran, sql)
			return &warehouse.Check{}, nil
		}}
		if got := checkReader(context.Background(), "p.marts.daily", "discount", g.Actions["p.marts.orders"], g, deps); got != CheckedClear {
			t.Errorf("%v: checked %q", c.preOps, got)
		}
		if len(ran) != 1 || strings.Contains(ran[0], c.preOps[0]) == c.call {
			t.Errorf("%v: dry ran %q, want the pre-operations in it %v", c.preOps, ran, !c.call)
		}
	}
}

// On a warehouse that bills its checks (Databricks), a changed model is
// checked by its query alone, without its hooks, and its readers aren't dry
// run; the report says which warehouse was used, and where readers came
// from.
func TestRunOnDemand(t *testing.T) {
	g := graph(t, "SELECT order_id, amount FROM src", "SELECT discount FROM `p.marts.orders`")
	g.Actions["p.marts.orders"].PreOps = []string{"SET spark.sql.ansi.enabled = true"}
	res := &changes.Result{BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{Changed: []changes.Changed{
			{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonSQL}},
			{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
		}}}
	prod := map[string][]warehouse.Column{
		"p.marts.orders": {f("order_id", "bigint"), f("amount", "double"), f("discount", "decimal(10,2)")},
		"p.marts.daily":  {f("discount", "decimal(10,2)")},
	}
	var planned []string
	deps := Deps{
		OnDemand: true,
		Schema:   func(_ context.Context, table string) ([]warehouse.Column, error) { return prod[table], nil },
		DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
			planned = append(planned, sql)
			if strings.Contains(sql, "discount") {
				return &warehouse.Check{Schema: []warehouse.Column{f("discount", "decimal(10,2)")}}, nil
			}
			return &warehouse.Check{Schema: []warehouse.Column{f("order_id", "bigint"), f("amount", "double")}}, nil
		},
		Readers: &fakeSource{columnLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders#discount": {{Table: "p.marts.daily", Column: "discount", DependencyType: lineage.Other}},
		}},
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 2 {
		t.Fatalf("planned %q, want the two changed models alone", planned)
	}
	for _, sql := range planned {
		if strings.Contains(sql, "SET ") {
			t.Errorf("planned a hook: %q", sql)
		}
	}
	if len(report.AtRisk) != 1 || report.AtRisk[0].Columns[0].Readers[0].Checked != "" {
		t.Fatalf("at risk %+v", report.AtRisk)
	}
	report.Warehouse = &WarehouseUse{Name: "Databricks SQL warehouse `Starter`", Started: true, Lineage: "Unity Catalog"}
	md := Markdown(report)
	if !strings.Contains(md, "checked on the Databricks SQL warehouse `Starter`, which this check started") || !strings.Contains(md, "Readers come from Unity Catalog lineage") {
		t.Errorf("markdown:\n%s", md)
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
			prod := map[string][]warehouse.Column{
				"p.marts.orders": {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
				"p.marts.daily":  {f("discount", "FLOAT")},
			}
			var swapped []string
			deps := Deps{
				Schema: func(_ context.Context, table string) ([]warehouse.Column, error) { return prod[table], nil },
				DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
					switch {
					case strings.Contains(sql, "FROM (\nSELECT order_id, amount FROM src\n)"):
						swapped = append(swapped, sql)
						if tc.dryRunErr != "" {
							return nil, &warehouse.APIError{Status: 400, Message: tc.dryRunErr}
						}
						return &warehouse.Check{Schema: []warehouse.Column{f("discount", "FLOAT")}}, nil
					case strings.Contains(sql, "p.marts.orders"):
						return &warehouse.Check{Schema: []warehouse.Column{f("discount", "FLOAT")}}, nil
					}
					return &warehouse.Check{Schema: []warehouse.Column{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
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
