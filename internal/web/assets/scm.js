// Source Control (ADR 0007): the repository's changes, stashes and
// recent commits, in the far-left column the file tree shows in (tree.js
// keeps which view it shows). ctrl+g shows it, and its keys are lazygit's
// files panel's: space stages, x discards, c goes to the commit box, P
// pushes, p pulls. Enter shows a file's difference in the source pane
// (see diff-view.js); gsh stages the change under the cursor there.
//
// The server lists git's status (GET /api/scm) and says when it may have
// changed (the "scm" event, which also says what git command runs); the
// page lists it again then, when files change and on focus, while the
// view shows.

import { S, get, post, redraw, hooks, setFocus, onMac, copy, say } from './state.js';
import { SRC, checkDisk } from './source.js';
import { T } from './tree.js';
import { setBase, unstagedText } from './git-hunks.js';
import { part } from './workspace.js';
import * as fg from './forge.js';
import { unsavedIn } from './vim-mode.js';

export const G = {
  loaded: false,
  error: '',
  st: null,        // scmView from the server
  busy: '',        // the git command running, as the header says it
  failed: '',      // the last command's error, red in the header
  cursor: '',      // the row the keys act on, by key
  open: { mr: true, conflicts: true, staged: true, changes: true, stashes: false, commits: true },
  filter: '',
  filtering: false,
  commits: [], more: false, logLoaded: false, logHead: '',
  commitFiles: {}, // sha → {files, parent}
  stashFiles: {},  // index → files
  // prompt is what a key asks: {kind: 'discard' | 'drop' | 'abort' |
  // 'commitAll' | 'branch' | 'stash', ...}; branch and stash have an input.
  prompt: null,
  amend: false,    // the box amends HEAD
  output: null,    // the git output's entries, while it shows
  // diff is the difference the source pane shows: {side, path, file,
  // rev, base, v (diffView), live, armed, under}.
  diff: null,
  // diffFlip is t's view, {style, mode}: it holds while diffStyle is
  // still style (see diffMode).
  diffFlip: null,
};

export const scmKey = 'ctrl+g';

// ---- what the browser keeps, per project ----

const root = () => (S.boot && S.boot.root) || '';
const storeKey = () => 'xf-web:scm:' + root();
let loadedFor = null;
let draft = '';

function restore() {
  if (loadedFor === root()) return;
  loadedFor = root();
  Object.assign(G, { loaded: false, error: '', st: null, busy: '', failed: '', cursor: '', filter: '', filtering: false,
    commits: [], more: false, logLoaded: false, logHead: '', commitFiles: {}, stashFiles: {}, prompt: null, amend: false, output: null, diff: null });
  G.open = { mr: true, conflicts: true, staged: true, changes: true, stashes: false, commits: true };
  draft = '';
  try {
    const v = JSON.parse(localStorage.getItem(storeKey()) || '{}');
    if (v.open) Object.assign(G.open, v.open);
    draft = v.draft || '';
  } catch { /* no storage */ }
  G.open.changes = true;
}

// openChanges opens the Changes section: it opens whenever Source
// Control shows, so closing it lasts only while the view stays.
export function openChanges() { restore(); G.open.changes = true; }

function keep() {
  try {
    const open = {};
    for (const k of ['mr', 'conflicts', 'staged', 'stashes', 'commits']) open[k] = !!G.open[k];
    localStorage.setItem(storeKey(), JSON.stringify({ open, draft }));
  } catch { /* no storage */ }
}

// The commit box's draft, kept until it is committed.
export const getDraft = () => { restore(); return draft; };
let draftTimer = 0;
export function setDraft(text) {
  draft = text;
  clearTimeout(draftTimer);
  draftTimer = setTimeout(keep, 300);
}

// ---- reading ----

export const shown = () => T.shown && T.view === 'scm';

let seq = 0, timer = 0;

// refresh lists the status again, soon: events come in bursts. It reads
// even with the view hidden, for the column's count.
export function refresh() {
  if (S.mode !== 'project') return;
  clearTimeout(timer);
  timer = setTimeout(load, 80);
}

async function load() {
  restore();
  const n = ++seq;
  try {
    const v = await get('/api/scm');
    if (n !== seq) return;
    G.st = v;
    G.busy = v.busy || '';
    G.error = v.error || '';
    G.loaded = true;
    if (G.open.commits && (!G.logLoaded || G.logHead !== logKey(v))) loadLog(false);
    keepStashes(v.stashes);
    if (!G.cursor || !rows().some((r) => r.key === G.cursor)) G.cursor = firstRow();
  } catch (e) {
    if (n !== seq) return;
    G.error = e.message;
  }
  redraw();
}

