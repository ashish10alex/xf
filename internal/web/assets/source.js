// The source pane: the selected action's file, beside its compiled SQL,
// edited in the shared editor and saved with :w (cmd+s, ctrl+s). The
// engine's watcher compiles a saved file, so the compiled SQL follows.
//
// A save names the hash of the text it started from, and the server
// refuses it when the file on disk has changed since: the page then offers
// to reload, overwrite or show the difference. While the pane has unsaved
// changes, following the editor waits and other files' actions can't be
// selected, so nothing switches the file away from under them. A pane
// without changes reloads when the file changes on disk.

import { EditorView, Vim, getCM, searchKeymap } from './vendor/codemirror.mjs';
import { S, get, post, redraw, hooks, setFocus, followPending, selectAction, goBack, showPlainFile, rightKey, say } from './state.js';
import { setBase, baseOf } from './git-hunks.js';
import { part } from './workspace.js';
import { noteBuffer, noteAction } from './buffers.js';
import { unsavedIn, staleIn, savesWith, discardsWith } from './vim-mode.js';

export const SRC = {
  shown: true,
  root: '',      // the project the file is of
  file: '',      // the file shown, relative to the root
  view: null,    // the file as last loaded or saved: {file, text, hash, lang, readOnly, base}
  dirty: false,  // the editor's text isn't view.text
  stale: false,  // the file changed on disk while dirty
  loading: false,
  error: '',
  editing: false, // the editor has focus
  saving: false,
  conflict: null, // a refused save: the file on disk now
  diffing: false,
  inserting: false, // the editor is in vim's insert mode
};

const shownKey = 'xf-web:source';
try { SRC.shown = localStorage.getItem(shownKey) !== '0'; } catch { /* no storage */ }

let ed = null; // the editor while the pane shows: see source-editor.js
let loadSeq = 0;
// cursorAfterLoad is the place (see the jump list) to put the cursor at
// once its file has loaded: gd or ctrl+o to another action's file.
let cursorAfterLoad = null;
// placeOnView is {id, from}: id the action picked in the palette, whose
// place in its file the cursor goes to once its view arrives, and from
// where the cursor was, for ctrl+o.
let placeOnView = null;

// setSourceEditor is called with the editor as the pane mounts it, and
// null as it goes; a mounted editor shows the file at once.
export function setSourceEditor(e) {
  ed = e;
  if (!ed) { SRC.editing = false; return; }
  if (SRC.view && SRC.view.file === SRC.file) show(SRC.view, false);
  else syncFile();
}

export const sourceEditor = () => ed;

// isLibraryFile says file is named by its path, outside the project: a
// library's file gd showed (python.js), which the tree doesn't list.
export const isLibraryFile = (file) => /^\/|^[A-Za-z]:[\\/]/.test(file || '');
// viewFile is the file to show: the selected action's, or a plain file.
const viewFile = () => (S.view && S.view.file) || S.plainFile || '';
// viewAction is the action selected, once its view (of file) is here.
const viewAction = (file) => (S.view && S.view.id === S.sel && S.view.file === file ? S.sel : '');

// show puts a loaded file in the editor.
function show(v, keepCursor) {
  if (!ed) return;
  shownAction = S.sel;
  const kept = keptFile;
  keptFile = null;
  if (kept && kept.file === v.file) putBack(v, kept);
  else ed.set(v.text, keepCursor);
  ed.setReadOnly(!!v.readOnly);
  setBase(ed.view, v.base);
  const doc = ed.view.state.doc;
  for (const e of J.list) if (e.file === v.file && e.pos == null) e.pos = posIn(doc, e);
  const at = cursorAfterLoad;
  cursorAfterLoad = null;
  if (at && at.file === v.file) {
    const pos = posIn(doc, at);
    ed.view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
  }
  noteBuffer(v.file, viewAction(v.file));
  if (hooks.sourceShown) hooks.sourceShown(v);
  if (hooks.sourceLoaded) hooks.sourceLoaded(v);
  markdownShown(v);
}

