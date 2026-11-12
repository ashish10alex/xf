package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"

	gitlabci "github.com/ashish10alex/xf/ci/gitlab"
	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/check"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/forge"
)

// ciSetup is what xf check init writes a CI job from (ADR 0041).
type ciSetup struct {
	forge string // "gitlab" or "github"
	// host and path are the origin remote's: "gitlab.example.com",
	// "group/repo".
	host, path string
	// dir is the project's folder in the repository, "" at its root.
	dir string
	// auth is "key", "wif" or "template"; include (GitLab) "remote" or
	// "copy".
	auth, include string
	// version is the xf release the job runs, "" for the latest.
	version          string
	failOn, failOver string
	// args are more flags for xf check: a prod setting only this machine has.
	args string
	dbt  bool
	// defaultProject and prodProject are where the checks dry run, "" when
	// only the compile knows.
	defaultProject, prodProject string
}

// runCheckInit is xf check init: it writes the CI job that runs xf check.
func runCheckInit(args []string) int {
	fs := flag.NewFlagSet("xf check init", flag.ContinueOnError)
	pf := addProjectFlags(fs)
	sf := addSettingFlags(fs, config.KeyDataformBin, config.KeyDbtBin, config.KeyDefaultBranch, config.KeyProdCompilerOptions, config.KeyDbtProdTarget)
	auth := fs.String("auth", "", "how the job signs in to Google: key, wif (workload identity federation) or template (your organisation's own)")
	include := fs.String("include", "", "GitLab: remote (include the template from GitHub) or copy (a copy at ci/xf-check.yml)")
	failOn := fs.String("fail-on", "", "fail the job at this column change `severity`: critical or warning")
	failOver := fs.String("fail-over", "", "fail the job when the changed actions scan more than this `size`, e.g. 100GB")
	version := fs.String("version", "", "the xf `release` the job runs, by default this xf's (else the latest)")
	yes := fs.Bool("yes", false, "don't ask: take the flags and the defaults, and write the files")
	update := fs.Bool("update", false, "GitLab: rewrite the copy at ci/xf-check.yml from this xf, and nothing else")
	commandUsage(fs, "xf check init gitlab|github [flags]", `Writes the CI job that runs xf check on each merge request (pull request):
.gitlab-ci.yml's xf-check job, or .github/workflows/xf-check.yml. In a
terminal it asks how the job signs in to Google, how GitLab gets the
template, and the gates; each has a flag, and --yes takes them without
asking. It shows a change to an existing .gitlab-ci.yml before writing it.
For workload identity federation it prints the gcloud commands, filled in
for this repository; it runs nothing in Google Cloud. It ends with a local
preflight and the roles CI's identity needs.`)
	// The forge comes first, as in the usage line; flags may follow it.
	var positional []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positional, args = args[:1], args[1:]
	}
	if help, err := parseArgs(fs, args); help {
		return check.OK
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "xf check init:", err)
		return check.Failed
	}
	if err := checkInit(append(positional, fs.Args()...), pf, sf, *auth, *include, *failOn, *failOver, *version, *yes, *update); err != nil {
		fmt.Fprintln(os.Stderr, "xf check init:", err)
		return check.Failed
	}
	return check.OK
}

