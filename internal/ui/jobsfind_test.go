package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/project"
)

// A run table's order: rows with no number yet stay last whichever way a
// number column sorts, as on xf web's page (jstest/jobs.test.js).
func TestRunTableOrder(t *testing.T) {
	job := func(name, state string, bytes int64) project.RunAction {
		a := project.RunAction{Target: project.Target{Schema: "s", Name: name}, State: state}
		if bytes > 0 {
			a.Job = &project.JobStats{BytesBilled: bytes, CostUSD: float64(bytes) / 1e9}
		}
		return a
	}
	tb := runTable{report: &project.RunReport{Actions: []project.RunAction{
		job("calendar", "SUCCEEDED", 20), job("cars", "RUNNING", 0), job("holds", "FAILED", 40), job("orders", "PENDING", 0)}}}
	names := func() []string {
		var out []string
		for _, i := range tb.order() {
			out = append(out, tb.report.Actions[i].Target.Name)
		}
		return out
	}
	tb.sortCol, tb.sortDesc = "billed", true
	if got := names(); !slices.Equal(got, []string{"holds", "calendar", "cars", "orders"}) {
		t.Errorf("billed, biggest first = %v", got)
	}
	tb.sortDesc = false
	if got := names(); !slices.Equal(got, []string{"calendar", "holds", "cars", "orders"}) {
		t.Errorf("billed, smallest first = %v", got)
	}
	tb.sortCol, tb.query = "", "S.C"
	if got := names(); !slices.Equal(got, []string{"calendar", "cars"}) {
		t.Errorf("search s.c = %v", got)
	}
}

// Where the summary doesn't fit, the order, the job count and the times go
// before the cost.
func TestRunTableSummaryFits(t *testing.T) {
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	tb := runTable{sortCol: "slot", sortDesc: true, report: &project.RunReport{Started: start, Actions: []project.RunAction{
		{State: "SUCCEEDED", Job: &project.JobStats{BytesBilled: 3 << 30, CostUSD: 0.0183, SlotMs: 64000}},
		{State: "RUNNING"}, {State: "PENDING"}}}}
	s := newStyles(true)
	wide := ansi.Strip(tb.summary(s, 200, start.Add(time.Minute), ""))
	for _, want := range []string{"1/3 done", "▶ 1 running", "· 1 pending", "elapsed 1m 00s", "1 BigQuery job", "slot 1m 04s", "billed 3.00 GiB", "est. $0.0183", "by slot time, biggest first"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide summary %q lacks %q", wide, want)
		}
	}
	narrow := ansi.Strip(tb.summary(s, 60, start.Add(time.Minute), ""))
	if ansi.StringWidth(narrow) > 60 || !strings.Contains(narrow, "1/3 done") || !strings.Contains(narrow, "est. $0.0183") || strings.Contains(narrow, "by slot") {
		t.Errorf("narrow summary = %q", narrow)
	}
}
