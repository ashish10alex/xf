// The palette's open projects ("%"): the key lists them, the project this
// tab showed before comes second, and a pick opens one without showing the
// column's projects tab.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, openProjectList, matchedProjects, paletteList, pickFromPalette } from '../assets/state.js';
import { T } from '../assets/tree.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  globalThis.open = () => ({});
  globalThis.location = { href: '/?project=finance' };
  localStorage.clear();
  sessionStorage.setItem('xf-web-shown', JSON.stringify(['finance', 'ops', 'shop']));
  Object.assign(S, {
    mode: 'project', key: 'finance', openIn: 'same-tab', switching: false, flash: '', overlay: null, state: { hasGraph: true },
    open: [{ key: 'finance', name: 'finance', root: '/r/finance' }, { key: 'shop', name: 'shop', root: '/r/shop' }, { key: 'ops', name: 'ops', root: '/r/ops' }],
  });
});

test('the list starts on the project this tab showed before', () => {
  openProjectList();
  assert.equal(S.overlay, 'palette');
  assert.deepEqual(paletteList().map((o) => o.key), ['finance', 'ops', 'shop']);
  assert.equal(S.palette.cursor, 1);
});

test('the query narrows by name or path', () => {
  openProjectList();
  S.palette.query = '%sh';
  assert.deepEqual(matchedProjects().map((o) => o.key), ['shop']);
  S.palette.query = '%/r/o';
  assert.deepEqual(matchedProjects().map((o) => o.key), ['ops']);
});

test('a pick opens the project and leaves the column alone', async () => {
  openProjectList();
  pickFromPalette();
  assert.equal(S.overlay, null);
  assert.equal(S.flash, 'opening ops…');
  assert.equal(localStorage.getItem('xf-web:tree:/r/ops'), null);
  assert.equal(T.view !== 'projects' || !T.shown, true);
});
