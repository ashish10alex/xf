---
name: xf-run
description: Run a Dataform or dbt project's actions with the xf CLI. `xf run` is `dataform run` (`dbt build` in dbt) with the tool's own flags plus `--defer`, which reads upstream tables not built in dev from prod instead of building them, and `--dry-run`, a free plan. Use when asked to run, build or test models or actions, or to see what a run would build, read and scan before running it.
---

# xf run

`xf run` takes the tool's own run flags as given (`dataform help run`, `dbt build --help`) and adds xf's. A real run writes to BigQuery and costs money, so every run starts as a free **dry run** that the user approves. Flags and exit codes are in `xf run --help`.

## Steps

1. **Ready the project.** From the project's root (or pass `--dir`):
   - `git fetch origin <default branch>`: the stale check compares the branch with its merge-base there, and xf never fetches.
   - `xf config` shows the settings. `--defer` needs `prodCompilerOptions` (Dataform) or `dbtProdTarget` (dbt). The value is the user's to give: ask for it rather than guess.
   - A `403` or `Access Denied` later is usually the wrong Google account: `xf config --auth` names the one xf uses.

2. **Plan** with a dry run. Pass `--defer` unless the user wants every upstream table built in dev:

   ```bash
   xf run --actions <name> --defer --dry-run --json 2>run.err >plan.json; echo $?
   ```

   Exit `0` is a plan; `1`, a plan where some dry runs failed; `3`, a plan the run would refuse (`refusal` says why); `4`, no plan, with the reason in `run.err`. Dataform's `--actions` takes names as compiled, table prefix included; `run.err` says when a name matches nothing. Done when every action in `builds` has a dry run that passed or reads a table the run builds first, and any `refusal` is settled in step 3.

3. **Settle a refusal** (exit `3`). Each needs a decision, so put it to the user with your recommendation, then dry run again with the flag they chose:
   - `stale`: tables read from prod that this branch changes, so the run would test prod's version, not the branch's. Recommend `--defer-build-changed` (builds them in dev first, in this run); `--defer-allow-stale` runs all the same.
   - `cannotDefer`: tables that can't be read from prod. Add `--include-deps` (dbt: select them too, `+model`).
   - `proxyViews` (without `--defer`): views left by an earlier deferred run would make the run read prod. Add `--defer`, `--include-deps`, or `--allow-proxy-views`.

4. **Ask before the real run.** Show the user what it builds (`builds`), what it reads from prod (`readsFromProd`), what the dry runs scan (`totalBytes`, `totalUSD`) and the `command`. A real run needs their yes. With no one to answer (a CI job, a headless run), report the plan and run nothing.

5. **Run** the same command without `--dry-run`. stdout is the tool's own output, and `xf:` lines on stderr say what was read from prod. The exit code is the tool's (or `3`/`4` as in step 2). The run goes into xf's run history, where xf web shows it.

6. **Report** the outcome: what was built, what was read from prod, and anything failed or left unchecked.

## Proxy views

In Dataform, `--defer` reads prod through **proxy views** that xf makes at the dev tables (labelled `dataform_tools_proxy`). Leave them in place: the next deferred run uses them again, and a real dev build replaces them. Remove them only when the user asks: `xf run --defer-remove-views --dry-run` lists them (with a selection, those upstream of it), and the same command without `--dry-run` removes them. dbt makes no views.

## Reading `xf run --dry-run --json`

- `command`: the tool's command a real run starts. `added`: actions xf added to the run (functions and procedures, or with `--defer-build-changed`, the stale tables).
- `builds[]`: each action the run builds, with its `dryRuns[]` (an incremental table has a `full` and an `incremental` one). Each has `bytes` and `costUSD`, or `error`, or `builtByRun`: a table it reads that isn't built yet and that this run builds first, so the dry run couldn't find it. That one is expected, not a failure.
- `readsFromProd[]`: `dev` → `prod`, with `stale: true` for a table this branch changes. `readsFromDev[]`: upstream tables built in dev, with `builtAt`.
- `proxyViews`: leftover views a run without `--defer` reads prod through.
- `refusal`: `reason` (`stale`, `cannotDefer` or `proxyViews`), `tables`, and `flags` that settle it.
- `totalBytes`, `totalUSD`: what the dry runs scan, the queries' bytes as BigQuery bills them. `failed`: dry runs with an `error`.
