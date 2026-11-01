import { test } from 'node:test';
import assert from 'node:assert/strict';
import { diffRows, wordMarks } from '../assets/diff.js';

const ops = (rows) => rows.map((r) => (r ? r.op + r.text : null));

test('the same text has no rows', () => {
  assert.deepEqual(diffRows('a\nb', 'a\nb'), []);
});

test('a changed line is a - and a + row, marking what differs', () => {
  const rows = diffRows('select a\nfrom t', 'select b\nfrom t');
  assert.deepEqual(ops(rows), ['-select a', '+select b', ' from t']);
  assert.deepEqual(rows[0].marks, [[7, 8]]);
  assert.deepEqual(rows[1].marks, [[7, 8]]);
});

test('added lines mark nothing, as all of them differ', () => {
  const rows = diffRows('a\nc', 'a\nb\nc');
  assert.deepEqual(ops(rows), [' a', '+b', ' c']);
  assert.deepEqual(rows[1].marks, []);
});

test('changes far apart keep three lines round each, with a gap between', () => {
  const lines = Array.from({ length: 20 }, (_, i) => `l${i}`);
  const changed = [...lines];
  changed[1] = 'x1';
  changed[18] = 'x18';
  const rows = diffRows(lines.join('\n'), changed.join('\n'));
  assert.deepEqual(ops(rows), [' l0', '-l1', '+x1', ' l2', ' l3', ' l4', null, ' l15', ' l16', ' l17', '-l18', '+x18', ' l19']);
});

test('word marks pick out whole words that differ', () => {
  const dev = 'alex-personal-dev-01.world_cup_dataform.AA_fct_matches';
  const prod = 'alex-personal-prod-01.world_cup_dataform.fct_matches';
  const [a, b] = wordMarks(dev, prod);
  assert.deepEqual(a.map(([f, t]) => dev.slice(f, t)), ['dev', 'AA_']);
  assert.deepEqual(b.map(([f, t]) => prod.slice(f, t)), ['prod']);
});

test('the same names have no word marks', () => {
  assert.deepEqual(wordMarks('p.d.t', 'p.d.t'), [[], []]);
});
