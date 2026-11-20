// The terminal tab's shells, each a tab of the log pane: ⌥⌘→/← goes
// through them in order, and ctrl+shift+` (a new one) stays xf's from
// inside the terminal.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S, isNewTermKey, termReserved } from '../assets/state.js';
import { activeTerm, nextTab, setTerms } from '../assets/log-tabs.js';

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
