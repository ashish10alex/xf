package frontend

import (
	"reflect"
	"testing"

	"github.com/ashish10alex/xf/internal/cost"
)

func TestCostSkippedSaysWhatEachMissingTableIs(t *testing.T) {
	notFound := func(table string) string {
		return "dry run failed: Not found: Table prod:" + table + " was not found in location EU"
	}
	// Compiled with prod's options: the branch's actions are named in dev.
	r := &cost.Report{Actions: []cost.Action{
		{Name: "dev.marts.orders", DryRunAs: "prod.marts.orders", Type: "table", Estimate: &cost.Estimate{Bytes: 1}},
		{Name: "dev.checks.a_new_assertions_rowConditions", DryRunAs: "prod.checks.a_new_assertions_rowConditions", Type: "assertion", Reason: notFound("marts.a_new")},
		{Name: "dev.checks.a_new_assertions_uniqueKey_0", DryRunAs: "prod.checks.a_new_assertions_uniqueKey_0", Type: "assertion", Reason: notFound("marts.a_new")},
		{Name: "dev.checks.gone", DryRunAs: "prod.checks.gone", Type: "assertion", Reason: notFound("raw.gone")},
		{Name: "dev.marts.a_new", DryRunAs: "prod.marts.a_new", Type: "table", New: true, Reason: "BigQuery gave no estimate"},
		{Name: "dev.marts.b", DryRunAs: "prod.marts.b", Type: "table", Reason: notFound("marts.a_new")},
		{Name: "dev.marts.c", DryRunAs: "prod.marts.c", Type: "view", Reason: notFound("marts.orders")},
	}}
	got := costSkipped(r)
	want := &CostSkipped{
		Count: 6,
		Chips: []CostChip{{"2 read a missing table", "missing"}, {"3 wait on a new table", "new"}, {"1 gave no byte count", "other"}},
		Where: "prod (EU)",
		Missing: []CostMissingRow{
			{Action: "checks.gone", Type: "assertion", Reads: "raw.gone", Status: "missing", Note: "not on this branch: check it", Kind: "missing"},
			{Action: "marts.c", Type: "view", Reads: "marts.orders", Status: "missing", Note: "changed on this branch", Kind: "missing"},
		},
		Waiting: []CostWaitingGroup{
			{
				Reads:      "marts.a_new",
				Count:      3,
				Actions:    []CostWaitingAction{{Action: "marts.b", Type: "table"}},
				Assertions: []string{"checks.a_new_assertions_rowConditions", "checks.a_new_assertions_uniqueKey_0"},
			},
		},
		Waits: 3,
		Other: []CostOtherGroup{
			{
				Why:     noBytes,
				Actions: []CostOtherAction{{Action: "marts.a_new", Type: "table", New: true}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if costSkipped(&cost.Report{Actions: r.Actions[:1]}) != nil {
		t.Error("a report with every action estimated has a not-estimated section")
	}
}

// An action that waits on two new tables is under each, and counted once.
func TestCostSkippedUnderEveryNewTable(t *testing.T) {
	r := &cost.Report{Actions: []cost.Action{
		{Name: "p.marts.a_new", Type: "table", New: true, Estimate: &cost.Estimate{Bytes: 1}},
		{Name: "p.marts.b_new", Type: "table", New: true, Estimate: &cost.Estimate{Bytes: 1}},
		{Name: "p.marts.out", Type: "table", New: true, Waits: []string{"p.marts.a_new", "p.marts.b_new"},
			Reason: "dry run failed: Not found: Table p:marts.b_new was not found in location EU"},
	}}
	got := costSkipped(r)
	out := []CostWaitingAction{{Action: "marts.out", Type: "table"}}
	want := []CostWaitingGroup{{Reads: "marts.b_new", Count: 1, Actions: out}, {Reads: "marts.a_new", Count: 1, Actions: out}}
	if got.Count != 1 || got.Waits != 1 || !reflect.DeepEqual(got.Waiting, want) || got.Chips[0].Text != "1 waits on a new table" {
		t.Errorf("got %+v", got)
	}
}

// Missing tables in more than one place keep their project.
func TestCostSkippedInMoreThanOnePlace(t *testing.T) {
	r := &cost.Report{Actions: []cost.Action{
		{Name: "p.marts.a", Type: "table", Reason: "dry run failed: Not found: Table p:raw.x was not found in location EU"},
		{Name: "p.marts.b", Type: "table", Reason: "dry run failed: Not found: Table q:raw.y was not found in location US"},
	}}
	got := costSkipped(r)
	if got.Where != "" || got.Missing[0].Reads != "p.raw.x" || got.Missing[1].Reads != "q.raw.y" {
		t.Errorf("got %+v", got)
	}
}

// Actions waiting on a column the branch adds are grouped by the column,
// apart from the dry runs that failed for another reason.
func TestCostSkippedGroupsActionsWaitingOnANewColumn(t *testing.T) {
	week2 := &cost.AddedColumn{Table: "acme.marts.combined", Column: "week2"}
	r := &cost.Report{Actions: []cost.Action{
		{Name: "acme.marts.dates", Type: "table", Reason: "dry run failed: Unrecognized name: week2", AddedColumn: week2},
		{Name: "acme.marts.weekly", Type: "view", Reason: "dry run failed: Unrecognized name: week2", AddedColumn: &cost.AddedColumn{Table: "acme.marts.combined", Column: "week2"}},
		{Name: "acme.marts.typo", Type: "table", Reason: "dry run failed: Unrecognized name: wek2"},
	}}
	got := costSkipped(r)
	wantColumns := []CostColumnGroup{{Column: "week2", Table: "marts.combined", Actions: []CostWaitingAction{{Action: "marts.dates", Type: "table"}, {Action: "marts.weekly", Type: "view"}}}}
	if !reflect.DeepEqual(got.Columns, wantColumns) {
		t.Errorf("columns %+v", got.Columns)
	}
	if want := []CostChip{{"2 wait on a new column", "new"}, {"1 couldn't be dry run", "other"}}; !reflect.DeepEqual(got.Chips, want) {
		t.Errorf("chips %+v", got.Chips)
	}
	if len(got.Other) != 1 || got.Other[0].Actions[0].Action != "marts.typo" {
		t.Errorf("other %+v", got.Other)
	}
}
