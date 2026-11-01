import { test } from 'node:test';
import assert from 'node:assert/strict';
import { F, rows, pill, key } from '../assets/forge.js';

const view = (pipeline, mr) => ({ state: 'ok', kind: 'gitlab', host: 'gitlab.example.com', path: 'g/p', branch: 'feat', base: 'develop',
  mr: mr || { number: 42, title: 'Feat', state: 'open', source: 'feat', target: 'develop', approvals: 0, required: 1, unresolved: 0, blockers: null },
  pipeline, checkedAt: 1 });

// A pipeline with only trigger jobs came with "stages": null from an older
// xf, and Source Control's list then threw, taking the view down.
test('a pipeline with no stages, or a stage with no jobs, lists without throwing', () => {
  F.v = view({ id: 1, status: 'success', url: 'u', stages: null });
  assert.deepEqual(rows().map((r) => r.kind), ['mr', 'mrnote', 'pipe']);
  F.v = view({ id: 1, status: 'failed', url: 'u', stages: [{ name: 'test', status: 'failed', jobs: null }] });
  assert.deepEqual(rows().map((r) => r.kind), ['mr', 'mrnote', 'pipe', 'stage']);
  assert.equal(pill().text, '!42 ✗');
});

// A trigger job opens like a stage, to the pipeline it started: there
// the work runs, while the parent holds only the trigger job.
test('a trigger job lists the stages and jobs of the pipeline it started', () => {
  const child = { id: 101, status: 'running', url: 'c', stages: [{ name: 'deploy', status: 'running', jobs: [{ name: 'deploy_staging', status: 'running', url: 'j' }] }] };
  F.v = view({ id: 100, status: 'success', url: 'u', stages: [{ name: 'triggers', status: 'running', jobs: [{ name: 'upload', status: 'running', url: 'c', downstream: child }] }] });
  const shown = () => rows().filter((r) => r.kind === 'stage' || r.kind === 'job').map((r) => r.kind + ' ' + (r.stage || r.job).name + ' ' + r.depth);
  assert.deepEqual(shown(), ['stage triggers 0', 'job upload 0', 'stage deploy 1', 'job deploy_staging 1']);
  const trigger = rows().find((r) => r.kind === 'job' && r.down);
  assert.equal(key('h', trigger), true);
  assert.deepEqual(shown(), ['stage triggers 0', 'job upload 0']);
  F.open = {};
  // Done and passed, it starts closed, as a stage does.
  child.status = 'success'; child.stages[0].status = 'success'; child.stages[0].jobs[0].status = 'success';
  F.v.pipeline.stages[0].status = 'success'; F.v.pipeline.stages[0].jobs[0].status = 'success';
  F.open['f:st:triggers'] = true;
  assert.deepEqual(shown(), ['stage triggers 0', 'job upload 0']);
  F.open = {};
  F.v = null;
});

test('no merge request offers to create one, and none on the base branch', () => {
  F.v = { ...view(null), mr: null };
  assert.equal(pill().text, '+ MR');
  assert.equal(rows()[0].kind, 'mrcreate');
  F.v = { ...view(null), mr: null, branch: 'develop' };
  assert.equal(pill(), null);
  F.v = null;
});

// Source Control's rows: git's first, then the merge request's region
// (layout A, ADR 0020), which tab goes to and back from.
test('the merge request rows come last; tab goes there and back, G stays in its region', async () => {
  const sc = await import('../assets/scm.js');
  const { S } = await import('../assets/state.js');
  S.mode = 'project';
  sc.getDraft(); // restore() resets G the first time
  sc.G.st = { branch: 'feat', head: 'abc', conflicts: [], staged: [], changes: [{ path: 'a.sqlx', kind: 'M' }, { path: 'b.sqlx', kind: 'M' }], stashes: [] };
  sc.G.open = { ...sc.G.open, mr: true, changes: true, commits: false };
  F.v = view({ id: 1, status: 'failed', url: 'u', stages: [{ name: 'test', status: 'failed', jobs: [{ name: 'unit', status: 'failed', url: 'j' }] }] });
  const keys = sc.rows().map((r) => r.key);
  const at = keys.indexOf('s:mr');
  assert.ok(at > keys.indexOf('s:commits'), keys.join(' '));
  assert.ok(keys.slice(at).every((k) => k === 's:mr' || k.startsWith('f:')), keys.join(' '));
  assert.ok(keys.slice(0, at).every((k) => !k.startsWith('f:')), keys.join(' '));

  sc.G.cursor = 'changes:b.sqlx';
  sc.key('tab');
  assert.equal(sc.G.cursor, 's:mr');
  sc.key('G');
  assert.equal(sc.G.cursor, 'f:st:test:0', 'G goes to the bottom of the region');
  sc.key('tab');
  assert.equal(sc.G.cursor, 'changes:b.sqlx', 'back to the git row tab left');
  sc.key('shift+tab');
  assert.equal(sc.G.cursor, 'f:st:test:0', 'and back to the region\'s row');
  sc.key('g');
  assert.equal(sc.G.cursor, 's:mr', 'g goes to the top of the region');
  sc.G.cursor = 'changes:a.sqlx';
  sc.key('G');
  assert.equal(sc.G.cursor, 's:commits', 'G in git stays in git');
  sc.key('j');
  assert.equal(sc.G.cursor, 's:mr', 'j goes on into the region');
  F.v = null;
});
