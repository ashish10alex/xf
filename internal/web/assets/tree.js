// The file tree (ADR 0006): the project's files in a column on the far
// left, as VS Code's explorer is. cmd+b (ctrl+b off a Mac) shows and hides
// it, ctrl+0 gives it the keys, and its keys are nvim-tree's. Opening a
// file shows it in the source pane: an action's file selects its action,
// any other file is shown with nothing selected (see showPlainFile).
//
// The server lists the files (GET /api/files) and says when they change
// (the "files" event); the page lists them again then, after a compile
// and on focus, while the tree shows.
//
// The column shows the tree, Source Control (ADR 0007, scm.js) or Search
// (ADR 0016, search.js), one at a time: ctrl+g shows Source Control, cmd+b
// the tree, each hiding the column when its view already shows, and cmd+
// shift+f Search, which cmd+b hides. Its fourth tab lists the projects
// open in xf web (ADR 0008), to switch to another.

import { S, get, post, redraw, hooks, setFocus, selectAction, showPlainFile, onMac, copy, absPath, say, openNewWorktree } from './state.js';
import { SRC } from './source.js';
import { part } from './workspace.js';
import { askRemoveWorktreeHere, openFoundRow, projectTabRows, switchProject } from './picker.js';
import { showMenu } from './ctx-menu.js';

export const T = {
  shown: false,
  view: 'files',   // what the column shows: 'files', 'scm', 'search' or 'projects'
  width: 0,        // px; 0 is the default
  open: {},        // the open folders, by path
  files: [],       // fileRow from the server: {path, git, action, actions, errors}
  dirs: [],        // the folders without files
  git: false,
  truncated: false,
  loaded: false,
  error: '',
  cursor: '',      // the row the keys act on, by path
  // centre is a reveal the list hasn't scrolled to yet: {path, always};
  // always centres the row, else only a row out of view is centred.
  centre: null,
  projCursor: '',  // the projects tab's row the keys act on, by key
  filter: '',      // what / narrowed the tree to
  filtering: false, // typing the filter
  // prompt is what a/r/d asks: {kind: 'new' | 'rename' | 'delete',
  // path, value, check, busy}; check is the rename's renameCheck once
  // the server says the rename renames actions.
  prompt: null,
};

export const treeKey = onMac ? '⌘b' : 'ctrl+b';

// ---- what the browser keeps, per project ----

const root = () => (S.boot && S.boot.root) || '';
const storeKey = () => 'xf-web:tree:' + root();
let loadedFor = null;

// restore reads the project's tree settings, once per project.
function restore() {
  if (loadedFor === root()) return;
  loadedFor = root();
  Object.assign(T, { projCursor: '', shown: false, view: 'files', width: 0, open: {}, files: [], dirs: [], loaded: false, error: '', cursor: '', centre: null, filter: '', filtering: false, prompt: null });
  try {
    const v = JSON.parse(localStorage.getItem(storeKey()) || '{}');
    T.shown = !!v.shown && !phone(); // on a phone it would cover the page
    T.width = v.width || 0;
    if (v.view === 'scm' || v.view === 'search' || v.view === 'projects') T.view = v.view;
    for (const d of v.open || []) T.open[d] = true;
  } catch { /* no storage */ }
  if (T.shown) refresh();
}

function keep() {
  try {
    localStorage.setItem(storeKey(), JSON.stringify({ shown: T.shown, view: T.view, width: T.width, open: Object.keys(T.open) }));
  } catch { /* no storage */ }
}

export function setTreeWidth(px) { T.width = px; keep(); }

// showColumnFor makes the column show its projects tab when the project
// at r loads next, as a switch from that tab wants: the list is there to
// switch back by.
export function showColumnFor(r) {
  try {
    const k = 'xf-web:tree:' + r;
    const v = JSON.parse(localStorage.getItem(k) || '{}');
    v.shown = true;
    v.view = 'projects';
    localStorage.setItem(k, JSON.stringify(v));
  } catch { /* no storage */ }
}

// ---- the projects tab ----

// projectRows are the projects open in xf web (ADR 0008) by name, so a
// row stays where it was after a switch, each with its worktrees that
// aren't open under it (ADR 0032).
export const projectRows = () => projectTabRows();

// openProjectRow opens the project of a row: an open one as the switcher
// does, a worktree not open yet by its folder.
export function openProjectRow(o, flip) {
  if (o.found) openFoundRow(o, flip);
  else switchProject(o.key, flip);
}

