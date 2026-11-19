# Dataform example

A small e-commerce project that exercises what xf shows for Dataform. Its
twin in `../dbt` models the same sources, staging views and marts under the
same names, so the two compiled graphs can be compared.

```bash
dataform compile        # from this directory
xf                      # or: xf --dir examples/dataform
```

It builds in `alex-personal-dev-01` (`defaultProject` in
`workflow_settings.yaml`); `../data/load.sh` loads the raw tables it reads.

Shared with the dbt example: `raw.customers|orders|payments` (declarations),
`stg_customers|stg_orders|stg_payments` (views), `dim_customers` (table,
partitioned and clustered), `fct_orders` (incremental, merged on
`order_id`), the `audit_log` operation and the checks on them.

Dataform only: JavaScript includes, actions generated with `publish()`,
JavaScript declarations, a disabled action, a manual assertion, project
`vars`, a table that sets its own dataset and name, and a BigQuery
[property graph](https://github.com/dataform-co/dataform/blob/main/docs/property_graphs.md)
(`definitions/graphs/graph.yaml`, `commerce_graph`) over the marts, which
`reporting.top_customers_graph` queries with `GRAPH_TABLE`. Property graphs
need `@dataform/core` 3.1.4 or later, hence `dataformCoreVersion: 3.1.5`.
