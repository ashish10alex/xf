// Search (ADR 0016): the project's files searched for text, and the
// matches replaced, in the far-left column the file tree shows in (tree.js
// keeps which view it shows), as VS Code's search view. cmd+shift+f
// (ctrl+shift+f, and on a Mac that too) shows it with the box focused.
// j/k in its list show each match in the source pane, enter goes there.
//
// The server matches (POST /api/search), so a replace (POST
// /api/search/replace) changes what was listed; it skips a match that no
// longer fits its file. Matches in the file the source pane shows are
// replaced in the editor instead, where u undoes them, and the server keeps
// the last replace's other files for an undo (POST /api/search/undo).

import { S, post, redraw, hooks, setFocus, onMac, say } from './state.js';
import { SRC, sourceEditor, goToLine } from './source.js';
import { T, showView } from './tree.js';
import { undoesWith } from './vim-mode.js';

export const F = {
  query: '',
  replace: '',
  case: false,
  word: false,
  regex: false,
  include: '',
  exclude: '',
  globs: false,      // the include and exclude fields show
  replacing: false,  // the replace field shows
  result: null,      // searchView from the server
  asked: '',         // the request result answers, as JSON
  busy: false,
  error: '',
  cursor: '',        // the row the keys act on, by key
  closed: {},        // the files collapsed, by path
  dismissed: {},     // the matches x took out, by matchKey
  box: '',           // the field to focus on the next draw
  // prompt is replace all's question: {count, files}.
  prompt: null,
  undo: false,       // the server can undo the last replace
  replaced: '',      // the file shown that the last replace changed
};

export const searchKey = onMac ? '⇧⌘f' : 'ctrl+shift+f';

// isSearchKey is cmd+shift+f, or ctrl+shift+f, on a Mac, and ctrl+shift+f
// elsewhere. The code is read, as shift changes the key.
export function isSearchKey(e) {
  if (!e.shiftKey || e.altKey || (e.code !== 'KeyF' && e.key !== 'f' && e.key !== 'F')) return false;
  return onMac ? e.metaKey !== e.ctrlKey : e.ctrlKey && !e.metaKey;
}

const phone = () => !!(window.matchMedia && window.matchMedia('(max-width: 760px)').matches);

// ---- what the browser keeps, per project ----

const root = () => (S.boot && S.boot.root) || '';
const storeKey = () => 'xf-web:search:' + root();
const kept = ['query', 'replace', 'case', 'word', 'regex', 'include', 'exclude', 'globs', 'replacing'];
let loadedFor = null;

// restore reads the project's search, once per project: the view calls it
// as it draws.
export function restore() {
  if (loadedFor === root()) return;
  loadedFor = root();
  Object.assign(F, { query: '', replace: '', case: false, word: false, regex: false, include: '', exclude: '', globs: false, replacing: false,
    result: null, asked: '', busy: false, error: '', cursor: '', closed: {}, dismissed: {}, box: '', prompt: null, undo: false, replaced: '' });
  try {
    const v = JSON.parse(localStorage.getItem(storeKey()) || '{}');
    for (const k of kept) if (k in v) F[k] = v[k];
  } catch { /* no storage */ }
  if (shown() && F.query) search(0);
}

function keep() {
  const v = {};
  for (const k of kept) v[k] = F[k];
  try { localStorage.setItem(storeKey(), JSON.stringify(v)); } catch { /* no storage */ }
}

export const shown = () => T.shown && T.view === 'search';

// ---- searching ----

let seq = 0, timer = 0;

// request is what the server is asked: the options, and the source
// pane's text while it has unsaved changes, searched in place of its file.
function request() {
  const r = { query: F.query, case: F.case, word: F.word, regex: F.regex, include: F.include, exclude: F.exclude, replacing: F.replacing, replace: F.replace };
  if (SRC.dirty && SRC.file && sourceEditor()) r.shown = { file: SRC.file, text: sourceEditor().view.state.doc.toString() };
  return r;
}

// search asks again after ms, so typing asks once it stops; an answer to
// an older request is dropped.
export function search(ms = 200) {
  restore();
  clearTimeout(timer);
  if (!F.query) {
    seq++;
    Object.assign(F, { result: null, asked: '', busy: false, error: '' });
    redraw();
    return;
  }
  timer = setTimeout(load, ms);
}

