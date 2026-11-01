---
status: accepted
---

# xf web's pane 2 has a target section, and lineage is a tab

Pane 2 has a **target** section where lineage's folded line was, open by default, and lineage moved into the tabs of the SQL's box: `sql | columns | lineage`. The section sets what a compile adds to the names the project builds. For Dataform these are a table prefix, a schema suffix and a database suffix, the CLI's `--table-prefix`, `--schema-suffix` and `--database-suffix`, which `workflow_settings.yaml` calls `namePrefix`, `datasetSuffix` and `projectSuffix`. For dbt the section is the profile's targets, since dbt has no prefix or suffix flags: its target names the datasets, and `+schema` models land in `<dataset>_<schema>`.

The fields are the settings `tablePrefix`, `schemaSuffix`, `databaseSuffix` and `dbtTarget`. The section writes them through the settings page's API, so the section, the page and the file always agree: to the file that already sets them, else to `.xf/config.json`. The settings page's Global tab keeps them in the user's config instead.

At first the section had a footer line under the fields: **project** and **you** chips that moved the values between the two files, the selected action's table with what the names add, and a folded table of where every dataset's actions land. The user took that line out (2026-10-23) as not needed.

`O` shows the lineage tab, and from it goes back to the SQL. `tab` goes round sql, columns and lineage (and errors).

## Applying a change

The names are part of the Dataform backend, as dbt's target is of dbt's: the engine compiles with them, its name (`Variant`) and its saved compiles and base snapshots are keyed on them, `xf serve` is started with them, CLI runs pass them, and API runs send them as the compile's `codeCompilationConfig`. So, as for `dbtTarget` before, a change reopens the project: the section saves, then reopens. The page reloads, and selects again the action that was selected, found by its file and the name the prefix doesn't change, since its ID changed.

A reopen opens the project again beside the one on show, which stays open and drawn, its target section saying it is compiling with the new names. Only once the new engine's first compile is in (a saved compile counts; 90 seconds at most) do its pages load again, on a replace rather than a reload, so the view transition holds the old page until the new one has drawn: no blank page, and no empty panes while it compiles. The session goes across with its actions under their new IDs, so the page lands on the same action. A reopen that fails to open leaves the old project as it was.

## Why

People who develop in Dataform build into their own datasets with a schema suffix, and there was no way to set one in xf web short of the free-text `devCompilerOptions`, which only column impact read, so the compiled SQL and the runs used the default targets. Of the four mockups (2026-10-22: the target row of the action card editable in place, a folding overrides section, a target tab beside sql and columns, and this), the user picked the target tab, then asked for lineage to move into the tabs and a compact target section open by default in its place: the names are always in view, two lines tall, and lineage keeps a tall box.

## Considered options

- **Compile again in place, without a reopen.** The engine is shared (`xf serve`) and keyed by what changes a compile's output; switching its options under its clients would mix saved compiles and Changed Actions' bases of other names. A reopen takes a few seconds and keeps the selection.
- **Write the names into `devCompilerOptions`.** One free-text key for column impact and for compiles would need parsing and rewriting flags in a string. The keys stay apart; `devCompilerOptions` still says what dev runs were built with, for column impact.

## Consequences

- `.xf/config.json` is meant to be committed, so a suffix saved there applies to everyone who opens the project. The settings page's Global tab keeps it in the user's config.
- dbt's profile is read from `profiles.yml` line by line (xf has no YAML library), with `env_var()` resolved; other Jinja shows as written.
- The first load fetches two more modules (`target.js`, `target-view.js`); `TestFirstLoadBudget` says so.
