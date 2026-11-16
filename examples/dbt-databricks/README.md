# dbt on Databricks example

A dbt-databricks project for xf web's Databricks support (ADR 0042), outside
the Dataform parity test. It reads the TPC-H tables of the `samples` catalog
that every workspace has, Free Edition included, and builds into
`<catalog>.xf_example`.

`profiles.yml` takes the workspace, SQL warehouse and token from the
environment (its header lists them), so it holds nothing personal.

```bash
export DATABRICKS_HOST=dbc-1234abcd-5678.cloud.databricks.com
xf web --dir examples/dbt-databricks
```

In xf web a model's columns show the built table's, from Unity Catalog,
without waking a warehouse; `K` checks the model's SQL on the SQL warehouse
(`DESCRIBE QUERY`), starting it when it is stopped.
