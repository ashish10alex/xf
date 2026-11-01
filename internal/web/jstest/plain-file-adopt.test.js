// A file opened as having no action (picked while the compile after a
// branch switch still ran) gets its action selected once a compile gives
// it one, whether or not the file tree is shown.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, setState, showPlainFile } from '../assets/state.js';

const file = 'definitions/marts/daily_orders.sqlx';
const id = 'p.mart.daily_orders';
let actions = {};
const asked = [];
globalThis.fetch = async (url) => {
  const u = new URL(url, 'http://127.0.0.1');
  if (u.pathname === '/api/files/action') {
    asked.push(u.searchParams.get('file'));
    return Response.json({ action: actions[u.searchParams.get('file')] || '' });
  }
  if (u.pathname === '/api/action' && u.searchParams.get('id') === id) {
    return Response.json({ id, file, anchor: id, pane: [], sql: { plain: 'select 1' }, lineage: { up: [], down: [] } });
  }
  return Response.json({});
};

const settle = () => new Promise((r) => setTimeout(r, 150));
const compiled = (n) => setState({ hasGraph: true, compiling: false, compiles: n, errors: 0 }, false);

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, sel: null, view: null, anchor: null, recent: [], back: [], plainFile: '', state: null });
  S.boot = { root: '/p', resume: {} };
  actions = {};
  asked.length = 0;
  hooks.sourceHolds = () => false;
  compiled(1);
  showPlainFile(file);
});

test('a compile that gives the plain file an action selects it', async () => {
  actions[file] = id;
  compiled(2);
  await settle();
  assert.deepEqual(asked, [file]);
  assert.equal(S.plainFile, '');
  assert.equal(S.sel, id);
});

test('a file still with no action stays shown on its own', async () => {
  compiled(2);
  await settle();
  assert.deepEqual(asked, [file]);
  assert.equal(S.plainFile, file);
  assert.equal(S.sel, null);
});

test('unsaved changes keep the file as it is', async () => {
  actions[file] = id;
  hooks.sourceHolds = () => true;
  compiled(2);
  await settle();
  assert.deepEqual(asked, []);
  assert.equal(S.plainFile, file);
});

test('a state with no new compile asks nothing', async () => {
  actions[file] = id;
  compiled(1);
  await settle();
  assert.deepEqual(asked, []);
  assert.equal(S.plainFile, file);
});