async function load() {
  const r = request();
  const asked = JSON.stringify(r);
  const n = ++seq;
  F.busy = true;
  redraw();
  try {
    const v = await post('/api/search', r);
    if (n !== seq) return;
    const changed = F.asked !== asked;
    F.result = v;
    F.asked = asked;
    F.error = v.error || '';
    F.busy = false;
    if (changed) F.dismissed = keepDismissed(v);
    if (!F.cursor || !rows().some((x) => x.key === F.cursor)) F.cursor = (rows()[0] || {}).key || '';
  } catch (e) {
    if (n !== seq) return;
    F.busy = false;
    F.error = e.message;
  }
  redraw();
}

// keepDismissed keeps what x took out of a list that is still listed.
function keepDismissed(v) {
  const now = {};
  for (const f of v.files) {
    if (F.dismissed['f:' + f.path]) now['f:' + f.path] = true;
    for (const m of f.matches) if (F.dismissed[matchKey(f.path, m)]) now[matchKey(f.path, m)] = true;
  }
  return now;
}

const matchKey = (path, m) => 'm:' + path + '\u0000' + m.line + '\u0000' + m.at + '\u0000' + m.text;

// set changes an option and searches again.
export function set(k, v) {
  restore();
  F[k] = v;
  keep();
  if (k === 'replacing' || k === 'replace') {
    // What a match becomes is the server's answer.
    if (F.query) search(k === 'replace' ? 120 : 0);
    redraw();
    return;
  }
  search(k === 'query' || k === 'include' || k === 'exclude' ? 200 : 0);
}

export function toggle(k) { set(k, !F[k]); }

// ---- the rows ----

// files are the result's files, without what x took out.
function files() {
  if (!F.result) return [];
  const out = [];
  for (const f of F.result.files) {
    if (F.dismissed['f:' + f.path]) continue;
    const matches = f.matches.filter((m) => !F.dismissed[matchKey(f.path, m)]);
    if (matches.length) out.push({ ...f, matches });
  }
  return out;
}

// rows are the list as drawn: a file, then its matches while it's open.
export function rows() {
  const out = [];
  for (const f of files()) {
    const open = !F.closed[f.path];
    out.push({ key: 'f:' + f.path, kind: 'file', path: f.path, file: f, open, count: f.matches.length });
    if (open) for (const m of f.matches) out.push({ key: matchKey(f.path, m), kind: 'match', path: f.path, file: f, m });
  }
  return out;
}

// counts are what the list shows: its matches and files.
export function counts() {
  const fs = files();
  return { matches: fs.reduce((n, f) => n + f.matches.length, 0), files: fs.length };
}

const current = () => rows().find((r) => r.key === F.cursor);

// ---- moving and showing ----

function move(n, preview = true) {
  const rs = rows();
  if (!rs.length) return;
  let i = rs.findIndex((r) => r.key === F.cursor);
  if (n === -Infinity) i = 0;
  else if (n === Infinity) i = rs.length - 1;
  else i = Math.min(Math.max((i < 0 ? 0 : i) + n, 0), rs.length - 1);
  F.cursor = rs[i].key;
  if (preview && rs[i].kind === 'match') show(rs[i], false);
}

// show puts the source pane at row's match: with edit, the editor gets the
// keys in vim's normal mode, else the keys stay in the list.
function show(r, edit) {
  if (!r || r.kind !== 'match') return;
  goToLine(r.path, r.file.action || '', r.m.line, r.m.col, { preview: !edit });
  if (edit) {
    if (hooks.sourceReturn) hooks.sourceReturn();
    if (phone()) { T.shown = false; }
  } else setFocus('tree');
}

// step is F4 and shift+F4: the next or previous match, from anywhere,
// shown in the source pane; the list keeps the keys when it has them.
export function step(n) {
  restore();
  const ms = rows().filter((r) => r.kind === 'match');
  if (!ms.length) { say(F.query ? 'no matches' : 'nothing searched: ' + searchKey + ' searches'); return; }
  const all = rows();
  let i = all.findIndex((r) => r.key === F.cursor);
  // The next match after the cursor, a file's first when it's on the file.
  let next;
  if (n > 0) next = all.slice(i + 1).find((r) => r.kind === 'match') || ms[0];
  else next = all.slice(0, Math.max(i, 0)).reverse().find((r) => r.kind === 'match') || ms[ms.length - 1];
  F.cursor = next.key;
  const inList = S.focus === 'tree' && shown();
  show(next, !inList);
}

function open(r) {
  if (!r) return;
  if (r.kind === 'file') {
    if (F.closed[r.path]) delete F.closed[r.path];
    else F.closed[r.path] = true;
    return;
  }
  show(r, true);
}

