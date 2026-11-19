// Launching standalone scratch queries from the project picker (and the
// projects overlay) with BigQuery or Databricks.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, pickerKey, openScratchPicker, closeScratchPicker } from '../assets/state.js';
import { SC } from '../assets/scratch.js';
import { tableToken, tableOptions, clearMetaCache } from '../assets/scratch-complete.js';
import { P, chooseProfile } from '../assets/databricks.js';

let posted = [];
const okJSON = (v) => ({ ok: true, status: 200, text: async () => JSON.stringify(v), json: async () => v });

beforeEach(() => {
  posted = [];
  globalThis.location = { href: '/' };
  globalThis.fetch = async (url, opts = {}) => {
    const body = opts.body ? JSON.parse(opts.body) : null;
    if (String(url).includes('/api/scratch/open')) {
      posted.push({ url: String(url), body });
      return okJSON({ ok: true, key: 'scratch-' + body.warehouse });
    }
    return okJSON({});
  };
  Object.assign(S, {
    mode: 'picker',
    overlay: null,
    key: '',
    openIn: 'same-tab',
    picker: [{ root: '/a', name: 'shop', key: 'shop', backend: 'dbt' }],
    pickerCursor: 0,
    pickerQuery: '',
    pickerConfirm: null,
    paths: null,
    find: null,
    flash: '',
    scratchWarehouses: [
      { id: 'bigquery', name: 'BigQuery' },
      { id: 'databricks', name: 'Databricks' },
    ],
    scratchPick: null,
  });
});

const tick = () => new Promise((r) => setTimeout(r, 0));

test('ctrl+s in the picker opens the scratch warehouse chooser, esc returns', () => {
  assert.equal(pickerKey('ctrl+s'), true);
  assert.deepEqual(S.scratchPick, { cursor: 0, opening: false });
  assert.equal(pickerKey('down'), true);
  assert.equal(S.scratchPick.cursor, 1);
  assert.equal(pickerKey('up'), true);
  assert.equal(S.scratchPick.cursor, 0);
  assert.equal(pickerKey('esc'), true);
  assert.equal(S.scratchPick, null);
});

test('enter opens the selected warehouse via /api/scratch/open', async () => {
  openScratchPicker();
  pickerKey('j'); // Databricks
  pickerKey('enter');
  await tick();
  assert.equal(posted.length, 1);
  assert.deepEqual(posted[0].body, { warehouse: 'databricks' });
  assert.equal(location.href, '/?project=scratch-databricks');
});

test('b and d shortcuts open BigQuery and Databricks directly', async () => {
  openScratchPicker();
  pickerKey('b');
  await tick();
  assert.equal(posted.length, 1);
  assert.deepEqual(posted[0].body, { warehouse: 'bigquery' });
  assert.equal(location.href, '/?project=scratch-bigquery');

  posted = [];
  openScratchPicker();
  pickerKey('d');
  await tick();
  assert.equal(posted.length, 1);
  assert.deepEqual(posted[0].body, { warehouse: 'databricks' });
  assert.equal(location.href, '/?project=scratch-databricks');
});

test('closeScratchPicker clears the chooser', () => {
  openScratchPicker();
  assert.ok(S.scratchPick);
  closeScratchPicker();
  assert.equal(S.scratchPick, null);
});

test('BigQuery project completion suggests hyphenated projects with backticks', async () => {
  clearMetaCache();
  S.caps = { scratch: true, warehouse: 'bigquery' };
  SC.cur = { id: 's1', project: 'default-proj', location: '', sql: '' };
  globalThis.fetch = async (url) => {
    const u = String(url);
    if (u.includes('/api/scratch/meta/models')) return okJSON([]);
    if (u.includes('/api/scratch/meta/projects')) return okJSON(['jlr-it-scanalytics-prod', 'other-proj']);
    if (u.includes('/api/scratch/meta/datasets?project=default-proj')) return okJSON(['JLR_DL_EDW']);
    return okJSON([]);
  };
  const sql = 'SELECT * FROM `jlr';
  const { tok, tokStart } = tableToken(sql, sql.length);
  assert.equal(tok, '`jlr');
  assert.equal(tokStart, 14);
  const res = await tableOptions(tok, tokStart, sql);
  assert.equal(res.from, 15);
  const projOpt = res.options.find((o) => o.label === 'jlr-it-scanalytics-prod');
  assert.ok(projOpt);
  assert.equal(projOpt.detail, 'project');
  assert.equal(projOpt.boost, 2);
});

test('Databricks completion chains across backtick-quoted catalog and schema', async () => {
  clearMetaCache();
  S.caps = { scratch: true, warehouse: 'databricks' };
  SC.cur = { id: 's1', project: '', location: '', sql: '' };
  globalThis.fetch = async (url) => {
    const u = String(url);
    if (u.includes('/api/scratch/meta/models')) return okJSON([]);
    if (u.includes('/api/scratch/meta/projects')) return okJSON(['my-cat', 'main']);
    if (u.includes('/api/scratch/meta/datasets?project=my-cat')) return okJSON(['my-sch', 'bronze']);
    if (u.includes('/api/scratch/meta/tables?project=my-cat&dataset=my-sch')) return okJSON([{ id: 'orders', type: 'MANAGED' }]);
    return okJSON([]);
  };

  // Step 1: catalog -> inserts `my-cat`.
  const s1 = 'SELECT * FROM my';
  const t1 = tableToken(s1, s1.length);
  const r1 = await tableOptions(t1.tok, t1.tokStart, s1);
  assert.ok(r1.options.some((o) => o.label === 'my-cat' && o.detail === 'catalog'));

  // Step 2: after `my-cat`. -> fetches schemas of my-cat.
  const s2 = 'SELECT * FROM `my-cat`.my';
  const t2 = tableToken(s2, s2.length);
  assert.equal(t2.tok, '`my-cat`.my');
  assert.equal(t2.tokStart, 14);
  const r2 = await tableOptions(t2.tok, t2.tokStart, s2);
  assert.equal(r2.from, 23);
  assert.ok(r2.options.some((o) => o.label === 'my-sch' && o.detail === 'schema'));

  // Step 3: after `my-cat`.`my-sch`. -> fetches tables of my-cat.my-sch.
  const s3 = 'SELECT * FROM `my-cat`.`my-sch`.ord';
  const t3 = tableToken(s3, s3.length);
  assert.equal(t3.tok, '`my-cat`.`my-sch`.ord');
  const r3 = await tableOptions(t3.tok, t3.tokStart, s3);
  assert.equal(r3.from, 32);
  assert.ok(r3.options.some((o) => o.label === 'orders'));
});

test('switching Databricks profile is blocked while a scratch query is running', async () => {
  SC.open = true;
  SC.run = { phase: 'running', job: { project: 'databricks', id: 's1' } };
  Object.assign(P, { list: [{ name: 'prod', host: 'https://dbc-prod' }], cursor: 0, saving: false, error: '' });
  await chooseProfile(0);
  assert.match(P.error, /cancel the running query/i);
  assert.equal(posted.length, 0);
  SC.open = false;
  SC.run = null;
});
