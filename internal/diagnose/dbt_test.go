package diagnose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// dbtProject is the dbt example with its graph from a fixture: dbt-v2.json
// (Fusion) or dbt-core.json.
func dbtProject(t *testing.T, fixture string) navigate.Project {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dbt"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testdata(t, "examples", fixture))
	if err != nil {
		t.Fatal(err)
	}
	g, err := (&dbt.Backend{}).Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return navigate.Project{Root: root, Graph: g, Dbt: true}
}

func TestDryRunDbtPlacesErrors(t *testing.T) {
	tests := []struct {
		name               string
		file               string
		rule               rule
		wantText, wantLine string
	}{
		{"column in literal SQL", "models/marts/dim_customers.sql",
			rule{when: "lifetime_value", token: "amount) as lifetime_value", msg: "Unrecognized name: amount; Did you mean amounts?"}, "amount", "    sum(p.amount) as lifetime_value"},
		{"a missing table, with no position, lands on its ref", "models/marts/dim_customers.sql",
			rule{when: "stg_payments", msg: "Not found: Table alex-personal-dev-01:xf_example.stg_payments was not found in location EU"}, "{{ ref('stg_payments') }}", "  left join {{ ref('stg_payments') }}"},
		{"inside a macro's output: the macro call", "models/staging/stg_payments.sql",
			rule{when: "payment_method", token: "/ 100", msg: "No matching signature for operator /"}, "{{ cents_to_dollars('amount') }}", "  {{ cents_to_dollars('amount') }} as amount"},
		{"a model that inlines an ephemeral one", "models/marts/customer_segments_v2.sql",
			rule{when: "segment", token: "cc.country_name\nfrom", msg: "Unrecognized name: cc"}, "cc", "  cc.country_name"},
		{"in an inlined ephemeral model: its ref()", "models/marts/customer_segments_v2.sql",
			rule{when: "segment", msg: "Not found: Dataset alex-personal-dev-01:xf_example_reference was not found in location EU"}, "{{ ref('int_customer_countries') }}", "left join {{ ref('int_customer_countries') }}"},
		{"after the if block", "models/marts/fct_orders.sql",
			rule{when: "group by 1, 2, 3, 4", token: "group by 1, 2, 3, 4", msg: "Syntax error: bad group"}, "group", "group by 1, 2, 3, 4"},
		{"a singular test", "tests/assert_positive_order_totals.sql",
			rule{when: "status = 'completed'", token: "amount < 0", msg: "Unrecognized name: amount"}, "amount", "where status = 'completed' and amount < 0"},
	}
	for _, fixture := range []string{"dbt-v2.json", "dbt-core.json"} {
		p := dbtProject(t, fixture)
		for _, tt := range tests {
			t.Run(fixture+"/"+tt.name, func(t *testing.T) {
				file := filepath.Join(p.Root, filepath.FromSlash(tt.file))
				src := read(t, file)
				f := &fakeDryRun{rules: []rule{tt.rule}}
				var errs []Diagnostic
				for _, d := range DryRunFile(context.Background(), p, file, src, f.run) {
					if d.Severity == Error {
						errs = append(errs, d)
					}
				}
				if len(errs) != 1 {
					t.Fatalf("got %v (queries %q)", renderAll(src, errs), f.queries)
				}
				d := errs[0]
				if d.Line != lineOf(t, src, tt.wantLine) {
					t.Errorf("line %d, want %d: %s", d.Line, lineOf(t, src, tt.wantLine), render(src, d))
				}
				if got := string(lineText(src, d.Line)[d.Start:d.End]); got != tt.wantText {
					t.Errorf("marks %q, want %q", got, tt.wantText)
				}
			})
		}
	}
}

// A dbt file's dry runs send the SQL the TUI and xf web do, and have the
// same cost hint as a Dataform file's.
func TestDryRunDbtSendsActionSQL(t *testing.T) {
	p := dbtProject(t, "dbt-v2.json")
	checked := 0
	for _, a := range p.Graph.Actions {
		if filepath.Ext(a.FileName) != ".sql" {
			continue
		}
		file := filepath.Join(p.Root, filepath.FromSlash(a.FileName))
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f := &fakeDryRun{}
		ds := DryRunFile(context.Background(), p, file, src, f.run)
		qs := a.DryRunQueries()
		for _, q := range qs {
			if !slices.Contains(f.queries, q.SQL) {
				t.Errorf("%s: did not send %q", a.ID, q.SQL)
			}
			checked++
		}
		if len(qs) == 0 && len(f.queries) > 0 {
			t.Errorf("%s (%s): dry-ran %q", a.ID, a.Kind, f.queries)
		}
		if len(qs) > 0 && (len(ds) != 1 || render(src, ds[0]) != "0 hint "+`"`+string(lineText(src, 0))+`"`+": Dry run: 1.00 GB processed (≈ $0.0061)") {
			t.Errorf("%s: got %q", a.ID, renderAll(src, ds))
		}
	}
	if checked < 8 {
		t.Errorf("only %d queries checked", checked)
	}
}

// SQL from before the file's latest compile may not be the file's: it isn't
// dry-run.
func TestDryRunDbtSkipsUncompiledSQL(t *testing.T) {
	p := dbtProject(t, "dbt-v2.json")
	file := filepath.Join(p.Root, "models", "marts", "dim_customers.sql")
	for _, state := range []project.SQLState{project.SQLRaw, project.SQLOutdated} {
		for _, a := range p.Graph.Actions {
			a.SQL = state
		}
		f := &fakeDryRun{}
		if ds := DryRunFile(context.Background(), p, file, read(t, file), f.run); len(ds) != 0 || len(f.queries) != 0 {
			t.Errorf("state %d: got %v, dry-ran %q", state, ds, f.queries)
		}
	}
}
