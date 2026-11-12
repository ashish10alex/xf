# Checking a branch: columns and cost

## Check what your branch does to columns

`I` shows the column impact of the same changed actions, as the Markdown report a pull request gets; `y` copies it.
Every changed table, view and incremental table is dry run and compared with its table in BigQuery, and Dataplex Data Lineage is asked who reads the columns it drops or retypes, and the tables it deletes.

The report is a ledger.
Its heading is the verdict ("✅ Nothing to fix", or "🚨 2 to fix before merging"), then there is one row per model: what it changes (all the columns it drops, retypes or adds, in one row) and who that affects, each reader with how it reads (exact copy, derived, may read) and what this branch does about it.
Readers still to fix are in bold.
A rename is a single row, `old → new`.
When every model is in one dataset, the dataset is named once in the heading and the rows leave it out.
Rows are numbered down the left, and a collapsed "Every reader" table follows, each reader citing its row by that number and the table it reads there (`1 · orders`; the old name, for a rename).
On the `--web` page the number is a link to its row.
The numbers are positions in that report: another check may number the same model differently.
They are bare digits, since `#1` would link to issue 1 on GitHub and GitLab.
Each row is marked: 🚨 a column or table something still reads is gone, ⚠️ a column something reads changes type, ❔ not checked or its readers couldn't be looked up, ℹ️ changed but nothing reads it any more, 🔁 renamed, 🆕 new, ✅ keeps every column.
The terminal draws the same ledger, with words for the marks.

A reader this branch changes that still names a dropped column may only name it: as an alias of a new column, say.
So xf dry runs it, with the changed table swapped for the query the branch builds it with, and lets BigQuery resolve the names: when the dry run passes the reader no longer reads the column ("no longer reads it (dry run passes)"), and when BigQuery can't find the column it does ("still reads it (dry run fails)").

An action that is new on the branch is marked 🆕: with no table in BigQuery yet there is nothing to lose, so it isn't dry run ("new: nothing reads it yet", or "new: read by `x` on this branch" when another action of the branch reads it).
When a table is already there, because someone ran the branch or an action deleted earlier is back, it is still compared ("new, its table is already there"), and marked as any changed table is.