// up is h: a file's matches close, a match goes to its file.
function up(r) {
  if (!r) return;
  if (r.kind === 'match') { F.cursor = 'f:' + r.path; return; }
  F.closed[r.path] = true;
}

function dismiss(r) {
  if (!r) return;
  const rs = rows();
  const i = rs.findIndex((x) => x.key === r.key);
  F.dismissed[r.key] = true;
  const after = rows();
  F.cursor = (after[Math.min(i, after.length - 1)] || {}).key || '';
}

// ---- the box ----

// openSearch is the key: the view shows with the box focused, on the
// source editor's selection when it has one on one line, else on the
// last query, selected.
export function openSearch() {
  restore();
  const ed = sourceEditor();
  if (ed && SRC.editing) {
    const sel = ed.view.state.selection.main;
    const text = ed.view.state.sliceDoc(sel.from, sel.to);
    if (text && !text.includes('\n')) { F.query = text; keep(); }
  }
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  showView('search');
  S.zoomed = false;
  setFocus('tree');
  F.box = 'query';
  F.prompt = null;
  search(0);
  redraw();
}

// focusSearch gives the view the keys, as ctrl+0 does: the list when it
// has rows, else the box.
export function focusSearch() {
  restore();
  S.zoomed = false;
  setFocus('tree');
  if (!rows().length) F.box = 'query';
}

// toList is enter or ↓ in a box: the list gets the keys.
export function toList() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  setFocus('tree');
  if (!rows().some((r) => r.key === F.cursor)) F.cursor = (rows()[0] || {}).key || '';
  const r = current();
  if (r && r.kind === 'file' && rows()[1]) move(1);
  else if (r) show(r, false);
  redraw();
}

// leaveBox is esc in a box: the keys go back to the source editor, or
// to the list when the pane is hidden.
export function leaveBox() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  if (SRC.shown && SRC.file) { setFocus('source'); if (hooks.sourceReturn) hooks.sourceReturn(); }
  else setFocus('tree');
  redraw();
}

// boxKey is a key in one of the view's fields, before the field has it:
// alt+c, alt+w and alt+r toggle case, whole word and regex, and cmd+alt+
// enter (ctrl+alt+enter) replaces all, as in VS Code. It reports whether
// it used the key.
export function boxKey(e) {
  if (e.altKey && !e.metaKey && !e.ctrlKey && !e.shiftKey) {
    const k = { KeyC: 'case', KeyW: 'word', KeyR: 'regex' }[e.code];
    if (k) { toggle(k); return true; }
  }
  if (e.key === 'Enter' && e.altKey && (onMac ? e.metaKey : e.ctrlKey)) { askReplaceAll(); return true; }
  if (e.key === 'Enter' || e.key === 'ArrowDown') { toList(); return true; }
  if (e.key === 'Escape') { leaveBox(); return true; }
  return false;
}

// ---- replacing ----

// inEditor says whether the source editor shows path, where a replace
// goes into its text.
function inEditor(path) {
  const ed = sourceEditor();
  return !!(ed && SRC.shown && SRC.file === path && SRC.view && SRC.view.file === path && !SRC.diffing);
}

// replaceInEditor changes matches in the editor's text, in one change u
// undoes, skipping one whose text isn't where the search found it.
function replaceInEditor(matches) {
  const view = sourceEditor().view;
  const doc = view.state.doc;
  const changes = [];
  let skipped = 0;
  for (const m of matches) {
    if (m.line >= doc.lines) { skipped++; continue; }
    const from = doc.line(m.line + 1).from + m.col, to = from + m.len;
    if (to > doc.length || doc.sliceString(from, to) !== m.text) { skipped++; continue; }
    changes.push({ from, to, insert: m.with });
  }
  if (changes.length) view.dispatch({ changes, userEvent: 'input.replace' });
  return { replaced: changes.length, skipped };
}