func checkInit(args []string, pf projectFlags, sf *settingFlags, auth, include, failOn, failOver, version string, yes, update bool) error {
	if len(args) != 1 || (args[0] != "gitlab" && args[0] != "github") {
		return errors.New("say which: xf check init gitlab, or xf check init github")
	}
	if update && args[0] != "gitlab" {
		return errors.New("--update is for GitLab's copy of the template")
	}
	root, name, err := pf.find()
	if err != nil {
		return err
	}
	settings, err := sf.load(root)
	if err != nil {
		return err
	}
	top, err := gitOut(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("the project isn't in a git repository")
	}
	s := ciSetup{forge: args[0], dir: gitPrefix(root), dbt: name == backend.DBT, version: cmp.Or(version, releaseTag()),
		failOn: failOn, failOver: failOver}
	if failOn != "" {
		if _, _, err := parseFailOn(failOn); err != nil {
			return err
		}
	}
	if update {
		path := filepath.Join(top, "ci", "xf-check.yml")
		if err := writeTemplateCopy(path, s.version); err != nil {
			return err
		}
		fmt.Println("Rewrote", relTo(top, path), "from xf", cmp.Or(s.version, "(a development build)"))
		return nil
	}
	ctx := context.Background()
	if url, err := forge.RemoteURL(ctx, root, "origin"); err == nil {
		if r, ok := forge.ParseRemote(url); ok {
			s.host, s.path = r.Host, strings.TrimSuffix(strings.Trim(r.Path, "/"), ".git")
		}
	}
	if !s.dbt {
		s.defaultProject, _ = defaultProject(root, settings)
		s.prodProject, _ = prodProject(settings.ProdCompilerOptions)
		if !settings.IsSet(config.KeyProdCompilerOptions) || settings.ProdCompilerOptions == "" {
			s.prodProject = s.defaultProject
		}
	}

	ask := asker{in: bufio.NewReader(os.Stdin), out: os.Stdout, on: !yes && interactive()}
	if !yes && !interactive() {
		return errors.New("run it in a terminal to answer its questions, or pass --yes with the flags")
	}
	selfManaged := s.forge == "gitlab" && s.host != "" && s.host != "gitlab.com"
	s.auth = cmp.Or(auth, ask.choose("How does the job sign in to Google?", []string{"wif", "key", "template"}, "wif",
		"wif: workload identity federation, no key stored · key: a service account key · template: your organisation's own CI template"))
	if !slices.Contains([]string{"wif", "key", "template"}, s.auth) {
		return fmt.Errorf("--auth must be key, wif or template, not %q", s.auth)
	}
	if s.forge == "gitlab" {
		def := "remote"
		note := "remote: include it from GitHub at the xf tag · copy: a copy at ci/xf-check.yml"
		if selfManaged {
			def = "copy"
			note += " (suggested: " + s.host + " may not reach GitHub's raw files)"
		}
		s.include = cmp.Or(include, ask.choose("How does GitLab get the template?", []string{"remote", "copy"}, def, note))
		if s.include != "remote" && s.include != "copy" {
			return fmt.Errorf("--include must be remote or copy, not %q", s.include)
		}
	}
	if failOn == "" {
		s.failOn = ask.text("Fail the job at a column change severity (critical, warning, or empty for never)?", "")
		if _, _, err := parseFailOn(s.failOn); err != nil {
			return err
		}
	}
	if failOver == "" {
		s.failOver = ask.text("Fail the job when the changed actions scan more than (such as 100GB, or empty for never)?", "")
	}
	key, flagName := config.KeyProdCompilerOptions, "--prod-compiler-options"
	set := settings.IsSet(key)
	if s.dbt {
		key, flagName, set = config.KeyDbtProdTarget, "--dbt-prod-target", unsetDbtProdTarget(settings, os.Getenv) == ""
	}
	if prod := prodSetting(root, settings, key, set); prod.Untracked {
		switch {
		case strings.ContainsAny(prod.Value, " \t") || prod.Value == "":
			fmt.Printf("%s is only in %s, which git doesn't track, so CI won't see it, and XF_ARGS splits on spaces: commit the file.\n", key, prod.Source)
		case ask.yes(fmt.Sprintf("%s is only in %s, which git doesn't track. Pass it in the job's XF_ARGS?", key, prod.Source), true):
			s.args = flagName + "=" + prod.Value
		default:
			fmt.Printf("Then commit %s, or CI compares with the default targets.\n", prod.Source)
		}
	}

	if s.forge == "github" {
		path := filepath.Join(top, ".github", "workflows", "xf-check.yml")
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists", relTo(top, path))
		}
		if err := writeFile(path, []byte(githubWorkflow(s))); err != nil {
			return err
		}
		fmt.Println("Wrote", relTo(top, path))
	} else if err := writeGitLab(top, s, ask); err != nil {
		return err
	}

	fmt.Println()
	fmt.Print(setupNotes(s))
	fmt.Println()
	base := cmp.Or(settings.DefaultBranch, config.DefaultBranch)
	p := preflightProbes(name, root, base, settings, false, nil, nil)
	p.NoAccess = true
	fmt.Print(runPreflight(ctx, p).Text())
	return nil
}

// asker asks questions at a terminal; when it is off, every answer is the
// default.
type asker struct {
	in  *bufio.Reader
	out io.Writer
	on  bool
}

func (a asker) line() string {
	s, _ := a.in.ReadString('\n')
	return strings.TrimSpace(s)
}

