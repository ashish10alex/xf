// Source Control's Graph reloads when refs change (such as after git push,
// even without an upstream), when reopened after a push while collapsed, and
// on manual R refresh, but does not reload on working-tree edits alone.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks } from '../assets/state.js';
import { G, toggle, key } from '../assets/scm.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const json = (v) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });

let status;
let commits;
let logCalls;

globalThis.fetch = async (url) => {
  const u = String(url);
  if (u.startsWith('/api/scm/log')) {
    logCalls++;
    return json({ commits, more: false });
  }
  if (u === '/api/scm') return json(status);
  return json({});
};

beforeEach(async () => {
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/graph-refresh', sep: '/' };
  logCalls = 0;
  status = {
    branch: 'feat', head: 'aaaa111', upstream: '', ahead: 0, behind: 0, refs: 'r1',
    staged: [], changes: [], conflicts: [], stashes: [], busy: '',
  };
  commits = [{ sha: 'aaaa111', short: 'aaaa111', subject: 'work', author: 'A', refs: [{ name: 'feat', kind: 'head' }] }];
  G.open.commits = true;
  G.logLoaded = false;
  G.logHead = '';
  hooks.scmEvent({ busy: '' });
  await sleep(120);
});

test('git push that changes refs reloads the graph even when upstream and ahead are unchanged', async () => {
  assert.equal(logCalls, 1);

  // A working-tree edit leaves refs unchanged: no redundant log reload.
  status = { ...status, changes: [{ path: 'a.sqlx', file: 'a.sqlx', kind: 'M' }] };
  hooks.scmEvent({ busy: '' });
  await sleep(120);
  assert.equal(logCalls, 1);

  // Pushing without -u keeps upstream="" and ahead=0, but updates refs.
  status = { ...status, refs: 'r2' };
  commits = [{
    sha: 'aaaa111', short: 'aaaa111', subject: 'work', author: 'A',
    refs: [{ name: 'feat', kind: 'head' }, { name: 'origin/feat', kind: 'remote' }],
  }];
  hooks.scmEvent({ busy: '' });
  await sleep(120);
  assert.equal(logCalls, 2);
  assert.equal(G.commits[0].refs.length, 2);
});

test('reopening Graph after a push while collapsed reloads commits', async () => {
  assert.equal(logCalls, 1);
  toggle({ kind: 'section', id: 'commits', open: true }, false);
  assert.equal(G.open.commits, false);

  status = { ...status, upstream: 'origin/feat', refs: 'r3' };
  commits = [{
    sha: 'aaaa111', short: 'aaaa111', subject: 'work', author: 'A',
    refs: [{ name: 'feat', kind: 'head' }, { name: 'origin/feat', kind: 'remote' }],
  }];
  hooks.scmEvent({ busy: '' });
  await sleep(120);
  assert.equal(logCalls, 1, 'does not fetch while collapsed');

  toggle({ kind: 'section', id: 'commits', open: false }, true);
  await sleep(20);
  assert.equal(logCalls, 2, 'reloads on reopen when status changed while collapsed');
  assert.equal(G.commits[0].refs.length, 2);
});

test('manual R refresh reloads the graph even when refs are unchanged', async () => {
  assert.equal(logCalls, 1);
  key('R');
  await sleep(120);
  assert.equal(logCalls, 2);
});
