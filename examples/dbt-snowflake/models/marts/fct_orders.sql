select
    order_date,
    order_status,
    count(*) as order_count,
    sum(total_price) as total_revenue
from {{ ref('stg_orders') }}
group by 1, 2
