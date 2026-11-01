package impact

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/lineage"
)

// TestMatchesExtension runs the extension's own column impact code
// (src/shared/columnLineage/impactSummary.ts, bundled with the extension's
// esbuild and run with node) on random cases and checks that this port
// answers the same: the readers it lists, and the order of tables and
// columns. The Markdown is xf's own ledger, so it isn't compared. It is opt-in: set XF_EXTENSION to a
// checkout of vscode-dataform-tools with its node_modules installed.
func TestMatchesExtension(t *testing.T) {
	dir := os.Getenv("XF_EXTENSION")
	if dir == "" {
		t.Skip("set XF_EXTENSION to a vscode-dataform-tools checkout to compare with it")
	}
	harness := bundleHarness(t, dir)
	rng := rand.New(rand.NewPCG(402, 1))
	// XF_PARITY_SCALE multiplies the random cases, for a longer run.
	scale := 1
	if n, err := strconv.Atoi(os.Getenv("XF_PARITY_SCALE")); err == nil && n > 0 {
		scale = n
	}

	type kase struct {
		Kind    string               `json:"kind"`
		Links   []lineage.ColumnLink `json:"links,omitempty"`
		Table   string               `json:"table,omitempty"`
		Name    string               `json:"name,omitempty"`
		Context *parityContext       `json:"context,omitempty"`
		Tables  []Table              `json:"tables,omitempty"`
	}
	var cases []kase
	for range 300 * scale {
		table := pick(rng, tablePool)
		cases = append(cases, kase{Kind: "readers", Links: randomLinks(rng), Table: table, Name: pick(rng, columnPool), Context: randomContext(rng)})
	}
	for range 200 * scale {
		cases = append(cases, kase{Kind: "sort", Tables: randomReport(rng).AtRisk})
	}

	in, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", harness)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	var answers []json.RawMessage
	if err := json.Unmarshal(out, &answers); err != nil || len(answers) != len(cases) {
		t.Fatalf("answers: %v (%d for %d cases)", err, len(answers), len(cases))
	}

	failures := 0
	fail := func(i int, format string, args ...any) {
		failures++
		if failures <= 5 {
			t.Errorf("case %d (%s): "+format, append([]any{i, cases[i].Kind}, args...)...)
		}
	}
	for i, c := range cases {
		switch c.Kind {
		case "readers":
			var want []Reader
			_ = json.Unmarshal(answers[i], &want)
			got := ImpactReaders(c.Links, c.Table, c.Name, c.Context.readerContext())
			if len(got) != 0 || len(want) != 0 {
				if !reflect.DeepEqual(got, want) {
					fail(i, "readers\n got %+v\nwant %+v", got, want)
				}
			}
		case "sort":
			type sorted struct {
				Table   string   `json:"table"`
				Columns []string `json:"columns"`
			}
			var want []sorted
			_ = json.Unmarshal(answers[i], &want)
			var got []sorted
			for _, tb := range SortTables(c.Tables) {
				cols := []string{}
				for _, col := range SortColumns(tb.Columns) {
					cols = append(cols, col.Column)
				}
				got = append(got, sorted{Table: tb.Table, Columns: cols})
			}
			if (len(got) > 0 || len(want) > 0) && !reflect.DeepEqual(got, want) {
				fail(i, "order\n got %v\nwant %v", got, want)
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d of %d cases differ from the extension", failures, len(cases))
	}
	t.Logf("%d cases match the extension", len(cases))
}

// bundleHarness bundles testdata/extension_harness.ts against the
// extension at dir, with the extension's own esbuild.
func bundleHarness(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "extension_harness.ts"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	entry := filepath.Join(tmp, "harness.ts")
	if err := os.WriteFile(entry, bytes.ReplaceAll(src, []byte("EXTENSION_DIR"), []byte(dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "harness.js")
	esbuild := filepath.Join(dir, "node_modules", "esbuild", "bin", "esbuild")
	if b, err := exec.Command(esbuild, entry, "--bundle", "--platform=node", "--log-level=warning", "--outfile="+out).CombinedOutput(); err != nil {
		t.Fatalf("esbuild: %v\n%s", err, b)
	}
	return out
}

// Names mix case, underscores and digits, as BigQuery names do, so the two
// sides' string ordering is put to the test.
var (
	tablePool = []string{
		"p.marts.orders", "p.marts.Orders_v2", "p.marts.daily", "p.marts._tmp", "p.marts.0100_DAILY_ACTUALS",
		"p.rep.r1", "p.rep.r10", "p.rep.R2", "p.fin.margins", "p.fin.margins_eu", "other.fin.margins",
		"dev.marts_ashish.orders", "dev.marts_ashish.daily", "dev.scratch.copy", "p.marts_assertions.orders_nonNull",
	}
	// againstPool are the projects a report compared with; nil is a
	// report without them, which keeps the extension's old wording.
	againstPool   = [][]string{nil, nil, {"p"}, {"acme-prod"}, {"acme-prod", "p"}}
	uncheckedPool = []string{
		"dry run failed: Unrecognized name: x", "no table in p yet", "deleted, and no table in acme-prod",
		"its prod table is unknown: nothing in the compile with prodCompilerOptions matches it",
	}
	columnPool = []string{"id", "ID", "discount", "Discount_Total", "net", "amount", "a_b", "aB", "x1", "x10", "x2", "note|pipe"}
	errorPool  = []string{"", "", "", "No access to Data Lineage", "denied | by policy", "line one\n  line two"}
)

func pick[T any](rng *rand.Rand, xs []T) T { return xs[rng.IntN(len(xs))] }

func randomReader(rng *rand.Rand) Reader {
	r := Reader{Table: pick(rng, tablePool), DependencyType: pick(rng, []lineage.DependencyType{lineage.ExactCopy, lineage.Other, lineage.TableOnly})}
	if r.DependencyType != lineage.TableOnly || rng.IntN(4) == 0 {
		r.Column = pick(rng, columnPool)
	}
	switch rng.IntN(6) {
	case 0:
		r.DeletedOnBranch = true
	case 1, 2:
		r.FilePath = "definitions/" + ShortTable(r.Table) + ".sqlx"
	case 3:
		r.FilePath = "definitions/x.sqlx"
		r.ChangedOnBranch = true
		if rng.IntN(2) == 0 {
			r.ProbablyUpdated = true
		} else {
			r.StillNamed = rng.IntN(2) == 0
		}
	}
	return r
}

func randomReaders(rng *rand.Rand, n int) []Reader {
	out := []Reader{}
	for range rng.IntN(n + 1) {
		out = append(out, randomReader(rng))
	}
	return out
}

func randomReport(rng *rand.Rand) *Report {
	r := &Report{ChangedCount: rng.IntN(6), AtRisk: []Table{}, Safe: []Table{}, Unchecked: []Unchecked{}, Against: pick(rng, againstPool)}
	if rng.IntN(4) > 0 {
		r.Comparison = &Comparison{HeadRef: "feat/x", BaseRef: "origin/main", MergeBase: "52863aa0c1d2", HeadLabel: "working tree"}
	}
	for range rng.IntN(4) {
		t := Table{Table: pick(rng, tablePool), Type: "table", Columns: []Column{}}
		if rng.IntN(4) == 0 {
			t.Deleted = true
			t.Readers = randomReaders(rng, 8)
			t.ReadersError = pick(rng, errorPool)
		} else {
			for range 1 + rng.IntN(4) {
				c := Column{Column: pick(rng, columnPool), Change: ColumnChange{Kind: Dropped}, Readers: randomReaders(rng, 30), ReadersError: pick(rng, errorPool)}
				if rng.IntN(2) == 0 {
					c.Change = ColumnChange{Kind: TypeChanged, From: "INT64", To: "STRING"}
				}
				t.Columns = append(t.Columns, c)
			}
		}
		r.AtRisk = append(r.AtRisk, t)
	}
	for range rng.IntN(3) {
		t := Table{Table: pick(rng, tablePool), Type: "view", Columns: []Column{}}
		for range rng.IntN(3) {
			t.Added = append(t.Added, pick(rng, columnPool))
		}
		r.Safe = append(r.Safe, t)
	}
	for range rng.IntN(3) {
		r.Unchecked = append(r.Unchecked, Unchecked{Table: pick(rng, tablePool), Reason: pick(rng, uncheckedPool)})
	}
	// New actions: with nothing to lose, and among the tables at risk.
	for range rng.IntN(3) {
		t := Table{Table: pick(rng, tablePool), Type: "table", Columns: []Column{}, New: true}
		if rng.IntN(2) == 0 {
			t.Exists = true
			for range rng.IntN(3) {
				t.Added = append(t.Added, pick(rng, columnPool))
			}
		}
		r.New = append(r.New, t)
	}
	for i := range r.AtRisk {
		if !r.AtRisk[i].Deleted && rng.IntN(4) == 0 {
			r.AtRisk[i].New, r.AtRisk[i].Exists = true, true
		}
	}
	if rng.IntN(3) == 0 {
		r.Unset = "prodCompilerOptions"
	}
	return r
}

func randomLinks(rng *rand.Rand) []lineage.ColumnLink {
	var out []lineage.ColumnLink
	for range rng.IntN(12) {
		r := randomReader(rng)
		out = append(out, lineage.ColumnLink{Table: r.Table, Column: r.Column, DependencyType: r.DependencyType, Assertion: rng.IntN(6) == 0})
	}
	return out
}

// parityContext is a ReaderContext as data, for both sides.
type parityContext struct {
	Files      map[string]string `json:"files"`
	Changed    []string          `json:"changed"`
	SQL        map[string]string `json:"sql"`
	ToProd     map[string]string `json:"toProd"`
	Deleted    []string          `json:"deleted"`
	Assertions []string          `json:"assertions"`
}

func randomContext(rng *rand.Rand) *parityContext {
	c := &parityContext{Files: map[string]string{}, SQL: map[string]string{}, ToProd: map[string]string{
		"dev.marts_ashish.orders": "p.marts.orders",
		"dev.marts_ashish.daily":  "p.marts.daily",
	}}
	for _, t := range tablePool {
		if strings.HasPrefix(t, "p.") && rng.IntN(2) == 0 {
			c.Files[t] = "definitions/" + ShortTable(t) + ".sqlx"
		}
		switch rng.IntN(8) {
		case 0:
			c.Changed = append(c.Changed, t)
			if rng.IntN(3) > 0 {
				c.SQL[t] = "SELECT " + pick(rng, columnPool) + " FROM `" + pick(rng, tablePool) + "`"
			}
		case 1:
			c.Deleted = append(c.Deleted, t)
		case 2:
			c.Assertions = append(c.Assertions, t)
		}
	}
	return c
}

func (c *parityContext) readerContext() ReaderContext {
	set := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	assertions := set(c.Assertions)
	return ReaderContext{
		ResolveFile: func(t string) string { return c.Files[t] },
		Changed:     set(c.Changed),
		SQLOf: func(t string) (string, bool) {
			s, ok := c.SQL[t]
			return s, ok
		},
		ToProd:      ToProdFunc(c.ToProd),
		Deleted:     set(c.Deleted),
		IsAssertion: func(t string) bool { return assertions[t] },
	}
}
