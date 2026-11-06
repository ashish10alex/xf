package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/runhistory"
)

func target(schema, name string) project.Target {
	return project.Target{Database: "demo-project", Schema: schema, Name: name}
}

// testChanges is the Changed Actions of a branch: a changed table, a new
// view and a removed one.
func testChanges() *changes.Result {
	return &changes.Result{
		BaseRef: "origin/main", MergeBase: "abc1234def5678", DefaultBranch: "main", HeadRef: "feat/orders", Head: changes.HeadWorktree,
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: "demo-project.analytics.daily_orders", Target: target("analytics", "daily_orders"), FileName: "definitions/daily_orders.sqlx", Type: "table", Reasons: []changes.Reason{changes.ReasonSQL, changes.ReasonConfig}},
				{ID: "demo-project.staging.stg_customers", Target: target("staging", "stg_customers"), FileName: "definitions/stg_customers.sqlx", Type: "view", Reasons: []changes.Reason{changes.ReasonNew}},
			},
			Deleted: []changes.Deleted{{ID: "demo-project.analytics.legacy_orders", FileName: "definitions/legacy_orders.sqlx", Type: "view"}},
		},
	}
}

// xf web lists the Changed Actions as the R overlay does.
func TestChangedRows(t *testing.T) {
	rows := ChangedRows(testChanges())
	var got []string
	for _, r := range rows {
		got = append(got, strings.TrimSpace(strings.Join([]string{r.Kind, r.File, r.Mark, r.Label, r.Tail}, "|")))
	}
	want := []string{
		"file|definitions/daily_orders.sqlx|||",
		"action|definitions/daily_orders.sqlx|●|analytics.daily_orders|table · sql, config",
		"file|definitions/stg_customers.sqlx|||",
		"action|definitions/stg_customers.sqlx|●|staging.stg_customers|view · new",
		"removed|||removed on this branch, not run|",
		"file|definitions/legacy_orders.sqlx|||",
		"action|definitions/legacy_orders.sqlx|✗|analytics.legacy_orders|view",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !rows[len(rows)-1].Removed || rows[1].Removed {
		t.Error("only the removed actions' rows are marked removed")
	}
}

// xf web finds actions as the palette does.
func TestFindActions(t *testing.T) {
	g := project.NewGraph([]*project.Action{
		{ID: "p.a.orders", Target: target("a", "orders"), Kind: project.KindTable, FileName: "definitions/orders.sqlx"},
		{ID: "p.a.orders_view", Target: target("a", "orders_view"), Kind: project.KindView, FileName: "definitions/v.sqlx"},
		{ID: "p.b.customers", Target: target("b", "customers"), Kind: project.KindTable, FileName: "definitions/c.sqlx"},
	})
	found, bad := FindActions(g, "dataform", "ord", nil)
	if bad != "" || len(found) != 2 || found[0].ID != "p.a.orders" {
		t.Errorf("ord: %v %q", found, bad)
	}
	found, _ = FindActions(g, "dataform", "", []*project.Action{g.Actions["p.b.customers"]})
	if len(found) != 3 || found[0].ID != "p.b.customers" {
		t.Errorf("empty query with customers first: %v", found)
	}
	if _, bad = FindActions(g, "dataform", "$nope", nil); bad != "$nope" {
		t.Errorf("bad kind = %q", bad)
	}
}

func TestJobsAndHistoryRows(t *testing.T) {
	start := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	rep := &project.RunReport{State: "SUCCEEDED", Started: start, Ended: start.Add(90 * time.Second), Actions: []project.RunAction{
		{Target: project.Target{Schema: "s", Name: "t"}, State: "SUCCEEDED", Started: start, Ended: start.Add(3 * time.Second), JobID: "j1",
			JobURL: "https://job", Job: &project.JobStats{BytesBilled: 1 << 20, CostUSD: 0.01, SlotMs: 2000}},
		{Target: project.Target{Schema: "s", Name: "u"}, State: "FAILED", FailureReason: "boom\n  bad"},
	}}
	rows, total := JobsTable(rep, time.Time{})
	if len(rows) != 2 || rows[0].Target != "s.t" || rows[0].State != "✓ succeeded" || rows[0].Duration != "3.0s" ||
		rows[0].Billed != "1.00 MiB" || rows[0].Cost != "$0.0100" || rows[0].JobURL != "https://job" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].State != "✗ failed" || rows[1].Failure != "boom bad" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if total.Target != "Total (1 BigQuery job)" || total.Duration != "1m 30s" {
		t.Errorf("total = %+v", total)
	}
	if r := rows[0]; r.DurationMs != 3000 || r.SlotMs != 2000 || r.Bytes != 1<<20 || r.CostUSD != 0.01 {
		t.Errorf("row 0's numbers = %+v", r)
	}
	if s := SummarizeJobs(rep, time.Time{}); s != (JobsSummary{Actions: 2, Succeeded: 1, Failed: 1, Elapsed: "1m 30s"}) {
		t.Errorf("summary = %+v", s)
	}
	// While the run goes on: the time so far, from now.
	live := &project.RunReport{State: "RUNNING", Started: start, Actions: []project.RunAction{
		{State: "RUNNING", Started: start, JobID: "j"}, {State: "PENDING"}, {State: "SKIPPED"}}}
	if s := SummarizeJobs(live, start.Add(23*time.Second)); s != (JobsSummary{Actions: 3, Running: 1, Pending: 1, Skipped: 1, Elapsed: "23s"}) {
		t.Errorf("live summary = %+v", s)
	}
	// A running job's duration is still growing, so it sorts with the
	// jobs that have none yet.
	if rows, _ := JobsTable(live, start.Add(23*time.Second)); rows[0].Duration != "23s" || rows[0].DurationMs != 0 {
		t.Errorf("running row = %+v", rows[0])
	}
	h := HistoryRows([]runhistory.Entry{{ID: "x", Via: "cli", Started: start, Ended: start.Add(90 * time.Second), State: "SUCCEEDED", Report: rep, Cmdline: "dataform run"}},
		start.Add(time.Hour))
	if len(h) != 1 || h[0].When != "today 09:00" || h[0].Jobs != 1 || h[0].Duration != "1m 30s" || h[0].Title != "cli · today 09:00 · ✓ succeeded" {
		t.Errorf("history = %+v", h)
	}
}

