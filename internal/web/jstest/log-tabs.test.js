// The log pane's tabs go round with ⌥⌘←/→ (ctrl+alt+←/→ off a Mac), the
// terminal's included, and the terminal leaves that key to xf (ADR 0023).
// tab no longer goes round them.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, handleKey, logTabKey, termReserved, nextTab } from '../assets/state.js';

globalThis.fetch = async () => new Response('{"rows":[]}', { headers: { 'Content-Type': 'application/json' } });

const key = (k, mods = {}) => ({ key: k, code: '', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, checks: null, zoomed: false, showLog: true, focus: 'log', logTab: 'log' });
  S.caps = { ...S.caps, history: true, terminal: true };
});

test('ctrl+alt+→ and ← are the log tab keys off a Mac, nothing else is', () => {
  assert.equal(logTabKey(key('ArrowRight', { ctrlKey: true, altKey: true })), 1);
  assert.equal(logTabKey(key('ArrowLeft', { ctrlKey: true, altKey: true })), -1);
  assert.equal(logTabKey(key('ArrowRight', { ctrlKey: true })), 0);
  assert.equal(logTabKey(key('ArrowRight', { altKey: true })), 0);
  assert.equal(logTabKey(key('ArrowRight', { ctrlKey: true, altKey: true, shiftKey: true })), 0);
  assert.equal(logTabKey(key('ArrowRight', { metaKey: true, altKey: true })), 0);
});

test('the terminal leaves the log tab keys to xf', () => {
  assert.equal(termReserved(key('ArrowRight', { ctrlKey: true, altKey: true })), true);
  assert.equal(termReserved(key('ArrowLeft', { ctrlKey: true, altKey: true })), true);
  assert.equal(termReserved(key('ArrowRight', { altKey: true })), false);
});

test('the next tab goes round log, jobs, history and the terminal', () => {
  const seen = [];
  for (let i = 0; i < 4; i++) { nextTab(1); seen.push(S.logTab); }
  assert.deepEqual(seen, ['jobs', 'history', 'term', 'log']);
  nextTab(-1);
  assert.equal(S.logTab, 'term');
  nextTab(-1);
  assert.equal(S.logTab, 'history');
});

test('without kept runs, it is the log and the terminal', () => {
  S.caps = { ...S.caps, history: false };
  nextTab(1);
  assert.equal(S.logTab, 'term');
  nextTab(1);
  assert.equal(S.logTab, 'log');
});

test('t still goes to the next tab, and tab no longer does', () => {
  handleKey('t');
  assert.equal(S.logTab, 'jobs');
  handleKey('tab');
  assert.equal(S.logTab, 'jobs');
});

test('ctrl+j hides the panel and shows it again on the tab it showed last', () => {
  S.logTab = 'history';
  handleKey('ctrl+j');
  assert.equal(S.showLog, false);
  handleKey('ctrl+j');
  assert.equal(S.showLog, true);
  assert.equal(S.logTab, 'history');
  assert.equal(S.focus, 'log');
});

test('ctrl+` shows the terminal tab, and again hides the panel', () => {
  S.showLog = false;
  handleKey('ctrl+`');
  assert.equal(S.showLog, true);
  assert.equal(S.logTab, 'term');
  handleKey('ctrl+j');
  assert.equal(S.showLog, false);
  handleKey('ctrl+j');
  assert.equal(S.logTab, 'term');
});
