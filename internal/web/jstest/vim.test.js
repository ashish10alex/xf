// The SQL pane's vim buffer is a port of the TUI's: these are the cases of
// internal/ui/buffer_test.go and search_test.go, so the two stay alike.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as vim from '../assets/vim.js';

function buffer(...lines) {
  const b = vim.newBuffer();
  b.height = 10;
  vim.setText(b, lines, true);
  return b;
}

const at = (b) => [b.row, b.col];

test('w and b step through words, an empty line counting as one', () => {
  const b = buffer('SELECT a.b_c, 42', '', '  FROM x');
  const want = [[0, 7], [0, 8], [0, 9], [0, 12], [0, 14], [1, 0], [2, 2]];
  const got = want.map(() => { vim.wordForward(b, false); return at(b); });
  assert.deepEqual(got, want);
  for (let i = want.length - 2; i >= 0; i--) {
    vim.wordBackward(b, false);
    assert.deepEqual(at(b), want[i], `b step ${i}`);
  }
});

test('W and B treat every non-blank as a word', () => {
  const b = buffer('SELECT a.b_c, 42');
  vim.wordForward(b, true);
  vim.wordForward(b, true);
  assert.equal(b.col, 14);
  vim.wordBackward(b, true);
  assert.equal(b.col, 7);
});

test('j and k keep the column past a short line', () => {
  const b = buffer('abcdefgh', 'ab', 'abcdefgh');
  vim.setCol(b, 6);
  vim.moveRow(b, 1);
  assert.equal(b.col, 1);
  vim.moveRow(b, 1);
  assert.equal(b.col, 6);
});

test('find: smart case, whole words, no overlaps', () => {
  const text = ['SELECT id, customer_id', 'FROM Orders -- orders, ID'].map((l) => Array.from(l));
  const hit = (row, from, to) => ({ row, from, to });
  const cases = [
    ['lower case ignores case', { text: 'orders' }, [hit(1, 5, 11), hit(1, 15, 21)]],
    ['upper case is exact', { text: 'Orders' }, [hit(1, 5, 11)]],
    ['substring', { text: 'id' }, [hit(0, 7, 9), hit(0, 20, 22), hit(1, 23, 25)]],
    ['whole word, any case', { text: 'id', word: true }, [hit(0, 7, 9), hit(1, 23, 25)]],
    ['whole word of an upper-case word', { text: 'ID', word: true }, [hit(0, 7, 9), hit(1, 23, 25)]],
    ['no overlap', { text: 'aa' }, []],
    ['empty', {}, []],
  ];
  for (const [name, p, want] of cases) assert.deepEqual(vim.find(p, text), want, name);
  assert.equal(vim.find({ text: 'aa' }, [Array.from('aaaaa')]).length, 2);
});

test('n and N step round the ends and say when they wrap', () => {
  const b = buffer('a x', 'x b', 'c x');
  vim.setSearch(b, { text: 'x', word: false, back: false });
  for (const [forward, row, col, wrapped] of [
    [true, 0, 2, false], [true, 1, 0, false], [true, 2, 2, false], [true, 0, 2, true],
    [false, 2, 2, true], [false, 1, 0, false],
  ]) {
    const [i, w] = vim.nextHit(b, forward);
    vim.gotoHit(b, i);
    assert.deepEqual([b.row, b.col, w], [row, col, wrapped], `step to ${row},${col}`);
  }
  assert.equal(vim.curHit(b), 1);
  vim.setCol(b, 1);
  assert.equal(vim.curHit(b), -1);

  // New text keeps the pattern and finds it again.
  vim.setText(b, ['x x'], true);
  assert.equal(b.hits.length, 2);
  assert.equal(b.hlHits, true);
});

test('the word at the cursor, or the next one from punctuation', () => {
  const b = buffer('  a.b_c, 42');
  for (const [col, want] of [[0, 'a'], [3, 'b_c'], [4, 'b_c'], [6, 'b_c'], [7, '42']]) {
    vim.setCol(b, col);
    assert.equal(vim.wordAtCursor(b)[0], want, `col ${col}`);
  }
  const end = buffer('a ;');
  vim.setCol(end, 2);
  assert.equal(vim.wordAtCursor(end)[0], '');
});
