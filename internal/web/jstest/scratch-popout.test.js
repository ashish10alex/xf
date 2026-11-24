// Popping out the Scratch view (or an individual Scratch tab) into a separate
// side-by-side browser window, restoring transferred tabs and in-flight runs,
// closing a pop-out with ctrl+q without closing the project in other windows,
// and docking tabs back into the project window.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, openScratchPalette, paletteList } from '../assets/state.js';
import { exitKey, exitLevel } from '../assets/exit.js';
import { clearSessionWorkspace } from '../assets/workspace.js';
import {
  SC, activeTab, resetTabs, setEditor, openScratchView, selectScratch,
  newDraft, runScratch, popoutScratch, popoutFeatures, openScratchPopout,
  importDockedTabs, dockScratch, closePopout, isRunning,
} from '../assets/scratch.js';

const okJSON = (v) => ({ ok: true, status: 200, text: async () => JSON.stringify(v), json: async () => v });
const tick = () => new Promise((r) => setTimeout(r, 0));

function makeMockEditor(initial = '') {
  let text = initial;
  return {
    doc: () => text,
    scope: () => text,
    scopeOffset: () => 0,
    block: () => false,
    set: (s) => { text = s; },
    save: () => ({ doc: text, selection: { main: 0, ranges: [{ anchor: text.length, head: text.length }] } }),
    restore: (st) => { text = st.doc; },
    mark: () => {},
    focus: () => {},
    blur: () => {},
    normal: () => {},
    view: { scrollDOM: { scrollTop: 0 } },
  };
}

let files, opened, closeCalls, cancelledJobs, closedProject, prevOpen, prevClose;

beforeEach(() => {
  localStorage.clear();
  resetTabs();
  opened = [];
  closeCalls = 0;
  cancelledJobs = [];
  closedProject = 0;
  files = {
    scratch_a: { id: 'scratch_a', from: 'shop.orders', project: 'proj-a', location: 'EU', sql: 'SELECT * FROM orders' },
    scratch_b: { id: 'scratch_b', from: 'shop.users', project: 'proj-b', location: 'US', sql: 'SELECT * FROM users' },
  };
  S.mode = 'project';
  S.key = 'demo_proj';
  S.boot = { name: 'demo_proj' };
  S.caps = { scratch: true, warehouse: 'bigquery', terminal: false };
  S.overlay = null;
  S.checks = null;
  S.inspector = null;
  S.flash = '';
  Object.assign(SC, {
    open: false, standalone: false, popout: false, list: [], listCursor: 0,
    delAsk: '', focus: 'results', newForm: null, flash: '', listOpen: false,
    defaults: null, recent: [], recentOpen: '', whOpen: false,
  });
  setEditor(makeMockEditor());

  prevOpen = globalThis.window.open;
  prevClose = globalThis.window.close;
  globalThis.window.closed = false;
  globalThis.window.open = (url, target, features) => {
    const win = { url, target, features, opener: globalThis.window };
    opened.push(win);
    return win;
  };
  globalThis.window.close = () => {
    closeCalls++;
    globalThis.window.closed = true;
  };

  globalThis.fetch = async (url, opts) => {
    const u = String(url);
    const body = opts && opts.body ? JSON.parse(opts.body) : null;
    if (u.startsWith('/api/scratch/defaults')) return okJSON({ project: 'def-proj', location: 'EU' });
    if (u.startsWith('/api/scratch/runs')) return okJSON([]);
    if (u.startsWith('/api/scratch/file?id=')) {
      const id = decodeURIComponent(u.split('id=')[1]);
      return okJSON({ ...files[id] });
    }
    if (u === '/api/scratch') return okJSON(Object.values(files).map((f) => ({ id: f.id, from: f.from, modified: '2026-11-24T10:00:00Z', preview: f.sql })));
    if (u.startsWith('/api/scratch/dryrun')) return okJSON({ ok: true, bytes: 100, warnBytes: 1e9, confirmBytes: 2e9 });
    if (u.startsWith('/api/scratch/save')) {
      if (files[body.id]) files[body.id].sql = body.sql;
      return okJSON({ ok: true });
    }
    if (u.startsWith('/api/scratch/run')) return okJSON({ project: 'proj-a', id: 'job_popout_1', location: 'EU' });
    if (u.startsWith('/api/scratch/job')) return okJSON({ state: 'RUNNING' });
    if (u.startsWith('/api/scratch/cancel')) { cancelledJobs.push(body.id); return okJSON({ ok: true }); }
    if (u.startsWith('/api/close')) { closedProject++; return okJSON({ open: [] }); }
    return okJSON({});
  };
});