func (a asker) choose(q string, options []string, def, note string) string {
	if !a.on {
		return def
	}
	for {
		fmt.Fprintf(a.out, "%s\n  %s\n  [%s] (%s): ", q, note, strings.Join(options, "/"), def)
		got := cmp.Or(a.line(), def)
		if slices.Contains(options, got) {
			return got
		}
		fmt.Fprintf(a.out, "  one of %s\n", strings.Join(options, ", "))
	}
}

func (a asker) text(q, def string) string {
	if !a.on {
		return def
	}
	fmt.Fprintf(a.out, "%s ", q)
	return cmp.Or(a.line(), def)
}

func (a asker) yes(q string, def bool) bool {
	if !a.on {
		return def
	}
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Fprintf(a.out, "%s [%s] ", q, hint)
	switch strings.ToLower(a.line()) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// releaseTag is this xf's release, "" for a development build.
func releaseTag() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if v := bi.Main.Version; strings.HasPrefix(v, "v") && !pseudoVersion.MatchString(v) && !strings.Contains(v, "+dirty") {
		return v
	}
	return ""
}

func relTo(top, path string) string {
	if rel, err := filepath.Rel(top, path); err == nil {
		return rel
	}
	return path
}

// templateHeader opens the copy of the GitLab template in a repository.
const templateHeader = "# Copied by `xf check init gitlab`"

// writeTemplateCopy writes the GitLab template, with a header naming the
// xf it came from, to path; it doesn't overwrite a file xf didn't write.
func writeTemplateCopy(path, version string) error {
	if data, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(data), templateHeader) {
		return fmt.Errorf("%s exists, and xf check init didn't write it", path)
	}
	return writeFile(path, []byte(templateCopy(version)))
}

func templateCopy(version string) string {
	from := "a development build of xf"
	if version != "" {
		from = "xf " + version
	}
	return templateHeader + " from " + from + "'s ci/gitlab/xf-check.yml\n" +
		"# (https://github.com/ashish10alex/xf). `xf check init gitlab --update` rewrites it\n" +
		"# from the xf you run.\n" + gitlabci.Template
}

// templateRef is the ref of the xf repository a job takes the template or
// the Action from: the release, else main.
func templateRef(s ciSetup) string { return cmp.Or(s.version, "main") }

// includeEntry is the template's line in .gitlab-ci.yml's include list.
func includeEntry(s ciSetup) string {
	if s.include == "copy" {
		return "- local: ci/xf-check.yml"
	}
	return "- remote: https://raw.githubusercontent.com/ashish10alex/xf/" + templateRef(s) + "/ci/gitlab/xf-check.yml"
}

// gitlabJob is the xf-check job that extends the template.
func gitlabJob(s ciSetup) string {
	var b strings.Builder
	b.WriteString("# xf check: the column impact and query cost of each merge request, as one\n")
	b.WriteString("# note on it (written by `xf check init gitlab`; see xf's docs/checks.md).\n")
	b.WriteString("# Needs the CI/CD variable XF_FORGE_TOKEN: a project access token with the api\n")
	b.WriteString("# scope and the Reporter role, masked and not protected.\n")
	switch s.auth {
	case "key":
		b.WriteString("# Google: a File variable GOOGLE_APPLICATION_CREDENTIALS holding a service account key.\n")
	case "wif":
		b.WriteString("# Google: a File variable GOOGLE_APPLICATION_CREDENTIALS holding the workload\n# identity federation config (`gcloud iam workload-identity-pools create-cred-config`).\n")
	}
	b.WriteString("xf-check:\n  variables:\n")
	if s.version != "" {
		fmt.Fprintf(&b, "    XF_VERSION: %s\n", s.version)
	} else {
		b.WriteString("    XF_VERSION: \"\"  # the latest release; pin a tag such as v0.0.5\n")
	}
	if s.dir != "" {
		fmt.Fprintf(&b, "    XF_DIR: %s\n", s.dir)
	}
	if s.failOn != "" {
		fmt.Fprintf(&b, "    XF_FAIL_ON: %s\n", s.failOn)
	}
	if s.failOver != "" {
		fmt.Fprintf(&b, "    XF_FAIL_OVER: %s\n", s.failOver)
	}
	if s.args != "" {
		fmt.Fprintf(&b, "    XF_ARGS: %q\n", s.args)
	}
	switch s.auth {
	case "wif":
		aud := "https://" + cmp.Or(s.host, "gitlab.com")
		fmt.Fprintf(&b, "  id_tokens:\n    GITLAB_OIDC_TOKEN:\n      aud: %s  # one of the provider's allowed audiences\n", aud)
		b.WriteString("  before_script:\n    - printf '%s' \"$GITLAB_OIDC_TOKEN\" > /tmp/gitlab-oidc-token  # where the config reads the token\n")
	case "template":
		b.WriteString("  # Sign in to Google with your organisation's template here: xf only needs\n")
		b.WriteString("  # GOOGLE_APPLICATION_CREDENTIALS exported by the end of before_script.\n")
		b.WriteString("  # before_script:\n  #   - !reference [.your_auth_template, script]\n")
	}
	return b.String()
}

