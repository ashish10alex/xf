import { test } from 'node:test';
import assert from 'node:assert/strict';
import { deferGroups } from '../assets/checks.js';

// Defer to prod's list writes each dataset once, its tables after it.
test('rows group by label head and dataset, in the order sent', () => {
  const g = deferGroups([
    { dev: 'd.a.t1', prod: 'p.a.t1', label: '→ prod', tone: 'prod' },
    { dev: 'd.b.t2', prod: 'p.b.t2', label: '→ prod', tone: 'prod' },
    { dev: 'd.a.t3', prod: 'p.a.t3', label: '→ prod', tone: 'prod' },
    { dev: 'd.a.t4', prod: 'p.a.t4', label: '⚠ prod · changed on this branch', tone: 'warn' },
    { dev: 'd.a.t5', label: '✓ dev · built 8m ago', tone: 'dev' },
    { dev: 'd.a.t6', label: '✓ dev · built in dev', tone: 'dev' },
    { dev: 'nodots', label: '⚠ not built in dev or prod', tone: 'warn' },
  ]);
  assert.deepEqual(g.map((x) => [x.head, x.ds, x.rows.map((r) => [r.dev, r.note])]), [
    ['→ prod', 'p.a', [['d.a.t1', ''], ['d.a.t3', '']]],
    ['→ prod', 'p.b', [['d.b.t2', '']]],
    ['⚠ prod', 'p.a', [['d.a.t4', 'changed on this branch']]],
    ['✓ dev', 'd.a', [['d.a.t5', 'built 8m ago'], ['d.a.t6', 'built in dev']]],
    ['⚠ not built in dev or prod', '', [['nodots', '']]],
  ]);
});