// keepStashes forgets what was read of a stash that is gone, or whose
// index another stash has now (a pop renumbers them): its files, whether
// its row is open, and its difference.
let stashIds = {};
function keepStashes(list) {
  const ids = {};
  for (const s of list) ids[s.index] = s.subject + '\u0000' + s.date;
  for (const i of Object.keys(stashIds)) {
    if (ids[i] === stashIds[i]) continue;
    delete G.stashFiles[i];
    delete G.open['st:' + i];
    if (G.diff && G.diff.side === 'stash' && G.diff.rev === i) G.diff = null;
  }
  stashIds = ids;
}

// logKey is what the log shows of the status: the commits (head) and which
// of them are pushed (a push moves ahead and behind, not head).
const logKey = (st) => st ? [st.head, st.upstream, st.ahead, st.behind].join(' ') : '';

// loadLog reads the commits again, or the next page. A read that a newer
// reload has overtaken is dropped, so an old answer never stays on screen.
let logSeq = 0;
async function loadLog(more) {
  const n = more ? logSeq : ++logSeq;
  const key = logKey(G.st);
  try {
    const v = await get('/api/scm/log?skip=' + (more ? G.commits.length : 0));
    if (n !== logSeq) return;
    G.commits = more ? G.commits.concat(v.commits) : v.commits;
    G.more = v.more;
    G.logLoaded = true;
    if (!more) G.logHead = key;
  } catch (e) { say(e.message); }
  redraw();
}

// count is how many files have changed, for the column's header.
export function count() {
  const st = G.st;
  if (!st) return 0;
  const seen = new Set();
  for (const f of [...st.conflicts, ...st.staged, ...st.changes]) seen.add(f.path);
  return seen.size;
}

// ---- a commit's row ----

// initials is an author's badge: the first letters of their first and
// last names ("Kanmi Oyebola" is KO), or a one-word name's first two
// ("Sam" is SA).
export function initials(name) {
  const words = String(name || '').trim().split(/\s+/).filter(Boolean);
  if (!words.length) return '';
  const s = words.length === 1 ? [...words[0]].slice(0, 2).join('') : [...words[0]][0] + [...words[words.length - 1]][0];
  return s.toUpperCase();
}

// commitSubject is how a commit's row says its subject: a merge as the
// branch it brought in, which is what tells merges apart.
export function commitSubject(s) {
  s = String(s || '');
  let m = /^Merge pull request #\d+ from \S+?\/(\S+)/.exec(s);
  if (m) return { merge: true, text: m[1] };
  m = /^Merge (?:remote-tracking )?branch '([^']+)'/.exec(s);
  if (m) return { merge: true, text: m[1] };
  return { merge: false, text: s };
}

// ---- the rows ----

