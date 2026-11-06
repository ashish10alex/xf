package impact

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// These port the extension's suites: "Column impact: schema diff"
// (columnImpactRules.test.ts) and "Column impact summary"
// (columnImpactSummary.test.ts). Keep them in step.

func f(name, typ string) bigquery.Field { return bigquery.Field{Name: name, Type: typ} }

func TestDiffSchemas(t *testing.T) {
	got := DiffSchemas(
		[]bigquery.Field{f("PLAYER_ID", "INTEGER"), f("goals", "STRING"), f("is_captain", "BOOL")},
		[]bigquery.Field{f("player_id", "INT64"), f("goals", "INTEGER"), f("assists", "INTEGER"), f("is_captain", "BOOLEAN")},
	)
	want := []ColumnImpact{
		{"goals", ColumnChange{Kind: TypeChanged, From: "INT64", To: "STRING"}},
		{"assists", ColumnChange{Kind: Dropped}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got := DiffSchemas([]bigquery.Field{f("player_id", "INT64")}, []bigquery.Field{f("PLAYER_ID", "INTEGER")}); len(got) != 0 {
		t.Errorf("matching schemas: %+v", got)
	}
	if got := DiffSchemas([]bigquery.Field{f("price", "DECIMAL"), f("total", "BIGDECIMAL")}, []bigquery.Field{f("price", "NUMERIC"), f("total", "BIGNUMERIC")}); len(got) != 0 {
		t.Errorf("DECIMAL and BIGDECIMAL are NUMERIC and BIGNUMERIC: %+v", got)
	}
}

// fakeSource answers column links by table#column, readers by table, and
// fails every lookup for the tables in failing.
type fakeSource struct {
	columnLinks map[string][]lineage.ColumnLink
	tableLinks  map[string][]lineage.ColumnLink
	failing     map[string]bool
	mu          sync.Mutex
	calls       []string
}

func (s *fakeSource) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *fakeSource) ColumnLinks(_ context.Context, table, column string) ([]lineage.ColumnLink, error) {
	s.record("downstream " + table + "#" + column)
	if s.failing[table] {
		return nil, errors.New("No access to Data Lineage")
	}
	return s.columnLinks[table+"#"+column], nil
}

func (s *fakeSource) TableOnlyReaders(_ context.Context, table string, linked map[string]bool) ([]lineage.ColumnLink, error) {
	var out []lineage.ColumnLink
	for _, l := range s.tableLinks[table] {
		if !linked[l.Table] {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *fakeSource) TableReaders(_ context.Context, table string) ([]lineage.ColumnLink, error) {
	s.record("readers " + table)
	return s.tableLinks[table], nil
}

var files = map[string]string{
	"p.marts.orders":    "definitions/orders.sqlx",
	"p.marts.revenue":   "definitions/revenue.sqlx",
	"p.marts.daily":     "definitions/daily.sqlx",
	"p.marts.customers": "definitions/customers.sqlx",
}

func readerContext(changed []string, sql map[string]string) ReaderContext {
	set := map[string]bool{}
	for _, c := range changed {
		set[c] = true
	}
	return ReaderContext{
		ResolveFile: func(table string) string { return files[table] },
		Changed:     set,
		SQLOf: func(table string) (string, bool) {
			s, ok := sql[table]
			return s, ok
		},
	}
}

func TestMentionsWholeIdentifiersOnly(t *testing.T) {
	for _, c := range []struct {
		sql, id string
		want    bool
	}{
		{"SELECT o.Discount FROM orders o", "discount", true},
		{"SELECT discount_pct FROM orders", "discount", false},
		{"SELECT `discount` FROM orders", "discount", true},
		{"SELECT total$ FROM t", "total", false}, // $ is part of an identifier in Dataform JS output
		{"SELECT xdiscount, discount FROM t", "discount", true},
		{"discount", "discount", true},
	} {
		if got := Mentions(c.sql, c.id); got != c.want {
			t.Errorf("Mentions(%q, %q) = %v", c.sql, c.id, got)
		}
	}
}

func TestAnnotateReader(t *testing.T) {
	ctx := readerContext([]string{"p.marts.revenue", "p.marts.daily"}, map[string]string{
		"p.marts.revenue": "SELECT order_id, amount FROM `p.marts.orders`",
		"p.marts.daily":   "SELECT SUM(discount) AS discount FROM `p.marts.orders`",
	})
	outside := AnnotateReader(lineage.ColumnLink{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy}, "discount", ctx)
	updated := AnnotateReader(lineage.ColumnLink{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other}, "discount", ctx)
	stillNames := AnnotateReader(lineage.ColumnLink{Table: "p.marts.daily", Column: "discount", DependencyType: lineage.Other}, "discount", ctx)
	if want := (Reader{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy}); outside != want {
		t.Errorf("outside %+v", outside)
	}
	if want := (Reader{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other, FilePath: "definitions/revenue.sqlx", ChangedOnBranch: true, ProbablyUpdated: true}); updated != want {
		t.Errorf("updated %+v", updated)
	}
	if !stillNames.StillNamed || stillNames.ProbablyUpdated {
		t.Errorf("still names %+v", stillNames)
	}
}

func TestAnnotateReaderFoldsDevRunsAndMarksDeletedReaders(t *testing.T) {
	ctx := readerContext(nil, nil)
	dev := map[string]string{"dev.marts_ashish.revenue": "p.marts.revenue", "dev.marts_ashish.old": "p.marts.old"}
	ctx.ToProd = ToProdFunc(dev)
	ctx.Deleted = map[string]bool{"p.marts.old": true}
	devRun := AnnotateReader(lineage.ColumnLink{Table: "dev.marts_ashish.revenue", Column: "net", DependencyType: lineage.Other}, "discount", ctx)
	deleted := AnnotateReader(lineage.ColumnLink{Table: "dev.marts_ashish.old", DependencyType: lineage.TableOnly}, "discount", ctx)
	if want := (Reader{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other, FilePath: "definitions/revenue.sqlx"}); devRun != want {
		t.Errorf("dev run %+v", devRun)
	}
	if want := (Reader{Table: "p.marts.old", DependencyType: lineage.TableOnly, DeletedOnBranch: true}); deleted != want {
		t.Errorf("deleted %+v: no file to open, and not outside the project", deleted)
	}
	updated := devRun
	updated.ChangedOnBranch, updated.ProbablyUpdated = true, true
	ranked := RankReaders([]Reader{deleted, {Table: "p.fin.margins", DependencyType: lineage.Other}, updated})
	if got := readerTables(ranked); !reflect.DeepEqual(got, []string{"p.fin.margins", "p.marts.revenue", "p.marts.old"}) {
		t.Errorf("ranked %v: deleted readers last", got)
	}
}

func TestRankReaders(t *testing.T) {
	ranked := RankReaders([]Reader{
		{Table: "p.marts.revenue", DependencyType: lineage.Other, FilePath: "r", ChangedOnBranch: true, ProbablyUpdated: true},
		{Table: "p.marts.daily", DependencyType: lineage.TableOnly, FilePath: "d"},
		{Table: "p.marts.customers", DependencyType: lineage.ExactCopy, FilePath: "c"},
		{Table: "p.fin.margins", DependencyType: lineage.Other},
		{Table: "p.marts.customers", DependencyType: lineage.ExactCopy, FilePath: "c"},
	})
	var got []string
	for _, r := range ranked {
		got = append(got, r.Table)
	}
	if want := []string{"p.fin.margins", "p.marts.customers", "p.marts.daily", "p.marts.revenue"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

var orders = Candidate{
	Table:    "p.marts.orders",
	FileName: "definitions/orders.sqlx",
	Type:     "table",
	Prod:     []bigquery.Field{f("order_id", "INT64"), f("discount", "NUMERIC"), f("amount", "FLOAT"), f("note", "STRING")},
	Dev:      []bigquery.Field{f("order_id", "INTEGER"), f("amount", "NUMERIC"), f("note", "STRING"), f("channel", "STRING")},
}

var unchangedView = Candidate{
	Table:    "p.marts.daily",
	FileName: "definitions/daily.sqlx",
	Type:     "view",
	Prod:     []bigquery.Field{f("day", "DATE")},
	Dev:      []bigquery.Field{f("day", "DATE"), f("orders", "INT64")},
}

func tables(ts []Table) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Table)
	}
	return out
}

func readerTables(rs []Reader) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Table)
	}
	return out
}

