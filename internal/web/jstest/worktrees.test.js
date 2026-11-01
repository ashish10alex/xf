// Worktrees (ADR 0032): a worktree's project is listed under its main
// checkout's, in the column's projects tab and the switcher, with the
// worktrees git found that xf hasn't opened; shift+del asks before
// removing one, xf's or another's.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, arrangePicker, handleKey, openNewWorktree, switchBranch } from '../assets/state.js';
import { askRemoveWorktreeHere, nextOpenProject, openByName, pickerKey, projectTabRows } from '../assets/picker.js';

beforeEach(() => {
  Object.assign(S, { mode: 'picker', key: '', open: [], pickerQuery: '', pickerCursor: 0, pickerConfirm: null, pickerWorktrees: null, flash: '' });
});

test('the projects tab puts a worktree under its main checkout', () => {
  S.open = [
    { key: 'shop@feat-x', name: 'shop@feat-x', root: '/r/shop/.xf/worktrees/feat-x', main: '/r/shop', worktree: 'feat-x', own: true },
    { key: 'ads', name: 'ads', root: '/r/ads' },
    { key: 'shop', name: 'shop', root: '/r/shop' },
    { key: 'shop-old', name: 'shop-old', root: '/r/shop-old' },
  ];
  assert.deepEqual(openByName().map((o) => o.key + (o.child ? ' (child)' : '')), ['ads', 'shop', 'shop@feat-x (child)', 'shop-old']);
  // Without its main open, it stands alone, by name.
  S.open = S.open.filter((o) => o.key !== 'shop');
  assert.deepEqual(openByName().map((o) => o.key + (o.child ? ' (child)' : '')), ['ads', 'shop-old', 'shop@feat-x']);
});

test('the projects tab lists an open project\'s worktrees that aren\'t open, after its open ones', () => {
  S.open = [
    { key: 'shop', name: 'shop', root: '/r/shop' },
    { key: 'shop@feat-x', name: 'shop@feat-x', root: '/r/shop/.xf/worktrees/feat-x', main: '/r/shop', worktree: 'feat-x', own: true },
    { key: 'ads', name: 'ads', root: '/r/ads' },
  ];
  S.openWorktrees = { '/r/shop': [{ root: '/r/agent', main: '/r/shop', name: 'shop@agent', branch: 'fix', backend: 'dbt', worktree: 'agent' }] };
  assert.deepEqual(projectTabRows().map((o) => o.key + (o.child ? ' (child)' : '') + (o.found ? ' (found)' : '')),
    ['ads', 'shop', 'shop@feat-x (child)', 'found:/r/agent (child) (found)']);
  // With none open beside it, they come straight under the project.
  S.open = S.open.filter((o) => o.key !== 'shop@feat-x');
  assert.deepEqual(projectTabRows().map((o) => o.key), ['ads', 'shop', 'found:/r/agent']);
  // Cycling the open projects skips them.
  S.key = 'shop';
  assert.equal(nextOpenProject().key, 'ads');
  S.openWorktrees = null;
});

test('the switcher nests worktrees, found ones too, and keeps them on a search', () => {
  S.picker = [
    { key: 'ads', name: 'ads', root: '/r/ads' },
    { key: 'shop', name: 'shop', root: '/r/shop' },
    { key: 'shop@feat-x', name: 'shop@feat-x', root: '/r/shop/.xf/worktrees/feat-x', main: '/r/shop', worktree: 'feat-x', own: true },
  ];
  S.pickerWorktrees = { '/r/shop': [{ root: '/r/agent', path: '/r/agent', main: '/r/shop', name: 'shop@agent', branch: 'fix', backend: 'dbt', worktree: 'agent' }] };
  arrangePicker(false);
  assert.deepEqual(S.picker.map((p) => p.name + (p.child ? ' (child)' : '') + (p.found ? ' (found)' : '')),
    ['ads', 'shop', 'shop@agent (child) (found)', 'shop@feat-x (child)']);
  // Arranged again, the found row isn't doubled.
  arrangePicker(true);
  assert.equal(S.picker.length, 4);
  // A search the found row doesn't match leaves it out.
  S.pickerQuery = 'feat';
  arrangePicker(false);
  assert.ok(!S.picker.some((p) => p.found));
});