var (
	includeRE    = regexp.MustCompile(`^include:\s*(.*)$`)
	stagesRE     = regexp.MustCompile(`^stages:\s*(.*)$`)
	listItemRE   = regexp.MustCompile(`^(\s+)-\s*(.*?)\s*(#.*)?$`)
	xfCheckJobRE = regexp.MustCompile(`(?m)^xf-check:`)
)

// editGitLabCI adds the include entry, the test stage when a stages list
// lacks it, and the job to a .gitlab-ci.yml. added are the new lines'
// numbers in the result, from 1.
func editGitLabCI(text, entry, job string) (out string, added []int, err error) {
	if xfCheckJobRE.MatchString(text) {
		return "", nil, errors.New(".gitlab-ci.yml already has an xf-check job")
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	insert := func(at int, l string) {
		lines = slices.Insert(lines, at, l)
		for i := range added {
			if added[i] > at {
				added[i]++
			}
		}
		added = append(added, at+1)
	}
	inc := slices.IndexFunc(lines, func(l string) bool { return includeRE.MatchString(l) })
	switch {
	case inc < 0:
		insert(0, "")
		insert(0, "  "+entry)
		insert(0, "include:")
	case strings.TrimSpace(includeRE.FindStringSubmatch(lines[inc])[1]) != "" && !strings.HasPrefix(strings.TrimSpace(includeRE.FindStringSubmatch(lines[inc])[1]), "#"):
		return "", nil, errors.New(".gitlab-ci.yml's include: is on one line; make it a list, then run xf check init again")
	default:
		indent := "  "
		if inc+1 < len(lines) {
			if m := listItemRE.FindStringSubmatch(lines[inc+1]); m != nil {
				indent = m[1]
			}
		}
		insert(inc+1, indent+entry)
	}
	if st := slices.IndexFunc(lines, func(l string) bool { return stagesRE.MatchString(l) }); st >= 0 {
		if rest := strings.TrimSpace(stagesRE.FindStringSubmatch(lines[st])[1]); rest != "" && !strings.HasPrefix(rest, "#") {
			if !strings.Contains(rest, "test") {
				return "", nil, errors.New(".gitlab-ci.yml's stages: is on one line and has no test stage, which the xf-check job runs in; add it")
			}
		} else {
			last, indent, hasTest := st, "  ", false
			for i := st + 1; i < len(lines); i++ {
				m := listItemRE.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				last, indent = i, m[1]
				hasTest = hasTest || m[2] == "test"
			}
			if !hasTest {
				insert(last+1, indent+"- test  # the xf-check job's stage")
			}
		}
	}
	insert(len(lines), "")
	for _, l := range strings.Split(strings.TrimRight(job, "\n"), "\n") {
		insert(len(lines), l)
	}
	slices.Sort(added)
	return strings.Join(lines, "\n") + "\n", added, nil
}

// writeGitLab writes .gitlab-ci.yml (and the template's copy), showing a
// change to an existing file first.
func writeGitLab(top string, s ciSetup, ask asker) error {
	path := filepath.Join(top, ".gitlab-ci.yml")
	job := gitlabJob(s)
	var out string
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out = "include:\n  " + includeEntry(s) + "\n\n" + job
	case err != nil:
		return err
	default:
		var added []int
		if out, added, err = editGitLabCI(string(data), includeEntry(s), job); err != nil {
			return err
		}
		fmt.Println("The change to .gitlab-ci.yml:")
		lines := strings.Split(out, "\n")
		for _, n := range added {
			fmt.Printf("  %4d + %s\n", n, lines[n-1])
		}
		if !ask.yes("Write it?", true) {
			return errors.New("left .gitlab-ci.yml as it was")
		}
	}
	if s.include == "copy" {
		copyPath := filepath.Join(top, "ci", "xf-check.yml")
		if err := writeTemplateCopy(copyPath, s.version); err != nil {
			return err
		}
		fmt.Println("Wrote", relTo(top, copyPath))
	}
	if err := writeFile(path, []byte(out)); err != nil {
		return err
	}
	fmt.Println("Wrote .gitlab-ci.yml")
	return nil
}