// Outside a project, xf web's settings show the tools on PATH and the
// Google accounts, an account that must sign in again in amber.
func TestLandingInfoRows(t *testing.T) {
	version := func(v string, err error) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return v, err }
	}
	rows := LandingInfoRows(context.Background(), LandingInfo{
		Exe: "/bin/xf", Build: "dev",
		Tools: []LandingTool{
			{Name: "dataform", Bin: "/usr/local/bin/dataform", Note: "a project can have its own", Version: version("3.0.8", nil)},
			{Name: "dbt", Note: ""},
			{Name: "gcloud", Bin: "/opt/gcloud", Version: version("", errors.New("broken\nmore"))},
		},
		ADC: func(context.Context) gcpauth.ADC {
			return gcpauth.ADC{Account: "me@work.example", Type: "authorized_user", Source: "/creds.json", SourceFrom: "gcloud's default file"}
		},
		Gcloud: func(context.Context) gcpauth.CLI {
			return gcpauth.CLI{Account: "me@work.example", SignInError: "Reauthentication failed.", Reauth: true}
		},
	})
	want := []InfoRow{
		{Title: "binaries"}, {Label: "xf", Value: "/bin/xf"}, {Value: "dev"},
		{Label: "dataform", Value: "/usr/local/bin/dataform", Note: "a project can have its own"}, {Value: "3.0.8"},
		{Label: "dbt", Value: "(not on PATH)"},
		{Label: "gcloud", Value: "/opt/gcloud"}, {Value: "(unknown) broken"},
		{Title: "google cloud"},
		{Label: "xf uses", Value: "me@work.example"}, {Value: "user login"},
		{Value: "/creds.json", Note: "gcloud's default file"}, {Label: "quota", Value: "(no quota project)"},
		{Label: "gcloud", Value: "me@work.example (reauthentication required)", Warn: true},
		{Value: gcpauth.CLILoginHint, Note: "xf's own calls still work; bq and gcloud commands don't", Warn: true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows =\n%+v\nwant\n%+v", rows, want)
	}
}

