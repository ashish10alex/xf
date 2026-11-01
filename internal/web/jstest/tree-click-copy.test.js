// The file tree: a click on a file opens it while the tree keeps the keys,
// y copies a row's absolute path, Y its path in the project, and m moves it.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { T, key, clickRow, rows } from '../assets/tree.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

let copied;
beforeEach(() => {
  copied = [];
  Object.defineProperty(globalThis, 'navigator', {
    value: { platform: 'Linux x86_64', userAgent: 'node', clipboard: { writeText: async (t) => { copied.push(t); } } },
    configurable: true,
  });
  globalThis.isSecureContext = true;
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/' };
  key('noop'); // reads the project's tree settings, once
  Object.assign(T, {
    view: 'files', shown: true, loaded: true, prompt: null, filtering: false, filter: '',
    files: [{ path: 'definitions/a.sqlx' }, { path: 'README.md' }], dirs: [], open: { definitions: true },
  });
});

const tick = () => new Promise((r) => setTimeout(r, 0));

test('y copies the absolute path, Y the path in the project', async () => {
  T.cursor = 'definitions/a.sqlx';
  assert.equal(key('y'), true);
  assert.equal(key('Y'), true);
  await tick();
  assert.deepEqual(copied, ['/r/shop/definitions/a.sqlx', 'definitions/a.sqlx']);
});

test('y on a folder copies the folder', async () => {
  T.cursor = 'definitions';
  key('y');
  await tick();
  assert.deepEqual(copied, ['/r/shop/definitions']);
});

test('a click on a file opens it and leaves the keys in the tree', async () => {
  S.focus = 'source';
  const r = rows().find((x) => x.path === 'README.md');
  clickRow(r);
  await tick();
  assert.equal(S.focus, 'tree');
  assert.equal(T.cursor, 'README.md');
  assert.equal(S.plainFile, 'README.md');
});

test('m asks where to move the row, r what to rename it to: the same box', () => {
  T.cursor = 'definitions/a.sqlx';
  assert.equal(key('m'), true);
  assert.deepEqual({ kind: T.prompt.kind, value: T.prompt.value, move: T.prompt.move }, { kind: 'rename', value: 'definitions/a.sqlx', move: true });
  T.prompt = null;
  key('r');
  assert.equal(T.prompt.move, false);
});
