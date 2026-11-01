// Search (docs/adr/0016): its key off a Mac, where setup.js pins the
// tests, and the list's rows, which x thins.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { F, isSearchKey, rows, counts, key, restore } from '../assets/search.js';

const ev = (o) => ({ key: 'F', code: 'KeyF', shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, ...o });

test('ctrl+shift+f is the key off a Mac', () => {
  assert.equal(isSearchKey(ev({ ctrlKey: true, shiftKey: true })), true);
  assert.equal(isSearchKey(ev({ metaKey: true, shiftKey: true })), false);
  assert.equal(isSearchKey(ev({ ctrlKey: true, key: 'f' })), false);
  assert.equal(isSearchKey(ev({ ctrlKey: true, shiftKey: true, altKey: true })), false);
  assert.equal(isSearchKey(ev({ ctrlKey: true, shiftKey: true, key: 'G', code: 'KeyG' })), false);
});

test('the rows are files and their matches, without what x took out', () => {
  restore(); // once, as the view does: it would empty the list later
  const m = (line, at) => ({ line, col: at, len: 3, text: 'ref', with: '', before: '', after: '', lineText: 'ref ref', at });
  F.query = 'ref';
  F.result = { files: [{ path: 'a.sqlx', matches: [m(0, 0), m(0, 4)] }, { path: 'b.sqlx', matches: [m(2, 0)] }], count: 3, searched: 2 };
  F.dismissed = {};
  F.closed = { 'b.sqlx': true };
  assert.deepEqual(rows().map((r) => r.kind + ' ' + r.path), ['file a.sqlx', 'match a.sqlx', 'match a.sqlx', 'file b.sqlx']);
  assert.deepEqual(counts(), { matches: 3, files: 2 });
  // x on a match takes it out, and the cursor stays where it was.
  F.cursor = rows()[1].key;
  key('x');
  assert.deepEqual(counts(), { matches: 2, files: 2 });
  assert.equal(F.cursor, rows()[1].key);
  // x on a file takes it and its matches out.
  F.cursor = rows()[0].key;
  key('x');
  assert.deepEqual(counts(), { matches: 1, files: 1 });
  F.result = null;
  F.query = '';
});