func TestBuildSplitsTablesAndLooksUpOnlyWhatIsAtRisk(t *testing.T) {
	src := &fakeSource{
		columnLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders#discount": {
				{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other},
				{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy},
				{Table: "p.marts_assertions.orders_nonNull", Column: "discount", DependencyType: lineage.Other, Assertion: true},
			},
		},
		tableLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders":    {{Table: "p.ml.events", DependencyType: lineage.TableOnly}, {Table: "p.marts.revenue", DependencyType: lineage.TableOnly}},
			"p.marts.customers": {{Table: "p.marts.revenue", DependencyType: lineage.TableOnly}},
		},
	}
	res, err := Build(context.Background(), []Candidate{
		orders,
		unchangedView,
		{Table: "p.marts.customers", FileName: "definitions/customers.sqlx", Type: "table", Deleted: true},
		{Table: "p.marts.ops", Type: "operations", UncheckedReason: "operation: a dry run of a script has no schema"},
	}, src, readerContext([]string{"p.marts.revenue"}, map[string]string{"p.marts.revenue": "SELECT amount AS net FROM `p.marts.customers`"}), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := tables(res.AtRisk); !reflect.DeepEqual(got, []string{"p.marts.orders", "p.marts.customers"}) {
		t.Errorf("at risk %v: most readers still to look at first", got)
	}
	o, customers := res.AtRisk[0], res.AtRisk[1]
	var cols []string
	for _, c := range o.Columns {
		cols = append(cols, c.Column+":"+c.Change.Kind)
	}
	if !reflect.DeepEqual(cols, []string{"discount:dropped", "amount:typeChanged"}) {
		t.Errorf("columns %v", cols)
	}
	if !reflect.DeepEqual(o.Added, []string{"channel"}) {
		t.Errorf("added %v", o.Added)
	}
	discount := o.Columns[0]
	if got := readerTables(discount.Readers); !reflect.DeepEqual(got, []string{"p.fin.margins", "p.ml.events", "p.marts.revenue"}) {
		t.Errorf("readers %v: assertions left out, table-level readers added", got)
	}
	if !discount.Readers[2].ProbablyUpdated {
		t.Error("revenue no longer names discount")
	}
	if !customers.Deleted || len(customers.Readers) != 1 || customers.Readers[0].Table != "p.marts.revenue" || !customers.Readers[0].StillNamed {
		t.Errorf("customers %+v: the deleted table name is what a reader has to stop naming", customers)
	}
	if len(res.Safe) != 1 || res.Safe[0].Table != "p.marts.daily" || !reflect.DeepEqual(res.Safe[0].Added, []string{"orders"}) {
		t.Errorf("safe %+v", res.Safe)
	}
	if want := []Unchecked{{Table: "p.marts.ops", Reason: "operation: a dry run of a script has no schema"}}; !reflect.DeepEqual(res.Unchecked, want) {
		t.Errorf("unchecked %+v", res.Unchecked)
	}
	for _, c := range src.calls {
		if strings.Contains(c, "p.marts.daily") {
			t.Errorf("a lookup for a safe table: %s", c)
		}
	}
}

