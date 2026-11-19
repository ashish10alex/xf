# Examples

Small projects to try xf on, and the source of the compile fixtures in
`testdata/examples` (`just fixtures-examples`). They build in the
`alex-personal-dev-01` GCP project, in the EU; `data/load.sh <project>`
loads the raw tables they read (a few dozen rows, from `data/raw/*.csv`)
into another project's `raw` dataset. To use another project, change
`defaultProject` in the Dataform projects' `workflow_settings.yaml` and set
`XF_GCP_PROJECT` for the dbt ones.

| Project | What it shows |
|---|---|
| [`dataform`](dataform) | A Dataform project: declarations, views, a partitioned table, an incremental table, operations, assertions, JavaScript includes, generated actions and a property graph |
| [`dbt`](dbt) | The same project in dbt, under the same names, plus seeds, a snapshot, an ephemeral model, a materialized view, tests, an analysis and an exposure |
| [`dbt-hooks`](dbt-hooks) | A dbt project with `on-run-start` and `on-run-end` hooks, which dbt v2 runs when it compiles, so xf only parses it |
| [`dbt-databricks`](dbt-databricks) | A dbt project on Databricks, reading the `samples` catalog every workspace has: xf web checks its models on demand (ADR 0042). Outside the parity test; its `profiles.yml` reads the workspace and token from the environment |
| [`dataform-broken`](dataform-broken), [`dbt-broken`](dbt-broken) | One bad `ref` each, to see compile errors |

```bash
xf --dir examples/dataform
```

```bash
xf --dir examples/dbt
```
