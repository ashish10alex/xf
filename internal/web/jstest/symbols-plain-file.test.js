// A file with no action (a .sqlx whose compile failed, a .py script) is
// still the file in the source pane: the symbol picker opens on it.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, openSymbols, showPlainFile } from '../assets/state.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, zoomed: false, flash: '', focus: 'source', sel: null, view: null, plainFile: '' });
  S.boot = { root: '/p' };
  hooks.sourceShownNow = () => true;
  hooks.sourceSymbols = async () => [];
});

test('the symbol picker opens on a file with no action', async () => {
  showPlainFile('definitions/Access/mapping_table_geography.sqlx');
  await openSymbols();
  assert.equal(S.overlay, 'palette');
  assert.equal(S.palette.query, '@');
});

test('with no file shown it says so', async () => {
  await openSymbols();
  assert.equal(S.overlay, null);
  assert.match(S.flash, /no source file open/);
});