func TestBuildSettlesADeletedTableReadOnlyByAnAssertionTheBranchDeletes(t *testing.T) {
	src := &fakeSource{tableLinks: map[string][]lineage.ColumnLink{
		"p.marts.snap": {
			{Table: "p.marts_assertions.compare_snap", DependencyType: lineage.TableOnly},
			{Table: "dev.marts_assertions.compare_snap", DependencyType: lineage.TableOnly},
			{Table: "dev.marts_assertions.orders_nonNull", DependencyType: lineage.TableOnly},
			{Table: "dev.scratch.someone_else", DependencyType: lineage.TableOnly},
		},
	}}
	ctx := readerContext(nil, nil)
	ctx.ToProd = ToProdFunc(map[string]string{
		"dev.marts_assertions.compare_snap":   "p.marts_assertions.compare_snap",
		"dev.marts_assertions.orders_nonNull": "p.marts_assertions.orders_nonNull",
	})
	ctx.Deleted = map[string]bool{"p.marts.snap": true, "p.marts_assertions.compare_snap": true}
	ctx.IsAssertion = func(table string) bool { return table == "p.marts_assertions.orders_nonNull" }
	res, err := Build(context.Background(), []Candidate{{Table: "p.marts.snap", Type: "table", Deleted: true}}, src, ctx, func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	snap := res.AtRisk[0]
	want := []Reader{
		{Table: "dev.scratch.someone_else", DependencyType: lineage.TableOnly},
		{Table: "p.marts_assertions.compare_snap", DependencyType: lineage.TableOnly, DeletedOnBranch: true},
	}
	if !reflect.DeepEqual(snap.Readers, want) {
		t.Errorf("readers %+v: one row for both runs of the deleted assertion; an assertion still on the branch left out; an unknown dev reader kept", snap.Readers)
	}
	snap.Readers = slices.DeleteFunc(snap.Readers, func(r Reader) bool { return !r.DeletedOnBranch })
	if !IsLowImpact(snap) {
		t.Error("nothing left reading it")
	}
	r := &Report{ChangedCount: 1, AtRisk: []Table{snap}}
	if l := BuildLedger(r); l.Summary != "Nothing to fix." {
		t.Errorf("summary %q", l.Summary)
	}
	if md := Markdown(r); !strings.Contains(md, "| ℹ️ | `snap` | deleted | `marts_assertions.compare_snap` · reads the table · deleted here |") {
		t.Error(md)
	}
}

func TestBuildKeepsAFailedLookupOnItsColumn(t *testing.T) {
	res, err := Build(context.Background(), []Candidate{orders}, &fakeSource{failing: map[string]bool{"p.marts.orders": true}}, readerContext(nil, nil), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.AtRisk[0].Columns {
		if c.ReadersError != "No access to Data Lineage" {
			t.Errorf("%s: %q", c.Column, c.ReadersError)
		}
	}
}

func TestBuildStopsStartingLookupsOnceCancelled(t *testing.T) {
	wide := Candidate{Table: "p.marts.wide", Type: "table", Dev: []bigquery.Field{f("id", "INT64")}}
	for i := range 10 {
		wide.Prod = append(wide.Prod, f(fmt.Sprintf("c%d", i), "INT64"))
	}
	src := &fakeSource{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := Build(ctx, []Candidate{wide}, src, readerContext(nil, nil), func(done, _ int) {
		if done == 1 {
			cancel()
		}
	})
	if res != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, %v", res, err)
	}
	if len(src.calls) != 4 {
		t.Errorf("%d calls: only the batch already started", len(src.calls))
	}
}

func markdownReport() *Report {
	var copies []Reader
	for i := range ledgerReaders + 2 {
		copies = append(copies, Reader{Table: fmt.Sprintf("p.rep.r%d", i), DependencyType: lineage.ExactCopy, FilePath: fmt.Sprintf("r%d.sqlx", i)})
	}
	return &Report{
		Comparison:   &Comparison{HeadRef: "feat/x", BaseRef: "origin/main", MergeBase: "52863aa0c1", HeadLabel: "working tree"},
		ChangedCount: 3,
		AtRisk: []Table{
			{
				Table: "p.marts.orders",
				Type:  "table",
				Columns: []Column{
					{
						Column: "discount",
						Change: ColumnChange{Kind: Dropped},
						Readers: slices.Concat(
							[]Reader{{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy}},
							copies,
							[]Reader{
								{Table: "p.marts.daily", Column: "net", DependencyType: lineage.Other, FilePath: "d.sqlx"},
								{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other, FilePath: "r.sqlx", ChangedOnBranch: true, ProbablyUpdated: true},
							}),
					},
					{Column: "amount", Change: ColumnChange{Kind: TypeChanged, From: "FLOAT64", To: "NUMERIC"}, Readers: []Reader{{Table: "p.marts.daily", Column: "amount", DependencyType: lineage.Other, FilePath: "d.sqlx"}}},
					{Column: "note", Change: ColumnChange{Kind: Dropped}, Readers: []Reader{}, ReadersError: "No access | denied"},
				},
			},
			{Table: "p.marts.customers", Type: "table", Deleted: true, Columns: []Column{}, Readers: []Reader{{Table: "p.marts.revenue", DependencyType: lineage.TableOnly, FilePath: "r.sqlx", ChangedOnBranch: true, ProbablyUpdated: true}}},
		},
		Safe: []Table{
			{Table: "p.marts.daily", Type: "view", Columns: []Column{}},
			{Table: "p.marts.weekly", Type: "table", Columns: []Column{}, Added: []string{"channel", "region"}},
		},
		Unchecked: []Unchecked{{Table: "p.marts.ops", Reason: "operation: a dry run of a script has no schema"}},
	}
}

func TestMarkdownIsALedgerOfModels(t *testing.T) {
	lines := strings.Split(Markdown(markdownReport()), "\n")
	want := []string{
		"### Column impact · `feat/x` → `main`",
		"",
		"**1 to fix before merging.** 5 models compared with production schemas · 1 not checked · dataset `marts` · base `52863aa`",
		"",
		"The models came from `feat/x` diffed against `main`, and their new columns were checked against the live tables in BigQuery.",
		"",
		"| # | | Model | What changed | Who is affected |",
		"| --: | :-: | --- | --- | --- |",
	}
	if !reflect.DeepEqual(lines[:8], want) {
		t.Errorf("head\n%s", strings.Join(lines[:8], "\n"))
	}
	rows := lines[8:13]
	for i, prefix := range []string{
		// orders: one row for its three columns; readers still to fix
		// first and in bold, then the settled ones, five at most.
		"| 1 | 🚨 | `orders` | drops `discount`, `note`; retypes `amount` (`FLOAT64` → `NUMERIC`) | **`fin.margins` reads `discount` · exact copy · outside the project**<br>**`rep.r0` reads `discount` · exact copy · not changed here**<br>",
		"| 2 | ℹ️ | `customers` | deleted | `revenue` · reads the table · probably updated |",
		"| 3 | ❔ | `ops` | not checked: operation: a dry run of a script has no schema | — |",
		"| 4 | ✅ | `daily` | keeps every column | — |",
		"| 5 | ✅ | `weekly` | keeps every column; adds `channel`, `region` | — |",
	} {
		if !strings.HasPrefix(rows[i], prefix) {
			t.Errorf("row %d\n got %s\nwant %s…", i, rows[i], prefix)
		}
	}
	orders := rows[0]
	for _, want := range []string{"+5 more in *Every reader*", "❔ readers couldn't be looked up: No access \\| denied"} { // pipes escaped
		if !strings.Contains(orders, want) {
			t.Errorf("orders row lacks %q:\n%s", want, orders)
		}
	}
	if !slices.Contains(lines, "<sub>🚨 a column or table something still reads is gone · ℹ️ changed, but nothing reads it any more · ❔ not checked, or its readers couldn't be looked up · ✅ keeps every column</sub>") &&
		!slices.Contains(lines, "<sub>🚨 a column or table something still reads is gone · ❔ not checked, or its readers couldn't be looked up · ℹ️ changed, but nothing reads it any more · ✅ keeps every column</sub>") {
		t.Errorf("legend of the marks used:\n%s", strings.Join(lines, "\n"))
	}
	every := slices.Index(lines, "<details><summary>Every reader (11)</summary>")
	if every < 0 {
		t.Fatalf("no Every reader:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{
		"| Reader | Reads | Of | On this branch |",
		"| `fin.margins` | `discount`, as an exact copy | 1 · `orders` | outside the project |",
		"| `daily` | `discount`, `amount`, derived | 1 · `orders` | not changed here |", // one row for both columns it reads
		"| `revenue` | `discount`, derived | 1 · `orders` | probably updated |",
		"| `revenue` | the whole table | 2 · `customers` | probably updated |",
	} {
		if !slices.Contains(lines[every:], want) {
			t.Errorf("Every reader lacks %q", want)
		}
	}
	// In the ledger's order: every reader of row 1 before any of row 2.
	if a, b := slices.Index(lines, "| `revenue` | `discount`, derived | 1 · `orders` | probably updated |"),
		slices.Index(lines, "| `revenue` | the whole table | 2 · `customers` | probably updated |"); a > b {
		t.Error("Every reader isn't in row order")
	}
}

func TestMarkdownNamesTablesByTheirDatasetWhenModelsDontShareOne(t *testing.T) {
	md := Markdown(&Report{ChangedCount: 2,
		AtRisk: []Table{{Table: "p.marts.orders", Type: "table",
			Columns: []Column{{Column: "id", Change: ColumnChange{Kind: Dropped}, Readers: []Reader{{Table: "dev.scratch.copy", Column: "id", DependencyType: lineage.ExactCopy}}}}}},
		Safe: []Table{{Table: "p.staging.orders", Type: "view", Columns: []Column{}}},
	})
	for _, want := range []string{
		"**1 to fix before merging.** 2 models compared with production schemas",
		"| 🚨 | `marts.orders` | drops `id` | **`dev.scratch.copy` · exact copy · outside the project** |", // another project: in full
		"| ✅ | `staging.orders` | keeps every column | — |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
	if strings.Contains(md, "dataset `") {
		t.Errorf("a shared dataset in\n%s", md)
	}
}

func TestMarkdownCapsEveryReader(t *testing.T) {
	var many []Reader
	for i := range MarkdownReaderList + 3 {
		many = append(many, Reader{Table: fmt.Sprintf("p.rep.r%d", i), Column: "a", DependencyType: lineage.Other, FilePath: fmt.Sprintf("r%d.sqlx", i)})
	}
	md := Markdown(&Report{ChangedCount: 1, AtRisk: []Table{{Table: "p.marts.orders", Type: "table",
		Columns: []Column{{Column: "a", Change: ColumnChange{Kind: Dropped}, Readers: many}}}}})
	for _, want := range []string{
		fmt.Sprintf("<details><summary>Every reader (%d)</summary>", MarkdownReaderList+3),
		"| +3 more | | | |",
		fmt.Sprintf("+%d more in *Every reader*", MarkdownReaderList+3-ledgerReaders),
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
}

func TestDevToProdFoldsDevRunsAndGuessesDeletedOnes(t *testing.T) {
	compile := func(json string) *project.Graph {
		g, err := dataform.ParseCompileJSON([]byte(json))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	prod := compile(`{"tables":[
		{"type":"table","target":{"database":"p","schema":"marts","name":"orders"},"canonicalTarget":{"database":"p","schema":"marts","name":"orders"}},
		{"type":"table","target":{"database":"p","schema":"marts","name":"daily"},"canonicalTarget":{"database":"p","schema":"marts","name":"daily"}}
	],"assertions":[{"target":{"database":"p","schema":"marts_assertions","name":"orders_nonNull"},"canonicalTarget":{"database":"p","schema":"marts_assertions","name":"orders_nonNull"}}]}`)
	dev := compile(`{"tables":[
		{"type":"table","target":{"database":"dev","schema":"marts_ashish","name":"orders"},"canonicalTarget":{"database":"p","schema":"marts","name":"orders"}},
		{"type":"table","target":{"database":"dev","schema":"marts_ashish","name":"daily"},"canonicalTarget":{"database":"dev","schema":"marts","name":"daily"}}
	],"assertions":[{"target":{"database":"dev","schema":"marts_assertions_ashish","name":"orders_nonNull"},"canonicalTarget":{"database":"p","schema":"marts_assertions","name":"orders_nonNull"}}]}`)
	got := DevToProd(dev, prod, []changes.Deleted{{ID: "p.marts.old"}, {ID: "p.other.gone"}})
	want := map[string]string{
		"dev.marts_ashish.orders":                    "p.marts.orders",
		"dev.marts_ashish.daily":                     "p.marts.daily", // by schema and name, the database differing
		"dev.marts_assertions_ashish.orders_nonNull": "p.marts_assertions.orders_nonNull",
		"dev.marts_ashish.old":                       "p.marts.old", // guessed from a table in its prod dataset
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if ToProdFunc(nil) != nil || ToProdFunc(got)("dev.scratch.x") != "dev.scratch.x" {
		t.Error("ToProdFunc")
	}
}

func TestSeverityByWhatIsStillRead(t *testing.T) {
	reader := Reader{Table: "p.a.b", DependencyType: lineage.Other}
	for _, c := range []struct {
		change  ColumnChange
		readers []Reader
		err     string
		want    Severity
	}{
		{ColumnChange{Kind: Dropped}, []Reader{reader}, "", SeverityCritical},
		{deletedChange, []Reader{reader}, "", SeverityCritical},
		{ColumnChange{Kind: TypeChanged, From: "INT64", To: "STRING"}, []Reader{reader}, "", SeverityWarning},
		{ColumnChange{Kind: Dropped}, []Reader{{Table: "p.a.b", ProbablyUpdated: true}}, "", SeverityLow},
		{deletedChange, []Reader{{Table: "p.a.b", DeletedOnBranch: true}}, "", SeverityLow},
		{ColumnChange{Kind: Dropped}, nil, "boom", SeverityUnknown},
	} {
		if got := ImpactSeverity(c.change, c.readers, c.err); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
	if w, ok := Worst(markdownReport()); !ok || w != SeverityCritical {
		t.Errorf("worst %v %v", w, ok)
	}
	if _, ok := Worst(&Report{}); ok {
		t.Error("nothing at risk")
	}
}

func graph(t *testing.T, ordersSQL, dailySQL string) *project.Graph {
	t.Helper()
	g, err := dataform.ParseCompileJSON(fmt.Appendf(nil, `{"tables":[
		{"type":"table","target":{"database":"p","schema":"marts","name":"orders"},"fileName":"definitions/orders.sqlx","query":%q},
		{"type":"view","target":{"database":"p","schema":"marts","name":"daily"},"fileName":"definitions/daily.sqlx","query":%q},
		{"type":"table","target":{"database":"p","schema":"marts","name":"fresh"},"fileName":"definitions/fresh.sqlx","query":"SELECT 1 AS a"}
	],"operations":[{"target":{"database":"p","schema":"marts","name":"cleanup"},"fileName":"definitions/cleanup.sqlx","queries":["DELETE FROM x WHERE true"]}],
	"projectConfig":{"defaultDatabase":"p","defaultLocation":"EU"}}`, ordersSQL, dailySQL))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSignatureFollowsChangedSQLOnly(t *testing.T) {
	res := &changes.Result{Diff: changes.Diff{Changed: []changes.Changed{{ID: "p.marts.orders", Reasons: []changes.Reason{changes.ReasonSQL}}}}}
	before := Signature(res, graph(t, "SELECT 1 AS a", "SELECT 1"))
	if Signature(res, graph(t, "SELECT 1 AS a", "SELECT 2")) != before {
		t.Error("an unchanged action's SQL changed the signature")
	}
	if Signature(res, graph(t, "SELECT 1 AS b", "SELECT 1")) == before {
		t.Error("a changed action's SQL left the signature alone")
	}
}

func TestSignatureFollowsAnIncrementalTablesIncrementalSQLAndPostOps(t *testing.T) {
	graph := func(extra string) *project.Graph {
		g, err := dataform.ParseCompileJSON(fmt.Appendf(nil, `{"tables":[{"type":"incremental","target":{"database":"p","schema":"marts","name":"orders"},"query":"SELECT 1 AS a",%s}]}`, extra))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	res := &changes.Result{Diff: changes.Diff{Changed: []changes.Changed{{ID: "p.marts.orders", Reasons: []changes.Reason{changes.ReasonSQL}}}}}
	before := Signature(res, graph(`"incrementalQuery":"SELECT 1 AS a WHERE x","postOps":["SELECT 1"]`))
	if Signature(res, graph(`"incrementalQuery":"SELECT 1 AS b WHERE x","postOps":["SELECT 1"]`)) == before {
		t.Error("a changed incremental query left the signature alone")
	}
	if Signature(res, graph(`"incrementalQuery":"SELECT 1 AS a WHERE x","postOps":["SELECT 2"]`)) == before {
		t.Error("changed post operations left the signature alone")
	}
	if Signature(res, graph(`"incrementalQuery":"SELECT 1 AS a WHERE x","postOps":["SELECT 1"],"incrementalPreOps":["SELECT 3"]`)) == before {
		t.Error("changed incremental pre operations left the signature alone")
	}
}

func TestRunDryRunsChangedTablesAndComparesWithProd(t *testing.T) {
	g := graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM x")
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: "p.marts.cleanup", FileName: "definitions/cleanup.sqlx", Type: "operations", Reasons: []changes.Reason{changes.ReasonSQL}},
				{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonSQL}},
				{ID: "p.marts.fresh", FileName: "definitions/fresh.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonNew}},
				{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
			},
			Deleted: []changes.Deleted{
				{ID: "p.marts.customers", FileName: "definitions/customers.sqlx", Type: "table"},
				{ID: "p.marts.customers_assertions_uniqueKey_0", FileName: "definitions/customers.sqlx", Type: "assertion"},
			},
		},
	}
	prod := map[string][]bigquery.Field{
		"p.marts.orders":    {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
		"p.marts.daily":     {f("day", "DATE")},
		"p.marts.customers": {f("id", "INT64")},
	}
	var dryRuns []string
	var mu sync.Mutex
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]bigquery.Field, error) { return prod[table], nil },
		DryRun: func(_ context.Context, project, location, sql string) (*bigquery.DryRun, error) {
			mu.Lock()
			dryRuns = append(dryRuns, project+"/"+location+": "+sql)
			mu.Unlock()
			if strings.Contains(sql, "day") {
				return nil, &bigquery.APIError{Status: 400, Message: "Unrecognized name: day"}
			}
			return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
		},
		Readers: &fakeSource{
			columnLinks: map[string][]lineage.ColumnLink{"p.marts.orders#discount": {{Table: "p.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy}}},
			tableLinks:  map[string][]lineage.ColumnLink{"p.marts.customers": {{Table: "p.marts.orders", DependencyType: lineage.TableOnly}}},
		},
	}
	var phases []string
	report, err := Run(context.Background(), res, g, deps, func(p Progress) {
		mu.Lock()
		phases = append(phases, fmt.Sprintf("%s %d/%d", p.Phase, p.Done, p.Total))
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ChangedCount != 4 {
		t.Errorf("changed count %d: the operation without hasOutput and the assertion aren't tables", report.ChangedCount)
	}
	// customers' only reader is orders, whose new SQL no longer names it
	if got := tables(report.AtRisk); !reflect.DeepEqual(got, []string{"p.marts.orders", "p.marts.customers"}) {
		t.Errorf("at risk %v", got)
	}
	if !report.AtRisk[1].Readers[0].ProbablyUpdated {
		t.Errorf("customers' readers %+v", report.AtRisk[1].Readers)
	}
	if got := report.AtRisk[0].Columns; len(got) != 1 || got[0].Column != "discount" || got[0].Readers[0].Table != "p.fin.margins" {
		t.Errorf("orders columns %+v", got)
	}
	want := []Unchecked{
		{Table: "p.marts.daily", FileName: "definitions/daily.sqlx", Reason: "dry run failed: Unrecognized name: day"},
	}
	if !reflect.DeepEqual(report.Unchecked, want) {
		t.Errorf("unchecked %+v", report.Unchecked)
	}
	// fresh is new and has no table: nothing to lose, so no dry run either
	wantNew := []Table{{Table: "p.marts.fresh", FileName: "definitions/fresh.sqlx", Type: "table", Columns: []Column{}, New: true}}
	if !reflect.DeepEqual(report.New, wantNew) {
		t.Errorf("new %+v", report.New)
	}
	if l := BuildLedger(report); l.Summary != "1 to fix before merging." || len(l.Rows) != 4 {
		t.Errorf("ledger %+v", l)
	}
	if !slices.Contains(dryRuns, "p/EU: SELECT order_id, amount FROM src") {
		t.Errorf("dry runs %v", dryRuns)
	}
	if report.Signature != Signature(res, g) || report.Comparison.HeadLabel != "working tree" || report.Comparison.MergeBase != "abcdef123456" {
		t.Errorf("report %+v", report)
	}
	if phases[0] != "Dry running 0/3" || phases[len(phases)-1] != "Looking up readers 2/2" {
		t.Errorf("phases %v", phases)
	}
	if !strings.Contains(Markdown(report), "### Column impact · `feat` → `main`\n\n**1 to fix before merging.** 4 models compared with prod tables in `p` · 1 not checked · dataset `marts` · base `abcdef1`") {
		t.Error(Markdown(report))
	}
}

// A new action whose table is already in BigQuery (a run of the branch
// built it, or an action deleted earlier is back) is still compared, and
// says it is new: with the tables at risk when it drops a column, else
// with the new ones.
func TestRunComparesANewActionWhoseTableExists(t *testing.T) {
	g := graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM x")
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{Changed: []changes.Changed{
			{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonNew}},
			{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonNew}},
		}},
	}
	prod := map[string][]bigquery.Field{
		"p.marts.orders": {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
		"p.marts.daily":  {f("day", "DATE")},
	}
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]bigquery.Field, error) { return prod[table], nil },
		DryRun: func(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
			if strings.Contains(sql, "day") {
				return &bigquery.DryRun{Schema: []bigquery.Field{f("day", "DATE"), f("week", "DATE")}}, nil
			}
			return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
		},
		Readers: &fakeSource{},
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AtRisk) != 1 || report.AtRisk[0].Table != "p.marts.orders" || !report.AtRisk[0].New {
		t.Errorf("at risk %+v", report.AtRisk)
	}
	if len(report.Safe) != 0 || len(report.Unchecked) != 0 {
		t.Errorf("safe %+v, unchecked %+v", report.Safe, report.Unchecked)
	}
	if len(report.New) != 1 || report.New[0].Table != "p.marts.daily" || !report.New[0].Exists {
		t.Fatalf("new %+v", report.New)
	}
	md := Markdown(report)
	for _, want := range []string{
		"**Nothing to fix.**",
		"| ℹ️ | `orders` | new, its table is already there; drops `discount` | nobody |",
		"| 🆕 | `daily` | new, its table is already there; keeps every column; adds `week` | — |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in:\n%s", want, md)
		}
	}
}

func TestRunComparesARenamedActionWithItsOldTable(t *testing.T) {
	g := graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM p.marts.orders")
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonSQL}},
				{ID: "p.marts.fresh", FileName: "definitions/fresh.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonNew},
					RenamedFrom: "p.marts.stale", RenamedFromFile: "definitions/stale.sqlx", Similarity: 100},
				{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonNew},
					RenamedFrom: "p.marts.orders_old", RenamedFromFile: "definitions/orders_old.sqlx", Similarity: 95},
			},
			Deleted: []changes.Deleted{
				{ID: "p.marts.orders_old", FileName: "definitions/orders_old.sqlx", Type: "table", RenamedTo: "p.marts.orders"},
				{ID: "p.marts.stale", FileName: "definitions/stale.sqlx", Type: "table", RenamedTo: "p.marts.fresh"},
			},
		},
	}
	// Neither new name has a table yet; of the old ones only orders_old does.
	prod := map[string][]bigquery.Field{
		"p.marts.orders_old": {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
		"p.marts.daily":      {f("day", "DATE")},
	}
	src := &fakeSource{
		columnLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders_old#discount": {{Table: "ext.reports.margin", Column: "discount", DependencyType: lineage.ExactCopy}},
		},
		tableLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders_old": {{Table: "p.marts.daily", DependencyType: lineage.TableOnly}, {Table: "ext.reports.margin", DependencyType: lineage.TableOnly}},
		},
	}
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]bigquery.Field, error) { return prod[table], nil },
		DryRun: func(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
			if strings.Contains(sql, "day") {
				return &bigquery.DryRun{Schema: []bigquery.Field{f("day", "DATE")}}, nil
			}
			return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
		},
		Readers: src,
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := tables(report.AtRisk); !reflect.DeepEqual(got, []string{"p.marts.orders", "p.marts.orders_old"}) && !reflect.DeepEqual(got, []string{"p.marts.orders_old", "p.marts.orders"}) {
		t.Fatalf("at risk %v", got)
	}
	for _, at := range report.AtRisk {
		switch at.Table {
		case "p.marts.orders":
			// Compared with the table it replaces, and its readers.
			if !at.FromOld || at.Exists || at.RenamedFrom != "p.marts.orders_old" || len(at.Columns) != 1 || at.Columns[0].Column != "discount" {
				t.Errorf("renamed %+v", at)
			} else if got := readerTables(at.Columns[0].Readers); !reflect.DeepEqual(got, []string{"ext.reports.margin", "p.marts.daily"}) {
				t.Errorf("discount readers %v", got)
			}
		case "p.marts.orders_old":
			if !at.Deleted || at.RenamedTo != "p.marts.orders" {
				t.Errorf("old %+v", at)
			}
			for _, r := range at.Readers {
				if r.Table == "p.marts.daily" && !r.ProbablyUpdated {
					t.Errorf("daily, moved onto the new name, isn't probably updated: %+v", r)
				}
			}
		}
	}
	if len(report.New) != 1 || report.New[0].Table != "p.marts.fresh" {
		t.Fatalf("new %+v", report.New)
	}
	if len(report.Unchecked) != 1 || report.Unchecked[0].Reason != "renamed to marts.fresh, and no table in p" {
		t.Errorf("unchecked %+v", report.Unchecked)
	}
	if report.ChangedCount != 3 {
		t.Errorf("changed count %d, want a rename counted once", report.ChangedCount)
	}
	// Its readers cite the rename's row by the table they read: the old one.
	if md := Markdown(report); !strings.Contains(md, "| `ext.reports.margin` | the whole table | 1 · `orders_old` | outside the project |") {
		t.Errorf("Every reader:\n%s", md)
	}
	md := Markdown(report)
	for _, want := range []string{
		// One row per rename: what the new table drops of the old one, and
		// who reads either.
		"| 🚨 | `orders_old` → `orders` | renamed, drops `discount` | **`ext.reports.margin` · reads the table · outside the project**<br>`daily` · reads the table · moved to the new name |",
		"| 🔁 | `stale` → `fresh` | renamed | ❔ the old table wasn't checked: renamed to marts.fresh, and no table in p |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "| 🆕 | `fresh`") || strings.Contains(md, "| ℹ️ | `orders_old` |") || strings.Contains(md, "| 🚨 | `orders_old` |") {
		t.Errorf("a renamed table's halves listed apart:\n%s", md)
	}
}

