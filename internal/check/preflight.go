package check

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gcpauth"
)

// Status is how one preflight line came out.
type Status string

// A Fail stops xf check: only what makes a check impossible fails (ADR
// 0041). What a check survives, as ❔ in its report, is a Warn.
const (
	Pass Status = "ok"
	Warn Status = "warning"
	Fail Status = "failed"
	Skip Status = "skipped"
)

// Line is one probe's result.
type Line struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	// Fix says what to do about a Warn or a Fail.
	Fix string `json:"fix,omitempty"`
}

// Preflight is what xf check probed before compiling: one Line a probe.
type Preflight struct {
	Lines []Line `json:"lines"`
	// ImpactIn and CostIn are the projects column impact dry runs the
	// branch in and query cost dry runs in, "" when they are known only
	// after the compile.
	ImpactIn string `json:"impactDryRunsIn,omitempty"`
	CostIn   string `json:"costDryRunsIn,omitempty"`
}

// Stops says a line failed, so neither check can be made.
func (p *Preflight) Stops() bool {
	return p != nil && slices.ContainsFunc(p.Lines, func(l Line) bool { return l.Status == Fail })
}

// Reason is why the preflight stopped xf check, in a sentence.
func (p *Preflight) Reason() string {
	var failed []string
	for _, l := range p.Lines {
		if l.Status == Fail {
			what, _, _ := strings.Cut(l.Detail, ": ")
			what, _, _ = strings.Cut(what, " (")
			failed = append(failed, l.Name+" "+what)
		}
	}
	return "the preflight found what stops both checks, as its checklist says: " + strings.Join(failed, "; ")
}

var textMarks = map[Status]string{Pass: "ok  ", Warn: "warn", Fail: "FAIL", Skip: "skip"}

// Text is the checklist for the terminal, a fix under each line that
// needs one.
func (p *Preflight) Text() string {
	width := 0
	for _, l := range p.Lines {
		width = max(width, len(l.Name))
	}
	var b strings.Builder
	b.WriteString("xf check: preflight\n")
	for _, l := range p.Lines {
		fmt.Fprintf(&b, "  %s  %-*s  %s\n", textMarks[l.Status], width, l.Name, l.Detail)
		if l.Fix != "" && (l.Status == Warn || l.Status == Fail) {
			fmt.Fprintf(&b, "  %s  %-*s  → %s\n", "    ", width, "", l.Fix)
		}
	}
	return b.String()
}

var mdMarks = map[Status]string{Pass: "✅", Warn: "⚠️", Fail: "❌", Skip: "➖"}

// Markdown is the checklist as a table, for the comment and --output.
func (p *Preflight) Markdown() string {
	lines := []string{"| | Preflight | |", "|---|---|---|"}
	for _, l := range p.Lines {
		detail := cell(l.Detail)
		if l.Fix != "" && (l.Status == Warn || l.Status == Fail) {
			detail += "<br>→ " + cell(l.Fix)
		}
		lines = append(lines, "| "+mdMarks[l.Status]+" | "+cell(l.Name)+" | "+detail+" |")
	}
	return strings.Join(lines, "\n")
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }

// Prod is the setting that picks prod's tables: prodCompilerOptions, or
// dbtProdTarget.
type Prod struct {
	Key string
	// Set says it is set, Value to what, Source where: a file as xf shows
	// it, or a flag.
	Set           bool
	Value, Source string
	// Untracked says Source is a file git doesn't track, so CI won't see it,
	// and Personal that it is the user's own config, outside the project.
	Untracked, Personal bool
}

