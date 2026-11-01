// cmd+shift+e, VS Code's Show Explorer: it shows the tree on the open
// file and gives it the keys, from any view of the column; again, with
// the tree holding the keys, it gives them back and the tree stays.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, isRevealKey } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { T, key, revealActive } from '../assets/tree.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/' };
  key('noop'); // reads the project's tree settings, once
  Object.assign(T, {
    view: 'files', shown: false, loaded: true, prompt: null, filtering: false, filter: '', centre: null, cursor: '',
    files: [{ path: 'definitions/sales/a.sqlx' }, { path: 'README.md' }], dirs: [], open: {},
  });
  SRC.file = 'definitions/sales/a.sqlx';
  S.focus = 'source';
});

test('it shows the tree on the open file, with the keys', () => {
  revealActive();
  assert.equal(T.shown, true);
  assert.equal(T.view, 'files');
  assert.equal(S.focus, 'tree');
  assert.equal(T.cursor, 'definitions/sales/a.sqlx');
  assert.deepEqual(T.centre, { path: 'definitions/sales/a.sqlx', always: true });
  assert.equal(T.open['definitions/sales'], true);
});

test('from Source Control it swaps to the tree', () => {
  Object.assign(T, { shown: true, view: 'scm' });
  revealActive();
  assert.equal(T.view, 'files');
  assert.equal(S.focus, 'tree');
});

test('again with the keys in the tree, they go back and the tree stays', () => {
  const was = hooks.sourceShownNow;
  hooks.sourceShownNow = () => true;
  try {
    revealActive();
    revealActive();
    assert.equal(T.shown, true);
    assert.equal(S.focus, 'source');
  } finally {
    hooks.sourceShownNow = was;
  }
});

test('the key is ctrl+shift+e off a Mac, not plain E', () => {
  assert.equal(isRevealKey({ ctrlKey: true, metaKey: false, altKey: false, shiftKey: true, key: 'E' }), true);
  assert.equal(isRevealKey({ ctrlKey: false, metaKey: false, altKey: false, shiftKey: true, key: 'E' }), false);
  assert.equal(isRevealKey({ ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, key: 'e' }), false);
});
