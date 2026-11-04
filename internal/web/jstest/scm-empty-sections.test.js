// Source Control leaves out an empty section, but Changes: on a clean
// tree it says so, and the cursor has somewhere to go.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { G, rows, key, getDraft } from '../assets/scm.js';
import { F } from '../assets/forge.js';

const f = { path: 'a.sqlx', kind: 'M' };
const st = (more) => ({ branch: 'feat', head: 'abc', op: '', conflicts: [], staged: [], changes: [], stashes: [], ...more });
const json = (v) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  S.mode = 'project';
  getDraft(); // restore() resets G the first time
  F.v = null;
  G.open = { ...G.open, conflicts: true, staged: true, changes: true, stashes: true, commits: false };
  G.filter = '';
  G.prompt = null;
});

test('a clean tree lists Changes, saying so, and no Staged Changes or Stashes', () => {
  G.st = st();
  const rs = rows();
  assert.deepEqual(rs.map((r) => r.key), ['s:changes', 'n:changes', 's:commits']);
  assert.equal(rs[1].kind, 'note');
  assert.equal(rs[1].text, 'nothing to commit, working tree clean');
});

test('the note is no row for j and k', () => {
  G.st = st();
  G.cursor = 's:changes';
  key('j');
  assert.equal(G.cursor, 's:commits');
  key('k');
  assert.equal(G.cursor, 's:changes');
});

test('with only staged files, Changes says there is nothing to stage', () => {
  G.st = st({ staged: [f] });
  assert.deepEqual(rows().map((r) => r.key), ['s:staged', 'staged:a.sqlx', 's:changes', 'n:changes', 's:commits']);
  assert.equal(rows()[3].text, 'no changes to stage');
});

test('a section with something in it is listed; a closed Changes has no note', () => {
  G.st = st({ staged: [f], changes: [f], stashes: [{ index: 0, subject: 'WIP', date: 'd', age: '1 h' }] });
  const keys = rows().map((r) => r.key);
  for (const k of ['s:staged', 's:changes', 'changes:a.sqlx', 's:stashes', 'st:0']) assert.ok(keys.includes(k), keys.join(' '));
  assert.ok(!keys.includes('n:changes'));
  G.st = st();
  G.open.changes = false;
  assert.deepEqual(rows().map((r) => r.key), ['s:changes', 's:commits']);
});

test('once the last stash is dropped, the cursor on Stashes goes to Changes', async () => {
  G.st = st({ stashes: [{ index: 0, subject: 'WIP', date: 'd', age: '1 h' }] });
  G.cursor = 's:stashes';
  globalThis.fetch = async (url) => json(String(url).includes('/api/scm/log') ? { commits: [], more: false } : st());
  key('R'); // reads the status again: the stash is gone
  await new Promise((r) => setTimeout(r, 150));
  assert.equal(G.st.stashes.length, 0);
  assert.equal(G.cursor, 's:changes');
});
