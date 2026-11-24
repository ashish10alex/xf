// Popping out the Checks page (Column impact / Query cost) into a separate
// side-by-side browser window, restoring transferred reports and flags,
// closing a pop-out with ctrl+q without closing the project in other windows,
// docking back into the project window, and the Checks command palette.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import {
  S, hooks, handleKey, openPalette, openChecksPalette, cancelPalette,
  paletteList, paletteCommands, checksCommands, pickFromPalette, setPaletteQuery,
  commandQuery, fileQuery, actionQuery, symbolQuery, projectQuery,
} from '../assets/state.js';
import {
  openCheck, popoutChecks, openChecksPopout, importDockedChecks, dockChecks, closeChecksPopout,
} from '../assets/checks.js';
import { exitKey, exitLevel } from '../assets/exit.js';
import { act, snapshot } from '../assets/agent.js';

const okJSON = (v) => ({ ok: true, status: 200, text: async () => JSON.stringify(v), json: async () => v });
const tick = () => new Promise((r) => setTimeout(r, 0));

const sampleCostReport = {
  what: 'working tree',
  checkedAt: '12:00',
  signature: 'sig-1',
  stale: false,
  report: { markdown: '# Cost\n- orders: 1 GB' },
};

const sampleImpactReport = {
  what: 'working tree',
  checkedAt: '12:01',
  signature: 'sig-1',
  stale: false,
  markdown: '# Impact\n- orders.id removed',
  page: { Title: 'Column impact', Verdict: 'ok', Summary: '0 breaking', Rows: [] },
};

let opened, closeCalls, closedProject, fetched, prevOpen, prevClose, prevHistory, replacedURLs;

beforeEach(() => {
  localStorage.clear();
  opened = [];
  closeCalls = 0;
  closedProject = 0;
  fetched = [];
  replacedURLs = [];

  Object.assign(S, {
    mode: 'project',
    key: 'demo_proj',
    boot: { name: 'demo_proj', root: '/demo', defaultBranch: 'main', panelPosition: 'bottom', backend: 'dataform', vimKeys: {} },
    caps: { impact: true, cost: true, branches: true, api: true, terminal: true, warehouse: 'bigquery' },
    state: { hasGraph: true, errors: 0, impactSig: 'sig-1' },
    flags: { deps: false, dependents: false, full: false, api: false },
    overlay: null,
    checks: null,
    checksPopout: false,
    checksTerm: false,
    checksTermFocus: false,
    checksTermZoomed: false,
    costProdOnly: false,
    impact: null,
    cost: null,
    inspector: null,
    flash: '',
    open: [],
    commandUses: {},
  });

  prevOpen = globalThis.window.open;
  prevClose = globalThis.window.close;
  prevHistory = globalThis.history;
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
  globalThis.history = {
    replaceState: (_s, _t, url) => {
      replacedURLs.push(String(url));
    },
  };

  globalThis.fetch = async (url, opts) => {
    const u = String(url);
    fetched.push({ url: u, body: opts && opts.body ? JSON.parse(opts.body) : null });
    if (u.startsWith('/api/cost')) return okJSON(sampleCostReport);
    if (u.startsWith('/api/impact')) return okJSON(sampleImpactReport);
    if (u.startsWith('/api/close')) { closedProject++; return okJSON({ open: [] }); }
    return okJSON({});
  };
});

