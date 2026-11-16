---
status: accepted
---

# Databricks is a second warehouse, for dbt projects

BigQuery was xf's only warehouse (ADR 0002), and `internal/bigquery` is a concrete client that about 45 files call directly.
A dbt project on the dbt-databricks adapter already compiles, browses, shows lineage and runs `dbt build` in xf, because dbt does that work; everything that asks the warehouse (dry runs, columns, scratch queries, impact, cost) calls BigQuery and fails.
xf web now supports Databricks as a second warehouse, for dbt projects only: Dataform builds in BigQuery alone.

## Decision

**A warehouse interface first, in one change that alters no behaviour.**
A core `Warehouse` interface holds what every warehouse does: check a query (its columns, the tables it reads, its error), a table's metadata, run a query and page its rows and cancel it, and list catalogs and tables.
What only some warehouses do is a separate interface that callers type-assert, as `backend.Runner` is for backends: `Coster`, `ViewWriter` (defer to prod's proxy views), `JobLister` (active and child jobs).
A capability a warehouse lacks is hidden in the UI, not shown disabled.
Columns are a neutral `Column`: name, the type as the warehouse spells it (`INT64`, `BIGINT`, `STRUCT<a: INT>`), nested fields, repeated, description.
Impact compares types within one warehouse, so there is no table mapping one warehouse's types to another's.
Every call site moves onto the interfaces in one change, the TUI's included, with BigQuery as the only implementation and the tests unchanged; Databricks code comes after it.

**What makes a project Databricks.**
The compiled manifest's `metadata.adapter_type` decides, and goes on `project.Graph` as its warehouse.
Before the first compile is done (the staged open, ADR 0028), the `type:` of the dbt target xf compiles with stands in, read by `internal/backend/dbt/profiles.go`.
When they disagree, the manifest wins.

**Where xf connects.**
The warehouse is the `databricksWarehouse` setting; unset, the dbt target's `host` and `http_path`; when the profile doesn't say, or says it in Jinja xf doesn't read, xf web asks with a form listing the workspace's warehouses and saves the pick.
The token is `DATABRICKS_TOKEN` or the profile's `token` when there is one; an OAuth profile's comes from `databricks auth token --profile <name>`, the CLI's own refresh.
The profile is the one in `~/.databrickscfg` whose host is the dbt target's, else `DEFAULT`; the `databricksProfile` setting overrides it.
`internal/databricks` talks plain REST (the SQL Statement Execution API, Unity Catalog, Query History), as `internal/bigquery` does, with no SDK.

**A check runs only when asked.**
A BigQuery dry run is free, so xf runs one on every save; Databricks has no dry run, and its stand-ins need a running SQL warehouse, which bills while it is up and stays up until it auto-stops.
So a Databricks check is a command in xf web: `DESCRIBE QUERY` gives the columns the model's SQL returns and `EXPLAIN` its error.
The tables a query reads stay empty: what needs them (defer to prod, lineage's source tables) is hidden for Databricks.
A stopped warehouse is started by the check, and xf web shows `warehouse starting` beside the result, so the wait and the bill are visible.

**Columns of a built table come free.**
The columns of the table a model already built come from Unity Catalog's REST API, shown without asking, as no warehouse wakes.
The columns its edited SQL would make come only with a check.

**Phases.**
1. Detection, the connection, the on-demand check and both kinds of columns; SQLFluff's `databricks` dialect and links to Catalog Explorer.
2. Scratch queries and the table inspector, through the Statement Execution API and Unity Catalog; hover cards on tables in the language server and xf web's editor read Unity Catalog too.
3. `xf impact` and `xf check`.
   These are runs someone asks for, so they may wake the warehouse: `DESCRIBE QUERY` for the changed models alone, and the report says a warehouse was used.
4. Cost, as a past run.

**Cost on Databricks is the model's last run.**
Databricks bills warehouse time, not bytes scanned, and prices no single query, so there is nothing to estimate before a run.
xf reads the Query History API (no warehouse wakes) and keeps the queries whose dbt query comment (`/* {"app": "dbt", "node_id": …} */`, on by default) names the model and the prod target.
It shows the latest run's duration, bytes read and rows, with its date and warehouse, labelled as a past run, and no dollar figure.
A project that turns dbt's query comment off gets no history, and xf says why.

**The TUI hides warehouse features for Databricks** (dry runs, columns, cost) with a one-line note.
Browsing, lineage, compiles and runs work as they do; the TUI is in maintenance mode and gets no Databricks work.

**Not now:** defer to prod (it needs the tables a query reads, and on Databricks would be better built on dbt's own `--defer --state`), Spark SQL functions in scratch completion (its own catalog, source and licence, as `bqfuncs`), and the queries of a run in the runs view (dbt's output only, until cost history can link them).

**Testing.**
`internal/databricks` is tested against `httptest`, as `internal/bigquery` is.
Live tests run only when `DATABRICKS_HOST` is set, against a Free Edition workspace and its `samples` catalog; CI has no workspace token.
`examples/dbt-databricks` is a project for xf web in the preview, outside the Dataform parity test.

## Why

- **A seam before a second warehouse.**
  Branching on the warehouse at each call site would spread the check through web, impact, the language server and cost; moving every caller first, with BigQuery alone behind it, keeps the refactor's diff free of new behaviour and Databricks' diff free of moves.
- **Optional capabilities, not one wide interface.**
  Half of what BigQuery does has no Databricks counterpart; type assertions keep "can this warehouse do X" a compile-time shape the UI checks once, not an `ErrUnsupported` every caller handles.
- **Types as the warehouse spells them.**
  A mapping loses `DECIMAL(p,s)` and `MAP`, and a user reads `INT64` on a Databricks column as a bug.
- **Never wake a billed warehouse on a save.**
  On-save checks would keep a warehouse up all day; a check, a scratch query, impact and check are things the user asked for.
- **The profile already says where.**
  xf reads `profiles.yml` for ADR 0037's target picker; its plain `host` and `http_path` lines name the warehouse without rendering Jinja.
  Auth goes through the Databricks CLI because it already holds OAuth tokens and refreshes them.
- **A past run is the honest Databricks cost.**
  An estimate from `EXPLAIN COST` is often missing or far off for Delta tables, and a dollar figure per query would be an allocation guess that ignores idle time and concurrency.

## Considered options

- **A narrow interface grown phase by phase:** less up front, but BigQuery and the interface would live side by side for months.
- **One interface with every method,** Databricks returning `ErrUnsupported`: fewer types, failures at run time.
- **`bigquery.Field` as the common type,** Databricks types mapped onto it: least churn, lossy and confusing.
- **A closed enum of canonical types:** strongest comparisons, most work, and an escape hatch for what it can't name anyway.
- **The refactor in steps per package, or interleaved with Databricks:** smaller reviews, but either a long half-moved state or refactor and behaviour in one diff.
- **Only the manifest, or only the profile, deciding the warehouse:** the first is late on a staged open; the second is wrong when `type:` is Jinja.
- **`dbt debug` for the connection:** one source of truth, but a dbt start-up per launch and output that isn't an API.
- **Rendering `profiles.yml`:** the Jinja, `env_var` and `DBT_PROFILES_DIR` handling ADR 0002 avoided.
- **Asking for the warehouse every time, or picking the only one:** asks for what the profile says, or guesses in a workspace with two.
- **xf's own OAuth login, or tokens only:** a login flow and token store to maintain, or no workspace that turns tokens off.
- **Checks on save, always or while the warehouse runs:** BigQuery's feel, but a billed warehouse kept awake.
- **Asking before starting a stopped warehouse, or refusing to:** safer for classic warehouses that take minutes, a click every morning.
- **The tables a query reads from `EXPLAIN`'s plan, or from the model's refs:** parsing a text format with no stable shape, or missing raw names and macros.
- **Columns only from a check, or only from Unity Catalog:** a warehouse wake to see a built table, or edits never shown before a run.
- **Impact on Databricks behind a flag, or from Unity Catalog alone:** free by default, but it can't say what a changed model drops.
- **Cost not offered, or `EXPLAIN COST` bytes:** no number, or a misleading one.
- **History from `system.query.history`, or by matching table names in query text:** a warehouse and system-schema grants, or fuzzy across incremental strategies and hand-run queries.
- **The median of recent runs, or a dollar estimate from DBU rates:** steadier but harder to explain, or a guess.
- **Live tests in CI:** strongest, but a workspace token in a public repository's CI and billed runs.

## Consequences

- This amends ADR 0002's "BigQuery is still the only warehouse": it is for Dataform.
- ADR 0002's dbt dry runs on save stay BigQuery's; Databricks checks are on demand.
- `project.Graph` gains the warehouse; if what `xf serve` sends changes shape for it, its protocol version goes up.
- A dbt project that disables the default query comment gets no cost on Databricks.
- Every caller of `internal/bigquery` changes in one diff, though nothing it does changes.
