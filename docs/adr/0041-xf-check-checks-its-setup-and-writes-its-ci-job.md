---
status: accepted
---

# xf check checks its setup first, and writes its CI job

ADR 0040 put `xf check` on pull requests, with a GitHub Action and a GitLab template.
Setting it up on a self-managed GitLab, with the organisation's own Workload Identity Federation template, took about ten pipeline runs, and each run found one problem:

- the job's image came out empty, so the runner used Alpine, and the template stopped on `curl: not found`;
- `XF_FORGE_TOKEN` was missing, then hidden because it was protected and the merge request's branch isn't, then hidden because it was scoped to another environment; each time the message was the same;
- `XF_INSTALL_DATAFORM: "false"`, for an image that has the Dataform CLI, also skipped `npm ci`, so `@dataform/core` was missing and nothing compiled;
- `prodCompilerOptions` was in an `.xf/config.json` that git doesn't track, so CI compared with dev tables, said so only in a hint, and passed;
- with a prod service account, column impact still failed: it dry runs the branch in the default project, and the docs' roles table speaks of one project;
- the service account showed as `[MASKED]` in the log, so which identity ran was guesswork.

Every one of these can be found in seconds, before a compile.
The same setup also took a hand-made copy of the template, because the include from GitHub couldn't be reached, and a conversation to work out the WIF commands and IAM grants for this repository.

## Decision

**A preflight runs first in every `xf check`.**
It probes what the checks will need and prints one checklist, a fix under each line that fails:

- the tools: the Dataform CLI (or dbt) and, when there is a `package.json`, `@dataform/core` in `node_modules`;
- git: `origin/<base>` exists, the merge-base is found, the clone isn't shallow;
- the settings: whether `prodCompilerOptions` (`dbtProdTarget`) is set, and whether it comes from a file git doesn't track;
- Google: the credential type and the identity it signs in as, from `GOOGLE_APPLICATION_CREDENTIALS` or the metadata server;
- access: a free `SELECT 1` dry run in the default project and in the prod project, and one Data Lineage search in the prod project;
- with `--comment`, the token: on GitLab its `api` scope and expiry (`GET /personal_access_tokens/self`), warning within 14 days of it; on both, one read of the merge request.

The probes run before the compile, so they know the projects (`defaultProject`, and `--default-database` in `prodCompilerOptions`) but not the datasets the changed actions read.
When the prod options don't name a database, the line says the prod project is known only after the compile.
`xf check --preflight` runs the probes alone and exits 0 or 2.
Outside CI the access lines say they ran as you, not as CI's identity.
`--json` carries the checklist under `preflight`.

**Only what makes a check impossible stops it.**
No Dataform CLI or `@dataform/core`, no merge-base, no Google credentials, or no `jobs.create` in a project a check dry runs in: xf prints the checklist and exits 2.
Data Lineage access, an unset `prodCompilerOptions` and the token are warnings, and the checks run.
When the preflight stops a run, the merge request is treated as when neither check could be made (ADR 0040): an existing xf comment becomes "couldn't check", with the checklist; no new comment is posted.

**A missing or unusable token no longer stops the run before the checks.**
xf runs both checks, writes the report and the Code Quality report, then exits 2 with the token's reason.
On GitLab the reason uses `CI_COMMIT_REF_PROTECTED` and `CI_ENVIRONMENT_NAME`: a variable that exists may be hidden because it is protected and this branch isn't, or scoped to an environment other than this job's.

**An unset prod target is said where the reviewer looks.**
In CI, when `prodCompilerOptions` (`dbtProdTarget`) isn't set, the comment's first line says it compared with the default targets, which may be dev tables.
A project whose default targets are prod sets it to `""`.
A committed `.xf/config.json` stays the place for it, and `XF_ARGS` the escape hatch; no setting is read from the environment.