// openProjectMenu is a row's right-click menu (ADR 0032): open it here or
// in a new tab; New Worktree… lists its repository's branches to open in
// a worktree, or a new branch to make in one; Remove Worktree asks before
// removing a worktree, xf's or another's; and its path.
export function openProjectMenu(e, o) {
  e.preventDefault();
  e.stopPropagation();
  T.projCursor = o.key;
  const here = o.key === S.key;
  const newTab = S.openIn === 'new-tab';
  // A worktree not open yet asks through its main checkout's project.
  const via = o.found ? S.open.find((x) => x.root === o.main) : o;
  const repo = o.child && o.main ? (S.open.find((x) => x.root === o.main) || o) : o;
  const items = [
    { label: 'Open', key: newTab ? 'shift+click' : 'click', off: here ? 'shown in this tab' : '', run: () => openProjectRow(o, newTab) },
    { label: 'Open in a New Tab', key: newTab ? 'click' : 'shift+click', off: here ? 'shown in this tab' : '', run: () => openProjectRow(o, !newTab) },
    '-',
    { label: 'New Worktree…', key: 'shift+enter in B', off: !S.caps.branches ? 'not available' : !via ? 'open its project first' : '',
      run: () => openNewWorktree(via.key, repo.name) },
  ];
  if (o.worktree) {
    items.push({ label: 'Remove Worktree',
      run: () => askRemoveWorktreeHere({ ...o, key: o.found ? '' : o.key, path: o.path || o.root }) });
  }
  items.push('-', { label: 'Copy Path', run: () => copy(o.root, 'copied ' + o.root) });
  showMenu(items, { x: e.clientX, y: e.clientY }, { scrollEl: e.currentTarget.closest('.pane-body') });
  redraw();
}

// projectsKey is the projects tab's keys: move, and enter opens the
// project under the cursor as the switcher does.
function projectsKey(k) {
  const rs = projectRows();
  let i = rs.findIndex((o) => o.key === T.projCursor);
  if (i < 0) i = Math.max(0, rs.findIndex((o) => o.key === S.key));
  const go = (n) => { if (rs.length) T.projCursor = rs[Math.min(Math.max(n, 0), rs.length - 1)].key; };
  switch (k) {
    case 'j': case 'down': go(i + 1); return true;
    case 'k': case 'up': go(i - 1); return true;
    case 'g': case 'home': go(0); return true;
    case 'G': case 'end': go(rs.length - 1); return true;
    case 'enter': case 'o': case 'l': if (rs[i]) openProjectRow(rs[i], false); return true;
  }
  return false;
}

// ---- listing ----

let seq = 0, timer = 0;

// refresh lists the files again, soon: events come in bursts.
export function refresh() {
  if (!T.shown || S.mode !== 'project') return;
  clearTimeout(timer);
  timer = setTimeout(load, 80);
}

async function load() {
  const n = ++seq;
  try {
    const v = await get('/api/files');
    if (n !== seq) return;
    T.files = v.files;
    T.dirs = v.dirs;
    T.git = v.git;
    T.truncated = !!v.truncated;
    T.error = '';
    T.loaded = true;
    if (!T.cursor || !rowPaths().has(T.cursor)) T.cursor = SRC.file || '';
  } catch (e) {
    if (n !== seq) return;
    T.error = e.message;
  }
  redraw();
}

// ---- the rows ----

// node is a folder of the tree: its folders and files by name.
function tree() {
  const top = { dirs: new Map(), files: [] };
  const dirOf = (path) => {
    let n = top;
    if (!path) return n;
    let at = '';
    for (const part of path.split('/')) {
      at = at ? at + '/' + part : part;
      if (!n.dirs.has(part)) n.dirs.set(part, { path: at, dirs: new Map(), files: [] });
      n = n.dirs.get(part);
    }
    return n;
  };
  for (const d of T.dirs) dirOf(d);
  for (const f of T.files) {
    const i = f.path.lastIndexOf('/');
    dirOf(i < 0 ? '' : f.path.slice(0, i)).files.push(f);
  }
  return top;
}

const byName = (a, b) => a.localeCompare(b, undefined, { sensitivity: 'base', numeric: true });
const gitRank = { C: 4, M: 3, A: 2, U: 1 };

