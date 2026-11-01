// The project picker: its search, a path that opens a project xf has
// not opened yet, and opening a project here or in a new tab.

import { S, arrangePicker, closeListed, copy, get, goToProject, hooks, post, projectURL, quit, redraw, removeListedWorktree, say } from './state.js';

// pickerKey is the picker's keys. Letters go to its search, so the list
// moves with ↑/↓ and ctrl+n/ctrl+p, ctrl+y copies the row's full path,
// del forgets, shift+del removes a worktree (ADR 0032) and ctrl+c quits. While the search is a path, tab completes it.
export function pickerKey(k) {
  // del on an open project, and shift+del on a worktree, ask first: y
  // closes or removes it, any other key keeps it.
  if (S.pickerConfirm) {
    const c = S.pickerConfirm;
    S.pickerConfirm = null;
    if (k === 'y') { if (c.remove) removeListedWorktree(c); else closeListed(c); }
    return true;
  }
  const n = (S.paths || S.picker).length;
  if (S.paths) {
    S.flash = '';
    if (k === 'tab') { completePath(); return true; }
    if (k === 'Delete' || k === 'shift+Delete') return true;
  }
  switch (k) {
    case 'down': S.pickerCursor = Math.min(S.pickerCursor + 1, Math.max(n - 1, 0)); return true;
    case 'up': S.pickerCursor = Math.max(S.pickerCursor - 1, 0); return true;
    case 'pgdown': S.pickerCursor = Math.min(S.pickerCursor + 10, Math.max(n - 1, 0)); return true;
    case 'pgup': S.pickerCursor = Math.max(S.pickerCursor - 10, 0); return true;
    case 'enter': pickProject(S.pickerCursor); return true;
    case 'shift+enter': pickProject(S.pickerCursor, false, true); return true;
    case 'ctrl+y': copyRowPath(S.pickerCursor); return true;
    case 'Delete': forgetProject(S.pickerCursor); return true;
    case 'shift+Delete': askRemoveWorktree(S.picker[S.pickerCursor]); return true;
    case 'esc':
      if (S.pickerQuery) setPickerQuery('');
      else if (S.overlay === 'projects') S.overlay = null;
      return true;
    case 'ctrl+c': if (S.overlay === 'projects') S.overlay = null; else quit(); return true;
  }
  return false;
}

// setPickerQuery lists the projects that every word of q is in, as the
// TUI's picker does (see ui.ProjectMatches).
let pickerSeq = 0;
export async function setPickerQuery(q) {
  S.pickerQuery = q;
  S.pickerCursor = 0;
  redraw();
  const seq = ++pickerSeq;
  try {
    const b = await get('/api/boot?q=' + encodeURIComponent(q));
    if (seq !== pickerSeq) return;
    S.picker = b.picker || [];
    arrangePicker(false);
    // Over a project, enter goes back to the project shown before.
    if (!q && S.overlay === 'projects' && S.picker[0] && S.picker[0].key === S.key && S.picker[1] && S.picker[1].open) S.pickerCursor = 1;
    S.paths = b.paths || null;
    S.pickerTotal = b.total ?? S.pickerTotal;
    S.flash = '';
  } catch (e) { say(e.message); }
  redraw();
}

// pickPath opens the project of a path row, which xf then remembers. A
// row that is no project says why; clicked, a folder row goes into the
// folder, as tab does, since a phone has no tab.
async function pickPath(i, clicked, flip) {
  const r = S.paths[i];
  if (!r || r.more) return;
  if (!r.backend) {
    if (clicked && r.folder) { setPickerQuery(r.path + pathSep()); return; }
    say(r.why || 'not a Dataform or dbt project');
    return;
  }
  openNew(r.root, r.backend, r.path, newTabFor(flip));
}

// openNew opens the project at root, which xf then remembers, in this
// tab or, with newTab, a new one.
async function openNew(root, backend, label, newTab) {
  // A new tab opens now, while the key press lets it, and goes to the
  // project once xf has it.
  const tab = newTab ? window.open('', '_blank') : null;
  if (newTab && !tab) { say(blocked); return; }
  S.flash = 'opening ' + label + '…';
  redraw();
  try {
    const { key } = await post('/api/pick', { root, backend });
    if (tab) { tab.opener = null; tab.location.href = projectURL(key); openedInTab(); } else goToProject(key);
  } catch (e) {
    if (tab) tab.close();
    say(e.message);
  }
}

