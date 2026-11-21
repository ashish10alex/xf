package check

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/impact"
)

const gib = int64(1) << 30

var comparison = &impact.Comparison{HeadRef: "feat", BaseRef: "origin/main", MergeBase: "1125280abc", HeadLabel: "working tree"}

// dropped is an impact report with a dropped column that a table still
// reads.
func dropped() *impact.Report {
	return &impact.Report{Comparison: comparison, ChangedCount: 1, AtRisk: []impact.Table{{
		Table: "acme.marts.orders", FileName: "definitions/orders.sqlx", Type: "table",
		Columns: []impact.Column{{Column: "discount", Change: impact.ColumnChange{Kind: impact.Dropped},
			Readers: []impact.Reader{{Table: "acme.finance.margins", Column: "discount", FilePath: "definitions/margins.sqlx"}}}},
	}}}
}

func clean() *impact.Report { return &impact.Report{Comparison: comparison, ChangedCount: 1} }

// costs is a cost report of one action scanning bytes, base at the
// merge-base.
func costs(bytes, base int64) *cost.Report {
	return &cost.Report{Comparison: comparison, Actions: []cost.Action{{Name: "acme.marts.orders", FileName: "definitions/orders.sqlx", Type: "table",
		Estimate: &cost.Estimate{Bytes: bytes}, Base: &cost.Estimate{Bytes: base}}}}
}

var opts = Options{QuietUnder: DefaultQuietUnder}

