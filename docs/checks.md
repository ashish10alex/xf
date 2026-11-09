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

An action that is new on the branch is marked 🆕: with no table in BigQuery yet there is nothing to lose, so it isn't dry run ("new: nothing reads it yet").
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

Until it is set, in a file or with the flag, every report says so: under "against" in the TUI, on stderr from `xf impact`, and as the last line of the Markdown:

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

In a Dataform project with `prodCompilerOptions` set, the dry runs are of the project compiled with those options, so the bytes are those of the tables prod runs read, not of your dev tables.
An action that needs a column another changed action adds can't be planned against prod until that one has run there, and is listed as not estimated.

Without it, `C` asks for it first, as `I` does (see [above](#check-what-your-branch-does-to-columns)), and until it is set the report says the dry runs read the project's default targets, which may be dev tables: in the TUI, in `xf cost`'s text and Markdown, and as `"unsetSetting": "prodCompilerOptions"` in its JSON.
A dbt project says the same of its target when nothing chooses one (no `dbtProdTarget`, `dbtTarget`, `--dbt-target` or `$DBT_TARGET`), with `"unsetSetting": "dbtProdTarget"`; it isn't asked for.

The check runs in the background engine, as `I` does: close the overlay and it carries on; open it again and the report is there, flagged when you've changed the branch since, until `c` checks again.
With `a` on in a Dataform project, it estimates what you pushed.

`xf cost` prints the same report from the command line, and can fail a CI job over a limit; see [below](#query-cost-in-cicd).

## Column impact in CI/CD

`xf impact` checks a pull request's column impact and prints the report as Markdown on stdout (or writes it to a file with `--output`), ready to post as a PR comment, with progress on stderr.
`--json` prints it as JSON instead, for scripts (see [below](#impact-json)).
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
| `--output path` | write the report to `path` instead of stdout |
| `--json` | print (or write) the report as JSON instead of Markdown |
| `--web` | open xf web on its Checks page, where the check runs as `I` runs it, instead of printing the report |
| `--default-branch name` | compare with `origin/<name>` rather than `main` (or set `defaultBranch` in `.xf/config.json`) |
| `--pushed` | check the branch pushed to origin instead of the checked-out files (Dataform only) |
| `--dataform-bin path` | the `dataform` binary to compile with |
| `--dbt-bin path`, `--dbt-target name` | the `dbt` binary, and the target xf compiles with |
| `--dbt-prod-target name` | compile a dbt project with this target and compare with the tables it builds (or set `dbtProdTarget`) |
| `--dev-compiler-options options` | fold readers that are dev runs built with these options into their actions (usually unset in CI) |

### Impact JSON

`--json` prints one document, and `--output impact.json` writes it to a file instead.
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

- **A Linux or macOS runner** with **Go 1.26+** to install xf, and **Node** for the `dataform` CLI (`npm i -g @dataform/cli`; run `npm ci` too if the project has a `package.json`).
- **The default branch's history.** xf never fetches: it needs `origin/main` and enough history to find the merge-base, so fetch everything (`fetch-depth: 0` in GitHub Actions) or at least `git fetch origin main`.
- **The PR's branch checked out**, so the report names it. A detached checkout still works, but the report shows a commit instead.
- **Google credentials** through Application Default Credentials: Workload Identity Federation, or a service account key in `GOOGLE_APPLICATION_CREDENTIALS`.
  The identity needs:

  | Role | On | For |
  |---|---|---|
  | `roles/bigquery.jobUser` | the project running dry runs | running the dry runs |
  | `roles/bigquery.dataViewer` | the dataset of each changed table | reading their current schemas |
  | `roles/datalineage.viewer` | each changed table's project | asking Dataplex for readers |

  With user credentials rather than a service account, the quota project (`gcloud auth application-default set-quota-project`) needs the Data Lineage API enabled too.

### GitHub Actions example

Add this to `.github/workflows/impact.yml`:

```yaml
name: Column impact

on:
  pull_request:
    types: [opened, synchronize, reopened]

jobs:
  impact:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write          # for workload identity federation
      pull-requests: write     # for sticky-pull-request-comment
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0       # xf needs history to find the merge-base

      - uses: actions/setup-node@v4
        with:
          node-version: "20"
      - run: npm i -g @dataform/cli
      # - run: npm ci  # if the project has a package.json

      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
          cache: false
      - run: go install github.com/ashish10alex/xf/cmd/xf@<tag or commit>

      - uses: google-github-actions/auth@v2
        with:
          workload_identity_provider: projects/123456789/locations/global/workloadIdentityPools/github/providers/github
          service_account: dataform-ci@my-project.iam.gserviceaccount.com

      - name: Check column impact
        id: impact
        run: |
          set +e
          xf impact --fail-on critical --output impact.md
          code=$?
          echo "code=$code" >> "$GITHUB_OUTPUT"
          [ "$code" -eq 2 ] && exit 2   # the check couldn't be made
          cat impact.md >> "$GITHUB_STEP_SUMMARY"
          exit 0

      - uses: marocchino/sticky-pull-request-comment@v2
        with:
          header: column-impact
          path: ${{ github.workspace }}/impact.md  # adjust if working-directory isn't the root

      - name: Fail on critical changes
        if: steps.impact.outputs.code == '1'\
        run: |\
          echo "::error::This PR drops columns or deletes tables that are still read. See the column impact comment."
          exit 1

      - name: Show the engine's log
        if: failure()
        run: cat "${TMPDIR:-/tmp}"/xf-*.log || true
```

Pull requests from forks get no `id-token` or secrets, so the check can't log in there. Run it on branches in the repository.

### Other CI systems

The same steps work anywhere a shell does. In GitLab CI, Cloud Build, Jenkins and the like:

```bash
git fetch origin main                     # xf needs origin/main; it never fetches
git checkout -B "$BRANCH" "$COMMIT"       # optional: the report names the branch
npm i -g @dataform/cli
go install github.com/ashish10alex/xf/cmd/xf@<tag or commit>
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json  # or workload identity

set +e
xf impact --fail-on critical --output impact.md
code=$?
set -e
[ "$code" -eq 2 ] && exit 2
# post impact.md to the merge request, e.g. with glab mr note
exit "$code"
```

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

### GitHub Actions example

The setup steps are those of the [column impact workflow](#github-actions-example); the check itself is:

```yaml
      - name: Check query cost
        id: cost
        run: |
          set +e
          xf cost --fail-over 100GB --output cost.md
          code=$?
          echo "code=$code" >> "$GITHUB_OUTPUT"
          [ "$code" -eq 2 ] && exit 2   # the check couldn't be made
          cat cost.md >> "$GITHUB_STEP_SUMMARY"
          exit 0

      - uses: marocchino/sticky-pull-request-comment@v2
        with:
          header: query-cost
          path: ${{ github.workspace }}/cost.md  # adjust if working-directory isn't the root

      - name: Fail over the limit
        if: steps.cost.outputs.code == '1'
        run: |
          echo "::error::This PR's changed actions scan more than 100 GB. See the query cost comment."
          exit 1
```

Run it after `xf impact` in the same job and the merge-base is compiled once for both.

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