A renamed action (see [Run Changed](running.md#run-what-your-branch-changed)) has no table under its new name until it runs, so it is compared with the table it was renamed from, in one 🔁 row, `old → new`.
Its old table's readers are listed too: Dataform doesn't drop it, so a reader still on the old name keeps reading a table that nothing updates any more.
A reader this branch moved onto the new name shows as "moved to the new name".
And where the renamed action is itself a reader of another changed table, it is judged by its new SQL under its new name ("renamed here"), as any reader the branch changes is.

A change your base branch already made isn't held against your branch.
When the table compared with is behind the base branch, say a column was dropped on `main` in a commit that isn't released yet, every later branch that touches the model would show that drop as its own.
So when a table drops or retypes a column, xf also dry runs the action as it is at the merge-base, which is already compiled: a change it makes too is listed as "already changed on `origin/main`", counts as low whoever reads the column, and never fails `--fail-on`.
The readers are still listed, since the next release breaks them all the same.
When the merge-base's version can't be dry run, the change stays your branch's.

The report started as the extension's "Column Impact of Branch Changes" and now goes its own way in layout; which readers it finds, and how it ranks them, still match the extension.

Dataplex also records lineage from dev runs, so a reader may be your own dev build of a project table.
To fold those into the actions they run, as the extension does, set `devCompilerOptions` to the compiler options you develop with, in your personal `~/.config/xf/config.json` (or `--dev-compiler-options`):

```json
{ "devCompilerOptions": "--schema-suffix=ashish" }
```

xf then compiles the project once more with them when it checks. Without it, dev-run readers show as readers outside the project.

By default the tables it compares with are your actions' default targets, from `workflow_settings.yaml` with no compiler options.
If those are dev tables, and prod runs reach prod through compiler options (a release configuration's `--default-database`, `--vars` and so on), set `prodCompilerOptions` to those options in the project's `.xf/config.json` (or `--prod-compiler-options`), so the whole team compares with prod:

```json
{ "prodCompilerOptions": "--default-database=acme-prod --vars=executionSetting=prod" }
```

xf then compiles the project once more with them, matches each action to its table there by dataset and name, and compares with that table and looks up its readers.
The report says which project it compared with (`Evaluated against: tables in acme-prod`).
Without it, a table you've already built from the branch in your default project compares as unchanged.
Readers come from prod runs in the last 30 days, about two hours behind.
Looking them up needs the Data Lineage API enabled and `roles/datalineage.viewer`.

So xf asks.
The first `I` or `C` in a project where nothing sets `prodCompilerOptions` opens a prompt for them.
In xf web it is a form: **Prod project** (`--default-database`), **Prod dataset** (`--default-schema`) and, under *more flags*, any other flags of `dataform compile`, as written.
An empty box says what it gives, the value in `workflow_settings.yaml` (`same as dev: acme-dev`), and the form warns when prod would name the same tables as dev.
`enter` compiles the project with the form's options and, when that works, saves them to `.xf/config.json` as one string (`--default-database=acme-prod`), keeping the file's other keys; a value the project doesn't compile with stays in the form with the error.
If your default targets are prod already, `enter` with every box empty: that saves `"prodCompilerOptions": ""`, which is an answer too.
`esc` checks against the default targets and asks again the next time xf starts.
Commit the file and nobody on the team is asked again.
The settings page shows the same form; in a dbt project, `dbtProdTarget` there is a pick of your profile's targets.

Until it is set, in a file or with the flag, every report says so: under "against" in the TUI, under the summary of `xf impact`'s text (on stderr with `--json`), and as the last line of the Markdown:

```
Compared with the project's default targets (`acme-dev`), which may be dev tables. Set `prodCompilerOptions` to compare with prod.
```

The check runs in the background engine: close the overlay and it carries on; open it again and the report is there, flagged when you've changed the branch since, until `c` checks again.
With `a` on, it checks what you pushed.

`xf impact` prints the same report from the command line, for scripts and CI; see [below](#column-impact-in-cicd).

## Check what your branch would cost

`C` shows what the same changed actions would scan in BigQuery, and what that costs on demand; `y` copies the report as the Markdown a pull request gets.
Every changed action with a query is dry run: tables, views, incremental tables, assertions and operations (in dbt: models, snapshots and tests).
Dry runs are free and run nothing.
The report lists them with the most bytes first:

- **Scans and cost.**
  BigQuery's estimate, in binary units as it bills (1 GiB is 2³⁰ bytes), priced at $6.25 per TiB.
  `≤` marks an upper bound, which is what BigQuery gives for clustered tables, and `≥` a lower bound; the total carries the mark too.
- **Incremental tables** count their incremental run, the one that recurs, with the full rebuild beside it.
- **Change** is against the same action at the commit where your branch left `main`, dry run now, so both sides read today's tables: `+1.20 GiB`, `no change`, or `new`.
  The branch's own figures show first and the change fills in after, since it needs the merge-base compiled.
- **⚠ no partition filter** marks a query that reads a partitioned table by name and never names its partition column (`_PARTITIONTIME` or `_PARTITIONDATE` for a table partitioned by ingestion time), so nothing can be pruned.
  It is a check of the compiled SQL's text: a column that is selected but not filtered on passes, and a table read through a view isn't judged, since the view may filter it.
  A table created with `require_partition_filter` fails its dry run instead, and shows under "Not estimated" with BigQuery's message.
- **Not estimated** lists the actions BigQuery gave no figure for, with the reason: a query that doesn't compile against the tables as they are, or a script (an operation, or a table with `pre_operations`), which BigQuery rarely estimates.
  They aren't in the total.
  One that reads tables the branch adds and no run has built yet is listed under each of them: BigQuery names only the first it doesn't find, so xf dry runs the others alone to see which are built.

In a Dataform project with `prodCompilerOptions` set, the dry runs are of the project compiled with those options, so the bytes are those of the tables prod runs read, not of your dev tables.
An action that needs a column another changed action adds can't be planned against prod until that one has run there, and is listed as not estimated.

Without it, `C` asks for it first, as `I` does (see [above](#check-what-your-branch-does-to-columns)), and until it is set the report says the dry runs read the project's default targets, which may be dev tables: in the TUI, in `xf cost`'s text and Markdown, and as `"unsetSetting": "prodCompilerOptions"` in its JSON.
A dbt project says the same of its target when nothing chooses one (no `dbtProdTarget`, `dbtTarget`, `--dbt-target` or `$DBT_TARGET`), with `"unsetSetting": "dbtProdTarget"`; it isn't asked for.

The check runs in the background engine, as `I` does: close the overlay and it carries on; open it again and the report is there, flagged when you've changed the branch since, until `c` checks again.
With `a` on in a Dataform project, it estimates what you pushed.

`xf cost` prints the same report from the command line, and can fail a CI job over a limit; see [below](#query-cost-in-cicd).

## Pull requests: xf check

`xf check` runs both checks, column impact and query cost, in one report for a pull request (a merge request on GitLab).
In CI it puts what it finds where the review happens:

- **One comment on the pull request**, with `--comment`, edited in place on each push.
  It opens with one line, the worst column change and what the branch adds to a run's cost, such as `🚨 1 dropped column still read · +4.20 TiB per run (≈ +$26.25) · 4.40 TiB total`, ending in a warning when `prodCompilerOptions` (`dbtProdTarget`) isn't set, so the checks compared with the default targets, which may be dev tables.
  A line under it says where each check dry ran (`Column impact dry ran the branch in acme-dev · query cost in acme-prod (EU)`); the two reports follow, folded.
- **Notes on the changed lines**: in GitHub Actions they show in the pull request's Files changed tab, on the line that drops the column or reads the table; for GitLab, `--codequality path` writes a Code Quality report the merge request shows.

It reports and passes by default; `--fail-on` and `--fail-over` gate a build as they do for `xf impact` and `xf cost`.

`xf check init gitlab` (or `github`) writes the CI job for a repository, as [below](#setting-it-up-xf-check-init), and every run starts with [a preflight](#the-preflight) that names what is missing in one go.

### Setting it up: xf check init

Run `xf check init gitlab` or `xf check init github` in the project.
It asks three things, each with a flag for scripts (`--yes` takes the flags and the defaults without asking):

- **How the job signs in to Google** (`--auth`): `wif` (workload identity federation), `key` (a service account key), or `template` (your organisation's own CI template).
  For `wif` it prints the `gcloud` commands for the pool, the provider (its issuer, and a condition on this repository from the `origin` remote), the service account, its binding, and on GitLab the credential config, all filled in; it runs nothing in Google Cloud.
  For `template` the job gets a marked `before_script` slot: all xf needs is `GOOGLE_APPLICATION_CREDENTIALS` exported there.
- **How GitLab gets the template** (`--include`): `remote`, from GitHub at the xf tag, or `copy`, a copy at `ci/xf-check.yml` with a header naming the xf it came from.
  It suggests `copy` on a host other than gitlab.com, which may not reach GitHub's raw files; `xf check init gitlab --update` rewrites the copy from the xf you run.
- **The gates** (`--fail-on`, `--fail-over`), off by default.

It writes `.github/workflows/xf-check.yml`, or a `.gitlab-ci.yml`.
An existing `.gitlab-ci.yml` gets the include, a `test` stage when its `stages:` list lacks one, and the `xf-check` job; init shows those lines and asks before writing.
When `prodCompilerOptions` (or `dbtProdTarget`) is only in an `.xf/config.json` git doesn't track, or in your own config, it offers to pass it in the job's `XF_ARGS`, since CI would otherwise compare with the default targets.
It ends with what is left to do outside the repository (the token, the sign-in), the roles CI's identity needs in each project, and a local preflight of the tools, git and settings.
It leaves out the access probes there, since they would run as you, not as CI's identity.

### The preflight

Every `xf check` first probes, in seconds and before compiling, what the checks will need, and prints a checklist on stderr with a fix under each line that needs one:

| Line | What it checks |
|---|---|
| Dataform CLI (or dbt) | the binary is found |
| `@dataform/core` | when `package.json` lists it, `node_modules` has it (`npm ci` ran) |
| merge-base | `origin/<base>` exists and shares a commit with `HEAD`; a shallow clone is named |
| `prodCompilerOptions` (or `dbtProdTarget`) | it is set, and not only in a file CI won't see: an untracked `.xf/config.json`, or your own config |
| Google | the credential type and the identity, such as `workload identity federation, as xf-check@acme.iam.gserviceaccount.com (from $GOOGLE_APPLICATION_CREDENTIALS)` |
| BigQuery | a free `SELECT 1` dry run in the project column impact dry runs the branch in (the default project) and in the one query cost dry runs in (the prod project, from `--default-database` in `prodCompilerOptions`) |
| Data Lineage | one search in the prod project, in `defaultLocation` |
| token | with `--comment`: on GitLab its `api` scope and its expiry (a warning within 14 days of it), and on both one read of the merge request |

Only what makes a check impossible stops it: no Dataform CLI or `@dataform/core`, no merge-base, no Google credentials, or no `jobs.create` in a project a check dry runs in.
Then xf check exits 2; `--output` gets the checklist, and a comment an earlier push left becomes "couldn't check" with the checklist, but no new comment is posted.
Data Lineage access, an unset prod setting and the token are warnings, and the checks run.
Outside CI the access lines say they ran as you.
A dbt project's projects come from its profile, so its access isn't probed.

`xf check --preflight` runs the probes alone and exits 0, or 2 when one would stop the checks; with `--json` it prints them as JSON.

### GitHub Actions

The xf repository is a GitHub Action.
Add `.github/workflows/xf-check.yml`:

```yaml
name: xf check

on:
  pull_request:

jobs:
  check:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write        # for workload identity federation
      pull-requests: write   # to post the comment
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0     # xf needs history to find the merge-base

      - uses: google-github-actions/auth@v2
        with:
          workload_identity_provider: projects/123456789/locations/global/workloadIdentityPools/github/providers/github
          service_account: dataform-ci@my-project.iam.gserviceaccount.com

      - uses: ashish10alex/xf@<tag>
        # with:
        #   working-directory: analytics/dataform
        #   fail-on: critical
        #   fail-over: 1TB
```

The action downloads the xf release of its tag, runs `npm ci` when the project has a `package.json`, installs the Dataform CLI when `dataform` isn't on PATH (at the project's `dataformCoreVersion`, else the `@dataform/core` version in `package-lock.json`), and runs `xf check --comment` against the pull request's base branch, with `GITHUB_TOKEN`.
It writes the report to the job summary too.
For dbt, add the steps that install dbt and its packages (`dbt deps`) before it; the action neither runs `npm ci` nor installs the Dataform CLI in a folder with a `dbt_project.yml`.
Google credentials are your own step, as above: the identity needs what [the CI job needs](#what-the-ci-job-needs).
A pull request from a fork gets no credentials, so the action skips it, says so in the job summary, and passes.

| Input | |
|---|---|
| `working-directory` | the project's folder, `.` by default |
| `version` | the xf release to run, by default the action's own tag (else the latest release) |
| `comment` | `false` to leave the pull request alone and only write the job summary and the notes |
| `fail-on`, `fail-over` | the gates, off by default: `critical` or `warning`, and a size such as `100GB` |
| `quiet-under` | how much a run must grow for cost alone to be worth a comment, `1GiB` by default |
| `default-branch` | the branch to compare with, by default the pull request's base |
| `npm-ci` | `auto` (the default: when there is a `package.json`), `true` or `false` |
| `install-dataform` | `auto` (the default: when `dataform` isn't on PATH), `true` or `false` |
| `args` | more flags for `xf check`, such as `--usd-per-tib 7.5` or `--dbt-prod-target prod` |

Its outputs are `exit-code` and `report`, the path of the report as Markdown.

### GitLab CI

Include the template from the xf repository at a tag, in `.gitlab-ci.yml`:

```yaml
include:
  - remote: https://raw.githubusercontent.com/ashish10alex/xf/<tag>/ci/gitlab/xf-check.yml

xf-check:
  variables:
    XF_VERSION: <tag>
    # XF_DIR: analytics/dataform
    # XF_FAIL_ON: critical
```

It runs on merge request pipelines in a `node` image, in the `test` stage, and takes the same settings as the action's inputs as variables (`XF_DIR`, `XF_COMMENT`, `XF_FAIL_ON`, `XF_FAIL_OVER`, `XF_QUIET_UNDER`, `XF_NPM_CI`, `XF_INSTALL_DATAFORM`, `XF_ARGS`); the template's header lists them.
Its first lines are plain `sh`, and stop with the tools the job's image lacks when it has no `bash`, `curl`, `git` or `tar`.
Where your GitLab can't reach GitHub's raw files, `xf check init gitlab --include copy` writes a copy of the template into the repository and includes it with `local:`.
It keeps the report (`xf-check.md`) and the Code Quality report as artifacts.
Set two CI/CD variables:

- `XF_FORGE_TOKEN`: a project access token with the `api` scope and the Reporter role, masked. It posts the note; GitLab's job token can't.
  Leave it unprotected, and its environment scope `*` or the job's environment: otherwise the job doesn't get it, and xf says which of the two may be why.
- `GOOGLE_APPLICATION_CREDENTIALS`: a File variable holding a service account key, or a workload identity federation config, as [below](#gitlab-ci-with-workload-identity-federation).

Both must be readable by merge request pipelines: a protected variable is hidden from a pipeline on an unprotected branch.

An image of your own that already has the Dataform CLI needs nothing set: `auto` leaves a `dataform` on PATH alone, and still runs `npm ci` for a project that takes `@dataform/core` from `package.json`.
An image that has its own `node_modules` and no network sets `XF_NPM_CI: "false"`.

#### GitLab CI with workload identity federation

Workload identity federation signs the job in with GitLab's short-lived OIDC token, so no service account key is stored in GitLab.
The steps below impersonate a service account; replace `PROJECT_NUMBER`, `my-project`, `xf-check@my-project.iam.gserviceaccount.com` and `my-group/my-repo` with yours.

1. Create a pool and a provider that trusts your GitLab, and limit it to your repository:

   ```sh
   gcloud iam workload-identity-pools create gitlab \
     --project=my-project --location=global

   gcloud iam workload-identity-pools providers create-oidc gitlab \
     --project=my-project --location=global --workload-identity-pool=gitlab \
     --issuer-uri="https://gitlab.com" \
     --allowed-audiences="https://gitlab.com" \
     --attribute-mapping="google.subject=assertion.sub,attribute.project_path=assertion.project_path" \
     --attribute-condition="assertion.project_path == 'my-group/my-repo'"
   ```

   On self-managed GitLab, the issuer is your instance's URL, such as `https://gitlab.example.com`.
   The audience can be any string, as long as step 4 asks for the same one.

2. Let jobs from the repository impersonate the service account, and give the service account what [the CI job needs](#what-the-ci-job-needs):

   ```sh
   gcloud iam service-accounts add-iam-policy-binding xf-check@my-project.iam.gserviceaccount.com \
     --role=roles/iam.workloadIdentityUser \
     --member="principalSet://iam.googleapis.com/projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/gitlab/attribute.project_path/my-group/my-repo"
   ```

3. Write the credential config. It holds no secret: it says where the job's token is and which service account to impersonate.

   ```sh
   gcloud iam workload-identity-pools create-cred-config \
     projects/PROJECT_NUMBER/locations/global/workloadIdentityPools/gitlab/providers/gitlab \
     --service-account=xf-check@my-project.iam.gserviceaccount.com \
     --credential-source-file=/tmp/gitlab-oidc-token \
     --credential-source-type=text \
     --output-file=xf-check-wif.json
   ```

   Paste the file's contents into a File variable named `GOOGLE_APPLICATION_CREDENTIALS`.

4. Have the job ask GitLab for a token and write it where the config looks for it:

   ```yaml
   include:
     - remote: https://raw.githubusercontent.com/ashish10alex/xf/<tag>/ci/gitlab/xf-check.yml

   xf-check:
     variables:
       XF_VERSION: <tag>
     id_tokens:
       GITLAB_OIDC_TOKEN:
         aud: https://gitlab.com   # one of the provider's allowed audiences
     before_script:
       - printf '%s' "$GITLAB_OIDC_TOKEN" > /tmp/gitlab-oidc-token
   ```

   `before_script` runs in the same shell as the template's script, before it.

**If your organisation has its own auth template**, use it instead of the steps above: all xf needs is `GOOGLE_APPLICATION_CREDENTIALS` exported by the end of the job's `before_script`, which runs in the same shell as the template's script.

```yaml
xf-check:
  extends: .id_tokens                              # if your template needs GitLab's OIDC token
  before_script:
    - !reference [.your_auth_template, script]   # exports GOOGLE_APPLICATION_CREDENTIALS
```

The preflight's Google line names the identity xf signs in as, and the BigQuery lines say whether it can create jobs in each project; GitLab masks the identity in the log when it matches a masked variable.

When the sign-in fails, xf exits 2 and the job prints the end of its log. The usual causes:

- **`invalid_grant` or "audience does not match"**: the `aud` in `id_tokens` isn't one of the provider's allowed audiences.
- **"The given credential is rejected by the attribute condition"**: the provider's condition doesn't match the job's token. A condition on `assertion.ref` or `assertion.ref_protected` rejects merge request pipelines from unprotected branches; match on `assertion.project_path` instead.
- **`iam.serviceAccounts.getAccessToken` permission denied**: step 2's binding is missing, or its `principalSet` names another repository path.
- **"no such file" for `/tmp/gitlab-oidc-token`**: the `before_script` line didn't run, as when the job's own `before_script` replaced it.

### When it comments

A pull request gets a comment only when there is something to say:

- a column change that something still reads (🚨 or ⚠️), or whose readers couldn't be looked up (❔), or a model that couldn't be compared with its table,
- a query without a partition filter, or an action BigQuery gave no estimate for,
- a run that grows by `--quiet-under` (1 GiB) or more, or, when the merge-base couldn't be estimated, a total of that much,
- a gate that tripped, or a check that couldn't be made.

A pull request that changes no actions gets none.
When an earlier push left a comment and this one has nothing to say, the comment becomes `✅ Resolved`.
When neither check can be made on a push, a comment an earlier push left says so, with its last report folded below, under the commit it checked.
Each project has its own comment, found by a hidden marker with its folder, so a repository with several projects runs `xf check --dir <folder>` (the action's `working-directory`) once for each.

### Notes on the changed lines

| Finding | Goes on |
|---|---|
| a dropped column that something reads | the first line of the diff hunk that removed it |
| a retyped column | the first line that names it |
| a read without a partition filter | the first line that names the table |
| a deleted table, an action not estimated | the file's first line |
| a cost over `--fail-over` | the check as a whole |

A note is an error only for what a set gate trips; otherwise a column change or a missing partition filter is a warning, and readers that couldn't be looked up or an action not estimated a notice.
So with no gate set nothing is red.
In GitHub Actions xf prints the notes as workflow commands on stderr, which need no token.

### xf check's flags and output

`xf check` prints the report as Markdown on stdout (or writes it with `--output`), and progress on stderr.
When one check can't be made, the report has the other and says why, and the exit code is the other's: one missing permission doesn't fail every pull request.

| Exit code | Meaning |
|---|---|
| `0` | The report was made, and no gate tripped. |
| `1` | `--fail-on` or `--fail-over` tripped. |
| `2` | Neither check could be made (the preflight stopped them, say), or `--comment` couldn't comment; without a token the checks run first and write their reports. The reason is on stderr. |

| Flag | |
|---|---|
| `--comment` | post the report on the pull request, as above; it needs `XF_FORGE_TOKEN` (or `GITHUB_TOKEN` in GitHub Actions). A job that isn't for a pull request (a push to `main`) says so and doesn't comment |
| `--preflight` | only run [the preflight](#the-preflight): print the checklist, and exit 0, or 2 when something would stop the checks |
| `--codequality path` | also write the notes as a GitLab Code Quality report |
| `--fail-on critical\|warning` | exit 1 at that column change severity or worse |
| `--fail-over size` | exit 1 when the changed actions scan more than `size` between them |
| `--quiet-under size` | how much a run must grow for cost alone to be worth a comment; `1GiB` by default |
| `--output path` | write the report to `path` instead of stdout |
| `--json` | print (or write) JSON: `impact` and `cost` hold [the impact JSON](#impact-json) and [the cost JSON](#json), `null` with `impactError` or `costError` beside when one couldn't be made; `verdict` is the first line, `worthAComment` says whether it would comment, and `preflight` holds the checklist's `lines` (each a `name`, `status`, `detail` and `fix`) |

It takes `xf impact`'s and `xf cost`'s other flags too: `--usd-per-tib`, `--default-branch`, `--pushed`, `--prod-compiler-options`, `--dev-compiler-options`, and the tools' (`--dataform-bin`, `--dbt-bin`, `--dbt-target`, `--dbt-prod-target`).

## Column impact in CI/CD

`xf impact` checks a pull request's column impact and prints the report as text, a line per model with the readers it affects under it, with progress on stderr.
`--output impact.md` also writes it as Markdown, ready to post as a PR comment, and `--json` prints it as JSON instead, for scripts (see [below](#impact-json)).

```
$ xf impact
Column impact · feature/v2-marts → main
1 to fix before merging. 2 models compared with prod tables in acme-prod · dataset marts · base 1125280

FIX  orders  drops discount
     ↳ finance.margins · exact copy · outside the project
new  daily   new: nothing reads it yet

FIX: a column or table something still reads is gone
Readers come from Dataplex lineage of prod runs in the last 30 days, about 2 h behind. A changed reader that still names a dropped column is dry run against the branch's table: a pass means it no longer reads it.
```
It always checks afresh.
Run from inside the project, it:

1. compiles the checked-out project, and the merge-base with `origin/main`, with the `dataform` CLI (or `dbt`, with `dbtProdTarget` when it is set),
2. dry runs each changed table, view and incremental table (in dbt: model), and reads its table in BigQuery,
3. asks Dataplex who reads the columns it drops or retypes, and the tables it deletes.

It starts a `xf serve` in the background to do the work, which stops a few seconds after `xf impact` exits; there is nothing to start or clean up.

On your own machine, `xf impact --web` opens xf web on the column impact tab of its Checks page, the page `I` (or **Check: column impact of changes** in the palette) opens: the check runs there with its progress, `y` copies the Markdown, `c` checks again, and xf web runs until ctrl+c as `xf web` does.
It can't be used with `--pushed`, `--fail-on`, `--output` or `--json`.

| Exit code | Meaning |
|---|---|
| `0` | The report was printed, and nothing reached `--fail-on`. |
| `1` | The report was printed, and `--fail-on critical` found a 🚨 change (or `--fail-on warning` a 🚨 or ⚠️ one). |
| `2` | The check couldn't be made: compile errors, no merge-base, no credentials, and so on. The reason is on stderr. |

A change whose readers couldn't be looked up (❔) never trips `--fail-on`; `xf impact` says so on stderr.

| Flag | |
|---|---|
| `--fail-on critical\|warning` | exit 1 at that severity or worse |
| `--output path` | also write the report to `path`: as Markdown, for a PR comment, or as JSON with `--json` |
| `--json` | print JSON on stdout instead of text |
| `--color auto\|always\|never` | colour the text, as `xf cost`'s [`--color`](#query-cost-in-cicd) does |
| `--web` | open xf web on its Checks page, where the check runs as `I` runs it, instead of printing the report |
| `--default-branch name` | compare with `origin/<name>` rather than `main` (or set `defaultBranch` in `.xf/config.json`) |
| `--pushed` | check the branch pushed to origin instead of the checked-out files (Dataform only) |
| `--dataform-bin path` | the `dataform` binary to compile with |
| `--dbt-bin path`, `--dbt-target name` | the `dbt` binary, and the target xf compiles with |
| `--dbt-prod-target name` | compile a dbt project with this target and compare with the tables it builds (or set `dbtProdTarget`) |
| `--dev-compiler-options options` | fold readers that are dev runs built with these options into their actions (usually unset in CI) |

### Impact JSON

`--json` prints one document, and `--output impact.json` writes it to a file too.
`version` is 1: fields may be added, and it changes if one is removed or changes meaning.
`worst` is the most urgent change's severity, as `--fail-on` ranks them, left out when nothing is at risk.
`atRisk` holds the tables with dropped or retyped columns, or deleted, each with its `columns` and their `readers`; each table and column has a `severity`, so a script or agent can pick out what to fix without working out the rules:

| `severity` | Means |
|---|---|
| `critical` | 🚨 a dropped column or deleted table that something still reads |
| `warning` | ⚠️ a retyped column that something still reads |
| `unknown` | ❔ its readers couldn't be looked up |
| `low` | ℹ️ nothing still reads it: no readers, only ones this branch probably updated, or the base branch made the change |

A table's `severity` is its most urgent column's, an `unknown` counting above a `warning` as the report's marks do.
A reader's `filePath` is set when it is an action of this project, and `changedOnBranch`, `probablyUpdated`, `stillNamed` and `deletedOnBranch` say what this branch does to it.
`safe`, `new` and `unchecked` are the rest of the report.

```json
{
  "version": 1,
  "worst": "critical",
  "comparison": {"headRef": "feat/orders", "baseRef": "origin/main", "mergeBase": "1125280…", "headLabel": "working tree"},
  "changedCount": 2,
  "safe": [{"table": "acme.marts.daily", "type": "view", "columns": []}],
  "new": [],
  "unchecked": [],
  "against": ["acme"],
  "checkedAt": "2026-10-31T19:52:04Z",
  "signature": "7cfba74…",
  "atRisk": [
    {
      "table": "acme.marts.orders",
      "fileName": "definitions/marts/orders.sqlx",
      "type": "table",
      "severity": "critical",
      "columns": [
        {
          "column": "discount",
          "change": {"kind": "dropped"},
          "readers": [{"table": "acme.finance.margins", "column": "discount", "dependencyType": "EXACT_COPY", "filePath": "definitions/finance/margins.sqlx"}],
          "severity": "critical"
        }
      ]
    }
  ]
}
```

### What the CI job needs

- **A Linux or macOS runner** for xf's release binary (the GitHub Action and the GitLab template download it), and **Node** for the `dataform` CLI (`npm i -g @dataform/cli`; run `npm ci` too if the project has a `package.json`).
- **The default branch's history.** xf never fetches: it needs `origin/main` and enough history to find the merge-base, so fetch everything (`fetch-depth: 0` in GitHub Actions) or at least `git fetch origin main`.
- **The PR's branch checked out**, so the report names it. A detached checkout still works, but the report shows a commit instead.
- **Google credentials** through Application Default Credentials: Workload Identity Federation, or a service account key in `GOOGLE_APPLICATION_CREDENTIALS`.
  The checks use two projects when `prodCompilerOptions` names another: column impact dry runs the branch as the project compiles by default, in the default project, and compares it with prod's tables; query cost dry runs against prod.
  The identity needs:

  | Role | On | For |
  |---|---|---|
  | `roles/bigquery.jobUser` | the default project (`defaultProject`) | column impact's dry run of the branch |
  | `roles/bigquery.dataViewer` | the datasets the models read in the default project | the same dry run |
  | `roles/bigquery.jobUser` | the prod project (`--default-database` in `prodCompilerOptions`) | query cost's dry runs |
  | `roles/bigquery.dataViewer` | the datasets the models read and build in the prod project | query cost's dry runs, and the changed tables' current schemas |
  | `roles/datalineage.viewer` | each changed table's project (the prod project), with the Data Lineage API enabled | asking Dataplex for readers |

  With one project, it is one `jobUser` and `dataViewer` on it.
  Dry runs are free, and none of these roles writes.
  An access-denied reason in a report names the role it lacks and where, such as `needs roles/bigquery.jobUser on acme-dev`.

  With user credentials rather than a service account, the quota project (`gcloud auth application-default set-quota-project`) needs the Data Lineage API enabled too.

### Column impact in a pull request

[`xf check`](#pull-requests-xf-check) runs this check and query cost together, and its [GitHub Action](#github-actions) and [GitLab template](#gitlab-ci) post the report on the pull request and its notes on the changed lines.
Use them rather than wiring `xf impact` into a workflow yourself.

### Other CI systems

The same steps work anywhere a shell does. In Cloud Build, Jenkins and the like:

```bash
git fetch origin main                     # xf needs origin/main; it never fetches
git checkout -B "$BRANCH" "$COMMIT"       # optional: the report names the branch
npm i -g @dataform/cli
curl -fsSL https://github.com/ashish10alex/xf/releases/download/<tag>/xf_linux_amd64.tar.gz | tar -xz xf
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json  # or workload identity

set +e
./xf check --fail-on critical --output check.md
code=$?
set -e
[ "$code" -eq 2 ] && exit 2
# post check.md to the merge request
exit "$code"
```

`xf impact` and `xf cost` run the same way, each on its own.
If your default branch isn't `main`, add `--default-branch <name>`, or commit `defaultBranch` in `.xf/config.json` so the TUI and CI agree.
When a run fails, the engine's log is `xf-<hash>-<tool>-<variant>-v<n>.log` in `$TMPDIR` (or `/tmp`; `%TEMP%` on Windows).

## Query cost in CI/CD

`xf cost` estimates what a pull request's changed actions would scan and prints a line for each, with progress on stderr.
It works in Dataform and dbt projects, always checks afresh, and starts and stops its own `xf serve` as `xf impact` does.

```text
$ xf cost --fail-over 100GB
4 changed actions in feature/v2-marts vs origin/main @ 1125280 (working tree)
⚠ marts.fct_order_items  ≤ 4.20 TiB  $26.25  +4.20 TiB  (full refresh 9.00 TiB)  [MISSING PARTITION FILTER: event_date on raw.events]
✓ marts.dim_customers      1.80 GiB  $0.011  new
✓ staging.stg_pageviews  420.00 MiB  $0.003  no change
? ops.audit_log          not estimated: BigQuery gives no estimate for a script
────────────────────────────────────────────────────────────
Total scanned: ≤ 4.20 TiB | Estimated cost: ≤ $26.26
✗ Gate failed: 4.20 TiB exceeds the limit of 100.00 GiB
```

What each column means is under [Check what your branch would cost](#check-what-your-branch-would-cost).

| Exit code | Meaning |
|---|---|
| `0` | The report was printed, and the total is within `--fail-over` (or there is no limit, or nothing to estimate). |
| `1` | The report was printed, and the total is over `--fail-over`. |
| `2` | The check couldn't be made: compile errors, no merge-base, no credentials, BigQuery refused every dry run, and so on. The reason is on stderr. |

The limit is on the total bytes, not the dollars, so the price never decides a build.
An action that couldn't be estimated is left out of the total and never trips the limit; `xf cost` says how many there were on stderr.
A missing partition filter is a warning only.
If the merge-base can't be compiled, the report has no change column, stderr says why, and the exit code is the same as with one.

| Flag | |
|---|---|
| `--fail-over size` | exit 1 when the changed actions scan more than `size` between them: `100GB`, `1.5TiB`, `500MB`. Units are binary with or without the `i` |
| `--usd-per-tib price` | price a TiB at `price` dollars instead of 6.25, for another region's on-demand rate |
| `--output path` | also write the report to `path`: as Markdown, for a PR comment, or as JSON with `--json` |
| `--json` | print JSON on stdout instead of text |
| `--color auto\|always\|never` | colour the text. `auto`, the default, colours a terminal and leaves a pipe, a file or a CI log plain; it also honours `NO_COLOR`. `always` is for CI systems that render ANSI colour |
| `--default-branch name` | compare with `origin/<name>` rather than `main` (or set `defaultBranch` in `.xf/config.json`) |
| `--pushed` | estimate the branch pushed to origin instead of the checked-out files (Dataform only) |
| `--prod-compiler-options options` | dry run the Dataform project compiled with these options (or set `prodCompilerOptions`) |
| `--dataform-bin path`, `--dbt-bin path`, `--dbt-target name` | the tool to compile with, and dbt's target |
| `--dbt-prod-target name` | dry run the dbt project compiled with this target (or set `dbtProdTarget`) |

The job needs what [the column impact job needs](#what-the-ci-job-needs), less Dataplex: `roles/bigquery.jobUser` on the project the dry runs run in, and `roles/bigquery.dataViewer` on the datasets the changed actions read.
For dbt, install dbt and its packages (`dbt deps`) instead of the `dataform` CLI.

### JSON

`--json` prints one document. `version` is 1: fields may be added, and it changes if one is removed or changes meaning.

```json
{
  "version": 1,
  "comparison": {"headRef": "feature/v2-marts", "baseRef": "origin/main", "mergeBase": "1125280…", "headLabel": "working tree"},
  "compiledWith": "prodCompilerOptions",
  "dryRunIn": "acme-prod (EU)",
  "actions": [
    {
      "name": "acme.marts.fct_order_items",
      "dryRunAs": "acme-prod.marts.fct_order_items",
      "file": "definitions/marts/fct_order_items.sqlx",
      "type": "incremental",
      "bytes": 4617948836659,
      "accuracy": "UPPER_BOUND",
      "usd": 26.25,
      "fullRefresh": {"bytes": 9895604649984, "usd": 56.25},
      "new": false,
      "base": {"bytes": 3221225472, "accuracy": "PRECISE", "usd": 0.018},
      "deltaBytes": 4614727611187,
      "warnings": [{"kind": "missing_partition_filter", "table": "acme.raw.events", "column": "event_date"}]
    },
    {"name": "acme.ops.audit_log", "type": "operations", "bytes": null, "usd": null, "new": false, "deltaBytes": null, "warnings": [], "error": "BigQuery gives no estimate for a script"}
  ],
  "totalBytes": 4620332507955,
  "totalBound": "upper",
  "totalUsd": 26.26,
  "baseTotalBytes": 3661627392,
  "deltaTotalBytes": 4616670880563,
  "usdPerTib": 6.25,
  "unestimated": 1,
  "gate": {"thresholdBytes": 107374182400, "exceeded": true},
  "checkedAt": "2026-10-06T09:30:00Z"
}
```

`bytes`, `usd` and `deltaBytes` are `null` when there is no figure, never 0; `accuracy` is BigQuery's (`PRECISE`, `UPPER_BOUND`, `LOWER_BOUND`, `UNKNOWN`); `totalBound` is `lower` or `upper` when the total is a bound; `gate` is there with `--fail-over`; `baseError` says why no action has a `base`; `unsetSetting` names the setting that would dry run against prod's tables (`prodCompilerOptions`, or `dbtProdTarget`) when nothing sets it.

`baseTotalBytes` is what the same actions scanned at the merge-base, a new action counting nothing, and `deltaTotalBytes` how many more the branch scans; both are `null` when the merge-base couldn't be compiled or an action other than a new one has no `base`.
`dryRunIn` is the project the dry runs ran in, with their location (left out when they ran in more than one), and an action's `dryRunAs` is the table they read for it when that isn't its `name` (prod's, with `prodCompilerOptions`).
When an incremental table's first build is in the total, `laterBytes`, `laterUsd` and `laterBound` give each run after the first, with its incremental run instead.

### Query cost in a pull request

[`xf check`](#pull-requests-xf-check) runs this check and column impact together, and its [GitHub Action](#github-actions) and [GitLab template](#gitlab-ci) post the report on the pull request; give them `fail-over` (`XF_FAIL_OVER`) to gate on cost.
To run `xf cost` alone in a job, set it up as [Other CI systems](#other-ci-systems) says, and run `xf cost --fail-over 100GB --output cost.md`.

## Agent skill for cost and impact

[`skills/xf-checks`](../skills/xf-checks/SKILL.md) is an [Agent Skill](https://agentskills.io) that teaches a coding agent (Claude Code, Codex and others that read skills) to run `xf cost --json` and `xf impact --json` on a branch and read what they report: what to fix, what is a bound, what wasn't checked.
Install it where your agent looks for skills, for every project or one.
For Claude Code, from a checkout of this repository:

```bash
mkdir -p ~/.claude/skills && cp -R skills/xf-checks ~/.claude/skills/
```

Other agents look elsewhere:

| Agent | Every project | One project |
|---|---|---|
| Claude Code | `~/.claude/skills/` | `.claude/skills/` |
| Codex | `~/.agents/skills/` | `.agents/skills/` |
| Antigravity | `~/.gemini/config/skills/` | `.agents/skills/` |
| T3 Code | where the agent it runs looks | |

A project's folder is shared with its team through git.
The skill grills you on each critical change before fixing anything, with the agent's own question tool when it has one (T3 Code shows Claude Code's and Codex's), else as a numbered list in its reply.
Then ask the agent what your branch costs, or whether it breaks anything downstream.
Copy it again when you update xf, so it matches the flags of the `xf` you run.
