// pane2Tab picks the tab pane 2's lower box starts on, sql or columns: in
// a new browser tab, and again once the setting changes. A declaration
// keeps its own tab (ADR 0015).

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, externalTab, pane2Default, sqlTabOn } from '../assets/state.js';
import '../assets/settings.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  Object.assign(S, { mode: 'project', lineageOpen: false, errTab: false, focus: 'source' });
  S.cols = { shown: false, name: '', type: '', typing: false, cursor: 0, of: null };
  S.boot = { root: '/p', pane2Tab: 'sql', vimKeys: {} };
});

test('columns starts pane 2 on its columns tab', () => {
  pane2Default('columns');
  assert.equal(sqlTabOn(), 'columns');
  pane2Default('sql');
  assert.equal(sqlTabOn(), 'sql');
});

test('a change of the setting switches the tab', () => {
  hooks.settingsChanged({ vimKeys: {}, pane2Tab: 'columns' });
  assert.equal(S.cols.shown, true);
  assert.equal(S.boot.pane2Tab, 'columns');
});

test('another setting changing leaves the tab picked', () => {
  S.boot.pane2Tab = 'columns';
  S.cols.shown = false; // tab went to sql since
  hooks.settingsChanged({ vimKeys: {}, pane2Tab: 'columns', diffStyle: 'side-by-side' });
  assert.equal(S.cols.shown, false);
});

test('a declaration keeps its tab, and the setting comes back after it', () => {
  const decl = { id: 'd', external: true, tableType: 'VIEW' };
  externalTab({ id: 'a' }, decl);
  assert.equal(S.cols.shown, false); // a declared view shows its SQL
  pane2Default('columns');
  assert.equal(S.cols.shown, false);
  externalTab(decl, { id: 'b' });
  assert.equal(S.cols.shown, true);
});