// marks are a folder's git status and errors, from its files'.
function marks(n) {
  let git = '', errors = 0, dirty = false;
  const take = (g) => { if ((gitRank[g] || 0) > (gitRank[git] || 0)) git = g; };
  for (const f of n.files) {
    take(f.git);
    errors += f.errors || 0;
    if (isDirty(f.path)) dirty = true;
  }
  for (const d of n.dirs.values()) {
    const m = marks(d);
    take(m.git);
    errors += m.errors;
    dirty = dirty || m.dirty;
  }
  return { git, errors, dirty };
}

const isDirty = (path) => SRC.dirty && SRC.file === path;

// filterWords are the filter's words, lowercased.
const filterWords = () => T.filter.toLowerCase().split(/\s+/).filter(Boolean);

// rows are the tree's rows as drawn: {path, name, dir, depth, open, git,
// errors, dirty, action}. A filter shows the files whose path has each of
// its words, in their folders, open.
export function rows() {
  const out = [];
  const words = filterWords();
  const matches = (p) => { const l = p.toLowerCase(); return words.every((w) => l.includes(w)); };
  const walk = (n, depth) => {
    const dirs = [...n.dirs.values()].sort((a, b) => byName(a.path, b.path));
    for (const d of dirs) {
      if (words.length && !hasMatch(d, matches)) continue;
      const open = words.length ? true : !!T.open[d.path];
      out.push({ path: d.path, name: d.path.slice(d.path.lastIndexOf('/') + 1), dir: true, depth, open, ...marks(d) });
      if (open) walk(d, depth + 1);
    }
    const files = n.files.filter((f) => !words.length || matches(f.path)).sort((a, b) => byName(a.path, b.path));
    for (const f of files) {
      out.push({ path: f.path, name: f.path.slice(f.path.lastIndexOf('/') + 1), dir: false, depth,
        git: f.git || '', errors: f.errors || 0, dirty: isDirty(f.path), action: f.action || '', actions: f.actions || 0 });
    }
  };
  walk(tree(), 0);
  return out;
}

function hasMatch(n, matches) {
  for (const f of n.files) if (matches(f.path)) return true;
  for (const d of n.dirs.values()) if (hasMatch(d, matches)) return true;
  return false;
}

function rowPaths() { return new Set(rows().map((r) => r.path)); }

// filesIn counts the files under folder path.
function filesIn(path) { return T.files.filter((f) => f.path.startsWith(path + '/')).length; }
const isFolder = (path) => T.dirs.includes(path) || T.files.some((f) => f.path.startsWith(path + '/'));

// ---- showing ----

// toSource gives the source pane's editor the keys in vim's normal mode, as
// every way out of the column into pane 1 does.
function toSource() {
  setFocus('source');
  if (hooks.sourceReturn) hooks.sourceReturn();
}

// toggleTree is cmd+b: it shows the column on the tree, or hides it when
// the tree shows.
export function toggleTree() { toggleView('files'); }

// toggleSCM is ctrl+g: the same for Source Control.
export function toggleSCM() { toggleView('scm'); }

// toggleColumn is the cmd+b key: it hides the column whichever view it
// shows, Source Control too, and shows it on the tree when hidden.
export function toggleColumn() { toggleView(T.shown ? T.view : 'files'); }

function toggleView(view) {
  restore();
  if (T.shown && T.view === view) {
    T.shown = false;
    keep();
    if (S.focus === 'tree') {
      T.prompt = null;
      T.filtering = false;
      if (hooks.sourceShownNow && hooks.sourceShownNow()) toSource();
      else setFocus('sql');
    }
    return;
  }
  T.shown = true;
  T.view = view;
  keep();
  refresh();
  // The open file's row shows in the middle of the list, as VS Code's
  // explorer reveals it.
  if (view === 'files') reveal(SRC.file, true);
  focusTree();
  // ctrl+g goes on into the commit box, ready to type.
  if (view === 'scm' && hooks.scmTypeOnShow) hooks.scmTypeOnShow();
}

// showView shows the column on view, as a click on its tab does.
export function showView(view) {
  restore();
  if (T.view === view && T.shown) return;
  T.shown = true;
  T.view = view;
  keep();
  refresh();
  focusTree();
}

