// Multiple tabs in Scratch queries: independent SQL/editor/dry-run/results/
// filters/sort state per tab, concurrent runs across tabs, tab lifecycle
// (open, deduplicate, switch, close, close-with-running-query confirm,
// close others, delete), keyboard shortcuts, palette commands, and
// workspace save/restore.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { S, hooks, openScratchPalette, paletteList } from '../assets/state.js';
import { scratchExShort } from '../assets/scratch-editor.js';
import { exitKey } from '../assets/exit.js';
import {
  SC, activeTab, resetTabs, setEditor, openScratchView, selectScratch,
  newDraft, switchTab, nextScratchTab, switchTabIndex, closeTab, closeOtherTabs,
  deleteScratch, runScratch, cancelRun, cancelAllRuns, isRunning, anyRunning,
  confirmRun, dismissConfirm, setFilter, setSort, toggleResults, scratchKey,
  tabLabel, canCloseTab, saveScratchWorkspace, restoreScratchWorkspace,
} from '../assets/scratch.js';

const okJSON = (v) => ({ ok: true, status: 200, text: async () => JSON.stringify(v), json: async () => v });
const tick = () => new Promise((r) => setTimeout(r, 0));

function makeMockEditor(initial = '') {
  let text = initial;
  let sel = { anchor: 0, head: 0 };
  let marks = null;
  let focused = false;
  return {
    doc: () => text,
    scope: () => text,
    scopeOffset: () => 0,
    block: () => false,
    set(t) { text = t; sel = { anchor: 0, head: 0 }; },
    save() { return { doc: text, selection: { ranges: [sel], main: 0 }, history: { done: [text] } }; },
    restore(j) { text = j.doc; sel = (j.selection && j.selection.ranges && j.selection.ranges[0]) || { anchor: 0, head: 0 }; },
    mark(m) { marks = m; },
    getMarks: () => marks,
    setSelection(a, h = a) { sel = { anchor: a, head: h }; },
    getSelection: () => sel,
    focus() { focused = true; },
    blur() { focused = false; },
    normal() {},
    isFocused: () => focused,
    view: { scrollDOM: { scrollTop: 0 } },
  };
}

let files, saves, cancels, createdSeq, jobHandlers, ed;

beforeEach(() => {
  resetTabs();
  files = {
    scratch_a: { id: 'scratch_a', from: 'shop.orders', project: 'p1', location: 'EU', sql: 'SELECT * FROM orders', modified: '2026-11-23T09:00:00Z', preview: 'SELECT * FROM orders' },
    scratch_b: { id: 'scratch_b', from: 'shop.users', project: 'p2', location: 'US', sql: 'SELECT * FROM users', modified: '2026-11-23T09:01:00Z', preview: 'SELECT * FROM users' },
    scratch_c: { id: 'scratch_c', from: '', project: 'p1', location: 'EU', sql: 'SELECT 1 AS x', modified: '2026-11-23T09:02:00Z', preview: 'SELECT 1 AS x' },
  };
  saves = [];
  cancels = [];
  createdSeq = 0;
  jobHandlers = {};
  ed = makeMockEditor();
  setEditor(ed);

  S.caps = { scratch: true, warehouse: 'bigquery' };
  S.overlay = null;
  S.view = null;
  S.sel = null;
  Object.assign(SC, {
    open: true, standalone: false, focus: 'results', listOpen: false,
    delAsk: '', recentOpen: '', whOpen: false, flash: '', recent: [], defaults: { project: 'def-p', location: 'EU' },
  });

  globalThis.fetch = async (url, opts = {}) => {
    const u = String(url);
    const body = opts.body ? JSON.parse(opts.body) : null;
    if (u === '/api/scratch') return okJSON(Object.values(files));
    if (u === '/api/scratch/defaults') return okJSON({ project: 'def-p', location: 'EU', standalone: false });
    if (u === '/api/scratch/runs') return okJSON(SC.recent || []);
    if (u.startsWith('/api/scratch/file?')) {
      const id = new URL(u, 'http://x').searchParams.get('id');
      if (!files[id]) return { ok: false, status: 404, text: async () => 'not found' };
      return okJSON({ ...files[id] });
    }
    if (u === '/api/scratch/create') {
      const id = 'scratch_new' + (++createdSeq);
      files[id] = { id, from: '', project: body.project || 'def-p', location: body.location || 'EU', sql: '', modified: '2026-11-23T09:05:00Z', preview: '' };
      return okJSON({ ...files[id] });
    }
    if (u === '/api/scratch/save') {
      saves.push(body);
      if (files[body.id]) files[body.id].sql = body.sql;
      return okJSON({ ok: true });
    }
    if (u === '/api/scratch/delete') {
      delete files[body.id];
      return okJSON({ ok: true });
    }
    if (u === '/api/scratch/dryrun') {
      return okJSON({ ok: true, bytes: 100, warnBytes: 1e9, confirmBytes: 2e9, writes: false, statementType: 'SELECT' });
    }
    if (u === '/api/scratch/run') {
      const jobId = 'job_' + body.id;
      return okJSON({ project: 'p1', location: 'EU', id: jobId });
    }
    if (u.startsWith('/api/scratch/job?')) {
      const id = new URL(u, 'http://x').searchParams.get('id');
      if (jobHandlers[id]) return okJSON(await jobHandlers[id]());
      return okJSON({ state: 'DONE', statementType: 'SELECT', bytesBilled: 100 });
    }
    if (u.startsWith('/api/scratch/rows?')) {
      const id = new URL(u, 'http://x').searchParams.get('id');
      return okJSON({ schema: [{ name: 'val', type: 'STRING' }], rows: [[id]], total: 1, nextToken: '' });
    }
    if (u === '/api/scratch/cancel') {
      cancels.push(body.id);
      return okJSON({ ok: true });
    }
    return okJSON({});
  };
});

