// python.js: how pyrefly's Markdown is split into a card's blocks, and how
// the status line names an interpreter.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mdBlocks, interpreterLabel, pythonKey, PY } from '../assets/python.js';
import { S } from '../assets/state.js';

test('a hover splits into its code, rules and paragraphs', () => {
  const md = '```python\n(function) print: def print(\n    *values: object\n) -> None: ...\n```\n---\nPrints the values.  \n  \nsep  \n&nbsp;&nbsp;string inserted between values.\n\nGo to [object](file:///t/builtins.pyi#L114,7)';
  assert.deepEqual(mdBlocks(md), [
    { code: '(function) print: def print(\n    *values: object\n) -> None: ...' },
    { rule: true },
    { text: 'Prints the values.' },
    { text: 'sep\n\u00a0\u00a0string inserted between values.' },
    { text: 'Go to [object](file:///t/builtins.pyi#L114,7)' },
  ]);
});

test('an interpreter is named by its environment, else its file', () => {
  assert.equal(interpreterLabel({ interpreter: '/p/proj/.venv/bin/python', where: '.venv' }), '.venv');
  assert.equal(interpreterLabel({ interpreter: 'C:\\envs\\dbt\\Scripts\\python.exe', where: 'picked' }), 'dbt');
  assert.equal(interpreterLabel({ interpreter: '/opt/homebrew/bin/python3', where: 'PATH' }), 'python3 (PATH)');
  assert.equal(interpreterLabel({ interpreter: '/usr/python3', where: 'picked' }), 'python3');
  assert.equal(interpreterLabel({ interpreter: '', where: '' }), "pyrefly's default");
});

test('the interpreter picker moves within its rows and esc closes it', () => {
  S.overlay = 'python';
  PY.overlay = { kind: 'interpreter', loading: false, list: [{ path: '/a/.venv/bin/python', where: '.venv' }], current: '', where: '.venv', cursor: 0, custom: '', saving: false, err: '' };
  pythonKey('j'); pythonKey('j'); pythonKey('j');
  assert.equal(PY.overlay.cursor, 2); // found by xf, .venv, another path
  pythonKey('k');
  assert.equal(PY.overlay.cursor, 1);
  pythonKey('esc');
  assert.equal(S.overlay, null);
  assert.equal(PY.overlay, null);
});