// githubWorkflow is .github/workflows/xf-check.yml.
func githubWorkflow(s ciSetup) string {
	var b strings.Builder
	b.WriteString("# xf check: the column impact and query cost of each pull request, as one\n")
	b.WriteString("# comment on it and notes on the changed lines (written by `xf check init github`).\n")
	b.WriteString("name: xf check\n\non:\n  pull_request:\n\njobs:\n  check:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n")
	if s.auth == "wif" {
		b.WriteString("      id-token: write        # for workload identity federation\n")
	}
	b.WriteString("      pull-requests: write   # to post the comment\n    steps:\n")
	b.WriteString("      - uses: actions/checkout@v4\n        with:\n          fetch-depth: 0     # xf needs history to find the merge-base\n\n")
	switch s.auth {
	case "wif":
		project := cmp.Or(s.prodProject, s.defaultProject, "my-project")
		b.WriteString("      - uses: google-github-actions/auth@v2\n        with:\n")
		b.WriteString("          workload_identity_provider: projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/github/providers/github\n")
		fmt.Fprintf(&b, "          service_account: xf-check@%s.iam.gserviceaccount.com\n\n", project)
	case "key":
		b.WriteString("      - uses: google-github-actions/auth@v2\n        with:\n          credentials_json: ${{ secrets.GCP_SA_KEY }}   # a service account key\n\n")
	case "template":
		b.WriteString("      # Sign in to Google here: xf only needs GOOGLE_APPLICATION_CREDENTIALS\n      # (Application Default Credentials) set for the next step.\n\n")
	}
	fmt.Fprintf(&b, "      - uses: ashish10alex/xf@%s\n", templateRef(s))
	var with []string
	if s.dir != "" {
		with = append(with, "working-directory: "+s.dir)
	}
	if s.failOn != "" {
		with = append(with, "fail-on: "+s.failOn)
	}
	if s.failOver != "" {
		with = append(with, "fail-over: "+s.failOver)
	}
	if s.args != "" {
		with = append(with, fmt.Sprintf("args: %q", s.args))
	}
	if len(with) > 0 {
		b.WriteString("        with:\n")
		for _, w := range with {
			b.WriteString("          " + w + "\n")
		}
	}
	return b.String()
}

// setupNotes says what is left to set up: the token, the Google sign-in
// (with the WIF commands filled in), and the roles CI's identity needs.
func setupNotes(s ciSetup) string {
	var b strings.Builder
	project := cmp.Or(s.prodProject, s.defaultProject, "PROJECT_ID")
	sa := "xf-check@" + project + ".iam.gserviceaccount.com"
	b.WriteString("Still to do, outside the repository:\n\n")
	if s.forge == "gitlab" {
		b.WriteString("- XF_FORGE_TOKEN: Settings → Access tokens: a project access token, Reporter role, api scope.\n")
		b.WriteString("  Then Settings → CI/CD → Variables: XF_FORGE_TOKEN, masked, NOT protected (merge request\n")
		b.WriteString("  pipelines run on unprotected branches), environment scope * or the job's environment.\n")
	} else {
		b.WriteString("- The comment is posted with GITHUB_TOKEN; nothing to set.\n")
	}
	switch s.auth {
	case "key":
		if s.forge == "gitlab" {
			b.WriteString("- GOOGLE_APPLICATION_CREDENTIALS: a File variable holding a service account key, not protected.\n")
		} else {
			b.WriteString("- GCP_SA_KEY: a repository secret holding a service account key.\n")
		}
	case "template":
		b.WriteString("- Sign in with your organisation's template where the job's comments say; it must export\n  GOOGLE_APPLICATION_CREDENTIALS before xf check runs.\n")
	case "wif":
		b.WriteString("- Workload identity federation. Run these yourself (xf ran nothing in Google Cloud):\n\n")
		b.WriteString(indentLines(wifCommands(s, project, sa), "    "))
		if s.forge == "gitlab" {
			b.WriteString("\n  Then paste xf-check-wif.json into a File variable GOOGLE_APPLICATION_CREDENTIALS, not protected.\n")
		}
	}
	b.WriteString("\nThe roles CI's identity needs")
	if s.auth == "wif" {
		b.WriteString(" (" + sa + ")")
	}
	b.WriteString(":\n\n")
	b.WriteString(rolesText(s))
	return b.String()
}

