// Source editor hover lookups (refsOf, includeAt, configKeyAt) stay cached
// for the same file, text and compile, and invalidate on compile, file load,
// or failure so changing a source's GCP project or an include in another file
// updates hover without editing the open file.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { hooks } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { refsOf, includeAt, configKeyAt } from '../assets/source-smart.js';
import { languageFor } from '../assets/sqlx.js';
import { tableOptions, clearMetaCache } from '../assets/scratch-complete.js';

const okJSON = (body) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

let calls;
let handler;
globalThis.fetch = async (url, opts) => {
  const body = opts && opts.body ? JSON.parse(opts.body) : null;
  calls.push({ url: String(url), body });
  return handler(String(url), body);
};

const countURL = (prefix) => calls.filter((c) => c.url.startsWith(prefix)).length;

beforeEach(() => {
  calls = [];
  handler = () => okJSON({});
  clearMetaCache();
  SRC.file = 'definitions/model.sqlx';
  SRC.view = { lang: 'sqlx', file: SRC.file, text: '' };
  hooks.sourceLoaded();
});

test('refsOf caches within a compile and refreshes after a compile', async () => {
  let project = 'old-gcp-project';
  handler = () => okJSON({ refs: [{ from: 14, to: 30, project, dataset: 'raw', table: 'orders', hasTable: true }], js: [] });

  const sql = 'select * from ${ref("orders")}';
  const r1 = await refsOf(sql);
  const r2 = await refsOf(sql);
  assert.equal(r1.refs[0].project, 'old-gcp-project');
  assert.equal(r2.refs[0].project, 'old-gcp-project');
  assert.equal(countURL('/api/source/refs'), 1);

  project = 'new-gcp-project';
  hooks.sourceDiagnose();

  const r3 = await refsOf(sql);
  assert.equal(r3.refs[0].project, 'new-gcp-project');
  assert.equal(countURL('/api/source/refs'), 2);
});

test('refsOf ignores an in-flight pre-compile lookup once a compile finishes', async () => {
  let releaseFirst;
  let n = 0;
  handler = () => {
    n++;
    if (n === 1) return new Promise((resolve) => { releaseFirst = () => resolve(okJSON({ refs: [{ project: 'old-proj' }], js: [] })); });
    return okJSON({ refs: [{ project: 'new-proj' }], js: [] });
  };

  const sql = 'select * from ${ref("orders")}';
  const p1 = refsOf(sql);
  hooks.sourceDiagnose();
  const r2 = await refsOf(sql);
  releaseFirst();
  await p1;

  assert.equal(r2.refs[0].project, 'new-proj');
  assert.equal(countURL('/api/source/refs'), 2);
});

test('a late failure from a pre-compile refsOf does not wipe a newer cached lookup', async () => {
  let failFirst;
  let n = 0;
  handler = () => {
    n++;
    if (n === 1) return new Promise((resolve) => { failFirst = () => resolve(new Response('boom', { status: 500 })); });
    return okJSON({ refs: [{ project: 'new-proj' }], js: [] });
  };

  const sql = 'select * from ${ref("orders")}';
  const p1 = refsOf(sql);
  hooks.sourceDiagnose();
  const r2 = await refsOf(sql);
  failFirst();
  assert.equal(await p1, null);

  // The newer post-compile entry must still be cached.
  const r3 = await refsOf(sql);
  assert.equal(r2.refs[0].project, 'new-proj');
  assert.equal(r3.refs[0].project, 'new-proj');
  assert.equal(countURL('/api/source/refs'), 2);
});

test('refsOf keys by file path so identical text in another file is not reused', async () => {
  handler = (_url, body) => okJSON({ refs: [{ project: body.file }], js: [] });

  const sql = 'select * from ${self()}';
  const r1 = await refsOf(sql);
  assert.equal(r1.refs[0].project, 'definitions/model.sqlx');

  SRC.file = 'definitions/other.sqlx';
  const r2 = await refsOf(sql);
  assert.equal(r2.refs[0].project, 'definitions/other.sqlx');
  assert.equal(countURL('/api/source/refs'), 2);
});

