// autoHidePane2 (on by default) hides pane 2 while the source pane shows a
// file with no action, such as a .py script or .gitlab-ci.yml; ⌥⌘b shows
// it for that file alone.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, rightShown, toggleRight, showPlainFile, showErrorsTab } from '../assets/state.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, zoomed: false, rightHidden: false, rightPeek: '', flash: '', focus: 'source', sel: null, view: null, plainFile: '', errHidden: false, errTab: false, errors: { groups: [] }, state: { errors: 0 } });
  S.boot = { root: '/p', autoHidePane2: true };
  hooks.sourceShownNow = () => true;
});

test('pane 2 hides beside a file with no action', () => {
  assert.equal(rightShown(), true);
  showPlainFile('.gitlab-ci.yml');
  assert.equal(rightShown(), false);
});

test('pane 2 stays when the setting is off', () => {
  S.boot.autoHidePane2 = false;
  showPlainFile('scripts/load.py');
  assert.equal(rightShown(), true);
});

test('pane 2 shows again beside an action', () => {
  showPlainFile('scripts/load.py');
  S.plainFile = '';
  S.view = { id: 'a', file: 'definitions/a.sqlx' };
  assert.equal(rightShown(), true);
});

test('the keys leave pane 2 as it hides', () => {
  S.focus = 'sql';
  S.zoomed = true;
  showPlainFile('scripts/load.py');
  assert.equal(S.focus, 'source');
  assert.equal(S.zoomed, false);
});

test('⌥⌘b shows pane 2 for that file alone, and hides it again', () => {
  showPlainFile('scripts/load.py');
  toggleRight();
  assert.equal(rightShown(), true);
  assert.equal(S.rightHidden, false);
  showPlainFile('.gitlab-ci.yml');
  assert.equal(rightShown(), false, 'another file hides it again');
  toggleRight();
  toggleRight();
  assert.equal(rightShown(), false);
  assert.equal(S.rightHidden, false, 'the choice kept for actions stays');
});

test('⌥⌘b beside a file with no action shows a pane 2 hidden by hand', () => {
  S.rightHidden = true;
  showPlainFile('scripts/load.py');
  toggleRight();
  assert.equal(rightShown(), true);
});

// A failing compile has no actions, so every file is one with no action:
// pane 2 stays for its errors tab.
test('pane 2 shows while the compile fails, beside any file', () => {
  S.state = { errors: 1 };
  showPlainFile('definitions/a.sqlx');
  assert.equal(rightShown(), true);
});

test('pane 2 hidden by hand shows while the compile fails', () => {
  S.rightHidden = true;
  S.state = { errors: 1 };
  assert.equal(rightShown(), true);
  S.state = { errors: 0 };
  assert.equal(rightShown(), false, 'the choice comes back with a clean compile');
});

test('⌥⌘b hides pane 2 for the failure alone', () => {
  S.state = { errors: 1 };
  S.focus = 'sql';
  toggleRight();
  assert.equal(rightShown(), false);
  assert.equal(S.focus, 'source', 'the keys leave pane 2');
  assert.equal(S.rightHidden, false, 'the choice kept for a clean compile stays');
  toggleRight();
  assert.equal(rightShown(), true);
});

test('e, or a click on the errors, shows a hidden pane 2', () => {
  S.state = { errors: 1 };
  toggleRight();
  showErrorsTab();
  assert.equal(rightShown(), true);
  assert.equal(S.focus, 'sql');
  assert.equal(S.errTab, true);
});
