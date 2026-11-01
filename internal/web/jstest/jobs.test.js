// The jobs table's search and sort (log-tabs.js): / keeps the rows whose
// target, state or failure has every word, S and the headers sort.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { jobsRows, jobsTableKey, nextJobsSort, sortJobsBy } from '../assets/log-tabs.js';

const table = {
  rows: [
    { target: 'b.calendar', state: '✓ succeeded', raw: 'SUCCEEDED', durationMs: 3200, bytes: 20 << 20, costUsd: 0.0001, hasDuration: true, hasJob: true },
    { target: 'a.cars', state: '▶ running', raw: 'RUNNING' }, // a running job's duration is still growing: none to sort by
    { target: 'c.holds', state: '✗ failed', raw: 'FAILED', failure: 'Access Denied', durationMs: 4000, bytes: 40 << 20, costUsd: 0.0002, hasDuration: true, hasJob: true },
    { target: 'a.orders', state: '· pending', raw: 'PENDING' },
  ],
};
const targets = () => jobsRows(table).map((r) => r.target);

function reset() {
  S.logTab = 'jobs';
  S.run = { ran: true, jobs: table };
  S.jobsQuery = '';
  S.jobsSort = { col: '', desc: false };
  S.jobsCursor = 0;
}

test('search keeps the rows with every word, in any field', () => {
  reset();
  assert.deepEqual(targets(), ['b.calendar', 'a.cars', 'c.holds', 'a.orders']);
  S.jobsQuery = 'A.';
  assert.deepEqual(targets(), ['a.cars', 'a.orders']);
  S.jobsQuery = 'running a';
  assert.deepEqual(targets(), ['a.cars']);
  S.jobsQuery = 'denied';
  assert.deepEqual(targets(), ['c.holds']);
  assert.equal(jobsTableKey('esc'), true);
  assert.equal(S.jobsQuery, '');
  assert.equal(jobsTableKey('esc'), false, 'with no search, esc is the global one');
});

test('numbers sort biggest first, jobs not looked up last either way', () => {
  reset();
  sortJobsBy('billed');
  assert.deepEqual(S.jobsSort, { col: 'billed', desc: true });
  assert.deepEqual(targets(), ['c.holds', 'b.calendar', 'a.cars', 'a.orders']);
  sortJobsBy('billed');
  assert.deepEqual(targets(), ['b.calendar', 'c.holds', 'a.cars', 'a.orders']);
  sortJobsBy('billed');
  assert.deepEqual(S.jobsSort, { col: '', desc: false });
  assert.deepEqual(targets(), ['b.calendar', 'a.cars', 'c.holds', 'a.orders']);
});

test('state sorts what needs looking at first; S goes round', () => {
  reset();
  sortJobsBy('state');
  assert.deepEqual(targets(), ['a.cars', 'c.holds', 'a.orders', 'b.calendar']);
  reset();
  nextJobsSort();
  assert.deepEqual(S.jobsSort, { col: 'duration', desc: true });
  for (let i = 0; i < 5; i++) nextJobsSort();
  assert.equal(S.jobsSort.col, 'state');
  nextJobsSort();
  assert.equal(S.jobsSort.col, '');
});

test('sorting puts the cursor on the top of the new order', () => {
  reset();
  S.jobsCursor = 2; // c.holds
  sortJobsBy('billed');
  assert.equal(S.jobsCursor, 0);
  assert.equal(jobsRows(table)[S.jobsCursor].target, 'c.holds');
  S.jobsCursor = 3;
  nextJobsSort();
  assert.equal(S.jobsCursor, 0);
});

test('a job that billed nothing is the smallest, not one not looked up', () => {
  reset();
  const t = {
    rows: [
      { target: 'big', raw: 'SUCCEEDED', bytes: 5e9, costUsd: 0.03, hasJob: true, hasDuration: true, durationMs: 9000 },
      { target: 'cached', raw: 'SUCCEEDED', hasJob: true, hasDuration: true }, // 0 B, 0 ms: omitted by the server
      { target: 'small', raw: 'SUCCEEDED', bytes: 10 << 20, costUsd: 0.0001, hasJob: true, hasDuration: true, durationMs: 100 },
      { target: 'unread', raw: 'SUCCEEDED' },
    ],
  };
  S.run = { ran: true, jobs: t };
  const order = () => jobsRows(t).map((r) => r.target);
  sortJobsBy('billed');
  assert.deepEqual(order(), ['big', 'small', 'cached', 'unread']);
  sortJobsBy('billed');
  assert.deepEqual(order(), ['cached', 'small', 'big', 'unread']);
  S.jobsSort = { col: '', desc: false };
  sortJobsBy('cost');
  sortJobsBy('cost');
  assert.deepEqual(order(), ['cached', 'small', 'big', 'unread']);
  S.jobsSort = { col: '', desc: false };
  sortJobsBy('duration');
  sortJobsBy('duration');
  assert.deepEqual(order(), ['cached', 'small', 'big', 'unread']);
});