// A row is {key, kind, ...}: a section's header ('section'), a changed
// file ('file', its section), a stash ('stash'), a stash's or commit's
// file ('sfile', 'cfile'), a commit ('commit'), or 'more' commits.
const base = (p) => p.slice(p.lastIndexOf('/') + 1);
const dir = (p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');

// shownPath is a file's path as the view says it: from the project when
// it is in it, else from the repository's top.
export const shownPath = (f) => f.file || f.path;

function matches(f) {
  const words = G.filter.toLowerCase().split(/\s+/).filter(Boolean);
  const p = shownPath(f).toLowerCase();
  return words.every((w) => p.includes(w));
}

// fileRow is a file's row: git is how it changed (f.kind: M, A, D, U, C).
function fileRow(key, kind, f, more) {
  return { key, kind, path: f.path, file: f.file || '', git: f.kind, xy: f.xy || '', name: base(shownPath(f)), dir: dir(shownPath(f)), ...more };
}

export function rows() {
  const st = G.st;
  if (!st || st.notGit) return [];
  const out = [];
  const files = (id, label, list) => {
    if (id === 'conflicts' && !list.length) return;
    const shownList = list.filter(matches);
    out.push({ key: 's:' + id, kind: 'section', id, label, count: list.length, open: !!G.open[id] });
    if (!G.open[id]) return;
    for (const f of shownList) out.push(fileRow(id + ':' + f.path, 'file', f, { section: id }));
  };
  files('conflicts', 'Merge Changes', st.conflicts);
  files('staged', 'Staged Changes', st.staged);
  files('changes', 'Changes', st.changes);
  out.push({ key: 's:stashes', kind: 'section', id: 'stashes', label: 'Stashes', count: st.stashes.length, open: !!G.open.stashes });
  if (G.open.stashes) {
    for (const s of st.stashes) {
      const k = 'st:' + s.index;
      out.push({ key: k, kind: 'stash', ...s, open: !!G.open[k] });
      if (G.open[k]) {
        const fs = G.stashFiles[s.index];
        if (!fs) out.push({ key: k + ':…', kind: 'note', text: 'listing…' });
        else for (const f of fs.filter(matches)) out.push(fileRow(k + ':' + f.path, 'sfile', f, { stash: s.index }));
      }
    }
  }
  out.push({ key: 's:commits', kind: 'section', id: 'commits', label: 'Recent commits', count: -1, open: !!G.open.commits });
  if (G.open.commits) {
    if (!G.logLoaded) out.push({ key: 'c:…', kind: 'note', text: 'listing…' });
    for (const c of G.commits) {
      const k = 'c:' + c.sha;
      out.push({ key: k, kind: 'commit', ...c, open: !!G.open[k] });
      if (G.open[k]) {
        const cf = G.commitFiles[c.sha];
        if (!cf) out.push({ key: k + ':…', kind: 'note', text: 'listing…' });
        else for (const f of cf.files.filter(matches)) out.push(fileRow(k + ':' + f.path, 'cfile', f, { sha: c.sha, parent: cf.parent }));
      }
    }
    if (G.more) out.push({ key: 'c:more', kind: 'more' });
  }
  // The branch's merge request and pipeline (forge.js) last: the view
  // draws them in a region docked below git's (inDock), behind a sash.
  if (fg.on()) {
    out.push({ key: 's:mr', kind: 'section', id: 'mr', label: fg.longWord(), count: -1, open: !!G.open.mr });
    if (G.open.mr) out.push(...fg.rows());
  }
  return out;
}

// inDock says r is in the merge request's region, below git's: its header
// bar (s:mr) and its rows.
export const inDock = (r) => !!r && (r.key === 's:mr' || r.key.startsWith('f:'));

function firstRow() {
  const rs = rows();
  const f = rs.find((r) => r.kind === 'file');
  if (!f) { const s = rs.find((r) => r.key === 's:changes'); if (s) return s.key; }
  return (f || rs[0] || {}).key || '';
}

export const current = () => rows().find((r) => r.key === G.cursor);

const selectable = (r) => r.kind !== 'note' && r.kind !== 'mrnote' && r.kind !== 'mrask';

// move moves the cursor n rows, straight from git's region into the
// merge request's and back; g and G (±Infinity) go to the top and bottom
// of the cursor's region.
function move(n) {
  let rs = rows().filter(selectable);
  if (!rs.length) return;
  if (n === -Infinity || n === Infinity) {
    const dock = inDock(rs.find((r) => r.key === G.cursor));
    rs = rs.filter((r) => inDock(r) === dock);
  }
  let i = rs.findIndex((r) => r.key === G.cursor);
  if (n === -Infinity) i = 0;
  else if (n === Infinity) i = rs.length - 1;
  else i = Math.min(Math.max((i < 0 ? 0 : i) + n, 0), rs.length - 1);
  G.cursor = rs[i].key;
}

// toggle opens or closes a section, a stash or a commit, listing a
// stash's or a commit's files the first time.
export function toggle(r, want) {
  const open = want === undefined ? !r.open : want;
  if (r.kind === 'section') { G.open[r.id] = open; keep(); if (open && r.id === 'commits' && !G.logLoaded) loadLog(false); return; }
  G.open[r.key] = open;
  if (!open) return;
  if (r.kind === 'commit' && !G.commitFiles[r.sha]) {
    get('/api/scm/commit?sha=' + r.sha).then((v) => { G.commitFiles[r.sha] = v; redraw(); }, (e) => say(e.message));
  }
  if (r.kind === 'stash' && !G.stashFiles[r.index]) {
    get('/api/scm/stash?i=' + r.index).then((v) => { G.stashFiles[r.index] = v.files; redraw(); }, (e) => say(e.message));
  }
}

// ---- the two regions ----

// was is the row each region had when tab left it.
const was = { git: '', dock: '' };

// switchRegion is tab (and shift+tab): to the other region, at the row it
// had when tab left it; the dock's header bar the first time, git's first
// change.
function switchRegion() {
  if (!fg.on()) return false;
  const rs = rows().filter(selectable);
  const has = (k) => k && rs.some((r) => r.key === k);
  if (inDock(rs.find((r) => r.key === G.cursor))) {
    was.dock = G.cursor;
    G.cursor = has(was.git) ? was.git : firstRow();
  } else {
    was.git = G.cursor;
    G.cursor = has(was.dock) ? was.dock : 's:mr';
  }
  return true;
}

// ---- showing ----

export function focusSCM() {
  restore();
  if (!G.loaded) refresh();
  if (!G.cursor) G.cursor = firstRow();
  S.zoomed = false;
  setFocus('tree');
}

// ---- git commands ----

// act runs a git command: the answer is flashed, a failure shown red in
// the header too, until the next command. opts.busy says what runs (as
// the server's busy does) until the answer comes.
export async function act(url, body, opts = {}) {
  if (G.busy) { say('git is busy ' + G.busy + ': wait for it, or stop it (✕)'); return false; }
  G.failed = '';
  if (opts.busy) { G.busy = opts.busy; say(opts.busy + '…'); }
  let r;
  try {
    r = await post(url, body || {});
  } catch (e) {
    if (opts.busy && G.busy === opts.busy) G.busy = '';
    say(e.message);
    refresh();
    return false;
  } finally {
    // The "scm" event that ends it may come after the answer.
    if (opts.busy && G.busy === opts.busy) G.busy = '';
  }
  if (!r.ok) {
    if (r.kind === 'git') G.failed = r.message;
    say(r.message);
    refresh();
    return false;
  }
  if (!opts.quiet) say(r.message);
  refresh();
  return true;
}

export function cancelGit() {
  post('/api/scm/cancel').then((r) => say(r.message), (e) => say(e.message));
}

// space: a change is staged, a staged file unstaged, a conflict marked
// resolved; on a section, all of it.
function stageToggle(r) {
  if (!r) return;
  if (r.kind === 'section') {
    if (r.id === 'changes') act('/api/scm/stage', { all: true });
    else if (r.id === 'staged') act('/api/scm/unstage', { all: true });
    else if (r.id === 'conflicts') act('/api/scm/stage', { paths: G.st.conflicts.map((f) => f.path) });
    else toggle(r);
    return;
  }
  if (r.kind === 'stash') { applyStash(r, false); return; }
  if (r.kind !== 'file') return;
  const next = nextKey(r);
  if (r.section === 'staged') act('/api/scm/unstage', { paths: [r.path] });
  else act('/api/scm/stage', { paths: [r.path] });
  if (next) G.cursor = next;
}

// nextKey is the row after r in its section, where the cursor goes once
// r leaves it.
function nextKey(r) {
  const rs = rows();
  const i = rs.findIndex((x) => x.key === r.key);
  const after = rs.slice(i + 1).find((x) => x.kind === 'file' && x.section === r.section);
  const before = rs.slice(0, i).reverse().find((x) => x.kind === 'file' && x.section === r.section);
  return (after || before || {}).key || '';
}

function stageAll() { act('/api/scm/stage', { all: true }); }

// x asks before the changes go: a file's, or the section's.
function askDiscard(r) {
  if (!r) return;
  if (r.kind === 'section' && r.id === 'changes' && G.st.changes.length) {
    G.prompt = { kind: 'discard', all: true, count: G.st.changes.length };
    return;
  }
  if (r.kind === 'file' && r.section === 'changes') {
    if (SRC.dirty && r.file && SRC.file === r.file) { say(unsavedIn(SRC.file)); return; }
    G.prompt = { kind: 'discard', paths: [r.path], name: shownPath(r), untracked: r.git === 'U' };
    return;
  }
  if (r.kind === 'file' && r.section === 'staged') { say('x discards a change that isn\'t staged: space unstages it first'); return; }
  say('x discards the change under the cursor, in Changes');
}

async function discard(p) {
  if (p.all && SRC.dirty) { say(unsavedIn(SRC.file)); return; }
  await act('/api/scm/discard', p.all ? { all: true } : { paths: p.paths });
}

// ---- committing ----

// The commit box's editor, which scm-view.js makes: its text and focus.
// typeNext asks the box, once it is in the pane, to take the focus in
// insert mode: the pane is only drawn after ctrl+g shows it.
export const box = { ed: null, typeNext: false };

export function focusBox() {
  if (!box.ed) { say('no commit box'); return; }
  box.ed.focus();
}

// commitState says which of the box's buttons have something to do:
// commit with anything staged or changed (it asks to stage the changes),
// when amending, or to finish a merge; commit all with anything staged or
// changed, or when amending. On a clean tree neither has.
export function commitState(st = G.st, amending = G.amend) {
  const any = !!st && (st.staged.length > 0 || st.changes.length > 0);
  return { commit: !!st && st.op === 'merge' || amending || any, all: amending || any };
}

export async function commit(opts = {}) {
  const ed = box.ed;
  if (G.st && !commitState()[opts.all ? 'all' : 'commit']) { say('nothing to commit, working tree clean'); return; }
  const message = ed ? ed.doc() : draft;
  const merging = G.st && G.st.op === 'merge';
  if (!message.trim() && !merging) { say('no commit message: type one in the box (c)'); focusBox(); return; }
  if (G.st && G.st.op && G.st.op !== 'merge') { say('a ' + G.st.op + ' is in progress: Continue finishes it'); return; }
  // Nothing staged but changes waiting: ask to stage them all, as commit
  // all does. The question takes the keys, so the box lets go of them.
  if (!opts.all && !G.amend && !merging && G.st && !G.st.staged.length && G.st.changes.length) {
    G.prompt = { kind: 'commitAll', count: G.st.changes.length };
    if (ed && ed.view) ed.view.contentDOM.blur();
    setFocus('tree');
    redraw();
    return;
  }
  const ok = await act('/api/scm/commit', { message, amend: G.amend, all: !!opts.all });
  if (ok) {
    G.amend = false;
    draft = '';
    keep();
    if (ed) ed.set('');
    if (G.open.commits) loadLog(false);
  }
}

// amend puts HEAD's message in the box to amend it, unless HEAD is
// pushed; again, it stops amending.
export async function amend() {
  if (G.amend) {
    G.amend = false;
    if (box.ed) box.ed.set(draft = '');
    keep();
    say('not amending');
    return;
  }
  let v;
  try { v = await get('/api/scm/amend'); } catch (e) { say(e.message); return; }
  if (v.pushed) { say('HEAD is pushed: amending it would need a forced push, which xf never does'); return; }
  G.amend = true;
  if (box.ed) box.ed.set(v.message);
  setDraft(v.message);
  say('amending HEAD: ⌘↵ commits, A again stops');
  redraw();
  focusBox();
}

// ---- remotes ----

// They say what runs at once, in the footer, the status line and the
// header, rather than when the server's "scm" event comes.
export const push = () => act('/api/scm/push', {}, { busy: 'pushing' });
export const pull = () => act('/api/scm/pull', {}, { busy: 'pulling' });
export const sync = () => act('/api/scm/sync', {}, { busy: 'syncing' });
export const fetch = () => act('/api/scm/fetch', {}, { busy: 'fetching' });

// ---- branches, stashes, operations ----

function askBranch(r) {
  const from = r && (r.kind === 'commit' || r.kind === 'cfile') ? (r.sha || '') : '';
  G.prompt = { kind: 'branch', from, short: from ? from.slice(0, 7) : '', value: '' };
}

function askStash() {
  if (!G.st || !G.st.changes.length && !G.st.staged.length) { say('no changes to stash'); return; }
  if (SRC.dirty) { say(unsavedIn(SRC.file)); return; }
  G.prompt = { kind: 'stash', value: '' };
}

function applyStash(r, pop) {
  if (SRC.dirty) { say(unsavedIn(SRC.file)); return; }
  act('/api/scm/stash/apply', { index: r.index, pop });
}

export function askAbort() {
  if (!G.st || !G.st.op) { say('nothing is in progress'); return; }
  G.prompt = { kind: 'abort', op: G.st.op };
  redraw();
}

export function continueOp() {
  if (!G.st || !G.st.op) { say('nothing is in progress'); return; }
  if (G.st.op === 'merge') { commit(); return; }
  act('/api/scm/continue', { message: '' });
}

export async function submitPrompt() {
  const p = G.prompt;
  if (!p || p.busy) return;
  if (p.kind === 'branch') {
    const name = (p.value || '').trim();
    if (!name) { G.prompt = null; redraw(); return; }
    G.prompt = null;
    await act('/api/scm/branch', { name, from: p.from });
    if (G.open.commits) loadLog(false);
    return;
  }
  if (p.kind === 'stash') {
    G.prompt = null;
    await act('/api/scm/stash/push', { message: p.value || '' });
    return;
  }
}

export function cancelPrompt() { G.prompt = null; redraw(); }

// confirmKey is a key while a y/n question shows.
function confirmKey(k) {
  const p = G.prompt;
  if (k !== 'y' && k !== 'enter') {
    G.prompt = null;
    if (p.kind === 'commitAll') { say('nothing committed: space stages a file'); redraw(); return true; }
    say('kept');
    return true;
  }
  G.prompt = null;
  if (p.kind === 'commitAll') { commit({ all: true }); return true; }
  if (p.kind === 'discard') discard(p);
  else if (p.kind === 'drop') act('/api/scm/stash/drop', { index: p.index });
  else if (p.kind === 'abort') act('/api/scm/abort');
  return true;
}

// ---- the git output ----

export async function openOutput() {
  try {
    const v = await get('/api/scm/output');
    G.output = v.entries;
    S.overlay = 'gitOutput';
  } catch (e) { say(e.message); }
  redraw();
}

export function closeOutput() { G.output = null; if (S.overlay === 'gitOutput') S.overlay = null; redraw(); }

// ---- the workspace (workspace.js) ----

// Source Control's open sections, filter and row go into the tab's
// workspace, and the difference the source pane shows, which opens again
// once the source pane has its file.
part('scm', () => ({
  open: G.open, filter: G.filter, cursor: G.cursor,
  diff: G.diff && { side: G.diff.side, path: G.diff.path, rev: G.diff.rev, base: G.diff.base },
}), (v) => {
  if (v.open) G.open = { ...G.open, ...v.open, changes: true };
  G.filter = v.filter || '';
  G.cursor = v.cursor || '';
  const d = v.diff;
  if (!d || !d.path) return;
  const r = d.side === 'commit' ? { kind: 'cfile', path: d.path, sha: d.rev, parent: d.base }
    : d.side === 'stash' ? { kind: 'sfile', path: d.path, stash: Number(d.rev) }
    : { kind: 'file', path: d.path, section: d.side === 'staged' ? 'staged' : 'changes' };
  const started = Date.now();
  const when = () => {
    if (SRC.view && !SRC.loading) openDiff(r);
    else if (Date.now() - started < 8000) setTimeout(when, 100);
  };
  setTimeout(when, 100);
});

// ---- the difference ----

const diffSide = { changes: 'changes', staged: 'staged', conflicts: 'changes' };

// openDiff shows r's difference in the source pane. A change to a file of
// the project is shown beside the source pane's editor, which edits it.
export async function openDiff(r) {
  if (!r) return;
  if (r.kind === 'file' && r.section === 'conflicts') {
    if (!r.file) { say(r.path + ' is outside the project: resolve it in a shell'); return; }
    G.diff = null;
    if (hooks.openFile) hooks.openFile(r.file);
    return;
  }
  const q = new URLSearchParams({ path: r.path });
  if (r.kind === 'file') q.set('side', diffSide[r.section]);
  else if (r.kind === 'cfile') { q.set('side', 'commit'); q.set('rev', r.sha); q.set('base', r.parent || ''); }
  else if (r.kind === 'sfile') { q.set('side', 'stash'); q.set('rev', String(r.stash)); }
  else return;
  let v;
  try { v = await get('/api/scm/diff?' + q); } catch (e) { say(e.message); return; }
  if (v.old.binary || v.new.binary) { say(shownPath(r) + ' isn\'t text: no difference to show'); return; }
  if (v.old.tooBig || v.new.tooBig) { say(shownPath(r) + ' is too big to show here'); return; }
  const d = { side: q.get('side'), path: r.path, file: v.file || '', rev: q.get('rev') || '', base: q.get('base') || '', v, live: !!v.live, armed: false, under: '', at: Date.now() };
  if (d.live) {
    if (SRC.dirty && SRC.file !== d.file && !(hooks.sourceFlush && await hooks.sourceFlush())) {
      say(unsavedIn(SRC.file));
      return;
    }
    G.diff = d;
    if (hooks.openFile) await hooks.openFile(d.file, { edit: false });
  } else {
    if (!SRC.shown && hooks.toggleSource) hooks.toggleSource();
    d.under = SRC.file;
    G.diff = d;
  }
  // On a phone the column covers the page: it goes, as the tree's does
  // once a file is picked.
  if (window.matchMedia && window.matchMedia('(max-width: 760px)').matches) { T.shown = false; setFocus('source'); } else setFocus('tree');
  redraw();
}

// reloadDiff reads a read-only difference again: after gsh changed the
// index it shows.
async function reloadDiff() {
  const d = G.diff;
  if (!d || d.live) return;
  const q = new URLSearchParams({ side: d.side, path: d.path, rev: d.rev, base: d.base });
  try { d.v = await get('/api/scm/diff?' + q); d.key = (d.key || 0) + 1; } catch (e) { say(e.message); }
  redraw();
}

// diffNow is the difference to show, or null: a live one waits for its
// file to show, and ends once another file shows, as a read-only one does
// once the file shown changes.
export function diffNow() {
  const d = G.diff;
  if (!d) return null;
  if (d.live) {
    if (SRC.file === d.file) {
      // Shown once the file has loaded, not only been named: while it
      // loads the editor still has the file before, whose first change
      // the difference would open on, and the load would then put the
      // cursor at the top. Shown, a reload (:e!) keeps it.
      if (d.armed || (SRC.view && SRC.view.file === d.file && !SRC.loading)) { d.armed = true; return d; }
      return null;
    }
    // Shown, then another file: it ends. Never shown (the open was
    // refused, or is slow), it gives up after a while.
    if (d.armed || Date.now() - d.at > 5000) G.diff = null;
    return null;
  }
  if (!SRC.shown || SRC.file !== d.under) { G.diff = null; return null; }
  return d;
}

export function endDiff() { G.diff = null; redraw(); }

// diffStyle is the diffStyle setting: how a difference shows to start
// with, inline unless it says side-by-side.
const diffStyle = () => (S.boot && S.boot.diffStyle === 'side-by-side' ? 'side-by-side' : 'inline');

// diffMode is how a difference shows: 'inline' or 'side', as diffStyle
// says until t flips it, and again once diffStyle changes.
export function diffMode() {
  const f = G.diffFlip;
  if (f && f.style === diffStyle()) return f.mode;
  G.diffFlip = null; // the setting changed: t's view is gone
  return diffStyle() === 'side-by-side' ? 'side' : 'inline';
}

export function toggleDiffMode() {
  G.diffFlip = { style: diffStyle(), mode: diffMode() === 'side' ? 'inline' : 'side' };
  redraw();
}

// stageText is gsh in the source pane: the index's version of the file
// shown becomes text, which the editor then compares with.
async function stageText(view, text, was) {
  if (!SRC.file) return;
  if (!G.st) await load();
  if (!G.st || G.st.notGit) { say('not in a git repository'); return; }
  const ok = await act('/api/scm/index', { path: G.st.prefix + SRC.file, text, was }, { quiet: true });
  if (!ok) return;
  setBase(view, text);
  if (SRC.view && SRC.view.file === SRC.file) SRC.view.base = text;
  say('staged the change');
}

// diffStager is gsh in a read-only difference: in a staged file's, the
// change is unstaged; in a change's (a file outside the project), staged.
export function diffStager(d) {
  return async (view, text, base, h) => {
    if (d.side === 'staged') {
      const index = view.state.doc.toString();
      const ok = await act('/api/scm/index', { path: d.path, text: unstagedText(base, view.state.doc, h), was: index, unstage: true }, { quiet: true });
      if (ok) { say('unstaged the change'); reloadDiff(); }
      return;
    }
    if (d.side === 'changes') {
      const ok = await act('/api/scm/index', { path: d.path, text, was: base }, { quiet: true });
      if (ok) { say('staged the change'); reloadDiff(); }
      return;
    }
    say('a commit\'s or a stash\'s change can\'t be staged');
  };
}

// ---- keys ----

export function key(k) {
  restore();
  const p = G.prompt;
  if (p && (p.kind === 'discard' || p.kind === 'drop' || p.kind === 'abort' || p.kind === 'commitAll')) return confirmKey(k);
  if (p || G.filtering) return false;
  const r = current();
  if (fg.key(k, r)) return true;
  switch (k) {
    case 'j': case 'down': move(1); return true;
    case 'k': case 'up': move(-1); return true;
    case 'g': case 'home': move(-Infinity); return true;
    case 'G': case 'end': move(Infinity); return true;
    case 'ctrl+d': case 'pgdown': move(15); return true;
    case 'ctrl+u': case 'pgup': move(-15); return true;
    case 'enter':
      if (!r) return true;
      if (r.kind === 'section' || r.kind === 'stash' || r.kind === 'commit') toggle(r);
      else if (r.kind === 'more') loadLog(true);
      else openDiff(r);
      return true;
    case 'l': case 'right':
      if (r && (r.kind === 'section' || r.kind === 'stash' || r.kind === 'commit')) { if (r.open) move(1); else toggle(r, true); } else if (r) openDiff(r);
      return true;
    case 'h': case 'left': {
      if (!r) return true;
      if ((r.kind === 'section' || r.kind === 'stash' || r.kind === 'commit') && r.open) { toggle(r, false); return true; }
      const rs = rows();
      const i = rs.findIndex((x) => x.key === r.key);
      const parent = rs.slice(0, i).reverse().find((x) => (r.kind === 'sfile' ? x.kind === 'stash' : r.kind === 'cfile' ? x.kind === 'commit' : x.kind === 'section'));
      if (parent) G.cursor = parent.key;
      return true;
    }
    case 'o':
      if (r && r.file && (r.kind === 'file')) { G.diff = null; if (hooks.openFile) hooks.openFile(r.file); } else say('o opens a changed file of the project');
      return true;
    case ' ': case 'space': stageToggle(r); return true;
    case 'a': stageAll(); return true;
    case 'x': askDiscard(r); return true;
    case 'c': focusBox(); return true;
    case 'A': amend(); return true;
    case 'P': push(); return true;
    case 'p':
      if (r && r.kind === 'stash') applyStash(r, true);
      else pull();
      return true;
    case 'f': fetch(); return true;
    case 's': askStash(); return true;
    case 'n': askBranch(r); return true;
    case 'd':
      if (r && r.kind === 'stash') G.prompt = { kind: 'drop', index: r.index, subject: r.subject };
      else say('d drops the stash under the cursor');
      return true;
    case 'y':
      if (r && (r.kind === 'commit' || r.kind === 'cfile')) copy(r.sha, 'copied ' + r.sha.slice(0, 7));
      else say('y copies the SHA of the commit under the cursor');
      return true;
    case 'R': refresh(); say('reading git\'s status again'); return true;
    case 'tab': case 'shift+tab': return switchRegion();
    case '/': G.filtering = true; return true;
    case 'esc':
      if (G.filter) { G.filter = ''; return true; }
      if (G.diff) { endDiff(); return true; }
      return false;
  }
  return false;
}

export function setFilter(q) {
  G.filter = q;
  const first = rows().find((r) => r.kind === 'file' || r.kind === 'sfile' || r.kind === 'cfile');
  if (first) G.cursor = first.key;
  redraw();
}

export function endFilter(accept) {
  G.filtering = false;
  if (!accept) G.filter = '';
  redraw();
}

export function clickRow(r) {
  G.cursor = r.key;
  setFocus('tree');
  if (fg.isRow(r)) { fg.act(r); redraw(); return; }
  if (r.kind === 'section' || r.kind === 'stash' || r.kind === 'commit') toggle(r);
  else if (r.kind === 'more') loadLog(true);
  else if (r.kind !== 'note') openDiff(r);
  redraw();
}

// ---- wiring ----

// The source pane's keys outside the editor: esc and q end the
// difference, t flips it between side by side and inline.
const sourceKey = hooks.sourceKey;
hooks.sourceKey = (k) => {
  if (diffNow()) {
    if (k === 'esc' || k === 'q') { endDiff(); return true; }
    if (k === 't') { toggleDiffMode(); return true; }
  }
  return sourceKey ? sourceKey(k) : false;
};

// scmCommands are the palette's Git commands: [label, key, run].
function commands() {
  const st = G.st;
  if (!st || st.notGit) return [];
  const show = (f) => () => { if (!shown() && hooks.toggleSCM) hooks.toggleSCM(); f(); };
  const out = [
    ['Git: commit', 'c', show(() => commit())],
    ['Git: commit all (stage every change first)', '', show(() => commit({ all: true }))],
    ['Git: amend the last commit', 'A', show(amend)],
    ['Git: stage all', 'a', () => act('/api/scm/stage', { all: true })],
    ['Git: unstage all', '', () => act('/api/scm/unstage', { all: true })],
    ['Git: push', 'P', push],
    ['Git: pull (fast-forward only)', 'p', pull],
    ['Git: sync (pull, then push)', '', sync],
    ['Git: fetch origin', 'f', fetch],
    ['Git: stash every change', 's', show(askStash)],
    ['Git: new branch', 'n', show(() => askBranch(null))],
    ['Git: show the git output', '', openOutput],
  ];
  if (st.op) out.unshift(['Git: ' + (st.op === 'merge' ? 'commit the merge' : 'continue the ' + st.op), '', show(continueOp)], ['Git: abort the ' + st.op, '', show(askAbort)]);
  return out;
}

hooks.scmCommands = commands;
// scmShowMR shows the view at the merge request's section (forge.js).
hooks.scmShowMR = () => {
  restore();
  // Not into the commit box, as ctrl+g goes: the keys are the list's.
  if (!shown() && hooks.toggleSCM) { hooks.toggleSCM(); box.typeNext = false; }
  G.open.mr = true;
  keep();
  G.cursor = 's:mr';
  focusSCM();
};
hooks.scmPush = push;
hooks.scmFocus = focusSCM;
hooks.scmTypeOnShow = () => { box.typeNext = true; };
hooks.scmKey = key;
hooks.scmOpenChanges = openChanges;
// A git command, here or in a terminal (a pull, a commit, a checkout),
// can change the source pane's file or its base without a compile, so
// the pane looks at the disk again.
let diskTimer = 0;
hooks.scmEvent = (ev) => {
  G.busy = ev.busy || '';
  refresh();
  clearTimeout(diskTimer);
  diskTimer = setTimeout(checkDisk, 120);
};
hooks.stageText = stageText;
hooks.scmCount = count;
const filesChanged = hooks.filesChanged;
hooks.filesChanged = () => { if (filesChanged) filesChanged(); refresh(); };

window.addEventListener('focus', () => refresh());
refresh();

export const commitKey = onMac ? '⌘↵' : 'ctrl+↵';
