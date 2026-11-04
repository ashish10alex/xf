// The commit box's buttons on a clean tree: nothing to commit, so Commit
// and Commit all are dimmed, and ⌘↵ says so rather than asking git.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { G, commitState, commit, getDraft } from '../assets/scm.js';

const st = (staged, changes, op = '') => ({ branch: 'feat', head: 'abc', op, conflicts: [], staged, changes, stashes: [] });
const f = { path: 'a.sqlx', kind: 'M' };

test('a clean tree has nothing for either button', () => {
  assert.deepEqual(commitState(st([], [])), { commit: false, all: false });
});

test('a change or a staged file is something to commit', () => {
  assert.deepEqual(commitState(st([], [f])), { commit: true, all: true });
  assert.deepEqual(commitState(st([f], [])), { commit: true, all: true });
});

test('amending a clean tree can commit, and commit all too', () => {
  assert.deepEqual(commitState(st([], []), true), { commit: true, all: true });
});

test('a merge to finish can commit with nothing left to stage', () => {
  assert.deepEqual(commitState(st([], [], 'merge')), { commit: true, all: false });
});

test('before git\'s status is read, neither can', () => {
  assert.deepEqual(commitState(null, false), { commit: false, all: false });
});

test('a commit on a clean tree says so and asks git nothing', async () => {
  S.mode = 'project';
  getDraft(); // restore() resets G the first time
  const asked = [];
  globalThis.fetch = async (url) => { asked.push(String(url)); return new Response('{"ok":true}', { headers: { 'Content-Type': 'application/json' } }); };
  G.st = st([], []);
  G.amend = false;
  await commit();
  await commit({ all: true });
  assert.deepEqual(asked.filter((u) => u.includes('/api/scm/commit')), []);
  assert.equal(S.flash, 'nothing to commit, working tree clean');
  G.st = null;
});
