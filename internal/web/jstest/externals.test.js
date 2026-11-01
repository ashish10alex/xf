// The declarations table's logic (externals.js): its filter, j/k, the
// source pane's cursor picking a row, and the tab pane 2 opens a
// declaration on.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S, externalTab, hooks } from '../assets/state.js';
import { externalRows, freshness, groupOf, moveInExternals, rowAt, setFilter } from '../assets/externals.js';

// A view the page asks for never comes; a POST is answered.
globalThis.fetch = (_, init) => (init.method === 'POST' ? Promise.resolve(new Response('{}')) : new Promise(() => {}));

const row = (name, line, table = true) => ({
  id: 'p.raw.' + name, name, kind: 'declaration', file: 'definitions/sources.js',
  ...(line == null ? {} : { at: { line, col: 2 } }), ...(table ? { consoleUrl: 'https://console/' + name } : {}),
});

function show(rows, sel) {
  S.view = { id: sel, file: 'definitions/sources.js', pane: [], externals: rows };
  S.sel = sel;
}

test('the filter narrows the rows, ignoring case', () => {
  show([row('ACCOUNTS_V', 1), row('ADDRESSES_V', 2), row('pre_built')], 'p.raw.ACCOUNTS_V');
  setFilter('addr');
  assert.deepEqual(externalRows().map((r) => r.name), ['ADDRESSES_V']);
  setFilter('');
  assert.equal(externalRows().length, 3);
});

test('j and k move through the rows the filter shows, and stop at the ends', () => {
  show([row('A', 1), row('B', 2), row('C', 3)], 'p.raw.A');
  moveInExternals(1);
  assert.equal(S.sel, 'p.raw.B');
  moveInExternals(2);
  assert.equal(S.sel, 'p.raw.C');
  moveInExternals(1);
  assert.equal(S.sel, 'p.raw.C');
  setFilter('.a');
  moveInExternals(-1); // C is filtered out: k picks the last row shown
  assert.equal(S.sel, 'p.raw.A');
  setFilter('');
  assert.equal(hooks.moveInExternals, moveInExternals, 'state.js moves through the table with j/k');
});

test('the source cursor picks the row on its line', async () => {
  show([row('A', 1), row('B', 4), row('built')], 'p.raw.A');
  hooks.sourceCursor('definitions/sources.js', 4);
  await new Promise((r) => setTimeout(r, 120));
  assert.equal(S.sel, 'p.raw.B');
  hooks.sourceCursor('definitions/sources.js', 2); // no row's line
  hooks.sourceCursor('definitions/other.js', 1); // another file
  await new Promise((r) => setTimeout(r, 120));
  assert.equal(S.sel, 'p.raw.B');
});

test('of names on one line, the cursor picks the one it is on', () => {
  const rows = [{ id: 'c', at: { line: 1, col: 2 } }, { id: 'o', at: { line: 1, col: 15 } }, { id: 'x', at: { line: 3, col: 0 } }];
  assert.equal(rowAt(rows, 1, 0).id, 'c'); // before them all
  assert.equal(rowAt(rows, 1, 9).id, 'c');
  assert.equal(rowAt(rows, 1, 15).id, 'o');
  assert.equal(rowAt(rows, 1, 40).id, 'o');
  assert.equal(rowAt(rows, 2, 0), undefined);
});

test('a declaration opens on its columns, a declared view on its SQL, and a model gets its tab back', () => {
  const model = { id: 'p.mart.orders' }, table = { id: 'p.raw.t', external: true }, view = { id: 'p.raw.v', external: true };
  S.cols.shown = false; // sql, for models
  externalTab(model, table);
  assert.equal(S.cols.shown, true, 'a declaration opens on its columns');
  externalTab(table, { ...table, tableType: 'TABLE' });
  assert.equal(S.cols.shown, true);
  S.cols.shown = false; // tab to the sql: it stays
  externalTab(table, { ...table, tableType: 'TABLE' });
  assert.equal(S.cols.shown, false);
  externalTab(table, view);
  externalTab(view, { ...view, tableType: 'VIEW' });
  assert.equal(S.cols.shown, false, "a view opens on its SQL once its type is known");
  externalTab(view, model);
  assert.equal(S.cols.shown, false, "the models' tab comes back");
  externalTab(model, { ...table, tableType: 'TABLE' });
  assert.equal(S.cols.shown, true, 'a declaration seen before opens on its columns again');
  externalTab(table, model);
  assert.equal(S.cols.shown, false);
});

test('a row says its table once in its group, and its freshness as a tone', () => {
  assert.equal(groupOf(row('FEAT_V', 1)), 'p.raw');
  assert.equal(groupOf({ id: 'weird', name: 'x' }), '');
  const now = new Date(2026, 9, 22, 12).getTime();
  const at = (value, tone = '') => freshness({ value, tone }, now);
  assert.equal(freshness(undefined, now).tone, 'none');
  assert.equal(at('2026-10-22 04:55 · 7h ago').tone, 'fresh');
  assert.equal(at('2026-10-10 04:55 · 12d ago').tone, 'ok');
  assert.equal(at('2026-06-01 04:55 · 4mo ago').tone, 'idle');
  assert.deepEqual(at('2025-10-28 04:55 · 11mo ago'), { tone: 'old', text: '11mo ago', when: '2025-10-28 04:55' }, 'old is amber, not an error');
  assert.deepEqual(at('✗ Access Denied: Table x', 'error'), { tone: 'err', text: '✗ Access Denied' });
  assert.equal(at('checking…', 'dim').tone, 'wait');
  assert.equal(at('no credentials', 'warn').tone, 'warn');
  assert.equal(at('N/A', 'dim').tone, 'none');
});
