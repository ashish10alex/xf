---
status: accepted
---

# xf run defers to prod from the CLI

Agents run Dataform and dbt from a terminal: `dataform run`, `dbt build`.
Defer to prod (ADR 0031) was only in xf web, so an agent that ran one model had to build everything upstream of it in dev first.
`xf run` gives the CLI the same defer to prod.
It is a copy of the tool's own run command with one more flag, as dbt's `--defer` is a flag of `dbt run`.

## Decision

**`xf run` is the tool's run command.**
In a Dataform project it takes `dataform run`'s flags, and in a dbt project `dbt build`'s, which is what xf web runs. xf passes the flags to the tool as given.
It reads only the flags that select actions (`--actions`, `--tags`, `--include-deps`, `--include-dependents`; dbt's `--select`, `--exclude`, `--selector`, `--resource-type`), so that it knows what the run builds.
It adds what xf's own runs add: the name settings (`tablePrefix`, `schemaSuffix`, `databaseSuffix`) or the dbt target, and, when the CLI can do it, a job prefix.
A flag that the user gives is not given twice.
Runs are local only.
API runs come later.

**`--defer` turns defer to prod on for this run alone.**
It is off by default.
It does not read or change the project's switch that xf web and nvim share, so an agent's run does not depend on state that the agent cannot see, and the agent does not change that state for anyone else.
The engine takes the switch per request (`DeferralRequest.On`).

- Dataform: before the run, xf makes the Proxy Views, as xf web does.
- dbt: xf adds `--defer --defer-state <prod's manifest>`, from the same parse with `dbtProdTarget` as xf web uses.
  While the shared switch is off, the engine's compile does not defer, so `xf run` compiles the project once with `--defer` to find out what dbt reads from prod.
- **Flags that change the compile are refused with `--defer`.**
  For Dataform these are `--default-database`, `--default-schema`, `--default-location`, `--assertion-schema`, `--vars`, `--database-suffix`, `--schema-suffix` and `--table-prefix`.
  For dbt they are `--target`, `--vars`, `--profile`, `--profiles-dir`, `--state`, `--defer-state` and `--favor-state`. xf works out what to defer from its own compile, so another compile would make the run write to different tables than the ones xf checked.
  They go in xf's config instead.
  Without `--defer`, they go to the tool as given.

**The checks that xf web asks about become refusals.**
An agent cannot answer a dialog.
So a run that needs a decision stops with exit code 3, and stderr names the flag that makes the decision:

- **Stale Deferral** (Dataform and dbt): an upstream table read from prod that the branch changes.
  `--defer-build-changed` adds those tables to the run, built in dev before the tables that read them.
  `--defer-allow-stale` runs all the same.
- **A table that cannot be deferred** (in neither place, no read access to prod, no dev dataset): there is no flag that allows it.
  The tool's own `--include-deps` builds it in dev.
- **Proxy Views left over, with `--defer` not given** (Dataform): the run would read prod.
  `--include-deps` builds those tables in dev.
  `--allow-proxy-views` runs all the same.
- A function or procedure is added to the run, as in xf web.

**`--dry-run` is xf's, in both tools, with or without `--defer`.**
Dataform's own `--dry-run` would fail on every upstream table that is not built in dev, unless xf first wrote Proxy Views. xf's dry run writes nothing and costs nothing.
It dry runs the SQL of each action that the run builds, with deferred tables read from prod.
It then prints the plan: what the run builds, what it reads from prod, which reads are stale, and each dry run's bytes or error.
A table that this run builds and that is not built yet is listed as "built by this run", not as an error.
The plan is text, or JSON with `--json`.
Dry runs are not kept in the run history.
A dry run that finds what a real run would refuse exits 3 in the same way.

**`--defer-remove-views` removes Proxy Views and exits.**
With no selection, it removes all of the project's Proxy Views, as xf web's palette command does.
With a selection, it removes only the views upstream of that selection.
With `--dry-run`, it lists the views and removes nothing.
Proxy Views have the extension's label, so this also removes views that the extension made.

**Output.** stdout is the tool's own output, so its `--json` is not changed. xf's lines go to stderr, after `xf:`.
A dry run's plan goes to stdout, because no tool runs.

**Exit codes.**
A run exits with the tool's code (Dataform's 1, dbt's 1 and 2).
A dry run exits 1 when one of its dry runs fails, as Dataform's own `--dry-run` does. xf exits 3 when the run needs a decision, and 4 when xf could not check the run (a compile error, a BigQuery error, a bad flag).

**History.** A real run is kept in xf's run history with what it read from prod, as a run from xf web is. So xf web shows the run and can run it again.

**Agents learn it from a skill,** `xf-run`, apart from `xf-checks`: those checks are free, and runs are not.
The skill tells an agent to dry run first, show the user the plan, and get a yes before a real run.
It also tells the agent to leave Proxy Views in place unless it is asked to remove them.
The next deferred run uses them again, and a real dev build replaces them.

## Why

- **Compute.**
  An agent that tests one model reads its upstream tables from prod instead of building them in dev.
  In dbt, `--defer` already does this.
  In Dataform, nothing else does.
- **A copy of the tool, not a new language.**
  Agents and people already know `dataform run` and `dbt build`.
  One added flag is easy to learn, and the tool's own flags go on working.
- **Refusing by default** stops the trap that a person sees in xf web and an agent does not: a run that succeeds against the prod version of a table that the branch changed, which tests the wrong thing.
- **xf owns the run.**
  It makes the views for the same set of actions that it then runs, and keeps the run in the history.
  A separate prepare step would let the two sets differ.

## Considered options

- **`xf defer prepare`, which makes the views and prints the tool's command:** less code, but the agent selects the run twice and the run is not in the history.
- **Following the project's switch, with `--defer` and `--no-defer` to override:** the same as xf web, but the result depends on state that the agent does not see.
- **xf's own selection flags for both tools:** one set of flags, but neither tool's users know them.
- **Dataform's own `--dry-run` after making the views:** close to the tool, but then a dry run writes to dev.
- **Warning about Stale Deferrals and running anyway, or building them always:** the first relies on an agent reading stderr.
  The second builds more than was asked for.
- **dbt's `--defer` as it is, without the stale check:** what dbt users expect, but the same trap for agents.
- **Compiling again with the flags that change the compile:** closer to the tool, but a second compile for each run, and more code.

## Consequences

- `remote.ProtocolVersion` goes up for `DeferralRequest.On`.
- The checks before a run are now in `internal/frontend`, and xf web's dialogs are built on top of them.
- With `--defer`, a dbt run compiles the project once more.
- An agent that follows the skill always asks the user before it spends BigQuery money.
