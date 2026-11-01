// Moving through the buffer list wraps past either end, so ctrl+n and
// ctrl+p keep cycling.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { BUF, BP, moveBuffers } from '../assets/buffers.js';

beforeEach(() => {
  BUF.list = [{ n: 1, file: 'a.sqlx' }, { n: 2, file: 'b.sqlx' }, { n: 3, file: 'c.sqlx' }];
  Object.assign(BP, { query: '', cursor: 0, actions: {} });
});

test('down from the last row goes to the first', () => {
  BP.cursor = 2;
  moveBuffers(1);
  assert.equal(BP.cursor, 0);
});

test('up from the first row goes to the last', () => {
  moveBuffers(-1);
  assert.equal(BP.cursor, 2);
});

test('a move of 0 keeps the row, and an empty list stays at 0', () => {
  BP.cursor = 1;
  moveBuffers(0);
  assert.equal(BP.cursor, 1);
  BUF.list = [];
  moveBuffers(1);
  assert.equal(BP.cursor, 0);
});