// markdown.js comes with the first Markdown file: pane 2 shows it
// rendered (ADR 0027).
let mdLoad = null;
function markdownShown(v) {
  if (v.lang === 'markdown' && !mdLoad) {
    mdLoad = import('./markdown.js').then(() => redraw());
    mdLoad.catch((e) => { mdLoad = null; say(e.message); });
  } else if (hooks.markdownShown) hooks.markdownShown(v);
}

// ---- the workspace (workspace.js) ----

// The file shown goes into the tab's workspace with its text, cursor,
// scroll and undo history, and unsaved changes with the text they started
// from, so a save still finds a file changed on disk since; the jump list
// and the alternate file go too.
part('source', (light) => {
  const out = { jumps: { list: J.list, at: J.at }, alt, lastPos: { ...lastPos } };
  if (!ed || !SRC.view || !SRC.file || SRC.view.file !== SRC.file) return out;
  Object.assign(out, { file: SRC.file, hash: SRC.view.hash, dirty: SRC.dirty, editor: ed.save(light), scroll: ed.view.scrollDOM.scrollTop });
  if (SRC.dirty) out.base = { text: SRC.view.text, hash: SRC.view.hash };
  return out;
}, (v) => {
  if (v.jumps && Array.isArray(v.jumps.list)) { J.list = v.jumps.list; J.at = Math.min(v.jumps.at ?? -1, J.list.length - 1); }
  alt = v.alt || null;
  Object.assign(lastPos, v.lastPos || {});
  if (v.file && v.editor) keptFile = v;
});

// keptFile is the workspace's file, put back once it loads.
let keptFile = null;

// putBack shows loaded file v as the workspace kept it: its unsaved text
// and undo history; its undo history when the file is as it was; else the
// file as it is now, the cursor where it was.
function putBack(v, k) {
  if (k.dirty && k.base) {
    ed.restore(k.editor);
    SRC.view = { ...v, text: k.base.text, hash: k.base.hash };
    SRC.dirty = ed.doc() !== SRC.view.text;
    SRC.stale = SRC.dirty && k.base.hash !== v.hash;
    if (SRC.dirty) setTimeout(() => armAutosave(true), 0);
  } else if (k.hash === v.hash && k.editor.doc === v.text) {
    ed.restore(k.editor);
  } else {
    ed.set(v.text);
    const sel = k.editor.selection;
    const r = sel && sel.ranges && sel.ranges[sel.main || 0];
    if (r) ed.view.dispatch({ selection: { anchor: Math.min(r.head, ed.view.state.doc.length) } });
  }
  requestAnimationFrame(() => { if (ed) ed.view.scrollDOM.scrollTop = k.scroll || 0; });
}

hooks.liveUnsaved = () => (SRC.dirty && SRC.file ? [SRC.file] : []);

// ---- the jump list ----

// The editor's jump list goes from file to file, as nvim's does with a
// language server: after gd to another action's file, ctrl+o comes back
// to where the cursor was and ctrl+i goes forward again. Vim's own jumps
// (G, gg, /, n, %), gd and the symbols list add to it. An entry is a
// place: {file, action, line, pos}, pos an offset in the file's text, or
// {file, action, line, col} (col in UTF-16 units) for one not shown yet,
// gd's target. As in vim, a line has one entry: a jump from or to it
// drops the older one. The places in the file shown move with its edits.
const jumpsMax = 100;
const J = { list: [], at: -1 };

// posIn is where place e is in doc.
function posIn(doc, e) {
  if (e.pos != null) return Math.min(e.pos, doc.length);
  const line = doc.line(Math.min(e.line + 1, doc.lines));
  return line.from + Math.min(e.col, line.length);
}

// placeAt is the place at pos in the file shown.
function placeAt(pos) {
  return { file: SRC.file, action: S.sel, pos, line: ed.view.state.doc.lineAt(pos).number - 1 };
}

// addJump records a jump between two places, dropping the entries ahead
// of the current one and older entries of their lines.
function addJump(from, to) {
  J.list = J.list.slice(0, J.at + 1);
  for (const e of [from, to]) {
    J.list = J.list.filter((o) => o.file !== e.file || o.line !== e.line);
    J.list.push(e);
  }
  if (J.list.length > jumpsMax) J.list = J.list.slice(-jumpsMax);
  J.at = J.list.length - 1;
}