// The project's info comes at once, with "…" for each lookup still
// going: a slow gcloud holds up only its own row.
func TestProjectInfoStream(t *testing.T) {
	gcloud := make(chan struct{})
	deps := Deps{Root: "/work/proj", Info: &ProjectInfo{
		ToolBin:     "/bin/dataform",
		ToolVersion: func(context.Context) (string, error) { return "3.0.8", nil },
		Gcloud: func(context.Context) gcpauth.CLI {
			<-gcloud
			return gcpauth.CLI{Account: "me@work.example", SignInError: "Reauthentication failed.", Reauth: true}
		},
	}}
	value := func(rows []InfoRow, label string) string {
		for _, r := range rows {
			if r.Label == label {
				return r.Value
			}
		}
		return "(none)"
	}
	var sent [][]InfoRow
	versionIn := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ProjectInfoStream(context.Background(), deps, "", false, func(rows []InfoRow) {
			sent = append(sent, rows)
			for _, r := range rows {
				if r.Value == "3.0.8" && len(sent) == 2 {
					close(versionIn)
				}
			}
		})
	}()
	select {
	case <-versionIn:
	case <-time.After(5 * time.Second):
		t.Fatal("the version never came while gcloud was still going")
	}
	if got := value(sent[0], "gcloud"); got != "…" {
		t.Errorf("first rows' gcloud = %q, want …", got)
	}
	if got := value(sent[1], "gcloud"); got != "…" {
		t.Errorf("gcloud = %q while it is still going, want …", got)
	}
	close(gcloud)
	<-done
	if len(sent) != 3 {
		t.Fatalf("sent %d times, want 3 (at once, version, gcloud)", len(sent))
	}
	last := sent[2]
	if got := value(last, "gcloud"); !strings.Contains(got, "reauthentication required") {
		t.Errorf("gcloud = %q, want reauthentication required", got)
	}
	if !reflect.DeepEqual(last, ProjectInfoRows(context.Background(), deps, "", false)) {
		t.Error("the last rows sent differ from ProjectInfoRows")
	}
}

