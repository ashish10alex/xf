// Lineage is a tab of pane 2's lower box, beside sql and columns; O shows
// it and tab goes round to it (ADR 0022).

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, handleKey } from '../assets/state.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, checks: null, zoomed: false, errTab: false, rightHidden: false, flash: '', lineageOpen: false, state: { errors: 0 } });
  S.cols.shown = false;
  S.cols.typing = false;
  S.sqlSearch.typing = false;
});

test('O shows the lineage tab with the keys, and again goes back to the sql', () => {
  S.focus = 'actions';
  assert.equal(handleKey('O'), true);
  assert.equal(S.lineageOpen, true);
  assert.equal(S.focus, 'lineage');
  handleKey('O');
  assert.equal(S.lineageOpen, false);
  assert.equal(S.focus, 'sql');
});

test('tab goes round sql, columns and lineage, and back to sql', () => {
  S.focus = 'sql';
  handleKey('tab');
  assert.equal(S.cols.shown, true);
  handleKey('tab');
  assert.equal(S.lineageOpen, true);
  assert.equal(S.focus, 'lineage');
  handleKey('tab');
  assert.equal(S.lineageOpen, false);
  assert.equal(S.cols.shown, false);
  assert.equal(S.focus, 'sql');
  handleKey('shift+tab');
  assert.equal(S.focus, 'lineage');
});

test('c from the lineage tab goes back to the sql', () => {
  S.focus = 'actions';
  handleKey('O');
  handleKey('c');
  assert.equal(S.lineageOpen, false);
  assert.equal(S.cols.shown, false);
  assert.equal(S.focus, 'sql');
});

test('the keys going to the sql box while lineage shows go to lineage', () => {
  S.focus = 'actions';
  handleKey('O');
  handleKey('ctrl+2'); // to the actions
  assert.equal(S.focus, 'actions');
  handleKey('ctrl+2'); // back to the box, on its lineage tab
  assert.equal(S.focus, 'lineage');
});