**xf says who it is and where it dry runs.**
The log's first lines name the credential type, the identity, and the project each check dry runs in.
The report names both checks' projects, not the identity: a service account's address on a public pull request says nothing to the reviewer.
An access-denied reason names the role it lacks and where: `needs roles/bigquery.jobUser on <project>`, `roles/bigquery.dataViewer on <dataset>`, `roles/datalineage.viewer on <project>, with the Data Lineage API enabled`.
`docs/checks.md`'s roles table is split into the default project (column impact's dry run of the branch) and the prod project (query cost's dry runs, the live schemas, Data Lineage).

**The template and the Action install in two steps, both `auto`.**
`XF_NPM_CI` (`npm-ci` on the Action) runs `npm ci` whenever there is a `package.json`.
`XF_INSTALL_DATAFORM` installs the Dataform CLI only when `dataform` isn't on PATH, at `dataformCoreVersion` from `workflow_settings.yaml`, else the version in `package-lock.json`.
A dbt project gets neither.
The GitLab template's first lines are plain `sh` and check that the image has `bash`, `curl`, `git` and `tar`, naming what is missing, before the script that needs them.

**`xf check init gitlab|github` writes the CI job.**
In a terminal it asks, with a flag for each answer for scripts:

- **Google sign-in:** a key, Workload Identity Federation, or the organisation's own template.
  A key: the job reads it from a variable or secret, and init says which to set.
  WIF: init prints the `gcloud` commands for the pool, the provider (its issuer and the condition on this repository from the `origin` remote) and the credential config, and the IAM grants for the default and prod projects; it runs nothing in GCP.
  An organisation's template: the job gets a marked `before_script` slot, and init says all xf needs is `GOOGLE_APPLICATION_CREDENTIALS` exported there.
- **The template (GitLab):** `include: remote:` from GitHub, or a copy at `ci/xf-check.yml` with a header naming its xf tag.
  On a host other than gitlab.com it suggests the copy.
  `xf check init gitlab --update` rewrites the copy from the running xf.
- **The gates:** `--fail-on` and `--fail-over`, off by default.

It writes `.github/workflows/xf-check.yml`, or `.gitlab-ci.yml` with the job, or adds the job to an existing `.gitlab-ci.yml` after showing the change.
When `prodCompilerOptions` is only in an untracked `.xf/config.json`, it offers to put it in the job's `XF_ARGS`, and says committing the file is the other way.
It ends with a local preflight of the tools, git and settings, without the access probes, since it runs as you, and prints the roles CI's identity needs in each project instead.

## Why

- **One run that names every problem** beats one push per problem.
  Each of the failures above was found by a probe that costs nothing and takes a second.
- **Inside `xf check`,** not beside it, so nobody needs to know it exists: the first failing run is the checklist.
- **Stop only on the impossible.**
  ADR 0040 reports what it can rather than fail on one missing permission; a preflight that stopped on Data Lineage access would undo that.
- **A report beats an early stop.**
  A first run without a token still leaves something to read, and the job still fails, so the token isn't forgotten.
- **Projects, not datasets,** because datasets need the compile, which would slow every run and repeat what the checks already report.
- **A dev comparison that passes silently is the worst outcome:** it hides what a branch drops.
  Saying so on the merge request, not only in the log, puts it in front of the person who can set it.
- **One install switch did two jobs:** an image with the CLI and a project with `package.json` wanted one and not the other.
- **init writes what the docs say**, filled in for the repository, for the setups the docs can't foresee: an organisation's own auth template, a host that can't reach GitHub.

## Considered options

- **A separate `xf doctor ci`, or `--preflight` alone:** no change to `xf check`, but only those who know of it run it.
- **Stopping on any failed probe:** a clearer setup, but a check that would have half-worked gives nothing.
- **Warnings only, never stopping:** the checks would then fail minutes later on what the preflight already knew.
- **Compiling first, to probe each dataset:** complete, but slower on every run, and the checks report it anyway.
- **Failing fast on a missing token, as before, with the better message:** seconds instead of minutes, but a first run leaves nothing to read.
- **Warning and exiting 0 on a missing token:** never fails a job over a comment, but a broken token goes unnoticed for weeks.
- **Posting the checklist as a new comment:** the author sees setup problems without opening the job, but a broken setup comments on every merge request.
- **Failing in CI until `prodCompilerOptions` is set:** forces the choice, at the cost of one more red run.
- **An `XF_PROD_COMPILER_OPTIONS` template variable, or every setting as an `XF_` environment variable:** easier to find, but another place a setting can live, or new precedence rules for every setting.
- **The identity in the comment too:** says who checked, but it is noise to the reviewer, and on GitLab it is often masked anyway.
- **One smarter install switch:** fewer variables, but `false` would still skip both.
- **`npm ci` always, without a switch:** simple, but an image that vendors `node_modules` couldn't turn it off.
- **init printing a snippet only, or writing without asking:** never edits a CI file, or never asks; but the snippet is pasted and drifts, and the defaults are wrong for an organisation's own auth.
- **init always printing the WIF steps, or leaving auth out:** wrong for a key or an organisation's template, or leaves the hardest part to the docs.
- **init always copying the template, or always including it from GitHub:** the copy works everywhere but always needs updating by hand; the include fails where GitHub's raw files can't be reached.
- **init committing `.xf/config.json` for a local-only prod setting:** xf web, the TUI and CI would agree, but the file may be kept out of git on purpose.
- **init probing access as you:** says little about CI's identity.
- **`xf init --ci gitlab|github`:** one init command, but it would write the shared config and the CI job, two jobs in one.

## Consequences

- `xf check` takes a few seconds longer: the probes, and on GitLab two calls for the token.
- This amends ADR 0040: a missing token no longer stops `xf check --comment` before the checks; exit 2 is now also "the checks ran, but the comment couldn't be posted".
- The GitLab template is embedded in the xf binary, so `init` can copy it and `--update` can refresh it at the running version.
- The template gains `XF_NPM_CI` and the Action `npm-ci`; `XF_INSTALL_DATAFORM: "false"` no longer skips `npm ci`.
  A job that set it to `false` for an image with its own `node_modules` and no network sets `XF_NPM_CI: "false"` too.
- The comment's first line can warn about an unset prod target, so a project that compares with prod by default sets `prodCompilerOptions` to `""` to keep it quiet.
- It ships as one change, then a release.
