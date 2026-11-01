// Source Control's Changes section opens whenever the view shows: by its
// key, its tab or the rail, and when a tab's workspace or the browser kept
// it closed. Closing it lasts while the view stays.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { G } from '../assets/scm.js';
import { T, key, toggleSCM, showView, toggleView } from '../assets/tree.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/' };
  key('noop'); // reads the project's tree settings, once
  Object.assign(T, { view: 'files', shown: false, loaded: true, prompt: null, filtering: false, filter: '' });
  G.open.changes = false;
});

test('ctrl+g opens Changes', () => {
  toggleSCM();
  assert.equal(T.view, 'scm');
  assert.equal(G.open.changes, true);
});

test('the tab opens Changes', () => {
  T.shown = true;
  showView('scm');
  assert.equal(G.open.changes, true);
});

test('closed while showing, it stays closed until the view shows again', () => {
  toggleView('scm');
  G.open.changes = false;
  showView('scm'); // shows already
  assert.equal(G.open.changes, false);
  toggleView('scm'); // hides
  toggleView('scm'); // shows
  assert.equal(G.open.changes, true);
});
