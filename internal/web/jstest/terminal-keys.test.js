// The terminal tab's keys off a Mac (setup.js says the platform isn't
// one): alt+z zooms it, and ctrl+z stays the shell's.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isTermZoomKey, termReserved } from '../assets/state.js';

const key = (k, mods = {}) => ({ key: k, code: 'Key' + k.toUpperCase(), ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });

test('alt+z zooms the terminal; ctrl+z and z are the shell\'s', () => {
  assert.equal(isTermZoomKey(key('z', { altKey: true })), true);
  assert.equal(termReserved(key('z', { altKey: true })), true);
  for (const e of [key('z'), key('z', { ctrlKey: true }), key('z', { metaKey: true }), key('z', { altKey: true, shiftKey: true })]) {
    assert.equal(termReserved(e), false, JSON.stringify(e));
  }
});