test('opening scratches creates separate tabs and deduplicates already-open ones', async () => {
  await openScratchView('scratch_a');
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(ed.doc(), 'SELECT * FROM orders');

  // Opening a second scratch opens a new tab.
  await selectScratch('scratch_b');
  assert.equal(SC.tabs.length, 2);
  assert.equal(SC.cur.id, 'scratch_b');
  assert.equal(ed.doc(), 'SELECT * FROM users');

  // Selecting scratch_a again switches to its existing tab instead of duplicating.
  await selectScratch('scratch_a');
  assert.equal(SC.tabs.length, 2);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(ed.doc(), 'SELECT * FROM orders');
});

test('opening a scratch reuses an untouched empty draft tab', async () => {
  await newDraft();
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.draft, true);
  assert.equal(canCloseTab(), false);

  await selectScratch('scratch_a');
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(canCloseTab(), true);
});

test('switching tabs preserves editor text, cursor, results, filters, sort and hideResults', async () => {
  await openScratchView('scratch_a');
  ed.set('SELECT * FROM orders WHERE id = 1');
  ed.setSelection(14, 20);
  ed.view.scrollDOM.scrollTop = 120;
  SC.run = { sql: 'SELECT 1', phase: 'done', schema: [{ name: 'a', type: 'STRING' }], rows: [['x'], ['y']] };
  setFilter(0, 'x');
  setSort(0);
  SC.sel = { r: 0, c: 0 };
  SC.scrollTop = 88;
  toggleResults();
  assert.equal(SC.hideResults, true);

  // Open a new empty tab and type different SQL.
  await newDraft();
  assert.equal(SC.tabs.length, 2);
  assert.equal(SC.cur.draft, true);
  assert.equal(ed.doc(), '');
  assert.equal(SC.run, null);
  assert.equal(SC.hideResults, false);
  assert.deepEqual(SC.filters, {});
  assert.equal(SC.sort, null);

  ed.set('SELECT 42');
  ed.setSelection(6, 9);

  // Switch back to tab 1: everything is restored!
  switchTab(SC.tabs[0].key);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(ed.doc(), 'SELECT * FROM orders WHERE id = 1');
  assert.deepEqual(ed.getSelection(), { anchor: 14, head: 20 });
  assert.equal(ed.view.scrollDOM.scrollTop, 120);
  assert.equal(SC.filters[0], 'x');
  assert.deepEqual(SC.sort, { c: 0, desc: false });
  assert.deepEqual(SC.sel, { r: 0, c: 0 });
  assert.equal(SC.scrollTop, 88);
  assert.equal(SC.hideResults, true);

  // Switch back to tab 2: its draft SQL and selection are preserved too!
  switchTab(SC.tabs[1].key);
  assert.equal(ed.doc(), 'SELECT 42');
  assert.deepEqual(ed.getSelection(), { anchor: 6, head: 9 });
});

