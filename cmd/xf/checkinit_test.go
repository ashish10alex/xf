package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitlabci "github.com/ashish10alex/xf/ci/gitlab"
)

func TestEditGitLabCI(t *testing.T) {
	existing := `include:
  - project: 'shared/pipelines'
    file: defaults/auth.yml

stages:
  - triggers

deploy:
  stage: triggers
  script: [echo hi]
`
	out, added, err := editGitLabCI(existing, "- local: ci/xf-check.yml", "xf-check:\n  variables:\n    XF_VERSION: v1\n")
	if err != nil {
		t.Fatal(err)
	}
	want := `include:
  - local: ci/xf-check.yml
  - project: 'shared/pipelines'
    file: defaults/auth.yml

stages:
  - triggers
  - test  # the xf-check job's stage

deploy:
  stage: triggers
  script: [echo hi]

xf-check:
  variables:
    XF_VERSION: v1
`
	if out != want {
		t.Errorf("out:\n%s\nwant:\n%s", out, want)
	}
	lines := strings.Split(out, "\n")
	var got []string
	for _, n := range added {
		got = append(got, lines[n-1])
	}
	if strings.Join(got, "|") != "  - local: ci/xf-check.yml|  - test  # the xf-check job's stage||xf-check:|  variables:|    XF_VERSION: v1" {
		t.Errorf("added %v: %q", added, got)
	}

	out, _, err = editGitLabCI("stages: [build, test]\nbuild:\n  script: [make]\n", "- local: ci/xf-check.yml", "xf-check:\n")
	if err != nil || !strings.HasPrefix(out, "include:\n  - local: ci/xf-check.yml\n\nstages: [build, test]") {
		t.Errorf("no include: %q, %v", out, err)
	}
	if _, _, err := editGitLabCI("stages: [build]\n", "- x", "xf-check:\n"); err == nil {
		t.Error("an inline stages without test should fail")
	}
	if _, _, err := editGitLabCI("include: 'a.yml'\n", "- x", "xf-check:\n"); err == nil {
		t.Error("an inline include should fail")
	}
	if _, _, err := editGitLabCI("xf-check:\n  stage: test\n", "- x", "xf-check:\n"); err == nil {
		t.Error("an existing job should fail")
	}
}

func TestGitLabJobAndWorkflow(t *testing.T) {
	s := ciSetup{forge: "gitlab", host: "gitlab.example.com", path: "g/repo", dir: "analytics", auth: "wif", include: "copy",
		version: "v0.0.5", failOn: "critical", args: "--prod-compiler-options=--default-database=acme-prod",
		defaultProject: "acme-dev", prodProject: "acme-prod"}
	job := gitlabJob(s)
	for _, want := range []string{"XF_VERSION: v0.0.5", "XF_DIR: analytics", "XF_FAIL_ON: critical", `XF_ARGS: "--prod-compiler-options=--default-database=acme-prod"`,
		"aud: https://gitlab.example.com", `printf '%s' "$GITLAB_OIDC_TOKEN" > /tmp/gitlab-oidc-token`} {
		if !strings.Contains(job, want) {
			t.Errorf("job lacks %q:\n%s", want, job)
		}
	}
	if e := includeEntry(s); e != "- local: ci/xf-check.yml" {
		t.Errorf("copy entry %q", e)
	}
	s.include = "remote"
	if e := includeEntry(s); e != "- remote: https://raw.githubusercontent.com/ashish10alex/xf/v0.0.5/ci/gitlab/xf-check.yml" {
		t.Errorf("remote entry %q", e)
	}
	notes := setupNotes(s)
	for _, want := range []string{"--issuer-uri=\"https://gitlab.example.com\"", "assertion.project_path == 'g/repo'",
		"attribute.project_path/g/repo", "xf-check@acme-prod.iam.gserviceaccount.com", "acme-dev: roles/bigquery.jobUser", "NOT protected"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	s.auth = "template"
	if job := gitlabJob(s); !strings.Contains(job, "#   - !reference [.your_auth_template, script]") || strings.Contains(job, "id_tokens") {
		t.Errorf("template job:\n%s", job)
	}

	gh := ciSetup{forge: "github", path: "o/r", auth: "wif", version: "v0.0.5", defaultProject: "acme-dev", prodProject: "acme-dev", failOver: "100GB"}
	wf := githubWorkflow(gh)
	for _, want := range []string{"id-token: write", "fetch-depth: 0", "service_account: xf-check@acme-dev.iam.gserviceaccount.com",
		"uses: ashish10alex/xf@v0.0.5", "fail-over: 100GB"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q:\n%s", want, wf)
		}
	}
	if notes := setupNotes(gh); !strings.Contains(notes, "token.actions.githubusercontent.com") || !strings.Contains(notes, "assertion.repository == 'o/r'") {
		t.Errorf("GitHub notes:\n%s", notes)
	}
}

func TestTemplateCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci", "xf-check.yml")
	if err := writeTemplateCopy(path, "v0.0.5"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), templateHeader+" from xf v0.0.5's") || !strings.HasSuffix(string(data), gitlabci.Template) {
		t.Errorf("copy:\n%s", data)
	}
	if err := writeTemplateCopy(path, "v0.0.6"); err != nil {
		t.Errorf("update: %v", err)
	}
	_ = os.WriteFile(path, []byte("mine\n"), 0o644)
	if err := writeTemplateCopy(path, "v0.0.6"); err == nil {
		t.Error("overwrote a file xf didn't write")
	}
}
