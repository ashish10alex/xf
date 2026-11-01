// The page log's lines (diag.js): what a key did, with no typed text.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { keyLabel, keyLine, note, diagLines, errText } from '../assets/diag.js';

test('a key typed into an editor or box is a dot; a shortcut is named', () => {
  assert.equal(keyLabel({ key: 'x' }, true), '·');
  assert.equal(keyLabel({ key: 'x' }, false), 'x');
  assert.equal(keyLabel({ key: 's', ctrlKey: true }, true), 'ctrl+s');
  assert.equal(keyLabel({ key: 'Escape' }, true), 'Escape');
  assert.equal(keyLabel({ key: ' ' }, false), 'space');
});

test('a key line says where the key went and whether the page drew', () => {
  const r = { label: 'j', target: 'body', state: 'project checks=cost', draws: 0, used: true, reached: true };
  assert.match(keyLine(r, true, 0), /^key j in body · project checks=cost → used, no draw$/);
  assert.match(keyLine({ ...r, used: undefined, reached: false }, true, 0), /taken before xf's keys/);
  assert.match(keyLine({ ...r, used: undefined, target: 'div.cm-content < div.cm-editor (hidden)' }, false, 0), /→ to the editor/);
});

test('the same line again counts up rather than repeating', () => {
  const before = diagLines().length;
  note('key · in editor');
  note('key · in editor');
  note('key · in editor');
  const lines = diagLines();
  assert.equal(lines.length, before + 1);
  assert.match(lines.at(-1), /key · in editor ×3$/);
});

test('an error is its message and the top of its stack', () => {
  const e = new TypeError("Cannot read properties of null (reading 'map')");
  assert.match(errText(e), /^TypeError: Cannot read properties of null/);
  assert.match(errText(e), /\n\s+at /);
  assert.equal(errText('plain'), 'Error: plain');
});
