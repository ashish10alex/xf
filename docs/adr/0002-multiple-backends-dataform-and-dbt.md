---
status: accepted
---

# Open Dataform and dbt projects through one model, as xf

The TUI only knew Dataform, but nothing it shows (actions, their targets, compiled SQL, lineage, compile errors) is Dataform's alone. dbt users on BigQuery want the same loop.
So the tool is now `xf` (it was `dft`, which named Dataform), and every tool that compiles a project sits behind `backend.Backend` (compile, parse a sent or saved graph, watch files).
Each backend turns its tool's output into a tool-neutral `project.Graph`, and the engine, TUI and language server only see that graph.
Dataform is `backend/dataform` (`dataform compile --json`); dbt is `backend/dbt` (`target/manifest.json`, from dbt-core 1.x or dbt v2).
BigQuery is still the only warehouse.

- **One model, more kinds.** dbt resources map onto actions; the kinds dbt adds (materialized view, ephemeral, seed, snapshot, test, analysis, source, exposure) join the list rather than being forced into Dataform's (a source is not a declaration in the UI's words, though it plays the same part).
  An action's ID is still its target, `database.schema.name`: for dbt the relation a resource builds, which keeps lineage, console links and the two tools' examples comparable.
  Exposures and unit tests have no relation, so their targets are named after their resource type (`exposure.revenue_dashboard`).
- **The project is the working directory.** xf no longer walks up to a settings file: the working directory, or `--dir`, must be the root.
  `workflow_settings.yaml` or `dataform.json` makes it Dataform's, `dbt_project.yml` dbt's; a directory with both needs `--backend`.
  Walking up would have to guess between a dbt project nested in a Dataform one and the other way round.
- **dbt compiles out of the project's way.** xf passes `--target-path` and `--log-path` under its cache, so compiling on every save never rewrites the `target/manifest.json` that the user's runs and `--state` comparisons rely on.
- **The dbt target is part of what xf compiles.**
  A target names the schemas, and so every action's ID, and Jinja can branch on `target.name`, so the graph depends on it.
  `dbtTarget` (`--dbt-target`) passes `--target` to `dbt parse` and `dbt compile`; unset, xf passes none and dbt picks as usual (`$DBT_TARGET`, then the profile's default).
  It is chosen when xf starts; there is no switcher, because xf would have to read `profiles.yml` (Jinja, `DBT_PROFILES_DIR`) to list the targets.
  The dbt binary, the target and `dbtCompileWithHooks` make the backend's variant, a short hash in the name of its artifact directory and of its `xf serve` socket, so a client never attaches to an engine that compiles differently and each keeps its partial-parse state.
  The binary counts as its resolved path (symlinks followed), not the version `dbt --version` prints: the path costs nothing to work out before connecting, where `--version` costs a dbt-core start-up on every launch, and two installs of one version can still differ in adapters.
  A Homebrew upgrade repoints the `dbt` symlink, so it gets a fresh engine too; one binary reached by two unrelated paths gets two engines, which is only wasteful.
  It is one target, for browsing; a prod target joins it when running or column impact come to dbt, as `prodCompilerOptions` did for Dataform (it did, as `dbtProdTarget`: ADR 0028).
- **dbt v2 is only parsed when the project has hooks.** dbt v2 (unlike dbt-core) runs `on-run-start` and `on-run-end` hooks during `dbt compile`, against the warehouse.
  A TUI that compiles on every save must not do that, so for v2 xf runs `dbt parse` first and, when the manifest lists hooks (the project's or a package's), shows the parsed graph with uncompiled SQL and says why.
  `dbtCompileWithHooks` opts in to compiling.
  A project without hooks is compiled as usual, and since hooks live only in `dbt_project.yml` files, the parse is skipped on later compiles until the project's or a package's `dbt_project.yml` changes.
- **dbt support starts with browsing, running and dry runs.**
  Compile, browse, compiled SQL, lineage, errors, recompile on save, running the selected action, Run Changed, dry runs and the language server work for dbt.
  Column impact stayed Dataform's at first; ADR 0028 brought it to dbt (dbt got saved compiles, and a staged open that shows the parsed project before its compile is done); the TUI hides the panes and keys a backend doesn't support instead of showing them disabled.
- **The language server reads dbt's Jinja itself, and doesn't dry-run.**
  `xf lsp` serves a dbt project as fully as a Dataform one, from the same shared engine, for dbt-core (which has no language server of its own) and dbt v2. `navigate` finds `{{ }}` and `{% %}` tags and the calls in them without a Jinja parser, and resolves `ref()` (with a package, or `v=`), `source()` and macro calls the way dbt does: the latest version unless `v=` says otherwise, and the file's own package before the project's.
  A ref jumps to the model's file, a source to its table's `- name:` line in the YAML, a macro to its `{% macro %}` tag.
  So that macros resolve, the trimmed manifest and `project.Graph` carry the project's and installed packages' macros (name, package, file, YAML docs, not their SQL), and each node's package, version and source name (`Action.DbtName`); dbt's own macros (`dbt`, `dbt_bigquery`) are left out: hundreds nobody edits, whose files are in dbt-core's Python package or, for v2, nowhere.
  Diagnostics are the compile's errors alone.
  BigQuery reports a dry run's errors at a line of the compiled SQL, and every `{{ }}` above it shifts the lines by however many its expansion takes, so there is no reliable way back to the source line; and without dry runs there are no "Did you mean" quick fixes.
  The TUI still dry-runs dbt models.
- **A dbt model is shown with its tests, and dry-run without its hooks.**
  A model's Actions pane lists it and every test that reads it (attached, singular, unit and other models' relationships tests), which is what `dbt build --select` of it runs, though they live in other files.
  A test picked from elsewhere shows its home model's list: the model it is attached to, else the only one it reads; moving within the pane keeps the list.
  Models, snapshots and tests are dry-run with their compiled query alone, in the model's project with BigQuery picking the location: the manifest holds hooks only as Jinja (`Action.RawOps`), and dbt hooks rarely declare what the query needs, as Dataform pre_operations do.
  An incremental model has one dry run, of what dbt compiled.
  A graph dbt v2 only parsed shows `not compiled` instead of dry-running Jinja.