test('refsOf does not cache a failed request', async () => {
  let n = 0;
  handler = () => {
    n++;
    if (n === 1) return new Response('boom', { status: 500 });
    return okJSON({ refs: [{ project: 'recovered' }], js: [] });
  };

  const sql = 'select * from ${ref("orders")}';
  assert.equal(await refsOf(sql), null);
  const r2 = await refsOf(sql);
  assert.equal(r2.refs[0].project, 'recovered');
  assert.equal(countURL('/api/source/refs'), 2);
});

test('includeAt refreshes after compile, file reload, file switch, or failure', async () => {
  let code = 'const env = "dev"';
  let fail = false;
  handler = (_url, body) => (fail
    ? new Response('boom', { status: 500 })
    : okJSON({ include: { from: 0, to: 10, code: `${code}:${body.file}`, file: 'includes/params.js', line: 0, col: 0 } }));

  SRC.file = 'includes/use.js';
  SRC.view = { lang: 'js', file: SRC.file, text: '' };
  const state = cm.EditorState.create({ doc: 'params.env' });
  const pos = 8; // on "env"

  assert.equal((await includeAt(state, pos)).code, 'const env = "dev":includes/use.js');
  assert.equal((await includeAt(state, pos)).code, 'const env = "dev":includes/use.js');
  assert.equal(countURL('/api/source/include'), 1);

  SRC.file = 'includes/other.js';
  assert.equal((await includeAt(state, pos)).code, 'const env = "dev":includes/other.js');
  assert.equal(countURL('/api/source/include'), 2);

  code = 'const env = "prod"';
  hooks.sourceDiagnose();
  assert.equal((await includeAt(state, pos)).code, 'const env = "prod":includes/other.js');
  assert.equal(countURL('/api/source/include'), 3);

  code = 'const env = "stage"';
  hooks.sourceLoaded();
  assert.equal((await includeAt(state, pos)).code, 'const env = "stage":includes/other.js');
  assert.equal(countURL('/api/source/include'), 4);

  fail = true;
  hooks.sourceDiagnose();
  assert.equal(await includeAt(state, pos), null);
  fail = false;
  assert.equal((await includeAt(state, pos)).code, 'const env = "stage":includes/other.js');
  assert.equal(countURL('/api/source/include'), 6);
});

test('configKeyAt caches per file/text/compile and does not cache failures', async () => {
  let doc = 'first';
  let fail = false;
  handler = (_url, body) => (fail
    ? new Response('boom', { status: 500 })
    : okJSON({ key: { from: 10, to: 14, path: 'type', kind: 'string', doc: `${doc}:${body.file}` } }));

  SRC.file = 'definitions/a.sqlx';
  SRC.view = { lang: 'sqlx', file: SRC.file, text: '' };
  const state = cm.EditorState.create({ doc: 'config {\n  type: "table"\n}', extensions: [languageFor('sqlx')] });
  const pos = 13; // on "type"

  assert.equal((await configKeyAt(state, pos)).doc, 'first:definitions/a.sqlx');
  assert.equal((await configKeyAt(state, pos)).doc, 'first:definitions/a.sqlx');
  assert.equal(countURL('/api/source/configkey'), 1);

  SRC.file = 'definitions/b.sqlx';
  assert.equal((await configKeyAt(state, pos)).doc, 'first:definitions/b.sqlx');
  assert.equal(countURL('/api/source/configkey'), 2);

  doc = 'second';
  hooks.sourceDiagnose();
  assert.equal((await configKeyAt(state, pos)).doc, 'second:definitions/b.sqlx');
  assert.equal(countURL('/api/source/configkey'), 3);

  fail = true;
  hooks.sourceDiagnose();
  assert.equal(await configKeyAt(state, pos), null);
  fail = false;
  assert.equal((await configKeyAt(state, pos)).doc, 'second:definitions/b.sqlx');
  assert.equal(countURL('/api/source/configkey'), 5);
});

test('a compile clears the cached scratch models list', async () => {
  let project = 'old-proj';
  handler = (url) => {
    if (url === '/api/scratch/meta/models') return okJSON([{ project, dataset: 'ds', name: 'orders', kind: 'table' }]);
    return okJSON([]);
  };

  await tableOptions('ord', 0, 'select * from ord');
  await tableOptions('ord', 0, 'select * from ord');
  assert.equal(countURL('/api/scratch/meta/models'), 1);

  project = 'new-proj';
  hooks.sourceDiagnose();
  await tableOptions('ord', 0, 'select * from ord');
  assert.equal(countURL('/api/scratch/meta/models'), 2);
});