// openPlace selects place e's action, whose file the pane then shows with
// the cursor there, or shows e's file alone when it has no action.
// Unsaved changes refuse it, or with autosave are saved first; it reports
// whether it is on its way.
function openPlace(e) {
  if (SRC.dirty && !canAutosave()) {
    say(SRC.stale ? staleIn(SRC.file) : unsavedIn(SRC.file));
    return false;
  }
  cursorAfterLoad = e;
  if (!e.action) {
    if (SRC.dirty) flush().then((ok) => { if (ok) showPlainFile(e.file); });
    else showPlainFile(e.file);
  } else selectAction(e.action);
  return true;
}

// ---- the alternate file ----

// ctrl+^ goes back to the file shown before, as vim's alternate file:
// where its cursor was, its action selected again.
let shownAction = null; // the action selected as the file shown loaded
let alt = null;         // {file, action, pos}
const lastPos = {};     // where the cursor was in each file left, by file

export const altFileName = () => (alt ? alt.file : '');

// fileDryRun is the failed dry runs of the file the page shows (the
// source pane's, else the selected action's), as the status line shows
// them: {count, name, label, msg}, or null when it has none.
export function fileDryRun() {
  const by = S.state && S.state.dryRunByFile;
  const file = (SRC.shown && SRC.file) || (S.view && S.view.file) || '';
  return (by && file && by[file]) || null;
}

export function altFile() {
  if (!alt) { say('no alternate file'); return; }
  const e = { file: alt.file, action: alt.action, line: 0, col: 0, pos: lastPos[alt.file] ?? null };
  if (e.pos == null) delete e.pos;
  openPlace(e);
}

// openBuffer shows file with action selected, or alone for none, the
// cursor where it was when the file was left: a buffer (buffers.js). It
// reports whether it is on its way.
export function openBuffer(file, action) {
  const e = { file, action, line: 0, col: 0 };
  if (lastPos[file] != null) e.pos = lastPos[file];
  return openPlace(e);
}

// goToPlace is gd to another action's file: backspace in the pane or
// ctrl+o in the editor comes back. With to.last the cursor goes where it
// was when that file was left, as a buffer's does (gf).
export function goToPlace(to) {
  if (!ed) return;
  if (to.last) {
    to = { ...to };
    delete to.last;
    if (lastPos[to.file] != null) to.pos = lastPos[to.file];
  }
  const from = placeAt(ed.view.state.selection.main.head);
  if (!openPlace(to)) return;
  addJump(from, to);
  if (from.action && from.action !== to.action && S.sel === to.action) S.back.push(from.action);
}

// goToLine is enter on an error in pane 2's errors tab, or a click in the
// declarations table: the cursor goes to 0-based line (and col) of file,
// which action is defined in, and the editor gets the keys. A hidden pane
// is shown; ctrl+o comes back. With opts.preview the editor doesn't get
// the keys and the jump list is left alone: a search's results show each
// match as the cursor passes it.
export function goToLine(file, action, line, col = 0, opts = {}) {
  const to = { file, action, line, col };
  const edit = !opts.preview;
  if (!SRC.shown) {
    toggleSource();
    if (viewFile() === file) cursorAfterLoad = to;
    else if (!openPlace(to)) return;
    if (edit) editSource();
    return;
  }
  if (ed && SRC.view && SRC.file === file) {
    const from = placeAt(ed.view.state.selection.main.head);
    to.pos = posIn(ed.view.state.doc, to);
    if (edit) addJump(from, to);
    ed.view.dispatch({ selection: { anchor: to.pos }, effects: EditorView.scrollIntoView(to.pos, { y: 'center' }) });
  } else if (edit) goToPlace(to);
  else openPlace(to);
  if (!edit) return;
  setFocus('source');
  editSource();
}

// stepJump is ctrl+o (n < 0) and ctrl+i (n > 0): in the file shown it
// answers the entry for vim to put the cursor at; in another, it opens
// that file and answers nothing.
function stepJump(adapter, n) {
  const e = stepEntry(n);
  return e && { find: () => adapter.posFromIndex(posIn(ed.view.state.doc, e)) };
}

