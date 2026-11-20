// ctrl+` on the Checks page shows the project's shells under the report
// with the keys, and leaves the project view's log pane as it was; ⌘j
// hides them.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { takeChecksTermFocus, toggleChecksTerm } from '../assets/checks.js';

beforeEach(() => {
  Object.assign(S, { mode: 'project', checks: 'cost', checksTerm: false, checksTermFocus: false, showLog: false, logTab: 'log', focus: 'source' });
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

test('⌘j hides the terminal whether it has the keys or not', () => {
  S.checksTerm = true;
  toggleChecksTerm(true);
  assert.equal(S.checksTerm, false);
  toggleChecksTerm(true);
  assert.equal(S.checksTerm, true);
});
