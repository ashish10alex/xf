// ctrl+` on the Checks page shows the project's shells under the report
// with the keys, and leaves the project view's log pane as it was; ⌘j
// hides them, and ⌘z / alt+z zooms them to fill the page.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { checkKey, closeChecks, openCheck, takeChecksTermFocus, toggleChecksTerm, toggleChecksTermZoom } from '../assets/checks.js';
import { closeTerm, setTerms } from '../assets/log-tabs.js';

beforeEach(() => {
  Object.assign(S, {
    mode: 'project', caps: { impact: true, cost: true, terminal: true }, boot: { defaultBranch: 'main' },
    state: {}, flags: { api: false }, impact: null, cost: null,
    checks: 'cost', checksTerm: false, checksTermFocus: false, checksTermZoomed: false,
    showLog: false, logTab: 'log', focus: 'source', zoomed: false,
  });
});

test('ctrl+` shows the terminal with the keys, once, and leaves the log pane alone', () => {
  toggleChecksTerm();
  assert.equal(S.checksTerm, true);
  assert.equal(takeChecksTermFocus(), true);
  assert.equal(takeChecksTermFocus(), false);
  assert.equal(S.showLog, false);
  assert.equal(S.logTab, 'log');
  assert.equal(S.focus, 'source');
});

test('ctrl+` while the terminal shows without the keys gives it them', () => {
  S.checksTerm = true;
  toggleChecksTerm();
  assert.equal(S.checksTerm, true);
  assert.equal(takeChecksTermFocus(), true);
});

test('⌘j hides the terminal whether it has the keys or not, and clears its zoom', () => {
  S.checksTerm = true;
  S.checksTermZoomed = true;
  toggleChecksTerm(true);
  assert.equal(S.checksTerm, false);
  assert.equal(S.checksTermZoomed, false);
  toggleChecksTerm(true);
  assert.equal(S.checksTerm, true);
  assert.equal(S.checksTermZoomed, false);
});

test('⌘z / alt+z zooms and unzooms the Checks terminal without touching the project view', () => {
  S.checksTerm = true;
  toggleChecksTermZoom();
  assert.equal(S.checksTerm, true);
  assert.equal(S.checksTermZoomed, true);
  assert.equal(takeChecksTermFocus(), true);
  assert.equal(S.zoomed, false);
  assert.equal(S.showLog, false);
  assert.equal(S.focus, 'source');

  toggleChecksTermZoom();
  assert.equal(S.checksTerm, true);
  assert.equal(S.checksTermZoomed, false);
  assert.equal(takeChecksTermFocus(), true);
});

test('leaving Checks, switching tabs or esc unzooms, while rechecking the same tab keeps it zoomed', () => {
  globalThis.fetch = async () => new Response(JSON.stringify({}), { headers: { 'Content-Type': 'application/json' } });

  S.checksTerm = true;
  S.checksTermZoomed = true;
  checkKey('cost', 'esc');
  assert.equal(S.checksTermZoomed, false);

  S.checksTermZoomed = true;
  openCheck('cost', true); // background recheck of the shown tab
  assert.equal(S.checksTermZoomed, true);

  openCheck('impact'); // switching to the other tab reveals it
  assert.equal(S.checks, 'impact');
  assert.equal(S.checksTermZoomed, false);

  S.checksTermZoomed = true;
  closeChecks();
  assert.equal(S.checks, null);
  assert.equal(S.checksTermZoomed, false);
});

test('closing the last shell on Checks hides the terminal and clears its zoom', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ ids: [] }), { headers: { 'Content-Type': 'application/json' } });
  setTerms({ ids: [1] });
  S.checksTerm = true;
  S.checksTermZoomed = true;
  await closeTerm(1);
  assert.equal(S.checksTerm, false);
  assert.equal(S.checksTermZoomed, false);
});
