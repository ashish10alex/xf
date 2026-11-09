---
status: accepted
---

# xf check reports on pull requests

`xf impact` and `xf cost` already work in CI: Markdown for a comment, JSON for scripts, exit codes for a gate.
Using them took about 60 lines of workflow YAML, a `go install`, two comments and a sticky-comment action, and only on GitHub.
`xf check` runs both checks and puts what they find where a reviewer looks: one comment on the pull request (merge request on GitLab) and notes on the changed lines.
A GitHub Action and a GitLab template in the xf repo package it.

## Decision

**`xf check` runs column impact and query cost in one `xf serve`.**
The merge-base is compiled once for both.
It writes one report: Markdown, and JSON with the two existing documents (each `version` 1) under `impact` and `cost`.
`xf impact` and `xf cost` stay as they are.
It takes both checks' gates, `--fail-on` and `--fail-over`, and both are off by default: out of the box it reports and passes.
It exits 1 when either gate trips, and 2 only when neither check could be made.
When one check can't be made (no Data Lineage access, say), the report has the other and `❔ Column impact couldn't be checked: <reason>`, stderr says so, and the exit code is what the other check gives.
It finds the pull request from the CI's environment (`GITHUB_EVENT_PATH`, `CI_MERGE_REQUEST_IID`).

**`xf check --comment` posts the report as one sticky comment.**
xf posts it itself, through `internal/forge`, on GitHub and GitLab alike.
The comment carries a hidden marker with the project's path (`<!-- xf-check:analytics/dataform -->`); each run edits the comment with its marker rather than adding one.
A repository with several projects runs `xf check --dir <path>` once for each and gets a comment for each, named by its project.
The token is `XF_FORGE_TOKEN`, else `GITHUB_TOKEN` on GitHub; GitLab needs `XF_FORGE_TOKEN`, a project access token with `api` scope and the Reporter role, since `CI_JOB_TOKEN` can't post notes.
`tokens.json` is never read for this.

**What the comment says.**
It starts with one line: the worst column change (`🚨 2 dropped columns still read`) and what the branch adds to the cost of a run, with the total beside it (`+4.2 TiB per run (≈ +$26) · 4.4 TiB total`), or the total alone when the merge-base couldn't be compiled.
The two reports follow in folded `<details>` sections.
Cost is per run only.

**A pull request with nothing to say gets no comment.**
Something to say is a critical, warning or unknown column change, a missing partition filter, an action that wasn't estimated, or a total that grows by 1 GiB or more (`--quiet-under` sets the size).
A pull request that changes no actions has nothing to say.
When an earlier push left a comment and this one has nothing to say, the comment becomes one line, `✅ Resolved`.
When neither check could be made and a comment exists, it becomes `❔ Couldn't check this push: <reason>`, with the last report folded below it under the commit it checked, so nobody reads old findings as current.

**Notes on the changed lines.**
On GitHub Actions (`GITHUB_ACTIONS=true`) xf prints `::error`, `::warning` and `::notice` lines itself, which need no token.
On GitLab, `--codequality <path>` writes a Code Quality report, which the template keeps as an artifact.
A note goes on the line that names the column or the table read; a dropped column's on the first line of the diff hunk that removed it, else on line 1.
Levels follow the gates: `error` only for what a set gate trips; otherwise a column change or a missing partition filter is a `warning`, and readers that couldn't be looked up or an action not estimated a `notice`.
With no gate set, nothing is red.

**A GitHub Action at the root of the xf repo,** `uses: ashish10alex/xf@v0.x`.
It downloads the release binary of its own tag, installs `@dataform/cli` (and runs `npm ci` when the project has a `package.json`; an input skips this for dbt, whose users install dbt), and runs `xf check --comment`, passing `GITHUB_TOKEN`.
Google credentials stay the user's own step (`google-github-actions/auth`), as they choose between Workload Identity Federation and a key.
A pull request from a fork gets no credentials, so the Action skips it, says why in the job summary, and passes.

**A GitLab template in the xf repo,** included with `include: remote:` from the raw file at a tag, set through variables.

**Not now:** a monthly cost (xf doesn't know how often an action runs), mentioning CODEOWNERS of the readers a change breaks, labels.

## Why

- **The reviewer reads the pull request,** not the job log or the job summary.
  One comment with the verdict first, and a note beside the line that drops the column, put it where the review happens.
- **Report first.**
  A new check that blocks merges in its first week, on a false positive such as the alias heuristic, gets turned off.
  Teams set the gates when they trust it.
- **Quiet.**
  A comment on every pull request is noise people learn to skip; a comment that appears only when something matters gets read.
- **xf posts, so there is one implementation.**
  Finding and editing its comment on two hosts, and the rules for when to post, are Go code with tests, not shell in two templates.
- **One command, one serve.**
  The verdict line, the quiet rule and the half-failed case need both reports in one place.
- **The release binary installs in seconds;** `go install` built xf on every run.

## Considered options

- **The Action posts the comment (a sticky-comment action), xf stays read-only:** no write path in xf, but GitHub only, and GitLab needs its own script.
- **Two commands, joined in YAML; or `xf impact --with-cost`:** no new command, but the verdict and quiet rules live in shell, or one command carries the other's flags and exit codes.
- **Labels, or only the job summary:** labels need another permission and are for routing, not review; few people open the summary.
- **Failing on critical changes, or over a cost limit, by default:** stronger, but blocks merges on a false positive.
- **Always a comment, or one whenever actions change:** shows it ran, but noisy.
- **Any change in cost, or cost only past a gate:** the first comments on every drift of a few MiB; the second hides cost unless someone sets a limit.
- **A dropped column's note on line 1, or on the file only:** always valid, but away from the change.
- **Notes by severity whatever the gates:** a dropped column that is read shows red even when nothing is blocked.
- **Exit 2 when either check fails, or `--only impact|cost`:** strict, but one missing permission fails every pull request; or each project must turn a check off.
- **One comment for all projects:** tidier, but parallel jobs would race to edit one body.
- **Leaving or deleting the comment when a check fails:** left, it shows findings that may be out of date; deleted, it takes the discussion's context with it.
- **The Action does Google auth too:** shorter YAML, but the Action holds credentials.
- **Installing only xf:** fewer assumptions, but every user adds the same Node and Dataform CLI steps.
- **A GitLab CI/CD Catalog component:** found in the catalog, but it must live in a GitLab project, a second thing to release; a snippet in the docs drifts.
- **A monthly cost from a `runsPerMonth` setting or Dataform's workflow configs:** what a reviewer wants to know, but a setting goes stale, and the API is Dataform only and needs another permission.
- **CODEOWNERS mentions:** a nudge, but it pings people on every false positive and can't reach readers outside the repository.

## Consequences

- `internal/forge` writes for the first time, for this command alone: it creates and edits one comment on a pull request.
  This amends ADR 0020, which says xf never writes to the host; xf web still never does.
- Tags of xf version the Action and the GitLab template too, so `@v0.x` gets that release's binary.
- `docs/checks.md`'s CI sections are rewritten around the Action and the template.
- Pull requests from forks are not checked.
