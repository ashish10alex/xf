// A $ ref's completion is asked for once and filtered on the page at each
// key after (partsResult's update), as byParts would filter it, until what
// is typed no longer is a $ word.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { partsResult, byParts } from '../assets/scratch-complete.js';

const all = ['events', 'stg_events', 'report', 'daily_reports.0300_PROMOTIONS_DATA_ROI'].map((label) => ({
  label, detail: 'table', apply: '${ref("' + label + '")}', type: 'class', boost: 3,
}));
const head = 'SELECT * FROM ';
const from = head.length;
const strip = (t) => t.replace(/^\$/, '');
const dollarWord = (t) => /^\$\w*$/.test(t);
// ctx is what CodeMirror passes update: the state after the key.
const ctx = (doc) => ({ state: { sliceDoc: (a, b) => doc.slice(a, b) } });
const result = (valid = dollarWord) => partsResult(all, from, strip, valid, '$');

test('update filters as byParts does for a longer prefix', () => {
  const r = result();
  assert.deepEqual(r.options, byParts(all, ''));
  for (const typed of ['$e', '$ev', '$events', '$0300_ROI', '$stg_e']) {
    const doc = head + typed;
    const u = r.update(r, from, doc.length, ctx(doc));
    assert.ok(u, typed);
    assert.deepEqual(u.options, byParts(all, strip(typed)), typed);
    assert.equal(u.from, from);
    assert.equal(u.filter, false);
    assert.equal(u.getMatch, r.getMatch);
    assert.equal(u.update, r.update);
  }
});

test('update gives null once the text is no longer a $ word', () => {
  const r = result();
  for (const typed of ['$ev.', '$ev ', '$ev`', '$ev--', '']) {
    const doc = head + typed;
    assert.equal(r.update(r, from, doc.length, ctx(doc)), null, JSON.stringify(typed));
  }
});

test('update gives null for a deletion that moves the range before from', () => {
  const r = result();
  // "M $e" deleted from "SELECT * FROM $ev": from maps to where it began.
  const doc = 'SELECT * FROv';
  assert.equal(r.update(r, from - 3, doc.length, ctx(doc)), null);
  assert.equal(r.update(r, from, from - 1, ctx(doc)), null);
});

test('update gives null once stillValid goes false, as after a compile', () => {
  let gen = 0;
  const g = gen;
  const r = result((t) => dollarWord(t) && g === gen);
  const doc = head + '$ev';
  assert.ok(r.update(r, from, doc.length, ctx(doc)));
  gen++;
  assert.equal(r.update(r, from, doc.length, ctx(doc)), null);
});

test('partsResult is null when nothing matches', () => {
  assert.equal(partsResult(all, from, strip, dollarWord, '$zzz'), null);
});
