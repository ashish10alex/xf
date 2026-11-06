// The terminal tab's keys off a Mac (setup.js says the platform isn't
// one): alt+z zooms it, and ctrl+z stays the shell's.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isTermKey, isTermZoomKey, termKeyLabel, termReserved } from '../assets/state.js';

const key = (k, mods = {}) => ({ key: k, code: 'Key' + k.toUpperCase(), ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });

test('alt+z zooms the terminal; ctrl+z and z are the shell\'s', () => {
  assert.equal(isTermZoomKey(key('z', { altKey: true })), true);
  assert.equal(termReserved(key('z', { altKey: true })), true);
  for (const e of [key('z'), key('z', { ctrlKey: true }), key('z', { metaKey: true }), key('z', { altKey: true, shiftKey: true })]) {
    assert.equal(termReserved(e), false, JSON.stringify(e));
  }
});

test('ctrl+` is the terminal off a Mac, ⌘` is not; xterm leaves it to xf; the label is ctrl+`', () => {
  const tick = (mods) => ({ key: '`', code: 'Backquote', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });
  assert.equal(isTermKey(tick({ ctrlKey: true })), true);
  assert.equal(termReserved(tick({ ctrlKey: true })), true);
  assert.equal(termKeyLabel, 'ctrl+`');
  for (const e of [tick({}), tick({ metaKey: true }), tick({ ctrlKey: true, shiftKey: true }), tick({ ctrlKey: true, altKey: true })]) {
    assert.equal(isTermKey(e), false, JSON.stringify(e));
  }
});
