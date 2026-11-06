// PIVOT and UNPIVOT in the Scratch editor: where they are offered, and
// the columns they make.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { afterTable, madeColumns, parseRefs, blank } from '../assets/scratch-complete.js';

const at = (sql) => afterTable(blank(sql), sql.length);

test('PIVOT and UNPIVOT go after a table in FROM or JOIN', () => {
  for (const sql of [
    'SELECT * FROM t ',
    'SELECT * FROM `p.d.t` AS x ',
    'SELECT * FROM d.t x ',
    'SELECT * FROM a, b ',
    'SELECT * FROM a JOIN b ',
    'SELECT * FROM (SELECT a, b FROM t WHERE c > 1) ',
    'SELECT * FROM (SELECT a FROM t) AS s ',
    'SELECT * FROM UNNEST([1, 2]) ',
  ]) assert.ok(at(sql), sql);
  for (const sql of [
    'SELECT ',
    'SELECT * FROM ',
    'SELECT * FROM t AS ',
    'SELECT * FROM t WHERE ',
    'SELECT * FROM t WHERE x = 1 ',
    'SELECT COUNT(*) ',
    'SELECT * FROM t WHERE x IN (1, 2) ',
    'SELECT a FROM t GROUP BY a ',
    // A .sqlx file's ${ref(…)}, blanked, is no table WHERE names.
    'SELECT * FROM                  WHERE ',
  ]) assert.ok(!at(sql), sql);
});

test('PIVOT is no alias of the table before it', () => {
  const [ref] = parseRefs("SELECT * FROM d.t PIVOT(SUM(x) FOR q IN ('Q1'))");
  assert.equal(ref.alias, '');
  const [u] = parseRefs('SELECT * FROM d.t UNPIVOT(v FOR n IN (a, b))');
  assert.equal(u.alias, '');
});

const made = (sql) => madeColumns(sql, blank(sql)).map((c) => c.name);

test('a PIVOT names a column for each value, as BigQuery does', () => {
  assert.deepEqual(made("SELECT * FROM t PIVOT(SUM(sales) FOR quarter IN ('Q1', \"Q2\", 'Q3' AS third))"), ['Q1', 'Q2', 'third']);
  assert.deepEqual(made('SELECT * FROM t PIVOT(SUM(s) FOR y IN (1, -2, TRUE, NULL, DATE \'2024-1-31\', 1.5, 2.5 AS half))'),
    ['_1', 'minus_2', 'TRUE', 'NULL', '_2024_01_31', 'half']);
  // An aggregate's prefix begins its columns' names.
  assert.deepEqual(made("SELECT * FROM t PIVOT(SUM(sales) AS total, COUNT(*) n FOR quarter IN ('Q1', 'Q2' second))"),
    ['total_Q1', 'n_Q1', 'total_second', 'n_second']);
  // Not yet closed: none.
  assert.deepEqual(made("SELECT * FROM t PIVOT(SUM(sales) FOR quarter IN ('Q1'"), []);
  // A comment or string that says pivot is none.
  assert.deepEqual(made("SELECT 'pivot(a for b in (1))' -- pivot(a for b in (1))"), []);
});

test('an UNPIVOT makes its values and name columns', () => {
  assert.deepEqual(madeColumns('SELECT * FROM t UNPIVOT(sales FOR quarter IN (Q1, Q2))', blank('SELECT * FROM t UNPIVOT(sales FOR quarter IN (Q1, Q2))')),
    [{ name: 'sales', by: 'unpivot' }, { name: 'quarter', by: 'unpivot' }]);
  assert.deepEqual(made("SELECT * FROM t UNPIVOT INCLUDE NULLS ((h1, h2) FOR half IN ((Q1, Q2) AS 'a', (Q3, Q4) AS 'b'))"), ['h1', 'h2', 'half']);
});
