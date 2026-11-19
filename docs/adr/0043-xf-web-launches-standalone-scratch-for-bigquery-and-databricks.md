---
status: accepted
---

# xf web launches standalone scratch queries for BigQuery and Databricks

`xf scratch` outside a project previously opened a single BigQuery-only standalone scratch editor with no path to or from `xf web`'s project picker, and no support for Databricks outside a dbt project.
Meanwhile, `xf` now supports BigQuery and Databricks as warehouses (ADR 0042), and users often want to run ad-hoc queries against either warehouse directly from `xf web`.

## Decision

**Separate standalone scratch workspaces per warehouse.**
Standalone scratch runs as two distinct open workspaces — `scratch-bigquery` (keeping `scratch` as a backward-compatible alias) and `scratch-databricks` — each with its own folder, metadata cache, SQL dialect, and `runs.json`:
- BigQuery keeps `~/.config/xf/tmp/` so existing standalone scratches and recent runs are preserved.
- Databricks uses `~/.config/xf/tmp/databricks/`.
Both can stay open simultaneously in separate tabs/windows and appear in open-project summaries.

**Entry points and lifecycle.**
- Both the `xf web` project picker landing page and the `⇄ projects` overlay expose a **Scratch** button (`ctrl+s`) opening a BigQuery / Databricks chooser (supporting same-tab and `shift+enter` new-tab opening).
- Standalone scratch exposes a clickable top-bar warehouse switcher (`BigQuery ▾` / `Databricks ▾`), `⇄ projects`, and `ctrl+q` closing back to the previously shown project or the picker (asking to confirm and cancelling if a scratch query is currently running).
- `xf scratch` gains `--warehouse bigquery|databricks`. Inside a project, bare `xf scratch` opens the project's scratch while explicit `--warehouse` opens standalone scratch (disallowed with `--dir`). Outside a project, bare `xf scratch` opens `xf web` directly onto the warehouse chooser, while warehouse-specific flags (`--project`/`--location` or `--databricks-*`) infer the warehouse.

**Standalone Databricks connection state is isolated from `config.json`.**
Outside a dbt project there is no `profiles.yml` or `.xf/config.json`.
Writing `databricksProfile` or `databricksWarehouse` to `~/.config/xf/config.json` would override `profiles.yml` for every dbt Databricks project (since user config takes precedence over `profiles.yml` in `databricks.Resolve`).
Instead, standalone Databricks scratch resolves via CLI flags → `~/.config/xf/tmp/databricks/connection.json` → user config → `~/.databrickscfg` (`DEFAULT`, `$DATABRICKS_HOST` match, or a single unambiguous profile) and persists UI profile and per-profile SQL warehouse picks in `connection.json`.
An in-UI `~/.databrickscfg` profile picker opens automatically when no workspace resolves (e.g. multiple profiles with no `DEFAULT`), and recent runs are scoped by workspace host so switching profiles never fetches one workspace's statement ID from another.

**Native REST API project and catalog completion.**
`warehouse.ProjectLister` lists BigQuery projects (`GET /bigquery/v2/projects`) and Databricks Unity Catalog catalogs (`GET /api/2.1/unity-catalog/catalogs`, waking no SQL warehouse), served cached at `GET /api/scratch/meta/projects` for SQL `FROM`/`JOIN` completion and the toolbar's project/catalog input.

## Why

- **Per-warehouse workspaces** fit `xf web`'s `projectState` model without mixing dialects, metadata caches, or incompatible `runs.json` job references, and let users keep BigQuery and Databricks scratches open side by side.
- **Isolated `connection.json`** keeps standalone Databricks picks per-profile and prevents silently breaking dbt projects that rely on `profiles.yml`.
- **REST API project/catalog listing** avoids spawning `gcloud` or `databricks` CLIs during interactive completion while reusing existing auth tokens.
