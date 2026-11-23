---
status: accepted
---

# xf impact finds external and BI SELECT-only readers from warehouse query history

Table-to-table lineage (Dataplex on BigQuery, `OBJECTS_MODIFIED` in Snowflake `ACCESS_HISTORY`, and table links in Databricks Unity Catalog) only records jobs that materialize another table or view. It misses `SELECT`-only consumers—Looker, Tableau, Looker Studio, Power BI, Metabase, Sigma, Hex, Omni, Lightdash, Hightouch, Census, scheduled queries, and ad-hoc analysts—so `xf impact` could previously report "Nothing to fix" on a leaf/mart table right before a pull request broke production dashboards.

## Decision

**Warehouse-native `SELECT`-only consumer discovery, with zero SaaS or BI connectors.**
`internal/lineage/consumers.go` adds a `Consumer` struct (`Tool`, `Asset`, `User`, `Queries`, `Days`, `LastSeen`) attached to `lineage.ColumnLink` and `impact.Reader`, plus a shared BI/external consumer parser (`ParseConsumer`) that extracts tool and asset metadata from SQL comments, query tags, job labels, client applications, and word-bounded service-account/role tokens while filtering out `xf`'s own queries (`IsOwnXFQuery`) and transformation-tool queries such as dbt tests and Dataform runs (`IsTransformToolQuery`).

**Per-warehouse discovery with strict cost and latency guardrails:**
- **BigQuery (`internal/lineage/jobreaders.go`)**: Opt-in via `queryHistoryProjects` (`auto`, `org`, or comma-separated GCP billing project IDs) so BigQuery stays 100% free by default. Runs one batched query per `(billingProject, region)` via `Source.Preload` concurrently with Dataplex `searchLinks`, bounded by `queryHistoryDays` (default 14, range 1–90), `queryHistoryMaxGB` (`maximumBytesBilled`, default 10 GiB; `0` for unlimited on slot reservations), per-table `QUALIFY ROW_NUMBER() <= 101`, `FARM_FINGERPRINT` full-query grouping, and a bounded 15-minute `JobHistoryCache` in the background engine.
- **Snowflake (`internal/snowflake/lineage.go`)**: Enabled by default whenever `queryHistoryDays > 0` (set `0` to disable), since `ACCESS_HISTORY` already executes on the virtual warehouse. Queries `ARRAY_SIZE(OBJECTS_MODIFIED) = 0` across both `DIRECT_OBJECTS_ACCESSED` and `BASE_OBJECTS_ACCESSED` (catching reads through secure views), pre-filtering matching `QUERY_ID`s in a CTE before joining `QUERY_HISTORY` for `QUERY_TAG`/`ROLE_NAME`/`QUERY_TEXT` and aggregating one row per consumer via `ARRAY_AGG(DISTINCT ...)`. Falls back to `ACCESS_HISTORY`-only on `QUERY_HISTORY` authorization errors.
- **Databricks (`internal/databricks/extreaders.go`)**: Enabled by default whenever `queryHistoryDays > 0` (set `0` to disable). Scans Databricks' REST Query History API (`/api/2.0/sql/history/queries`, waking no SQL warehouse, capped at 10 pages) concurrently via `Readers.Preload`, matching tables with exact multi-part identifiers (no bare-table leaf fallback) and classifying multi-table `JOIN` SQL matches as `TableOnly` (`Warning`).

**Severity and error isolation:**
- Confirmed column-level external reads (`DependencyType: Other`) and any external read of a deleted table produce `SeverityCritical`.
- Heuristic `SELECT *` (or ambiguous multi-table `JOIN`) external reads (`DependencyType: TableOnly`) on a dropped column produce `SeverityWarning` so a one-off `SELECT * LIMIT 10` does not falsely trip `--fail-on critical`.
- Optional query-history errors (missing `roles/bigquery.resourceViewer`, scan cap hit, row cap truncation, or `QUERY_HISTORY` denied) are surfaced as report-level `Report.Warnings` notes rather than per-column `ReadersError`, so they never downgrade clean columns to `SeverityUnknown`.

## Known limitations

- **BigQuery cache hits:** BigQuery does not populate `referenced_tables` on cache-hit jobs, so heavily cached dashboards may be undercounted.
- **Views in BigQuery / Databricks:** Queries against a view record the underlying base table in BigQuery `referenced_tables`; column-text matching does not resolve view column aliases.
- **Opaque BI IDs:** Tableau Query Tagging emits `workbook-luid` / `datasource-luid` UUIDs rather than human-readable dashboard titles; resolving titles requires an optional BI Metadata API integration later.
- **BigQuery clustered upper bounds:** `INFORMATION_SCHEMA.JOBS_BY_PROJECT` is a clustered metadata table whose dry-run upper bound can be much larger than actual billed bytes after cluster pruning; projects on slot reservations should set `queryHistoryMaxGB` to `0`.

## Consequences

- `queryHistoryProjects`, `queryHistoryDays`, and `queryHistoryMaxGB` are added to `internal/config`, CLI flags (`xf impact`, `xf check`), `engine.ImpactRequest`, `action.yml`, and `ci/gitlab/xf-check.yml`.
- `xf check --preflight` runs a free 0-row dry run against `INFORMATION_SCHEMA.JOBS` when `queryHistoryProjects` is configured, warning if `roles/bigquery.resourceViewer` is missing.
- `impact.Reader.Consumer` is emitted in `--json` output when present; `Reader.Table` holds the canonical `external:...` consumer ID, and Markdown/Text/Web/Notes render the sanitized `Consumer.Label()` and `Consumer.Note()`.
