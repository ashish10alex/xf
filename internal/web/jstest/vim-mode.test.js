import { test, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { vimOn, unsavedIn, staleIn, savesWith, discardsWith, undoesWith } from '../assets/vim-mode.js';
import { setVimMode, setVimKeys, vimKey, vimMode } from '../assets/editor.js';

afterEach(() => setVimMode(false));

test('vimMode is off until the setting says on', () => {
  assert.equal(vimOn(), false);
  assert.equal(vimMode(), false);
  setVimMode(true);
  assert.equal(vimOn(), true, 'setVimMode tells vim-mode.js, which the messages read');
  assert.equal(vimMode(), true);
});

test('with vim, the messages name the ex commands, as they always have', () => {
  setVimMode(true);
  assert.equal(unsavedIn('a.sqlx'), 'unsaved changes in a.sqlx: :w saves, :e! discards');
  assert.equal(staleIn('a.sqlx'), 'a.sqlx changed on disk: :w to choose what to keep, :e! discards');
  assert.deepEqual([savesWith(), discardsWith(), undoesWith()], [':w', ':e!', 'u']);
});

test('without vim, they name the keys and the palette command that work', () => {
  // setup.js: not a Mac.
  assert.equal(unsavedIn('a.sqlx'), 'unsaved changes in a.sqlx: ctrl+s saves, ctrl+shift+p "discard unsaved changes" discards');
  assert.equal(staleIn('a.sqlx'), 'a.sqlx changed on disk: ctrl+s to choose what to keep, ctrl+shift+p "discard unsaved changes" discards');
  assert.equal(undoesWith(), 'ctrl+z');
});

test('the menus and the help show vim keys only with vim on', () => {
  setVimKeys({ vimNextHunk: ']c' });
  assert.equal(vimKey('vimNextHunk'), '', 'no key that does nothing');
  setVimMode(true);
  assert.equal(vimKey('vimNextHunk'), ']c');
  assert.equal(vimKey('vimHover'), 'K');
  setVimKeys({});
});