// stepEntry steps the jump list n entries: in the file shown it answers
// the entry there; in another, it opens that file and answers nothing.
function stepEntry(n) {
  if (!J.list.length || !ed) return undefined;
  const t = Math.max(0, Math.min(J.list.length - 1, J.at + n));
  const e = J.list[t];
  if (e.file !== SRC.file) {
    if (openPlace(e)) J.at = t;
    return undefined;
  }
  J.at = t;
  return e;
}

// stepPlain is ctrl+o and ctrl+i without vim. As vim's do, a step back
// from the newest entry first records where the cursor is, so ctrl+i
// comes back to it.
function stepPlain(n) {
  if (!ed) return;
  if (n < 0 && J.at === J.list.length - 1) {
    const here = placeAt(ed.view.state.selection.main.head);
    const last = J.list[J.at];
    if (!last || last.file !== here.file || last.line !== here.line) {
      J.list = J.list.filter((o) => o.file !== here.file || o.line !== here.line);
      J.list.push(here);
      J.at = J.list.length - 1;
    }
  }
  const e = stepEntry(n);
  if (!e) return;
  const pos = posIn(ed.view.state.doc, e);
  ed.view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
}

// sourceJumps is the editor's jump list, as editor.js's installJumps
// calls it.
export const sourceJumps = {
  add: (adapter, from, to) => addJump(placeAt(adapter.indexFromPos(from)), placeAt(adapter.indexFromPos(to))),
  move: stepJump,
  find(adapter, n) {
    const e = J.list[Math.max(0, Math.min(J.list.length - 1, J.at + n))];
    return e && ed && e.file === SRC.file ? adapter.posFromIndex(posIn(ed.view.state.doc, e)) : undefined;
  },
  // Without vim: a jump between two offsets in the file shown, and
  // ctrl+o (n < 0) and ctrl+i (n > 0).
  addAt: (from, to) => { if (ed) addJump(placeAt(from), placeAt(to)); },
  step: stepPlain,
};

// syncFile shows the selected action's file, unless that's the one shown.
export function syncFile() {
  if (!SRC.shown) { placeOnView = null; return; }
  const root = (S.boot && S.boot.root) || '';
  if (SRC.root !== root) { // another project
    SRC.root = root;
    SRC.file = '';
    SRC.view = null;
    SRC.dirty = false;
  }
  const file = viewFile();
  const { at, from } = takePlace(file);
  if (file === SRC.file && (SRC.view || SRC.loading || SRC.error)) {
    noteAction(file, viewAction(file));
    if (at && SRC.loading) cursorAfterLoad = at;
    else if (at && ed && SRC.view) {
      at.pos = posIn(ed.view.state.doc, at);
      if (from) addJump(from, at);
      ed.view.dispatch({ selection: { anchor: at.pos }, effects: EditorView.scrollIntoView(at.pos, { y: 'center' }) });
    }
    return;
  }
  if (SRC.dirty) return; // selecting another file's action is refused while dirty
  if (at) {
    if (from) addJump(from, at);
    cursorAfterLoad = at;
  }
  load(file);
}

// placeAction puts the cursor where action id is defined, once its view
// arrives: a pick in the palette.
export function placeAction(id) {
  if (!SRC.shown) { placeOnView = null; return; }
  const from = ed && SRC.view && SRC.view.file === SRC.file ? placeAt(ed.view.state.selection.main.head) : null;
  placeOnView = { id, from };
}

// takePlace is {at, from}: the place placeAction asked for, once the view
// is its action's, and where the cursor was then.
function takePlace(file) {
  const v = S.view;
  const p = placeOnView;
  if (!p || !v || v.id !== p.id) return {};
  placeOnView = null;
  return file && v.at ? { at: { file, action: v.id, line: v.at.line, col: v.at.col }, from: p.from } : {};
}

