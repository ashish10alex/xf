// agent.js, which an agent in the browser preview loads with xfAgent():
// the page as one object, steps through the page's own code that refuse
// what costs money or can't be undone, a wait for a state and the log.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, onRedraw, redraw } from '../assets/state.js';
import { SET } from '../assets/settings.js';
import { SC } from '../assets/scratch.js';
import { T } from '../assets/theme.js';
import { showMenu, closeMenu } from '../assets/ctx-menu.js';
import { snapshot, act, waitFor, log, danger, keyInit } from '../assets/agent.js';

let asked;
beforeEach(() => {
  asked = [];
  globalThis.fetch = async (url) => {
    asked.push(String(url));
    const u = String(url);
    const id = new URL(u, 'http://x').searchParams.get('id');
    const body = u.includes('/api/unsaved') ? { files: [] }
      : u.startsWith('/api/action?') ? { id, name: id, file: 'definitions/x.sqlx', pane: [{ id, file: 'definitions/x.sqlx' }] }
        : { open: [] };
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  S.boot = { name: 'demo', root: '/demo', sep: '/' };
  Object.assign(S, { mode: 'project', key: 'demo', overlay: null, checks: null, inspector: null, zoomed: false, flash: '', focus: 'actions', run: null, sel: null, view: null, pickerConfirm: null });
  S.caps = { terminal: false };
  Object.assign(SET, { open: false, closing: false });
  Object.assign(SC, { open: false, standalone: false, confirm: null, delAsk: '' });
  T.pick = null;
  closeMenu();
});

const titles = (s) => s.commands.map((c) => c.title);

test('a snapshot is plain JSON of what the page shows', () => {
  S.overlay = 'help';
  const s = snapshot();
  assert.deepEqual(JSON.parse(JSON.stringify(s)), s);
  assert.equal(s.mode, 'project');
  assert.equal(s.layer, 'help');
  assert.equal(s.focus, 'actions');
  assert.ok(Array.isArray(s.log) && s.log.length <= 10);
  // No commands under an overlay other than the palette.
  assert.deepEqual(s.commands, []);
});

test('the snapshot lists the commands legal now, marking the dangerous', () => {
  let s = snapshot();
  assert.ok(titles(s).includes('View: zoom pane'));
  assert.ok(!titles(s).includes('Run'));
  const close = s.commands.find((c) => c.title.startsWith('Close project'));
  assert.equal(close.dangerous, true);
  assert.equal(s.commands.find((c) => c.title === 'View: zoom pane').dangerous, false);
  S.caps.run = true;
  S.view = { id: 'a.b', name: 'b', file: 'definitions/b.sqlx', pane: [], run: { actions: ['a.b'] } };
  s = snapshot();
  assert.deepEqual(s.view, { id: 'a.b', name: 'b', file: 'definitions/b.sqlx' });
  assert.equal(s.commands.find((c) => c.title === 'Run').dangerous, true);
  assert.equal(s.commands.find((c) => c.title === 'Run option: full refresh').dangerous, true);
});

test('the top layer is the menu, then the theme picker, settings, an overlay', () => {
  SET.open = true;
  assert.equal(snapshot().layer, 'settings');
  assert.deepEqual(snapshot().commands, []);
  showMenu([{ label: 'Copy Path', run() {} }, '-', { label: 'Delete', key: 'd', off: 'busy', run() {} }], { x: 0, y: 0 });
  const s = snapshot();
  assert.equal(s.layer, 'menu');
  assert.deepEqual(s.menu, [{ label: 'Copy Path', key: '', off: '' }, { label: 'Delete', key: 'd', off: 'busy' }]);
});

test('danger names what a title would do that needs a yes', () => {
  for (const t of ['Run', 'Run with dependents', 'Run: query (the selection, else the file)', 'Run option: full refresh', 'Git: push', 'Git: commit all (stage every change first)', 'File: discard unsaved changes', 'Worktree: remove this worktree', 'Remove Worktree', 'Delete', 'Close project: back to the projects', 'Defer to prod: remove proxy views', 'Databricks: check the model on the SQL warehouse (starts it when stopped)']) {
    assert.ok(danger(t), t);
  }
  for (const t of ['Run: cancel', 'Run changed actions', 'Run option: include dependencies', 'View: zoom pane', 'Git: switch branch', 'Git: show the git output', 'Check: compile', 'Copy Path']) {
    assert.equal(danger(t), '', t);
  }
});

test('a key spec reads into the fields a KeyboardEvent has', () => {
  assert.deepEqual(keyInit('ctrl+shift+p'), { key: 'P', code: 'KeyP', shiftKey: true, ctrlKey: true, metaKey: false, altKey: false });
  assert.deepEqual(keyInit('G'), { key: 'G', code: 'KeyG', shiftKey: true, ctrlKey: false, metaKey: false, altKey: false });
  assert.equal(keyInit('esc').key, 'Escape');
  assert.equal(keyInit('cmd+s').metaKey, true);
  assert.equal(keyInit('ctrl+`').code, 'Backquote');
  assert.equal(keyInit('ctrl+1').code, 'Digit1');
  assert.equal(keyInit('space').key, ' ');
});

test('a key goes through handleKey where there is no browser', async () => {
  const r = await act('key', 'z');
  assert.equal(r.ok, true);
  assert.equal(r.used, true);
  assert.equal(S.zoomed, true);
  assert.equal(r.snapshot.panes.zoomed, true);
});

test('keys that close, quit, run or answer a confirm are refused', async () => {
  let r = await act('key', 'ctrl+q');
  assert.equal(r.ok, false);
  assert.equal(r.blocked, true);
  assert.match(r.reason, /closes the project/);

  // On a page it goes back to the project, which act allows (main.js,
  // not loaded here, does the going back).
  S.checks = 'cost';
  S.cost = { report: null };
  r = await act('key', 'ctrl+q');
  assert.notEqual(r.blocked, true);
  S.checks = null;

  S.overlay = 'confirmQuit';
  r = await act('key', 'y');
  assert.equal(r.blocked, true);
  assert.equal(S.overlay, 'confirmQuit');
  r = await act('key', 'esc');
  assert.equal(r.ok, true);
  assert.equal(S.overlay, null);

  S.caps.run = true;
  r = await act('key', 'r');
  assert.equal(r.blocked, true);
  assert.match(r.reason, /warehouse/);
  S.focus = 'source'; // r isn't a run there
  assert.equal((await act('key', 'r')).blocked, false);

  S.mode = 'picker';
  assert.equal((await act('key', 'Delete')).blocked, true);
  assert.match((await act('key', 'ctrl+c')).reason, /quits xf web/);
});

test('a command runs through the palette by a unique part of its title', async () => {
  const r = await act('command', 'zoom pane');
  assert.equal(r.ok, true);
  assert.equal(r.picked, 'View: zoom pane');
  assert.equal(S.zoomed, true);
  assert.equal(S.overlay, null);
  assert.ok(asked.includes('/api/command'), 'its use is counted, as a pick in the palette is');
});

test('an unknown or ambiguous command says so with candidates', async () => {
  let r = await act('command', 'View:');
  assert.equal(r.ok, false);
  assert.match(r.reason, /ambiguous/);
  assert.ok(r.candidates.length > 1);
  r = await act('command', 'no such thing');
  assert.match(r.reason, /nothing matches/);
  S.overlay = 'help';
  assert.match((await act('command', 'zoom pane')).reason, /no commands here/);
});

test('a dangerous command is refused unless allowDangerous', async () => {
  let r = await act('command', 'Close project');
  assert.equal(r.ok, false);
  assert.equal(r.blocked, true);
  assert.match(r.reason, /closes the project/);
  assert.ok(!asked.some((u) => u.includes('/api/close')));
  assert.ok(log(5).some((l) => /agent command Close project → blocked/.test(l)), 'noted in the page log');

  r = await act('command', 'Close project', { allowDangerous: true });
  assert.equal(r.ok, true);
  await waitFor(() => asked.some((u) => u.includes('/api/close')), 1000);
});

test('the palette opens on a query and picks from what it lists', async () => {
  let r = await act('palette', { query: '>zoom' });
  assert.equal(r.ok, true);
  assert.equal(r.snapshot.palette.query, '>zoom');
  assert.ok(r.snapshot.palette.items.includes('View: zoom pane'));
  r = await act('palette', { pick: 'View: zoom pane' });
  assert.equal(r.ok, true);
  assert.equal(S.zoomed, true);
  assert.equal(S.overlay, null);

  r = await act('palette', { query: '>close project', pick: 0 });
  assert.equal(r.blocked, true);
  assert.equal(S.overlay, 'palette');
  assert.equal((await act('closeOverlay')).closed, 'palette');
  assert.equal(S.overlay, null);
});

test('the palette waits for async action search before picking', async () => {
  S.state = { hasGraph: true };
  globalThis.fetch = async (url) => {
    const u = String(url);
    if (u.includes('/api/find')) {
      await new Promise((res) => setTimeout(res, 15));
      return new Response(JSON.stringify({ items: [{ id: 'a.orders', name: 'orders', file: 'definitions/orders.sqlx' }], found: 1, total: 1 }), { headers: { 'Content-Type': 'application/json' } });
    }
    const id = new URL(u, 'http://x').searchParams.get('id');
    const body = u.startsWith('/api/action?') ? { id, name: 'orders', file: 'definitions/orders.sqlx', pane: [{ id, file: 'definitions/orders.sqlx' }] } : { open: [] };
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  const r = await act('palette', { query: '#orders', pick: 0 });
  assert.equal(r.ok, true);
  assert.equal(r.picked, 'orders');
  assert.equal(S.sel, 'a.orders');
});

test('selectAction selects through the page, and focus moves the keys', async () => {
  let r = await act('selectAction', 'a.orders');
  assert.equal(r.ok, true);
  assert.equal(S.sel, 'a.orders');
  assert.equal((await waitFor((s) => s.view && s.view.id === 'a.orders', 1000)).selected, 'a.orders');
  r = await act('focus', 'sql');
  assert.equal(r.ok, true);
  assert.equal(S.focus, 'sql');
  S.mode = 'picker';
  assert.equal((await act('selectAction', 'a.x')).ok, false);
});

test('closeOverlay closes the top layer, and never the project', async () => {
  assert.match((await act('closeOverlay')).reason, /nothing to close/);
  S.overlay = 'help';
  let r = await act('closeOverlay');
  assert.equal(r.closed, 'help');
  assert.equal(S.overlay, null);
  // confirmQuit's esc keeps the project.
  S.overlay = 'confirmQuit';
  await act('closeOverlay');
  assert.equal(S.overlay, null);
  assert.ok(!asked.some((u) => u.includes('/api/close')));
  SC.open = true;
  SC.standalone = true;
  assert.equal((await act('closeOverlay')).ok, false);
  SC.open = false;
  showMenu([{ label: 'Copy Path', run() {} }], { x: 0, y: 0 });
  r = await act('closeOverlay');
  assert.equal(r.closed, 'menu');
  assert.equal(snapshot().menu, null);
});

test('a menu row is picked by its label, a dangerous or dimmed one refused', async () => {
  const ran = [];
  showMenu([
    { label: 'Copy Path', run: () => ran.push('copy') },
    { label: 'Copy Relative Path', run: () => ran.push('rel') },
    { label: 'Rename…', off: 'busy', run: () => ran.push('rename') },
    { label: 'Delete', run: () => ran.push('delete') },
  ], { x: 0, y: 0 });
  assert.match((await act('pickMenu', 'Copy')).reason, /ambiguous/);
  assert.equal((await act('pickMenu', 'Delete')).blocked, true);
  assert.match((await act('pickMenu', 'Rename')).reason, /busy/);
  const r = await act('pickMenu', 'Copy Path');
  assert.equal(r.ok, true);
  assert.deepEqual(ran, ['copy']);
  assert.equal(snapshot().menu, null);
  assert.match((await act('pickMenu', 'Copy Path')).reason, /no menu open/);
});

test('an unknown op says which there are', async () => {
  const r = await act('fly');
  assert.equal(r.ok, false);
  assert.match(r.reason, /key, command, palette/);
});

test('waitFor resolves on the draw that makes it true, beside onRedraw', async () => {
  let draws = 0;
  onRedraw(() => { draws++; });
  const p = waitFor((s) => s.flash === 'done', 1000);
  S.flash = 'done';
  redraw();
  const s = await p;
  assert.equal(s.flash, 'done');
  assert.ok(draws >= 1, "main.js's draw still runs");
  onRedraw(() => {});
});

test('waitFor resolves without a draw, as in a hidden tab', async () => {
  const p = waitFor((s) => s.focus === 'log', 1000);
  setTimeout(() => { S.focus = 'log'; }, 20);
  assert.equal((await p).focus, 'log');
});

test('waitFor resolves at once when it already holds', async () => {
  const s = await waitFor((x) => x.mode === 'project', 50);
  assert.equal(s.mode, 'project');
});

test('waitFor rejects on timeout saying where the page was', async () => {
  await assert.rejects(waitFor((s) => s.palette.query === 'x', 150), (err) => {
    assert.match(err.message, /not after 150ms \(mode project, layer none, focus actions/);
    assert.match(err.message, /the predicate threw/);
    return true;
  });
});

test('log gives the page log\'s last n lines', async () => {
  await act('focus', 'sql');
  const lines = log(3);
  assert.equal(lines.length, 3);
  assert.match(lines.at(-1), /agent focus sql → ok$/);
  assert.ok(log().length <= 50);
});
