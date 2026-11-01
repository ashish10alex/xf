// A Checks report the branch has moved on from is checked afresh, not
// shown with a warning: the server keeps a report per branch and returns
// it, stale or not, unless the page asks with refresh.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { openCheck, recheckShowing } from '../assets/checks.js';

let asked = [];
let answer = () => ({});
globalThis.fetch = async (path, init) => {
  const body = JSON.parse(init.body || '{}');
  asked.push({ path, refresh: body.refresh });
  return new Response(JSON.stringify(answer(body)), { headers: { 'Content-Type': 'application/json' } });
};

const report = (signature, extra) => ({ signature, checkedAt: '21:42', ...extra });
const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  asked = [];
  answer = () => report('new');
  S.boot = { ...(S.boot || {}), defaultBranch: 'main' };
  S.caps.impact = true;
  S.caps.cost = true;
  S.flags.api = false;
  S.overlay = null;
  S.checks = null;
  S.state = { ...(S.state || {}), impactSig: 'new' };
  S.impact = null;
});

test('opening a report the branch moved on from checks afresh', async () => {
  S.impact = { report: report('old'), token: '1', api: false, base: S.boot.defaultBranch };
  await openCheck('impact');
  await settle();
  assert.deepEqual(asked.map((a) => a.refresh), [true]);
  assert.equal(S.impact.report.signature, 'new');
});

test('a report that still matches is shown as it is', async () => {
  S.impact = { report: report('new'), token: '1', api: false, base: S.boot.defaultBranch };
  await openCheck('impact');
  assert.equal(asked.length, 0);
});

test('a kept report the server says is stale is checked afresh', async () => {
  answer = (b) => (b.refresh ? report('new') : report('old', { stale: true }));
  await openCheck('impact');
  await settle();
  assert.deepEqual(asked.map((a) => a.refresh), [false, true]);
  assert.equal(S.impact.report.signature, 'new');
  assert.ok(!S.impact.report.stale);
});

test('a compile that moves the branch on rechecks the report showing', async () => {
  S.checks = 'impact';
  S.impact = { report: report('new'), token: '1', api: false, base: S.boot.defaultBranch };
  recheckShowing();
  assert.equal(asked.length, 0);
  S.state = { ...S.state, impactSig: 'newer' };
  answer = () => report('newer');
  recheckShowing();
  await settle();
  assert.deepEqual(asked.map((a) => a.refresh), [true]);
  assert.equal(S.impact.report.signature, 'newer');
});

test('no recheck under a prompt or while one runs', () => {
  S.checks = 'impact';
  S.state = { ...S.state, impactSig: 'newer' };
  S.impact = { report: report('new'), token: '1', api: false, base: S.boot.defaultBranch };
  S.overlay = 'prod';
  recheckShowing();
  S.overlay = null;
  S.impact.loading = true;
  recheckShowing();
  assert.equal(asked.length, 0);
});
