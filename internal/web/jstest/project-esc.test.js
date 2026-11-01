// ctrl+q goes back one level (exit.js): an overlay closes, a page goes
// back to the project, and the project closes at once unless it would
// lose something, when a confirm asks (y, enter or ctrl+q closes, any other
// key stays). esc, q and ctrl+c leave nothing.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, handleKey } from '../assets/state.js';
import { checkKey } from '../assets/checks.js';
import { exitKey } from '../assets/exit.js';
import { SET, settingsKey } from '../assets/settings.js';
import { SC } from '../assets/scratch.js';
import { T } from '../assets/theme.js';

let closed;
beforeEach(() => {
  closed = 0;
  globalThis.fetch = async (url) => {
    if (String(url).includes('/api/close')) closed++;
    const body = String(url).includes('/api/unsaved') ? { files: S.unsavedForTest || [] } : String(url).includes('/api/term/state') ? { command: S.termForTest || '' } : { open: [] };
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  S.boot = { name: 'demo' };
  Object.assign(S, { mode: 'project', overlay: null, checks: null, zoomed: false, errTab: false, rightHidden: false, flash: '', focus: 'actions', run: null, unsavedForTest: null, termForTest: '' });
  S.caps = { ...(S.caps || {}), terminal: false };
  S.cols.shown = false;
  S.cols.typing = false;
  S.sqlSearch.typing = false;
  Object.assign(SET, { open: false, closing: false, typing: false, mode: 'ui', query: '', json: {} });
  Object.assign(SC, { open: false, standalone: false });
  T.pick = null;
});

const tick = () => new Promise((r) => setTimeout(r, 0));

test('esc, q and ctrl+c leave nothing in pane 2', () => {
  for (const f of ['actions', 'sql', 'lineage']) {
    S.focus = f;
    for (const k of ['esc', 'q', 'ctrl+c']) {
      handleKey(k);
      assert.equal(S.overlay, null, f + ' ' + k);
    }
  }
});

test('esc unzooms first', () => {
  S.zoomed = true;
  handleKey('esc');
  assert.equal(S.zoomed, false);
  assert.equal(S.overlay, null);
});

test('ctrl+q closes the project at once when nothing would be lost', async () => {
  assert.equal(exitKey(), true);
  await tick();
  assert.equal(S.overlay, null);
  assert.equal(closed, 1);
});

test('ctrl+q asks when a run is in progress; ctrl+q again closes', async () => {
  S.run = { running: true };
  exitKey();
  await tick();
  assert.equal(S.overlay, 'confirmQuit');
  assert.equal(closed, 0);
  exitKey();
  await tick();
  assert.equal(S.overlay, null);
  assert.equal(closed, 1);
});

test('ctrl+q asks for unsaved changes in another tab, and y closes', async () => {
  S.unsavedForTest = ['models/a.sqlx'];
  exitKey();
  await tick();
  assert.equal(S.overlay, 'confirmQuit');
  assert.deepEqual(S.quitUnsaved, ['models/a.sqlx']);
  handleKey('y');
  await tick();
  assert.equal(closed, 1);
});

test('in the confirm any other key, esc and q included, keeps the project open', () => {
  for (const k of ['esc', 'q', 'n']) {
    S.overlay = 'confirmQuit';
    handleKey(k);
    assert.equal(S.overlay, null, k);
    assert.equal(S.mode, 'project', k);
    assert.equal(closed, 0, k);
  }
});

test('lineage has no enter jump', () => {
  S.focus = 'lineage';
  assert.equal(handleKey('enter'), false);
});

test('ctrl+q closes an overlay before anything else', () => {
  S.overlay = 'help';
  exitKey();
  assert.equal(S.overlay, null);
  assert.equal(closed, 0);
});

test('esc on the Checks page leaves nothing; ctrl+q goes back', () => {
  S.checks = 'impact';
  S.impact = {};
  checkKey('impact', 'esc');
  checkKey('impact', 'y');
  checkKey('impact', 'q');
  assert.equal(S.checks, 'impact');
  exitKey();
  assert.equal(S.checks, null);
  assert.equal(closed, 0);
});

test('esc in Settings leaves nothing; ctrl+q goes back', () => {
  Object.assign(SET, { open: true });
  settingsKey('esc');
  settingsKey('y');
  settingsKey('q');
  assert.equal(SET.open, true);
  exitKey();
  assert.equal(SET.open, false);
});

test('esc in Settings still clears the search', () => {
  Object.assign(SET, { open: true, query: 'vim' });
  settingsKey('esc');
  assert.equal(SET.query, '');
  assert.equal(SET.open, true);
});

test('ctrl+q leaves the Scratch view and not the project', async () => {
  Object.assign(SC, { open: true });
  exitKey();
  await tick();
  assert.equal(SC.open, false);
  assert.equal(closed, 0);
});

test('ctrl+q in a standalone Scratch does nothing', () => {
  Object.assign(SC, { open: true, standalone: true });
  assert.equal(exitKey(), false);
  assert.equal(SC.open, true);
});
