// A quick git command (staging, discarding) doesn't flash Source Control's
// "discarding…" row: it shows only once the command has run a while, and a
// status read that began before the command ended doesn't bring it back.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks } from '../assets/state.js';
import { G, busyShown } from '../assets/scm.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const json = (v) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });
const view = (busy) => ({ staged: [], changes: [], conflicts: [], stashes: [], busy });

let reads = [];
globalThis.fetch = async (url) => {
  if (String(url).startsWith('/api/scm/log')) return json({ commits: [], more: false });
  if (String(url) === '/api/scm') return new Promise((resolve) => reads.push(resolve));
  return json({});
};

beforeEach(async () => {
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/' };
  hooks.scmEvent({ busy: '' });
  await sleep(100);
  for (const r of reads) r(json(view('')));
  reads = [];
  await sleep(10);
});

test('a command shows as running only after a while', async () => {
  hooks.scmEvent({ busy: 'discarding' });
  assert.equal(G.busy, 'discarding');
  assert.equal(busyShown(), '');
  await sleep(320);
  assert.equal(busyShown(), 'discarding');
  hooks.scmEvent({ busy: '' });
  assert.equal(busyShown(), '');
});

test('a read begun before the command ended does not bring its busy back', async () => {
  hooks.scmEvent({ busy: 'discarding' });
  await sleep(100); // the read starts
  assert.equal(reads.length, 1);
  hooks.scmEvent({ busy: '' });
  reads.shift()(json(view('discarding'))); // the old read answers late
  await sleep(10);
  assert.equal(G.busy, '');
  await sleep(100); // the event's own read
  reads.shift()(json(view('')));
  await sleep(10);
  assert.equal(G.busy, '');
});