// replace replaces the matches of targets, [{path, hash, matches}]: the
// file the editor shows there, the others on disk.
async function replace(targets) {
  if (!F.replacing) { say('show the replace field first (the ▸ beside the box)'); return; }
  if (!targets.length) return;
  const disk = targets.filter((f) => !inEditor(f.path));
  const here = targets.find((f) => inEditor(f.path));
  const parts = [];
  if (here) {
    const r = replaceInEditor(here.matches);
    if (r.replaced) {
      F.replaced = here.path;
      parts.push((r.replaced === 1 ? '1 match' : r.replaced + ' matches') + ' in ' + here.path + ', unsaved: ' + undoesWith() + ' undoes it');
    }
    if (r.skipped) parts.push(r.skipped + ' skipped in ' + here.path + ': its text changed');
  }
  if (disk.length) {
    try {
      const v = await post('/api/search/replace', {
        query: F.query, case: F.case, word: F.word, regex: F.regex, replacing: true, replace: F.replace,
        files: disk.map((f) => ({ path: f.path, hash: f.hash || '', matches: f.matches.map((m) => ({ line: m.line, text: m.text, lineText: m.lineText, at: m.at })) })),
      });
      F.undo = v.undo;
      parts.unshift(v.message);
    } catch (e) { parts.unshift(e.message); }
  }
  say(parts.join(' · '));
  search(0);
}

const asTargets = (fs) => fs.map((f) => ({ path: f.path, hash: f.hash, matches: f.matches }));

// replaceRow is r and a row's button: a match, or a file's matches.
export function replaceRow(r) {
  if (!r) return;
  replace(asTargets([r.kind === 'file' ? r.file : { ...r.file, matches: [r.m] }]));
}

// askReplaceAll asks before replacing every match listed, refusing while
// the list stops short of them all.
export function askReplaceAll() {
  restore();
  if (!F.replacing) { set('replacing', true); F.box = 'replace'; redraw(); return; }
  if (F.result && F.result.truncated) { say('the list stops at ' + F.result.count + ' matches: narrow the search to replace them all'); return; }
  const c = counts();
  if (!c.matches) { say('nothing to replace'); return; }
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  setFocus('tree');
  F.prompt = c;
  redraw();
}

function promptKey(k) {
  if (k === 'y' || k === 'enter') { F.prompt = null; replace(asTargets(files())); return true; }
  F.prompt = null;
  say('replaced nothing');
  return true;
}

export async function undoReplace() {
  try {
    const v = await post('/api/search/undo');
    F.undo = false;
    say(v.message + (F.replaced ? ' · in ' + F.replaced + ', ' + undoesWith() + ' undoes it' : ''));
  } catch (e) { F.undo = false; say(e.message); }
  F.replaced = '';
  search(0);
}

// ---- keys ----

// key is the list's keys while the view has focus; its fields have their
// own (boxKey).
export function key(k) {
  restore();
  if (F.prompt) return promptKey(k);
  const r = current();
  switch (k) {
    case 'j': case 'down': move(1); return true;
    case 'k': case 'up': move(-1); return true;
    case 'g': case 'home': move(-Infinity); return true;
    case 'G': case 'end': move(Infinity); return true;
    case 'ctrl+d': case 'pgdown': move(15); return true;
    case 'ctrl+u': case 'pgup': move(-15); return true;
    case 'enter': case 'o': open(r); return true;
    case 'l': case 'right':
      if (r && r.kind === 'file') { if (F.closed[r.path]) delete F.closed[r.path]; else move(1); }
      return true;
    case 'h': case 'left': up(r); return true;
    case 'x': dismiss(r); return true;
    case 'r': replaceRow(r); return true;
    case 'R': search(0); say('searching again'); return true;
    case '/': case 'esc': F.box = 'query'; return true;
  }
  return false;
}

export function clickRow(r) {
  F.cursor = r.key;
  setFocus('tree');
  open(r);
  redraw();
}

// commands are the palette's Search commands: [label, key, run].
function commands() {
  const out = [['Search: in files', searchKey, openSearch]];
  if (F.undo) out.push(['Search: undo the last replace', '', undoReplace]);
  return out;
}

// ---- wiring ----

hooks.searchKey = key;
hooks.searchFocus = focusSearch;
hooks.openSearch = openSearch;
hooks.searchStep = step;
hooks.searchCommands = commands;
hooks.searchShown = () => { restore(); return shown(); };
// A file changed on disk: the matches are searched again, the cursor
// kept where it is.
const filesChanged = hooks.filesChanged;
hooks.filesChanged = () => { if (filesChanged) filesChanged(); if (shown() && F.query) search(300); };
// The source pane's unsaved text, which was searched in place of its
// file, is gone (:e!, another file shown): its file is searched again.
const sourceLoaded = hooks.sourceLoaded;
hooks.sourceLoaded = (v) => {
  if (sourceLoaded) sourceLoaded(v);
  if (shown() && F.query && F.asked.includes('"shown":')) search(0);
};
