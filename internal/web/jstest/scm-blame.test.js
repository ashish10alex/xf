// Source Control's Blame section (Option C: vertical file minimap + author
// lens): author rows filter the file-order range list; clicking or pressing
// enter on a range jumps to its start line and enables inline git blame.

import { test, beforeEach, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { G, rows, key, clickRow, getDraft, formatRange, shortAge, blameSegments, blameShortName, clearBlameLens } from '../assets/scm.js';
import { F } from '../assets/forge.js';

const st = () => ({ branch: 'feat', head: 'abc', op: '', conflicts: [], staged: [], changes: [], stashes: [] });

const sampleSummary = {
  totalLines: 30,
  people: [
    {
      id: 'fresh',
      author: 'Uncommitted',
      mail: '',
      you: true,
      fresh: true,
      lines: 2,
      time: 0,
      summary: 'Uncommitted changes',
      sha: '',
      ranges: [{ start: 29, end: 30, lines: 2, time: 0, summary: 'Uncommitted changes', sha: '' }],
    },
    {
      id: 'm:alice@co',
      author: 'Alice Chen',
      mail: 'alice@co',
      you: false,
      fresh: false,
      lines: 20,
      time: 2000,
      summary: 'fix filter',
      sha: 'aaaa11112222',
      ranges: [
        { start: 1, end: 12, lines: 12, time: 1000, summary: 'initial', sha: 'aaaa00001111' },
        { start: 21, end: 28, lines: 8, time: 2000, summary: 'fix filter', sha: 'aaaa11112222' },
      ],
    },
    {
      id: 'm:bob@co',
      author: 'Bob',
      mail: 'bob@co',
      you: false,
      fresh: false,
      lines: 8,
      time: 1500,
      summary: 'add join',
      sha: 'bbbb11112222',
      ranges: [{ start: 13, end: 20, lines: 8, time: 1500, summary: 'add join', sha: 'bbbb11112222' }],
    },
  ],
};

let savedGoTo, savedEnable;

beforeEach(() => {
  savedGoTo = hooks.goToLine;
  savedEnable = hooks.enableBlame;
  S.mode = 'project';
  S.sel = 'proj.ds.orders';
  S.view = { id: 'proj.ds.orders', file: 'definitions/orders.sqlx' };
  getDraft();
  F.v = null;
  G.st = st();
  G.open = { ...G.open, conflicts: true, staged: true, changes: true, stashes: false, blame: true, commits: false };
  G.filter = '';
  G.prompt = null;
  G.diff = null;
  G.blameLens = '';
  G.blameActive = null;
  SRC.shown = true;
  SRC.file = 'definitions/orders.sqlx';
  SRC.view = { file: 'definitions/orders.sqlx', base: 'SELECT 1\n' };
  G.blame = { file: 'definitions/orders.sqlx', state: 'ok', error: '', summary: sampleSummary };
});

afterEach(() => {
  hooks.goToLine = savedGoTo;
  hooks.enableBlame = savedEnable;
  SRC.file = '';
  SRC.view = null;
  G.blame = { file: '', state: 'none', error: '', summary: null };
  G.blameLens = '';
  G.blameActive = null;
});

test('Blame section sits after Graph, defaults closed on fresh project, and lists author lens rows + file-order ranges when open', () => {
  const savedBoot = S.boot;
  S.boot = { ...(S.boot || {}), root: '/r/fresh-blame-default' };
  getDraft();
  assert.equal(G.open.blame, false, 'Blame starts closed by default');
  S.boot = savedBoot;
  getDraft();
  G.st = st();
  G.open = { ...G.open, conflicts: true, staged: true, changes: true, stashes: false, blame: true, commits: false };
  G.blame = { file: 'definitions/orders.sqlx', state: 'ok', error: '', summary: sampleSummary };

  const rs = rows();
  const blameIdx = rs.findIndex((r) => r.key === 's:blame');
  const commitsIdx = rs.findIndex((r) => r.key === 's:commits');
  assert.ok(commitsIdx > -1 && blameIdx > commitsIdx, 'Blame sits after Graph');
  assert.equal(rs[blameIdx].sub, 'orders.sqlx');
  assert.equal(rs[blameIdx].meta, '30L');

  const blameRows = rs.slice(blameIdx + 1);
  assert.deepEqual(blameRows.map((r) => [r.key, r.kind]), [
    ['bl:fresh', 'blame'],
    ['bl:m:alice@co', 'blame'],
    ['bl:m:bob@co', 'blame'],
    ['bl:r:1', 'blamerange'],
    ['bl:r:13', 'blamerange'],
    ['bl:r:21', 'blamerange'],
    ['bl:r:29', 'blamerange'],
  ]);
  assert.equal(blameRows[1].pct, 67);
  assert.equal(blameRows[2].pct, 27);
  assert.equal(blameSegments().segments.length, 4);
});

test('clicking an author row toggles the author lens, filtering the range feed while minimap keeps all segments', () => {
  const aliceRow = rows().find((r) => r.key === 'bl:m:alice@co');
  clickRow(aliceRow);
  assert.equal(G.blameLens, 'm:alice@co');

  const rs = rows();
  const ranges = rs.filter((r) => r.kind === 'blamerange');
  assert.deepEqual(ranges.map((r) => [r.range.start, r.lensed]), [[1, true], [21, true]]);
  assert.equal(rs.find((r) => r.key === 'bl:m:alice@co').active, true);
  assert.equal(rs.find((r) => r.key === 'bl:m:bob@co').dimmed, true);
  assert.equal(blameSegments().segments.length, 4, 'minimap still has all file segments');

  // Clicking again clears the lens.
  clickRow(rs.find((r) => r.key === 'bl:m:alice@co'));
  assert.equal(G.blameLens, '');
  assert.equal(rows().filter((r) => r.kind === 'blamerange').length, 4);

  // clearBlameLens also resets it.
  G.blameLens = 'm:bob@co';
  clearBlameLens();
  assert.equal(G.blameLens, '');
});

test('clicking a range row jumps to its start line, closes any diff, records active range, and enables inline blame', () => {
  const jumped = [];
  let enabled = 0;
  hooks.goToLine = (file, action, line, col) => jumped.push({ file, action, line, col });
  hooks.enableBlame = () => { enabled++; };

  G.diff = { side: 'changes', path: 'definitions/orders.sqlx' };
  const rangeRow = rows().find((r) => r.key === 'bl:r:21');
  clickRow(rangeRow);
  assert.equal(G.diff, null, 'closes open diff');
  assert.equal(enabled, 1, 'enables inline git blame');
  assert.deepEqual(G.blameActive, { file: 'definitions/orders.sqlx', start: 21, end: 28 });
  assert.deepEqual(jumped, [{ file: 'definitions/orders.sqlx', action: 'proj.ds.orders', line: 20, col: 0 }]);
});

test('keyboard l/h/enter/esc work with author lens and range rows', () => {
  const jumped = [];
  let enabled = 0;
  hooks.goToLine = (file, action, line, col) => jumped.push({ file, action, line, col });
  hooks.enableBlame = () => { enabled++; };

  // l on an author pins the lens and jumps cursor to their first range.
  G.cursor = 'bl:m:alice@co';
  key('l');
  assert.equal(G.blameLens, 'm:alice@co');
  assert.equal(G.cursor, 'bl:r:1');

  // enter on a range row jumps to its start line and enables inline blame.
  key('enter');
  assert.equal(enabled, 1);
  assert.deepEqual(jumped, [{ file: 'definitions/orders.sqlx', action: 'proj.ds.orders', line: 0, col: 0 }]);

  // h on a range row returns cursor to its author; esc clears the lens.
  key('h');
  assert.equal(G.cursor, 'bl:m:alice@co');
  key('esc');
  assert.equal(G.blameLens, '');
});

test('untracked and loading states show a helpful note', () => {
  G.blame = { file: 'definitions/orders.sqlx', state: 'untracked', error: '', summary: null };
  assert.equal(rows().find((r) => r.key === 'bl:note').text, 'untracked file');

  G.blame = { file: 'other.sqlx', state: 'ok', error: '', summary: sampleSummary };
  assert.equal(rows().find((r) => r.key === 'bl:…').text, 'reading blame…');
});

test('formatRange, shortAge, and blameShortName format compactly', () => {
  assert.equal(formatRange(42, 42), 'L42');
  assert.equal(formatRange(1, 29), 'L1–29');
  assert.equal(blameShortName({ fresh: true, author: 'Uncommitted' }), 'Uncomm.');
  assert.equal(blameShortName({ fresh: false, you: true, author: 'Ashish Alex' }), 'You');
  assert.equal(blameShortName({ fresh: false, you: false, author: 'Kanmi Odutola' }), 'Kanmi');
  const now = 1_000_000_000;
  assert.equal(shortAge(Math.floor(now / 1000) - 10, now), 'now');
  assert.equal(shortAge(Math.floor(now / 1000) - 3 * 86400, now), '3d');
  assert.equal(shortAge(Math.floor(now / 1000) - 2 * 3600, now), '2h');
});
