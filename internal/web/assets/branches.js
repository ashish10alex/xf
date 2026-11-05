// The branch: the indicator's parts, B's switcher and b's base picker
// (the branch R, I and C compare with), as the TUI's.

import { openCheck, requestChanged } from './checks.js';
import { S, get, getAs, goToProject, hooks, post, postAs, projectURL, redraw, say } from './state.js';
import { savesWith, discardsWith } from './vim-mode.js';

// The status line's branch is read at start, when a compile starts, when
// the page gets focus back, and is sent by the server when HEAD changes
// and after a switch or fetch (see web/branch.go).
let branchSeq = 0;
export function readBranch() {
  if (S.mode !== 'project' || !S.caps.branches) return;
  const seq = ++branchSeq;
  get('/api/branch').then((v) => { if (seq === branchSeq) { S.branch = v.parts; redraw(); } }).catch(() => {});
}

// onBranch takes the branch an event says, over any read still out.
export function onBranch(parts) {
  branchSeq++;
  S.branch = parts;
}
window.addEventListener('focus', readBranch);
document.addEventListener('visibilitychange', () => { if (!document.hidden) readBranch(); });

// branchRunBlocker is ui.BranchRunBlocker: a local run reads files as it
// goes, so a switch waits for it. An API run compiles the pushed branch.
const branchRunBlocker = 'a run is in progress: a switch would change its files as it runs (ctrl+c cancels it)';
const localRun = () => !!(S.run && S.run.running && !S.run.api);

// B: the branch switcher.
export async function openBranches() {
  if (!S.caps.branches) { say('switching branch is not available'); return; }
  if (localRun()) { say(branchRunBlocker); return; }
  S.zoomed = false;
  S.branches = { query: '', list: [], total: 0, note: '', detached: '', cursor: 0, loading: true, busy: '', err: '', listedFor: null };
  S.overlay = 'branches';
  redraw();
  await listBranches(false);
}

// openNewWorktree is the projects tab's "New worktree…" on an open
// project, this tab's or another's (ADR 0032): B's list of that project's
// branches, where enter opens one in a worktree and a name no branch has
// makes it, from that checkout's HEAD, in a new one. Nothing here changes,
// so neither a run nor unsaved changes hold it up.
export async function openNewWorktree(key, name) {
  if (!S.caps.branches) { say('worktrees are not available'); return; }
  S.zoomed = false;
  S.branches = { query: '', list: [], total: 0, note: '', detached: '', cursor: 0, loading: true, busy: '', err: '', listedFor: null,
    worktree: true, project: key, projectName: name };
  S.overlay = 'branches';
  redraw();
  await listBranches(false);
}

// keyOf is the project the list's requests name: the projects tab's, or
// this tab's.
const keyOf = (b) => b.project || S.key;

let branchesSeq = 0;
// listBranches lists the branches the query matches; keep keeps the
// cursor on its branch (after a fetch).
async function listBranches(keep) {
  const b = S.branches;
  if (!b) return;
  const seq = ++branchesSeq;
  const cur = keep && b.list[b.cursor] ? b.list[b.cursor].name : null;
  try {
    const r = await getAs(keyOf(b), '/api/branches?q=' + encodeURIComponent(b.query));
    if (seq !== branchesSeq || S.branches !== b) return;
    if (r.notGit) {
      S.overlay = null; S.branches = null;
      say('not a git repository: there are no branches to switch to');
      return;
    }
    b.loading = false;
    b.list = r.branches; b.total = r.total; b.note = r.note || ''; b.detached = r.detached || ''; b.head = r.head || '';
    b.listedFor = b.query;
    if (r.error) b.err = r.error;
    b.cursor = cur ? Math.max(b.list.findIndex((x) => x.name === cur), 0) : 0;
  } catch (e) {
    if (S.branches === b) { b.loading = false; b.err = e.message; }
  }
  redraw();
}

export function setBranchesQuery(q) {
  S.branches.query = q;
  S.branches.cursor = 0;
  listBranches(false);
}

export function moveBranches(n) {
  const b = S.branches;
  const last = b.list.length - (newBranchName(b) ? 0 : 1);
  b.cursor = Math.min(Math.max(b.cursor + n, 0), Math.max(last, 0));
}

// newBranchName is the branch the query names when no branch has that
// name, which the switcher's last row makes (git switch -c), as VS Code's
// does: spaces become dashes. "" while the list is of another query.
export function newBranchName(b) {
  const name = (b.query || '').trim().replace(/\s+/g, '-');
  if (!name || b.loading || b.listedFor !== b.query) return '';
  return b.list.some((r) => r.name === name) ? '' : name;
}

