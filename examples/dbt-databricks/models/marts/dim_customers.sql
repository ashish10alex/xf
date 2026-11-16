select
    c.customer_id,
    c.name,
    c.segment,
    count(o.order_id) as orders,
    max(o.ordered_on) as last_ordered_on,
    struct(min(o.ordered_on) as first, max(o.ordered_on) as last) as order_span
from {{ ref('stg_customers') }} as c
left join {{ ref('fct_orders') }} as o on o.customer_id = c.customer_id
group by all
