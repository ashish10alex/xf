// The dependency inspector's filters (ADR 0033): with apply to all on,
// every row's query uses the global filter; off, each row keeps the global
// filter it had, to change on its own. A preset fills the rows it names,
// and the rest as a new row is. A row's result keeps its own grid state,
// apart from Scratch's. The search shows only the tables that have it,
// and ticking all ticks those. The workspace keeps the page as it was. A
// filter pasted from a spreadsheet is tidied to read on one line.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { IN, filterOf, sqlOf, setGlobal, setApplyAll, setRowFilter, applyPreset, resultOf, ticked, setOn, setAllOn, setSearch, shownNodes, tidyFilter, cycleResult, sqlTokens, setTickedOnly, addTable } from '../assets/inspector.js';
import { hooks } from '../assets/state.js';
import { SC, setFilter, activeFilters } from '../assets/scratch.js';

const node = (key, depth, table = true) => ({ id: 'p.d.' + key, name: key, key: 'd.' + key, depth, table, read: 'p.d.' + key });

beforeEach(() => {
  IN.v = { key: 'd.orders', name: 'orders', depth: 3, nodes: [node('orders', 0), node('eph', 1, false), node('stg', 1)], edges: [], runs: {} };
  IN.rows = { 'd.orders': { on: true, filter: '' }, 'd.stg': { on: true, filter: '' } };
  IN.applyAll = true;
  IN.global = '';
  IN.results = {};
  IN.search = '';
});

test('apply to all gives every row the global filter', () => {
  setGlobal('id = 7');
  assert.equal(filterOf('d.stg'), 'id = 7');
  assert.equal(sqlOf('d.stg'), 'SELECT * FROM `p.d.stg` WHERE id = 7');
  setRowFilter('d.stg', 'x = 1'); // read only while apply to all is on
  assert.equal(filterOf('d.stg'), 'id = 7');
});

test('turning apply to all off keeps the global filter on each row, to change', () => {
  setGlobal('id = 7');
  setApplyAll(false);
  assert.equal(filterOf('d.stg'), 'id = 7');
  setRowFilter('d.stg', '');
  assert.equal(sqlOf('d.stg'), 'SELECT * FROM `p.d.stg`');
  assert.equal(filterOf('d.orders'), 'id = 7');
});

test('a row with no table has no row, and unticked rows are left out', () => {
  setOn('d.stg', false);
  assert.deepEqual(ticked(), ['d.orders']);
  assert.equal(sqlOf('d.eph'), 'SELECT * FROM `p.d.eph`'); // never asked: the page draws it no row
});

test('a preset fills the rows it names and starts the rest afresh', () => {
  applyPreset({ id: 'abc', global: 'g', applyAll: false, rows: { 'd.stg': { on: false, filter: 'f' } } }, true);
  assert.equal(IN.preset, 'abc');
  assert.deepEqual(IN.rows['d.stg'], { on: false, filter: 'f' });
  assert.deepEqual(IN.rows['d.orders'], { on: true, filter: '' });
});

test("a row's result has its own filters, not Scratch's", () => {
  const ctx = resultOf('d.stg');
  ctx.run = { rows: [], schema: [{ name: 'a', type: 'STRING' }] };
  setFilter(0, 'x', ctx);
  assert.deepEqual(activeFilters(ctx), [[0, 'x']]);
  assert.deepEqual(activeFilters(), []);
  assert.equal(SC.filters[0], undefined);
});

test('the search shows the tables that have it, and ticking all ticks those', () => {
  IN.v.nodes.push({ ...node('stg_pay', 2), read: 'prod.d.stg_pay' });
  IN.rows['d.stg_pay'] = { on: true, filter: '' };
  setSearch('STG');
  assert.deepEqual(shownNodes().map((n) => n.key), ['d.stg', 'd.stg_pay']);
  setSearch('prod.');
  assert.deepEqual(shownNodes().map((n) => n.key), ['d.stg_pay']);
  setAllOn(false);
  assert.deepEqual(ticked(), ['d.orders', 'd.stg']);
  setSearch('');
  setTickedOnly(true);
  assert.deepEqual(shownNodes().map((n) => n.key), ['d.orders', 'd.stg']);
  setSearch('stg');
  assert.deepEqual(shownNodes().map((n) => n.key), ['d.stg']);
  setTickedOnly(false);
});

