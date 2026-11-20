// The terminal tab's shells, each a tab of the log pane: ⌥⌘→/← goes
// through them in order, and ctrl+shift+` (a new one) and alt+w (close it)
// stay xf's from inside the terminal.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S, isNewTermKey, isTermCloseKey, termCloseLabel, termReserved } from '../assets/state.js';
import { activeTerm, closeTerm, nextTab, setTerms } from '../assets/log-tabs.js';

// The server answers a close with the shells left.
let left = [];
globalThis.fetch = async () => new Response(JSON.stringify({ ids: left }), { headers: { 'Content-Type': 'application/json' } });

test('next tab goes through each shell before leaving the terminal', () => {
  S.caps = { history: true, terminal: true };
  setTerms({ ids: [3, 7, 9] });
  S.logTab = 'log';
  const seen = [];
  for (let i = 0; i < 5; i++) { nextTab(1); seen.push(S.logTab === 'term' ? 'term' + activeTerm() : S.logTab); }
  assert.deepEqual(seen, ['jobs', 'history', 'term3', 'term7', 'term9']);
  nextTab(1);
  assert.equal(S.logTab, 'log', 'past the last shell, round to the log');
  nextTab(-1);
  assert.equal(S.logTab + activeTerm(), 'term9', 'back into the terminal on its last shell');
  nextTab(-1);
  assert.equal(activeTerm(), 7);
});

test('a closed shell gives way to the first', () => {
  setTerms({ ids: [3, 7, 9] });
  S.termId = 7;
  setTerms({ ids: [3, 9] });
  assert.equal(activeTerm(), 3);
  setTerms({ ids: [] });
  assert.equal(activeTerm(), 0, 'none known: the server\'s first');
});

test('ctrl+shift+` is a new terminal, read by code, and xf keeps it', () => {
  const tick = (mods) => ({ key: '~', code: 'Backquote', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });
  assert.equal(isNewTermKey(tick({ ctrlKey: true, shiftKey: true })), true);
  assert.equal(termReserved(tick({ ctrlKey: true, shiftKey: true })), true);
  for (const e of [tick({ shiftKey: true }), tick({ ctrlKey: true }), tick({ ctrlKey: true, shiftKey: true, altKey: true }), tick({ metaKey: true, shiftKey: true })]) {
    assert.equal(isNewTermKey(e), false, JSON.stringify(e));
  }
});

test('alt+w closes the terminal, read by code, and xf keeps it; ⌘w and ctrl+w are not it', () => {
  const w = (mods) => ({ key: '∑', code: 'KeyW', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });
  assert.equal(isTermCloseKey(w({ altKey: true })), true);
  assert.equal(termReserved(w({ altKey: true })), true);
  assert.equal(termCloseLabel, 'alt+w');
  for (const e of [w({}), w({ ctrlKey: true }), w({ metaKey: true }), w({ altKey: true, shiftKey: true }), w({ altKey: true, metaKey: true })]) {
    assert.equal(isTermCloseKey(e), false, JSON.stringify(e));
    assert.equal(termReserved(e), false, JSON.stringify(e));
  }
});

test('closing the shell shown shows the one before it, or after the first, the one after', async () => {
  S.caps = { history: true, terminal: true };
  S.logTab = 'term';
  setTerms({ ids: [1, 3, 4] });
  S.termId = 4;
  left = [1, 3];
  await closeTerm();
  assert.equal(activeTerm(), 3);
  S.termId = 1;
  left = [3];
  await closeTerm();
  assert.equal(activeTerm(), 3);
  setTerms({ ids: [3, 5] });
  S.termId = 5;
  left = [5];
  await closeTerm(3);
  assert.equal(activeTerm(), 5, 'closing another shell keeps the one shown');
});
