// The settings page's top banner flags what stops xf working: the tool's
// CLI missing, a Google account that must sign in again. A lookup still
// going ("…") flags nothing.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { attentionItems } from '../assets/settings-view.js';

beforeEach(() => {
  S.mode = 'project';
  S.boot = { backend: 'dataform' };
});

test('a missing dataform CLI is flagged once, from settings or info', () => {
  const keys = [{ key: 'dataformBin', value: '(not found)', from: 'npm i -g @dataform/cli' }];
  const info = [{ title: 'binaries' }, { label: 'dataform', value: '(not found)', warn: true }];
  const items = attentionItems(info, [], keys);
  assert.equal(items.length, 1);
  assert.match(items[0].message, /dataform/);
  assert.equal(items[0].command, 'npm i -g @dataform/cli');
  // Before the info's first line, the settings alone flag it.
  assert.equal(attentionItems(null, [], keys).length, 1);
});

test('gcloud needing sign-in is flagged with its command', () => {
  const info = [{ title: 'google cloud' },
    { label: 'gcloud', value: 'me@work.example (reauthentication required)', warn: true },
    { value: 'run `gcloud auth login`', warn: true }];
  const items = attentionItems(info, [], []);
  assert.equal(items.length, 1);
  assert.equal(items[0].command, 'gcloud auth login');
});

test('a lookup still going flags nothing', () => {
  const info = [{ title: 'google cloud' }, { label: 'xf uses', value: '…' }, { label: 'gcloud', value: '…' }];
  assert.deepEqual(attentionItems(info, [], []), []);
});

test('the pyrefly note names the config, or offers to create one', async () => {
  const { pyreflyNote } = await import('../assets/settings-view.js');
  assert.equal(pyreflyNote(null), null);
  assert.equal(pyreflyNote({ off: true }), null);
  const none = pyreflyNote({ interpreter: '/p/.venv/bin/python', label: '.venv', where: 'picked' });
  assert.equal(none.config, '');
  assert.equal(none.canCreate, true);
  assert.equal(none.interpreter, '.venv');
  assert.equal(pyreflyNote({ missing: 'pyrefly not found' }).canCreate, false);
  const mine = pyreflyNote({ config: 'pyrefly.toml' });
  assert.equal(mine.inProject, true);
  assert.equal(mine.canCreate, false);
  assert.equal(pyreflyNote({ config: '/home/me/pyrefly.toml' }).inProject, false);
});
