package cost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/impact"
)

func pastReport() *Report {
	started := time.Date(2026, 11, 17, 6, 0, 0, 0, time.Local)
	runs := []PastRun{
		{Name: "main.shop.daily", Type: "view"},
		{Name: "main.shop.orders", Type: "table", Ran: true, Started: started, Duration: 151 * time.Second, ReadBytes: 4 << 30, ReadRows: 1234567, Warehouse: "Prod jobs"},
		{Name: "main.shop.returns", Type: "table", New: true},
	}
	SortPastRuns(runs)
	return &Report{Comparison: &impact.Comparison{HeadRef: "feat", BaseRef: "origin/main", MergeBase: "abcdef1234", HeadLabel: "working tree"},
		Actions:  []Action{},
		PastRuns: &PastRuns{Source: "Databricks Query History", Target: "prod", Days: 30, Runs: runs}}
}

// A Databricks report shows each changed model's last run, as a past run
// with no dollar figure, and applies no gate.
func TestPastRunsReport(t *testing.T) {
	r := pastReport()
	if r.PastRuns.Runs[0].Name != "main.shop.orders" || r.PastRuns.Runs[2].Name != "main.shop.returns" {
		t.Errorf("order: %+v", r.PastRuns.Runs)
	}
	text := Text(r, Options{Threshold: 1 << 40})
	for _, want := range []string{
		"3 changed models in",
		"Last runs from Databricks Query History, target prod, in the last 30 days",
		"main.shop.orders   ran Nov 17 06:00 · 2m 31s · read 4.00 GiB, 1,234,567 rows · on Prod jobs",
		"main.shop.daily    no run in the last 30 days",
		"main.shop.returns  new on this branch: not run yet",
		"Last runs read 4.00 GiB between them (1 of 3 models)",
		"Gate not applied",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "$") {
		t.Errorf("a dollar figure:\n%s", text)
	}
	md := Markdown(r, Options{})
	if !strings.Contains(md, "| `main.shop.orders` | table | Nov 17 06:00 | 2m 31s | 4.00 GiB | 1,234,567 | Prod jobs |") || strings.Contains(md, "$") {
		t.Errorf("markdown:\n%s", md)
	}
	r.Unset = "dbtProdTarget"
	if md := Markdown(r, Options{}); !strings.Contains(md, "Set `dbtProdTarget` to show prod's") {
		t.Errorf("unset:\n%s", md)
	}
	data, err := JSON(r, Options{})
	var doc struct {
		PastRuns *PastRuns `json:"pastRuns"`
	}
	if err != nil || json.Unmarshal(data, &doc) != nil || doc.PastRuns == nil || len(doc.PastRuns.Runs) != 3 {
		t.Errorf("json: %s, %v", data, err)
	}
	if r.Exceeds(1) {
		t.Error("past runs tripped a gate")
	}
}

func TestPastRunsSnowflakeNoReadRows(t *testing.T) {
	started := time.Date(2026, 11, 17, 6, 0, 0, 0, time.Local)
	r := &Report{
		Actions: []Action{},
		PastRuns: &PastRuns{
			Source:        "Snowflake Query History",
			WarehouseUsed: "COMPUTE_WH",
			Target:        "prod",
			Days:          30,
			NoReadRows:    true,
			Runs: []PastRun{
				{Name: "analytics.xf_example.fct_orders", Type: "table", Ran: true, Started: started, Duration: 12 * time.Second, ReadBytes: 2 << 20, Warehouse: "PROD_WH"},
			},
		},
	}
	text := Text(r, Options{})
	if !strings.Contains(text, "read on virtual warehouse COMPUTE_WH") {
		t.Errorf("text missing warehouse note:\n%s", text)
	}
	if strings.Contains(text, "0 rows") {
		t.Errorf("text should omit 0 rows on Snowflake:\n%s", text)
	}
	md := Markdown(r, Options{})
	if strings.Contains(md, "Rows read") || !strings.Contains(md, "read on virtual warehouse `COMPUTE_WH`") {
		t.Errorf("markdown:\n%s", md)
	}
}