// wifCommands are the gcloud commands for a pool, a provider that trusts
// this repository's CI, the service account, and (GitLab) the config.
func wifCommands(s ciSetup, project, sa string) string {
	repo := cmp.Or(s.path, "GROUP/REPO")
	pool := "gitlab"
	issuer := "https://" + cmp.Or(s.host, "gitlab.com")
	mapping := "google.subject=assertion.sub,attribute.project_path=assertion.project_path"
	condition := "assertion.project_path == '" + repo + "'"
	attr := "attribute.project_path"
	if s.forge == "github" {
		pool, issuer = "github", "https://token.actions.githubusercontent.com"
		mapping = "google.subject=assertion.sub,attribute.repository=assertion.repository"
		condition = "assertion.repository == '" + repo + "'"
		attr = "attribute.repository"
	}
	audiences := ""
	if s.forge == "gitlab" {
		audiences = " \\\n  --allowed-audiences=\"" + issuer + "\""
	}
	lines := []string{
		"PROJECT_NUMBER=$(gcloud projects describe " + project + " --format='value(projectNumber)')",
		"",
		"gcloud iam workload-identity-pools create " + pool + " --project=" + project + " --location=global",
		"",
		"gcloud iam workload-identity-pools providers create-oidc " + pool + " \\\n  --project=" + project + " --location=global --workload-identity-pool=" + pool + " \\\n  --issuer-uri=\"" + issuer + "\"" + audiences + " \\\n  --attribute-mapping=\"" + mapping + "\" \\\n  --attribute-condition=\"" + condition + "\"",
		"",
		"gcloud iam service-accounts create xf-check --project=" + project,
		"",
		"gcloud iam service-accounts add-iam-policy-binding " + sa + " \\\n  --role=roles/iam.workloadIdentityUser \\\n  --member=\"principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/" + pool + "/" + attr + "/" + repo + "\"",
	}
	if s.forge == "gitlab" {
		lines = append(lines, "",
			"gcloud iam workload-identity-pools create-cred-config \\\n  projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/"+pool+"/providers/"+pool+" \\\n  --service-account="+sa+" \\\n  --credential-source-file=/tmp/gitlab-oidc-token --credential-source-type=text \\\n  --output-file=xf-check-wif.json")
	} else {
		lines = append(lines, "", "# then put projects/$PROJECT_NUMBER/... in the workflow's workload_identity_provider")
	}
	return strings.Join(lines, "\n") + "\n"
}

// rolesText is the roles CI's identity needs in each project.
func rolesText(s ciSetup) string {
	if s.dbt {
		return "  - roles/bigquery.jobUser and roles/bigquery.dataViewer in the projects the dbt profile's\n    targets build in, and roles/datalineage.viewer in the prod target's project.\n"
	}
	dev, prod := cmp.Or(s.defaultProject, "the default project"), cmp.Or(s.prodProject, "the prod project")
	var b strings.Builder
	if dev == prod {
		fmt.Fprintf(&b, "  - %s: roles/bigquery.jobUser (both checks' dry runs, which are free),\n    roles/bigquery.dataViewer on the datasets the models read and build,\n    roles/datalineage.viewer, with the Data Lineage API enabled\n", dev)
		return b.String()
	}
	fmt.Fprintf(&b, "  - %s: roles/bigquery.jobUser, and roles/bigquery.dataViewer on the datasets the\n    models read (column impact dry runs the branch here)\n", dev)
	fmt.Fprintf(&b, "  - %s: roles/bigquery.jobUser, roles/bigquery.dataViewer on the datasets the models\n    read and build (query cost's dry runs, the live schemas), and roles/datalineage.viewer,\n    with the Data Lineage API enabled\n", prod)
	return b.String()
}

func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