- **A dbt run is `dbt build` of the selected node, in the project.**
  `r` runs `dbt build --select <fqn>`, with `+` before the selector for `d`, `+` after it for `D`, `--full-refresh` for `f` and the backend's `--target`, from the project root, so `target/` and `logs/` land where a run from the terminal puts them (only compiles stay in xf's cache).
  `dbt build` rather than `dbt run` because it builds every kind `r` can select (models, seeds, snapshots, tests) and runs a model's tests after it in DAG order, as a Dataform run runs assertions.
  Nodes are selected by their fqn (`xf_example.marts.fct_orders`), kept on the action as `RunName`: dbt-core and dbt v2 both match it to exactly that node, where `package.name` matches nothing once a model is in a folder and a bare name is ambiguous across packages and model versions.
  Hooks and disabled nodes are marked `NoRun`.
  A backend that can run implements `backend.Runner`, which also gives the command line the TUI shows; the Dataform API toggle stays Dataform's.
- **dbt decides what Run Changed runs; xf supplies the base.** dbt has `state:modified`, but only against a manifest you give it with `--state`, usually a prod run's. xf makes that base the way it does for Dataform, from a git snapshot of the merge-base with `defaultBranch`, with the working tree's `dbt_packages/` mirrored in and its profile, and runs `dbt parse` on it: state:modified compares files and configs, not compiled SQL, so a parse is enough, needs no warehouse and runs no v2 hooks.
  The whole manifest is cached (the trimmed one lacks checksums and configs), keyed by commit, engine name and day.
  `dbt ls --select state:modified --indirect-selection empty --state <base>` then lists the working tree's modified nodes, in a target path of xf's own; tests come only when they changed themselves, because `dbt build` of a model runs its tests. xf keeps the nodes `dbt build` runs, says why each changed by comparing the two manifests (`sql` for the file's checksum, `config`, `new`, else `upstream`: a macro or contract), lists runnable nodes the base has and the working tree doesn't as removed, and runs the checked ones by fqn, as `r` does. dbt reads the working tree itself, so a dbt project can't compare the pushed commit, and has no `a`.

## Considered options

- **Have dbt produce Dataform's types.**
  The smallest change, but Dataform-only fields and names would leak into everything and dbt's kinds would have nowhere to go.
- **Make `Graph` and `Action` interfaces each backend implements.**
  More flexible, but every UI call site becomes a method call for no present gain; one plain struct both backends fill is easier to test.
- **Compile dbt v2 anyway, or compile a copy of the project with its hooks removed.**
  The first runs DDL and inserts on save; the second breaks on package hooks, relative paths and partial-parse state.
- **Read the dbt profile for the dry runs' project and location.** dbt runs its jobs in the target's execution project and location, but finding them means rendering `profiles.yml` (`env_var`, `DBT_PROFILES_DIR`) or calling `dbt debug`; the model's own project, with BigQuery inferring the location from the tables a query reads, is right for most projects and needs nothing.
- **Run dbt with `dbt run`, or a command per kind (`run`, `seed`, `snapshot`, `test`).**
  `dbt run` can't run seeds, snapshots or tests; a command per kind drops the neighbours `d` and `D` add when they are of another kind.
- **Diff dbt's compiled graphs in Go, as for Dataform.**
  Instant refreshes, but the base would need a full `dbt compile` (warehouse queries, and v2 runs hooks), a parse-only graph has no compiled SQL to compare, and xf would re-implement what dbt knows about macros and contracts.
  Asking dbt costs a `dbt ls` per refresh instead (about half a second on v2, a few seconds on dbt-core).
- **Take the base from a `--state` directory the user supplies (a prod run's artifacts).** dbt's usual slim-CI setup, but `R` would do nothing until it is set up.
- **Leave dbt editing to dbt's own language servers, or only add what they lack (BigQuery hovers, dry runs).** dbt v2 ships a language server, but dbt-core has none, and xf's hovers and jumps work the same in both tools' projects.
- **Dry-run dbt files in the editor, placing errors by aligning the compiled SQL with the source.**
  Identical lines line up, but a macro's expansion, a Jinja `if` or a loop leaves lines that match nothing, and a misplaced error is worse than one at the top of the file.
  Left for later, with a source map from the Jinja.

## Consequences

- The examples (`examples/dataform`, `examples/dbt` and their broken and hooks variants) model the same project under the same names, and a parity test compares the two compiled graphs.
  A change to one example's shared part needs the same change in the other.
- `xf serve` sockets and logs are per backend and variant (`xf-<hash>-<tool>-<variant>-v<n>.sock`), so `--backend dataform` and `--backend dbt` on one directory each get their own engine.
  Both tools' variants include the binary's resolved path, so a client never attaches to an engine compiling with another `dataform` or `dbt`.
  An editor whose `xf lsp` resolves a different `dataform` than the TUI gets an engine of its own; `xf --nvim` passes `--dataform-bin` to its language server, so they share one.
- A dbt run runs the project's `on-run-start` and `on-run-end` hooks, as any `dbt build` does; xf doesn't warn about them.
- The trimmed dbt manifest a graph carries keeps each node's `fqn`, which changed what `xf serve` sends (protocol v6), and later each node's package, version and source name and the non-built-in macros, for the language server (protocol v8).
- A package's resources and macros are in files under `dbt_packages/<package>/`, though dbt names them relative to the package; xf joins the two, so `o` and the editor open the right file.
  A project that moves its packages (`packages-install-path`) gets the default path anyway.
- The language server's dbt navigation is pattern-matching, not a Jinja parser: a `ref()` built from variables (`ref(model_name)`) or a macro reached through `adapter.dispatch` has nowhere to jump to.
- Run Changed in a dbt project lets dbt's rules decide, so it differs from Dataform's at the edges: an edit dbt ignores (`+tags`) isn't a change.
  One gap xf fills: dbt lists a changed ephemeral model but not the models it is inlined into, and only those can be built, so xf lists them instead, as `upstream`.
  It reaches `xf serve` as protocol v7, so a TUI never asks an older serve that refuses it.