// Outside a project the first rows come before any lookup lands.
func TestLandingInfoStreamFirst(t *testing.T) {
	var first []InfoRow
	LandingInfoStream(context.Background(), LandingInfo{
		Exe: "/bin/xf", Build: "dev",
		Tools: []LandingTool{{Name: "dataform", Bin: "/bin/dataform", Version: func(context.Context) (string, error) { return "3.0.8", nil }}},
		ADC:   func(context.Context) gcpauth.ADC { return gcpauth.ADC{} },
	}, func(rows []InfoRow) {
		if first == nil {
			first = rows
		}
	})
	want := []InfoRow{
		{Title: "binaries"}, {Label: "xf", Value: "/bin/xf"}, {Value: "dev"},
		{Label: "dataform", Value: "/bin/dataform"}, {Value: "…"},
		{Title: "google cloud"}, {Label: "xf uses", Value: "…"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("first rows =\n%+v\nwant\n%+v", first, want)
	}
}

// A cost report whose every action failed to estimate has no rows, as
// [], not null: xf web's cost tab could not draw null.
func TestCostReportNoEstimates(t *testing.T) {
	v := CostReport(&cost.Report{Actions: []cost.Action{{Name: "p.d.orders", Type: "table", Reason: "dry run failed"}}})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"rows":[]`) || v.Empty != "" || v.NotEstimated == nil || v.NotEstimated.Count != 1 {
		t.Errorf("report is %s, want no rows as [] and the action under others", b)
	}
}

// A first build and a table read from dev are noted on the row, and how
// many tables were read from dev is said once above.
func TestCostReportExplain(t *testing.T) {
	v := CostReport(&cost.Report{DryRunIn: "acme-prod (EU)",
		Comparison: &impact.Comparison{HeadRef: "feat_x", BaseRef: "origin/develop", MergeBase: "abc1234", HeadLabel: "working tree"},
		Actions:    []cost.Action{{Name: "p.marts.agg", Type: "table", Estimate: &cost.Estimate{Bytes: 1 << 30}}}})
	want := "The actions came from <code>feat_<wbr>x</code> diffed against <code>develop</code>, and their queries, on the branch and at the merge-base, " +
		"were dry run against the live tables in <code>acme-prod</code> (EU); nothing was run."
	if v.Explain != want {
		t.Errorf("Explain:\n got %s\nwant %s", v.Explain, want)
	}
}

func TestCostReportFirstBuildAndFromDev(t *testing.T) {
	checked := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	read := cost.DevRead{Table: "prod.marts.agg", Dev: "dev.marts_dev.agg", BuiltAt: checked.Add(-12 * time.Minute)}
	v := CostReport(&cost.Report{CheckedAt: checked, Actions: []cost.Action{
		{Name: "p.marts.agg", Type: "incremental", New: true, Estimate: &cost.Estimate{Bytes: 1 << 30}, FirstBuild: true, Incremental: &cost.Estimate{Bytes: 1 << 28}},
		{Name: "p.marts.report", Type: "table", Estimate: &cost.Estimate{Bytes: 1 << 20}, Base: &cost.Estimate{Bytes: 3 << 30}, FromDev: []cost.DevRead{read}},
		{Name: "p.marts.other", Type: "table", Estimate: &cost.Estimate{Bytes: 1 << 10}, FromDev: []cost.DevRead{read}},
	}})
	rows := map[string]CostRow{}
	for _, r := range v.Rows {
		rows[r.Action] = r
	}
	agg := rows["marts.agg"]
	if agg.Notes != "new: its first run is a full build, counted; incremental runs sized on dev's copy" || agg.Counted != cost.RunFull ||
		agg.Full != (CostFigure{"1.00 GiB", "$0.006"}) || agg.Incremental != (CostFigure{"256.00 MiB", "$0.002"}) {
		t.Errorf("first build %+v", agg)
	}
	rep := rows["marts.report"]
	if rep.Notes != "reads dev's marts_dev.agg (built 12m ago)" || rep.Change != "-3.00 GiB (-$0.018)" || rep.Was != "3.00 GiB · $0.018" ||
		rep.Counted != cost.RunFull || rep.Incremental != (CostFigure{}) {
		t.Errorf("report %+v", rep)
	}
	if v.Later != "Each run after the first: 257.00 MiB | $0.002" {
		t.Errorf("Later %q", v.Later)
	}
	if v.FromDev != "defer to prod: 1 new table read from dev, sized as dev has it" {
		t.Errorf("FromDev %q", v.FromDev)
	}
	if !strings.Contains(v.Markdown, "reads `marts.agg` from dev") || !strings.Contains(v.Markdown, "Defer to prod: 1 new table") {
		t.Errorf("markdown %s", v.Markdown)
	}
}

func TestSummarizeJobsCountsJobsNotReadYet(t *testing.T) {
	r := &project.RunReport{Actions: []project.RunAction{
		{State: "SUCCEEDED", JobID: "a"},
		{State: "SUCCEEDED", JobID: "b", Job: &project.JobStats{}},
		{State: "FAILED", JobID: "c", JobErr: "not found"},
		{State: "RUNNING", JobID: "d"},
		{State: "SKIPPED"},
	}}
	if got := SummarizeJobs(r, time.Time{}).Reading; got != 1 {
		t.Errorf("Reading = %d, want only the finished action whose job is neither read nor refused", got)
	}
}

func TestJobsTableSaysWhichNumbersAreKnown(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	r := &project.RunReport{Actions: []project.RunAction{
		{Target: target("s", "cached"), State: "SUCCEEDED", JobID: "a", Started: t0, Ended: t0, Job: &project.JobStats{}},
		{Target: target("s", "unread"), State: "SUCCEEDED", JobID: "b"},
	}}
	rows, _ := JobsTable(r, time.Time{})
	if !rows[0].HasJob || !rows[0].HasDuration || rows[1].HasJob || rows[1].HasDuration {
		t.Errorf("rows = %+v", rows)
	}
}
