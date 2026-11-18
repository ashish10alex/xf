# dbt projects

Run `xf` in a dbt project's root and it compiles the project with the dbt in the project's `.venv` or `venv`, else the `dbt` on your `PATH` (or `--dbt-bin`, `$DBT_BIN`, `"dbtBin"`), dbt-core 1.x or dbt v2, and shows the same panes as for Dataform: every model by materialization (table, view, incremental, materialized view, ephemeral), seeds, snapshots, data and unit tests, analyses, sources and exposures, with their compiled SQL, columns, partitions and lineage.
Saving a file recompiles.
Compile errors show against the file dbt names (`e`).

- **A dbt project shows after about a second, before its compile is done.**
  `dbt compile` first lists the tables of every schema the project builds into, which on a large BigQuery project takes a minute or more, on every open and every save.
  So xf runs `dbt parse` first and shows every action and the lineage straight away, with each model's Jinja marked `compiling…`.
  It then compiles two things at once: the action you are looking at with the tests its Actions pane lists (one `dbt compile --select` of just those, a few seconds, or longer when the tests' schema is a large one dbt has to list too), and the whole project.
  Move to another action and xf compiles that one and its tests instead.
  The first action is the file open in your editor, else the one xf showed last, else the model you changed last (the newest file that differs from your default branch, or the newest file).
  After a save the saved file is compiled first and the other actions keep their last SQL, marked `outdated, compiling…`, until the whole compile lands.
  Dry runs and the columns pane wait for an action's compiled SQL; Run Changed (`R`) and query cost (`C`) wait for the whole compile.
- xf saves each clean dbt compile, as it does Dataform's: while nothing it was compiled from has changed (the project's `.sql`, `.yml`, `.csv`, `.py` and `.md` files outside `target/` and `logs/`, installed packages, `profiles.yml` in the project and in `~/.dbt` or `$DBT_PROFILES_DIR`, the dbt binary, the day), the next xf starts on it without running dbt at all.
  Each dbt binary and target keeps its own.
