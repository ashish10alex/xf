// Drag-and-drop moves in the file tree: dropping a file or folder onto a
// folder (or another file in it, or the root) moves it via /api/files/rename,
// refusing no-ops, moves into a folder's own subtree, and unsaved files.

import { test, beforeEach, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { T, key, rows, dropDir, targetPath, canDrop, startDrag, dragOver, dragLeave, endDrag, dropInto } from '../assets/tree.js';

let posts = [];
let filesResponse = { files: [], dirs: [], git: true };
let renameError = null;

globalThis.fetch = async (url, opts) => {
  const path = typeof url === 'string' ? url : url.url;
  if (path.includes('/api/files/rename')) {
    const body = JSON.parse(opts.body);
    posts.push(body);
    if (renameError) {
      return new Response(JSON.stringify({ error: renameError }), { status: 400, headers: { 'Content-Type': 'application/json' } });
    }
    const { from, to } = body;
    filesResponse = {
      ...filesResponse,
      files: filesResponse.files.map((f) => (f.path === from || f.path.startsWith(from + '/')
        ? { ...f, path: to + f.path.slice(from.length) }
        : f)),
    };
    return new Response(JSON.stringify({ ok: true, message: 'moved ' + from + ' to ' + to }), {
      headers: { 'Content-Type': 'application/json' },
    });
  }
  if (path.includes('/api/files')) {
    return new Response(JSON.stringify(filesResponse), { headers: { 'Content-Type': 'application/json' } });
  }
  return new Response('{}', { headers: { 'Content-Type': 'application/json' } });
};

beforeEach(() => {
  posts = [];
  renameError = null;
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/shop', sep: '/' };
  S.flash = '';
  SRC.file = '';
  SRC.dirty = false;
  key('noop'); // reads the project's tree settings, once
  filesResponse = {
    files: [
      { path: 'definitions/staging/orders.sqlx' },
      { path: 'definitions/marts/sales/revenue.sqlx' },
      { path: 'README.md' },
    ],
    dirs: ['definitions/empty'],
    git: true,
  };
  Object.assign(T, {
    view: 'files', shown: true, loaded: true, prompt: null, filtering: false, filter: '', drag: null,
    files: filesResponse.files, dirs: filesResponse.dirs,
    open: { definitions: true, 'definitions/staging': true, 'definitions/marts': true },
  });
});

afterEach(() => {
  endDrag();
});

test('dropDir picks the folder itself for a folder row and its parent for a file row', () => {
  const rs = rows();
  assert.equal(dropDir(rs.find((r) => r.path === 'definitions/marts')), 'definitions/marts');
  assert.equal(dropDir(rs.find((r) => r.path === 'definitions/staging/orders.sqlx')), 'definitions/staging');
  assert.equal(dropDir(rs.find((r) => r.path === 'README.md')), '');
  assert.equal(dropDir(null), '');
});

test('targetPath joins the dragged item name onto the destination folder', () => {
  assert.equal(targetPath('definitions/staging/orders.sqlx', 'definitions/marts'), 'definitions/marts/orders.sqlx');
  assert.equal(targetPath('definitions/staging/orders.sqlx', ''), 'orders.sqlx');
  assert.equal(targetPath('README.md', 'definitions/marts'), 'definitions/marts/README.md');
  assert.equal(targetPath('definitions/staging', 'definitions/marts'), 'definitions/marts/staging');
});

test('canDrop refuses same folder, self, descendants, and dirty files', () => {
  assert.equal(canDrop('definitions/staging/orders.sqlx', 'definitions/staging'), false);
  assert.equal(canDrop('README.md', ''), false);
  assert.equal(canDrop('definitions/staging', 'definitions/staging'), false);
  assert.equal(canDrop('definitions', 'definitions/marts'), false);
  assert.equal(canDrop('definitions/staging/orders.sqlx', 'definitions/marts'), true);
  assert.equal(canDrop('definitions/staging/orders.sqlx', ''), true);

  SRC.file = 'definitions/staging/orders.sqlx';
  SRC.dirty = true;
  assert.equal(canDrop('definitions/staging/orders.sqlx', 'definitions/marts'), false);
  assert.equal(canDrop('definitions/staging', 'definitions/marts'), false);
  assert.equal(canDrop('README.md', 'definitions/marts'), true);
});

test('startDrag refuses unsaved files and open prompts', () => {
  SRC.file = 'definitions/staging/orders.sqlx';
  SRC.dirty = true;
  assert.equal(startDrag('definitions/staging/orders.sqlx'), false);
  assert.equal(T.drag, null);
  assert.match(S.flash, /unsaved/i);

  SRC.dirty = false;
  T.prompt = { kind: 'new', path: '', value: '' };
  assert.equal(startDrag('README.md'), false);
  assert.equal(T.drag, null);
});

test('dragging a file into a folder posts /api/files/rename and reveals it', async () => {
  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  const marts = rows().find((r) => r.path === 'definitions/marts');
  assert.equal(dragOver(dropDir(marts), marts), true);
  assert.deepEqual(T.drag, { from: 'definitions/staging/orders.sqlx', over: 'definitions/marts' });

  const moved = await dropInto('definitions/marts');
  assert.equal(moved, true);
  assert.equal(T.drag, null);
  assert.deepEqual(posts, [{ from: 'definitions/staging/orders.sqlx', to: 'definitions/marts/orders.sqlx' }]);
  assert.equal(T.cursor, 'definitions/marts/orders.sqlx');
  assert.equal(T.open['definitions/marts'], true);
});

test('dragging a nested file to the project root moves it to top level', async () => {
  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  assert.equal(dragOver('', null), true);
  assert.deepEqual(T.drag, { from: 'definitions/staging/orders.sqlx', over: '' });

  const moved = await dropInto('');
  assert.equal(moved, true);
  assert.deepEqual(posts, [{ from: 'definitions/staging/orders.sqlx', to: 'orders.sqlx' }]);
  assert.equal(T.cursor, 'orders.sqlx');
});

test('moving an open folder keeps it and its open subfolders open at the new path', async () => {
  T.open = { definitions: true, 'definitions/marts': true, 'definitions/marts/sales': true };
  assert.equal(startDrag('definitions/marts'), true);
  const moved = await dropInto('definitions/empty');
  assert.equal(moved, true);
  assert.deepEqual(posts, [{ from: 'definitions/marts', to: 'definitions/empty/marts' }]);
  assert.equal(T.open['definitions/marts'], undefined);
  assert.equal(T.open['definitions/marts/sales'], undefined);
  assert.equal(T.open['definitions/empty'], true);
  assert.equal(T.open['definitions/empty/marts'], true);
  assert.equal(T.open['definitions/empty/marts/sales'], true);
});

test('moving the currently open file follows it to its new path', async () => {
  SRC.file = 'definitions/staging/orders.sqlx';
  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  const moved = await dropInto('definitions/marts');
  assert.equal(moved, true);
  assert.equal(S.plainFile, 'definitions/marts/orders.sqlx');
  assert.equal(T.cursor, 'definitions/marts/orders.sqlx');
});

test('a refused server move reports the error and leaves no prompt behind', async () => {
  renameError = 'definitions/marts/orders.sqlx already exists';
  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  const moved = await dropInto('definitions/marts');
  assert.equal(moved, false);
  assert.equal(T.prompt, null);
  assert.equal(T.drag, null);
  assert.equal(S.flash, 'definitions/marts/orders.sqlx already exists');
});

test('dragLeave clears the highlighted drop target while keeping the drag active', () => {
  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  assert.equal(dragOver('definitions/marts', null), true);
  assert.equal(T.drag.over, 'definitions/marts');
  dragLeave();
  assert.deepEqual(T.drag, { from: 'definitions/staging/orders.sqlx', over: null });
});

test('hovering over a collapsed folder opens it after a brief pause', async () => {
  T.open = { definitions: true, 'definitions/staging': true };
  const marts = rows().find((r) => r.path === 'definitions/marts');
  assert.equal(marts.open, false);

  assert.equal(startDrag('definitions/staging/orders.sqlx'), true);
  assert.equal(dragOver('definitions/marts', marts), true);
  assert.equal(!!T.open['definitions/marts'], false);
  await new Promise((r) => setTimeout(r, 560));
  assert.equal(T.open['definitions/marts'], true);
});