// Probes are what Run asks, filled in by xf check.
type Probes struct {
	// Dbt says the project is dbt's: its projects come from the profile,
	// so access isn't probed.
	Dbt bool
	// Bin is the Dataform CLI or dbt; BinErr why it wasn't found.
	Bin    string
	BinErr error
	// Core is @dataform/core: CoreNeeded says package.json lists it,
	// CoreVersion the version node_modules has, "" for none.
	CoreNeeded  bool
	CoreVersion string
	// Base is the branch compared with, "origin/main". Git runs git in the
	// project.
	Base string
	Git  func(args ...string) (string, error)
	Prod Prod
	// DefaultProject is the project the default compile dry runs in, and
	// ProdProject the prod compile's; "" with a reason in DefaultUnknown or
	// ProdUnknown when only the compile says. Location is the default
	// location, "EU".
	DefaultProject, DefaultUnknown string
	ProdProject, ProdUnknown       string
	Location                       string
	ADC                            func(ctx context.Context) gcpauth.ADC
	// DryRun dry runs SELECT 1 in a project, which needs bigquery.jobs.create
	// there; Lineage searches Data Lineage in projects/<project>/locations/<location>.
	DryRun  func(ctx context.Context, project, location string) error
	Lineage func(ctx context.Context, project, location string) error
	// NoAccess leaves the access probes out (xf check init, which runs as
	// you); AsYou says the probes run outside CI, as you.
	NoAccess, AsYou bool
	// Comment says --comment is set: PR and PRErr are FindPR's, and
	// Comments the PR's, to probe the token with.
	Comment  bool
	PR       *PR
	PRErr    error
	Comments *forge.Comments
	Now      func() time.Time
}

// expiresSoon is how close to its expiry a token is warned about.
const expiresSoon = 14 * 24 * time.Hour

// Run probes what xf check will need, in seconds, before the compile.
func Run(ctx context.Context, p Probes) *Preflight {
	out := &Preflight{}
	add := func(l Line) { out.Lines = append(out.Lines, l) }
	tools(p, add)
	gitLines(p, add)
	prodLine(p, add)

	signedIn := false
	if p.ADC != nil {
		a := p.ADC(ctx)
		who := gcpauth.TypeText(a.Type)
		if a.Account != "" {
			who += ", as " + a.Account
		}
		if a.SourceFrom != "" {
			who += " (from " + a.SourceFrom + ")"
		}
		switch {
		case a.Error != "" && a.Type == "":
			add(Line{Name: "Google", Status: Fail, Detail: a.Error, Fix: googleFix(p.AsYou)})
		case a.Error != "":
			add(Line{Name: "Google", Status: Fail, Detail: who + ": " + a.Error, Fix: googleFix(p.AsYou)})
		default:
			signedIn = true
			add(Line{Name: "Google", Status: Pass, Detail: who})
		}
	}

	if !p.Dbt {
		out.ImpactIn = p.DefaultProject
		out.CostIn = p.DefaultProject
		if p.Prod.Set && p.Prod.Value != "" {
			out.CostIn = p.ProdProject
		}
	}
	switch {
	case p.NoAccess:
	case p.Dbt:
		add(Line{Name: "BigQuery", Status: Skip, Detail: "dbt: the projects come from the profile, known after the compile"})
	case !signedIn:
		add(Line{Name: "BigQuery", Status: Skip, Detail: "not probed without Google credentials"})
	default:
		access(ctx, p, out, add)
	}
	if p.Comment {
		tokenLines(ctx, p, add)
	}
	return out
}

func googleFix(asYou bool) string {
	if asYou {
		return gcpauth.LoginHint
	}
	return "export " + gcpauth.EnvFile + " before xf check: a service account key, or a workload identity federation config"
}

func tools(p Probes, add func(Line)) {
	name := "Dataform CLI"
	if p.Dbt {
		name = "dbt"
	}
	switch {
	case p.BinErr != nil:
		fix := "npm i -g @dataform/cli, or set dataformBin"
		if p.Dbt {
			fix = "install dbt and its adapter, or set dbtBin"
		}
		add(Line{Name: name, Status: Fail, Detail: p.BinErr.Error(), Fix: fix})
	case p.Bin != "":
		add(Line{Name: name, Status: Pass, Detail: p.Bin})
	}
	if p.Dbt || !p.CoreNeeded {
		return
	}
	if p.CoreVersion == "" {
		add(Line{Name: "@dataform/core", Status: Fail, Detail: "package.json lists it, and node_modules doesn't have it, so nothing compiles",
			Fix: "run npm ci before xf check (XF_NPM_CI / npm-ci does, unless it is false)"})
		return
	}
	add(Line{Name: "@dataform/core", Status: Pass, Detail: p.CoreVersion + " in node_modules"})
}