// switchBranch switches to the branch under the cursor, or i's; on the
// last row, it makes the branch the query names. For "New worktree…" it
// opens them in a worktree instead.
export async function switchBranch(i) {
  const b = S.branches;
  if (!b || b.busy) return;
  if (b.naming) return nameWorktree();
  if (b.worktree) return openWorktree(i);
  if (i !== undefined) b.cursor = i;
  const row = b.list[b.cursor];
  if (!row) {
    const name = newBranchName(b);
    if (name && b.cursor === b.list.length) await createBranch(b, name);
    return;
  }
  if (row.current) { S.overlay = null; S.branches = null; say('already on ' + row.name); return; }
  if (localRun()) { b.err = branchRunBlocker; redraw(); return; }
  if (hooks.sourceHolds && hooks.sourceHolds()) {
    b.err = 'the source pane has unsaved changes: save them (' + savesWith() + ') or discard them (' + discardsWith() + ') before you switch';
    redraw();
    return;
  }
  b.busy = 'switching to ' + row.name; b.err = '';
  redraw();
  try {
    const r = await post('/api/branch/switch', { name: row.name });
    if (S.branches !== b) return;
    b.busy = '';
    if (r.ok) { S.overlay = null; S.branches = null; say(r.message); } else b.err = r.message;
  } catch (e) {
    if (S.branches === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// worktreeFolder is worktree.Folder: the folder xf makes a worktree
// named name in, xf_ and name with what a folder can't hold turned to -.
const slug = (s) => s.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^[-.]+|[-.]+$/g, '') || 'worktree';
export const worktreeFolder = (name) => 'xf_' + slug(slug(name).replace(/^xf_/, ''));

// openWorktree is shift+enter: the branch under the cursor, or i's,
// opened as a project of its own in a git worktree (ADR 0032): the
// checkout it is in already, or a new one in the main checkout's
// .xf/worktrees, which asks its name first (nameWorktree). This checkout
// doesn't change, so neither a run nor unsaved changes hold it up. It
// opens in this tab or a new one as webOpenProjectIn says. On the last
// row of "New worktree…" it makes the branch the query names, in a new
// worktree.
export async function openWorktree(i) {
  const b = S.branches;
  if (!b || b.busy || b.naming) return;
  if (i !== undefined) b.cursor = i;
  const made = b.worktree && b.cursor === b.list.length ? newBranchName(b) : '';
  const row = made ? { name: made } : b.list[b.cursor];
  if (!row) return;
  if (row.current) { b.err = (b.worktree ? 'the checkout of ' + b.projectName + ' is on ' : 'this checkout is on ') + row.name + ': pick another branch to open beside it'; redraw(); return; }
  if (row.worktree) { await makeWorktree(b, row.name, false, ''); return; }
  // A new worktree: its name first, the branch's to start with.
  const name = slug(row.name);
  b.naming = { branch: row.name, made: !!made, name, placeholder: name };
  b.err = '';
  redraw();
}

// setWorktreeName is the name field's text.
export function setWorktreeName(text) {
  if (S.branches && S.branches.naming) { S.branches.naming.name = text; redraw(); }
}

// cancelWorktreeName goes back from the name to the list (esc).
export function cancelWorktreeName() {
  if (S.branches && S.branches.naming && !S.branches.busy) { S.branches.naming = null; S.branches.err = ''; redraw(); }
}

// nameWorktree makes the worktree the name field names (enter): in
// xf_ and the name, or the branch's when the field is empty.
export async function nameWorktree() {
  const b = S.branches;
  if (!b || !b.naming || b.busy) return;
  const n = b.naming;
  await makeWorktree(b, n.branch, n.made, worktreeFolder(n.name.trim() || n.placeholder));
}

// makeWorktree asks xf for branch's worktree, made in folder when it
// has none, and opens its project.
async function makeWorktree(b, branch, made, folder) {
  // A new tab opens now, while the key press lets it, and goes to the
  // project once xf has it.
  const tab = S.openIn === 'new-tab' ? window.open('', '_blank') : null;
  if (S.openIn === 'new-tab' && !tab) { b.err = 'the browser blocked the new tab: allow pop-ups for xf web to open projects in one'; redraw(); return; }
  b.busy = made ? 'making the branch ' + branch + ' in the worktree ' + folder : folder ? 'making the worktree ' + folder + ' for ' + branch : 'opening ' + branch;
  b.err = '';
  redraw();
  try {
    const r = await postAs(keyOf(b), '/api/worktree/open', { name: branch, new: made, ...(folder ? { folder } : {}) });
    if (S.branches === b) b.busy = '';
    if (!r.ok) {
      if (tab) tab.close();
      if (S.branches === b) b.err = r.message;
    } else {
      if (S.branches === b) { S.overlay = null; S.branches = null; }
      // From the projects tab, the column shows that tab in the project it
      // opens, as a switch from there does.
      if (b.worktree && r.root && hooks.showColumnFor) hooks.showColumnFor(r.root);
      if (tab) { tab.opener = null; tab.location.href = projectURL(r.key); say(r.message + ' · in a new tab'); } else { say(r.message); goToProject(r.key); }
    }
  } catch (e) {
    if (tab) tab.close();
    if (S.branches === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// createBranch makes branch name at HEAD and switches to it: the
// working tree stays as it is, so unsaved changes may stay too.
async function createBranch(b, name) {
  if (localRun()) { b.err = branchRunBlocker; redraw(); return; }
  b.busy = 'making ' + name; b.err = '';
  redraw();
  try {
    const r = await post('/api/scm/branch', { name, from: '' });
    if (S.branches !== b) return;
    b.busy = '';
    if (r.ok) { S.overlay = null; S.branches = null; say(r.message); } else b.err = r.message;
  } catch (e) {
    if (S.branches === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// fetchBranches fetches origin, then lists the branches again.
export async function fetchBranches() {
  const b = S.branches;
  if (!b || b.busy) return;
  b.busy = 'fetching origin'; b.err = '';
  redraw();
  try {
    const r = await postAs(keyOf(b), '/api/branch/fetch', {});
    if (S.branches !== b) return;
    b.busy = '';
    if (!r.ok) b.err = r.message; else await listBranches(true);
  } catch (e) {
    if (S.branches === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// b: the base, the branch R, I and C compare with (defaultBranch). The
// picker lists the branches but the checked-out one, the base first; a
// pick saves it for the project and applies at once (the "base" event).
// It opens over R or the Checks page, and esc goes back to them.
export async function openBase() {
  if (!S.caps.changed || !S.caps.branches) { say('changing the base is not available'); return; }
  S.zoomed = false;
  const back = S.overlay === 'changed' ? 'changed' : null;
  S.base = { query: '', list: [], total: 0, cursor: 0, loading: true, busy: '', err: '', back };
  S.overlay = 'base';
  redraw();
  await listBases(false);
}

export function closeBase() {
  const back = S.base ? S.base.back : null;
  S.base = null;
  S.overlay = back && S[back] ? back : null;
}

let basesSeq = 0;
// listBases lists the branches the query matches; keep keeps the cursor
// on its branch (after a fetch).
async function listBases(keep) {
  const b = S.base;
  if (!b) return;
  const seq = ++basesSeq;
  const cur = keep && b.list[b.cursor] ? b.list[b.cursor].name : null;
  try {
    const r = await get('/api/bases?q=' + encodeURIComponent(b.query));
    if (seq !== basesSeq || S.base !== b) return;
    if (r.notGit) { closeBase(); say('not a git repository: there are no branches to compare with'); redraw(); return; }
    b.loading = false;
    b.list = r.branches; b.total = r.total; b.err = r.error || '';
    b.cursor = cur ? Math.max(b.list.findIndex((x) => x.name === cur), 0) : 0;
  } catch (e) {
    if (S.base === b) { b.loading = false; b.err = e.message; }
  }
  redraw();
}

export function setBaseQuery(q) {
  S.base.query = q;
  S.base.cursor = 0;
  listBases(false);
}

export function moveBase(n) {
  const b = S.base;
  b.cursor = Math.min(Math.max(b.cursor + n, 0), Math.max(b.list.length - 1, 0));
}

// pickBase makes the branch under the cursor, or i's, the base.
export async function pickBase(i) {
  const b = S.base;
  if (!b || b.busy) return;
  if (i !== undefined) b.cursor = i;
  const row = b.list[b.cursor];
  if (!row) return;
  b.busy = 'comparing with ' + row.name; b.err = '';
  redraw();
  try {
    const r = await post('/api/base', { name: row.name });
    if (S.base !== b) return;
    b.busy = '';
    if (r.ok) { closeBase(); say(r.message); onBase(row.name); } else b.err = r.message;
  } catch (e) {
    if (S.base === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// fetchBases fetches origin, then lists the branches again.
export async function fetchBases() {
  const b = S.base;
  if (!b || b.busy) return;
  b.busy = 'fetching origin'; b.err = '';
  redraw();
  try {
    const r = await post('/api/branch/fetch', {});
    if (S.base !== b) return;
    b.busy = '';
    if (!r.ok) b.err = r.message; else await listBases(true);
  } catch (e) {
    if (S.base === b) { b.busy = ''; b.err = e.message; }
  }
  redraw();
}

// onBase takes a new base, from this page or another: R and the Checks
// page, when open, compare with it.
export function onBase(name) {
  if (!name || name === S.boot.defaultBranch) return;
  S.boot.defaultBranch = name;
  if (S.changed && (S.overlay === 'changed' || (S.base && S.base.back === 'changed'))) requestChanged();
  if (S.checks) openCheck(S.checks);
  redraw();
}

export function baseKey(k) {
  switch (k) {
    case 'esc': case 'q': closeBase(); return true;
    case 'enter': pickBase(); return true;
    case 'ctrl+r': fetchBases(); return true;
    case 'j': case 'down': moveBase(1); return true;
    case 'k': case 'up': moveBase(-1); return true;
  }
  return true;
}

// branchesKey has the switcher's keys while its query hasn't (a click
// took the focus away).
export function branchesKey(k) {
  if (S.branches && S.branches.naming) {
    if (k === 'enter') nameWorktree();
    else if (k === 'esc' || k === 'q') cancelWorktreeName();
    return true;
  }
  switch (k) {
    case 'esc': case 'q': S.overlay = null; S.branches = null; return true;
    case 'enter': switchBranch(); return true;
    case 'shift+enter': openWorktree(); return true;
    case 'ctrl+r': fetchBranches(); return true;
    case 'j': case 'down': moveBranches(1); return true;
    case 'k': case 'up': moveBranches(-1); return true;
  }
  return true;
}
