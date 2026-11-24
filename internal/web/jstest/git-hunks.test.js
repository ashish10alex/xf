import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { hunksOf, textOf } from '../assets/git-hunks.js';

// fresh is hunksOf with the base given as a Text, so it never reads the
// memo of the last base string.
const doc = (s) => cm.Text.of(s.split('\n'));
const fresh = (base, text) => hunksOf(doc(base), text);
const shape = (hunks) => hunks.map(({ start, added, removed }) => ({ start, added, removed }));

test('the same base string gives the same Text', () => {
  const base = 'select a\nfrom t';
  const t = textOf(base);
  assert.equal(textOf(base), t);
  assert.equal(t.toString(), base);
});

test('the editor Text comes back as it is and keeps the base', () => {
  const base = 'a\nb';
  const t = textOf(base);
  const d = doc('a\nc');
  assert.equal(textOf(d), d);
  assert.equal(textOf(base), t);
});

test('a different base string builds its own Text', () => {
  const t = textOf('a\nb');
  const u = textOf('a\nc');
  assert.notEqual(u, t);
  assert.equal(u.toString(), 'a\nc');
  const again = textOf('a\nb');
  assert.notEqual(again, u);
  assert.equal(again.toString(), 'a\nb');
});

test('the same base against two docs gives what two fresh calls give', () => {
  const base = 'select a\nfrom t\nwhere x = 1\norder by a';
  const docA = doc('select a, b\nfrom t\nwhere x = 1\norder by a');
  const docB = doc('select a\nfrom t\norder by a\nlimit 10');
  const a = hunksOf(base, docA);
  const b = hunksOf(base, docB);
  assert.deepEqual(shape(a), shape(fresh(base, docA)));
  assert.deepEqual(shape(b), shape(fresh(base, docB)));
  assert.notDeepEqual(shape(a), shape(b));
});

test('a different base never reads the first base\'s Text', () => {
  const text = doc('one\ntwo\nthree');
  const base1 = 'one\ntwo\nthree';
  const base2 = 'zero\none\nthree';
  assert.deepEqual(hunksOf(base1, text), []);
  const h2 = hunksOf(base2, text);
  // Had the memo given base1's Text, there would be no hunks.
  assert.notDeepEqual(h2, []);
  assert.deepEqual(shape(h2), shape(fresh(base2, text)));
  // base1 again after base2 took the memo: built again, still no hunks.
  assert.deepEqual(hunksOf(base1, text), []);
});

test('a string doc takes the memo and the base is built again', () => {
  const base = 'a\nb\nc';
  assert.deepEqual(shape(hunksOf(base, 'a\nx\nc')), shape(fresh(base, 'a\nx\nc')));
  assert.equal(textOf('a\nx\nc').toString(), 'a\nx\nc');
  assert.equal(textOf(base).toString(), base);
  assert.deepEqual(shape(hunksOf(base, doc('a\nb\nc\nd'))), shape(fresh(base, doc('a\nb\nc\nd'))));
});
