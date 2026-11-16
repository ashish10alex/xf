select
    o.order_id,
    o.customer_id,
    o.ordered_on,
    o.status,
    count(l.line) as lines,
    sum(l.net_amount) as net_amount
from {{ ref('stg_orders') }} as o
left join {{ ref('stg_order_lines') }} as l on l.order_id = o.order_id
group by all
