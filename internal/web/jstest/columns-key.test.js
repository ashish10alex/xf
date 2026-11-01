// c goes round pane 2's sql, columns and lineage tabs and never compiles
// (ADR 0017).

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, handleKey, hooks } from '../assets/state.js';

const calls = [];
globalThis.fetch = async (url, opts) => {
  calls.push((opts && opts.method || 'GET') + ' ' + url);
  return new Response('{}', { headers: { 'Content-Type': 'application/json' } });
};

beforeEach(() => {
  calls.length = 0;
  Object.assign(S, { mode: 'project', overlay: null, checks: null, zoomed: false, errTab: false, lineageOpen: false, rightHidden: false, flash: '' });
  S.cols.shown = false;
  S.cols.typing = false;
  S.sqlSearch.typing = false;
});

const compiled = () => calls.some((c) => c.includes('/api/compile'));

test('c from the actions list shows the columns and gives them the keys', () => {
  S.focus = 'actions';
  assert.equal(handleKey('c'), true);
  assert.equal(S.cols.shown, true);
  assert.equal(S.focus, 'sql');
  assert.equal(compiled(), false);
});

test('c goes round the sql, columns and lineage tabs', () => {
  S.focus = 'sql';
  handleKey('c');
  assert.equal(S.cols.shown, true);
  assert.equal(S.lineageOpen, false);
  handleKey('c');
  assert.equal(S.lineageOpen, true);
  assert.equal(S.focus, 'lineage');
  handleKey('c');
  assert.equal(S.lineageOpen, false);
  assert.equal(S.cols.shown, false);
  assert.equal(S.focus, 'sql');
  assert.equal(compiled(), false);
});

test('c on the errors tab goes to the columns, even when they showed before', () => {
  S.focus = 'sql';
  S.state = { ...S.state, errors: 1 };
  S.errTab = true;
  S.cols.shown = true;
  handleKey('c');
  assert.equal(S.errTab, false);
  assert.equal(S.cols.shown, true);
  S.state = { ...S.state, errors: 0 };
});

test('c from a zoomed log unzooms so the columns show', () => {
  S.focus = 'log';
  S.zoomed = true;
  handleKey('c');
  assert.equal(S.cols.shown, true);
  assert.equal(S.zoomed, false);
  assert.equal(S.focus, 'sql');
});

test('c then z zooms the columns', () => {
  S.focus = 'actions';
  handleKey('c');
  handleKey('z');
  assert.equal(S.zoomed, true);
  assert.equal(S.focus, 'sql');
});

test('c in the file tree does nothing', () => {
  S.focus = 'tree';
  hooks.treeKey = () => false; // the tree has no c of its own
  handleKey('c');
  assert.equal(S.cols.shown, false);
  assert.equal(compiled(), false);
});

test('c in the errors overlay does not compile', () => {
  S.focus = 'actions';
  S.overlay = 'errors';
  handleKey('c');
  assert.equal(compiled(), false);
  assert.equal(S.overlay, 'errors');
});