func gitLines(p Probes, add func(Line)) {
	if p.Git == nil {
		return
	}
	shallow, _ := p.Git("rev-parse", "--is-shallow-repository")
	deep := "fetch the whole history: fetch-depth: 0 on actions/checkout, GIT_DEPTH: \"0\" on GitLab"
	if _, err := p.Git("rev-parse", "--verify", "--quiet", p.Base+"^{commit}"); err != nil {
		branch := strings.TrimPrefix(p.Base, "origin/")
		add(Line{Name: "merge-base", Status: Fail, Detail: "no " + p.Base + ": xf never fetches",
			Fix: "git fetch origin " + branch + " before xf check, or set defaultBranch (--default-branch) if the default branch isn't " + branch})
		return
	}
	mb, err := p.Git("merge-base", "HEAD", p.Base)
	if err != nil || mb == "" {
		fix := "fetch more history of the branch and " + p.Base
		if strings.TrimSpace(shallow) == "true" {
			fix = "the clone is shallow: " + deep
		}
		add(Line{Name: "merge-base", Status: Fail, Detail: "HEAD and " + p.Base + " have no common commit here", Fix: fix})
		return
	}
	add(Line{Name: "merge-base", Status: Pass, Detail: short(strings.TrimSpace(mb)) + " with " + p.Base})
}

func prodLine(p Probes, add func(Line)) {
	pr := p.Prod
	if pr.Key == "" {
		return
	}
	switch {
	case !pr.Set:
		fix := "set " + pr.Key + " in a committed .xf/config.json, or pass --prod-compiler-options in XF_ARGS; \"\" says the default targets are prod"
		if p.Dbt {
			fix = "set dbtProdTarget in a committed .xf/config.json, or pass --dbt-prod-target in XF_ARGS"
		}
		add(Line{Name: pr.Key, Status: Warn, Detail: "not set: the checks compare with the default targets, which may be dev tables", Fix: fix})
	case pr.Personal:
		add(Line{Name: pr.Key, Status: Warn, Detail: quoteValue(pr.Value) + " from " + pr.Source + ", your own config, so CI won't see it",
			Fix: "set it in a committed .xf/config.json, or pass it in the job's XF_ARGS"})
	case pr.Untracked:
		add(Line{Name: pr.Key, Status: Warn, Detail: quoteValue(pr.Value) + " from " + pr.Source + ", which git doesn't track, so CI won't see it",
			Fix: "commit " + pr.Source + ", or pass it in the job's XF_ARGS"})
	case pr.Value == "":
		add(Line{Name: pr.Key, Status: Pass, Detail: `"" (the default targets are prod), from ` + pr.Source})
	default:
		add(Line{Name: pr.Key, Status: Pass, Detail: quoteValue(pr.Value) + ", from " + pr.Source})
	}
}

func quoteValue(v string) string { return "`" + v + "`" }