test('queries can run concurrently across tabs and finish independently', async () => {
  let resolveA, resolveB;
  jobHandlers.job_scratch_a = () => new Promise((r) => { resolveA = r; });
  jobHandlers.job_scratch_b = () => new Promise((r) => { resolveB = r; });

  await openScratchView('scratch_a');
  const tabA = activeTab();
  await runScratch(true);
  assert.equal(isRunning(tabA), true);
  assert.equal(anyRunning(), true);

  // Open tab B while tab A is still running.
  await selectScratch('scratch_b');
  const tabB = activeTab();
  assert.equal(isRunning(tabB), false, 'tab B is not running yet');
  assert.equal(anyRunning(), true, 'tab A is still running in the background');
  assert.equal(hooks.scratchRunning(), true);

  // Run query in tab B concurrently!
  await runScratch(true);
  assert.equal(isRunning(tabB), true);
  assert.equal(isRunning(tabA), true);

  // Tab A finishes while tab B is active.
  resolveA({ state: 'DONE', statementType: 'SELECT', bytesBilled: 50 });
  await tick();
  await tick();
  assert.equal(tabA.run.phase, 'done');
  assert.deepEqual(tabA.run.rows, [['job_scratch_a']]);
  assert.equal(tabB.run.phase, 'running');

  // Tab B finishes too.
  resolveB({ state: 'DONE', statementType: 'SELECT', bytesBilled: 75 });
  await tick();
  await tick();
  assert.equal(tabB.run.phase, 'done');
  assert.deepEqual(tabB.run.rows, [['job_scratch_b']]);
  assert.equal(anyRunning(), false);
});

test('closing a tab with a running query asks confirmation and cancels on confirm', async () => {
  let resolveA;
  jobHandlers.job_scratch_a = () => new Promise((r) => { resolveA = r; });

  await openScratchView('scratch_a');
  const tabA = activeTab();
  await runScratch(true);
  await selectScratch('scratch_b');
  assert.equal(SC.tabs.length, 2);

  // Trying to close background running tab A switches to it and asks.
  const closed = await closeTab(tabA.key);
  assert.equal(closed, false);
  assert.equal(activeTab(), tabA);
  assert.equal(SC.confirm.action, 'closeTab');
  assert.equal(cancels.length, 0);

  // Dismissing keeps the tab open and running.
  dismissConfirm();
  assert.equal(SC.confirm, null);
  assert.equal(SC.tabs.length, 2);

  // Ask again and confirm: cancels job and closes tab A, switching to tab B.
  await closeTab(tabA.key);
  await confirmRun();
  assert.deepEqual(cancels, ['job_scratch_a']);
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_b');
  resolveA({ state: 'DONE', cancelled: true, error: 'cancelled' });
});

test('closing the last saved tab leaves a fresh empty draft tab', async () => {
  await openScratchView('scratch_a');
  assert.equal(SC.tabs.length, 1);
  await closeTab();
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.draft, true);
  assert.equal(ed.doc(), '');
});

test('closeOtherTabs keeps only the active tab and cancelsAllRuns cancels every running tab', async () => {
  let resolveA, resolveB;
  jobHandlers.job_scratch_a = () => new Promise((r) => { resolveA = r; });
  jobHandlers.job_scratch_b = () => new Promise((r) => { resolveB = r; });

  await openScratchView('scratch_a');
  await runScratch(true);
  await selectScratch('scratch_b');
  await runScratch(true);
  await selectScratch('scratch_c');
  assert.equal(SC.tabs.length, 3);

  // cancelAllRuns cancels both running tabs.
  await cancelAllRuns();
  assert.deepEqual(cancels.sort(), ['job_scratch_a', 'job_scratch_b']);
  resolveA({ state: 'DONE', cancelled: true, error: 'cancelled' });
  resolveB({ state: 'DONE', cancelled: true, error: 'cancelled' });
  await tick();
  await tick();

  // Now closeOtherTabs closes tabs A and B without prompting.
  await closeOtherTabs();
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_c');
});

test('keyboard shortcuts cycle, jump, open, and close tabs', async () => {
  await openScratchView('scratch_a');
  await selectScratch('scratch_b');
  await selectScratch('scratch_c');
  assert.equal(SC.cur.id, 'scratch_c');

  // [ and ] wrap around tabs.
  scratchKey(']');
  assert.equal(SC.cur.id, 'scratch_a');
  scratchKey('[');
  assert.equal(SC.cur.id, 'scratch_c');

  // Number keys 1..9 jump directly to tabs.
  scratchKey('2');
  assert.equal(SC.cur.id, 'scratch_b');
  scratchKey('1');
  assert.equal(SC.cur.id, 'scratch_a');

  // t opens a new empty draft tab.
  scratchKey('t');
  await tick();
  assert.equal(SC.tabs.length, 4);
  assert.equal(SC.cur.draft, true);

  // x closes the active tab.
  scratchKey('x');
  await tick();
  assert.equal(SC.tabs.length, 3);
  assert.equal(SC.cur.id, 'scratch_c');
});