// load reads file and shows it, its unsaved changes gone.
async function load(file, keepCursor) {
  const seq = ++loadSeq;
  if (SRC.file && file && SRC.file !== file) {
    if (ed && SRC.view && SRC.view.file === SRC.file) lastPos[SRC.file] = ed.view.state.selection.main.head;
    alt = { file: SRC.file, action: shownAction };
  }
  SRC.file = file;
  if (file && !isLibraryFile(file) && hooks.treeReveal) hooks.treeReveal(file);
  SRC.dirty = false;
  SRC.stale = false;
  SRC.error = '';
  if (!file) {
    SRC.view = null;
    if (ed) { ed.set(''); ed.setReadOnly(true); setBase(ed.view, null); }
    if (hooks.pythonShown) hooks.pythonShown(null);
    redraw();
    return;
  }
  SRC.loading = true;
  redraw();
  try {
    const v = await get('/api/source?file=' + encodeURIComponent(file));
    if (seq !== loadSeq) return;
    SRC.view = v;
    show(v, keepCursor);
  } catch (e) {
    if (seq !== loadSeq) return;
    SRC.view = null;
    SRC.error = e.message;
    if (ed) { ed.set(''); ed.setReadOnly(true); setBase(ed.view, null); }
    if (hooks.pythonShown) hooks.pythonShown(null);
  }
  SRC.loading = false;
  redraw();
}

// checkDisk reloads a pane without changes whose file changed on disk, and
// marks a pane with changes stale.
export async function checkDisk() {
  if (!SRC.shown || !SRC.view || SRC.saving || SRC.loading) return;
  const file = SRC.file;
  let v;
  try { v = await get('/api/source?file=' + encodeURIComponent(file)); } catch { return; }
  if (file !== SRC.file || !SRC.view || SRC.saving) return;
  if (v.hash === SRC.view.hash) {
    keepBase(v); // a git add or commit elsewhere
    if (SRC.stale) { SRC.stale = false; redraw(); }
    return;
  }
  if (SRC.dirty) { SRC.stale = true; redraw(); return; }
  SRC.view = v;
  show(v, true);
  say(file + ' changed on disk: reloaded');
}

// keepBase compares the text with v's base, when that is another.
function keepBase(v) {
  if (SRC.view) SRC.view.base = v.base;
  if (ed && (v.base ?? null) !== baseOf(ed.view)) setBase(ed.view, v.base);
}

// onEdit keeps dirty as the text changes, and the jump list's places in
// the file with it.
export function onEdit(u) {
  if (!ed || !SRC.view) return;
  if (u && u.changes) {
    for (const e of J.list) {
      if (e.file !== SRC.file || e.pos == null) continue;
      e.pos = u.changes.mapPos(e.pos);
      e.line = u.state.doc.lineAt(Math.min(e.pos, u.state.doc.length)).number - 1;
    }
  }
  const was = SRC.dirty;
  const doc = ed.view.state.doc;
  SRC.dirty = doc.length !== SRC.view.text.length || ed.doc() !== SRC.view.text;
  if (was !== SRC.dirty) {
    if (!SRC.dirty) followPending();
    redraw();
  }
  if (SRC.dirty && !SRC.inserting) armAutosave();
  else clearTimeout(autoTimer); // clean again, or typing in insert mode
  if (u && u.docChanged && hooks.markdownEdited) hooks.markdownEdited();
}

// save writes the editor's text, or opens the conflict when the file
// changed on disk. hash, when given, overwrites the file whose hash it is.
// An autosave (auto) says nothing when it works and opens no conflict: it
// marks the pane stale, which pauses autosave until a :w. It reports
// whether the text was saved.
export async function save(hash, auto) {
  if (!ed || !SRC.view) { if (!auto) say('no file to save'); return false; }
  if (SRC.view.readOnly) { if (!auto) say(SRC.file + " can't be saved here: " + SRC.view.readOnly); return false; }
  if (SRC.saving) return false;
  clearTimeout(autoTimer);
  const file = SRC.file;
  const text = ed.doc();
  SRC.saving = true;
  redraw();
  try {
    const v = await post('/api/source/save', { file, text, hash: hash || SRC.view.hash });
    if (file !== SRC.file) return false;
    SRC.view = v;
    keepBase(v);
    SRC.stale = false;
    SRC.dirty = ed.doc() !== v.text; // typed while saving
    if (S.overlay === 'sourceConflict') S.overlay = null;
    SRC.conflict = null;
    if (!auto) say('saved ' + file);
    if (hooks.sourceSaved) hooks.sourceSaved(v);
    if (!SRC.dirty) followPending();
    else armAutosave();
    return true;
  } catch (e) {
    if (e.status === 409 && e.data && e.data.current) {
      if (auto) {
        SRC.stale = true;
      } else {
        SRC.conflict = e.data.current;
        SRC.diffing = false;
        S.overlay = 'sourceConflict';
        if (ed) ed.blur();
      }
    } else say(e.message);
    return false;
  } finally {
    SRC.saving = false;
    redraw();
  }
}