test('the workspace keeps the page, without its dry runs when short of room', () => {
  Object.assign(IN, { model: 'p.d.orders', open: false, tab: 'graph', sel: 'd.stg', search: 'st', dry: { 'd.stg': { ok: true, bytes: 5 }, 'd.orders': { pending: true } } });
  const w = hooks.inspectorSave(false);
  assert.deepEqual([w.model, w.open, w.tab, w.sel, w.search], ['p.d.orders', false, 'graph', 'd.stg', 'st']);
  assert.deepEqual(Object.keys(w.dry), ['d.stg']);
  assert.deepEqual(hooks.inspectorSave(true).dry, {});
  assert.deepEqual(hooks.inspectorKept(), { model: 'p.d.orders', name: 'orders' });
});

test('cycling goes round the rows that have run, in their order', () => {
  IN.v.nodes.push(node('pay', 2));
  IN.v.runs = { 'd.pay': { phase: 'failed', sql: 'x' }, 'd.orders': { phase: 'queued', sql: 'y' } };
  IN.sel = 'd.stg';
  cycleResult(1);
  assert.equal(IN.sel, 'd.orders');
  cycleResult(1);
  assert.equal(IN.sel, 'd.pay');
  cycleResult(1);
  assert.equal(IN.sel, 'd.orders');
  cycleResult(-1);
  assert.equal(IN.sel, 'd.pay');
  IN.v.runs = {};
});

test('adding a table the page lists ticks and selects it, clearing a search that hides it', async () => {
  setOn('d.stg', false);
  setSearch('orders');
  await addTable('p.d.stg');
  assert.equal(IN.rows['d.stg'].on, true);
  assert.equal(IN.sel, 'd.stg');
  assert.equal(IN.search, '');
});

test("a row's query is split into pane 2's highlight classes", () => {
  const toks = sqlTokens("SELECT * FROM `p.d.t` WHERE id IN (1, 2) AND lower(name) = 'it''s' -- x");
  assert.equal(toks.map(([, t]) => t).join(''), "SELECT * FROM `p.d.t` WHERE id IN (1, 2) AND lower(name) = 'it''s' -- x");
  const kind = (text) => (toks.find(([, t]) => t.trim() === text) || [])[0];
  assert.equal(kind('SELECT'), 'k');
  assert.equal(kind('`p.d.t`'), 'sb');
  assert.equal(kind('lower'), 'nf');
  assert.equal(kind('-- x'), 'c1');
  assert.ok(toks.some(([c, t]) => c === 'mi' && t === '1'));
});

test('a filter is tidied outside its quotes, and one with a comment is left be', () => {
  assert.equal(tidyFilter('order_id IN (\t1001\t, 1002\t,   1003 )', true), 'order_id IN (1001, 1002, 1003)');
  assert.equal(tidyFilter('a = 1  \n\n  AND b IN (\t1 ,\n 2 )\n', true), 'a = 1\nAND b IN (1,\n2)');
  assert.equal(tidyFilter("name = 'a   b' AND  x IN('p' ,'q')", true), "name = 'a   b' AND x IN('p', 'q')");
  assert.equal(tidyFilter("s = 'it\\'s  ,x' ", true), "s = 'it\\'s  ,x'");
  assert.equal(tidyFilter('x = 1 -- note\n AND y = 2', true), 'x = 1 -- note\n AND y = 2');
  assert.equal(tidyFilter('id IN (1,2) ', false), 'id IN (1, 2) ');
});
