// Keys are named as the TUI's bindings name them, so state.js's handlers
// can be the TUI's. setup.js says the platform isn't a Mac, so these are
// the ctrl (Windows and Linux) forms of cmd+s and cmd+p.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { keyName, isSaveKey, isPaletteKey, isCommandsKey, isProjectsKey, onMac } from '../assets/state.js';

const key = (k, mods = {}) => ({ key: k, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });

test('keyName names keys as the TUI does', () => {
  for (const [e, want] of [
    [key('j'), 'j'],
    [key('G', { shiftKey: true }), 'G'],
    [key('d', { ctrlKey: true }), 'ctrl+d'],
    [key('D', { ctrlKey: true, shiftKey: true }), 'ctrl+d'],
    [key('Enter'), 'enter'],
    [key('Escape'), 'esc'],
    [key('Tab', { shiftKey: true }), 'shift+tab'],
    [key(' '), 'space'],
    [key('ArrowDown'), 'down'],
  ]) assert.equal(keyName(e), want, JSON.stringify(e));
});

test('save, palette and commands keys off a Mac', () => {
  assert.equal(onMac, false);
  assert.equal(isSaveKey(key('s', { ctrlKey: true })), true);
  assert.equal(isSaveKey(key('s', { metaKey: true })), false);
  assert.equal(isSaveKey(key('s', { ctrlKey: true, shiftKey: true })), false);
  assert.equal(isPaletteKey(key('p', { ctrlKey: true })), true);
  assert.equal(isPaletteKey(key('P', { ctrlKey: true, shiftKey: true })), false);
  assert.equal(isCommandsKey(key('P', { ctrlKey: true, shiftKey: true })), true);
});

test('off a Mac, ctrl+n moves only in an app window', async () => {
  const { appWindow, ctrlNMoves, paletteKeyMovesDown } = await import('../assets/state.js');
  // setup.js's matchMedia matches nothing: a tab, which never sees ctrl+n,
  // so the palette's ctrl+p again moves down there.
  assert.equal(appWindow, false);
  assert.equal(ctrlNMoves, false);
  assert.equal(paletteKeyMovesDown, true);
});

test('projects key off a Mac is ctrl+shift+w', () => {
  assert.equal(isProjectsKey(key('W', { ctrlKey: true, shiftKey: true })), true);
  assert.equal(isProjectsKey(key('w', { ctrlKey: true, shiftKey: true })), true);
  assert.equal(isProjectsKey(key('W', { metaKey: true, shiftKey: true })), false);
  assert.equal(isProjectsKey(key('w', { ctrlKey: true })), false);
});
