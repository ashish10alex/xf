package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/impact"
)

func TestVerdictUnsetAndDryRuns(t *testing.T) {
	r := &Report{Project: ".", Impact: &impact.Report{Unset: "prodCompilerOptions", DryRunIn: "acme-dev"}, Cost: &cost.Report{DryRunIn: "acme-dev (EU)"}}
	if v := Verdict(r, Options{}); !strings.HasSuffix(v, "· ⚠️ compared with the default targets, which may be dev tables (`prodCompilerOptions` isn't set)") {
		t.Errorf("verdict %q", v)
	}
	if md := Markdown(r, Options{}); !strings.Contains(md, "Column impact dry ran the branch in `acme-dev` · query cost in `acme-dev (EU)`") {
		t.Errorf("markdown:\n%s", md)
	}
	r.Impact.Unset = ""
	if v := Verdict(r, Options{}); strings.Contains(v, "default targets") {
		t.Errorf("set: %q", v)
	}
}

// goodProbes is a project whose every probe passes: Dataform with
// @dataform/core installed, prod options naming acme-prod.
func goodProbes() Probes {
	return Probes{
		Bin: "/usr/bin/dataform", CoreNeeded: true, CoreVersion: "3.0.50",
		Base: "origin/main",
		Git: func(args ...string) (string, error) {
			switch args[0] {
			case "rev-parse":
				if args[1] == "--is-shallow-repository" {
					return "false", nil
				}
				return "abc", nil
			case "merge-base":
				return "1125280aaaa", nil
			}
			return "", errors.New("unexpected")
		},
		Prod:           Prod{Key: "prodCompilerOptions", Set: true, Value: "--default-database=acme-prod", Source: ".xf/config.json"},
		DefaultProject: "acme-dev", ProdProject: "acme-prod", Location: "EU",
		ADC: func(context.Context) gcpauth.ADC {
			return gcpauth.ADC{Type: "external_account", Account: "xf-check@acme.iam.gserviceaccount.com", SourceFrom: "$GOOGLE_APPLICATION_CREDENTIALS"}
		},
		DryRun:  func(context.Context, string, string) error { return nil },
		Lineage: func(context.Context, string, string) error { return nil },
	}
}

func line(t *testing.T, p *Preflight, name, contains string) Line {
	t.Helper()
	for _, l := range p.Lines {
		if l.Name == name && strings.Contains(l.Detail, contains) {
			return l
		}
	}
	t.Fatalf("no %s line with %q in:\n%s", name, contains, p.Text())
	return Line{}
}

func TestPreflightPasses(t *testing.T) {
	var probed []string
	p := goodProbes()
	p.DryRun = func(_ context.Context, project, location string) error {
		probed = append(probed, project+" "+location)
		return nil
	}
	p.Lineage = func(_ context.Context, project, location string) error {
		probed = append(probed, "lineage "+project+" "+location)
		return nil
	}
	pre := Run(context.Background(), p)
	if pre.Stops() {
		t.Fatalf("stops:\n%s", pre.Text())
	}
	for _, l := range pre.Lines {
		if l.Status != Pass {
			t.Errorf("%s is %s: %s", l.Name, l.Status, l.Detail)
		}
	}
	if got := strings.Join(probed, "|"); got != "acme-dev EU|acme-prod EU|lineage acme-prod eu" {
		t.Errorf("probed %q", got)
	}
	if pre.ImpactIn != "acme-dev" || pre.CostIn != "acme-prod" {
		t.Errorf("dry runs in %q and %q", pre.ImpactIn, pre.CostIn)
	}
	line(t, pre, "Google", "workload identity federation, as xf-check@acme.iam.gserviceaccount.com (from $GOOGLE_APPLICATION_CREDENTIALS)")
	line(t, pre, "BigQuery", "jobs in acme-dev (column impact dry runs the branch here)")
	line(t, pre, "merge-base", "1125280 with origin/main")
	if !strings.Contains(pre.Text(), "ok    Google") || !strings.Contains(pre.Markdown(), "| ✅ | Google |") {
		t.Errorf("text or markdown:\n%s\n%s", pre.Text(), pre.Markdown())
	}
}