// ---- autosave ----

// With sourceAutosaveMs set, leaving insert mode or a normal-mode change
// starts a countdown that any further change restarts and going back into
// insert mode stops; when it ends the file is saved. Half-typed text isn't
// saved while insert mode lasts. A file changed on disk pauses it (the
// pane is stale) until :w settles the conflict.
let autoTimer = 0;

export const autosaveMs = () => (S.boot && S.boot.sourceAutosaveMs) || 0;

// canAutosave says whether an autosave may write the file now.
const canAutosave = () => autosaveMs() > 0 && SRC.view && !SRC.view.readOnly && !SRC.stale && SRC.dirty;

// armAutosave starts the countdown; left, the editor counts as out of
// insert mode, which it can be left in.
function armAutosave(left) {
  clearTimeout(autoTimer);
  if (!canAutosave() || (SRC.inserting && !left)) return;
  autoTimer = setTimeout(() => { if (canAutosave() && (left || !SRC.inserting)) save(null, true); }, autosaveMs());
}

// onLeave starts the countdown as the editor loses focus.
export function onLeave() { armAutosave(true); }

// onMode follows vim's mode: insert stops the countdown, leaving it
// starts one.
export function onMode(mode) {
  const inserting = mode === 'insert' || mode === 'replace';
  if (inserting === SRC.inserting) return;
  SRC.inserting = inserting;
  if (inserting) clearTimeout(autoTimer);
  else armAutosave();
}

// flush saves the changes now, when autosave may; it reports whether
// nothing unsaved is left.
export async function flush() {
  if (!SRC.dirty) return true;
  if (!canAutosave()) return false;
  return save(null, true);
}

// discard drops the unsaved changes (:e!), reading the file again.
export function discard() {
  if (!SRC.file) return;
  load(SRC.file, true).then(() => { say('changes discarded'); followPending(); });
}

// The conflict's choices.
export function conflictReload() {
  const v = SRC.conflict;
  S.overlay = null;
  SRC.conflict = null;
  if (!v) return;
  SRC.view = v;
  SRC.dirty = false;
  SRC.stale = false;
  show(v, true);
  say('reloaded ' + v.file + ': your changes are gone');
  followPending();
}

export function conflictOverwrite() {
  const v = SRC.conflict;
  if (v) save(v.hash);
}

export function conflictCancel() {
  S.overlay = null;
  SRC.conflict = null;
  SRC.stale = true;
  say('kept your changes: ' + savesWith() + ' asks again, ' + discardsWith() + ' discards them');
}

export function toggleDiff() { SRC.diffing = !SRC.diffing; }

// conflictKey is the conflict overlay's keys.
export function conflictKey(k) {
  switch (k) {
    case 'r': conflictReload(); return true;
    case 'o': conflictOverwrite(); return true;
    case 'd': toggleDiff(); return true;
    case 'esc': case 'q': conflictCancel(); return true;
  }
  return true;
}

// ---- showing and focusing the pane ----

export function toggleSource() {
  if (SRC.shown && SRC.dirty) { say(unsavedIn(SRC.file)); return; }
  // Pane 1 and pane 2 are never both hidden.
  if (SRC.shown && S.rightHidden) { say('pane 2 hidden: ' + rightKey() + ' shows it'); return; }
  SRC.shown = !SRC.shown;
  try { localStorage.setItem(shownKey, SRC.shown ? '1' : '0'); } catch { /* no storage */ }
  if (SRC.shown) {
    setFocus('source');
    syncFile();
  } else {
    if (S.focus === 'source') { S.focus = 'sql'; S.zoomed = false; }
    SRC.file = '';
    SRC.view = null;
  }
}

