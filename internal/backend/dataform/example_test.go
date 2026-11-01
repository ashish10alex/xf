package dataform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// The example project (examples/dataform), compiled by `just
// fixtures-examples`. TestExamplesParity in package backend compares what
// it shares with the dbt example; this covers what only Dataform has.
func TestExampleProject(t *testing.T) {
	g := loadGraph(t, "examples/dataform.json")
	if g.HasErrors() {
		t.Fatalf("errors: %+v", g.Errors)
	}
	get := func(id project.ActionID) *project.Action {
		t.Helper()
		a := g.Actions[id]
		if a == nil {
			t.Fatalf("no action %s", id)
		}
		return a
	}
	if a := get("alex-personal-dev-01.xf_example.legacy_orders"); !a.Disabled {
		t.Error("legacy_orders isn't disabled")
	}
	for _, c := range []string{"gb", "us"} {
		a := get(project.ActionID("alex-personal-dev-01.reporting.orders_" + c))
		if a.Kind != project.KindView || a.FileName != "definitions/reports/orders_by_country.js" || !strings.Contains(a.Query, `country_code = "`+c+`"`) {
			t.Errorf("generated view orders_%s = %+v", c, a)
		}
	}
	audit := get("alex-personal-dev-01.xf_example.audit_log")
	if audit.Kind != project.KindOperation || !audit.HasOutput || len(audit.Queries) != 2 {
		t.Errorf("audit_log = %+v", audit)
	}
	if udf := get("alex-personal-dev-01.xf_example.create_udf"); !strings.Contains(udf.Queries[0], "`alex-personal-dev-01.xf_example.cents_to_dollars`") {
		t.Errorf("create_udf = %q", udf.Queries)
	}
	if a := get("alex-personal-dev-01.xf_example_assertions.assert_positive_order_totals"); a.Kind != project.KindAssertion || a.ParentAction != nil {
		t.Errorf("manual assertion = %+v", a)
	}
	if a := get("alex-personal-dev-01.xf_example_assertions.xf_example_dim_customers_assertions_uniqueKey_0"); a.ParentAction == nil || a.ParentAction.Name != "dim_customers" {
		t.Errorf("generated assertion = %+v", a)
	}
	fct := get("alex-personal-dev-01.xf_example.fct_orders")
	if !fct.HasIncrementalVariant() || !strings.Contains(fct.IncrementalQuery, "WHERE o.order_date > since") || strings.Contains(fct.Query, "WHERE o.order_date > since") {
		t.Errorf("fct_orders variants: full %q, incremental %q", fct.Query, fct.IncrementalQuery)
	}
	if a := get("alex-personal-dev-01.xf_example.stg_payments"); !strings.Contains(a.Query, "ROUND(amount / 100, 2)") {
		t.Errorf("stg_payments doesn't use the include: %q", a.Query)
	}
	if a := get("alex-personal-dev-01.reporting.revenue_report"); a.Kind != project.KindTable {
		t.Errorf("revenue_report = %+v", a)
	}
}

func TestExampleBrokenProject(t *testing.T) {
	g := loadGraph(t, "examples/dataform-broken.json")
	if len(g.Errors) == 0 {
		t.Fatal("no errors")
	}
	for _, e := range g.Errors {
		if e.FileName != "definitions/order_totals.sqlx" || !strings.Contains(e.Message, "order_lines") {
			t.Errorf("error = %+v", e)
		}
	}
}

// TestE2EExampleProject compiles examples/dataform with the dataform CLI
// on PATH. It runs only with XF_E2E=1.
func TestE2EExampleProject(t *testing.T) {
	if os.Getenv("XF_E2E") == "" {
		t.Skip("set XF_E2E=1 to run against the real dataform CLI")
	}
	bin, err := exec.LookPath("dataform")
	if err != nil {
		t.Skip("no dataform on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "dataform"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Backend{Bin: bin}.Compile(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if g.HasErrors() || len(g.Actions) != 20 {
		t.Errorf("%d actions, errors %+v; want 20 and none", len(g.Actions), g.Errors)
	}
}