func TestPreflightStopsOnlyOnTheImpossible(t *testing.T) {
	p := goodProbes()
	p.CoreVersion = ""
	p.DryRun = func(_ context.Context, project, _ string) error {
		if project == "acme-dev" {
			return &bigquery.APIError{Status: 403, Message: "Access Denied: Project acme-dev: User does not have bigquery.jobs.create permission in project acme-dev."}
		}
		return nil
	}
	p.Lineage = func(context.Context, string, string) error {
		return errors.New("no access to Data Lineage in acme-prod")
	}
	pre := Run(context.Background(), p)
	if !pre.Stops() {
		t.Fatal("doesn't stop")
	}
	if l := line(t, pre, "@dataform/core", "node_modules doesn't have it"); l.Status != Fail || !strings.Contains(l.Fix, "npm ci") {
		t.Errorf("core: %+v", l)
	}
	if l := line(t, pre, "BigQuery", "jobs in acme-dev"); l.Status != Fail || l.Fix != "grant roles/bigquery.jobUser on acme-dev to the identity xf runs as" {
		t.Errorf("dev jobs: %+v", l)
	}
	if l := line(t, pre, "BigQuery", "jobs in acme-prod"); l.Status != Pass {
		t.Errorf("prod jobs: %+v", l)
	}
	if l := line(t, pre, "Data Lineage", "acme-prod"); l.Status != Warn {
		t.Errorf("lineage is a warning, not a stop: %+v", l)
	}
	if r := pre.Reason(); !strings.Contains(r, "@dataform/core") || !strings.Contains(r, "jobs in acme-dev") {
		t.Errorf("reason %q", r)
	}
	if !strings.Contains(pre.Text(), "→ run npm ci") {
		t.Errorf("no fix in the text:\n%s", pre.Text())
	}
}

func TestPreflightGit(t *testing.T) {
	p := goodProbes()
	p.Git = func(args ...string) (string, error) {
		if args[0] == "rev-parse" && args[1] == "--is-shallow-repository" {
			return "true", nil
		}
		return "", errors.New("exit 1")
	}
	l := line(t, Run(context.Background(), p), "merge-base", "no origin/main")
	if l.Status != Fail || !strings.Contains(l.Fix, "git fetch origin main") {
		t.Errorf("no base: %+v", l)
	}
	p.Git = func(args ...string) (string, error) {
		switch {
		case args[1] == "--is-shallow-repository":
			return "true", nil
		case args[0] == "rev-parse":
			return "abc", nil
		}
		return "", errors.New("exit 1")
	}
	l = line(t, Run(context.Background(), p), "merge-base", "no common commit")
	if l.Status != Fail || !strings.Contains(l.Fix, "shallow") || !strings.Contains(l.Fix, "GIT_DEPTH") {
		t.Errorf("shallow: %+v", l)
	}
}

func TestPreflightProdSetting(t *testing.T) {
	p := goodProbes()
	p.Prod = Prod{Key: "prodCompilerOptions"}
	pre := Run(context.Background(), p)
	if l := line(t, pre, "prodCompilerOptions", "not set"); l.Status != Warn || !strings.Contains(l.Fix, "XF_ARGS") {
		t.Errorf("unset: %+v", l)
	}
	if pre.CostIn != "acme-dev" || pre.Stops() {
		t.Errorf("unset: cost dry runs in %q, stops %v", pre.CostIn, pre.Stops())
	}
	p.Prod = Prod{Key: "prodCompilerOptions", Set: true, Value: "--default-database=acme-prod", Source: ".xf/config.json", Untracked: true}
	if l := line(t, Run(context.Background(), p), "prodCompilerOptions", "which git doesn't track"); l.Status != Warn {
		t.Errorf("untracked: %+v", l)
	}
	p.Prod = Prod{Key: "prodCompilerOptions", Set: true, Value: "", Source: ".xf/config.json"}
	if l := line(t, Run(context.Background(), p), "prodCompilerOptions", "the default targets are prod"); l.Status != Pass {
		t.Errorf("empty: %+v", l)
	}
	p.Prod = Prod{Key: "prodCompilerOptions", Set: true, Value: "--vars=env=prod", Source: "--prod-compiler-options"}
	p.ProdProject, p.ProdUnknown = "", "prodCompilerOptions don't name --default-database"
	pre = Run(context.Background(), p)
	if l := line(t, pre, "BigQuery", "query cost's project is known only after the compile"); l.Status != Skip {
		t.Errorf("unknown prod: %+v", l)
	}
	line(t, pre, "BigQuery", "jobs in acme-dev (column impact dry runs the branch here)")
}

