// Hidden and gitignored files in the file tree: ignored files and folders
// are marked ignored (dimmed), parent folders holding only ignored entries
// inherit ignored, and lazy ignored folders expand on demand via /api/files/dir.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { T, key, rows, clickRow, reveal, loadDir } from '../assets/tree.js';

let dirCalls = [];
let dirResponses = {};

globalThis.fetch = async (url) => {
  const u = new URL(typeof url === 'string' ? url : url.url, 'http://localhost');
  if (u.pathname === '/api/files/dir') {
    const p = u.searchParams.get('path');
    dirCalls.push(p);
    const body = dirResponses[p] || { files: [], dirs: [] };
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  }
  return new Response('{}', { headers: { 'Content-Type': 'application/json' } });
};

beforeEach(() => {
  dirCalls = [];
  dirResponses = {
    node_modules: {
      files: [{ path: 'node_modules/top.txt', ignored: true }],
      dirs: ['node_modules/pkg'],
    },
    'node_modules/pkg': {
      files: [{ path: 'node_modules/pkg/index.js', ignored: true }],
      dirs: [],
    },
  };
  S.mode = 'project';
  S.boot = { ...(S.boot || {}), root: '/r/ignored-test', sep: '/' };
  SRC.file = '';
  SRC.dirty = false;
  key('noop');
  Object.assign(T, {
    view: 'files', shown: true, loaded: true, git: true, prompt: null,
    filtering: false, filter: '', drag: null, centre: null, cursor: '',
    files: [
      { path: '.env', ignored: true },
      { path: '.gitignore' },
      { path: '.xf/config.json' },
      { path: '.xf/tmp/scratch_1.sql', ignored: true },
      { path: 'definitions/orders.sqlx' },
      { path: 'logs/run.log', ignored: true },
    ],
    dirs: ['definitions/empty'],
    lazyDirs: ['node_modules'],
    dirEntries: {},
    loadingDirs: {},
    open: { '.xf': true },
  });
});

test('folders inherit ignored only when all their children are ignored', () => {
  const rs = rows();
  const byPath = Object.fromEntries(rs.map((r) => [r.path, r]));

  // .xf has both tracked config.json and ignored tmp/ -> not ignored.
  assert.equal(byPath['.xf'].ignored, false);
  assert.equal(byPath['.xf/config.json'].ignored, false);
  assert.equal(byPath['.xf/tmp'].ignored, true);

  // logs only holds ignored run.log -> ignored.
  assert.equal(byPath.logs.ignored, true);

  // Lazy dir node_modules is marked ignored & lazy.
  assert.equal(byPath.node_modules.ignored, true);
  assert.equal(byPath.node_modules.lazy, true);

  // Non-ignored root files & folders stay normal.
  assert.equal(byPath['.env'].ignored, true);
  assert.equal(byPath['.gitignore'].ignored, false);
  assert.equal(byPath.definitions.ignored, false);
});

test('expanding a lazy folder fetches its immediate children via /api/files/dir', async () => {
  const nm = rows().find((r) => r.path === 'node_modules');
  assert.equal(nm.open, false);
  clickRow(nm);
  await new Promise((r) => setTimeout(r, 10));

  assert.deepEqual(dirCalls, ['node_modules']);
  const rs = rows();
  const byPath = Object.fromEntries(rs.map((r) => [r.path, r]));
  assert.equal(byPath.node_modules.open, true);
  assert.equal(byPath.node_modules.lazy, false);
  assert.equal(byPath['node_modules/top.txt'].ignored, true);
  assert.equal(byPath['node_modules/pkg'].ignored, true);
  assert.equal(byPath['node_modules/pkg'].lazy, true);

  // Expanding nested lazy folder fetches the next level.
  clickRow(byPath['node_modules/pkg']);
  await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(dirCalls, ['node_modules', 'node_modules/pkg']);
  const nested = rows().find((r) => r.path === 'node_modules/pkg/index.js');
  assert.equal(nested.ignored, true);
});

test('filtering matches ignored files and lazy folder names without auto-opening unloaded lazy dirs', () => {
  T.filter = '.env';
  assert.deepEqual(rows().map((r) => r.path), ['.env']);

  T.filter = 'node_mod';
  const rs = rows();
  assert.deepEqual(rs.map((r) => r.path), ['node_modules']);
  assert.equal(rs[0].open, false);
});

test('revealing a file inside a lazy folder loads its ancestor chain', async () => {
  reveal('node_modules/pkg/index.js', true);
  await new Promise((r) => setTimeout(r, 20));
  assert.deepEqual(dirCalls, ['node_modules', 'node_modules/pkg']);
  assert.ok(rows().some((r) => r.path === 'node_modules/pkg/index.js'));
  assert.equal(T.cursor, 'node_modules/pkg/index.js');
});

test('loadDir prunes removed subdirectories from dirEntries on refresh', async () => {
  T.open.node_modules = true;
  T.open['node_modules/pkg'] = true;
  await loadDir('node_modules');
  assert.ok(T.dirEntries['node_modules/pkg']);

  // Simulate node_modules/pkg being deleted on disk and refreshed.
  dirResponses.node_modules = { files: [{ path: 'node_modules/top.txt', ignored: true }], dirs: [] };
  await loadDir('node_modules');
  assert.equal(T.dirEntries['node_modules/pkg'], undefined);
});
