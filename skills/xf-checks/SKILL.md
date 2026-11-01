---
name: xf-checks
description: Check a Dataform or dbt branch before it merges, with the xf CLI. `xf cost` gives what its changed actions would scan and cost in BigQuery; `xf impact` gives the columns and tables it drops or retypes that something downstream still reads. Use when asked what a branch or pull request will cost, whether a change breaks downstream tables, or to review a data pipeline change before merging.
---

# xf checks

Both commands compare the checked-out branch (uncommitted files included) with its merge-base on `origin/<default branch>`, and only **dry run** and read metadata: they run nothing in BigQuery and cost nothing. Flags and settings are in `xf cost --help`, `xf impact --help` and `xf config`.

## Steps

1. **Ready the project.** From the project's root (or pass `--dir`):
   - `git fetch origin <default branch>`: xf never fetches, and needs the merge-base.
   - `xf config` shows the settings it will use. Note `prodCompilerOptions` (Dataform) or `dbtProdTarget` (dbt): without one, the checks read the default targets, which may be dev tables, and their figures describe dev. Its value is the user's to give; ask for it rather than guess.
   - A `403` or `Access Denied` later is usually the wrong Google account: `xf config --auth` names the one xf signs in with.

2. **Run** each check the question needs, JSON on stdout and progress on stderr:

   ```bash
   xf cost --json 2>cost.err >cost.json; echo $?
   xf impact --json 2>impact.err >impact.json; echo $?
   ```

   Each compiles the project and its merge-base, and can take minutes. Exit `0` is a report; `2` is a failed check, with the reason in the `.err` file. Exit `1` comes only with `--fail-over` or `--fail-on`, which are for CI gates.

3. **Read** the JSON with the fields below. Done when every `critical` and `warning` in `impact.json`, every `warnings` entry and every `error` in `cost.json`, and both totals are accounted for.

4. **Grill** the user on each `critical` risk before changing anything, as [Grilling critical risks](#grilling-critical-risks) says. Done when every `critical` column and table has a decision the user gave and confirmed, or, with no one to answer, is in the report as an open decision.

5. **Report** to the user: the totals and what the branch adds, each risk with the file to change, and anything left unchecked. Carry out the plan from step 4, then run the check again until each risk reads `low` or is gone.

## Grilling critical risks

A `critical` risk breaks a reader on merge, and whether that is acceptable is the user's decision. Find the facts first, yourself: why the branch removes it (`git diff $(git merge-base HEAD origin/<default branch>)` on the changed action's file) and how each reader uses it (its `filePath`, else its `table`). Then ask in **rounds**, each round holding every question whose earlier answers are in:

1. Per critical column or table: is the removal intended, or a mistake to put back?
2. Per reader of an intended removal:
   - in this project (`filePath`): update the reader on this branch, keep the column until the reader moves, or merge and let the reader break.
   - outside it: who owns it, and whether to keep the column until they move or merge and tell them.

Give each question 2 to 4 options with your recommendation first, drawn from the facts (a reader this project owns and only selects the column is a quick update on this branch), and a line on each option's trade-off. Use the harness's question tool when it has one (Claude Code's `AskUserQuestion`, Codex's `request_user_input` in plan mode), in batches if it takes only a few questions at a time; otherwise write the round as a numbered list of questions with lettered options, end your reply there, and wait for the answers. When no one is there to answer (a CI job, a headless run), put each question in the report as an open decision with your recommendation, and change nothing.

## Reading `xf cost --json`

- `totalBytes`, `totalUsd`: one run of every changed action. `totalBound` `upper` means the real figure is that much or less (clustered tables); `lower`, that much or more.
- `deltaTotalBytes`: how many more bytes the branch scans than the merge-base, both read against today's tables. `null` when the merge-base couldn't be estimated (`baseError` says why).
- `unsetSetting`: the dry runs read default targets (step 1).
- Per action in `actions`:
  - `bytes`, `usd`: one run. `null` (never 0) means no estimate, and `error` says why; it is left out of the totals.
  - `deltaBytes`: the change against the merge-base; `new: true` when the merge-base has no such action.
  - `warnings` of kind `missing_partition_filter`: the query reads `table` without filtering on its partition `column`, so it scans every partition. The fix is a `WHERE` on that column.
  - An incremental table counts its incremental run, with `fullRefresh` beside it. `firstBuild: true` means its table isn't built yet, so the first run is a full build; `laterBytes` at the top level is each run after that.
  - `dryRunAs`: the table the dry run read for it, prod's under `prodCompilerOptions`. `fromDev`: tables the branch adds that were read from dev, so their sizes are dev's.
- Sizes are binary units, as BigQuery bills: 1 GiB is 2³⁰ bytes, priced per TiB at `usdPerTib`.

## Reading `xf impact --json`

- `worst`: the most urgent severity found; absent when nothing is at risk.
- `atRisk`: each table with a dropped or retyped column (`columns[].change.kind` is `dropped` or `typeChanged`, the latter with `from` and `to`) or deleted on the branch (`deleted: true`). Every table and column has a `severity`:

  | `severity` | Means | Act |
  |---|---|---|
  | `critical` | a dropped column or deleted table that something still reads | grill the user (step 4) |
  | `warning` | a retyped column that something still reads | check each reader handles the new type |
  | `unknown` | its readers couldn't be looked up (`readersError`) | report it as unchecked: it is not safe |
  | `low` | nothing still reads it, or the base branch made the change (`change.onBase`) | nothing |

- `readers[]`: who reads the column, from Dataplex lineage. `filePath` is the reader's file when it is in this project; a reader without one is outside the project, so name its `table` to the user. `probablyUpdated` means the branch changed the reader and it no longer names the column; `stillNamed` means it changed it and still does.
- `unchecked`: changed tables the check couldn't compare, each with its `reason`.
- `unset`: the comparison was with default targets (step 1). It is spelled `unsetSetting` in `xf cost`.
