// gf's file name under the cursor and ctrl+x ctrl+f's file names
// (source-paths.js).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { nameAt, splitTyped, pathOptions } from '../assets/source-paths.js';

const name = (text, col) => { const at = nameAt(text, col); return at && at.name; };

test('gf reads the file name under the cursor, as vim does', () => {
  const line = 'const c = require("includes/constants");';
  assert.equal(name(line, line.indexOf('constants')), 'includes/constants');
  assert.equal(name(line, line.indexOf('includes')), 'includes/constants');
  // Off a name, the first one after the cursor.
  assert.equal(name('see   ../README.md', 3), '../README.md');
  assert.equal(name('-- see definitions/a.sqlx.', 8), 'definitions/a.sqlx');
  assert.equal(name('x = ..', 4), '..');
  assert.equal(name('a.sqlx   ', 7), null);
  assert.equal(name('', 0), null);
});

test('gF reads the line after the name', () => {
  for (const [text, line, col] of [['definitions/a.sqlx:12', 12, 0], ['a.sqlx:3:7', 3, 6], ['a.js(40)', 40, 0], ['a.js @ 9', 9, 0], ['a.js', 0, 0], ['a.js: x', 0, 0]]) {
    const at = nameAt(text, 0);
    assert.deepEqual([at.line, at.col], [line, col], text);
  }
});

test('ctrl+x ctrl+f completes the name after the last /', () => {
  assert.deepEqual(splitTyped(''), { dir: '', base: '' });
  assert.deepEqual(splitTyped('incl'), { dir: '', base: 'incl' });
  assert.deepEqual(splitTyped('../includes/co'), { dir: '../includes/', base: 'co' });
  assert.deepEqual(splitTyped('definitions/'), { dir: 'definitions/', base: '' });
});

test('ctrl+x ctrl+f lists names by prefix, dot names only after a dot', () => {
  const entries = [{ name: 'orders.sqlx' }, { name: 'staging', dir: true }, { name: '.sqlfluff' }, { name: 'Other.js' }, { name: 'xorders.sqlx' }];
  const names = (base) => pathOptions(entries, base).map((e) => e.name);
  assert.deepEqual(names(''), ['Other.js', 'orders.sqlx', 'staging', 'xorders.sqlx']);
  assert.deepEqual(names('o'), ['Other.js', 'orders.sqlx']);
  assert.deepEqual(names('.'), ['.sqlfluff']);
  assert.deepEqual(names('zz'), []);
});
