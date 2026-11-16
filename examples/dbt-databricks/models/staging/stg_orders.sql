select
    o_orderkey as order_id,
    o_custkey as customer_id,
    o_orderstatus as status,
    o_totalprice as amount,
    o_orderdate as ordered_on
from {{ source('tpch', 'orders') }}