// newTabFor says whether a pick opens in a new tab: over a project as
// webOpenProjectIn says, on the projects page in this tab; shift+enter
// (flip) does the other.
function newTabFor(flip) {
  const asked = S.overlay === 'projects' && S.openIn === 'new-tab';
  return asked !== !!flip;
}

const blocked = 'the browser blocked the new tab: allow pop-ups for xf web to open projects in one';

// openTab opens url in a new tab, the opener let go, and says when the
// browser blocks it.
function openTab(url) {
  const tab = window.open(url, '_blank');
  if (!tab) { say(blocked); return; }
  tab.opener = null;
  openedInTab();
}

// openedInTab closes the list over the project once a pick went to a new
// tab, which the browser usually brings to the front.
function openedInTab() {
  if (S.overlay === 'projects') S.overlay = null;
  say('opened in a new tab');
}

// completePath is tab on a path: into the selected folder row's folder,
// or as far as the folder rows all agree, or into the folder typed.
function completePath() {
  const q = S.pickerQuery;
  const r = S.paths[S.pickerCursor];
  if (r && r.folder) { setPickerQuery(r.path + pathSep()); return; }
  const folders = [...new Set(S.paths.filter((p) => p.folder).map((p) => p.path))];
  if (folders.length === 1) { setPickerQuery(folders[0] + pathSep()); return; }
  if (folders.length > 1) {
    let common = folders[0];
    for (const f of folders) {
      let j = 0;
      while (j < common.length && j < f.length && common[j].toLowerCase() === f[j].toLowerCase()) j++;
      common = common.slice(0, j);
    }
    if (common.length > q.length) { setPickerQuery(common); return; }
  }
  const typed = S.paths[0];
  if (typed && !typed.folder && typed.root && !/no such folder|not a folder|can't read/.test(typed.note || '') && !/[\\/]$/.test(q)) {
    setPickerQuery(q + pathSep());
  }
}

// pathSep is the separator the path typed uses.
function pathSep() {
  const q = S.pickerQuery;
  return q.includes('\\') && !q.includes('/') ? '\\' : '/';
}

export async function pickProject(i, clicked, flip) {
  if (S.paths) { pickPath(i, clicked, flip); return; }
  const p = S.picker[i];
  if (!p) return;
  // A worktree git found opens as a new project would (ADR 0032).
  if (p.found) { openNew(p.root, p.backend, p.name, newTabFor(flip)); return; }
  // A remembered project opens by its key: a new tab's page opens it.
  if (newTabFor(flip)) { openTab(projectURL(p.key)); return; }
  if (S.overlay === 'projects' && p.key === S.key) { S.overlay = null; return; } // this tab's
  S.flash = 'opening ' + p.name + '…';
  redraw();
  try {
    const { key } = await post('/api/pick', { root: p.root });
    goToProject(key);
  } catch (e) { say(e.message); }
}

// copyRowPath is ctrl+y: the full path of the project, or folder, under
// the cursor.
function copyRowPath(i) {
  const r = (S.paths || S.picker)[i];
  if (!r || !r.root) { say('no path to copy here'); return; }
  copy(r.root, 'copied ' + r.root);
}

// forgetProject is del on a project: an open one closes, once confirmed;
// one that isn't is forgotten.
async function forgetProject(i) {
  const p = S.picker[i];
  if (!p) return;
  if (p.found) { say('xf has not opened ' + p.name + ' yet: there is nothing to forget'); return; }
  if (p.open) { S.pickerConfirm = { key: p.key, name: p.name, unsaved: p.unsaved || [] }; return; }
  try {
    await post('/api/forget', { root: p.root });
    S.picker.splice(i, 1);
    S.pickerTotal--;
    S.pickerCursor = Math.min(S.pickerCursor, Math.max(S.picker.length - 1, 0));
    say('forgot ' + p.name);
  } catch (e) { say(e.message); }
}

// removeQuestion is what asking to remove p's worktree needs, or null,
// said why, when p is in none. Any worktree can be removed, xf's or
// another's (an agent session's), but not a main checkout; one xf didn't
// make is named by its folder, as it lives outside .xf/worktrees.
function removeQuestion(p) {
  if (!p) return null;
  if (!p.worktree) { say(p.name + ' is not in a worktree'); return null; }
  return { remove: true, root: p.root, key: p.key, name: p.name, unsaved: p.unsaved || [], folder: p.own ? '' : p.path || p.root };
}

