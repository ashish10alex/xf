---
status: accepted
---

# dbt column impact compiles the branch with dbtProdTarget

Column impact works in dbt projects, in xf web (`I`) and `xf impact`. A new setting, `dbtProdTarget` (`--dbt-prod-target`), names the profile target the project's prod runs use, such as `prod`. This is the prod target that ADR 0002 said would join `dbtTarget`. When it names another target than the one xf compiles with, the check compiles the working tree with it and works on that compile alone:

- it dry runs each changed model as that compile has it, so the SQL reads prod's tables;
- it compares the result with the model's table under that target, and asks Dataplex who reads that table's columns;
- it compiles the merge-base with the same target (a snapshot, cached per commit and day) to tell the base branch's changes from the branch's.

The Changed Actions are worked out on xf's own compile, so they are renamed to the prod compile's tables (`impact.Retarget`), matching models by kind and fqn. A deleted model is in neither compile, so its prod table is guessed from a model that shared its dataset. The same map folds readers that are runs of xf's target, usually your dev builds, into the models they build. Query cost (`C`, `xf cost`) uses `dbtProdTarget` too, so one setting picks the tables both checks use, as `prodCompilerOptions` does in Dataform.

Without `dbtProdTarget`, or with xf's own target, both checks use xf's compile. The reports say they used the profile's default target only when nothing picks a target: no `dbtProdTarget`, `dbtTarget` or `$DBT_TARGET`.

## Why

A dev target's upstream tables often don't exist: dbt users build only what they changed, and `--defer` reads the rest from prod. Dry running xf's dev compile would then fail for most models, while prod's upstream tables are there. Dataform's check dry runs xf's own compile and only names prod's tables (`Deps.ProdTarget`), because a Dataform dev schema usually holds the whole project. A dev table you have already built from your branch also has the branch's columns, so comparing with it shows nothing.

## Considered options

- **Dataform's way: dry run xf's compile, and map each action to prod's table with a `dbt parse --target prod`.** A parse is cheaper and connects to nothing, but the dry runs would read the dev target's tables, with the failures above.
- **No new setting: compare with whatever `dbtTarget` is, as `xf cost` did.** This works in CI with `--dbt-target prod`. In xf web, though, the target is the one you browse with, usually dev, and switching it to prod recompiles everything under prod's names.
- **A prompt for the prod target in xf web, as `prodCompilerOptions` has.** It is left out for now. The reports say when no target was picked, and the setting is on the settings page.

## Consequences

- A check with `dbtProdTarget` runs one more `dbt compile` of the working tree, connecting to the warehouse with the prod target's credentials as any compile does. In CI, that identity needs to read prod's tables anyway.
- The TUI has no `I` in dbt projects. It is in maintenance mode, so its `C` only passes the setting on.
- A dbt v2 project with hooks is only parsed (ADR 0002), so it can't be checked: the check says why.
- `ImpactRequest` and `CostRequest` carry `dbtProdTarget`, so `remote.ProtocolVersion` went to 20.