func access(ctx context.Context, p Probes, out *Preflight, add func(Line)) {
	type use struct {
		project, why string
	}
	var uses []use
	if out.ImpactIn == "" {
		add(Line{Name: "BigQuery", Status: Skip, Detail: "column impact's project is known only after the compile: " + p.DefaultUnknown})
	}
	if out.CostIn == "" {
		add(Line{Name: "BigQuery", Status: Skip, Detail: "query cost's project is known only after the compile: " + p.ProdUnknown})
	}
	switch {
	case out.ImpactIn != "" && out.ImpactIn == out.CostIn:
		uses = append(uses, use{out.ImpactIn, "both checks dry run here"})
	default:
		if out.ImpactIn != "" {
			uses = append(uses, use{out.ImpactIn, "column impact dry runs the branch here"})
		}
		if out.CostIn != "" {
			uses = append(uses, use{out.CostIn, "query cost dry runs here, on prod's tables"})
		}
	}
	asYou := ""
	if p.AsYou {
		asYou = ", as you, not CI's identity"
	}
	for _, u := range uses {
		err := p.DryRun(ctx, u.project, p.Location)
		if err == nil {
			add(Line{Name: "BigQuery", Status: Pass, Detail: "jobs in " + u.project + " (" + u.why + asYou + ")"})
			continue
		}
		fix := "grant roles/bigquery.jobUser on " + u.project + " to the identity xf runs as"
		if strings.Contains(errText(err), "(needs roles/") {
			fix = "grant it to the identity xf runs as"
		}
		if errors.Is(err, bigquery.ErrAuth) {
			fix = googleFix(p.AsYou)
		}
		add(Line{Name: "BigQuery", Status: Fail, Detail: "jobs in " + u.project + " (" + u.why + asYou + "): " + oneLine(errText(err)), Fix: fix})
	}

	lineageIn := cmp.Or(out.CostIn, out.ImpactIn)
	if lineageIn == "" || p.Lineage == nil {
		return
	}
	location := strings.ToLower(p.Location)
	if location == "" {
		add(Line{Name: "Data Lineage", Status: Skip, Detail: "no defaultLocation to search in"})
		return
	}
	if err := p.Lineage(ctx, lineageIn, location); err != nil {
		add(Line{Name: "Data Lineage", Status: Warn, Detail: lineageIn + " (" + location + asYou + "): " + oneLine(errText(err)),
			Fix: "grant roles/datalineage.viewer on " + lineageIn + ", with the Data Lineage API enabled; until then column impact marks readers ❔"})
		return
	}
	add(Line{Name: "Data Lineage", Status: Pass, Detail: lineageIn + " (" + location + asYou + ")"})
}

// errText is a BigQuery error's message, without the "(HTTP 403)".
func errText(err error) string {
	if e, ok := errors.AsType[*bigquery.APIError](err); ok {
		return e.Message
	}
	return err.Error()
}

func tokenLines(ctx context.Context, p Probes, add func(Line)) {
	switch {
	case errors.Is(p.PRErr, ErrNoPR):
		add(Line{Name: "token", Status: Skip, Detail: "this job isn't for a pull request or merge request, so it doesn't comment"})
		return
	case p.PRErr != nil:
		add(Line{Name: "token", Status: Warn, Detail: p.PRErr.Error(), Fix: "the checks run, then xf check exits 2"})
		return
	case p.Comments == nil:
		return
	}
	what := "pull request #" + fmt.Sprint(p.PR.Number)
	if p.PR.Repo.Kind == forge.GitLab {
		what = "merge request !" + fmt.Sprint(p.PR.Number)
	}
	var notes []string
	tok, err := p.Comments.Token(ctx)
	switch {
	case errors.Is(err, forge.ErrToken):
		add(Line{Name: "token", Status: Warn, Detail: p.PR.Repo.Host + " refused XF_FORGE_TOKEN: it may have expired or been revoked",
			Fix: "make a new project access token (api scope, Reporter role) and update the variable"})
		return
	case err == nil:
		if !slices.Contains(tok.Scopes, "api") {
			add(Line{Name: "token", Status: Warn, Detail: "its scopes are " + strings.Join(tok.Scopes, ", ") + ", without api, which posting a note needs",
				Fix: "make a new token with the api scope"})
			return
		}
		notes = append(notes, "api scope")
		if tok.ExpiresAt != "" {
			now := time.Now
			if p.Now != nil {
				now = p.Now
			}
			if t, err := time.Parse(time.DateOnly, tok.ExpiresAt); err == nil && t.Sub(now()) < expiresSoon {
				add(Line{Name: "token", Status: Warn, Detail: "it expires on " + tok.ExpiresAt + "; posting fails after that",
					Fix: "rotate it, and update the XF_FORGE_TOKEN variable"})
				return
			}
			notes = append(notes, "expires "+tok.ExpiresAt)
		}
	}
	if err := p.Comments.Read(ctx); err != nil {
		add(Line{Name: "token", Status: Warn, Detail: "couldn't read " + what + ": " + err.Error(),
			Fix: "give the token access to this project (the Reporter role or more)"})
		return
	}
	add(Line{Name: "token", Status: Pass, Detail: strings.Join(append(notes, "reads "+what), "; ")})
}
