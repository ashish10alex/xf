---
status: accepted
---

# Snowflake is a third warehouse, for dbt projects

BigQuery was xf's first warehouse and Databricks its second (ADR 0042), behind
`internal/warehouse`. A dbt project on `dbt-snowflake` already compiles,
browses, shows lineage and runs `dbt build` in xf, because dbt does that work;
everything that asks the warehouse (checks, columns, scratch queries, impact,
cost) now works on Snowflake too, in both `xf check` and `xf web`.

## Why

`dbt-snowflake` is the largest dbt adapter alongside BigQuery and Databricks.
Because ADR 0042 already split `internal/warehouse` and the on-demand check
workflow out of BigQuery assumptions, adding Snowflake requires only a thin
REST client (`internal/snowflake`) and dialect/UI wiring without introducing
any new Go module dependencies.

## Decision

**Plain REST via Snowflake SQL API v2, with no SDK.**
`internal/snowflake` talks plain HTTP/JSON to `/api/v2/statements`,
implementing `warehouse.Warehouse`, `warehouse.Waker` and
`warehouse.ProjectLister`. Metadata commands (`SHOW WAREHOUSES`, `SHOW
DATABASES`, `SHOW SCHEMAS`, `SHOW OBJECTS`, `DESCRIBE TABLE`) run in
Snowflake's Cloud Services layer and never resume a suspended virtual
warehouse; xf omits `warehouse` on them and restricts `executeMetadata` strictly
to `SHOW` and `DESCRIBE` (because Snowflake falls back to a user's
`DEFAULT_WAREHOUSE` for statements that need compute). Identifiers in `SHOW`
and `DESCRIBE` stay unquoted when plain and unreserved so dbt-snowflake's
lowercase manifest names fold to Snowflake's uppercase objects.

**Detection and connection.**
`metadata.adapter_type` (`snowflake`) on `project.Graph` decides, with the dbt
target's `type:` standing in before the first compile finishes. Account, user,
role, database, schema and virtual warehouse resolve from `snowflakeWarehouse` /
`snowflakeConnection` settings, `profiles.yml`, `connections.toml` (`~/.snowflake`
or `~/Library/Application Support/snowflake` on macOS, plus `[connections.<name>]`
and `default_connection_name` in `config.toml`), and `SNOWFLAKE_*` environment
variables. When the dbt target or `$SNOWFLAKE_ACCOUNT` names a specific account
with no matching section, xf never falls back to a default connection for a
different account.

**Authentication.**
Snowflake SQL API v2 accepts bearer tokens with
`X-Snowflake-Authorization-Token-Type`:
- Programmatic Access Tokens (`PROGRAMMATIC_ACCESS_TOKEN`): `$SNOWFLAKE_TOKEN`,
  `profiles.yml` `token` (or `password` with `authenticator:
  programmatic_access_token`), or `connections.toml`.
- Key-Pair JWT (`KEYPAIR_JWT`): RS256 JWT signed natively with Go's stdlib from
  an unencrypted RSA private key (`$SNOWFLAKE_PRIVATE_KEY[_PATH]`,
  `profiles.yml` `private_key[_path]`, or `connections.toml`), cached 50
  minutes.
- OAuth (`OAUTH`): `$SNOWFLAKE_OAUTH_TOKEN` or `authenticator: oauth` with
  `token`.

**On-demand checks (`K`, `xf impact`, `xf check`).**
Because a Snowflake virtual warehouse bills while it runs, xf never runs checks
on save or in the language server. `Check` runs `SELECT * FROM (<query>) LIMIT
0` on the chosen virtual warehouse, scanning zero rows and returning exact
Snowflake column types (`NUMBER(p,s)`, `VARCHAR(n)`, `TIMESTAMP_NTZ(s)`, …)
matching `DESCRIBE TABLE`. Built-table columns, hover cards and LSP cards come
from `DESCRIBE TABLE` and `SHOW OBJECTS` without resuming a warehouse, and link
to Snowsight for `org-account` identifiers (hiding the link for locator-style
accounts).

**Column impact (`xf impact` and `xf check`).**
`internal/snowflake/lineage.go` implements `impact.Readers` from
`SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY` (column-level lineage in
`OBJECTS_MODIFIED[*].columns[*].directSources` and table-level lineage, last 30
days) and `SNOWFLAKE.ACCOUNT_USAGE.OBJECT_DEPENDENCIES` (views, materialized
views and dynamic tables, available on all editions). Unlike Databricks' REST
Lineage API, querying `ACCOUNT_USAGE` views requires compute and executes on
the configured virtual warehouse (resuming it if suspended); impact reports
record this warehouse use whenever models are checked or lineage queries run.
If `ACCESS_HISTORY` fails and `OBJECT_DEPENDENCIES` returns no readers, impact
surfaces the error (marking columns ❔) rather than falsely reporting no
readers.

**Query cost (`xf cost` and `xf check`).**
Snowflake bills compute time rather than bytes scanned, so cost follows ADR
0042's past-run model (`cost.PastRuns`, `Source: "Snowflake Query History"`):
each changed model's latest finished build matching dbt's JSON query comment
for the prod target, read from `SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY` (30
days) with fallback to `INFORMATION_SCHEMA.QUERY_HISTORY` (7 days) when
`ACCOUNT_USAGE` is not granted. Unlike Databricks' REST Query History API,
these `SELECT` queries run on the virtual warehouse (resuming it if suspended);
the cost report notes the warehouse used, omits the `Rows read` column (since
Snowflake Query History reports `BYTES_SCANNED` and `ROWS_PRODUCED`, not rows
read), and `xf web` does not auto-refresh cost on Snowflake.

**`xf web` and standalone scratch.**
Snowflake shares Databricks' on-demand check flow, virtual warehouse picker
(`snowflakeWarehouse`), dependency inspector, and standalone scratch workspace
(`xf scratch --warehouse snowflake`, `scratch-snowflake`, connection picker
from `connections.toml`), using Snowflake SQL quoting (`"ident"`),
multi-statement scratch execution (`MULTI_STATEMENT_COUNT: "0"`), CodeMirror
Snowflake SQL highlighting, and `sqlfluff --dialect snowflake`.
`/api/scratch/functions?dialect=snowflake` currently returns `[]` (a Snowflake
function catalog can be added later as ADR 0042's amendment did for Spark SQL).
The TUI (in maintenance mode) hides warehouse features for Snowflake with a
status notice.

## Testing

- Unit tests in `internal/snowflake` (`httptest` mock of SQL API v2) covering
  auth (PAT, OAuth, RS256 key-pair JWT including `.global` accounts and
  encrypted-key hints), `connections.toml`/`config.toml` resolution without
  cross-account credential leaks, lowercase/reserved-word identifier handling in
  `SHOW`/`DESCRIBE`, `Check` type formatting and line-offset adjustment, async
  202 polling + cancel, multi-statement scripts and `ErrResultGone`, `ACCESS_HISTORY`
  + `OBJECT_DEPENDENCIES` lineage, and `QUERY_HISTORY` UTC timestamps, `since`
  windows, and `INFORMATION_SCHEMA` fallback.
- Integration tests in `cmd/xf/warehouse_test.go` (`TestSnowflakeImpactCostPreflight`,
  `TestCatalogTables`) and `internal/web/snowflake_test.go` (`TestSnowflakeWebSupport`,
  `TestStandaloneScratchSnowflake`), plus JS dialect tests in
  `internal/web/jstest/scratch-dialect.test.js`.
