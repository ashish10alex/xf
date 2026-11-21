package impact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

func TestCheckMultiTableInBranchSubstitution(t *testing.T) {
	// stg_orders adds new_col; fct_orders reads stg_orders.new_col and drops old_col.
	// Without multi-table substitution, fct_orders fails dry run against prod stg_orders.
	stg := &project.Action{
		ID:     "p.stg.orders",
		Target: project.Target{Database: "p", Schema: "stg", Name: "orders"},
		Kind:   project.KindTable,
		Query:  "SELECT id, amount AS new_col FROM `p.raw.orders`",
	}
	fct := &project.Action{
		ID:                "p.marts.fct_orders",
		Target:            project.Target{Database: "p", Schema: "marts", Name: "fct_orders"},
		Kind:              project.KindTable,
		DependencyTargets: []project.Target{stg.Target},
		Query:             "SELECT id, new_col FROM `p.stg.orders`",
	}
	g := project.NewGraph([]*project.Action{stg, fct})
	res := &changes.Result{
		HeadRef: "feat", BaseRef: "main", MergeBase: "abc1234",
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: stg.ID, Type: "table", FileName: "stg_orders.sqlx"},
				{ID: fct.ID, Type: "table", FileName: "fct_orders.sqlx"},
			},
		},
	}
	deps := Deps{
		Schema: func(_ context.Context, table string) ([]warehouse.Column, error) {
			switch table {
			case "p.stg.orders":
				return []warehouse.Column{{Name: "id", Type: "INT64"}}, nil
			case "p.marts.fct_orders":
				return []warehouse.Column{{Name: "id", Type: "INT64"}, {Name: "old_col", Type: "STRING"}}, nil
			}
			return nil, nil
		},
		DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
			// Raw reference to p.stg.orders fails because prod stg.orders has no new_col.
			if strings.Contains(sql, "`p.stg.orders`") {
				return nil, errors.New("Unrecognized name: new_col")
			}
			if strings.Contains(sql, "SELECT id, new_col FROM") && strings.Contains(sql, "AS new_col") {
				return &warehouse.Check{Schema: []warehouse.Column{{Name: "id", Type: "INT64"}, {Name: "new_col", Type: "INT64"}}}, nil
			}
			return &warehouse.Check{Schema: []warehouse.Column{{Name: "id", Type: "INT64"}, {Name: "new_col", Type: "INT64"}}}, nil
		},
		Readers: emptyReaders{},
	}
	rep, err := Run(context.Background(), res, g, deps, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unchecked) != 0 {
		t.Fatalf("Unchecked = %+v, want none (fct_orders should succeed via in-branch substitution)", rep.Unchecked)
	}
	if len(rep.AtRisk) != 1 || rep.AtRisk[0].Table != "p.marts.fct_orders" || len(rep.AtRisk[0].Columns) != 1 || rep.AtRisk[0].Columns[0].Column != "old_col" {
		t.Fatalf("AtRisk = %+v, want fct_orders dropping old_col", rep.AtRisk)
	}
}

