select
    c_custkey as customer_id,
    c_name as name,
    c_mktsegment as segment,
    c_nationkey as nation_id
from {{ source('tpch', 'customer') }}
