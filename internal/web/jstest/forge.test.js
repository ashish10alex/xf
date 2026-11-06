import { test } from 'node:test';
import assert from 'node:assert/strict';
import { F, rows, pill, key, shownTitle, hostsMatching } from '../assets/forge.js';

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

// A draft says so once: its pill, not also a "Draft:" in its title or a
// blocker below it.
test('a draft\'s row drops the title\'s draft marker', () => {
  for (const t of ['Draft: Feat', 'draft:Feat', '[Draft] Feat', '(Draft) Feat', 'WIP: Feat', 'Draft:: Feat']) {
    assert.equal(shownTitle({ title: t, draft: true }), 'Feat', t);
  }
  assert.equal(shownTitle({ title: 'Draft: Feat', draft: false }), 'Draft: Feat', 'only a draft\'s');
  assert.equal(shownTitle({ title: 'Drafting the plan', draft: true }), 'Drafting the plan');
  assert.equal(shownTitle({ title: 'Draft:', draft: true }), 'Draft:', 'never empty');
});

test('a draft\'s notes list its blockers, and no draft blocker', () => {
  F.v = view(null, { number: 42, title: 'Draft: Feat', state: 'open', draft: true, source: 'feat', target: 'develop', approvals: 0, required: 1, unresolved: 0, blockers: ['needs a rebase'] });
  const notes = rows().filter((r) => r.kind === 'mrnote').map((r) => r.text);
  assert.deepEqual(notes, ['0/1 approvals · 0 unresolved', '⚠ needs a rebase', 'no pipeline']);
  assert.equal(pill().text, '!42 draft');
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

// text is what a vnode tree says, and kbds the keys it shows as <kbd>.
const text = (n) => (n == null || typeof n === 'boolean' ? '' : typeof n !== 'object' ? String(n) : Array.isArray(n) ? n.map(text).join('') : text(n.props && n.props.children));
const kbds = (n, out = []) => {
  if (Array.isArray(n)) n.forEach((c) => kbds(c, out));
  else if (n && typeof n === 'object' && n.props) { if (n.type === 'kbd') out.push(text(n)); kbds(n.props.children, out); }
  return out;
};

// The pipeline's row says its number, as a stage's and a job's say their
// names: its icon (and the tooltip) say how it stands.
test('the pipeline row names the pipeline, not its status, and its key is a kbd', async () => {
  const { ForgeRow } = await import('../assets/forge-view.js');
  const p = { id: 7, status: 'failed', url: 'u', stages: [] };
  const row = ForgeRow({ r: { key: 'f:pipe', kind: 'pipe', p }, cur: false, focused: false, onClick() {}, refEl() {} });
  const name = [].concat(row.props.children).find((c) => c && c.props && c.props.class === 'scm-name');
  assert.equal(text(name), 'pipeline #7');
  assert.match(row.props.title, /^pipeline failed/);
  assert.deepEqual(kbds(row), ['O']);
  F.v = { ...view(null), mr: null };
  const create = ForgeRow({ r: rows()[0], cur: false, focused: false, onClick() {}, refEl() {} });
  assert.deepEqual(kbds(create), ['m']);
  F.v = null;
});

// A search of the Settings for the section ("Git hosting") or what it
// holds ("token") showed no host: the list hid while searching.
test('a search of the Settings finds the hosts by the section or by name', () => {
  F.hosts = { hosts: [{ host: 'github.com', kind: 'github' }, { host: 'gitlab.example.com', kind: 'gitlab' }] };
  const names = (q) => (hostsMatching(q) || []).map((h) => h.host);
  assert.deepEqual(names('git hosting'), ['github.com', 'gitlab.example.com']);
  assert.deepEqual(names('token'), ['github.com', 'gitlab.example.com']);
  assert.deepEqual(names('example'), ['gitlab.example.com']);
  assert.equal(hostsMatching('autosave'), null);
  F.hosts = null;
});
