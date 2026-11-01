package impact

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

func dbtModel(db, schema, name, fqn string, kind project.ActionKind) *project.Action {
	t := project.Target{Database: db, Schema: schema, Name: name}
	return &project.Action{ID: t.ID(), Target: t, CanonicalTarget: t, Kind: kind, RunName: fqn}
}

func TestRetargetNamesTheChangesAsTheOtherTargetDoes(t *testing.T) {
	dev := project.NewGraph([]*project.Action{
		dbtModel("dev", "xf_dev", "dim_customers", "p.marts.dim_customers", project.KindTable),
		dbtModel("dev", "xf_dev_rpt", "revenue", "p.reporting.revenue", project.KindTable),
		dbtModel("dev", "xf_dev", "not_null_dim_customers_id", "p.marts.not_null_dim_customers_id", project.KindTest),
		dbtModel("raw", "raw", "orders", "", project.KindSource),
	})
	prod := project.NewGraph([]*project.Action{
		dbtModel("prod", "xf", "dim_customers", "p.marts.dim_customers", project.KindTable),
		dbtModel("prod", "xf_rpt", "revenue", "p.reporting.revenue", project.KindTable),
		dbtModel("prod", "xf", "not_null_dim_customers_id", "p.marts.not_null_dim_customers_id", project.KindTest),
		dbtModel("raw", "raw", "orders", "", project.KindSource),
	})
	res := &changes.Result{MergeBase: "abc", Diff: changes.Diff{
		Changed: []changes.Changed{
			{ID: "dev.xf_dev.dim_customers", FileName: "models/marts/dim_customers.sql", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}},
			{ID: "dev.xf_dev.not_null_dim_customers_id", Type: "test", Parent: "dev.xf_dev.dim_customers"},
			{ID: "dev.xf_dev.brand_new", Type: "table"},
		},
		Deleted: []changes.Deleted{{ID: "dev.xf_dev_rpt.old_report", Type: "table"}, {ID: "elsewhere.ds.gone", Type: "view"}},
	}}

	got, toProd := Retarget(res, dev, prod)
	if c := got.Changed[0]; c.ID != "prod.xf.dim_customers" || c.Target.Database != "prod" || c.FileName != "models/marts/dim_customers.sql" {
		t.Errorf("changed model: %+v", c)
	}
	if c := got.Changed[1]; c.ID != "prod.xf.not_null_dim_customers_id" || c.Parent != "prod.xf.dim_customers" {
		t.Errorf("test: %+v", c)
	}
	// Not in the other compile: kept.
	if c := got.Changed[2]; c.ID != "dev.xf_dev.brand_new" {
		t.Errorf("unknown action: %+v", c)
	}
	// Guessed from revenue, which shared its dataset.
	if d := got.Deleted[0]; d.ID != "prod.xf_rpt.old_report" {
		t.Errorf("deleted: %+v", d)
	}
	if d := got.Deleted[1]; d.ID != "elsewhere.ds.gone" {
		t.Errorf("deleted with no dataset to go by: %+v", d)
	}
	if toProd["dev.xf_dev.dim_customers"] != "prod.xf.dim_customers" || toProd["dev.xf_dev_rpt.old_report"] != "prod.xf_rpt.old_report" {
		t.Errorf("readers' map: %v", toProd)
	}
	if _, ok := toProd["raw.raw.orders"]; ok {
		t.Error("a source the targets share is mapped")
	}
	// res itself is left alone.
	if res.Changed[0].ID != "dev.xf_dev.dim_customers" || res.Deleted[0].ID != "dev.xf_dev_rpt.old_report" {
		t.Error("Retarget changed its input")
	}
}

// The dbt check, end to end on fakes: the changes retargeted to the prod
// compile are dry run there, compared with prod's tables, and a reader that
// is a dev run of one of the project's models folds into its prod table.
func TestRunOnAProdTargetCompile(t *testing.T) {
	devDim := dbtModel("dev", "xf_dev", "dim_customers", "p.marts.dim_customers", project.KindTable)
	dev := project.NewGraph([]*project.Action{devDim, dbtModel("dev", "xf_dev_rpt", "revenue", "p.reporting.revenue", project.KindTable)})
	prodDim := dbtModel("prod", "xf", "dim_customers", "p.marts.dim_customers", project.KindTable)
	prodDim.Query = "select customer_id from `prod`.`raw`.`customers`"
	revenue := dbtModel("prod", "xf_rpt", "revenue", "p.reporting.revenue", project.KindTable)
	revenue.Query = "select email from `prod`.`xf`.`dim_customers`"
	prod := project.NewGraph([]*project.Action{prodDim, revenue})
	res := &changes.Result{BaseRef: "origin/main", MergeBase: "abc", HeadRef: "feat", Head: changes.HeadWorktree, Diff: changes.Diff{
		Changed: []changes.Changed{{ID: devDim.ID, FileName: "models/marts/dim_customers.sql", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL}}},
	}}

	res, runs := Retarget(res, dev, prod)
	var dryRunIn []string
	var mu sync.Mutex
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]bigquery.Field, error) {
			if table == "prod.xf.dim_customers" {
				return []bigquery.Field{f("customer_id", "INT64"), f("email", "STRING")}, nil
			}
			return nil, nil
		},
		DryRun: func(_ context.Context, project, _, _ string) (*bigquery.DryRun, error) {
			mu.Lock()
			dryRunIn = append(dryRunIn, project)
			mu.Unlock()
			return &bigquery.DryRun{Schema: []bigquery.Field{f("customer_id", "INTEGER")}}, nil
		},
		Readers: &fakeSource{columnLinks: map[string][]lineage.ColumnLink{"prod.xf.dim_customers#email": {
			{Table: "prod.ext.customer_emails", Column: "email", DependencyType: lineage.ExactCopy},
			{Table: "dev.xf_dev_rpt.revenue", Column: "email", DependencyType: lineage.ExactCopy},
		}}},
		ToProd: ToProdFunc(runs),
	}
	report, err := Run(context.Background(), res, prod, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AtRisk) != 1 || report.AtRisk[0].Table != "prod.xf.dim_customers" {
		t.Fatalf("at risk %+v", report.AtRisk)
	}
	col := report.AtRisk[0].Columns
	if len(col) != 1 || col[0].Column != "email" || col[0].Change.Kind != Dropped {
		t.Fatalf("columns %+v", col)
	}
	if got := readerTables(col[0].Readers); !slices.Contains(got, "prod.ext.customer_emails") || !slices.Contains(got, "prod.xf_rpt.revenue") || slices.Contains(got, "dev.xf_dev_rpt.revenue") {
		t.Errorf("readers %v: the dev run should fold into prod's revenue", got)
	}
	if !slices.Equal(dryRunIn, []string{"prod"}) || !slices.Equal(report.Against, []string{"prod"}) {
		t.Errorf("dry run in %v, against %v", dryRunIn, report.Against)
	}
}