func TestWorth(t *testing.T) {
	unfiltered := costs(gib, gib)
	unfiltered.Actions[0].Unfiltered = []cost.Unfiltered{{Table: "acme.raw.events", Column: "event_date"}}
	unestimated := costs(gib, gib)
	unestimated.Actions = append(unestimated.Actions, cost.Action{Name: "acme.ops.audit", Type: "operations", Reason: "a script"})
	noBase := costs(2*gib, 0)
	noBase.BaseError = "no compile"
	unchecked := clean()
	unchecked.Unchecked = []impact.Unchecked{{Table: "acme.marts.x", Reason: "Access Denied"}}
	retypedLow := clean()
	retypedLow.AtRisk = []impact.Table{{Table: "acme.marts.x", Columns: []impact.Column{{Column: "c", Change: impact.ColumnChange{Kind: impact.TypeChanged}}}}}
	for _, c := range []struct {
		name string
		r    Report
		want bool
	}{
		{"nothing", Report{Impact: clean(), Cost: costs(gib, gib)}, false},
		{"a change nothing reads", Report{Impact: retypedLow, Cost: costs(gib, gib)}, false},
		{"small growth", Report{Impact: clean(), Cost: costs(gib+100<<20, gib)}, false},
		{"growth of 1 GiB", Report{Impact: clean(), Cost: costs(2*gib, gib)}, true},
		{"dropped column read", Report{Impact: dropped(), Cost: costs(gib, gib)}, true},
		{"missing partition filter", Report{Impact: clean(), Cost: unfiltered}, true},
		{"not estimated", Report{Impact: clean(), Cost: unestimated}, true},
		{"no merge-base, total over", Report{Impact: clean(), Cost: noBase}, true},
		{"impact failed", Report{ImpactError: "no lineage", Cost: costs(gib, gib)}, true},
		{"a model not checked", Report{Impact: unchecked, Cost: costs(gib, gib)}, true},
		{"no changes", Report{Impact: &impact.Report{}, Cost: &cost.Report{}}, false},
		{"partial missing base with zero growth on big model", Report{Impact: clean(), Cost: &cost.Report{Actions: []cost.Action{
			{Name: "acme.marts.big", Estimate: &cost.Estimate{Bytes: 500 * gib}, Base: &cost.Estimate{Bytes: 500 * gib}},
			{Name: "acme.marts.small", Estimate: &cost.Estimate{Bytes: 10 << 20}},
		}}}, false},
		{"past runs small", Report{Impact: clean(), Cost: &cost.Report{PastRuns: &cost.PastRuns{
			Runs: []cost.PastRun{{Name: "a", Ran: true, ReadBytes: 10 << 20}},
		}}}, false},
		{"past runs over QuietUnder", Report{Impact: clean(), Cost: &cost.Report{PastRuns: &cost.PastRuns{
			Runs: []cost.PastRun{{Name: "a", Ran: true, ReadBytes: 2 * gib}},
		}}}, true},
		{"past runs truncated", Report{Impact: clean(), Cost: &cost.Report{PastRuns: &cost.PastRuns{
			Truncated: true,
		}}}, true},
		{"past runs why", Report{Impact: clean(), Cost: &cost.Report{PastRuns: &cost.PastRuns{
			Why: "query comment is off",
		}}}, true},
	} {
		if got := c.r.Worth(opts); got != c.want {
			t.Errorf("%s: Worth = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVerdictAndCode(t *testing.T) {
	r := &Report{Project: ".", Impact: dropped(), Cost: costs(4*1024*gib+200*gib, 200*gib)}
	if got, want := Verdict(r, opts), "🚨 1 dropped column still read · +4.00 TiB per run (≈ +$25.00) · 4.20 TiB total"; got != want {
		t.Errorf("Verdict = %q, want %q", got, want)
	}
	if r.Code(opts) != OK {
		t.Error("report only should exit 0")
	}
	gated := Options{Gate: true, FailOn: impact.SeverityCritical}
	if r.Code(gated) != Tripped {
		t.Error("--fail-on critical should trip")
	}
	gated = Options{Cost: cost.Options{Threshold: 100 * gib}}
	if r.Code(gated) != Tripped || !strings.Contains(Verdict(r, gated), "❌ over the 100.00 GiB limit") {
		t.Errorf("--fail-over should trip: %q", Verdict(r, gated))
	}
	half := &Report{ImpactError: "Data Lineage API is off", Cost: costs(gib, 2*gib)}
	if got, want := Verdict(half, opts), "❔ Column impact couldn't be checked · -1.00 GiB per run (≈ -$0.006) · 1.00 GiB total"; got != want {
		t.Errorf("Verdict = %q, want %q", got, want)
	}
	notChecked := clean()
	notChecked.Unchecked = []impact.Unchecked{{Table: "acme.marts.x", Reason: "Access Denied"}}
	if got := Verdict(&Report{Impact: notChecked, Cost: &cost.Report{}}, opts); got != "❔ 1 model not checked · nothing to dry run" {
		t.Errorf("Verdict = %q", got)
	}
	if half.Code(opts) != OK {
		t.Error("one half failing should exit 0")
	}
	if (&Report{ImpactError: "x", CostError: "y"}).Code(opts) != Failed {
		t.Error("neither should exit 2")
	}
	md := Markdown(half, opts)
	for _, want := range []string{"### xf check\n", "❔ Column impact couldn't be checked: Data Lineage API is off", "<details><summary>Query cost</summary>", "#### Query Cost"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q:\n%s", want, md)
		}
	}
	if Title("analytics/dataform") != "xf check · `analytics/dataform`" {
		t.Error(Title("analytics/dataform"))
	}
}

func TestJSON(t *testing.T) {
	data, err := JSON(&Report{Project: ".", Impact: dropped(), CostError: "no credentials"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version   int            `json:"version"`
		Worth     bool           `json:"worthAComment"`
		Impact    map[string]any `json:"impact"`
		Cost      any            `json:"cost"`
		CostError string         `json:"costError"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || !doc.Worth || doc.Impact["worst"] != "critical" || doc.Cost != nil || doc.CostError != "no credentials" {
		t.Errorf("JSON = %s", data)
	}
}

func TestPlan(t *testing.T) {
	worth := &Report{Project: "analytics", Impact: dropped(), Cost: costs(gib, gib)}
	quiet := &Report{Project: "analytics", Impact: clean(), Cost: costs(gib, gib)}
	failed := &Report{Project: "analytics", ImpactError: "compile errors", CostError: "compile errors"}
	marker := Marker("analytics")

	if a, _ := Plan(quiet, opts, "abc1234ff", ""); a != Leave {
		t.Error("quiet and no comment: leave")
	}
	a, body := Plan(worth, opts, "abc1234ff", "")
	if a != Post || !IsOurs(body, marker) || !strings.Contains(body, "🚨 1 dropped column") {
		t.Fatalf("worth: %v %q", a, body)
	}
	if IsOurs(body, Marker("other")) {
		t.Error("another project's marker matched")
	}
	if a, _ := Plan(worth, opts, "abc1234ff", body); a != Leave {
		t.Error("the same body: leave")
	}
	a, resolved := Plan(quiet, opts, "def5678", body)
	if a != Edit || !strings.Contains(resolved, "✅ Resolved") {
		t.Errorf("quiet with a comment: %v %q", a, resolved)
	}
	if a, _ := Plan(quiet, opts, "def5678", resolved); a != Leave {
		t.Error("already resolved: leave")
	}
	if a, _ := Plan(failed, opts, "0001112", ""); a != Leave {
		t.Error("failed with no comment: leave")
	}
	a, fail := Plan(failed, opts, "0001112", body)
	if a != Edit || !strings.Contains(fail, "Couldn't check this push (0001112):** compile errors") ||
		!strings.Contains(fail, "<details><summary>Last report, of abc1234</summary>") || !strings.Contains(fail, "🚨 1 dropped column") {
		t.Fatalf("failed: %v\n%s", a, fail)
	}
	// Failing again keeps the report it folded, not the failure.
	_, again := Plan(failed, opts, "0009999", fail)
	if strings.Count(again, "Last report") != 1 || !strings.Contains(again, "Last report, of abc1234") || !strings.Contains(again, "🚨 1 dropped column") {
		t.Errorf("failed twice:\n%s", again)
	}
	// After a resolved comment there is no report to fold.
	_, afterResolved := Plan(failed, opts, "0009999", resolved)
	if strings.Contains(afterResolved, "Last report") {
		t.Errorf("failed after resolved:\n%s", afterResolved)
	}
}

type files map[string][2]string // file: text, diff

func (f files) Text(file string) string { return f[file][0] }
func (f files) Diff(file string) string { return f[file][1] }

func TestNotes(t *testing.T) {
	src := files{"definitions/orders.sqlx": {
		"config { type: \"table\" }\nselect\n  id,\n  amount\nfrom ${ref(\"events\")}\n",
		"diff --git a/definitions/orders.sqlx b/definitions/orders.sqlx\n--- a/definitions/orders.sqlx\n+++ b/definitions/orders.sqlx\n@@ -3 +3 @@ select\n-  id\n+  id,\n@@ -5 +4,0 @@\n-  discount,\n",
	}}
	c := costs(gib, gib)
	c.Actions[0].Unfiltered = []cost.Unfiltered{{Table: "acme.raw.events", Column: "event_date"}}
	r := &Report{Impact: dropped(), Cost: c}
	notes := Notes(r, opts, src, "analytics")
	if len(notes) != 2 {
		t.Fatalf("notes = %+v", notes)
	}
	if n := notes[0]; n.Level != Warning || n.File != "analytics/definitions/orders.sqlx" || n.Line != 4 ||
		n.Text != "This branch drops discount from marts.orders, which finance.margins (definitions/margins.sqlx) reads." {
		t.Errorf("dropped: %+v", n)
	}
	if n := notes[1]; n.Level != Warning || n.Line != 5 || n.Check != "xf/missing-partition-filter" {
		t.Errorf("partition: %+v", n)
	}
	gated := Notes(r, Options{Gate: true, FailOn: impact.SeverityCritical, Cost: cost.Options{Threshold: 1}}, src, "")
	if gated[0].Level != Error || gated[0].File != "definitions/orders.sqlx" || gated[len(gated)-1].Check != "xf/cost-limit" || gated[len(gated)-1].File != "" {
		t.Errorf("gated: %+v", gated)
	}

	gh := GitHub([]Note{{Level: Warning, File: "a,b.sqlx", Line: 3, Title: "T: x", Text: "50% off\nnow"}, {Level: Error, Text: "over"}})
	if want := "::warning file=a%2Cb.sqlx,line=3,title=T%3A x::50%25 off%0Anow\n::error::over\n"; gh != want {
		t.Errorf("GitHub = %q, want %q", gh, want)
	}
	data, err := CodeQuality(gated)
	if err != nil {
		t.Fatal(err)
	}
	var issues []struct {
		Severity string `json:"severity"`
		Location struct {
			Path  string `json:"path"`
			Lines struct{ Begin int }
		} `json:"location"`
	}
	if err := json.Unmarshal(data, &issues); err != nil || len(issues) != 2 || issues[0].Severity != "critical" || issues[0].Location.Lines.Begin != 4 {
		t.Errorf("CodeQuality = %s (%v)", data, err)
	}
}

// A drop that breaks an incremental table's own run is an error to fix
// with nothing reading it downstream; uniqueKey's note points at the
// config that still names the column.
func TestNotesOnADropThatBreaksTheIncrementalRun(t *testing.T) {
	src := files{"definitions/inventory.sqlx": {
		"config {\n  type: \"incremental\",\n  uniqueKey: [\"sku\", \"row_num\"]\n}\nselect\n  sku,\n  amount\nfrom x\n",
		"diff --git a/definitions/inventory.sqlx b/definitions/inventory.sqlx\n--- a/definitions/inventory.sqlx\n+++ b/definitions/inventory.sqlx\n@@ -8,2 +7,0 @@\n-  note,\n-  row_num\n",
	}}
	r := &Report{Impact: &impact.Report{Comparison: comparison, ChangedCount: 1, AtRisk: []impact.Table{{
		Table: "acme.marts.inventory", FileName: "definitions/inventory.sqlx", Type: "incremental",
		Columns: []impact.Column{
			{Column: "row_num", Change: impact.ColumnChange{Kind: impact.Dropped, BreaksRun: impact.BreaksRunUniqueKey}, Readers: []impact.Reader{}},
			{Column: "note", Change: impact.ColumnChange{Kind: impact.Dropped, BreaksRun: impact.BreaksRunTable},
				Readers: []impact.Reader{{Table: "acme.finance.margins", Column: "note", FilePath: "definitions/margins.sqlx"}}},
		},
	}}}}
	notes := Notes(r, Options{Gate: true, FailOn: impact.SeverityCritical}, src, "")
	if len(notes) != 2 {
		t.Fatalf("notes = %+v", notes)
	}
	if n := notes[0]; n.Level != Error || n.Line != 3 || n.Title != "Dropped column still in uniqueKey" ||
		n.Text != "This branch drops row_num from marts.inventory, but its uniqueKey still names it, so every incremental run fails, after a full refresh too: take row_num out of uniqueKey." {
		t.Errorf("uniqueKey: %+v", n)
	}
	if n := notes[1]; n.Level != Error || n.Line != 7 || n.Title != "Dropped column breaks the incremental run" ||
		!strings.HasSuffix(n.Text, "until a full refresh rebuilds it. finance.margins (definitions/margins.sqlx) reads it too.") {
		t.Errorf("table: %+v", n)
	}
	if v := impactVerdict(r); !strings.HasPrefix(v, "🚨 2 dropped columns") {
		t.Errorf("verdict %q", v)
	}
	onlyOwn := &Report{Impact: &impact.Report{Comparison: comparison, ChangedCount: 1, AtRisk: []impact.Table{{
		Table: "acme.marts.inventory", FileName: "definitions/inventory.sqlx", Type: "incremental",
		Columns: []impact.Column{
			{Column: "row_num", Change: impact.ColumnChange{Kind: impact.Dropped, BreaksRun: impact.BreaksRunUniqueKey}, Readers: []impact.Reader{}},
		},
	}}}}
	if got, want := impactVerdict(onlyOwn), "🚨 1 dropped column breaks the incremental run"; got != want {
		t.Errorf("breaksRun-only verdict = %q, want %q", got, want)
	}
	delAndOwn := &Report{Impact: &impact.Report{Comparison: comparison, ChangedCount: 2, AtRisk: []impact.Table{
		onlyOwn.Impact.AtRisk[0],
		{Table: "acme.marts.old", Deleted: true, Readers: []impact.Reader{{Table: "acme.fin.r"}}},
	}}}
	if got, want := impactVerdict(delAndOwn), "🚨 1 dropped column breaks the incremental run, and 1 deleted table still read"; got != want {
		t.Errorf("deleted+breaksRun verdict = %q, want %q", got, want)
	}
	allThree := &Report{Impact: &impact.Report{Comparison: comparison, ChangedCount: 2, AtRisk: []impact.Table{
		r.Impact.AtRisk[0],
		{Table: "acme.marts.old", Deleted: true, Readers: []impact.Reader{{Table: "acme.fin.r"}}},
	}}}
	if got, want := impactVerdict(allThree), "🚨 1 dropped column and 1 deleted table still read, and 1 dropped column breaks the incremental run"; got != want {
		t.Errorf("dropped+deleted+breaksRun verdict = %q, want %q", got, want)
	}
}

func TestLines(t *testing.T) {
	if got := namingLine("select\n  my_discount,\n  Discount\n", "discount"); got != 3 {
		t.Errorf("namingLine = %d, want the whole word on 3", got)
	}
	if got := namingLine("select 1", "discount"); got != 1 {
		t.Errorf("namingLine none = %d", got)
	}
	diff := "@@ -10,2 +10,2 @@\n-  a,\n-  b\n+  a\n+  c\n@@ -20 +19,0 @@\n-  discount\n"
	if got := removedLine(diff, "b"); got != 10 {
		t.Errorf("removedLine b = %d", got)
	}
	if got := removedLine(diff, "discount"); got != 19 {
		t.Errorf("removedLine discount = %d", got)
	}
	if got := removedLine(diff, "nothing"); got != 1 {
		t.Errorf("removedLine none = %d", got)
	}
	// Dotted nested STRUCT fields fall back to the leaf field name.
	if got := namingLine("select\n  struct(id, zip) as user\n", "user.zip"); got != 2 {
		t.Errorf("namingLine nested = %d, want 2", got)
	}
	if got := removedLine("@@ -4 +4,0 @@\n-  zip\n", "user.zip"); got != 4 {
		t.Errorf("removedLine nested = %d, want 4", got)
	}
}

func TestFindPR(t *testing.T) {
	event := t.TempDir() + "/event.json"
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":42,"head":{"sha":"abc"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	pr, err := FindPR(env(map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_PATH": event, "GITHUB_REPOSITORY": "o/r",
		"GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com", "GITHUB_TOKEN": "gh"}), os.ReadFile)
	if err != nil || pr.Number != 42 || pr.Commit != "abc" || pr.Token != "gh" || pr.Repo.Kind != forge.GitHub || pr.Repo.Path != "o/r" || pr.Repo.API != "https://api.github.com" {
		t.Errorf("GitHub: %+v, %v", pr, err)
	}
	push := t.TempDir() + "/push.json"
	_ = os.WriteFile(push, []byte(`{"ref":"refs/heads/main"}`), 0o644)
	if _, err := FindPR(env(map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_PATH": push, "GITHUB_TOKEN": "gh"}), os.ReadFile); !errors.Is(err, ErrNoPR) {
		t.Errorf("push: %v", err)
	}
	pr, err = FindPR(env(map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "7", "CI_SERVER_URL": "https://gitlab.example.com",
		"CI_PROJECT_PATH": "g/p", "CI_COMMIT_SHA": "def", "XF_FORGE_TOKEN": "gl"}), os.ReadFile)
	if err != nil || pr.Number != 7 || pr.Repo.Kind != forge.GitLab || pr.Repo.API != "https://gitlab.example.com/api/v4" || pr.Token != "gl" {
		t.Errorf("GitLab: %+v, %v", pr, err)
	}
	pr, err = FindPR(env(map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "7", "CI_JOB_TOKEN": "job",
		"CI_COMMIT_REF_PROTECTED": "false", "CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": "feat", "CI_ENVIRONMENT_NAME": "gcp/prod"}), os.ReadFile)
	if !errors.Is(err, ErrNoToken) || pr == nil || pr.Number != 7 {
		t.Fatalf("GitLab without a token: %+v, %v", pr, err)
	}
	for _, want := range []string{"XF_FORGE_TOKEN", "Protected", "`feat` isn't", "`*` or `gcp/prod`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("GitLab without a token: %q lacks %q", err, want)
		}
	}
	_, err = FindPR(env(map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "7", "CI_COMMIT_REF_PROTECTED": "true"}), os.ReadFile)
	if msg := err.Error(); strings.Contains(msg, "Protected") || !strings.Contains(msg, "this job has none") {
		t.Errorf("GitLab on a protected branch, no environment: %q", msg)
	}
	if _, err := FindPR(env(map[string]string{}), os.ReadFile); err == nil {
		t.Error("outside CI should fail")
	}
}

// Attention leaves out what needs no look in both reports; without it,
// as the comment is made, they are whole.
func TestMarkdownWithAttention(t *testing.T) {
	im := dropped()
	im.ChangedCount = 2
	im.Safe = []impact.Table{{Table: "acme.marts.daily", Type: "view", Columns: []impact.Column{}}}
	r := &Report{Impact: im, Cost: costs(gib, gib)}
	o := Options{QuietUnder: DefaultQuietUnder}
	if md := Markdown(r, o); !strings.Contains(md, "`daily`") || strings.Contains(md, "Not shown") {
		t.Errorf("whole:\n%s", md)
	}
	o.Attention = true
	md := Markdown(r, o)
	for _, want := range []string{"`orders`", "Not shown: 1 more model,", "Not shown: 1 more action,"} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in\n%s", want, md)
		}
	}
	if strings.Contains(md, "`daily`") {
		t.Errorf("daily listed:\n%s", md)
	}
}

func TestPhase3GatesAndInlineNotes(t *testing.T) {
	// 1. --fail-on unknown trips on SeverityUnknown and promotes its note to Error.
	unkReport := &Report{
		Impact: &impact.Report{
			Comparison:   comparison,
			ChangedCount: 1,
			AtRisk: []impact.Table{{
				Table:    "acme.marts.orders",
				FileName: "definitions/orders.sqlx",
				Type:     "table",
				Columns: []impact.Column{{
					Column:       "discount",
					Change:       impact.ColumnChange{Kind: impact.Dropped, From: "FLOAT64"},
					ReadersError: "lineage unavailable",
				}},
			}},
		},
	}
	if unkReport.ImpactTripped(Options{Gate: true, FailOn: impact.SeverityWarning}) {
		t.Error("--fail-on warning should not trip on SeverityUnknown")
	}
	if !unkReport.ImpactTripped(Options{Gate: true, FailOn: impact.SeverityUnknown}) {
		t.Error("--fail-on unknown should trip on SeverityUnknown")
	}
	unkNotes := Notes(unkReport, Options{Gate: true, FailOn: impact.SeverityUnknown}, files{}, "")
	if len(unkNotes) != 1 || unkNotes[0].Level != Error {
		t.Errorf("--fail-on unknown notes = %+v, want 1 Error", unkNotes)
	}
	warnNotes := Notes(unkReport, Options{Gate: true, FailOn: impact.SeverityWarning}, files{}, "")
	if len(warnNotes) != 1 || warnNotes[0].Level != Notice {
		t.Errorf("--fail-on warning notes for unknown = %+v, want 1 Notice", warnNotes)
	}

	// 2. --fail-on-unchecked ignores permanent structural entries (scripts/unbuilt tables)
	// and trips only on real check failures, emitting file-level xf/unchecked notes.
	permanentUnchecked := &Report{
		Impact: &impact.Report{
			Comparison:   comparison,
			ChangedCount: 3,
			Unchecked: []impact.Unchecked{
				{Table: "acme.ops.script", FileName: "definitions/script.sqlx", Reason: "operation: a dry run of a script has no schema"},
				{Table: "acme.marts.empty", FileName: "definitions/empty.sqlx", Reason: "nothing to dry run"},
				{Table: "acme.marts.unbuilt", FileName: "definitions/unbuilt.sqlx", Reason: "no table in acme yet"},
			},
		},
	}
	if permanentUnchecked.ImpactTripped(Options{FailOnUnchecked: true}) {
		t.Error("--fail-on-unchecked should not trip on permanent structural unchecked entries")
	}
	if got := Notes(permanentUnchecked, Options{FailOnUnchecked: true}, files{}, ""); len(got) != 0 {
		t.Errorf("permanent unchecked notes = %+v, want none", got)
	}

	uncheckedReport := &Report{
		Impact: &impact.Report{
			Comparison:   comparison,
			ChangedCount: 2,
			Unchecked: []impact.Unchecked{
				{Table: "acme.ops.script", FileName: "definitions/script.sqlx", Reason: "operation: a dry run of a script has no schema"},
				{Table: "acme.marts.broken", FileName: "definitions/broken.sqlx", Reason: "dry run failed\ndetails"},
			},
		},
	}
	if uncheckedReport.ImpactTripped(Options{}) {
		t.Error("unchecked model should not trip without FailOnUnchecked")
	}
	if !uncheckedReport.ImpactTripped(Options{FailOnUnchecked: true}) {
		t.Error("unchecked model should trip with FailOnUnchecked")
	}
	ucNotes := Notes(uncheckedReport, Options{FailOnUnchecked: true}, files{}, "warehouse")
	if len(ucNotes) != 1 || ucNotes[0].Level != Error || ucNotes[0].Check != "xf/unchecked" || ucNotes[0].File != "warehouse/definitions/broken.sqlx" {
		t.Errorf("unchecked notes = %+v", ucNotes)
	}

	// 3. --fail-over-delta, --fail-on-unfiltered, --fail-on-unestimated: verdict + notes + CodeQuality.
	costReport := &Report{
		Impact: &impact.Report{Comparison: comparison},
		Cost: &cost.Report{
			Comparison: comparison,
			Actions: []cost.Action{
				{
					Name:       "acme.marts.big",
					FileName:   "definitions/big.sqlx",
					Type:       "table",
					Estimate:   &cost.Estimate{Bytes: 20 * gib},
					Base:       &cost.Estimate{Bytes: 5 * gib},
					Unfiltered: []cost.Unfiltered{{Table: "acme.raw.events", Column: "event_date"}},
				},
				{
					Name:     "acme.marts.tiny_growth",
					FileName: "definitions/tiny.sqlx",
					Type:     "table",
					Estimate: &cost.Estimate{Bytes: 5*gib + 1},
					Base:     &cost.Estimate{Bytes: 5 * gib},
				},
				{
					Name:     "acme.marts.medium_growth",
					FileName: "definitions/medium.sqlx",
					Type:     "table",
					Estimate: &cost.Estimate{Bytes: 7 * gib},
					Base:     &cost.Estimate{Bytes: 5 * gib},
				},
				{
					Name:     "acme.marts.smaller",
					FileName: "definitions/smaller.sqlx",
					Type:     "table",
					Estimate: &cost.Estimate{Bytes: 2 * gib},
					Base:     &cost.Estimate{Bytes: 4 * gib},
				},
				{
					Name:     "acme.marts.missing",
					FileName: "definitions/missing.sqlx",
					Type:     "table",
					Reason:   "not found",
				},
			},
		},
	}
	co := Options{
		QuietUnder: DefaultQuietUnder,
		Cost: cost.Options{
			DeltaThreshold:    10 * gib,
			FailOnUnfiltered:  true,
			FailOnUnestimated: true,
		},
	}
	if !costReport.CostTripped(co) || costReport.Code(co) != Tripped {
		t.Fatalf("expected CostTripped and Tripped exit code")
	}
	verdict := Verdict(costReport, co)
	for _, want := range []string{"growth over the 10.00 GiB limit", "❌ 1 query without a partition filter", "1 action not estimated"} {
		if !strings.Contains(verdict, want) {
			t.Errorf("Verdict = %q, missing %q", verdict, want)
		}
	}
	if strings.Contains(verdict, "⚠️ 1 query without a partition filter") {
		t.Errorf("Verdict should fold unfiltered warning into ❌ when gated: %q", verdict)
	}
	notes := Notes(costReport, co, files{}, "")
	var deltaNotes []Note
	byCheck := map[string]Note{}
	for _, n := range notes {
		byCheck[n.Check] = n
		if n.Check == "xf/cost-delta" {
			deltaNotes = append(deltaNotes, n)
		}
	}
	for _, checkName := range []string{"xf/missing-partition-filter", "xf/not-estimated", "xf/cost-delta-limit"} {
		n, ok := byCheck[checkName]
		if !ok {
			t.Errorf("missing note %q in %+v", checkName, notes)
			continue
		}
		if n.Level != Error {
			t.Errorf("note %q level = %q, want error", checkName, n.Level)
		}
	}
	// Tiny +1 B growth is excluded; big (+15 GiB) is Error, medium (+2 GiB < 10/2 GiB) is Warning.
	if len(deltaNotes) != 2 ||
		deltaNotes[0].File != "definitions/big.sqlx" || deltaNotes[0].Level != Error ||
		deltaNotes[1].File != "definitions/medium.sqlx" || deltaNotes[1].Level != Warning {
		t.Errorf("xf/cost-delta notes = %+v", deltaNotes)
	}
	cq, err := CodeQuality(notes)
	if err != nil {
		t.Fatalf("CodeQuality: %v", err)
	}
	if !strings.Contains(string(cq), `"xf/cost-delta"`) || strings.Contains(string(cq), `"xf/cost-delta-limit"`) {
		t.Errorf("CodeQuality output unexpected:\n%s", cq)
	}

	// 4. --fail-over-delta with BaseError says no merge-base in verdict and notes.
	noBaseReport := &Report{
		Impact: &impact.Report{Comparison: comparison},
		Cost: &cost.Report{
			Comparison: comparison,
			BaseError:  "merge-base compile failed",
			Actions: []cost.Action{{
				Name:     "acme.marts.big",
				FileName: "definitions/big.sqlx",
				Type:     "table",
				Estimate: &cost.Estimate{Bytes: 70 * gib},
			}},
		},
	}
	noBaseOpts := Options{QuietUnder: DefaultQuietUnder, Cost: cost.Options{DeltaThreshold: 50 * gib}}
	if v := Verdict(noBaseReport, noBaseOpts); !strings.Contains(v, "no merge-base: total over the 50.00 GiB delta limit") {
		t.Errorf("no-base verdict = %q", v)
	}
	nbNotes := Notes(noBaseReport, noBaseOpts, files{}, "")
	if len(nbNotes) != 2 || !strings.Contains(nbNotes[0].Text, "no merge-base") || !strings.Contains(nbNotes[1].Text, "No merge-base estimate") {
		t.Errorf("no-base notes = %+v", nbNotes)
	}
}

func TestPastRunsUnfilteredWorthVerdictAndNotes(t *testing.T) {
	src := files{"models/orders.sql": {
		"select id\nfrom shop.raw.events\n",
		"",
	}}
	r := &Report{
		Impact: clean(),
		Cost: &cost.Report{
			Comparison: comparison,
			PastRuns: &cost.PastRuns{
				Source: "Snowflake Query History",
				Target: "prod",
				Days:   30,
				Runs: []cost.PastRun{{
					Name:       "shop.marts.orders",
					FileName:   "models/orders.sql",
					Type:       "table",
					Ran:        true,
					ReadBytes:  10 << 20, // well under QuietUnder
					Unfiltered: []cost.Unfiltered{{Table: "shop.raw.events", Column: "EVENT_DATE"}},
				}},
			},
		},
	}
	if !r.Worth(opts) {
		t.Error("PastRuns with unfiltered table should be Worth a comment")
	}
	if v := Verdict(r, opts); !strings.Contains(v, "⚠️ 1 query without a partition/cluster filter") {
		t.Errorf("Verdict = %q", v)
	}
	notes := Notes(r, opts, src, "")
	if len(notes) != 1 || notes[0].Check != "xf/missing-partition-filter" || notes[0].Level != Warning || notes[0].Line != 2 {
		t.Errorf("Notes = %+v", notes)
	}
	gated := Options{QuietUnder: DefaultQuietUnder, Cost: cost.Options{FailOnUnfiltered: true}}
	if !r.CostTripped(gated) || r.Code(gated) != Tripped {
		t.Errorf("FailOnUnfiltered should trip on PastRuns")
	}
	if v := Verdict(r, gated); !strings.Contains(v, "❌ 1 query without a partition/cluster filter") {
		t.Errorf("gated Verdict = %q", v)
	}
	if gn := Notes(r, gated, src, ""); len(gn) != 1 || gn[0].Level != Error {
		t.Errorf("gated Notes = %+v, want Error", gn)
	}
}
