// The Scratch editor's bracket scan resumes from a copy of the last run,
// and gives what a scan from the start gives; the signature help works the
// call out once per transaction.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';

const dateTrunc = {
  name: 'DATE_TRUNC', snippet: 'DATE_TRUNC(${date_value}, ${granularity})', detail: 'DATE_TRUNC(date_value, granularity)', url: '',
  signatures: [{
    label: 'DATE_TRUNC(date_value, granularity)', returns: 'DATE',
    params: [{ name: 'date_value', span: [11, 21] }, { name: 'granularity', span: [23, 34], values: ['DAY', 'MONTH'] }],
  }],
};
globalThis.fetch = async () => new Response(JSON.stringify([dateTrunc]));

const { S } = await import('../assets/state.js');
const F = await import('../assets/scratch-functions.js');

// rand is a seeded generator, so a failure comes back the same.
function rand(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const pieces = [
  "'", '"', '`', "'''", '"""', "''", '\\', '--', '#', '/*', '*/', '(', ')', '[', ']', ',', ',', ' ', ' ', ' ', '\n', '\n',
  'select', 'from', 'a', 'b_1', 'x.y', 'DATE_TRUNC', 'IN', 'unpivot include nulls', 'order by', '42', '1.5', '7e3', '0x1f', '-', '/', '*',
];

function sql(r, length) {
  let s = '';
  while (s.length < length) s += pieces[Math.floor(r() * pieces.length)];
  return s;
}

const same = (text, why) => assert.deepStrictEqual(F.scan(text), F.scanFresh(text), `${why} at length ${text.length}`);

function edit(r, text, at) {
  const kind = Math.floor(r() * 3);
  const piece = pieces[Math.floor(r() * pieces.length)];
  if (kind === 0) return text.slice(0, at) + piece + text.slice(at);
  if (kind === 1) return text.slice(0, at) + text.slice(at + 1 + Math.floor(r() * 5));
  return text.slice(0, at) + piece + text.slice(at + 1);
}

for (const [dialect, caps] of [['BigQuery', {}], ['Databricks', { warehouse: 'databricks' }]]) {
  test(`${dialect}: scan from a copy gives what a scan from the start gives`, () => {
    S.caps = caps;
    for (const seed of [1, 2, 3]) {
      const r = rand(seed * 7919 + dialect.length);
      const text = sql(r, 2500 + Math.floor(r() * 5000));
      // Every prefix, growing, as typing at the end gives.
      for (let n = 0; n <= text.length; n++) same(text.slice(0, n), `seed ${seed} growing`);
      // And shrinking, as deleting at the end gives.
      for (let n = text.length; n >= 0; n -= 1 + Math.floor(r() * 40)) same(text.slice(0, n), `seed ${seed} shrinking`);
      F.scan(text);
      const marks = F.scanMarks();
      assert.ok(marks.length > 0, `seed ${seed} has a copy`);
      // Edits before the first copy, at each side of a copy, and past the last.
      const at = [Math.floor(r() * marks[0])];
      for (const m of marks) at.push(m - 1, m, m + 1);
      at.push(marks[marks.length - 1] + 1 + Math.floor(r() * (text.length - marks[marks.length - 1] - 1)));
      for (const k of at) {
        for (let n = 0; n < 4; n++) {
          F.scan(text);
          const changed = edit(r, text, k);
          same(changed, `seed ${seed} edit at ${k}`);
          for (let n2 = Math.max(0, k - 3); n2 <= Math.min(changed.length, k + 300); n2 += 1 + Math.floor(r() * 3)) same(changed.slice(0, n2), `seed ${seed} edit at ${k}, prefix`);
          same(changed, `seed ${seed} edit at ${k}, again`);
        }
      }
    }
  });

  test(`${dialect}: an unclosed comment or string then closed scans on past it`, () => {
    S.caps = caps;
    const code = 'select DATE_TRUNC(a, b), f(x, [1, 2], g(y))\n'.repeat(120);
    for (const [open, close] of [['/* not closed (', ' */'], ["'still a string (", "'"], ['"""three (', '"""'], ['`name (', '`'], ["'''three (", "'''"]]) {
      const start = code + 'h(a, ' + open + code.slice(0, 3000);
      same(start, `open ${open}`);
      assert.equal(F.scan(start).inCode, false, `${open} is not code`);
      const closed = start + close + ' k(1, ';
      same(closed, `closed ${open}`);
      for (let n = start.length - 10; n <= closed.length; n++) same(closed.slice(0, n), `closing ${open}`);
    }
  });
}

test('a word, a number, -- or a quote that goes on past a copy is read whole', () => {
  S.caps = {};
  // Each fill at each offset, so a copy falls just after each kind of step.
  for (const [fill, lead] of ['a ', '1 ', "'' ", '- ', '/ '].flatMap((f) => [...f].map((_, k) => [f, ' '.repeat(k)]))) {
    const text = 'f(' + lead + fill.repeat(Math.floor(4500 / fill.length)) + 'abc def';
    F.scan(text);
    for (const m of F.scanMarks()) {
      for (const more of ['X', '7', "'", '-', '*']) {
        same(text, 'before');
        same(text.slice(0, m) + more + text.slice(m), `${JSON.stringify(more)} at ${m} after ${JSON.stringify(fill)}`);
      }
    }
  }
});

test('the signature help works the call out once a transaction, and esc ends it', async () => {
  S.caps = {};
  await F.loadFunctions();
  let calls = 0;
  const spy = (state) => { calls++; return state.doc.toString(); };
  const ext = F.functionExtensions(spy);
  const sigOn = ext[0];
  let state = cm.EditorState.create({ doc: 'select ', selection: { anchor: 7 }, extensions: ext });
  const type = (s) => {
    const head = state.selection.main.head;
    state = state.update({ changes: { from: head, insert: s }, selection: { anchor: head + s.length }, userEvent: 'input.type' }).state;
  };
  const tooltip = () => state.facet(cm.showTooltip).filter(Boolean);

  for (const ch of 'DATE_TRUNC') type(ch);
  assert.equal(calls, 0, 'no scan while it is off');
  assert.equal(state.field(sigOn), false);

  calls = 0;
  type('(');
  assert.equal(calls, 1, '( scans once');
  let sig = state.field(sigOn);
  assert.deepStrictEqual(sig.info, F.callInfo(state.doc.toString()));
  assert.equal(sig.info.param.name, 'date_value');
  assert.equal(tooltip().length, 1);
  assert.equal(tooltip()[0].pos, state.doc.length);

  calls = 0;
  type('d');
  assert.equal(calls, 1, 'a letter in the call scans once');
  calls = 0;
  type(',');
  assert.equal(calls, 1, ', scans once');
  sig = state.field(sigOn);
  assert.deepStrictEqual(sig.info, F.callInfo(state.doc.toString()));
  assert.equal(sig.info.param.name, 'granularity');
  assert.equal(tooltip().length, 1);

  // esc turns it off, and the tooltip goes.
  const view = { get state() { return state; }, dispatch(spec) { state = state.update(spec).state; } };
  calls = 0;
  F.escapeSig(view, sigOn);
  assert.equal(state.field(sigOn), false);
  assert.equal(tooltip().length, 0);
  assert.equal(calls, 0);

  // A space typed past the call turns it on, scans once, and finds no call.
  type(' DAY) ');
  calls = 0;
  type(' ');
  assert.equal(calls, 1, 'a space scans once');
  assert.equal(state.field(sigOn), false);
  assert.equal(tooltip().length, 0);
});

test('a change of dialect scans again: # is a comment in BigQuery alone', () => {
  const text = 'f(a, ' + 'b, '.repeat(1500) + '# c, (\nd';
  S.caps = {};
  same(text, 'BigQuery');
  S.caps = { warehouse: 'databricks' };
  same(text, 'Databricks');
  assert.notDeepStrictEqual(F.scan(text), (S.caps = {}, F.scan(text)));
});