// focusTree gives the column the keys, showing it if hidden.
export function focusTree() {
  restore();
  if (!T.shown) { toggleView(T.view); return; }
  S.zoomed = false;
  if (T.view === 'scm' && hooks.scmFocus) { hooks.scmFocus(); return; }
  if (T.view === 'search' && hooks.searchFocus) { hooks.searchFocus(); return; }
  if (!T.cursor) T.cursor = SRC.file || (rows()[0] || {}).path || '';
  setFocus('tree');
}

// closeOnPhone hides the tree that covers the page on a phone, once a file
// is picked.
function phone() { return !!(window.matchMedia && window.matchMedia('(max-width: 760px)').matches); }

// reveal opens the folders file is in and puts the cursor on it, which
// the list scrolls to the middle when out of view, or always when centre
// (cmd+b showing the tree).
export function reveal(file, centre) {
  restore();
  if (!file) return;
  T.cursor = file;
  if (!T.shown) return;
  T.centre = { path: file, always: !!centre };
  const parts = file.split('/');
  let changed = false;
  for (let i = 1; i < parts.length; i++) {
    const d = parts.slice(0, i).join('/');
    if (!T.open[d]) { T.open[d] = true; changed = true; }
  }
  if (changed) keep();
}

// ---- opening ----

// openFile shows file in the source pane: its first action selected, or
// with none, the file alone. opts.action names that action when the tree
// hasn't listed the file (the palette's pick). Unsaved changes in another
// file refuse it, unless autosave saves them first.
export async function openFile(file, opts = {}) {
  if (!file) return;
  if (SRC.dirty && SRC.file !== file) {
    const saved = hooks.sourceFlush ? await hooks.sourceFlush() : false;
    if (!saved) {
      say(SRC.stale ? SRC.file + ' changed on disk: :w to choose what to keep, :e! discards' : 'unsaved changes in ' + SRC.file + ': :w saves, :e! discards');
      return;
    }
  }
  if (!SRC.shown && hooks.toggleSource) hooks.toggleSource();
  const f = T.files.find((x) => x.path === file);
  const action = (f && f.action) || opts.action || (S.view && S.view.file === file && S.sel);
  // The file goes with the action, which a compile since the files were
  // listed may have renamed (the tree re-lists them only while it shows).
  if (S.view && S.view.file === file) { /* already shown, its action kept */ } else if (action) selectAction(action, { file });
  else showPlainFile(file);
  reveal(file);
  if (phone()) { T.shown = false; keep(); }
  if (opts.edit !== false) toSource();
  redraw();
}

// ---- keys ----

function move(n) {
  const rs = rows();
  if (!rs.length) return;
  let i = rs.findIndex((r) => r.path === T.cursor);
  if (n === -Infinity) i = 0;
  else if (n === Infinity) i = rs.length - 1;
  else i = Math.min(Math.max((i < 0 ? 0 : i) + n, 0), rs.length - 1);
  T.cursor = rs[i].path;
}

const current = () => rows().find((r) => r.path === T.cursor);

function setOpen(path, open) {
  if (open) T.open[path] = true;
  else delete T.open[path];
  keep();
}

// enter is l or enter on a row: a folder opens (or, open, its first
// row is next), a file opens in the source pane.
function enter(r, l) {
  if (!r) return;
  if (r.dir) {
    if (r.open && l) move(1);
    else setOpen(r.path, !r.open || l);
    return;
  }
  openFile(r.path);
}

// up is h: an open folder closes, else the cursor goes to the folder the
// row is in.
function up(r) {
  if (!r) return;
  if (r.dir && r.open && !T.filter) { setOpen(r.path, false); return; }
  const i = r.path.lastIndexOf('/');
  if (i > 0) T.cursor = r.path.slice(0, i);
}

// folderAt is the folder a new file goes in: the row's, or the folder
// the row is in.
function folderAt(r) {
  if (!r) return '';
  if (r.dir) return r.path;
  const i = r.path.lastIndexOf('/');
  return i < 0 ? '' : r.path.slice(0, i);
}

// holdsDirty says whether path, or a file under it, has unsaved changes.
const holdsDirty = (path) => SRC.dirty && (SRC.file === path || SRC.file.startsWith(path + '/'));

// askNew is a: a file or folder in the row's folder, or in dir when given
// (the menu's New File… on the list's empty space gives the root, '').
export function askNew(dir) {
  const d = dir == null ? folderAt(current()) : dir;
  T.prompt = { kind: 'new', path: d, value: d ? d + '/' : '' };
}

