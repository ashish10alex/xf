// esc in the Scratch view leaves nothing: ctrl+q does (exit.js), and q and
// ctrl+c don't.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { SC, scratchKey } from '../assets/scratch.js';

beforeEach(() => {
  Object.assign(SC, { open: true, standalone: false, confirm: null, delAsk: '', focus: 'results', cur: null, flash: '' });
});

const tick = () => new Promise((r) => setTimeout(r, 0));

test('esc, q and ctrl+c leave nothing', async () => {
  for (const k of ['esc', 'q', 'ctrl+c', 'y']) {
    scratchKey(k);
    await tick();
    assert.equal(SC.open, true, k);
    assert.equal(SC.focus, 'results', k);
  }
});