func TestStaticDAGMultiHopSelectStarAndExcept(t *testing.T) {
	base := &project.Action{
		ID:     "p.stg.orders",
		Target: project.Target{Database: "p", Schema: "stg", Name: "orders"},
		Kind:   project.KindTable,
		Query:  "SELECT id FROM `p.raw.orders`",
	}
	vPass := &project.Action{
		ID:                "p.int.v_orders",
		Target:            project.Target{Database: "p", Schema: "int", Name: "v_orders"},
		Kind:              project.KindView,
		DependencyTargets: []project.Target{base.Target},
		Query:             "SELECT * FROM `p.stg.orders`",
	}
	vExcept := &project.Action{
		ID:                "p.int.v_clean",
		Target:            project.Target{Database: "p", Schema: "int", Name: "v_clean"},
		Kind:              project.KindView,
		DependencyTargets: []project.Target{base.Target},
		Query:             "SELECT * EXCEPT (discount) FROM `p.stg.orders`",
	}
	consumer := &project.Action{
		ID:                "p.marts.rpt_margins",
		Target:            project.Target{Database: "p", Schema: "marts", Name: "rpt_margins"},
		Kind:              project.KindTable,
		DependencyTargets: []project.Target{vPass.Target},
		Query:             "SELECT id, SUM(discount) AS total_disc FROM `p.int.v_orders` GROUP BY 1",
	}
	downstreamOfExcept := &project.Action{
		ID:                "p.marts.rpt_clean",
		Target:            project.Target{Database: "p", Schema: "marts", Name: "rpt_clean"},
		Kind:              project.KindTable,
		DependencyTargets: []project.Target{vExcept.Target},
		Query:             "SELECT id, discount FROM `p.int.v_clean`",
	}
	// dbt import CTE + arithmetic multiplication must NOT trigger SELECT * propagation.
	dbtImportCTE := &project.Action{
		ID:                "p.marts.dbt_cte",
		Target:            project.Target{Database: "p", Schema: "marts", Name: "dbt_cte"},
		Kind:              project.KindTable,
		DependencyTargets: []project.Target{base.Target},
		Query:             "WITH src AS (SELECT * FROM `p.stg.orders`) SELECT id, qty * price AS total FROM src WHERE qty * 2 > 0",
	}
	sfExclude := &project.Action{
		ID:                "p.int.sf_clean",
		Target:            project.Target{Database: "p", Schema: "int", Name: "sf_clean"},
		Kind:              project.KindView,
		DependencyTargets: []project.Target{base.Target},
		Query:             "SELECT * EXCLUDE (discount) FROM `p.stg.orders`",
	}
	g := project.NewGraph([]*project.Action{base, vPass, vExcept, consumer, downstreamOfExcept, dbtImportCTE, sfExclude})
	links := StaticDAGReaders(g, "p.stg.orders", "discount")
	got := map[string]lineage.DependencyType{}
	for _, l := range links {
		got[l.Table] = l.DependencyType
	}
	if got["p.int.v_orders"] != lineage.ExactCopy {
		t.Errorf("v_orders link = %q, want EXACT_COPY; all=%+v", got["p.int.v_orders"], links)
	}
	if got["p.marts.rpt_margins"] != lineage.Other {
		t.Errorf("rpt_margins multi-hop link = %q, want OTHER; all=%+v", got["p.marts.rpt_margins"], links)
	}
	if got["p.int.v_clean"] != lineage.Other {
		t.Errorf("v_clean uses SELECT * EXCEPT (discount) and should be OTHER (breaks on drop): %+v", links)
	}
	if got["p.int.sf_clean"] != lineage.Other {
		t.Errorf("sf_clean uses SELECT * EXCLUDE (discount) and should be OTHER (breaks on drop): %+v", links)
	}
	if _, ok := got["p.marts.rpt_clean"]; ok {
		t.Errorf("rpt_clean downstream of EXCEPT(discount) should not be reported: %+v", links)
	}
	if _, ok := got["p.marts.dbt_cte"]; ok {
		t.Errorf("dbt_cte (import CTE + arithmetic *) must NOT be reported as reading discount: %+v", links)
	}
}

func TestSwapBranchTablesIsDeterministicAcrossDAG(t *testing.T) {
	b := &project.Action{ID: "p.s.b", Target: project.Target{Database: "p", Schema: "s", Name: "b"}, Kind: project.KindTable, Query: "SELECT 1 AS id"}
	a := &project.Action{ID: "p.s.a", Target: project.Target{Database: "p", Schema: "s", Name: "a"}, Kind: project.KindTable, Query: "SELECT id FROM `p.s.b`"}
	g := project.NewGraph([]*project.Action{a, b})
	branch := map[project.ActionID]bool{a.ID: true, b.ID: true}
	readerSQL := "SELECT * FROM `p.s.a` JOIN `p.s.b` USING (id)"
	for range 50 {
		out, ok := swapBranchTables(readerSQL, "p.s.r", g, branch, 3)
		if !ok || strings.Contains(out, "`p.s.a`") || strings.Contains(out, "`p.s.b`") {
			t.Fatalf("left unswapped prod reference: ok=%v sql=%s", ok, out)
		}
	}
}

// partialReaders fails ColumnLinks for one extra project but still returns
// the links it found.
type partialReaders struct{}

func (partialReaders) ColumnLinks(context.Context, string, string) ([]lineage.ColumnLink, error) {
	return []lineage.ColumnLink{{Table: "p.rpt.linked", Column: "a"}}, errors.New("denied-bi: no access to Data Lineage")
}

func (partialReaders) TableOnlyReaders(_ context.Context, _ string, linked map[string]bool) ([]lineage.ColumnLink, error) {
	if !linked["p.rpt.linked"] {
		return nil, errors.New("linked readers not passed on")
	}
	return []lineage.ColumnLink{{Table: "p.rpt.tableonly", DependencyType: lineage.TableOnly}}, nil
}

func (partialReaders) TableReaders(context.Context, string) ([]lineage.ColumnLink, error) {
	return nil, nil
}

func TestColumnReadersKeepsTableOnlyReadersOnPartialError(t *testing.T) {
	links, err := columnReaders(context.Background(), partialReaders{}, "p.stg.orders", "a")
	if err == nil || !strings.Contains(err.Error(), "denied-bi") {
		t.Fatalf("err = %v, want the denied project's error", err)
	}
	got := map[string]bool{}
	for _, l := range links {
		got[l.Table] = true
	}
	if !got["p.rpt.linked"] || !got["p.rpt.tableonly"] {
		t.Fatalf("links = %+v, want both the column link and the table-only reader", links)
	}
}
