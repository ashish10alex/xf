// <leader>nt (vimFileTree) toggles the file tree when vimMode is on:
// in the editor (via vimKey / setVimKeys) and outside it (in the file
// tree itself and other project panes via handleKey).

import { test, beforeEach, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, handleKey, vimKeyTokens } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { T, key as treeKey } from '../assets/tree.js';
import { setVimMode, setVimKeys, vimKey } from '../assets/editor.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

let origSourceShownNow, origScmKey;

beforeEach(() => {
  S.mode = 'project';
  S.overlay = null;
  S.checks = null;
  S.inspector = null;
  S.sqlSearch.typing = false;
  S.cols.typing = false;
  S.cols.shown = false;
  S.errTab = false;
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/', vimKeys: {} };
  treeKey('noop'); // reads the project's tree settings, once
  Object.assign(T, {
    view: 'files', shown: false, loaded: true, prompt: null, filtering: false, filter: '', centre: null, cursor: '',
    files: [{ path: 'definitions/sales/a.sqlx' }, { path: 'README.md' }], dirs: [], open: {},
  });
  SRC.file = 'definitions/sales/a.sqlx';
  S.focus = 'source';
  origSourceShownNow = hooks.sourceShownNow;
  origScmKey = hooks.scmKey;
  hooks.sourceShownNow = () => true;
  setVimMode(true);
  setVimKeys({});
});

afterEach(() => {
  handleKey('esc'); // cancel any pending held sequence
  setVimMode(false);
  setVimKeys({});
  hooks.sourceShownNow = origSourceShownNow;
  hooks.scmKey = origScmKey;
});

test('vimKeyTokens converts vim key notation to keyName tokens', () => {
  assert.deepEqual(vimKeyTokens('<Space>nt'), ['space', 'n', 't']);
  assert.deepEqual(vimKeyTokens(',nt'), [',', 'n', 't']);
  assert.deepEqual(vimKeyTokens('<C-n>'), ['ctrl+n']);
  assert.deepEqual(vimKeyTokens('<Tab><CR><Esc>'), ['tab', 'enter', 'esc']);
});

test('vimFileTree defaults to <leader>nt and respects vimMode and custom keys', () => {
  assert.equal(vimKey('vimFileTree'), '<Space>nt');
  setVimKeys({ vimLeader: ',' });
  assert.equal(vimKey('vimFileTree'), ',nt');
  setVimKeys({ vimFileTree: '<leader>e' });
  assert.equal(vimKey('vimFileTree'), '<Space>e');
  setVimMode(false);
  assert.equal(vimKey('vimFileTree'), '');
});

test('<leader>nt toggles the file tree open and closed from non-editor panes', () => {
  assert.equal(handleKey('space'), true);
  assert.equal(T.shown, false);
  assert.equal(handleKey('n'), true);
  assert.equal(T.shown, false);
  assert.equal(handleKey('t'), true);
  assert.equal(T.shown, true);
  assert.equal(T.view, 'files');
  assert.equal(S.focus, 'tree');
  assert.equal(T.cursor, 'definitions/sales/a.sqlx');

  // Pressed again inside the file tree, it closes and returns focus to source.
  assert.equal(handleKey('space'), true);
  assert.equal(handleKey('n'), true);
  assert.equal(handleKey('t'), true);
  assert.equal(T.shown, false);
  assert.equal(S.focus, 'source');
});

test('with vimMode off, <leader>nt does nothing', () => {
  setVimMode(false);
  handleKey('space');
  handleKey('n');
  handleKey('t');
  assert.equal(T.shown, false);
});

test('custom vimLeader and vimFileTree in S.boot.vimKeys work outside the editor', () => {
  S.boot.vimKeys = { vimLeader: ',', vimFileTree: '<leader>e' };
  assert.equal(handleKey(','), true);
  assert.equal(handleKey('e'), true);
  assert.equal(T.shown, true);
  assert.equal(S.focus, 'tree');
});

test('a mismatched key after the leader replays and acts normally', () => {
  T.shown = true;
  S.focus = 'tree';
  T.cursor = 'definitions';
  assert.equal(handleKey('space'), true);
  assert.equal(handleKey('j'), true);
  assert.equal(T.shown, true);
  assert.equal(T.cursor, 'README.md');
});

test('space space n t still triggers <leader>nt', () => {
  assert.equal(handleKey('space'), true);
  assert.equal(handleKey('space'), true);
  assert.equal(handleKey('n'), true);
  assert.equal(handleKey('t'), true);
  assert.equal(T.shown, true);
  assert.equal(S.focus, 'tree');
});

test('esc cancels a pending leader sequence without toggling', () => {
  assert.equal(handleKey('space'), true);
  assert.equal(handleKey('esc'), true);
  handleKey('n');
  handleKey('t');
  assert.equal(T.shown, false);
});

test('space in Source Control is not intercepted by <leader>nt', () => {
  T.shown = true;
  T.view = 'scm';
  S.focus = 'tree';
  let staged = false;
  hooks.scmKey = (k) => { if (k === 'space') { staged = true; return true; } return false; };
  assert.equal(handleKey('space'), true);
  assert.equal(staged, true);
});

test('a file tree prompt is not intercepted by <leader>nt', () => {
  T.shown = true;
  T.view = 'files';
  S.focus = 'tree';
  T.prompt = { kind: 'delete', path: 'README.md', dir: false, count: 0 };
  assert.equal(handleKey('space'), true);
  assert.equal(T.prompt, null, 'delete confirmation cancelled by non-y key');
});