func TestMarkdownListsNewActionsAndSaysWhenTheDefaultTargetsWereCompared(t *testing.T) {
	r := &Report{ChangedCount: 1, Against: []string{"acme-dev"},
		New: []Table{{Table: "acme-dev.marts.fresh", Type: "table", New: true}}}
	md := Markdown(r)
	if !strings.Contains(md, "| 🆕 | `fresh` | new: nothing reads it yet | — |") || !strings.Contains(md, "compared with prod tables in `acme-dev`") {
		t.Error(md)
	}
	if strings.Contains(md, "Compared with") {
		t.Errorf("a hint though prodCompilerOptions is set:\n%s", md)
	}
	r.Unset = "prodCompilerOptions"
	want := "\n\n<sub>Compared with the project's default targets (`acme-dev`), which may be dev tables. Set `prodCompilerOptions` to compare with prod.</sub>"
	if md := Markdown(r); !strings.HasSuffix(md, want) {
		t.Error(md)
	}
}

func TestRunComparesWithProdTargets(t *testing.T) {
	g := graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM x")
	// The compile with prodCompilerOptions (--default-database=prd): fresh
	// isn't in it, as when it was added after that compile.
	prodCompile, err := dataform.ParseCompileJSON([]byte(`{"tables":[
		{"type":"table","target":{"database":"prd","schema":"marts","name":"orders"}},
		{"type":"view","target":{"database":"prd","schema":"marts","name":"daily"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: "p.marts.daily", FileName: "definitions/daily.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonSQL}},
				{ID: "p.marts.fresh", FileName: "definitions/fresh.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonNew}},
				{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
			},
			Deleted: []changes.Deleted{{ID: "p.marts.customers", FileName: "definitions/customers.sqlx", Type: "table"}},
		},
	}
	prodRuns := DevToProd(prodCompile, g, res.Deleted)
	schemas := map[string][]bigquery.Field{
		// The default targets match the branch: built from it already.
		"p.marts.orders": {f("order_id", "INT64"), f("amount", "FLOAT")},
		"p.marts.fresh":  {f("a", "INT64")},
		// Prod still has discount.
		"prd.marts.orders":    {f("order_id", "INT64"), f("amount", "FLOAT"), f("discount", "NUMERIC")},
		"prd.marts.daily":     {f("day", "DATE")},
		"prd.marts.customers": {f("id", "INT64")},
	}
	var read []string
	var mu sync.Mutex
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]bigquery.Field, error) {
			mu.Lock()
			read = append(read, table)
			mu.Unlock()
			return schemas[table], nil
		},
		DryRun: func(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
			if strings.Contains(sql, "day") {
				return &bigquery.DryRun{Schema: []bigquery.Field{f("day", "DATE")}}, nil
			}
			return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INTEGER"), f("amount", "FLOAT")}}, nil
		},
		Readers: &fakeSource{
			columnLinks: map[string][]lineage.ColumnLink{"prd.marts.orders#discount": {
				{Table: "prd.marts.daily", Column: "discount", DependencyType: lineage.Other}, // a prod run of daily
				{Table: "prd.fin.margins", Column: "discount", DependencyType: lineage.ExactCopy},
			}},
			tableLinks: map[string][]lineage.ColumnLink{"prd.marts.customers": {{Table: "prd.marts.orders", DependencyType: lineage.TableOnly}}},
		},
		ToProd:     ToProdFunc(prodRuns),
		ProdTarget: ProdTargetFunc(prodRuns),
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range read {
		if !strings.HasPrefix(table, "prd.") {
			t.Errorf("read %s, not a prod table", table)
		}
	}
	if got := tables(report.AtRisk); !reflect.DeepEqual(got, []string{"prd.marts.orders", "prd.marts.customers"}) {
		t.Fatalf("at risk %v", got)
	}
	readers := report.AtRisk[0].Columns[0].Readers
	if got := readerTables(readers); !reflect.DeepEqual(got, []string{"prd.fin.margins", "prd.marts.daily"}) {
		t.Errorf("discount's readers %v", got)
	}
	if daily := readers[1]; !daily.ProbablyUpdated || daily.FilePath != "definitions/daily.sqlx" {
		t.Errorf("daily's prod run should fold into the action: %+v", daily)
	}
	if r := report.AtRisk[1].Readers; len(r) != 1 || r[0].Table != "prd.marts.orders" || !r[0].ProbablyUpdated {
		t.Errorf("customers' readers %+v", r)
	}
	if got := tables(report.Safe); !reflect.DeepEqual(got, []string{"prd.marts.daily"}) {
		t.Errorf("safe %v", got)
	}
	want := []Unchecked{{Table: "p.marts.fresh", FileName: "definitions/fresh.sqlx",
		Reason: "its prod table is unknown: nothing in the compile with prodCompilerOptions matches it"}}
	if !reflect.DeepEqual(report.Unchecked, want) {
		t.Errorf("unchecked %+v", report.Unchecked)
	}
	if !reflect.DeepEqual(report.Against, []string{"prd"}) {
		t.Errorf("against %v", report.Against)
	}
	md := Markdown(report)
	for _, s := range []string{"compared with prod tables in `prd`", "| ✅ | `marts.daily` | keeps every column | — |", "| ❔ | `p.marts.fresh` |"} {
		if !strings.Contains(md, s) {
			t.Errorf("Markdown lacks %q:\n%s", s, md)
		}
	}
}

// The table in BigQuery is behind the base branch: discount was dropped
// there, in a commit that isn't released yet. The branch drops amount. Only
// amount is the branch's doing; discount is marked as the base's and counts
// as low though something reads it.
func TestRunTellsTheBaseBranchsChangesFromTheBranchsOwn(t *testing.T) {
	g := graph(t, "SELECT order_id FROM src", "SELECT day FROM x")
	base := graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM x")
	res := &changes.Result{
		BaseRef: "origin/main", MergeBase: "abcdef123456", HeadRef: "feat", Head: changes.HeadWorktree,
		Diff: changes.Diff{Changed: []changes.Changed{
			{ID: "p.marts.orders", FileName: "definitions/orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
		}},
	}
	prod := []bigquery.Field{f("order_id", "STRING"), f("amount", "FLOAT"), f("discount", "NUMERIC")}
	reader := func(col string) []lineage.ColumnLink {
		return []lineage.ColumnLink{{Table: "p.fin.margins", Column: col, DependencyType: lineage.ExactCopy}}
	}
	baseAsked := 0
	deps := Deps{
		Schema: func(context.Context, string) ([]bigquery.Field, error) { return prod, nil },
		DryRun: func(_ context.Context, _, _, sql string) (*bigquery.DryRun, error) {
			if strings.Contains(sql, "amount") { // the merge-base's
				return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INT64"), f("amount", "FLOAT64")}}, nil
			}
			return &bigquery.DryRun{Schema: []bigquery.Field{f("order_id", "INT64")}}, nil
		},
		Readers: &fakeSource{columnLinks: map[string][]lineage.ColumnLink{
			"p.marts.orders#amount": reader("amount"), "p.marts.orders#discount": reader("discount"), "p.marts.orders#order_id": reader("order_id"),
		}},
		Base: func(context.Context) (*project.Graph, error) { baseAsked++; return base, nil },
	}
	report, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AtRisk) != 1 || baseAsked != 1 {
		t.Fatalf("at risk %+v, base asked for %d times", report.AtRisk, baseAsked)
	}
	// The branch's own first, then the base's: the drop, then the retype.
	var got []string
	for _, c := range report.AtRisk[0].Columns {
		got = append(got, fmt.Sprintf("%s %s onBase=%v %d", c.Column, c.Change.Kind, c.Change.OnBase, ImpactSeverity(c.Change, c.Readers, c.ReadersError)))
	}
	want := []string{
		fmt.Sprintf("amount dropped onBase=false %d", SeverityCritical),
		fmt.Sprintf("discount dropped onBase=true %d", SeverityLow),
		fmt.Sprintf("order_id typeChanged onBase=true %d", SeverityLow),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("columns %q\nwant    %q", got, want)
	}
	if worst, _ := Worst(report); worst != SeverityCritical {
		t.Errorf("worst %d", worst)
	}
	md := Markdown(report)
	for _, want := range []string{
		"| 🚨 | `orders` | drops `amount`; already changed on `origin/main`: `discount`, `order_id` (`STRING` → `INT64`) |",
		"<sub>\"Already changed on `origin/main`\": `origin/main` makes the change too, and the table compared with is behind it.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in:\n%s", want, md)
		}
	}

	// Only the base's changes left: the table is low, and nothing gates.
	g = graph(t, "SELECT order_id, amount FROM src", "SELECT day FROM x")
	report, err = Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := Worst(report); !found || BuildLedger(report).Summary != "Nothing to fix." {
		t.Errorf("%s", BuildLedger(report).Summary)
	}
	if worst, _ := Worst(report); worst != SeverityLow {
		t.Errorf("worst %d", worst)
	}

	// No telling what the base did: every change stays the branch's.
	deps.Base = func(context.Context) (*project.Graph, error) { return nil, errors.New("could not compile") }
	report, err = Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil || HasOnBase(report) || strings.Contains(Markdown(report), "already changed on") {
		t.Errorf("%+v, %v", report.AtRisk, err)
	}
}

func TestAnnotateReaderJudgesARenamedReaderByItsNewSQL(t *testing.T) {
	ctx := readerContext([]string{"p.marts.revenue"}, map[string]string{"p.marts.revenue": "SELECT net FROM orders"})
	ctx.Deleted = map[string]bool{"p.marts.old_revenue": true}
	ctx.Renamed = map[string]string{"p.marts.old_revenue": "p.marts.revenue"}
	updated := AnnotateReader(lineage.ColumnLink{Table: "p.marts.old_revenue", Column: "net", DependencyType: lineage.Other}, "discount", ctx)
	want := Reader{Table: "p.marts.revenue", Column: "net", DependencyType: lineage.Other, FilePath: "definitions/revenue.sqlx",
		ChangedOnBranch: true, ProbablyUpdated: true, RenamedFrom: "p.marts.old_revenue"}
	if updated != want {
		t.Errorf("renamed reader %+v, want %+v", updated, want)
	}
	if got := ReaderNote(updated); got != "renamed here, probably updated" {
		t.Error(got)
	}
	still := AnnotateReader(lineage.ColumnLink{Table: "p.marts.old_revenue", DependencyType: lineage.TableOnly}, "net", ctx)
	if !still.StillNamed || ReaderNote(still) != "renamed here, still names it" || IsSettled(still) {
		t.Errorf("still names it: %+v", still)
	}
}

func TestImpactReadersLeaveOutARenamedTableReadingItsOldSelf(t *testing.T) {
	ctx := readerContext([]string{"p.marts.revenue"}, map[string]string{"p.marts.revenue": "SELECT net FROM orders"})
	ctx.Deleted = map[string]bool{"p.marts.old_revenue": true}
	ctx.Renamed = map[string]string{"p.marts.old_revenue": "p.marts.revenue"}
	links := []lineage.ColumnLink{{Table: "p.marts.old_revenue", DependencyType: lineage.TableOnly}, {Table: "p.fin.margins", DependencyType: lineage.TableOnly}}
	if got := readerTables(ImpactReaders(links, "p.marts.old_revenue", "old_revenue", ctx)); !reflect.DeepEqual(got, []string{"p.fin.margins"}) {
		t.Errorf("readers %v, want the incremental table's read of itself left out", got)
	}
}