test('popoutScratch moves all tabs to a new window and returns the original window to the project', async () => {
  await openScratchView('scratch_a');
  SC.filters = { 0: 'paid' };
  SC.sort = { c: 0, desc: true };
  SC.run = { sql: 'SELECT * FROM orders', phase: 'done', schema: [{ name: 'id', type: 'INT64' }], rows: [['1']], total: 1, next: '', children: [] };
  await selectScratch('scratch_b');
  const ed = makeMockEditor('SELECT * FROM users WHERE active = true');
  setEditor(ed);

  assert.equal(popoutScratch(), true);
  assert.equal(opened.length, 1);
  assert.equal(opened[0].opener, null);
  assert.match(opened[0].features, /^popup,width=\d+,height=\d+,left=-?\d+,top=-?\d+$/);
  assert.equal(SC.open, false);
  assert.equal(SC.tabs.length, 0);

  const parsed = new URL(opened[0].url, 'http://127.0.0.1');
  assert.equal(parsed.searchParams.get('project'), 'demo_proj');
  assert.equal(parsed.searchParams.get('scratch'), 'popout');
  const token = parsed.searchParams.get('handoff');
  assert.ok(token);

  // Restore in the popped-out window.
  await openScratchPopout(token);
  assert.equal(SC.open, true);
  assert.equal(SC.popout, true);
  assert.equal(SC.tabs.length, 2);
  assert.equal(SC.cur.id, 'scratch_b');
  assert.equal(SC.cur.sql, 'SELECT * FROM users WHERE active = true');
  assert.equal(SC.tabs[0].cur.id, 'scratch_a');
  assert.deepEqual(SC.tabs[0].filters, { 0: 'paid' });
  assert.deepEqual(SC.tabs[0].sort, { c: 0, desc: true });
  assert.deepEqual(SC.tabs[0].run.rows, [['1']]);
});

test('popping out a single tab leaves remaining tabs in place and keeps a running job alive', async () => {
  let resolveJob;
  const baseFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts) => {
    if (String(url).startsWith('/api/scratch/job')) {
      return new Promise((r) => { resolveJob = () => r(okJSON({ state: 'DONE', statementType: 'SELECT' })); });
    }
    if (String(url).startsWith('/api/scratch/rows')) {
      return okJSON({ schema: [{ name: 'n', type: 'INT64' }], rows: [['42']], total: 1 });
    }
    return baseFetch(url, opts);
  };

  await openScratchView('scratch_a');
  await runScratch(true);
  assert.equal(isRunning(SC.tabs[0]), true);
  const tabAKey = SC.tabs[0].key;
  await selectScratch('scratch_b');
  assert.equal(SC.tabs.length, 2);

  // Drag out tab A while it is running.
  assert.equal(popoutScratch({ tabKey: tabAKey }), true);
  assert.equal(cancelledJobs.length, 0);
  assert.equal(SC.open, true);
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_b');

  // New window picks up tab A and resumes polling its running job.
  const token = new URL(opened[0].url, 'http://127.0.0.1').searchParams.get('handoff');
  resetTabs();
  SC.open = false;
  await openScratchPopout(token);
  assert.equal(SC.popout, true);
  assert.equal(SC.tabs.length, 1);
  assert.equal(SC.cur.id, 'scratch_a');
  assert.equal(isRunning(SC.tabs[0]), true);
  resolveJob();
  await tick();
  await tick();
  assert.equal(SC.tabs[0].run.phase, 'done');
  assert.deepEqual(SC.tabs[0].run.rows, [['42']]);
});

test('ctrl+q in a popped-out Scratch window closes only the pop-out and never the project', async () => {
  await openScratchView('scratch_a');
  SC.popout = true;

  assert.equal(exitLevel(), 'popout');
  assert.equal(exitKey(), true);
  await tick();
  await tick();
  assert.equal(closeCalls, 1);
  assert.equal(closedProject, 0);
});