// askRemoveWorktree is shift+del on a worktree's row in the switcher: it
// asks there, in its footer, before removing it.
export function askRemoveWorktree(p) {
  const q = removeQuestion(p);
  if (q) S.pickerConfirm = q;
}

// askRemoveWorktreeHere is the projects tab's Remove Worktree and the
// palette's "Worktree: remove this worktree": a dialog of its own over
// the page asks, as closing a project's does.
export function askRemoveWorktreeHere(p) {
  const q = removeQuestion(p);
  if (!q) return;
  S.removeWorktree = q;
  S.overlay = 'confirmRemoveWorktree';
}

// switchProject opens the open project key from the column's projects
// list: in this tab or a new one as webOpenProjectIn says, and
// shift+click does the other, as shift+enter does in the switcher. The
// column shows in the project it opens, so the list is there to come
// back by; the palette's list (plain) leaves the column as it is.
export function switchProject(key, flip, plain) {
  const o = S.open.find((x) => x.key === key);
  if (!o || key === S.key) return;
  if (!plain && hooks.showColumnFor) hooks.showColumnFor(o.root);
  if ((S.openIn === 'new-tab') !== !!flip) { openTab(projectURL(key)); return; }
  S.flash = 'opening ' + o.name + '…';
  redraw();
  goToProject(key);
}

// openByName are the projects open in xf web (ADR 0008) by name, as the
// column's projects tab lists them, so a project keeps its place after a
// switch. A worktree's project comes under its main checkout's, when that
// is open, marked child (ADR 0032).
export function openByName() {
  const list = S.open.length ? [...S.open] : S.boot ? [{ key: S.key, name: S.boot.name, root: S.boot.root }] : [];
  const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }) || a.key.localeCompare(b.key);
  const roots = new Set(list.map((o) => o.root));
  const out = [];
  for (const o of list.filter((x) => !(x.main && roots.has(x.main))).sort(byName)) {
    out.push(o);
    for (const k of list.filter((x) => x.main && x.main === o.root).sort(byName)) out.push({ ...k, child: true });
  }
  return out;
}

// projectTabRows are the column's projects tab's rows: openByName, with
// under each open project its repository's worktrees that aren't open,
// marked found, to switch to (ADR 0032). A found row's key is its root's,
// so the cursor can rest on it.
export function projectTabRows() {
  const rows = openByName();
  const found = S.openWorktrees || {};
  const out = [];
  rows.forEach((o, i) => {
    out.push(o);
    const main = o.child ? o.main : o.root;
    const next = rows[i + 1];
    if (next && next.child && next.main === main) return; // after its last open worktree
    for (const f of found[main] || []) out.push({ ...f, key: 'found:' + f.root, found: true, child: true });
  });
  return out;
}

// The projects tab's worktrees are read when it shows, and again once the
// open projects change or 10s on; now reads them at once, after a
// removal.
let wtFor = null, wtAt = 0, wtSeq = 0;
export function readOpenWorktrees(now) {
  const sig = S.open.map((o) => o.key).sort().join('\n');
  if (!now && sig === wtFor && Date.now() - wtAt < 10000) return;
  wtFor = sig; wtAt = Date.now();
  const n = ++wtSeq;
  get('/api/worktrees').then((m) => { if (n === wtSeq) { S.openWorktrees = m; redraw(); } }).catch(() => {});
}

// openFoundRow opens a worktree's project from the projects tab that
// isn't open: in this tab or a new one as webOpenProjectIn says,
// shift+click the other way, with the column on the projects tab there.
export function openFoundRow(o, flip) {
  if (hooks.showColumnFor) hooks.showColumnFor(o.root);
  openNew(o.root, o.backend, o.name, (S.openIn === 'new-tab') !== !!flip);
}

// nextOpenProject is the open project after this one in openByName,
// wrapping round to the first, or null when no other is open.
export function nextOpenProject() {
  const rows = openByName();
  const i = rows.findIndex((o) => o.key === S.key);
  return rows.length < 2 || i < 0 ? null : rows[(i + 1) % rows.length];
}

// cycleProject is "Project: next open project", which goes round the open
// projects one at a time. It always opens in this tab, whatever
// webOpenProjectIn says, since a new tab per step would leave a trail of
// tabs.
export function cycleProject() {
  const o = nextOpenProject();
  if (!o) { say('no other project is open'); return; }
  S.flash = 'opening ' + o.name + '…';
  redraw();
  goToProject(o.key);
}
