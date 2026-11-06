// cmd+enter previews the selected action: its unsaved source is saved and
// compiled first, the log pane shows the preview tab, and what the server
// asks about waits for y or n.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, handleKey, hooks, nextTab } from '../assets/state.js';
import { PV, previewSelected } from '../assets/preview.js';

let posts = [];
let answer = {};
globalThis.fetch = async (url, init) => {
  const u = String(url);
  if (init && init.method === 'POST') posts.push([u, JSON.parse(init.body || '{}')]);
  const body = u.includes('/api/preview') ? answer : u.includes('/api/scratch/job') ? { state: 'RUNNING' } : {};
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
};

beforeEach(() => {
  posts = [];
  Object.assign(S, { mode: 'project', overlay: null, checks: null, inspector: null, zoomed: false, showLog: false, focus: 'source', logTab: 'log',
    sel: 'p.mart.orders', incremental: false, state: { compiling: false, compiles: 3 } });
  S.caps = { ...S.caps, history: true, terminal: true, scratch: true };
  Object.assign(PV, { run: null, ask: null, error: '', waiting: '', name: '' });
  hooks.sourceHolds = () => false;
});

test('the preview tab comes before the terminal', () => {
  const seen = [];
  for (let i = 0; i < 5; i++) { nextTab(1); seen.push(S.logTab); }
  assert.deepEqual(seen, ['jobs', 'history', 'preview', 'term', 'log']);
});

test('a preview shows the tab and follows the job, the keys staying put', async () => {
  answer = { job: { project: 'p', id: 'j1' }, sql: 'SELECT 1', name: 'mart.orders' };
  await previewSelected();
  assert.equal(S.showLog, true);
  assert.equal(S.logTab, 'preview');
  assert.equal(S.focus, 'source');
  const [url, body] = posts.find(([u]) => u.includes('/api/preview'));
  assert.ok(url.includes('/api/preview'));
  assert.deepEqual(body, { id: 'p.mart.orders', file: '', incremental: false });
  assert.equal(PV.error, '');
  assert.equal(PV.run.job.id, 'j1');
  assert.equal(PV.run.restored, undefined, 'a new job, not a recent result');
  assert.equal(PV.name, 'mart.orders');
});

test('unsaved source is saved, and the preview waits for its compile', async () => {
  let saved = false;
  hooks.sourceHolds = () => !saved;
  hooks.sourceSave = async () => { saved = true; S.state = { compiling: true, compiles: 3 }; return true; };
  answer = { job: { project: 'p', id: 'j2' }, sql: 'SELECT 2', name: 'mart.orders' };
  const done = previewSelected();
  await new Promise((r) => setTimeout(r, 30));
  assert.equal(PV.waiting, 'compiling');
  assert.ok(!posts.some(([u]) => u.includes('/api/preview')), 'not before the compile ends');
  S.state = { compiling: false, compiles: 4 };
  await done;
  assert.equal(PV.run.job.id, 'j2');
});

test('a refused save previews nothing', async () => {
  hooks.sourceHolds = () => true;
  hooks.sourceSave = async () => false;
  await previewSelected();
  assert.ok(!posts.some(([u]) => u.includes('/api/preview')));
  assert.equal(PV.waiting, '');
});

test('what the server asks takes the keys, and y previews anyway', async () => {
  answer = { ask: ['The dry run says this scans 56 GiB.'], danger: true, name: 'mart.big' };
  hooks.sourceReturn = () => {};
  await previewSelected();
  assert.ok(PV.ask);
  assert.equal(S.focus, 'log');
  answer = { job: { project: 'p', id: 'j3' }, sql: 'SELECT BIG', name: 'mart.big' };
  assert.equal(handleKey('y'), true);
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(PV.ask, null);
  assert.equal(S.focus, 'source');
  const last = posts.filter(([u]) => u.includes('/api/preview')).pop();
  assert.equal(last[1].force, true);
  assert.equal(PV.run.job.id, 'j3');
});

test('n dismisses the question and runs nothing', async () => {
  answer = { ask: ['Its pre_operations run first.'], danger: true, name: 'mart.inc' };
  await previewSelected();
  const before = posts.length;
  handleKey('n');
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(PV.ask, null);
  assert.equal(posts.length, before);
  assert.equal(PV.run, null);
});