test('ctrl+q in a popped-out Scratch window with a running query asks first, then cancels and closes', async () => {
  await openScratchView('scratch_a');
  SC.popout = true;
  SC.tabs[0].run = { phase: 'running', job: { project: 'proj-a', id: 'job_live' }, sql: 'SELECT 1' };

  assert.equal(exitKey(), true);
  await tick();
  assert.equal(SC.confirm.action, 'closePopout');
  assert.equal(closeCalls, 0);
  assert.equal(closedProject, 0);

  // Second ctrl+q confirms, cancels the running query, and closes only the pop-out.
  assert.equal(exitKey(), true);
  await tick();
  await tick();
  assert.deepEqual(cancelledJobs, ['job_live']);
  assert.equal(closeCalls, 1);
  assert.equal(closedProject, 0);
});

test('dockScratch sends tabs back to a peer project window and closes the pop-out', async () => {
  await openScratchView('scratch_a');
  SC.popout = true;
  let dockedToken = '';
  const prevDock = hooks.dockScratchToPeer;
  hooks.dockScratchToPeer = async (projectKey, token) => {
    assert.equal(projectKey, 'demo_proj');
    dockedToken = token;
    return true;
  };
  try {
    await dockScratch();
    assert.ok(dockedToken);
    assert.equal(closeCalls, 1);

    // Peer window imports the docked tabs.
    SC.popout = false;
    SC.open = false;
    resetTabs();
    await importDockedTabs(dockedToken);
    assert.equal(SC.open, true);
    assert.equal(SC.tabs.length, 1);
    assert.equal(SC.cur.id, 'scratch_a');
  } finally {
    hooks.dockScratchToPeer = prevDock;
  }
});

test('popoutScratch cleans up and stays open when the browser blocks the popup', async () => {
  globalThis.window.open = () => null;
  await openScratchView('scratch_a');
  assert.equal(popoutScratch(), false);
  assert.equal(SC.open, true);
  assert.equal(SC.tabs.length, 1);
  assert.match(SC.flash, /blocked the new window/);
});

test('popoutFeatures places side-by-side by default and preserves multi-monitor drop coordinates', () => {
  const side = popoutFeatures(null, {
    screen: { availLeft: 0, availTop: 0, availWidth: 1600, availHeight: 1000 },
    window: { screenX: 0, screenY: 0, outerWidth: 800, outerHeight: 900 },
  });
  assert.match(side, /left=808,/);

  const otherMonitor = popoutFeatures({ screenX: 2200, screenY: 200 }, {
    screen: { availLeft: 0, availTop: 0, availWidth: 1600, availHeight: 1000 },
    window: { screenX: 0, screenY: 0, outerWidth: 800, outerHeight: 900 },
  });
  assert.match(otherMonitor, /left=1816,top=176/);
});

test('palette lists pop-out commands in project Scratch and dock command in pop-out Scratch', async () => {
  await openScratchView('scratch_a');
  openScratchPalette();
  let titles = paletteList().map((c) => c.title);
  assert.ok(titles.includes('Scratch: pop out in new window'));
  assert.ok(titles.includes('Scratch: move tab to new window'));

  SC.popout = true;
  openScratchPalette();
  titles = paletteList().map((c) => c.title);
  assert.ok(titles.includes('Scratch: dock back to project'));
  assert.ok(titles.includes('Close scratch window'));
  assert.ok(!titles.includes('Scratch: pop out in new window'));
});

test('clearSessionWorkspace removes inherited workspace and unsaved keys', () => {
  const backing = new Map([
    ['xf-web-unsaved', '{"/repo":["a.sqlx"]}'],
    ['xf-web-ws:/repo', '{"v":1}'],
    ['other-key', 'keep'],
  ]);
  const prevSS = globalThis.sessionStorage;
  globalThis.sessionStorage = {
    get length() { return backing.size; },
    key: (i) => [...backing.keys()][i] ?? null,
    getItem: (k) => (backing.has(k) ? backing.get(k) : null),
    setItem: (k, v) => { backing.set(k, String(v)); },
    removeItem: (k) => { backing.delete(k); },
  };
  try {
    clearSessionWorkspace();
    assert.equal(backing.has('xf-web-unsaved'), false);
    assert.equal(backing.has('xf-web-ws:/repo'), false);
    assert.equal(backing.get('other-key'), 'keep');
  } finally {
    globalThis.sessionStorage = prevSS;
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
  }
});
