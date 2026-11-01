{{ config(materialized='table') }}
{# a comment #}
select o.id, {{ dbt_utils.star(ref('orders')) }}
from {{ ref('orders') }} as o
{% if is_incremental() %} where ts > (select max(ts) from {{ this }}) {% endif %}
-- #not a jinja
