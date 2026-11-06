// The result grid lays out nested cells as BigQuery's console does: a
// STRUCT column is a column per field, and an array's elements are lines
// down its row.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { leavesOf, rowLines, heightOf, anyArrays } from '../assets/scratch.js';

const ts = { name: 'AT', type: 'TIMESTAMP' };
const schema = [
  { name: 'SITE', type: 'STRING' },
  { name: 'EVENTS', type: 'RECORD', mode: 'REPEATED', fields: [ts, { name: 'N', type: 'INTEGER' }] },
];

// shown is the lines as text, '' where a line has no cell.
const shown = (lines, width) => lines.map((l) => Array.from({ length: width }, (_, i) => (l[i] ? String(l[i].v) : '')));

test('a STRUCT is a column per field, named below its column', () => {
  const ls = leavesOf(schema);
  assert.deepEqual(ls.map((l) => [l.col, l.sub, l.arr]), [[0, '', false], [1, 'AT', true], [1, 'N', true]]);
});

test('an array of structs is a line per element; the other columns are on its first', () => {
  const row = ['A', [['t1', '1'], ['t2', '2'], ['t3', '3']]];
  assert.deepEqual(shown(rowLines(schema, row), 3), [['A', 't1', '1'], ['', 't2', '2'], ['', 't3', '3']]);
  assert.equal(heightOf(schema, row), 3);
});

test('an empty or NULL array is one line', () => {
  assert.equal(heightOf(schema, ['A', []]), 1);
  assert.equal(heightOf(schema, ['A', null]), 1);
});

test('arrays side by side share lines, the longer setting the height', () => {
  const s = [{ name: 'X', type: 'STRING', mode: 'REPEATED' }, { name: 'Y', type: 'INTEGER', mode: 'REPEATED' }];
  assert.deepEqual(shown(rowLines(s, [['a', 'b', 'c'], ['1']]), 2), [['a', '1'], ['b', ''], ['c', '']]);
});

test('an array inside an array element takes lines of its own', () => {
  const s = [{ name: 'O', type: 'RECORD', mode: 'REPEATED', fields: [{ name: 'K', type: 'STRING' }, { name: 'V', type: 'STRING', mode: 'REPEATED' }] }];
  const row = [[['k1', ['a', 'b']], ['k2', ['c']]]];
  assert.deepEqual(shown(rowLines(s, row), 2), [['k1', 'a'], ['', 'b'], ['k2', 'c']]);
});

test('a NULL STRUCT is NULL in each of its fields', () => {
  const s = [{ name: 'S', type: 'RECORD', fields: [ts, { name: 'N', type: 'INTEGER' }] }];
  const lines = rowLines(s, [null]);
  assert.equal(lines.length, 1);
  assert.deepEqual(lines[0].map((x) => x.v), [null, null]);
  assert.equal(anyArrays(s), false);
});

test('a flat result is the columns as they are, a line a row', () => {
  const s = [{ name: 'A', type: 'STRING' }, { name: 'B', type: 'INT64' }];
  assert.equal(anyArrays(s), false);
  assert.deepEqual(leavesOf(s).map((l) => l.sub), ['', '']);
  assert.deepEqual(shown(rowLines(s, ['x', '2']), 2), [['x', '2']]);
});