test('New worktree… lists another project\'s branches and makes a new one in a worktree', async () => {
  const sent = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (path, init) => {
    sent.push({ path, project: init.headers['X-Xf-Project'], body: init.body ? JSON.parse(init.body) : null });
    const body = path.startsWith('/api/branches') ? { branches: [{ name: 'main', current: true }, { name: 'fix' }], total: 2 } : { ok: false, message: 'stop here' };
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  try {
    Object.assign(S, { mode: 'project', key: 'ads', openIn: 'same-tab', caps: { ...(S.caps || {}), branches: true } });
    await openNewWorktree('shop', 'shop');
    assert.equal(S.overlay, 'branches');
    assert.deepEqual(sent.map((r) => r.project), ['shop']);
    // The checkout's own branch can't open beside it.
    await switchBranch(0);
    assert.match(S.branches.err, /checkout of shop is on main/);
    // enter on a branch opens it in a worktree, asking of shop.
    await switchBranch(1);
    assert.deepEqual(sent.at(-1), { path: '/api/worktree/open', project: 'shop', body: { name: 'fix', new: false } });
    // A name no branch has makes it, in a new worktree.
    S.branches.query = 'ash try';
    S.branches.listedFor = 'ash try';
    await switchBranch(S.branches.list.length);
    assert.deepEqual(sent.at(-1), { path: '/api/worktree/open', project: 'shop', body: { name: 'ash-try', new: true } });
    assert.equal(S.branches.err, 'stop here');
  } finally {
    globalThis.fetch = realFetch;
    S.overlay = null; S.branches = null;
  }
});

test('the projects tab\'s Remove Worktree asks in a dialog of its own, not the switcher', async () => {
  const sent = [];
  const answers = [{ ok: false, kind: 'dirty', message: 'it has changes that removing it loses: mine.sqlx' },
    { ok: false, message: 'kept by git' }, { ok: true, message: 'removed the worktree shop@agent' }];
  const realFetch = globalThis.fetch;
  let answered;
  globalThis.fetch = async (path, init) => {
    sent.push({ path, body: init.body ? JSON.parse(init.body) : null });
    if (path === '/api/worktree/remove') await new Promise((r) => { answered = r; });
    const body = path === '/api/worktree/remove' ? answers.shift() : {};
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  const tick = () => new Promise((r) => setTimeout(r, 0));
  try {
    Object.assign(S, { mode: 'project', key: 'shop', overlay: null });
    const row = { key: '', name: 'shop@agent', root: '/r/agent', path: '~/agent', main: '/r/shop', worktree: 'agent', found: true };
    askRemoveWorktreeHere(row);
    assert.equal(S.overlay, 'confirmRemoveWorktree');
    assert.equal(S.removeWorktree.folder, '~/agent');
    // Any key but y or enter keeps it, and asks nothing of the server.
    handleKey('n');
    assert.equal(S.overlay, null);
    assert.match(S.flash, /kept/);
    assert.deepEqual(sent, []);
    // enter removes it: the dialog stays while it works, and keys wait.
    // Changes it would lose are named, and asked about again.
    askRemoveWorktreeHere(row);
    handleKey('enter');
    await tick();
    assert.equal(S.overlay, 'confirmRemoveWorktree');
    assert.equal(S.removeWorktree.busy, true);
    handleKey('n');
    assert.equal(S.overlay, 'confirmRemoveWorktree');
    answered();
    await tick(); await tick();
    assert.equal(S.overlay, 'confirmRemoveWorktree');
    assert.match(S.removeWorktree.dirty, /mine\.sqlx/);
    assert.equal(S.removeWorktree.err, '');
    // y removes it anyway; a refusal still shows in the dialog.
    handleKey('y');
    await tick();
    answered();
    await tick(); await tick();
    assert.equal(S.overlay, 'confirmRemoveWorktree');
    assert.equal(S.removeWorktree.err, 'kept by git');
    // y tries again; once it is gone, the dialog closes and says so.
    handleKey('y');
    await tick();
    answered();
    await tick(); await tick();
    assert.equal(S.overlay, null);
    assert.match(S.flash, /removed the worktree/);
    const removes = sent.filter((r) => r.path === '/api/worktree/remove').map((r) => r.body);
    assert.deepEqual(removes.map((b) => b.root), ['/r/agent', '/r/agent', '/r/agent']);
    assert.deepEqual(removes.map((b) => b.force), [false, true, true]);
  } finally {
    globalThis.fetch = realFetch;
  }
});

test('shift+del asks before removing a worktree, xf\'s or another\'s, never a main checkout', () => {
  S.picker = [
    { key: 'shop', name: 'shop', root: '/r/shop' },
    { key: 'shop@feat-x', name: 'shop@feat-x', root: '/r/shop/.xf/worktrees/feat-x', main: '/r/shop', worktree: 'feat-x', own: true },
    { key: 'shop@agent', name: 'shop@agent', root: '/r/agent', path: '~/agent', main: '/r/shop', worktree: 'agent' },
  ];
  S.pickerCursor = 0;
  pickerKey('shift+Delete');
  assert.equal(S.pickerConfirm, null);
  assert.match(S.flash, /not in a worktree/);
  S.pickerCursor = 1;
  pickerKey('shift+Delete');
  assert.equal(S.pickerConfirm.remove, true);
  assert.equal(S.pickerConfirm.root, '/r/shop/.xf/worktrees/feat-x');
  assert.equal(S.pickerConfirm.folder, '');
  // Any key but y keeps it.
  pickerKey('n');
  assert.equal(S.pickerConfirm, null);
  // One xf didn't make asks too, naming its folder.
  S.pickerCursor = 2;
  pickerKey('shift+Delete');
  assert.equal(S.pickerConfirm.root, '/r/agent');
  assert.equal(S.pickerConfirm.folder, '~/agent');
  pickerKey('n');
});

test('the switcher\'s footer asks again before losing a worktree\'s changes', async () => {
  const sent = [];
  const answers = [{ ok: false, kind: 'dirty', message: 'it has changes that removing it loses: mine.sqlx' }, { ok: true, message: 'removed' }];
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (path, init) => {
    if (path === '/api/worktree/remove') sent.push(JSON.parse(init.body));
    const body = path === '/api/worktree/remove' ? answers.shift() : [];
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
  const tick = () => new Promise((r) => setTimeout(r, 0));
  try {
    S.picker = [{ key: 'shop@agent', name: 'shop@agent', root: '/r/agent', path: '~/agent', main: '/r/shop', worktree: 'agent' }];
    S.pickerCursor = 0;
    pickerKey('shift+Delete');
    pickerKey('y');
    await tick(); await tick();
    assert.match(S.pickerConfirm.dirty, /mine\.sqlx/);
    pickerKey('y');
    await tick(); await tick();
    assert.equal(S.pickerConfirm, null);
    assert.deepEqual(sent.map((b) => b.force), [false, true]);
  } finally {
    globalThis.fetch = realFetch;
  }
});
