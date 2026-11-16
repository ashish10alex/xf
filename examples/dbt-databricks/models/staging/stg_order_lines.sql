select
    l_orderkey as order_id,
    l_linenumber as line,
    l_quantity as quantity,
    l_extendedprice * (1 - l_discount) as net_amount
from {{ source('tpch', 'lineitem') }}
