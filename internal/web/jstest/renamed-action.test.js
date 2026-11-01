// An edit to a file's config that renames its action (a new schema, say)
// keeps the action selected under its new name, rather than leaving
// nothing selected once the compile no longer has the old one.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, selectAction, refreshView } from '../assets/state.js';
import { BUF } from '../assets/buffers.js';

const oldID = 'p.mart_old.orders';
const newID = 'p.mart.orders';
const file = 'definitions/orders.sqlx';

const asked = [];
globalThis.fetch = async (url) => {
  const u = new URL(url, 'http://127.0.0.1');
  if (u.pathname !== '/api/action') return new Response('{}', { headers: { 'Content-Type': 'application/json' } });
  asked.push(u);
  const id = u.searchParams.get('id');
  if (id !== newID && u.searchParams.get('file') !== file) return new Response('no such action\n', { status: 404 });
  const v = { id: newID, file, anchor: newID, sql: { plain: 'select 1' }, lineage: { up: [], down: [] } };
  return new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });
};

const settle = () => new Promise((r) => setTimeout(r, 150));

beforeEach(() => {
  asked.length = 0;
  Object.assign(S, { mode: 'project', overlay: null, sel: null, view: null, anchor: null, recent: [], back: [], plainFile: '' });
  BUF.root = (S.boot && S.boot.root) || '';
  BUF.list = [];
});

test('a recompile that renames the shown action selects it under its new name', async () => {
  S.sel = oldID;
  S.anchor = oldID;
  S.recent = [oldID, 'p.x.other'];
  S.view = { id: oldID, file, anchor: oldID, sql: { plain: 'select 1' }, lineage: { up: [], down: [] } };
  refreshView();
  await settle();
  assert.equal(asked[0].searchParams.get('file'), file);
  assert.equal(S.sel, newID);
  assert.equal(S.view.id, newID);
  assert.equal(S.anchor, newID);
  assert.deepEqual(S.recent, [newID, 'p.x.other']);
});

test('selecting an old name its buffer kept finds the action its file defines now', async () => {
  BUF.list = [{ n: 1, file, action: oldID }];
  selectAction(oldID);
  await settle();
  assert.equal(asked[0].searchParams.get('file'), file);
  assert.equal(S.sel, newID);
});

test('an action gone with no file to follow leaves nothing selected', async () => {
  selectAction('p.gone.x');
  await settle();
  assert.equal(asked[0].searchParams.get('file'), null);
  assert.equal(S.sel, null);
  assert.equal(S.view, null);
});

test('a file picked by an action a compile since renamed selects the action it defines now', async () => {
  selectAction(oldID, { file });
  await settle();
  assert.equal(asked[0].searchParams.get('file'), file);
  assert.equal(S.sel, newID);
  assert.equal(S.view.id, newID);
});

test('a file picked by an action that is gone opens with nothing selected', async () => {
  selectAction('p.gone.x', { file: 'definitions/gone.sqlx' });
  await settle();
  assert.equal(S.sel, null);
  assert.equal(S.view, null);
  assert.equal(S.plainFile, 'definitions/gone.sqlx');
});