- dbt finds your profile as it always does (`profiles.yml` in the project, then `~/.dbt`).
  It compiles for `$DBT_TARGET` or else the profile's default target, unless you pick one with `--dbt-target prod` or `"dbtTarget": "prod"` in a config file.
  The target names the schemas (and so the actions' IDs) and anything your Jinja branches on with `target.name`; the status bar shows it when you set one.
  A target is picked when xf starts: restart it to switch, or in xf web pick one in pane 2's target section, which reopens the project.
- When it compiles, xf has dbt write its `target/` and `logs/` under its cache directory (`~/Library/Caches/xf/dbt` on macOS, `~/.cache/xf/dbt` on Linux, `%LocalAppData%\xf\dbt` on Windows), so compiling never touches the project's own `target/`, which your runs and `--state` comparisons use.
  Each dbt binary, target and `dbtCompileWithHooks` setting gets its own directory there and its own `xf serve`, so two xf on different targets, or one on dbt-core and one on dbt v2, don't share a graph.
- **dbt v2 runs `on-run-start` and `on-run-end` hooks when it compiles**, not only when it runs.
  So when a dbt v2 project (or a package) has hooks, xf stops at the parse and shows the graph with uncompiled SQL, marked `not compiled`, rather than run them on every save.
  Set `"dbtCompileWithHooks": true` in `.xf/config.json` (or `--dbt-compile-with-hooks=true`) to compile anyway. dbt-core doesn't run hooks when it compiles, so with it xf always compiles.
- An incremental model is compiled once, as dbt compiles it (as a full build when the table doesn't exist), so `v` doesn't apply.
- `r` runs the selected model, seed, snapshot or test and the rest of its Actions pane (a model's tests) with `dbt build --select`, naming each by its fqn (`xf_example.marts.fct_orders`), or with `s` the selected one alone; `d` adds `+` before each for its parents, `D` after each for its children, `f` adds `--full-refresh` (after asking) and the target you picked is passed on.
  The run uses dbt's own `target/` and `logs/` in the project, as a run from your terminal would, and runs the project's `on-run-start` and `on-run-end` hooks, as any `dbt build` does.
  The footer shows the exact command while the Run pane is focused, ready to paste into a terminal.
- A model's Actions pane lists the tests that read it too (its generic, singular and unit tests, and other models' `relationships` tests that point at it), the ones `r` runs with it; selecting a test shows the model it belongs to, and `[`/`]` move through the list.
  Each model, snapshot and test shows its dry run: the compiled query alone, since dbt keeps hooks only as Jinja, run in the model's own project with BigQuery picking the location.
  When xf could only parse a dbt v2 project, dry runs say `not compiled`; while an action's compile is still on its way they say `compiling…`.
- `R` ([Run Changed](running.md#run-what-your-branch-changed)) lets dbt decide what changed: xf parses the commit where your branch left `main` (a git snapshot, with your installed `dbt_packages/`; a parse runs nothing in the warehouse) and lists what `dbt ls --select state:modified --state <that parse>` selects in your working tree, so a changed macro counts too.
  Each says why: `sql` (its file), `config`, `new`, or `upstream` (a macro or contract it depends on, or an ephemeral model it inlines: dbt lists only the ephemeral model, which can't be built, so xf lists the models that inline it).
  Tests show only when they changed themselves, since `dbt build` of a model runs its tests anyway.
  `r` builds the checked ones by fqn, with `d`, `D` and `f` as for `r`; models removed on the branch are listed, never run.
  There is no `a`: dbt runs your working tree. dbt's own rules apply, so some edits (such as `+tags`) don't count as changes.
- The language server works in dbt projects' `.sql` files (filetype `sql`, or the `sql.jinja`, `jinja.sql` or `dbt` some dbt plugins set): go-to-definition from `ref()` to the model's file, from `source()` to the table's entry in its YAML and from a macro call to its `{% macro %}`, as well as CTEs; hovers with the table card over `ref()`, `source()` and `{{ this }}`, and a macro's signature and documented arguments over its call; completion of names inside `ref('…')` and `source('…', '…')`, of macros in a tag, and of whole `ref()` and `source()` calls after `{{`; and an outline of a file's CTEs, references and macros.
  Names resolve as dbt resolves them: `ref('name')` picks the latest version unless you pass `v=`, and a name in several packages picks the file's own package, then the project's.
  Installed packages (`dbt_packages/`) count; dbt's built-in macros don't.
  Diagnostics are the compile's errors and the file's dry runs, as in a Dataform project: BigQuery's errors point into the compiled SQL, and xf traces them back by lining up the SQL between the file's Jinja tags with it, so an error lands on the line and word that made it, an error in a `{{ }}`'s output (a `ref()`, a macro) on that tag, and one in an ephemeral model's inlined CTE on the `ref()` that brought it in.
  Each file also gets its bytes and cost as a hint, and BigQuery's "Did you mean …?" as a quick fix.
  A file is dry-run only once its compile is current, never with SQL marked `compiling…` or `outdated`.
- `C` and `xf cost` ([query cost](checks.md#check-what-your-branch-would-cost)) dry run the changed models, snapshots and tests against the tables your target resolves to. dbt has no `prodCompilerOptions`: set `dbtProdTarget` (`--dbt-prod-target`) to the profile target your prod runs use, such as `prod`, and the dry runs are of the project compiled with it instead, so they read prod's tables.
  To show the change against the merge-base, xf runs `dbt compile` on a snapshot of that commit, once per commit and day, which takes as long as a compile of the project and connects to the warehouse as any compile does. dbt v2 isn't compiled there when the snapshot has hooks (see above), and the change column is then blank.
  There is no `a` or `--pushed`: dbt compares your working tree.
- Column impact ([`I` in xf web, `xf impact`](checks.md#check-what-your-branch-does-to-columns)) compares the changed models (tables, views, incremental models and materialized views) with their tables in BigQuery.
  With `dbtProdTarget` set to another target than the one xf compiles with, xf compiles your working tree with it and works on that compile alone: it dry runs each changed model as prod would build it, reading prod's tables (which exist where a dev target's often don't), compares it with prod's table, and asks Dataplex who reads prod's columns.
  Readers that are runs of xf's own target, your dev builds, fold into the models they build.
  The merge-base is compiled with the same target, once per commit and day, to tell `main`'s changes from your branch's.
  Without `dbtProdTarget`, it compares with the tables xf's target builds, and when nothing picks that target either (no `dbtTarget`, `--dbt-target` or `$DBT_TARGET`) the report says it compared with the profile's default target.
  It isn't asked for, as `prodCompilerOptions` is.
  A dbt v2 project with hooks is only parsed (see above), so it can't be checked.
  The TUI has no `I` in dbt projects.

## Databricks

A dbt project on the dbt-databricks adapter builds in Databricks ([ADR 0042](adr/0042-databricks-is-a-second-warehouse-for-dbt.md)).
xf knows it from the compiled manifest's adapter, and before the first compile from the `type:` of the dbt target it compiles with.
Browsing, lineage, compiles and runs work as they do on BigQuery; what asks the warehouse is xf web's, and works differently, since a Databricks SQL warehouse bills while it runs.

- **A model is checked only when you ask**: `K` in xf web, or the palette's *Databricks: check the model*.
  The check runs `DESCRIBE QUERY` on the model's compiled query (its hooks left out) for the columns it returns, and `EXPLAIN` for its error.
  A stopped warehouse is started by the check: the model's `check` line says `warehouse starting` while it waits, and `warehouse started` beside the result.
  Nothing checks on a save, a compile or in the language server.
- **A built table's columns show without asking**, from Unity Catalog, which wakes no warehouse; the columns tab says they are the built table's.
  After a check, it shows the columns of the SQL as compiled instead.
- **Scratch queries and the dependency inspector run on the SQL warehouse** when you run them, starting it when it is stopped: the run line says `SQL warehouse starting` while it waits.
  There is no dry run, so nothing reaches the warehouse as you type; a statement that changes data is still asked about first, from its first words.
  A scratch's first line names the catalog its unqualified names are in.
  Results are read a chunk at a time, so a large one pages as on BigQuery; Databricks keeps a result for a short while, and has no table of it to search every row of, so a search covers the loaded rows.
  Completion lists Unity Catalog's schemas, tables and columns; Databricks SQL's functions aren't listed yet.
- **Hover cards** on a table, in xf web's editors and the language server, read Unity Catalog and link to Catalog Explorer.
- `u` opens a table in Catalog Explorer.
- **Column impact** (`xf impact`, `xf check`, and `I` in xf web) runs when you ask for it, and may start the SQL warehouse.
  It plans the changed models alone with `DESCRIBE QUERY`, each by its query without its hooks, and compares them with their tables in Unity Catalog.
  Unity Catalog's lineage says who reads a column the branch drops or retypes; a reader whose column lineage it doesn't track is listed as one that may read it.
  Readers aren't dry run, as they are on BigQuery.
  The report names the SQL warehouse, and says when the check started it.
  In xf web, an impact report the branch has moved on from is marked stale rather than checked again on its own: `c` checks again.
- **`xf check`'s preflight** probes the workspace and the SQL warehouse, which asking doesn't start, in place of Google and BigQuery.
  Without credentials it stops both checks; with no SQL warehouse chosen it warns, and query cost still runs.
- **Query cost** (`C`, `xf cost`, and `xf check`'s cost) is each changed model's last run, not an estimate: Databricks bills warehouse time, not bytes scanned, so there is nothing to estimate before a run.
  xf reads Query History, which wakes no warehouse, back 30 days, and keeps each model's latest build whose dbt query comment names it and the prod target (`dbtProdTarget`, else the target xf compiles with, which the report warns may be dev).
  It shows when the run started, how long it took, the bytes and rows it read (Query History counts no rows a table build writes), and the SQL warehouse it ran on, with no dollar figure; a model with no run says so.
  The run is of the model as it was then, not as the branch has it; `--fail-over` doesn't apply.
  dbt's query comment is on by default: a project that turns `query-comment` off gets no runs, and the report says why.
- Defer to prod is hidden.
- The TUI shows browsing, lineage, compiles and runs, and says on its status line that dry runs, columns and cost are off.

Where xf connects:

- **The workspace** is the dbt target's `host`, else the `~/.databrickscfg` profile's, else `$DATABRICKS_HOST`.
- **The SQL warehouse** is the `databricksWarehouse` setting (an ID or an HTTP path), else the dbt target's `http_path`.
  When neither names one, or names it in Jinja xf doesn't read, the first check lists the workspace's warehouses to pick from and saves the pick to `.xf/config.json`.
- **The token** is `$DATABRICKS_TOKEN`, else the dbt target's `token`, else the profile's `token`, else `databricks auth token --profile <profile>`, which refreshes an OAuth login (`databricks auth login --host <workspace>` first).
  The profile is the `databricksProfile` setting, else the `~/.databrickscfg` profile whose host is the dbt target's (or `$DATABRICKS_HOST`'s, when xf can't read the target's), else `DEFAULT`.

`examples/dbt-databricks` is a small project that reads the `samples` catalog every workspace has.