// editSource gives the keys to the editor.
export function editSource() {
  if (S.overlay) return;
  SRC.editing = true;
  if (ed && !ed.view.hasFocus) ed.focus();
  queueMicrotask(() => { if (SRC.editing && !S.overlay && ed && !ed.view.hasFocus) ed.focus(); });
}

// returnToSource gives the keys back to the editor in vim's normal mode,
// as a palette closed with nothing picked does; without vim, to type.
export function returnToSource() {
  editSource();
  const adapter = ed && getCM(ed.view);
  const vim = adapter && adapter.state.vim;
  if (vim && (vim.insertMode || vim.visualMode)) Vim.handleKey(adapter, '<Esc>', 'user');
}

// searchSource gives the keys to the editor and starts vim's / there, or
// without vim opens the find panel, as cmd+f does.
const findKey = searchKeymap.find((b) => b.key === 'Mod-f');
export function searchSource() {
  editSource();
  queueMicrotask(() => {
    if (!ed) return;
    const adapter = getCM(ed.view);
    if (adapter) Vim.handleKey(adapter, '/', 'user');
    else if (findKey) findKey.run(ed.view);
  });
}

export function blurSource() {
  SRC.editing = false;
  if (ed && ed.view.hasFocus) ed.blur();
}

// sourceKey is the focused pane's keys while the editor doesn't have them.
export function sourceKey(k) {
  switch (k) {
    case 'enter': case 'i': case 'a': editSource(); return true;
    case '/': searchSource(); return true;
    case 'backspace': goBack(); return true; // back from a gd
    case 'ctrl+^': case 'ctrl+6': altFile(); return true;
  }
  return false;
}

// blocks says whether selecting id would lose unsaved changes: it is an
// action of another file. With autosave on, the changes are saved first
// and id is selected once they are.
export function blocks(id, opts) {
  if (!SRC.dirty) return false;
  const same = S.view && [...S.view.pane, ...(S.view.externals || [])].some((a) => a.id === id && a.file === SRC.file);
  if (same || id === S.sel) return false;
  if (canAutosave()) {
    flush().then((ok) => {
      if (ok) selectAction(id, opts);
      else say(SRC.file + ' changed on disk: ' + savesWith() + ' to choose what to keep');
    });
    return true;
  }
  say(SRC.stale ? staleIn(SRC.file) : unsavedIn(SRC.file));
  return true;
}

hooks.sourceBlocks = blocks;
hooks.sourceHolds = () => SRC.dirty;
hooks.sourceSave = () => save();
hooks.sourceFile = () => (SRC.shown ? SRC.file : '');
hooks.sourceView = syncFile;
hooks.placeAction = placeAction;
hooks.goToLine = goToLine;
hooks.sourceCompiled = () => { checkDisk(); if (hooks.sourceDiagnose) hooks.sourceDiagnose(); };
hooks.sourceKey = sourceKey;
hooks.sourceFocus = editSource;
hooks.sourceBlur = blurSource;
hooks.sourceReturn = returnToSource;
hooks.toggleSource = toggleSource;
hooks.conflictKey = conflictKey;
hooks.sourceShownNow = () => SRC.shown;
// sourceLang is the language of the file the pane shows, '' for none.
hooks.sourceLang = () => (SRC.shown && SRC.view && SRC.view.file === SRC.file ? SRC.view.lang : '');
hooks.sourceLoading = () => SRC.shown && SRC.loading;
hooks.sourceEditing = () => SRC.shown && SRC.editing;
hooks.sourceFlush = () => (canAutosave() ? flush() : Promise.resolve(false));
hooks.sourceAutosaves = () => canAutosave();
hooks.altFile = altFile;

window.addEventListener('focus', () => checkDisk());
// Going to another project keeps unsaved text in the tab's workspace, so
// only leaving xf web asks.
window.addEventListener('beforeunload', (e) => {
  if (!SRC.dirty || S.switching) return;
  e.preventDefault();
  e.returnValue = '';
});

// ---- the difference ----

export { diffRows } from './diff.js';