// askRename is r, and with move m: the same box with the row's path,
// r's with the name selected, m's with the folder, to type another.
export function askRename(move) {
  const r = current();
  if (!r) return;
  if (holdsDirty(r.path)) { say('unsaved changes in ' + SRC.file + ': :w saves, :e! discards'); return; }
  T.prompt = { kind: 'rename', path: r.path, value: r.path, dir: r.dir, move: !!move };
}

export function askDelete() {
  const r = current();
  if (!r) return;
  if (holdsDirty(r.path)) { say('unsaved changes in ' + SRC.file + ': :w saves, :e! discards'); return; }
  T.prompt = { kind: 'delete', path: r.path, dir: r.dir, count: r.dir ? filesIn(r.path) : 0 };
}

// copyPath is y (abs) and Y: the row's path as the system writes it, or
// as the project does.
export function copyPath(r, abs) {
  if (!r) return;
  const p = abs ? absPath(r.path) : r.path;
  copy(p, 'copied ' + p);
}

export function cancelPrompt() { T.prompt = null; redraw(); }

// submitPrompt does what the prompt asked, with the name typed.
export async function submitPrompt() {
  const p = T.prompt;
  if (!p || p.busy) return;
  const value = (p.value || '').trim().replace(/^\/+/, '');
  try {
    if (p.kind === 'new') {
      if (!value || value.endsWith('/') && value.replace(/\/+$/, '') === p.path) { T.prompt = null; redraw(); return; }
      const dir = value.endsWith('/');
      const path = value.replace(/\/+$/, '');
      p.busy = true;
      redraw();
      await post('/api/files/new', { path, dir });
      T.prompt = null;
      reveal(path);
      if (dir) setOpen(path, true);
      await load();
      if (!dir) openFile(path);
      else say('made ' + path + '/');
      return;
    }
    if (p.kind === 'rename') {
      const to = value.replace(/\/+$/, '');
      if (!to || to === p.path) { T.prompt = null; redraw(); return; }
      if (!p.check || p.check.to !== to) {
        p.busy = true;
        redraw();
        const c = await post('/api/files/rename', { from: p.path, to, check: true });
        p.busy = false;
        if (c.renames.length) { p.check = { ...c, to }; redraw(); return; } // asks y/n
      }
      await rename(p.path, to);
      return;
    }
    if (p.kind === 'delete') {
      p.busy = true;
      redraw();
      const r = await post('/api/files/delete', { path: p.path });
      T.prompt = null;
      if (SRC.file === p.path || SRC.file.startsWith(p.path + '/')) showPlainFile('');
      say(r.message);
      await load();
    }
  } catch (e) {
    if (T.prompt) T.prompt.busy = false;
    say(e.message);
  }
}

async function rename(from, to) {
  T.prompt = { ...T.prompt, busy: true };
  redraw();
  const r = await post('/api/files/rename', { from, to });
  T.prompt = null;
  const open = SRC.file;
  if (open === from || open.startsWith(from + '/')) {
    // The file shown keeps showing, by its new name; its action, renamed
    // or not, is selected again once the compile lists it.
    const now = to + open.slice(from.length);
    showPlainFile(now);
    reveal(now);
  } else reveal(to);
  if (T.open[from]) { setOpen(from, false); setOpen(to, true); }
  say(r.message);
  await load();
}

// promptKey is a key while the rename's confirmation shows.
function confirmKey(k) {
  const p = T.prompt;
  if (k === 'y' || k === 'enter') { rename(p.path, p.check.to).catch((e) => { T.prompt = null; say(e.message); }); return true; }
  if (k === 'n' || k === 'esc' || k === 'q') { T.prompt.check = null; redraw(); return true; }
  return true;
}

function deleteKey(k) {
  if (k === 'y' || k === 'enter') { submitPrompt(); return true; }
  T.prompt = null;
  say('kept');
  return true;
}

