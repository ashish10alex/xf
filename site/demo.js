// The xf web demo: xf web's own panes (internal/web/assets: view.js,
// tree-view.js, search-view.js, scm-view.js, target-view.js,
// source-smart.js) drawn in plain JavaScript from a small made-up
// Dataform project, the one the impact and cost examples below talk
// about. Nothing here asks a server: the project, its compile, its dry
// runs and its runs are all in this file.
(function () {
  'use strict';

  var root = document.getElementById('xfd');
  if (!root) return;
  var live = document.getElementById('live');
  var reduced = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // ---- the project ----

  var DB = 'analytics-prod';
  var BRANCH = 'feat/orders-v2';
  var CORE = '3.0.71';

  // FILES are the project's files by path: what the source pane shows.
  var FILES = {};
  // A are the actions by name, in the order the files list them.
  var A = {};
  var ORDER = [];

  function cols(spec) {
    return spec.split('|').map(function (c) {
      var m = c.trim().match(/^(\S+) (\S+)(?: (.*))?$/);
      return { name: m[1], type: m[2], desc: m[3] || '' };
    });
  }

  function add(a) {
    A[a.name] = a;
    ORDER.push(a.name);
    var f = FILES[a.file];
    if (f) f.actions.push(a.name);
    return a;
  }

  function file(path, text, extra) {
    var lang = /\.sqlx$/.test(path) ? 'sqlx' : /\.js$/.test(path) ? 'js' : 'yaml';
    FILES[path] = { path: path, text: text.replace(/^\n/, ''), lang: lang, actions: [], git: (extra && extra.git) || '' };
  }

  // A model: its .sqlx, its action, and the assertions its config asks for.
  function model(name, dir, o, text) {
    var path = 'definitions/' + dir + '/' + name + '.sqlx';
    file(path, text, o);
    var src = FILES[path].text;
    var kind = (src.match(/type: "(\w+)"/) || [])[1];
    var schema = (src.match(/schema: "(\w+)"/) || [])[1] || 'marts';
    var deps = [];
    src.replace(/\$\{ref\("([^"]+)"(?:, "([^"]+)")?\)\}/g, function (_, a, b) {
      var n = b || a;
      if (deps.indexOf(n) < 0) deps.push(n);
    });
    var a = add({
      name: name, kind: kind, schema: schema, file: path, deps: deps,
      desc: (src.match(/description: "([^"]+)"/) || [])[1] || '',
      cols: cols(o.cols), dry: o.dry, updated: o.updated, partition: (src.match(/partitionBy: "(\w+)"/) || [])[1],
      rows: o.rows, size: o.size, dryError: o.dryError,
    });
    var as = src.match(/assertions: \{([\s\S]*?)\n  \}/);
    if (as) {
      var unique = as[1].match(/uniqueKey: \["(\w+)"\]/);
      var nonNull = as[1].match(/nonNull: \["(\w+)"\]/);
      // In the compile's order: the row conditions, then the unique key.
      if (nonNull) assertion(a, 'rowConditions', nonNull[1], o.asrtDry[1]);
      if (unique) assertion(a, 'uniqueKey_0', unique[1], o.asrtDry[0]);
    }
  }

  function assertion(parent, what, col, dry) {
    var pc = parent.cols.filter(function (c) { return c.name === col; })[0];
    add({
      name: parent.schema + '_' + parent.name + '_assertions_' + what, kind: 'assertion', schema: 'dataform_assertions',
      file: parent.file, deps: [parent.name], parent: parent.name, check: what, col: col, dry: dry,
      updated: ['N/A', 'dim'],
      cols: what === 'uniqueKey_0'
        ? [{ name: col, type: pc.type, desc: '' }, { name: 'index_row_count', type: 'INT64', desc: '' }]
        : [{ name: 'failing_row_condition', type: 'STRING', desc: '' }].concat(parent.cols),
    });
  }

  file('definitions/sources/raw.js', '\n' +
    '// The shop\'s tables, as its nightly export loads them.\n' +
    'const tables = ["customers", "orders", "payments", "order_lines"];\n' +
    '\n' +
    'tables.forEach((name) =>\n' +
    '  declare({\n' +
    '    database: "analytics-prod",\n' +
    '    schema: "raw",\n' +
    '    name,\n' +
    '  })\n' +
    ');\n');
  [
    ['customers', 'id INT64|email STRING|country STRING|created_at TIMESTAMP', '3,204,118', '402.11 MiB'],
    ['orders', 'id INT64|user_id INT64|created_at TIMESTAMP|status STRING|discount_code STRING', '48,211,904', '6.12 GiB'],
    ['payments', 'id INT64|order_id INT64|amount_cents INT64|method STRING|status STRING|created_at TIMESTAMP', '51,730,442', '4.37 GiB'],
    ['order_lines', 'order_id INT64|line_number INT64|product_id INT64|quantity INT64|unit_price NUMERIC|order_date DATE', '9,871,224,031', '1.39 TiB'],
  ].forEach(function (d) {
    add({
      name: d[0], kind: 'declaration', schema: 'raw', file: 'definitions/sources/raw.js', deps: [],
      cols: cols(d[1]), rows: d[2], size: d[3], updated: ['2026-11-12 05:31 · 4h ago', ''],
      desc: d[0] === 'order_lines' ? 'Partitioned by order_date.' : '',
    });
  });

  model('stg_customers', 'staging', {
    cols: 'customer_id INT64 Primary key.|email STRING Lower case.|country_code STRING ISO 3166-1 alpha-2.|created_at TIMESTAMP',
    dry: '38.12 MiB $0.000', updated: ['2026-09-18 10:42 · 8w ago', 'error'], rows: '3,201,377', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "staging",\n' +
    '  description: "Customers, one row each, from the shop\'s users."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  id AS customer_id,\n' +
    '  LOWER(email) AS email,\n' +
    '  UPPER(country) AS country_code,\n' +
    '  created_at\n' +
    'FROM ${ref("raw", "customers")}\n' +
    'WHERE email IS NOT NULL\n');

  model('stg_orders', 'staging', {
    cols: 'order_id INT64 Primary key.|customer_id INT64|order_date DATE|status STRING|discount_code STRING Null when none was used.',
    dry: '212.40 MiB $0.001', updated: ['2026-09-18 10:42 · 8w ago', 'error'], rows: '48,211,904', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "staging",\n' +
    '  description: "Orders, one row each, with the discount code used."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  id AS order_id,\n' +
    '  user_id AS customer_id,\n' +
    '  DATE(created_at) AS order_date,\n' +
    '  LOWER(status) AS status,\n' +
    '  NULLIF(discount_code, "") AS discount_code\n' +
    'FROM ${ref("raw", "orders")}\n');

  model('stg_payments', 'staging', {
    cols: 'payment_id INT64 Primary key.|order_id INT64|amount NUMERIC In dollars.|method STRING',
    dry: '96.75 MiB $0.001', updated: ['2026-09-18 10:42 · 8w ago', 'error'], rows: '50,904,310', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "staging",\n' +
    '  description: "Successful payments, in dollars."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  id AS payment_id,\n' +
    '  order_id,\n' +
    '  CAST(amount_cents AS NUMERIC) / 100 AS amount,\n' +
    '  method\n' +
    'FROM ${ref("raw", "payments")}\n' +
    'WHERE status = "success"\n');

  model('stg_order_lines', 'staging', {
    cols: 'order_id INT64|line_number INT64|product_id INT64|quantity INT64|unit_price NUMERIC In dollars.|order_date DATE The order\'s date: the partition of raw.order_lines.',
    dry: '1.39 TiB $8.672', updated: ['2026-09-18 10:42 · 8w ago', 'error'], rows: '9,871,224,031', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "staging",\n' +
    '  description: "Order lines: what each order bought."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  order_id,\n' +
    '  line_number,\n' +
    '  product_id,\n' +
    '  quantity,\n' +
    '  unit_price,\n' +
    '  order_date\n' +
    'FROM ${ref("raw", "order_lines")}\n');

  model('dim_customers', 'marts', {
    git: 'M',
    cols: 'customer_id INT64 Primary key.|email STRING|country_code STRING|customer_since DATE|first_order_date DATE Date of the customer\'s first order.|order_count INT64|lifetime_value NUMERIC Sum of successful payments, in dollars.',
    dry: '4.67 GiB $0.028', updated: ['2026-11-12 06:04 · 3h ago', ''], rows: '3,201,377', size: '284.30 MiB',
    asrtDry: ['≤ 48.20 MiB $0.000', '≤ 24.10 MiB $0.000'],
  }, '\n' +
    'config {\n' +
    '  type: "table",\n' +
    '  tags: ["marts", "daily"],\n' +
    '  description: "Customers with their order history.",\n' +
    '  bigquery: {\n' +
    '    partitionBy: "first_order_date",\n' +
    '    clusterBy: ["customer_id"]\n' +
    '  },\n' +
    '  columns: {\n' +
    '    customer_id: "Primary key.",\n' +
    '    first_order_date: "Date of the customer\'s first order.",\n' +
    '    lifetime_value: "Sum of successful payments, in dollars."\n' +
    '  },\n' +
    '  assertions: {\n' +
    '    uniqueKey: ["customer_id"],\n' +
    '    nonNull: ["customer_id"]\n' +
    '  }\n' +
    '}\n' +
    '\n' +
    'pre_operations {\n' +
    '  DECLARE run_started TIMESTAMP DEFAULT CURRENT_TIMESTAMP();\n' +
    '}\n' +
    '\n' +
    'WITH orders AS (\n' +
    '  SELECT\n' +
    '    o.customer_id,\n' +
    '    MIN(o.order_date) AS first_order_date,\n' +
    '    COUNT(*) AS order_count,\n' +
    '    SUM(p.amount) AS lifetime_value\n' +
    '  FROM ${ref("stg_orders")} AS o\n' +
    '  LEFT JOIN ${ref("stg_payments")} AS p USING (order_id)\n' +
    '  GROUP BY 1\n' +
    ')\n' +
    'SELECT\n' +
    '  c.customer_id,\n' +
    '  c.email,\n' +
    '  c.country_code,\n' +
    '  DATE(c.created_at) AS customer_since,\n' +
    '  o.first_order_date,\n' +
    '  COALESCE(o.order_count, 0) AS order_count,\n' +
    '  COALESCE(o.lifetime_value, 0) AS lifetime_value\n' +
    'FROM ${ref("stg_customers")} AS c\n' +
    'LEFT JOIN orders AS o USING (customer_id)\n');

  model('fct_orders', 'marts', {
    git: 'M',
    cols: 'order_id INT64 Primary key.|customer_id INT64|country_code STRING|order_date DATE|status STRING|order_total NUMERIC Paid, in dollars.|payment_count INT64',
    dry: { full: '214.08 GiB $1.307', incremental: '≤ 2.31 GiB $0.014' }, updated: ['2026-11-12 06:09 · 3h ago', ''],
    rows: '48,211,904', size: '3.86 GiB', asrtDry: ['≤ 1.86 GiB $0.011'],
  }, '\n' +
    'config {\n' +
    '  type: "incremental",\n' +
    '  description: "Orders, one row each, with what was paid.",\n' +
    '  uniqueKey: ["order_id"],\n' +
    '  bigquery: {\n' +
    '    partitionBy: "order_date"\n' +
    '  },\n' +
    '  assertions: {\n' +
    '    uniqueKey: ["order_id"]\n' +
    '  }\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  o.order_id,\n' +
    '  o.customer_id,\n' +
    '  c.country_code,\n' +
    '  o.order_date,\n' +
    '  o.status,\n' +
    '  SUM(p.amount) AS order_total,\n' +
    '  COUNT(p.payment_id) AS payment_count\n' +
    'FROM ${ref("stg_orders")} AS o\n' +
    'LEFT JOIN ${ref("stg_payments")} AS p USING (order_id)\n' +
    'LEFT JOIN ${ref("dim_customers")} AS c USING (customer_id)\n' +
    '${when(incremental(), `WHERE o.order_date >= (SELECT MAX(order_date) FROM ${self()})`)}\n' +
    'GROUP BY 1, 2, 3, 4, 5\n');

  model('fct_order_items', 'marts', {
    git: 'A',
    cols: 'order_id INT64|line_number INT64|product_id INT64|quantity INT64|line_total NUMERIC In dollars.|customer_id INT64|country_code STRING|order_date DATE',
    dry: '1.42 TiB $8.890', updated: ['N/A', 'dim'], rows: '', size: '',
  }, '\n' +
    'config {\n' +
    '  type: "table",\n' +
    '  description: "Order lines with their order\'s customer and country.",\n' +
    '  bigquery: {\n' +
    '    clusterBy: ["order_id"]\n' +
    '  }\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  l.order_id,\n' +
    '  l.line_number,\n' +
    '  l.product_id,\n' +
    '  l.quantity,\n' +
    '  l.quantity * l.unit_price AS line_total,\n' +
    '  o.customer_id,\n' +
    '  o.country_code,\n' +
    '  l.order_date\n' +
    'FROM ${ref("stg_order_lines")} AS l\n' +
    'JOIN ${ref("fct_orders")} AS o USING (order_id)\n');

  model('agg_daily_revenue', 'marts', {
    git: 'M',
    cols: 'order_date DATE|orders INT64|revenue NUMERIC In dollars.|net_revenue NUMERIC Revenue less refunds, in dollars.',
    dry: '38.50 GiB $0.235', updated: ['2026-11-12 06:12 · 3h ago', ''], rows: '2,861', size: '139.70 KiB',
  }, '\n' +
    'config {\n' +
    '  type: "table",\n' +
    '  description: "Orders and revenue per day.",\n' +
    '  bigquery: {\n' +
    '    partitionBy: "order_date"\n' +
    '  }\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  order_date,\n' +
    '  COUNT(*) AS orders,\n' +
    '  SUM(order_total) AS revenue,\n' +
    '  SUM(IF(status = "refunded", 0, order_total)) AS net_revenue\n' +
    'FROM ${ref("fct_orders")}\n' +
    'GROUP BY order_date\n');

  model('rpt_cohorts', 'reports', {
    cols: 'cohort_month DATE|customers INT64|avg_lifetime_value NUMERIC In dollars.',
    dry: '61.03 MiB $0.000', updated: ['2026-10-02 14:11 · 6w ago', 'error'], rows: '96', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "reports",\n' +
    '  description: "Customers and their lifetime value by the month they joined."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  DATE_TRUNC(customer_since, MONTH) AS cohort_month,\n' +
    '  COUNT(*) AS customers,\n' +
    '  AVG(lifetime_value) AS avg_lifetime_value\n' +
    'FROM ${ref("dim_customers")}\n' +
    'GROUP BY cohort_month\n');

  model('rpt_promotions', 'reports', {
    git: 'M',
    cols: 'discount_code STRING|orders INT64|revenue NUMERIC In dollars.',
    dryError: 'Unrecognized name: discount_code at [2:3]',
    updated: ['2026-10-02 14:11 · 6w ago', 'error'], rows: '412', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "reports",\n' +
    '  description: "Orders and revenue per discount code."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  discount_code,\n' +
    '  COUNT(*) AS orders,\n' +
    '  SUM(order_total) AS revenue\n' +
    'FROM ${ref("fct_orders")}\n' +
    'WHERE discount_code IS NOT NULL\n' +
    'GROUP BY discount_code\n');

  model('rpt_weekly_sales', 'reports', {
    git: 'M',
    cols: 'week DATE Monday of the week.|orders INT64|revenue NUMERIC Revenue less refunds, in dollars.',
    dry: '1.21 MiB $0.000', updated: ['2026-10-02 14:11 · 6w ago', 'error'], rows: '409', size: 'view',
  }, '\n' +
    'config {\n' +
    '  type: "view",\n' +
    '  schema: "reports",\n' +
    '  description: "Orders and revenue per week."\n' +
    '}\n' +
    '\n' +
    'SELECT\n' +
    '  DATE_TRUNC(order_date, WEEK(MONDAY)) AS week,\n' +
    '  SUM(orders) AS orders,\n' +
    '  SUM(net_revenue) AS revenue\n' +
    'FROM ${ref("agg_daily_revenue")}\n' +
    'GROUP BY week\n');

  file('includes/constants.js', '\n' +
    '// Values the models share.\n' +
    'const LOOKBACK_DAYS = 3;\n' +
    'const PAID = ["success", "settled"];\n' +
    '\n' +
    'module.exports = { LOOKBACK_DAYS, PAID };\n');

  file('workflow_settings.yaml', '\n' +
    'defaultProject: analytics-prod\n' +
    'defaultLocation: US\n' +
    'defaultDataset: marts\n' +
    'defaultAssertionDataset: dataform_assertions\n' +
    'dataformCoreVersion: ' + CORE + '\n');

  var KINDS = { table: 'tbl', view: 'view', incremental: 'inc', assertion: 'asrt', declaration: 'decl' };
  var down = {};
  ORDER.forEach(function (n) {
    A[n].deps.forEach(function (d) { (down[d] = down[d] || []).push(n); });
  });

  // ---- the compile ----

  var S = {
    file: 'definitions/marts/dim_customers.sqlx',
    sel: 'dim_customers',
    focus: 'actions',
    tab: 'sql',
    line: 1,
    view: 'files',
    dirs: { definitions: true, 'definitions/marts': true, 'definitions/staging': true, 'definitions/reports': true },
    treeCursor: 'definitions/marts/dim_customers.sqlx',
    query: '',
    col: { name: '', type: '', cursor: 0 },
    lin: 1,
    back: [],
    flags: { deps: false, dependents: false, full: false, api: false },
    target: { prefix: '', schema: '', database: '' },
    targetOpen: true,
    incremental: true,
    compiling: false,
    compiledAt: '09:41:07',
    took: '1.1',
    run: null,
    log: [],
    showLog: false,
    flash: '',
    mobile: 'panel',
  };

  // id is a's table, with the target's prefix and suffixes as a compile
  // with them names it; a declaration's is its own.
  function id(a) {
    if (a.kind === 'declaration') return DB + '.' + a.schema + '.' + a.name;
    var t = S.target;
    return DB + (t.database ? '_' + t.database : '') + '.' + a.schema + (t.schema ? '_' + t.schema : '') + '.' + (t.prefix ? t.prefix + '_' : '') + a.name;
  }

  // compiled is a's SQL as the compile gives it: refs as tables, the
  // pre_operations before the query, and an incremental table's
  // incremental or full variant.
  function compiled(a, incremental) {
    var t = '`' + id(a) + '`';
    if (a.kind === 'assertion') {
      var p = '`' + id(A[a.parent]) + '`';
      if (a.check === 'uniqueKey_0') {
        return 'SELECT\n  *\nFROM (\n  SELECT\n    ' + a.col + ',\n    COUNT(1) AS index_row_count\n  FROM ' + p +
          '\n  GROUP BY ' + a.col + '\n  ) AS data\nWHERE index_row_count > 1';
      }
      return 'SELECT\n  \'' + a.col + ' IS NOT NULL\' AS failing_row_condition,\n  *\nFROM ' + p + '\nWHERE NOT (' + a.col + ' IS NOT NULL)';
    }
    var src = FILES[a.file].text.replace(/^config \{[\s\S]*?\n\}\n/m, '');
    var pre = '';
    src = src.replace(/^pre_operations \{\n([\s\S]*?)\n\}\n/m, function (_, body) { pre = body; return ''; });
    src = src.replace(/^\$\{when\(incremental\(\), `([^`]*)`\)\}\n/m, function (_, body) { return incremental ? body + '\n' : ''; });
    var refs = function (s) {
      return s.replace(/\$\{self\(\)\}/g, t).replace(/\$\{ref\("([^"]+)"(?:, "([^"]+)")?\)\}/g, function (_, x, y) {
        return '`' + id(A[y || x]) + '`';
      });
    };
    src = refs(src).replace(/^\n+/, '').replace(/\n+$/, '');
    return pre ? '-- pre_operations\n' + refs(pre) + '\n\n-- query\n' + src : src;
  }

  // ---- highlighting ----

  var KW = toSet('select from where with as and or not in is null like between group by order having limit join left right inner outer full cross on using union all distinct case when then else end over partition qualify window declare default set if exists create table view insert into merge update delete values interval asc desc cast safe_cast extract');
  var TYPES = toSet('int64 string numeric bignumeric float64 bool boolean date datetime time timestamp bytes array struct json geography');
  var JSKW = toSet('const let var function return if else for of new module exports');
  function toSet(s) { var o = {}; s.split(' ').forEach(function (w) { o[w] = true; }); return o; }

  function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }

  // Tokens are [class, text]; lines() splits them at line breaks into
  // each line's HTML.
  function lines(tokens) {
    var out = [''];
    tokens.forEach(function (t) {
      var parts = t[1].split('\n');
      parts.forEach(function (p, i) {
        if (i) out.push('');
        if (!p) return;
        out[out.length - 1] += t[0] === 'raw' ? p : t[0] ? '<span class="' + t[0] + '">' + esc(p) + '</span>' : esc(p);
      });
    });
    return out;
  }

  // matchBrace is the index of the } closing the { at i in s.
  function matchBrace(s, i) {
    var depth = 0;
    for (var j = i; j < s.length; j++) {
      var c = s[j];
      if (c === '"' || c === "'" || c === '`') { j = s.indexOf(c, j + 1); if (j < 0) return s.length - 1; continue; }
      if (c === '{') depth++;
      else if (c === '}' && --depth === 0) return j;
    }
    return s.length - 1;
  }

  function sqlTokens(s, out) {
    var re = /(--[^\n]*)|('[^'\n]*'|"[^"\n]*")|(`[^`\n]*`)|(\$\{)|(\d+(?:\.\d+)?\b)|([A-Za-z_][\w]*)|(\s+)|([(),.;])|([^\sA-Za-z_\d])/g;
    var m;
    while ((m = re.exec(s))) {
      if (m[1]) out.push(['t-c', m[1]]);
      else if (m[2]) out.push(['t-s', m[2]]);
      else if (m[3]) out.push(['t-n', m[3]]);
      else if (m[4]) {
        var end = matchBrace(s, m.index + 1);
        island(s.slice(m.index + 2, end), out);
        re.lastIndex = end + 1;
      } else if (m[5]) out.push(['t-m', m[5]]);
      else if (m[6]) {
        var w = m[6].toLowerCase();
        var call = s[re.lastIndex] === '(';
        out.push([KW[w] && !(call && (w === 'if' || w === 'left' || w === 'right')) ? 't-k' : TYPES[w] ? 't-kt' : w === 'true' || w === 'false' ? 't-kc' : call ? 't-nb' : 't-n', m[6]]);
      } else if (m[7]) out.push(['', m[7]]);
      else if (m[8]) out.push(['t-p', m[8]]);
      else out.push(['t-o', m[9]]);
    }
  }

  // island is a ${ } in SQL: JavaScript, and a ref() in it a link to its
  // table, whose card a hover shows.
  function island(code, out) {
    var r = code.match(/^ref\(("[^"]+"(?:, "[^"]+")?)\)$/);
    out.push(['', '${']);
    if (r) {
      var names = r[1].match(/"([^"]+)"/g).map(function (q) { return q.slice(1, -1); });
      var name = names[names.length - 1];
      out.push(['t-nf', 'ref'], ['', '(']);
      out.push(['raw', '<span class="ref" tabindex="0" role="button" data-ref="' + esc(name) + '" aria-label="' + esc(names.join('.')) +
        ': show its table, enter goes to it">' + r[1].split(/(".*?")/).map(function (p) {
          return p ? (p[0] === '"' ? '<span class="t-s">' + esc(p) + '</span>' : esc(p)) : '';
        }).join('') + '</span>']);
      out.push(['', ')']);
    } else jsTokens(code, out);
    out.push(['', '}']);
  }

  function jsTokens(s, out) {
    var re = /(\/\/[^\n]*)|("[^"\n]*"|'[^'\n]*'|`[^`]*`)|(\d+(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)|(\s+)|([^\sA-Za-z_$\d])/g;
    var m;
    while ((m = re.exec(s))) {
      if (m[1]) out.push(['t-c', m[1]]);
      else if (m[2]) out.push(['t-s', m[2]]);
      else if (m[3]) out.push(['t-m', m[3]]);
      else if (m[4]) {
        var rest = s.slice(re.lastIndex);
        out.push([m[4] === 'true' || m[4] === 'false' || m[4] === 'null' ? 't-kc' : JSKW[m[4]] ? 't-k' : /^\s*:/.test(rest) ? 't-na' : /^\(/.test(rest) ? 't-nf' : '', m[4]]);
      } else out.push(['', m[5] || m[6]]);
    }
  }

  // sqlxTokens splits a .sqlx into its config { } (JavaScript), its
  // pre_operations { } (SQL) and its SQL.
  function sqlxTokens(s) {
    var out = [];
    var re = /^(config|pre_operations|post_operations|js) \{/gm;
    var at = 0, m;
    while ((m = re.exec(s))) {
      if (m.index < at) continue;
      sqlTokens(s.slice(at, m.index), out);
      var open = m.index + m[1].length + 1;
      var end = matchBrace(s, open);
      out.push(['t-kt', m[1]], ['', ' {']);
      if (m[1] === 'pre_operations' || m[1] === 'post_operations') sqlTokens(s.slice(open + 1, end), out);
      else jsTokens(s.slice(open + 1, end), out);
      out.push(['', '}']);
      at = end + 1;
      re.lastIndex = at;
    }
    sqlTokens(s.slice(at), out);
    return out;
  }

  function yamlTokens(s) {
    var out = [];
    s.split('\n').forEach(function (l, i) {
      if (i) out.push(['', '\n']);
      var m = l.match(/^(\s*)([\w.-]+)(:)(\s*)(.*)$/);
      if (!m) { out.push(['', l]); return; }
      out.push(['', m[1]], ['t-na', m[2]], ['', m[3] + m[4]], [/^[\d.]+$/.test(m[5]) ? 't-m' : 't-s', m[5]]);
    });
    return out;
  }

  function highlight(text, lang) {
    if (lang === 'sql') { var o = []; sqlTokens(text, o); return lines(o); }
    if (lang === 'js') { var j = []; jsTokens(text, j); return lines(j); }
    if (lang === 'yaml') return lines(yamlTokens(text));
    return lines(sqlxTokens(text));
  }

  // ---- what the panes show ----

  var sel = function () { return S.sel && A[S.sel]; };
  var fileOf = function (path) { return FILES[path]; };

  function details(a) {
    var d = [];
    if (a.kind === 'incremental') {
      d.push(['full', a.dry.full, S.incremental ? 'dim' : '']);
      d.push(['incremental', a.dry.incremental, S.incremental ? '' : 'dim']);
    } else if (a.dryError) d.push(['dry run', '✗ ' + a.dryError, 'error']);
    else if (a.dry) d.push(['dry run', a.dry, '']);
    if (a.updated) d.push(['updated', a.updated[0], a.updated[1]]);
    if (a.partition) d.push(['partition', a.partition, '']);
    return d;
  }

  function lineageRows(a) {
    var rows = [];
    var section = function (title, list) {
      rows.push({ header: title + ' (' + list.length + ')' });
      if (!list.length) rows.push({ none: true });
      list.forEach(function (n, i) { rows.push({ ref: A[n], last: i === list.length - 1 }); });
    };
    section('upstream', a.deps);
    section('downstream', down[a.name] || []);
    return rows;
  }

  function filteredCols(a) {
    var n = S.col.name.toLowerCase(), t = S.col.type.toLowerCase();
    return a.cols.filter(function (c) {
      return (!n || (c.name + ' ' + c.desc).toLowerCase().indexOf(n) >= 0) && (!t || c.type.toLowerCase().indexOf(t) >= 0);
    });
  }

  // runActions are what r runs: the action, with its upstream actions
  // and its downstream ones as d and D ask, in the order they build.
  function runActions(a) {
    var want = {};
    want[a.name] = true;
    var walk = function (n, next) {
      next(n).forEach(function (m) {
        if (!want[m] && A[m].kind !== 'declaration') { want[m] = true; walk(m, next); }
      });
    };
    if (S.flags.deps) walk(a.name, function (n) { return A[n].deps; });
    if (S.flags.dependents) walk(a.name, function (n) { return down[n] || []; });
    var done = {}, out = [];
    var visit = function (n) {
      if (done[n] || !want[n]) return;
      done[n] = true;
      A[n].deps.forEach(visit);
      out.push(n);
    };
    ORDER.forEach(visit);
    return out;
  }

  function runCommand(a) {
    var parts = ['dataform', 'run', '--actions', id(a)];
    if (S.flags.deps) parts.push('--include-deps');
    if (S.flags.dependents) parts.push('--include-dependents');
    if (S.flags.full) parts.push('--full-refresh');
    var t = S.target;
    if (t.prefix) parts.push('--table-prefix=' + t.prefix);
    if (t.schema) parts.push('--schema-suffix=' + t.schema);
    if (t.database) parts.push('--database-suffix=' + t.database);
    return parts.join(' ');
  }

  var actionCount = ORDER.length;
  var changed = Object.keys(FILES).filter(function (p) { return FILES[p].git; }).sort();
  var dryErrors = ORDER.filter(function (n) { return A[n].dryError; });

  // ---- drawing ----

  var ICON = function (body) {
    return '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' + body + '</svg>';
  };
  var ICONS = {
    files: ICON('<path d="M14 3H9a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V8z" /><path d="M14 3v5h5" /><path d="M4 8v11a2 2 0 0 0 2 2h8" />'),
    search: ICON('<circle cx="11" cy="11" r="6.5" /><path d="m20 20-4.3-4.3" />'),
    scm: ICON('<circle cx="6" cy="18" r="2.5" /><circle cx="6" cy="6" r="2.5" /><circle cx="18" cy="8" r="2.5" /><path d="M6 8.5v7" /><path d="M18 10.5c0 4-4 5-10 6" />'),
    projects: ICON('<rect x="3" y="8" width="13" height="13" rx="2" /><path d="M8 8V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-3" />'),
    term: ICON('<rect x="3" y="4" width="18" height="16" rx="2" /><path d="m7 10 3 2.5L7 15" /><path d="M12.5 15H17" />'),
    settings: ICON('<path d="M20 7h-9" /><path d="M14 17H4" /><circle cx="17" cy="17" r="3" /><circle cx="7" cy="7" r="3" />'),
  };

  root.innerHTML =
    '<div class="xfd-views" role="tablist" aria-label="Show">' +
      '<button type="button" role="tab" data-do="mobile" data-arg="files" data-fk="m:files">files</button>' +
      '<button type="button" role="tab" data-do="mobile" data-arg="source" data-fk="m:source">source</button>' +
      '<button type="button" role="tab" data-do="mobile" data-arg="panel" data-fk="m:panel">actions · sql</button>' +
    '</div>' +
    '<div class="xfd-app">' +
      '<div class="compile-bar" aria-hidden="true"></div>' +
      '<div class="main-row">' +
        '<nav class="rail" aria-label="Views" data-part="rail"></nav>' +
        '<section class="pane tree-pane" data-pane="tree" data-part="tree"></section>' +
        '<div style="flex:1 1 0;min-width:0;min-height:0;display:flex;flex-direction:column">' +
          '<div class="workspace" data-part="workspace">' +
            '<section class="pane source-pane" data-pane="source" aria-label="Source" data-part="source"></section>' +
            '<section class="pane right-pane" data-pane="right" aria-label="The file\'s actions">' +
              '<div class="right-col">' +
                '<section class="pane actions-pane" data-pane="actions" data-part="actions"></section>' +
                '<section class="pane target-pane" data-pane="target" data-part="target"></section>' +
                '<section class="pane run-pane" data-pane="run" data-part="run"></section>' +
                '<section class="pane sql-pane" data-pane="sql" data-part="sql"></section>' +
              '</div>' +
            '</section>' +
          '</div>' +
          '<section class="pane log-pane" data-pane="log" data-part="log" hidden></section>' +
        '</div>' +
      '</div>' +
      '<div class="status" data-part="status"></div>' +
      '<div class="footer" data-part="footer"></div>' +
    '</div>' +
    '<div class="sc-card" role="tooltip" id="xfd-card" hidden></div>';

  var part = {};
  Array.prototype.forEach.call(root.querySelectorAll('[data-part]'), function (el) { part[el.getAttribute('data-part')] = el; });
  var bar = root.querySelector('.compile-bar');
  var card = document.getElementById('xfd-card');

  function title(inner) { return '<header class="pane-title">' + inner + '</header>'; }

  function drawRail() {
    var btn = function (name, label, on, count, extra) {
      return '<button type="button" class="rail-btn' + (on ? ' on' : '') + (extra || '') + '" title="' + label + '" aria-label="' + label + '"' +
        (name === 'term' || name === 'settings' ? '' : ' aria-pressed="' + on + '"') +
        ' data-do="rail" data-arg="' + name + '" data-fk="rail:' + name + '">' + ICONS[name] + (count || '') + '</button>';
    };
    part.rail.innerHTML =
      btn('files', 'Files (⌘b)', S.view === 'files') +
      btn('search', 'Search (⇧⌘f)', S.view === 'search') +
      btn('scm', 'Source Control (⌃⇧g)', S.view === 'scm', '<span class="rail-count" aria-hidden="true">' + changed.length + '</span>') +
      btn('projects', 'Projects', S.view === 'projects') +
      btn('term', 'Terminal (⌃`)', false) +
      btn('settings', 'Settings (ctrl+,)', false, '', ' rail-end');
  }

  // treeRows are the file tree's rows: folders first, open ones with
  // their files under them.
  function treeRows() {
    var paths = Object.keys(FILES).sort();
    var rows = [];
    var walk = function (dir, depth) {
      var dirs = {}, files = [];
      paths.forEach(function (p) {
        if (dir && p.indexOf(dir + '/') !== 0) return;
        var rest = dir ? p.slice(dir.length + 1) : p;
        var i = rest.indexOf('/');
        if (i >= 0) dirs[rest.slice(0, i)] = true;
        else files.push(p);
      });
      Object.keys(dirs).sort().forEach(function (d) {
        var full = dir ? dir + '/' + d : d;
        rows.push({ path: full, name: d, dir: true, depth: depth, open: !!S.dirs[full] });
        if (S.dirs[full]) walk(full, depth + 1);
      });
      files.forEach(function (p) {
        var f = FILES[p];
        var errs = f.actions.filter(function (n) { return A[n].dryError; }).length;
        rows.push({ path: p, name: p.slice(p.lastIndexOf('/') + 1), depth: depth, git: f.git, errors: errs, actions: f.actions.length });
      });
    };
    walk('', 0);
    return rows;
  }

  function rowAttrs(key, cur) { return ' data-fk="' + esc(key) + '" tabindex="' + (cur ? 0 : -1) + '"'; }

  var lastView = null;
  function drawTree() {
    var focused = S.focus === 'tree';
    part.tree.classList.toggle('focused', focused);
    part.tree.hidden = !S.view;
    if (!S.view) { lastView = null; return; }
    var name = { files: 'files', scm: 'source control', search: 'search', projects: 'projects' }[S.view];
    var count = S.view === 'scm' ? '<span class="col-count">' + changed.length + '</span>' : '';
    var html = '';
    if (S.view !== lastView) {
      lastView = S.view;
      var note = { files: '⌘b hides', search: '⇧⌘f searches', scm: '⌃⇧g hides', projects: '⌘b hides' }[S.view];
      part.tree.setAttribute('aria-label', name);
      part.tree.innerHTML = title('<span class="pane-name" data-t>' + name + count + '</span>') +
        '<div class="pane-body">' +
        (S.view === 'search' ? '<div class="ps-boxes"><div class="ps-field"><input type="search" data-in="query" data-fk="in:query" aria-label="Search the project\'s files" placeholder="search" spellcheck="false" autocomplete="off" value="' + esc(S.query) + '"></div></div><div class="ps-summary" data-sum></div>' : '') +
        (S.view === 'scm' ? '<div class="scm-branch"><span class="scm-branch-name">⎇ ' + BRANCH + '</span><span class="scm-ahead" title="2 commits not pushed">↑2</span></div>' : '') +
        '<div class="tree-list" role="list" data-list></div></div>' +
        '<footer class="pane-note">' + note + '</footer>';
    }
    var list = part.tree.querySelector('[data-list]');
    if (S.view === 'files') {
      html = treeRows().map(function (r) {
        var cur = r.path === S.treeCursor;
        var cls = 'tree-row' + (r.dir ? ' dir' : '') + (cur ? ' cur' : '') + (!r.dir && r.path === S.file ? ' active' : '') + (r.git ? ' git-' + r.git : '') + (r.errors ? ' has-errors' : '');
        var why = [r.path, r.git === 'M' ? 'changed' : r.git === 'A' ? 'added' : '', r.errors ? r.errors + ' error' : '', r.actions ? r.actions + ' action' + (r.actions === 1 ? '' : 's') : ''].filter(Boolean).join(' · ');
        return '<button type="button" role="listitem" class="' + cls + '" title="' + esc(why) + '" style="padding-left:' + (0.5 + r.depth * 1.5) + 'ch"' +
          (r.dir ? ' aria-expanded="' + r.open + '"' : '') + (!r.dir && r.path === S.file ? ' aria-current="true"' : '') +
          ' data-do="' + (r.dir ? 'dir' : 'open') + '" data-arg="' + esc(r.path) + '"' + rowAttrs('tree:' + r.path, cur) + '>' +
          '<span class="tree-caret" aria-hidden="true">' + (r.dir ? (r.open ? '▾' : '▸') : '') + '</span>' +
          '<span class="tree-name">' + esc(r.name) + (r.dir ? '/' : '') + '</span>' +
          (r.errors ? '<span class="tree-err">' + r.errors + '</span>' : '') +
          (r.git ? '<span class="tree-git">' + r.git + '</span>' : '') + '</button>';
      }).join('');
    } else if (S.view === 'search') {
      var q = S.query.trim().toLowerCase();
      var nFiles = 0, nHits = 0;
      if (q) {
        Object.keys(FILES).sort().forEach(function (p) {
          var hits = [];
          FILES[p].text.split('\n').forEach(function (l, i) {
            var at = l.toLowerCase().indexOf(q);
            if (at >= 0) hits.push({ line: i + 1, before: l.slice(0, at).replace(/^\s+/, ''), hit: l.slice(at, at + q.length), after: l.slice(at + q.length) });
          });
          if (!hits.length) return;
          nFiles++; nHits += hits.length;
          var slash = p.lastIndexOf('/');
          html += '<button type="button" role="listitem" class="tree-row ps-file" title="' + esc(p) + '" data-do="open" data-arg="' + esc(p) + '"' + rowAttrs('ps:' + p, false) + '>' +
            '<span class="tree-caret" aria-hidden="true">▾</span><span class="tree-name">' + esc(p.slice(slash + 1)) + (slash > 0 ? '<span class="ps-dir">' + esc(p.slice(0, slash)) + '</span>' : '') + '</span>' +
            '<span class="ps-count">' + hits.length + '</span></button>';
          hits.forEach(function (h) {
            html += '<button type="button" role="listitem" class="tree-row ps-match" title="' + esc(p + ':' + h.line) + '" data-do="goto" data-arg="' + esc(p + ':' + h.line) + '"' + rowAttrs('ps:' + p + ':' + h.line, false) + '>' +
              '<span class="ps-text"><span class="dim">' + esc(h.before) + '</span><mark class="ps-hit">' + esc(h.hit) + '</mark><span class="dim">' + esc(h.after) + '</span></span>' +
              '<span class="ps-line-no">' + h.line + '</span></button>';
          });
        });
      }
      var first = list.parentNode.querySelector('[data-sum]');
      first.innerHTML = !q ? '<span class="dim">type to search the project\'s files</span>'
        : nHits ? '<span>' + nHits + ' result' + (nHits === 1 ? '' : 's') + ' in ' + nFiles + ' file' + (nFiles === 1 ? '' : 's') + '</span>'
          : '<span class="dim">no results in ' + Object.keys(FILES).length + ' files</span>';
      var firstRow = html.indexOf('tabindex="-1"');
      if (firstRow >= 0) html = html.slice(0, firstRow) + 'tabindex="0"' + html.slice(firstRow + 13);
    } else if (S.view === 'scm') {
      html = '<div class="scm-row section" role="listitem">changes <span class="scm-count">' + changed.length + '</span></div>' +
        changed.map(function (p, i) {
          var f = FILES[p], slash = p.lastIndexOf('/');
          return '<button type="button" role="listitem" class="scm-row file git-' + f.git + (p === S.file ? ' cur' : '') + '" style="padding-left:2ch" title="' + esc(p) + ' · ' + (f.git === 'M' ? 'changed' : 'added') + '" data-do="open" data-arg="' + esc(p) + '"' + rowAttrs('scm:' + p, i === 0) + '>' +
            '<span class="scm-name">' + esc(p.slice(slash + 1)) + '</span><span class="scm-dir">' + esc(p.slice(0, slash)) + '</span><span class="spacer"></span><span class="scm-git">' + f.git + '</span></button>';
        }).join('');
    } else {
      html = '<button type="button" role="listitem" class="tree-row proj-row here cur" title="~/repos/analytics · shown in this tab" data-do="proj"' + rowAttrs('proj:here', true) + '>' +
        '<span class="proj-lead">▸</span><span class="proj-name">analytics</span></button>' +
        '<button type="button" role="listitem" class="tree-row proj-row" data-do="proj-add"' + rowAttrs('proj:add', false) + '>' +
        '<span class="proj-lead">+</span><span class="proj-name">add another project…</span></button>';
    }
    list.innerHTML = html;
  }

  function drawSource() {
    var f = fileOf(S.file);
    var focused = S.focus === 'source';
    part.source.classList.toggle('focused', focused);
    var code = highlight(f.text.replace(/\n$/, ''), f.lang);
    var w = String(code.length).length;
    var scroll = part.source.querySelector('.code-scroll');
    var keep = scroll && part.source.getAttribute('data-file') === S.file ? scroll.scrollTop : 0;
    part.source.setAttribute('data-file', S.file);
    part.source.innerHTML = title('<span class="pane-name">' + esc(S.file) + '</span>') +
      '<div class="pane-body"><div class="code-scroll src-code" tabindex="0" data-fk="src" aria-label="' + esc(S.file) + ', line ' + S.line + '"><div class="code-lines">' +
      code.map(function (l, i) {
        return '<div class="code-line' + (i + 1 === S.line ? ' current' : '') + '" data-do="line" data-arg="' + (i + 1) + '"><span class="gutter" style="min-width:' + (w + 2) + 'ch">' + (i + 1) + '</span><span class="code">' + (l || ' ') + '</span></div>';
      }).join('') + '</div></div></div>' +
      '<footer class="pane-note">' + (focused ? 'hover a ref for its table · click goes to it' : 'the .sqlx, editable in xf web') + '</footer>';
    scroll = part.source.querySelector('.code-scroll');
    scroll.scrollTop = keep;
    if (S.revealLine) {
      var el = scroll.querySelectorAll('.code-line')[S.line - 1];
      if (el) scroll.scrollTop = Math.max(0, el.offsetTop - scroll.clientHeight / 3);
      S.revealLine = false;
    }
  }

  function plainNote() {
    return '<div class="pad plain-note"><span>' + esc(S.file) + '</span><span class="dim"> · no action in this file</span></div>';
  }

  function drawActions() {
    var f = fileOf(S.file);
    var focused = S.focus === 'actions';
    part.actions.classList.toggle('focused', focused);
    var list = f.actions;
    var body;
    if (!list.length) body = plainNote();
    else {
      var kindW = Math.max.apply(null, list.map(function (n) { return A[n].kind.length; })) + 1;
      body = '<div class="actions" role="listbox" aria-label="Actions in ' + esc(S.file) + '">' + list.map(function (n) {
        var a = A[n], on = n === S.sel;
        return '<div class="action' + (on ? ' sel' : '') + '" role="option" aria-selected="' + on + '" data-do="sel" data-arg="' + esc(n) + '"' + rowAttrs('act:' + n, on) + '>' +
          '<div class="action-row"><span class="marker" aria-hidden="true">' + (on ? '▸' : '') + '</span>' +
          '<span class="kind k-' + KINDS[a.kind] + '" style="min-width:' + kindW + 'ch">' + a.kind.toUpperCase() + '</span>' +
          '<span class="aname">' + esc(id(a)) + '</span></div>' +
          details(a).map(function (d) {
            return '<div class="detail"><span class="dlabel">' + d[0] + '</span><span class="dvalue ' + d[2] + '">' + esc(d[1]) + '</span></div>';
          }).join('') + '</div>';
      }).join('') + '</div>';
    }
    part.actions.innerHTML = title('<span class="pane-name">actions · ' + esc(S.file) + '</span>') + '<div class="pane-body">' + body + '</div>';
  }

  function drawTarget() {
    var t = S.target;
    var summary = [t.prefix && 'prefix ' + t.prefix + '_', t.schema && 'schema _' + t.schema, t.database && 'database _' + t.database].filter(Boolean).join(' · ') || 'as workflow_settings.yaml';
    if (!S.targetOpen) {
      part.target.className = 'pane target-pane tgt-closed';
      part.target.innerHTML = '<header class="pane-title"><button type="button" class="caret link" style="text-decoration:none" aria-expanded="false" data-do="tgt" data-fk="tgt">▸ target · ' + esc(summary) + '</button></header>';
      return;
    }
    part.target.className = 'pane target-pane';
    var field = function (key, label, tip) {
      var v = t[key];
      var us = v ? '<span class="tgt-us" aria-hidden="true">_</span>' : '';
      return '<label class="tgt-field' + (v ? ' set' : '') + '" title="' + tip + '"><span class="tgt-lab">' + label + '</span>' +
        (key !== 'prefix' ? us : '') + '<input data-in="tgt-' + key + '" data-fk="in:tgt-' + key + '" value="' + esc(v) + '" placeholder="' + (key === 'prefix' ? 'none' : 'no suffix') + '" spellcheck="false" autocomplete="off" aria-label="' + label + ': enter applies">' + (key === 'prefix' ? us : '') + '</label>';
    };
    part.target.innerHTML = '<header class="pane-title"><button type="button" class="caret link" style="text-decoration:none;color:inherit" aria-expanded="true" aria-label="target: hide" data-do="tgt" data-fk="tgt">▾</button><span class="pane-name">target</span></header>' +
      '<div class="pane-body tgt-body"><div class="tgt-row">' +
      field('prefix', 'prefix', '--table-prefix · namePrefix in workflow_settings.yaml') +
      field('schema', 'schema', '--schema-suffix · datasetSuffix in workflow_settings.yaml') +
      field('database', 'database', '--database-suffix · projectSuffix in workflow_settings.yaml') +
      '</div></div>';
  }

  function drawRun() {
    var a = sel();
    var f = S.flags;
    var focused = S.focus === 'run';
    part.run.className = 'pane run-pane' + (focused ? ' focused' : '') + (f.api ? ' api' : '');
    var toggle = function (flag, k, label) {
      var on = f[flag];
      return '<button type="button" class="toggle toggle-' + flag + (on ? ' on' : '') + '" aria-pressed="' + on + '" data-do="flag" data-arg="' + flag + '" data-fk="flag:' + flag + '">' +
        '<span class="box" aria-hidden="true">' + (!on ? '[ ]' : flag === 'full' ? '[!]' : '[✓]') + '</span> <kbd>' + k + '</kbd> ' + label + '</button>';
    };
    var tail = '';
    if (S.run && S.run.running) tail = '<span class="warn">running…</span>';
    else if (!a) tail = '<span>' + esc(S.file) + '</span><span class="dim"> · no action in this file</span>';
    else if (a.kind === 'declaration') tail = '<span class="dim">a declaration has nothing to run</span>';
    else {
      var verb = f.full ? (f.api ? '▸ full-refresh in GCP' : '▸ full-refresh') : (f.api ? '▸ run in GCP' : '▸ run');
      tail = '<button type="button" class="run-btn' + (f.full ? ' danger' : '') + '" title="' + esc(runCommand(a)) + '" data-do="run" data-fk="run"><kbd>r</kbd> <span class="verb ' + (f.full ? 'warn' : 'dim') + '">' + verb + '</span></button>';
    }
    part.run.innerHTML = title('<span class="pane-name">run' + (f.api ? '<span class="gcp"> · GCP</span>' : '') + (f.full ? '<span class="full-badge"> · ⚠ full-refresh</span>' : '') + '</span>') +
      '<div class="pane-body"><div class="run-line"><span class="toggles">' +
      toggle('deps', 'd', 'deps') + toggle('dependents', 'D', 'dependents') + toggle('full', 'f', 'full-refresh') + toggle('api', 'a', 'api') +
      '</span><span class="dim sep">·</span>' + tail + '</div>' +
      (f.api ? '<div class="dim command">runs the pushed branch: origin/' + BRANCH + '</div>' : '') +
      (a && a.kind !== 'declaration' ? '<div class="dim command run-cmd">' + esc(runCommand(a)) + '</div>' : '') + '</div>';
  }

  var sqlKey = null, colsKey = null;
  function drawSQL() {
    var a = sel();
    var focused = S.focus === 'sql' || S.focus === 'lineage';
    part.sql.classList.toggle('focused', focused);
    var counts = a ? ' ↑' + a.deps.length + ' ↓' + (down[a.name] || []).length : '';
    var tab = function (name, label) {
      var on = S.tab === name;
      return '<button type="button" role="tab" class="tab" id="xfd-tab-' + name + '" aria-controls="xfd-panel-' + name + '" aria-selected="' + on + '" tabindex="' + (on ? 0 : -1) + '" data-do="tab" data-arg="' + name + '" data-fk="tab:' + name + '">' + label + '</button>';
    };
    var variant = '';
    if (a && a.kind === 'incremental' && S.tab !== 'lineage') {
      var side = function (incr, label) {
        return '<button type="button" class="tab" aria-pressed="' + (S.incremental === incr) + '" aria-selected="' + (S.incremental === incr) + '" data-do="variant" data-arg="' + (incr ? 'incremental' : 'full') + '" data-fk="var:' + label + '">' + label + '</button>';
      };
      variant = '<span class="variant" title="SQL for an incremental run or a full rebuild (i)"><span class="dim"> · </span>' + side(true, 'incremental') + '<span class="dim"> / </span>' + side(false, 'full') + '</span>';
    }
    var head = part.sql.querySelector('.pane-title');
    var headHTML = '<span class="tabs" role="tablist" aria-label="The action\'s">' + tab('sql', 'sql') + '<span class="bar" aria-hidden="true">│</span>' + tab('columns', 'columns') + '<span class="bar" aria-hidden="true">│</span>' + tab('lineage', 'lineage' + counts) + '</span>' + variant;
    if (!head) {
      part.sql.innerHTML = '<header class="pane-title"></header><div class="pane-body">' +
        '<div class="tabpanel" role="tabpanel" id="xfd-panel-sql" aria-labelledby="xfd-tab-sql"></div>' +
        '<div class="tabpanel" role="tabpanel" id="xfd-panel-columns" aria-labelledby="xfd-tab-columns"></div>' +
        '<div class="tabpanel" role="tabpanel" id="xfd-panel-lineage" aria-labelledby="xfd-tab-lineage"></div>' +
        '</div><footer class="pane-note" hidden></footer>';
      head = part.sql.querySelector('.pane-title');
    }
    head.innerHTML = headHTML;
    var panels = { sql: part.sql.querySelector('#xfd-panel-sql'), columns: part.sql.querySelector('#xfd-panel-columns'), lineage: part.sql.querySelector('#xfd-panel-lineage') };
    Object.keys(panels).forEach(function (k) { panels[k].hidden = S.tab !== k; });
    var note = part.sql.querySelector('.pane-note');
    note.hidden = S.tab !== 'lineage' || !a;
    note.textContent = 'double-click jumps · ⌫ back';

    // sql
    var key = (a ? a.name : S.file) + '|' + S.incremental + '|' + JSON.stringify(S.target);
    if (key !== sqlKey) {
      sqlKey = key;
      if (!a) panels.sql.innerHTML = plainNote();
      else if (a.kind === 'declaration') panels.sql.innerHTML = '<div class="dim pad">a declaration has no SQL: its table is made outside the project</div>';
      else {
        var code = highlight(compiled(a, S.incremental), 'sql');
        var w = String(code.length).length + 1;
        panels.sql.innerHTML = '<div class="code-scroll sql-code" tabindex="0" aria-label="Compiled SQL of ' + esc(id(a)) + '"><div class="code-lines">' + code.map(function (l, i) {
          return '<div class="code-line' + (i === 0 ? ' current' : '') + '"><span class="gutter" style="min-width:' + (w + 1) + 'ch">' + (i + 1) + '</span><span class="code">' + (l || ' ') + '</span></div>';
        }).join('') + '</div></div>';
      }
    }

    // columns: the boxes stay while their action does, so typing keeps on.
    var ck = a ? a.name : '';
    if (ck !== colsKey) {
      colsKey = ck;
      panels.columns.innerHTML = !a ? plainNote() :
        '<div class="col-search-bar">' +
        '<div class="col-search-box"><span class="col-search-icon" aria-hidden="true">⌕</span><input class="col-search-input" data-in="col-name" data-fk="in:col-name" aria-label="Search columns by name or description" placeholder="Search columns by name or description… (/)" spellcheck="false" autocomplete="off" value="' + esc(S.col.name) + '"></div>' +
        '<div class="col-search-box type-box"><input class="col-search-input" data-in="col-type" data-fk="in:col-type" aria-label="Filter columns by type" placeholder="Filter type (e.g. INT64)…" spellcheck="false" autocomplete="off" value="' + esc(S.col.type) + '"></div>' +
        '<span class="dim col-total" data-total aria-live="polite"></span></div>' +
        '<div class="table-scroll"><table class="grid"><thead><tr><th scope="col">Column</th><th scope="col">Type</th><th scope="col">Mode</th><th scope="col">Description</th></tr></thead><tbody data-rows></tbody></table><div class="dim pad" data-none hidden>no column matches</div></div>';
    }
    if (a) {
      var fields = filteredCols(a);
      S.col.cursor = Math.min(S.col.cursor, Math.max(fields.length - 1, 0));
      panels.columns.querySelector('[data-total]').textContent = fields.length === a.cols.length ? a.cols.length + ' column' + (a.cols.length === 1 ? '' : 's') : fields.length + ' of ' + a.cols.length;
      panels.columns.querySelector('[data-none]').hidden = fields.length > 0;
      panels.columns.querySelector('[data-rows]').innerHTML = fields.map(function (c, i) {
        var on = i === S.col.cursor;
        return '<tr class="' + (on ? 'cur' : '') + '" data-do="col" data-arg="' + i + '"' + rowAttrs('col:' + c.name, i === S.col.cursor) + '><td>' + esc(c.name) + '</td><td class="dim">' + c.type + '</td><td class="dim"></td><td class="desc">' + esc(c.desc) + '</td></tr>';
      }).join('');
    }

    // lineage
    if (!a) panels.lineage.innerHTML = plainNote();
    else {
      var rows = lineageRows(a);
      if (!rows[S.lin] || !rows[S.lin].ref) S.lin = rows.findIndex(function (r) { return r.ref; });
      panels.lineage.innerHTML = '<div class="lineage" role="listbox" aria-label="Upstream and downstream of ' + esc(a.name) + '">' + rows.map(function (r, i) {
        if (r.header) return '<div class="lin-header dim" role="presentation">' + r.header + '</div>';
        if (r.none) return '<div class="dim lin-none" role="presentation">  none</div>';
        var x = r.ref, on = i === S.lin, full = id(x);
        return '<div class="lin-row' + (on ? ' cur' : '') + (x.kind === 'declaration' ? ' ext' : '') + '" role="option" aria-selected="' + on + '" title="' + esc(full) + '" data-do="lin" data-arg="' + i + '"' + rowAttrs('lin:' + x.name, on) + '>' +
          '<span class="dim" aria-hidden="true">' + (r.last ? '└' : '├') + '</span><span class="lin-kind kind-word k-' + KINDS[x.kind] + '">' + x.kind + '</span>' +
          '<span class="lin-name"><span class="dim">' + esc(full.slice(0, -x.name.length)) + '</span>' + esc(x.name) + '</span></div>';
      }).join('') + '</div>';
    }
  }

  function drawLog() {
    part.log.hidden = !S.showLog;
    if (!S.showLog) return;
    var r = S.run;
    part.log.classList.toggle('focused', S.focus === 'log');
    var scroll = part.log.querySelector('.log-scroll');
    var rest = r ? r.label + (r.running ? ' · ctrl+c cancels' : '') : '';
    part.log.innerHTML = title('<span class="pane-name">log' + (rest ? '<span class="dim"> · ' + esc(rest) + '</span>' : '') + '</span>') +
      '<div class="pane-body"><div class="log-scroll" tabindex="0" aria-label="Run log" aria-live="polite">' +
      S.log.map(function (l) { return '<div class="log-line ' + (l[0] || '') + '">' + esc(l[1]) + '</div>'; }).join('') + '</div></div>';
    scroll = part.log.querySelector('.log-scroll');
    scroll.scrollTop = scroll.scrollHeight;
  }

  // siteTheme is the site's theme, which the demo shows as xf's own.
  function siteTheme() {
    var t = document.documentElement.getAttribute('data-theme');
    if (t) return { name: t === 'light' ? 'xf Light' : 'xf Dark', auto: false };
    var light = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
    return { name: light ? 'xf Light' : 'xf Dark', auto: true };
  }

  var SPIN = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
  var spin = 0;
  function drawStatus() {
    var parts = [actionCount + ' actions', 'core ' + CORE, 'compiled ' + S.compiledAt + ' (' + S.took + 's)'];
    var state;
    if (S.compiling) state = '<span class="warn">' + (reduced ? '◌' : SPIN[spin % SPIN.length]) + ' compiling… (target)</span>';
    else {
      state = '<span class="ok">idle</span>';
      if (dryErrors.length) state += '<span class="dim"> · </span><button type="button" class="link error" title="' + esc(A[dryErrors[0]].name + ': ' + A[dryErrors[0]].dryError) + '" data-do="errs" data-fk="errs">✗ ' + dryErrors.length + ' dry run error' + (dryErrors.length === 1 ? '' : 's') + ' (e)</button>';
    }
    var r = S.run;
    var th = siteTheme();
    part.status.innerHTML = '<span class="badge">DATAFORM</span>' +
      '<span class="dim" title="~/repos/analytics">analytics</span><span class="dim"> · </span>' +
      '<span title="2 commits not pushed">⎇ ' + BRANCH + ' <span class="ok">↑2</span></span><span class="dim hide-narrow"> vs main</span><span class="dim"> · </span>' +
      '<span class="dim status-parts" title="' + parts.join(' · ') + '">' + parts.map(function (p) { return p + ' · '; }).join('') + '</span>' + state +
      (r && r.running ? '<span class="dim"> · </span><span class="warn">▶ running ' + esc(r.short) + (r.api ? ' in GCP' : '') + '</span>' : '') +
      '<span class="spacer"></span><span class="status-right"><span class="dim hide-narrow" title="Defer to prod is off: click in xf web to read upstream tables not built in dev from prod.">defer off</span>' +
      '<button type="button" class="link dim" title="Colour theme · here, it follows the site\'s" data-do="theme" data-fk="theme">◐ ' + (th.auto ? 'auto · ' : '') + th.name + '</button></span>';
  }

  // KEYS are the footer's keys for each pane, as xf web's Footer gives
  // them; those this demo answers can be tapped.
  var TAPS = { enter: 1, y: 1, Y: 1, d: 1, D: 1, f: 1, a: 1, r: 1, '/': 1, '⌫': 1, O: 1, tab: 1, j: 1, k: 1 };
  function footerKeys() {
    var a = sel();
    var t = a ? [['u', 'BigQuery'], ['y/Y', 'copy name/path']] : [];
    var list = [], find = [];
    if (S.focus === 'sql' && S.tab === 'columns') { list = [['y', 'copy name'], ['Y', 'name and type']]; find = [['/', 'filter']]; }
    else if (S.focus === 'source') { list = [['enter', 'edit'], ['E', 'hide source']]; find = [['/', 'search']]; }
    else if (S.focus === 'actions') list = [['enter', 'show its sql']].concat(t);
    else if (S.focus === 'lineage') list = t.concat([['⌫', 'back'], ['tab', 'next tab'], ['O', 'sql']]);
    else if (S.focus === 'run') list = [['R', 'run changed actions'], ['T', 'run tags'], ['d', 'deps'], ['D', 'dependents'], ['f', 'full refresh'], ['a', 'run via Dataform API']];
    else if (S.focus === 'sql') find = [['/', 'search']];
    var right = ['actions', 'lineage', 'run', 'target', 'sql'].indexOf(S.focus) >= 0;
    return [['⌘p', 'find']].concat(find, list, [['z', 'zoom'], ['r', 'run'], ['?', 'help']], right ? [['esc', 'projects']] : []);
  }

  function drawFooter() {
    part.footer.className = 'footer' + (S.flash ? ' warn' : '');
    if (S.flash) { part.footer.textContent = S.flash; return; }
    part.footer.innerHTML = '<span class="keys">' + footerKeys().map(function (kd) {
      return '<span class="key">' + kd[0].split(/(?!^)\/(?!$)/).map(function (k) {
        return TAPS[k] ? '<kbd class="tap" role="button" tabindex="-1" data-do="key" data-arg="' + esc(k) + '" title="press ' + esc(k) + '">' + esc(k) + '</kbd>' : '<kbd>' + esc(k) + '</kbd>';
      }).join('/') + ' ' + kd[1] + '</span>';
    }).join('') + '</span>';
  }

  function drawFrame() {
    ['tree', 'source', 'actions', 'run', 'log'].forEach(function (x) { part[x].classList.toggle('focused', S.focus === x); });
    part.sql.classList.toggle('focused', S.focus === 'sql' || S.focus === 'lineage');
    var right = ['actions', 'run', 'sql', 'lineage', 'target'].indexOf(S.focus) >= 0;
    root.querySelector('.right-pane').classList.toggle('right-focused', right);
    part.workspace.classList.toggle('no-tree', !S.view);
    bar.classList.toggle('on', S.compiling);
    root.setAttribute('data-view', S.mobile);
    Array.prototype.forEach.call(root.querySelectorAll('.xfd-views [role="tab"]'), function (b) {
      var on = b.getAttribute('data-arg') === S.mobile;
      b.setAttribute('aria-selected', on);
      b.tabIndex = on ? 0 : -1;
    });
  }

  // draw redraws the panes, keeping the keys where they were: on the
  // element with the same data-fk, or the one asked for.
  function draw(which) {
    var active = document.activeElement;
    var fk = S.wantFocus || (active && root.contains(active) && active.getAttribute('data-fk'));
    S.wantFocus = null;
    var all = !which;
    if (all || which.rail) drawRail();
    if (all || which.tree) drawTree();
    if (all || which.source) drawSource();
    if (all || which.actions) drawActions();
    if (all || which.target) drawTarget();
    if (all || which.run) drawRun();
    if (all || which.sql) drawSQL();
    if (all || which.log) drawLog();
    drawStatus();
    drawFooter();
    drawFrame();
    if (fk && (!active || !root.contains(active) || active.getAttribute('data-fk') !== fk || !active.isConnected)) {
      var el = root.querySelector('[data-fk="' + (window.CSS && CSS.escape ? CSS.escape(fk) : fk) + '"]');
      if (el) el.focus({ preventScroll: true });
    }
    if (fk) {
      var row = root.querySelector('[data-fk="' + (window.CSS && CSS.escape ? CSS.escape(fk) : fk) + '"]');
      if (row) nearest(row);
    }
  }

  // nearest scrolls el into view inside its pane, never the page.
  function nearest(el) {
    var box = el.parentElement;
    while (box && box !== root && !(box.scrollHeight > box.clientHeight && /auto|scroll/.test(getComputedStyle(box).overflowY))) box = box.parentElement;
    if (!box || box === root) return;
    var e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
    var head = box.querySelector('thead');
    var top = b.top + (head && box.contains(el) && el.tagName === 'TR' ? head.getBoundingClientRect().height : 0);
    if (e.top < top) box.scrollTop -= top - e.top;
    else if (e.bottom > b.bottom) box.scrollTop += e.bottom - b.bottom;
  }

  // ---- doing things ----

  function say(text) { if (live) live.textContent = text; }

  var flashT = null;
  function flash(text) {
    S.flash = text;
    say(text);
    clearTimeout(flashT);
    flashT = setTimeout(function () { S.flash = ''; drawFooter(); }, 2600);
    drawFooter();
  }

  function openFile(path, opts) {
    opts = opts || {};
    var f = FILES[path];
    if (!f) return;
    var changedFile = S.file !== path;
    S.file = path;
    S.treeCursor = path;
    var parts = path.split('/');
    for (var i = 1; i < parts.length; i++) S.dirs[parts.slice(0, i).join('/')] = true;
    if (opts.sel) S.sel = opts.sel;
    else if (changedFile || !f.actions.length) S.sel = f.actions[0] || null;
    if (changedFile) { S.col.cursor = 0; }
    S.line = opts.line || (changedFile ? 1 : S.line);
    S.revealLine = !!opts.line;
    if (changedFile) {
      var scroll = part.source.querySelector('.code-scroll');
      if (scroll) scroll.scrollTop = 0;
    }
    if (S.sel) say('opened ' + path + ' · ' + id(A[S.sel]));
    else say('opened ' + path + ': no action in this file');
  }

  function select(name) {
    if (!A[name]) return;
    if (A[name].file !== S.file) openFile(A[name].file, { sel: name });
    S.sel = name;
    S.col.cursor = 0;
    say('selected ' + id(A[name]));
  }

  // jump goes to the lineage row's action, as double-click and enter do;
  // ⌫ comes back.
  function jump(i) {
    var a = sel();
    var r = a && lineageRows(a)[i];
    if (!r || !r.ref) return;
    S.back.push(a.name);
    select(r.ref.name);
    var rows = lineageRows(A[S.sel]);
    S.lin = rows.findIndex(function (x) { return x.ref; });
    S.focus = 'lineage';
    S.wantFocus = 'lin:' + rows[S.lin].ref.name;
    draw();
  }

  function back() {
    var n = S.back.pop();
    if (!n) { flash('nothing to go back to'); return; }
    select(n);
    draw();
  }

  function copy(text, what) {
    var done = function () { flash('copied ' + what); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(done, function () { flash('copy: the browser said no'); });
    else flash('copy: the browser said no');
  }

  function compile() {
    S.compiling = true;
    S.flash = '';
    draw({});
    var started = Date.now();
    var tick = setInterval(function () { spin++; drawStatus(); }, 90);
    setTimeout(function () {
      clearInterval(tick);
      S.compiling = false;
      var now = new Date();
      S.compiledAt = now.toTimeString().slice(0, 8);
      S.took = ((Date.now() - started) / 1000 + 0.3).toFixed(1);
      sqlKey = null;
      draw({ actions: 1, sql: 1, run: 1, target: 1 });
      say('compiled: the names now end as the target says');
    }, reduced ? 150 : 650);
  }

  function startRun() {
    var a = sel();
    if (!a || a.kind === 'declaration') { flash(a ? 'a declaration has nothing to run' : 'no action in this file'); return; }
    if (S.run && S.run.running) { flash('a run is under way'); return; }
    var names = runActions(a);
    var f = S.flags;
    S.run = { running: true, short: a.name, api: f.api, label: (f.api ? 'run in GCP' : 'run') + ' · ' + a.name };
    S.showLog = true;
    S.log = f.api
      ? [['dim', 'invoking origin/' + BRANCH + ' in Dataform, ' + DB + ' (US)…']]
      : [['dim', '$ ' + runCommand(a)], ['', 'Compiling...'], ['', ''], ['', 'Compiled successfully.'], ['', ''], ['', 'Running...'], ['', '']];
    names.forEach(function (n) {
      var x = A[n];
      var what = x.kind === 'assertion' ? 'Assertion passed: ' : 'Table created: ';
      S.log.push({ pending: true, line: ['ok', what + ' ' + id(x) + ' [' + x.kind + ']'] });
    });
    say('running ' + names.length + ' action' + (names.length === 1 ? '' : 's'));
    var shown = S.log.filter(function (l) { return !l.pending; });
    var queue = S.log.filter(function (l) { return l.pending; }).map(function (l) { return l.line; });
    S.log = shown;
    draw();
    var step = function () {
      if (!queue.length) {
        S.run.running = false;
        var stamp = new Date().toTimeString().slice(0, 5);
        names.forEach(function (n) {
          if (A[n].kind !== 'assertion') A[n].updated = ['2026-11-12 ' + stamp + ' · just now', ''];
        });
        say('run finished: ' + names.length + ' action' + (names.length === 1 ? '' : 's'));
        draw({ actions: 1, run: 1, log: 1 });
        return;
      }
      S.log.push(queue.shift());
      draw({ log: 1, run: 1 });
      setTimeout(step, reduced ? 60 : 420);
    };
    setTimeout(step, reduced ? 60 : 700);
  }

  // setFocus moves the focus to pane p: its border and the footer's
  // keys change, nothing is drawn again, so the keys stay where they are.
  function setFocus(p) {
    if (p === 'right' || S.focus === p) return;
    S.focus = p;
    drawFooter();
    drawFrame();
  }

  // ---- the card of a ref ----

  function showCard(el) {
    var a = A[el.getAttribute('data-ref')];
    if (!a) return;
    var full = id(a);
    var meta = ['US'];
    if (a.rows) meta.push(a.rows + ' rows');
    if (a.size && a.size !== 'view') meta.push(a.size);
    if (a.updated && a.updated[0] !== 'N/A') meta.push(a.updated[0].split(' · ')[0]);
    if (a.kind !== 'declaration') meta.push(a.file);
    card.innerHTML = '<div class="sc-card-bar"><span class="sc-card-kind">' + a.kind + '</span><span class="sc-card-actions">click goes to it</span></div>' +
      '<div class="sc-card-nameline"><span class="sc-card-qual">' + esc(full.slice(0, -a.name.length)) + '</span><span class="sc-card-name">' + esc(a.name) + '</span></div>' +
      (a.desc ? '<div class="sc-card-desc">' + esc(a.desc) + '</div>' : '') +
      '<div class="sc-card-meta">' + esc(meta.join(' · ')) + '</div>' +
      '<div class="sc-card-table">' + a.cols.map(function (c) {
        return '<span class="sc-card-colname">' + esc(c.name) + '</span><span class="sc-card-coltype">' + c.type + '</span><span class="sc-card-coldesc">' + esc(c.desc) + '</span>';
      }).join('') + '</div>';
    card.hidden = false;
    el.setAttribute('aria-describedby', 'xfd-card');
    var r = el.getBoundingClientRect(), box = root.getBoundingClientRect();
    var cw = card.offsetWidth, ch = card.offsetHeight;
    var left = Math.min(Math.max(8, r.left - box.left), box.width - cw - 8);
    var top = r.top - box.top - ch - 6;
    if (top < 8) top = r.bottom - box.top + 6;
    card.style.left = left + 'px';
    card.style.top = top + 'px';
  }
  function hideCard() {
    card.hidden = true;
    var el = root.querySelector('[aria-describedby="xfd-card"]');
    if (el) el.removeAttribute('aria-describedby');
  }

  // ---- events ----

  root.addEventListener('mouseover', function (e) {
    var ref = e.target.closest && e.target.closest('.ref');
    if (ref) showCard(ref);
  });
  root.addEventListener('mouseout', function (e) {
    var ref = e.target.closest && e.target.closest('.ref');
    if (ref && !(e.relatedTarget && ref.contains(e.relatedTarget)) && document.activeElement !== ref) hideCard();
  });
  root.addEventListener('focusin', function (e) {
    var ref = e.target.closest('.ref');
    if (ref) showCard(ref); else hideCard();
    var pane = e.target.closest('[data-pane]');
    if (pane) {
      var p = pane.getAttribute('data-pane');
      if (p === 'sql' && S.tab === 'lineage') p = 'lineage';
      if (p !== 'right') setFocus(p);
    }
  });
  root.addEventListener('mousedown', function (e) {
    var pane = e.target.closest('[data-pane]');
    if (!pane) return;
    var p = pane.getAttribute('data-pane');
    if (p === 'sql' && S.tab === 'lineage') p = 'lineage';
    if (p === 'right') p = 'sql';
    setFocus(p);
  });

  root.addEventListener('click', function (e) {
    var t = e.target.closest('[data-do]');
    if (!t || !root.contains(t)) return;
    var arg = t.getAttribute('data-arg');
    switch (t.getAttribute('data-do')) {
      case 'rail':
        if (arg === 'term') { flash('the terminal: a shell in the log pane, in xf web itself'); return; }
        if (arg === 'settings') { flash('settings: a page of its own, in xf web itself'); return; }
        S.view = S.view === arg ? null : arg;
        if (S.view) { S.focus = 'tree'; S.mobile = 'files'; }
        S.wantFocus = S.view === 'search' ? 'in:query' : 'rail:' + arg;
        draw();
        return;
      case 'dir':
        S.dirs[arg] = !S.dirs[arg];
        S.treeCursor = arg;
        S.wantFocus = 'tree:' + arg;
        draw({ tree: 1 });
        return;
      case 'open':
        openFile(arg);
        S.focus = S.view === 'files' || S.view === 'scm' || S.view === 'search' ? 'tree' : S.focus;
        if (window.innerWidth <= 760) S.mobile = 'source';
        S.wantFocus = t.getAttribute('data-fk');
        draw();
        return;
      case 'goto': {
        var i = arg.lastIndexOf(':');
        openFile(arg.slice(0, i), { line: +arg.slice(i + 1) });
        if (window.innerWidth <= 760) S.mobile = 'source';
        S.wantFocus = t.getAttribute('data-fk');
        draw();
        return;
      }
      case 'proj': flash('analytics: the project shown'); return;
      case 'proj-add': flash('the projects switcher opens here, in xf web itself'); return;
      case 'line':
        S.line = +arg;
        setFocus('source');
        Array.prototype.forEach.call(part.source.querySelectorAll('.code-line'), function (l, j) { l.classList.toggle('current', j + 1 === S.line); });
        return;
      case 'sel':
        select(arg);
        S.focus = 'actions';
        S.wantFocus = 'act:' + arg;
        draw({ source: 1, actions: 1, run: 1, sql: 1, tree: 1 });
        return;
      case 'tgt':
        S.targetOpen = !S.targetOpen;
        S.wantFocus = 'tgt';
        draw({ target: 1 });
        return;
      case 'flag': toggleFlag(arg); return;
      case 'run': startRun(); return;
      case 'tab': pickTab(arg); return;
      case 'variant':
        S.incremental = arg === 'incremental';
        S.wantFocus = t.getAttribute('data-fk');
        draw({ actions: 1, sql: 1 });
        return;
      case 'lin':
        S.lin = +arg;
        S.focus = 'lineage';
        S.wantFocus = t.getAttribute('data-fk');
        if (e.detail >= 2) { jump(S.lin); return; }
        draw({ sql: 1 });
        return;
      case 'col':
        S.col.cursor = +arg;
        S.focus = 'sql';
        S.wantFocus = t.getAttribute('data-fk');
        draw({ sql: 1 });
        return;
      case 'errs':
        select(dryErrors[0]);
        S.focus = 'actions';
        S.wantFocus = 'act:' + dryErrors[0];
        draw();
        flash(A[dryErrors[0]].name + ': ✗ ' + A[dryErrors[0]].dryError);
        return;
      case 'theme': {
        var toggle = document.getElementById('theme-toggle');
        if (toggle) toggle.click();
        return;
      }
      case 'key': press(arg); return;
      case 'mobile':
        S.mobile = arg;
        if (arg === 'files' && !S.view) S.view = 'files';
        S.wantFocus = 'm:' + arg;
        draw();
        return;
    }
  });

  root.addEventListener('click', function (e) {
    var ref = e.target.closest('.ref');
    if (!ref) return;
    goToRef(ref);
  });

  function goToRef(ref) {
    var a = A[ref.getAttribute('data-ref')];
    hideCard();
    if (!a) return;
    openFile(a.file, { sel: a.name });
    S.focus = 'source';
    S.wantFocus = 'src';
    draw();
  }

  function toggleFlag(flag) {
    S.flags[flag] = !S.flags[flag];
    S.wantFocus = S.wantFocus || 'flag:' + flag;
    say(flag + (S.flags[flag] ? ' on' : ' off'));
    draw({ run: 1 });
  }

  function pickTab(name) {
    S.tab = name;
    S.focus = name === 'lineage' ? 'lineage' : 'sql';
    S.wantFocus = 'tab:' + name;
    draw({ sql: 1 });
  }

  root.addEventListener('dblclick', function (e) {
    var t = e.target.closest('[data-do="lin"]');
    if (t) jump(+t.getAttribute('data-arg'));
  });

  root.addEventListener('input', function (e) {
    var k = e.target.getAttribute('data-in');
    if (k === 'query') { S.query = e.target.value; draw({ tree: 1 }); }
    else if (k === 'col-name') { S.col.name = e.target.value; S.col.cursor = 0; draw({ sql: 1 }); }
    else if (k === 'col-type') { S.col.type = e.target.value; S.col.cursor = 0; draw({ sql: 1 }); }
  });

  // applyTarget keeps the target's three boxes and compiles with them.
  function applyTarget() {
    var next = {};
    ['prefix', 'schema', 'database'].forEach(function (k) {
      var el = root.querySelector('[data-in="tgt-' + k + '"]');
      next[k] = el ? el.value.trim().replace(/[^\w-]/g, '') : S.target[k];
    });
    if (JSON.stringify(next) === JSON.stringify(S.target)) return;
    S.target = next;
    compile();
  }
  root.addEventListener('change', function (e) {
    var k = e.target.getAttribute('data-in') || '';
    if (k.indexOf('tgt-') === 0) applyTarget();
  });

  // move moves the cursor of the focused list by n rows.
  function move(n) {
    var a = sel();
    if (S.focus === 'tree') {
      var rows = Array.prototype.slice.call(part.tree.querySelectorAll('[data-list] [data-fk]'));
      var at = rows.indexOf(document.activeElement);
      if (at < 0) at = rows.findIndex(function (r) { return r.tabIndex === 0; });
      var next = rows[Math.min(Math.max(at + n, 0), rows.length - 1)];
      if (!next) return;
      if (S.view === 'files') {
        S.treeCursor = next.getAttribute('data-arg');
        S.wantFocus = next.getAttribute('data-fk');
        draw({ tree: 1 });
      } else {
        rows.forEach(function (r) { r.tabIndex = r === next ? 0 : -1; });
        next.focus();
        nearest(next);
      }
    } else if (S.focus === 'actions' && a) {
      var list = FILES[S.file].actions;
      var i = Math.min(Math.max(list.indexOf(S.sel) + n, 0), list.length - 1);
      select(list[i]);
      S.wantFocus = 'act:' + list[i];
      draw({ actions: 1, run: 1, sql: 1 });
    } else if (S.focus === 'lineage' && a) {
      var lr = lineageRows(a);
      for (var j = S.lin + n; j >= 0 && j < lr.length; j += n) {
        if (lr[j].ref) { S.lin = j; S.wantFocus = 'lin:' + lr[j].ref.name; draw({ sql: 1 }); return; }
      }
    } else if (S.focus === 'sql' && S.tab === 'columns' && a) {
      var fields = filteredCols(a);
      S.col.cursor = Math.min(Math.max(S.col.cursor + n, 0), Math.max(fields.length - 1, 0));
      if (fields[S.col.cursor]) S.wantFocus = 'col:' + fields[S.col.cursor].name;
      draw({ sql: 1 });
    } else if (S.focus === 'source') {
      var count = FILES[S.file].text.replace(/\n$/, '').split('\n').length;
      S.line = Math.min(Math.max(S.line + n, 1), count);
      S.revealLine = true;
      S.wantFocus = 'src';
      draw({ source: 1 });
    }
  }

  // press is a key, from the keyboard or a tap on the footer.
  function press(k) {
    var a = sel();
    switch (k) {
      case 'j': case 'down': move(1); return true;
      case 'k': case 'up': move(-1); return true;
      case 'd': toggleFlag('deps'); return true;
      case 'D': toggleFlag('dependents'); return true;
      case 'f': toggleFlag('full'); return true;
      case 'a': toggleFlag('api'); return true;
      case 'r': startRun(); return true;
      case 'y': case 'Y':
        if (S.focus === 'sql' && S.tab === 'columns' && a) {
          var c = filteredCols(a)[S.col.cursor];
          if (c) copy(k === 'y' ? c.name : c.name + ' ' + c.type, k === 'y' ? c.name : c.name + ' ' + c.type);
          return true;
        }
        var x = S.focus === 'lineage' && a ? (lineageRows(a)[S.lin] || {}).ref : a;
        if (!x) return false;
        if (k === 'y') copy(id(x), id(x)); else copy(x.file, x.file);
        return true;
      case 'enter':
        if (S.focus === 'lineage') { jump(S.lin); return true; }
        if (S.focus === 'actions' && a) { S.tab = 'sql'; pickTab('sql'); return true; }
        return false;
      case '⌫': case 'backspace':
        if (S.focus === 'lineage') { back(); return true; }
        return false;
      case 'tab': {
        var tabs = ['sql', 'columns', 'lineage'];
        pickTab(tabs[(tabs.indexOf(S.tab) + 1) % tabs.length]);
        return true;
      }
      case 'O': pickTab('sql'); return true;
      case '/':
        if (S.tab === 'columns' && (S.focus === 'sql' || S.focus === 'actions')) {
          var box = root.querySelector('[data-in="col-name"]');
          if (box) { box.focus(); return true; }
        }
        S.view = 'search';
        S.focus = 'tree';
        S.wantFocus = 'in:query';
        draw();
        return true;
    }
    return false;
  }

  root.addEventListener('keydown', function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    var t = e.target;
    if (t.matches('input')) {
      var k = t.getAttribute('data-in') || '';
      if (e.key === 'Enter' && k.indexOf('tgt-') === 0) { e.preventDefault(); applyTarget(); }
      else if (e.key === 'Escape' && k.indexOf('col-') === 0) {
        e.preventDefault();
        S.col.name = ''; S.col.type = '';
        colsKey = null;
        S.wantFocus = 'tab:columns';
        S.focus = 'sql';
        draw({ sql: 1 });
      } else if (e.key === 'Enter' && k.indexOf('col-') === 0) {
        e.preventDefault();
        var f = filteredCols(sel() || { cols: [] })[0];
        S.wantFocus = f ? 'col:' + f.name : null;
        S.focus = 'sql';
        draw({ sql: 1 });
      } else if (e.key === 'ArrowDown' && k === 'query') {
        e.preventDefault();
        var first = part.tree.querySelector('[data-list] [data-fk]');
        if (first) first.focus();
      }
      return;
    }
    if (t.matches('[role="tab"]')) {
      var tabs = Array.prototype.slice.call(t.parentNode.querySelectorAll('[role="tab"]'));
      var i = tabs.indexOf(t), j = null;
      if (e.key === 'ArrowRight') j = (i + 1) % tabs.length;
      if (e.key === 'ArrowLeft') j = (i - 1 + tabs.length) % tabs.length;
      if (e.key === 'Home') j = 0;
      if (e.key === 'End') j = tabs.length - 1;
      if (j !== null) { e.preventDefault(); tabs[j].click(); return; }
    }
    if (t.classList.contains('ref') && e.key === 'Enter') { e.preventDefault(); goToRef(t); return; }
    if (e.key === 'Escape') { hideCard(); if (S.flash) { S.flash = ''; drawFooter(); } return; }
    var key = { ArrowDown: 'down', ArrowUp: 'up', Enter: 'enter', Backspace: 'backspace' }[e.key] || e.key;
    if (key === 'enter' && t.matches('button, [role="button"]') && !t.matches('[role="option"]')) return;
    if (key === ' ') return;
    if (key.length > 1 && ['down', 'up', 'enter', 'backspace'].indexOf(key) < 0) return;
    if (press(key)) e.preventDefault();
  });

  // The site's theme picks xf's: the ◐ label follows it.
  new MutationObserver(function () { drawStatus(); }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  if (window.matchMedia) {
    var mq = window.matchMedia('(prefers-color-scheme: light)');
    if (mq.addEventListener) mq.addEventListener('change', drawStatus);
  }

  // A narrow screen starts on the actions and the SQL, a wide one with
  // the files open beside them, as the screenshot shows dim_customers.
  // Until the rail is used, the width picks.
  var autoView = true;
  var fit = function () {
    if (!autoView) return;
    var want = window.innerWidth >= 1100 ? 'files' : null;
    if (want !== S.view) { S.view = want; draw(); }
  };
  root.addEventListener('click', function (e) { if (e.target.closest('[data-do="rail"], [data-do="mobile"]')) autoView = false; }, true);
  window.addEventListener('resize', fit);
  S.view = window.innerWidth >= 1100 ? 'files' : null;
  // A phone keeps the target closed, for room.
  if (window.innerWidth <= 760) S.targetOpen = false;
  draw();
})();
