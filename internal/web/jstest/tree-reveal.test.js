// The file tree: cmd+b showing it centres the open file's row, another
// reveal (a file opened elsewhere) only one out of view, and a hidden tree
// asks for none.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { T, key, reveal, toggleTree } from '../assets/tree.js';

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
});

test('cmd+b centres the open file, its folders open', () => {
  toggleTree();
  assert.equal(T.shown, true);
  assert.equal(T.cursor, 'definitions/sales/a.sqlx');
  assert.deepEqual(T.centre, { path: 'definitions/sales/a.sqlx', always: true });
  assert.equal(T.open.definitions, true);
  assert.equal(T.open['definitions/sales'], true);
});

test('another reveal centres only a row out of view', () => {
  T.shown = true;
  reveal('README.md');
  assert.deepEqual(T.centre, { path: 'README.md', always: false });
});

test('a hidden tree moves its cursor but asks for no scroll', () => {
  reveal('README.md');
  assert.equal(T.cursor, 'README.md');
  assert.equal(T.centre, null);
});
