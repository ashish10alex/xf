// Column completion offers each column name once, however many of the
// query's tables have it: in any case, with the first table's spelling.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mergeColumns } from '../assets/scratch-complete.js';

const col = (name, type, description = '') => ({ name, type, description });
const table = (name, cols, aliases = []) => ({ name, aliases, owner: aliases.length === 1 ? aliases[0] : name, cols });

test('a column in several tables is one completion', () => {
  const got = mergeColumns([
    table('daily', [col('ORDER_NUMBER', 'INTEGER'), col('ORDER_DATE', 'DATE')]),
    table('extract', [col('order_number', 'INTEGER', 'The order.')]),
    table('status', [col('ORDER_NUMBER', 'STRING'), col('STATUS', 'STRING')], ['s']),
  ], true);
  assert.deepEqual(got.map((o) => [o.label, o.detail]), [
    ['ORDER_NUMBER', 'INTEGER/STRING · 3 tables'],
    ['ORDER_DATE', 'DATE · daily'],
    ['STATUS', 'STRING · s'],
  ]);
  assert.equal(got[0].info, 'daily       INTEGER\nextract     INTEGER\nstatus (s)  STRING\n\nThe order.');
  assert.equal(got[1].info, undefined);
});

test('a table read twice counts once, and its info names both aliases', () => {
  const [o] = mergeColumns([table('orders', [col('ID', 'INTEGER', 'Key.')], ['a', 'b'])], true);
  assert.equal(o.detail, 'INTEGER · orders');
  assert.equal(o.info, 'orders (a, b)  INTEGER\n\nKey.');
});

test('one table names no owner unless asked', () => {
  const [o] = mergeColumns([table('orders', [col('ID', '', 'Key.')])], false);
  assert.equal(o.detail, '');
  assert.equal(o.info, 'Key.');
});