// key is the tree's keys while it has focus; the prompt's input has its
// own.
export function key(k) {
  restore();
  if (T.view === 'scm') return hooks.scmKey ? hooks.scmKey(k) : false;
  if (T.view === 'search') return hooks.searchKey ? hooks.searchKey(k) : false;
  if (T.view === 'projects') return projectsKey(k);
  const p = T.prompt;
  if (p && p.kind === 'delete') return deleteKey(k);
  if (p && p.check) return confirmKey(k);
  if (p || T.filtering) return false;
  const r = current();
  switch (k) {
    case 'j': case 'down': move(1); return true;
    case 'k': case 'up': move(-1); return true;
    case 'g': case 'home': move(-Infinity); return true;
    case 'G': case 'end': move(Infinity); return true;
    case 'ctrl+d': case 'pgdown': move(15); return true;
    case 'ctrl+u': case 'pgup': move(-15); return true;
    case 'l': case 'right': enter(r, true); return true;
    case 'enter': case 'o': enter(r, false); return true;
    case 'h': case 'left': up(r); return true;
    case 'a': askNew(); return true;
    case 'r': askRename(); return true;
    case 'm': askRename(true); return true;
    case 'd': askDelete(); return true;
    case 'y': copyPath(r, true); return true;
    case 'Y': copyPath(r, false); return true;
    case 'R': refresh(); say('listing the files again'); return true;
    case '/': T.filtering = true; return true;
    case 'esc':
      if (T.filter) { T.filter = ''; return true; }
      return false;
    case 'W': T.open = {}; keep(); return true; // collapse all, as nvim-tree's W
  }
  return false;
}

// setFilter is the filter as typed: the cursor goes to the first match.
export function setFilter(q) {
  T.filter = q;
  const first = rows().find((r) => !r.dir);
  if (first) T.cursor = first.path;
  redraw();
}

export function endFilter(accept) {
  T.filtering = false;
  if (!accept) T.filter = '';
  redraw();
}

// clickRow is a click on a row: a folder opens or closes, a file opens
// in the source pane while the tree keeps the keys, as VS Code's explorer
// does; a double-click on a file gives the editor the keys (dblClickRow).
export function clickRow(r) {
  T.cursor = r.path;
  setFocus('tree');
  if (r.dir) setOpen(r.path, !r.open);
  else openFile(r.path, { edit: false });
  redraw();
}

export function dblClickRow(r) {
  if (r.dir) return; // its first click opened it
  toSource();
  redraw();
}

// rowMenu is a right-click on a row, or with r null on the list's empty
// space (the project's root): the cursor goes there and the menu shows at
// xy. Each item does what its key does.
export function rowMenu(r, xy, scrollEl) {
  restore();
  T.prompt = null;
  T.filtering = false;
  if (r) T.cursor = r.path;
  setFocus('tree');
  redraw();
  const items = [];
  if (!r || r.dir) items.push({ label: 'New File…', key: 'a', run: () => askNew(r ? r.path : '') });
  if (r && !r.dir) items.push({ label: 'Open', key: 'enter', run: () => openFile(r.path) });
  if (r) {
    items.push('-',
      { label: 'Copy Path', key: 'y', run: () => copyPath(r, true) },
      { label: 'Copy Relative Path', key: 'Y', run: () => copyPath(r, false) },
      '-');
    const busy = holdsDirty(r.path) ? 'unsaved changes in ' + SRC.file : '';
    items.push({ label: 'Rename…', key: 'r', off: busy, run: () => askRename() });
    items.push({ label: 'Move…', key: 'm', off: busy, run: () => askRename(true) });
    items.push({ label: 'Delete', key: 'd', off: busy, run: () => askDelete() });
  } else {
    items.push({ label: 'Copy Path', run: () => copy(S.boot.root, 'copied ' + S.boot.root) });
  }
  showMenu(items, xy, { scrollEl });
}

// The row the keys act on and the filter go into the tab's workspace
// (workspace.js); whether the column shows is kept per project already.
part('tree', () => ({ cursor: T.cursor, filter: T.filter }), (v) => {
  restore();
  T.cursor = v.cursor || '';
  T.filter = v.filter || '';
});

hooks.treeKey = key;
hooks.treeFocus = focusTree;
hooks.toggleTree = toggleTree;
hooks.toggleColumn = toggleColumn;
hooks.toggleSCM = toggleSCM;
hooks.scmShown = () => { restore(); return T.shown && T.view === 'scm'; };
hooks.treeShown = () => { restore(); return T.shown; };
hooks.treeLoading = () => { restore(); return T.shown && T.view === 'files' && !T.loaded && !T.error; };
hooks.showColumnFor = showColumnFor;
hooks.projectsShown = () => { restore(); return T.shown && T.view === 'projects'; };
hooks.treeReveal = reveal;
hooks.filesChanged = refresh;
hooks.openFile = openFile;
hooks.isFolder = isFolder;

window.addEventListener('focus', () => refresh());