test('popoutChecks moves Checks to a new window with reports and flags, and returns the original window to the project', async () => {
  try {
    await openCheck('cost');
    assert.equal(S.checks, 'cost');
    assert.equal(S.cost.report.checkedAt, '12:00');
    S.costProdOnly = true;
    S.flags.api = true;
    S.checksTerm = true;
    S.cost.api = true;
    S.cost.prodOnly = true;

    assert.equal(popoutChecks(), true);
    assert.equal(opened.length, 1);
    assert.equal(opened[0].opener, null);
    assert.match(opened[0].features, /^popup,width=\d+,height=\d+,left=-?\d+,top=-?\d+$/);
    assert.equal(S.checks, null);
    assert.equal(S.checksTermZoomed, false);

    const parsed = new URL(opened[0].url, 'http://127.0.0.1');
    assert.equal(parsed.searchParams.get('project'), 'demo_proj');
    assert.equal(parsed.searchParams.get('checks'), 'cost');
    const token = parsed.searchParams.get('handoff');
    assert.ok(token);

    // Restore in the popped-out window: report is reused without re-fetching /api/cost.
    fetched = [];
    S.cost = null;
    S.flags.api = false;
    S.costProdOnly = false;
    S.checksTerm = false;

    await openChecksPopout('cost', token);
    assert.equal(S.checks, 'cost');
    assert.equal(S.checksPopout, true);
    assert.equal(S.flags.api, true);
    assert.equal(S.costProdOnly, true);
    assert.equal(S.checksTerm, true);
    assert.equal(S.cost.report.checkedAt, '12:00');
    assert.equal(fetched.filter((f) => f.url.startsWith('/api/cost')).length, 0);

    // Switching tabs in a popped-out Checks window updates the URL so reload keeps the tab.
    await openCheck('impact');
    assert.equal(S.checks, 'impact');
    assert.ok(replacedURLs.some((u) => u.includes('checks=impact')));
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('popoutChecks fresh opens a new Checks window from the project view without changing current view', async () => {
  try {
    assert.equal(popoutChecks({ which: 'impact', fresh: true }), true);
    assert.equal(opened.length, 1);
    const parsed = new URL(opened[0].url, 'http://127.0.0.1');
    assert.equal(parsed.searchParams.get('project'), 'demo_proj');
    assert.equal(parsed.searchParams.get('checks'), 'impact');
    assert.equal(parsed.searchParams.get('handoff'), null);
    assert.equal(S.checks, null);

    // Fresh pop-out with no handoff fetches the report from the server.
    await openChecksPopout('impact', '');
    assert.equal(S.checks, 'impact');
    assert.equal(S.checksPopout, true);
    assert.equal(S.impact.report.checkedAt, '12:01');
    assert.equal(fetched.filter((f) => f.url.startsWith('/api/impact')).length, 1);
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('popoutChecks cleans up and stays open when the browser blocks the popup', async () => {
  try {
    globalThis.window.open = () => null;
    await openCheck('cost');
    assert.equal(popoutChecks(), false);
    assert.equal(S.checks, 'cost');
    assert.match(S.flash, /blocked the new window/);
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('ctrl+q in a popped-out Checks window closes only the pop-out and never the project', async () => {
  try {
    await openCheck('impact');
    S.checksPopout = true;

    assert.equal(exitLevel(), 'checksPopout');
    assert.equal(exitKey(), true);
    await tick();
    assert.equal(closeCalls, 1);
    assert.equal(closedProject, 0);

    // If window.close() does not close the tab, fall back to the project view.
    globalThis.window.close = () => { closeCalls++; };
    globalThis.window.closed = false;
    S.checks = 'impact';
    S.checksPopout = true;
    closeChecksPopout();
    assert.equal(S.checksPopout, false);
    assert.equal(S.checks, null);
    assert.ok(replacedURLs.some((u) => u === '/?project=demo_proj'));
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('dockChecks sends Checks state back to a peer project window and closes the pop-out', async () => {
  const prevDock = hooks.dockChecksToPeer;
  try {
    await openCheck('cost');
    S.checksPopout = true;
    let dockedToken = '';
    hooks.dockChecksToPeer = async (projectKey, token) => {
      assert.equal(projectKey, 'demo_proj');
      dockedToken = token;
      return true;
    };
    await dockChecks();
    assert.ok(dockedToken);
    assert.equal(closeCalls, 1);

    // Peer window imports the docked Checks state.
    S.checksPopout = false;
    S.checks = null;
    S.cost = null;
    fetched = [];
    await importDockedChecks(dockedToken);
    assert.equal(S.checks, 'cost');
    assert.equal(S.checksPopout, false);
    assert.equal(S.cost.report.checkedAt, '12:00');
    assert.equal(fetched.filter((f) => f.url.startsWith('/api/cost')).length, 0);

    // Without a peer window, dockChecks switches this window back to the project.
    hooks.dockChecksToPeer = async () => false;
    S.checks = 'cost';
    S.checksPopout = true;
    await dockChecks();
    assert.equal(S.checksPopout, false);
    assert.equal(S.checks, null);
    assert.ok(replacedURLs.some((u) => u === '/?project=demo_proj'));
  } finally {
    hooks.dockChecksToPeer = prevDock;
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('Checks command palette lists Checks commands only, adapts to popout state, and runs picks', async () => {
  try {
    // Project palette includes fresh pop-out commands for impact and cost.
    const projTitles = paletteCommands().map((c) => c.title);
    assert.ok(projTitles.includes('Check: open column impact in new window'));
    assert.ok(projTitles.includes('Check: open query cost in new window'));

    await openCheck('cost');
    assert.equal(handleKey('commands'), true);
    assert.equal(S.overlay, 'palette');
    assert.equal(S.palette.checks, true);
    assert.equal(S.palette.scratch, false);

    let titles = paletteList().map((c) => c.title);
    assert.ok(titles.includes('Checks: pop out in new window'));
    assert.ok(titles.includes('Checks: column impact'));
    assert.ok(!titles.includes('Checks: query cost'));
    assert.ok(titles.includes('Checks: check again'));
    assert.ok(titles.includes('Checks: copy report as Markdown'));
    assert.ok(titles.includes('Checks: prod only (ignore dev stand-ins)'));
    assert.ok(titles.includes('Git: compare with branch'));
    assert.ok(titles.includes('View: terminal'));
    assert.ok(titles.includes('Project: switch or add'));
    assert.ok(titles.includes('Close checks: back to the project'));
    assert.ok(!titles.includes('Checks: dock back to project'));
    assert.ok(!titles.includes('Check: compile'), 'project-only command should not appear on Checks');

    for (const q of ['>pop out', 'pop out', '  pop out']) {
      setPaletteQuery(q);
      assert.equal(commandQuery(), 'pop out', q);
      for (const f of [fileQuery, actionQuery, symbolQuery, projectQuery]) assert.equal(f(), null, q);
      assert.deepEqual(paletteList().map((c) => c.title), ['Checks: pop out in new window'], q);
    }

    // Picking pop out runs it and closes the palette.
    pickFromPalette(0);
    assert.equal(S.overlay, null);
    assert.equal(opened.length, 1);
    assert.equal(S.checks, null);

    // Popped-out Checks palette lists dock & close window, not pop out or switch project.
    S.checks = 'impact';
    S.impact = { report: sampleImpactReport, loading: false, api: false, base: 'main', token: '1' };
    S.checksPopout = true;
    openChecksPalette();
    titles = paletteList().map((c) => c.title);
    assert.ok(titles.includes('Checks: dock back to project'));
    assert.ok(titles.includes('Close checks window'));
    assert.ok(titles.includes('Checks: query cost'));
    assert.ok(!titles.includes('Checks: pop out in new window'));
    assert.ok(!titles.includes('Project: switch or add'));
    assert.ok(!titles.includes('Checks: prod only (ignore dev stand-ins)'));

    // After closing Checks palette, the project palette behaves normally again.
    cancelPalette();
    assert.equal(S.overlay, null);
    S.checks = null;
    S.checksPopout = false;
    openPalette('>');
    assert.equal(S.palette.checks, false);
    setPaletteQuery('');
    assert.equal(fileQuery(), '');
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});

test('agent exposes Checks commands, drives Checks palette, and guards closing a Checks pop-out', async () => {
  try {
    await openCheck('cost');
    const snap = snapshot();
    assert.equal(snap.checks, 'cost');
    assert.equal(snap.checksPopout, false);
    assert.ok(snap.commands.some((c) => c.title === 'Checks: pop out in new window'));

    const r = await act('palette', { query: 'column impact', pick: 0 });
    assert.equal(r.ok, true);
    assert.equal(r.picked, 'Checks: column impact');
    assert.equal(S.checks, 'impact');

    S.checksPopout = true;
    const closed = await act('closeOverlay');
    assert.equal(closed.ok, false);
    assert.match(closed.reason, /popped-out Checks/);
    assert.ok(checksCommands().some((c) => c.title === 'Close checks window'));
  } finally {
    globalThis.window.open = prevOpen;
    globalThis.window.close = prevClose;
    globalThis.history = prevHistory;
  }
});
