// The palette's query: a file, "#" or "$kind" an action, ">" a command,
// "@" a symbol (docs/adr/0014), and how it ranks the files and commands.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S, rankFiles, fileQuery, actionQuery, commandQuery, symbolQuery, byRecent } from '../assets/state.js';

test('the query says what the palette lists', () => {
  for (const [q, want] of [
    ['', { file: '', action: null }],
    ['orders', { file: 'orders', action: null }],
    ['#', { file: null, action: '' }],
    ['#orders', { file: null, action: 'orders' }],
    ['  #orders', { file: null, action: 'orders' }],
    ['$view orders', { file: null, action: '$view orders' }],
    ['>run', { file: null, action: null, command: 'run' }],
    ['@ref', { file: null, action: null, symbol: 'ref' }],
  ]) {
    S.palette.query = q;
    assert.equal(fileQuery(), want.file, q);
    assert.equal(actionQuery(), want.action, q);
    assert.equal(commandQuery(), want.command ?? null, q);
    assert.equal(symbolQuery(), want.symbol ?? null, q);
  }
  S.palette.query = '';
});

const files = [
  { path: 'README.md' },
  { path: 'definitions/orders.sqlx', git: 'M' },
  { path: 'definitions/sources.js' },
  { path: 'definitions/reporting/orders_daily.sqlx' },
  { path: 'includes/constants.js', git: 'U' },
  { path: 'workflow_settings.yaml' },
];
const paths = (list) => list.map((f) => f.path);

test('an empty query lists first, then git changes, then by path', () => {
  assert.deepEqual(paths(rankFiles(files, '', ['definitions/sources.js', 'README.md'])), [
    'definitions/sources.js', 'README.md',
    'definitions/orders.sqlx', 'includes/constants.js',
    'definitions/reporting/orders_daily.sqlx', 'workflow_settings.yaml',
  ]);
});

test('a name match beats a path match, which beats scattered letters', () => {
  assert.deepEqual(paths(rankFiles(files, 'orders', [])), ['definitions/orders.sqlx', 'definitions/reporting/orders_daily.sqlx']);
  // "reporting" is only in a folder; "rep" scattered is in README's path too.
  assert.deepEqual(paths(rankFiles(files, 'reporting', [])), ['definitions/reporting/orders_daily.sqlx']);
  assert.deepEqual(paths(rankFiles(files, 'def ord', [])), ['definitions/orders.sqlx', 'definitions/reporting/orders_daily.sqlx']);
  // "wfs" is scattered in the name: before a file it's scattered in the path of.
  const r = paths(rankFiles([{ path: 'a/w/f/s.txt' }, { path: 'workflow_settings.yaml' }], 'wfs', []));
  assert.deepEqual(r, ['workflow_settings.yaml', 'a/w/f/s.txt']);
});

test('first files lead within a tier', () => {
  assert.deepEqual(paths(rankFiles(files, 'orders', ['definitions/reporting/orders_daily.sqlx'])),
    ['definitions/reporting/orders_daily.sqlx', 'definitions/orders.sqlx']);
});

test('commands done before come first, the last done first', () => {
  const was = S.commandUses;
  S.commandUses = {
    'Git: Pull': { count: 9, last: '2026-10-01T10:00:00Z' },
    'View: Log': { count: 1, last: '2026-10-20T10:00:00Z' },
  };
  const titles = byRecent([{ title: 'Run' }, { title: 'Git: Pull' }, { title: 'Compile' }, { title: 'View: Log' }]).map((c) => c.title);
  assert.deepEqual(titles, ['View: Log', 'Git: Pull', 'Run', 'Compile']);
  S.commandUses = was;
});
