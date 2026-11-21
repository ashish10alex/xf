# dbt on Snowflake example

A dbt-snowflake project for xf's Snowflake support (ADR 0044), outside the
Dataform parity test. It reads the TPC-H tables in `SNOWFLAKE_SAMPLE_DATA`
that every Snowflake account has, and builds into `<database>.xf_example`.

`profiles.yml` takes the account, user, warehouse, database and token from the
environment (its header lists them), so it holds nothing personal.

```bash
export SNOWFLAKE_ACCOUNT=orgname-accountname
export SNOWFLAKE_TOKEN=<programmatic-access-token>
export SNOWFLAKE_WAREHOUSE=COMPUTE_WH
xf web --dir examples/dbt-snowflake
```

In `xf web` a model's built table columns come from `DESCRIBE TABLE` in
Snowflake's Cloud Services layer without resuming a warehouse; `K` checks the
model's compiled SQL on the virtual warehouse (`SELECT * FROM (<query>) LIMIT 0`).