test('j/k browses scratches in the current tab unless running, when it opens a new tab', async () => {
  await openScratchView('scratch_a');
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');

  // j replaces the idle tab with scratch_b.
  scratchKey('j');
  await tick();
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_b');

  // Start a run in scratch_b; j then opens scratch_c in a new tab so the run stays alive.
  let resolveB;
  jobHandlers.job_scratch_b = () => new Promise((r) => { resolveB = r; });
  await runScratch(true);
  scratchKey('j');
  await tick();
  assert.equal(SC.tabs.length, 2);
  assert.equal(SC.cur.id, 'scratch_c');
  assert.equal(isRunning(SC.tabs[0]), true);
  resolveB({ state: 'DONE', statementType: 'SELECT' });
  await tick();
});

test('deleting a scratch closes its open tab without disturbing other tabs', async () => {
  await openScratchView('scratch_a');
  await selectScratch('scratch_b');
  assert.equal(SC.tabs.length, 2);
  await deleteScratch('scratch_b');
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');
});

test('tabLabel names tabs from model or SQL and disambiguates duplicates', async () => {
  await newDraft();
  await newDraft();
  assert.equal(tabLabel(SC.tabs[0]), 'new scratch 1');
  assert.equal(tabLabel(SC.tabs[1]), 'new scratch 2');

  await selectScratch('scratch_a');
  assert.equal(tabLabel(activeTab()), 'shop.orders');
  await selectScratch('scratch_c');
  assert.equal(tabLabel(activeTab()), 'SELECT 1 AS x');
});

test('palette lists tab commands when multiple tabs are open', async () => {
  await openScratchView('scratch_a');
  openScratchPalette();
  let titles = paletteList().map((c) => c.title);
  assert.ok(!titles.includes('Scratch: next tab'));
  assert.ok(titles.includes('Scratch: close tab'));

  await selectScratch('scratch_b');
  openScratchPalette();
  titles = paletteList().map((c) => c.title);
  assert.ok(titles.includes('Scratch: next tab'));
  assert.ok(titles.includes('Scratch: previous tab'));
  assert.ok(titles.includes('Scratch: close tab'));
  assert.ok(titles.includes('Scratch: close other tabs'));
});

test('workspace saves and restores multiple open tabs and active index', async () => {
  await openScratchView('scratch_a');
  SC.filters = { 0: 'ord' };
  await selectScratch('scratch_b');
  await newDraft();
  switchTabIndex(1); // active is scratch_b

  const saved = saveScratchWorkspace(false);
  assert.equal(saved.tabs.length, 3);
  assert.equal(saved.activeIndex, 1);
  assert.equal(saved.tabs[0].id, 'scratch_a');
  assert.deepEqual(saved.tabs[0].filters, { 0: 'ord' });
  assert.equal(saved.tabs[1].id, 'scratch_b');
  assert.equal(saved.tabs[2].draft, true);

  resetTabs();
  SC.open = false;
  await restoreScratchWorkspace(saved);
  assert.equal(SC.open, true);
  assert.equal(SC.tabs.length, 3);
  assert.equal(SC.cur.id, 'scratch_b');
  assert.deepEqual(SC.tabs[0].filters, { 0: 'ord' });
  assert.equal(SC.tabs[2].cur.draft, true);
});

test('workspace restore remains backward-compatible with legacy single-tab state', async () => {
  resetTabs();
  SC.open = false;
  await restoreScratchWorkspace({ id: 'scratch_a', focus: 'results', listOpen: true, filters: { 0: 'hi' } });
  assert.equal(SC.open, true);
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(SC.listOpen, true);
  assert.deepEqual(SC.filters, { 0: 'hi' });
});

test('every scratch editor ex command short name registers with Vim.defineEx', () => {
  assert.ok(Object.keys(scratchExShort).length > 0);
  for (const [name, short] of Object.entries(scratchExShort)) {
    assert.doesNotThrow(() => cm.Vim.defineEx(name, short, () => {}), `defineEx(${name}, ${short})`);
  }
});

test('ctrl+q from multi-tab Scratch returns to the project without closing it', async () => {
  let closed = 0;
  const prevFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts) => {
    if (String(url).includes('/api/close')) closed++;
    return prevFetch(url, opts);
  };
  try {
    S.mode = 'project';
    S.overlay = null;
    await openScratchView('scratch_a');
    await newDraft();
    assert.equal(SC.tabs.length, 2);
    assert.equal(SC.open, true);
    assert.equal(exitKey(), true);
    await tick();
    await tick();
    assert.equal(SC.open, false);
    assert.equal(closed, 0);
  } finally {
    globalThis.fetch = prevFetch;
  }
});
