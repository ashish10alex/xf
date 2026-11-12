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