func TestPreflightGoogleAndDbt(t *testing.T) {
	p := goodProbes()
	p.ADC = func(context.Context) gcpauth.ADC { return gcpauth.ADC{Error: "no Application Default Credentials"} }
	pre := Run(context.Background(), p)
	if l := line(t, pre, "Google", "no Application Default Credentials"); l.Status != Fail || !strings.Contains(l.Fix, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Errorf("no ADC: %+v", l)
	}
	line(t, pre, "BigQuery", "not probed")

	p = goodProbes()
	p.Dbt, p.Bin, p.CoreNeeded, p.Prod = true, "/usr/bin/dbt", false, Prod{Key: "dbtProdTarget"}
	pre = Run(context.Background(), p)
	line(t, pre, "dbt", "/usr/bin/dbt")
	if l := line(t, pre, "BigQuery", "profile"); l.Status != Skip {
		t.Errorf("dbt access: %+v", l)
	}
	if l := line(t, pre, "dbtProdTarget", "not set"); !strings.Contains(l.Fix, "--dbt-prod-target") {
		t.Errorf("dbt prod: %+v", l)
	}

	p = goodProbes()
	p.AsYou = true
	line(t, Run(context.Background(), p), "BigQuery", "as you, not CI's identity")
	p.NoAccess = true
	for _, l := range Run(context.Background(), p).Lines {
		if l.Name == "BigQuery" || l.Name == "Data Lineage" {
			t.Errorf("NoAccess still probed: %+v", l)
		}
	}
}

func TestPreflightToken(t *testing.T) {
	expires := "2026-12-31"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/personal_access_tokens/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"scopes": []string{"api"}, "expires_at": expires})
		case "/api/v4/projects/g%2Fp/merge_requests/7":
			fmt.Fprint(w, `{"iid":7}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	repo := forge.RepoAt(forge.GitLab, srv.URL, "g/p")
	pr := &PR{Repo: repo, Number: 7, Token: "tok"}
	p := goodProbes()
	p.Comment, p.PR, p.Comments = true, pr, forge.NewComments(repo, 7, "tok", nil)
	p.Now = func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }
	if l := line(t, Run(context.Background(), p), "token", "api scope; expires 2026-12-31; reads merge request !7"); l.Status != Pass {
		t.Errorf("token: %+v", l)
	}
	expires = "2026-11-20"
	if l := line(t, Run(context.Background(), p), "token", "expires on 2026-11-20"); l.Status != Warn {
		t.Errorf("expiring: %+v", l)
	}
	expires = ""
	p.Comments = forge.NewComments(repo, 8, "tok", nil)
	if l := line(t, Run(context.Background(), p), "token", "couldn't read merge request !7"); l.Status != Warn {
		t.Errorf("unreadable MR: %+v", l)
	}

	p = goodProbes()
	p.Comment, p.PRErr = true, fmt.Errorf("%w: set XF_FORGE_TOKEN", ErrNoToken)
	pre := Run(context.Background(), p)
	if l := line(t, pre, "token", "no token to comment with"); l.Status != Warn || pre.Stops() {
		t.Errorf("no token: %+v, stops %v", l, pre.Stops())
	}
	p.PRErr = ErrNoPR
	if l := line(t, Run(context.Background(), p), "token", "doesn't comment"); l.Status != Skip {
		t.Errorf("no PR: %+v", l)
	}
}

func TestReportWithPreflight(t *testing.T) {
	stopped := &Preflight{Lines: []Line{{Name: "@dataform/core", Status: Fail, Detail: "missing", Fix: "run npm ci"}}}
	r := &Report{Project: ".", Preflight: stopped}
	md := Markdown(r, Options{})
	if !strings.Contains(md, "❌ Neither check was made") || !strings.Contains(md, "| ❌ | @dataform/core | missing<br>→ run npm ci |") {
		t.Errorf("stopped markdown:\n%s", md)
	}
	if a, _ := Plan(r, Options{}, "abc", ""); a != Leave {
		t.Errorf("a stopped first run posts: %v", a)
	}
	a, body := Plan(r, Options{}, "abc", Body(Marker("."), "old", "### xf check\n\nold report"))
	if a != Edit || !strings.Contains(body, "Couldn't check this push (abc):** the preflight found what stops both checks") ||
		!strings.Contains(body, "| ❌ | @dataform/core |") || !strings.Contains(body, "old report") {
		t.Errorf("stopped edit %v:\n%s", a, body)
	}
	data, err := JSON(r, Options{})
	if err != nil || !strings.Contains(string(data), `"preflight": {`) {
		t.Errorf("JSON: %s, %v", data, err)
	}
}
