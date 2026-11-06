// The settings page: an Overview of the project (or, outside one, the
// tools and the Google sign-in), read-only, and the Settings: xf's config
// files, the user's (Global) and the open project's .xf/config.json
// (Project), as rows a field at a time (UI) or as the file's text (JSON). Each field saves on its own (enter, leaving
// it, or a choice) with the check a file's value gets; "remove" takes a
// key out of the file. The JSON view saves the whole file, after
// sourceAutosaveMs of no typing or with :w / cmd+s, once it checks.
// Settings xf web reads as it goes apply at once; the rest when the
// project reopens, which a banner offers. The gear at the top and , open
// it; ctrl+q leaves it.

import { S, get, getLines, post, redraw, hooks, fitFocus, pane2Default, say } from './state.js';
import { setVimKeys, setVimMode, setWrap } from './editor.js';

export const SECTIONS = ['Running', 'Tool', 'Editor & look', 'Scratch & source', 'Git hosting'];
export const GIT = 'Git hosting'; // the hosts and their tokens (forge-view.js), then forgePollSeconds
export const VIM = 'Vim'; // the vim keys of the editor, in the user's file
export const INFO = 'info'; // the read-only section: the project's, or the tools'

export const SET = {
  open: false,
  view: 'overview', // overview (the info, read-only) or settings; kept between openings
  only: 'all',     // the rows shown: all, or changed (those the scope's file sets)
  tab: 'user',     // the scope: user (Global) or project
  mode: 'ui',      // ui (the cards) or json (the file's text)
  data: null,      // the server's settings: levels, warnings, keys, pending
  error: '',       // why they couldn't be read
  cursor: 0,       // the selected card among those shown
  reveal: false,   // scroll the selected card into view on the next draw
  query: '', typing: false,
  editing: '',     // the key whose field has the keys
  drafts: {},      // tab:key → text typed and not saved
  errors: {},      // tab:key → why a save was refused
  saved: '',       // where the last change went
  failed: '',      // why the last change was refused, for the footer
  pyrefly: null,   // /api/python/status for the project: its pyrefly config and interpreter, null for none
  info: null,      // the project's info rows, or outside one the tools' and Google's, read-only
  reopening: false,
  section: '',     // the section the rows are scrolled to (the sidebar's highlight)
  goTo: '',        // a section to scroll to on the next draw
  closing: false,  // asking whether to save or discard JSON drafts before closing
  json: {},        // level → its file in the JSON view (see fileState)
};

const reopenKey = 'xf-web:settings-reopen';
const id = (key) => SET.tab + ':' + key;

// fileState is a level's file as the JSON view has it: the text last
// loaded or saved (base) and its hash, the text typed since (draft, null
// for none), the marks of the last check, a word on where it stands, and
// the file on disk when it changed under a draft (conflict).
function fileState(level) {
  if (!SET.json[level]) SET.json[level] = { loaded: false, base: '', hash: '', found: false, draft: null, marks: [], status: '', bad: false, conflict: null, saving: false };
  return SET.json[level];
}
export const jsonOf = (level) => fileState(level || levelName());
// hasDraft says whether level (or any level) has JSON not saved yet.
export const hasDraft = (level) => (level ? fileState(level).draft !== null : Object.values(SET.json).some((j) => j.draft !== null));

// openSettings shows the page, on view when given, else the one it showed
// last: in a project both levels, else the user's.
export async function openSettings(view) {
  SET.open = true;
  if (view) SET.view = view;
  SET.cursor = 0;
  SET.reveal = true;
  SET.query = '';
  SET.typing = false;
  SET.editing = '';
  SET.saved = '';
  SET.failed = '';
  SET.errors = {};
  SET.drafts = {};
  SET.closing = false;
  SET.json = {};
  SET.section = '';
  SET.info = null;
  SET.pyrefly = null;
  redraw();
  const info = readInfo(false); // beside the settings, not after them
  await load();
  if (!hasProject() && SET.tab === 'project') SET.tab = 'user';
  if (SET.mode === 'json') loadFile(levelName());
  await info;
}

// refreshInfo asks again for the info rows, e.g. after the user re-authenticates
// in the terminal and focuses the browser; the JSON view looks at its
// file again too.
export async function refreshInfo() {
  if (!SET.open) return;
  checkFiles();
  await readInfo(true);
}

// infoPath is where the info rows come from: the project's, or outside
// one the tools' and Google's; '' for none.
function infoPath() {
  if (S.mode === 'project' && S.caps && S.caps.info) return '/api/info?following=' + (S.following ? '1' : '0') + '&stream=1';
  if (S.mode === 'picker') return '/api/picker/info?stream=1';
  return '';
}

// readInfo asks for the info rows, which come as lines: the first at
// once, with "…" for each lookup still going (the tool's version, git,
// gcloud, Google's token), then one more as each lands, so what is known
// shows at once and a slow gcloud holds up only its own rows. Each line
// is drawn as it comes, except asking again over whole rows (refresh):
// those stay until the new ones are whole, so they don't blink to "…".
let infoAsk = null;
async function readInfo(again) {
  const path = infoPath();
  if (!path) return;
  if (infoAsk) infoAsk.abort();
  const ask = infoAsk = new AbortController();
  const live = !again || !SET.info || SET.info.some((r) => r.value === '…');
  let last = null;
  try {
    await getLines(path, (rows) => {
      if (ask !== infoAsk) return;
      last = rows;
      if (live) { SET.info = rows; redraw(); }
    }, ask.signal);
    if (ask === infoAsk && last && !live) { SET.info = last; redraw(); }
  } catch {
    if (ask === infoAsk && !SET.info) { SET.info = []; redraw(); }
  } finally {
    if (ask === infoAsk) infoAsk = null;
  }
}


// closeSettings leaves the page; with a JSON draft it asks first, and
// force leaves without asking (the drafts were saved or discarded).
export function closeSettings(force) {
  if (!force && hasDraft()) { SET.closing = true; redraw(); return; }
  SET.open = false;
  SET.editing = '';
  SET.closing = false;
  if (infoAsk) { infoAsk.abort(); infoAsk = null; }
  clearTimeout(autoTimer);
  redraw();
}

// closeSaving saves every draft, then leaves when they all saved.
export async function closeSaving() {
  for (const level of Object.keys(SET.json)) {
    if (hasDraft(level)) await saveFile(level);
  }
  if (hasDraft()) { SET.closing = false; redraw(); return; } // a save was refused: its marks show
  closeSettings(true);
}

// closeDiscarding drops the drafts and leaves.
export function closeDiscarding() {
  for (const j of Object.values(SET.json)) { j.draft = null; j.conflict = null; }
  closeSettings(true);
}

async function load() {
  try {
    SET.data = await get('/api/settings');
    SET.error = '';
  } catch (e) {
    SET.error = e.message;
  }
  redraw();
  if (hasProject()) readPyrefly();
}

// readPyrefly asks for the project's pyrefly: the config it uses and the
// interpreter, which the Python language server row shows.
async function readPyrefly() {
  try {
    SET.pyrefly = await get('/api/python/status');
  } catch {
    SET.pyrefly = null;
  }
  redraw();
}

// createPyreflyConfig writes the project's pyrefly.toml, as Python: create
// pyrefly config does.
export async function createPyreflyConfig() {
  try {
    const r = await post('/api/python/config', { create: true });
    say(r.message);
  } catch (e) {
    say(e.message);
  }
  await readPyrefly();
}

// openPyreflyConfig leaves the page for the config file, which is in the
// project.
export function openPyreflyConfig() {
  const file = SET.pyrefly && SET.pyrefly.config;
  if (!file || !hooks.openFile) return;
  closeSettings(true);
  hooks.openFile(file);
}

export const hasProject = () => !!(SET.data && SET.data.root);
export const levelName = () => (SET.tab === 'project' ? 'project' : 'user');
export const levelOf = (name) => SET.data && SET.data.levels.find((l) => l.level === name);
export const level = () => levelOf(levelName());

// hasInfo says whether the page has an Overview: the project's info, or
// outside one the tools' and Google's.
export const hasInfo = () => (S.mode === 'project' && !!(S.caps && S.caps.info)) || S.mode === 'picker';

// viewShown is the view the page shows: the Settings when there is no
// Overview to show.
export const viewShown = () => (hasInfo() ? SET.view : 'settings');

// setView shows the Overview or the Settings.
export function setView(view) {
  if (view === 'overview' && !hasInfo()) return;
  SET.view = view;
  SET.editing = '';
  SET.reveal = true;
  redraw();
}

// setOnly shows all the rows, or only those the scope's file sets.
export function setOnly(only) {
  SET.only = only;
  SET.cursor = 0;
  SET.reveal = true;
  redraw();
}

// sectionsShown are the sections of keys the page has, in order: the
// tool's sections, then Vim.
export function sectionsShown() {
  if (!SET.data) return [];
  return [...SECTIONS, VIM].filter((s) => SET.data.keys.some((k) => k.section === s));
}

// matches says whether k is one the search finds: by title, key, doc,
// hint or section.
function matches(k, q) {
  return !q || (k.title + ' ' + k.key + ' ' + k.doc + ' ' + k.hint + ' ' + k.section).toLowerCase().includes(q);
}

// rows are the keys shown: by section, then as documented, those the
// search matches (and, showing the changed only, the scope's file sets).
export function rows() {
  if (!SET.data) return [];
  const q = SET.query.trim().toLowerCase();
  const keys = SET.data.keys.filter((k) => matches(k, q) && (SET.only !== 'changed' || fileValue(k) !== null));
  return [...SECTIONS, VIM].flatMap((s) => keys.filter((k) => k.section === s));
}

// matchesAll is how many keys the search finds among them all, whether
// or not they show only the changed: what showing them all would find.
export function matchesAll() {
  if (!SET.data) return 0;
  const q = SET.query.trim().toLowerCase();
  return SET.data.keys.filter((k) => matches(k, q)).length;
}

// fileValue is what the scope's file says of k, null when nothing.
export const fileValue = (k) => (levelName() === 'user' ? k.user : k.project);
// changedCount is how many keys level's file (the scope's when unset) sets.
export const changedCount = (lvl) => (SET.data ? SET.data.keys.filter((k) => ((lvl || levelName()) === 'user' ? k.user : k.project) != null).length : 0);
export const draftOf = (k) => SET.drafts[id(k.key)];
export const errorOf = (k) => SET.errors[id(k.key)];

// locked says why the scope can't change k, '' when it can.
export function locked(k) {
  const l = level();
  if (!l) return 'no project is open';
  if (l.error) return l.error;
  if (SET.tab === 'project' && k.userOnly) return 'Global only';
  return '';
}

export function setDraft(k, text) {
  SET.drafts[id(k.key)] = text;
  delete SET.errors[id(k.key)];
}

const draftRefusal = 'unsaved JSON draft: save or discard';

// save writes value for k at the scope's level; unset takes it out. An
// empty value for a key the file doesn't set is written only with force.
// While the JSON view has a draft of the file, it refuses: two edits of
// one file would fight.
export async function save(k, value, unset, force) {
  if (locked(k)) return;
  const key = id(k.key);
  if (!unset && value === undefined) return;
  const was = fileValue(k);
  // Nothing changed, or an empty field that was empty: nothing to write.
  if (!unset && (was === value || (was === null && value === '' && !force))) { delete SET.drafts[key]; redraw(); return; }
  if (hasDraft(levelName())) {
    SET.errors[key] = draftRefusal;
    SET.failed = draftRefusal;
    redraw();
    return;
  }
  try {
    const a = await post('/api/settings', { level: levelName(), key: k.key, value: unset ? '' : value, unset: !!unset });
    SET.data = a;
    delete SET.drafts[key];
    delete SET.errors[key];
    SET.failed = '';
    SET.saved = (unset ? 'took ' + k.key + ' out of ' : 'saved ' + k.key + ' to ') + a.saved;
  } catch (e) {
    SET.errors[key] = e.message;
    SET.failed = k.key + ': ' + e.message;
  }
  redraw();
}

// commit saves k's draft, if there is one; emptying a set field takes the
// key out, as remove does.
export function commit(k) {
  const d = draftOf(k);
  if (d === undefined) return;
  const v = d.trim();
  if (v === '' && fileValue(k) !== null) save(k, '', true);
  else save(k, v);
}

// setEmpty writes an empty value, which only prodCompilerOptions gives a
// meaning: the default targets are prod.
export function setEmpty(k) { save(k, '', false, true); }

export async function reopen() {
  if (SET.reopening) return;
  SET.reopening = true;
  redraw();
  try {
    try { sessionStorage.setItem(reopenKey, SET.tab); } catch { /* no storage */ }
    await post('/api/reopen');
  } catch (e) {
    try { sessionStorage.removeItem(reopenKey); } catch { /* no storage */ }
    SET.reopening = false;
    say(e.message);
  }
}

// reopenAfterReload opens the page again after Reopen now reloaded it.
export function reopenAfterReload() {
  let tab = null;
  try { tab = sessionStorage.getItem(reopenKey); sessionStorage.removeItem(reopenKey); } catch { /* no storage */ }
  if (!tab) return;
  openSettings().then(() => { if (tab === 'project' && hasProject()) { SET.tab = 'project'; redraw(); } });
}

// setTab picks the scope, Global (user) or Project; a JSON draft of the
// other stays.
export function setTab(tab) {
  if (tab === 'project' && !hasProject()) return;
  SET.tab = tab;
  SET.cursor = 0;
  SET.reveal = true;
  SET.editing = '';
  if (SET.mode === 'json') { loadFile(levelName()); showFile(); }
}

// setMode shows the rows (ui) or the file's text (json), on the
// Settings; a draft stays.
export function setMode(mode) {
  SET.view = 'settings';
  if (SET.mode === mode) { redraw(); return; }
  SET.mode = mode;
  SET.editing = '';
  if (mode === 'json') loadFile(levelName());
  else SET.reveal = true;
  redraw();
}

// flipScope is tab's: Project and Global in turn, Global only outside a
// project.
function flipScope() {
  if (!hasProject()) return;
  setTab(SET.tab === 'user' ? 'project' : 'user');
}

// cycle moves k's choice by dir and saves it.
function cycle(k, dir) {
  if (locked(k)) return;
  const choices = k.kind === 'bool' ? ['true', 'false'] : k.choices || [];
  if (!choices.length) return;
  const cur = fileValue(k);
  let i = choices.indexOf(cur);
  i = i < 0 ? (dir > 0 ? 0 : choices.length - 1) : (i + dir + choices.length) % choices.length;
  save(k, choices[i]);
}

function scrollPage(px) {
  const el = typeof document !== 'undefined' && document.querySelector('.set-main');
  if (el) el.scrollTop += px;
}

// overviewKey is the Overview's keys: it scrolls; / searches the Settings.
function overviewKey(k) {
  switch (k) {
    case 'j': case 'down': scrollPage(40); return true;
    case 'k': case 'up': scrollPage(-40); return true;
    case 'ctrl+d': case 'pgdown': scrollPage(300); return true;
    case 'ctrl+u': case 'pgup': scrollPage(-300); return true;
    case 'g': case 'home': scrollPage(-1e9); return true;
    case 'G': case 'end': scrollPage(1e9); return true;
    case 'r': refreshInfo(); return true;
    case 'c': setOnly('changed'); setView('settings'); return true;
    case '/': setView('settings'); SET.typing = true; return true;
    case 'R': if (SET.data && SET.data.pending.length) reopen(); return true;
  }
  return true;
}

// settingsKey is the page's keys while no field has them.
export function settingsKey(k) {
  if (SET.typing) return false;
  if (SET.closing && hasDraft()) {
    switch (k) {
      case 's': case 'enter': closeSaving(); return true;
      case 'd': closeDiscarding(); return true;
      case 'esc': case 'c': SET.closing = false; return true;
    }
    return true;
  }
  if (k === 'o' || k === 's') { setView(k === 'o' ? 'overview' : 'settings'); return true; }
  if (viewShown() === 'overview') return overviewKey(k);
  if (SET.mode === 'json') {
    switch (k) {
        case 'tab': case 'shift+tab': flipScope(); return true;
      case 'enter': case 'i': if (jsonEditor) jsonEditor.focus(); return true;
      case 'R': if (SET.data && SET.data.pending.length) reopen(); return true;
    }
    return true;
  }
  const list = rows();
  const cur = list[Math.min(SET.cursor, list.length - 1)];
  const move = (n) => {
    const next = Math.max(0, Math.min(list.length - 1, n));
    if (next === SET.cursor && n > SET.cursor) { scrollPage(40); return; }
    SET.cursor = next;
    SET.reveal = true;
  };
  switch (k) {
    case 'esc':
      if (SET.query) { SET.query = ''; SET.cursor = 0; SET.reveal = true; }
      return true;
    case 'j': case 'down': move(SET.cursor + 1); return true;
    case 'k': case 'up': move(SET.cursor - 1); return true;
    case 'ctrl+d': case 'pgdown': scrollPage(300); return true;
    case 'ctrl+u': case 'pgup': scrollPage(-300); return true;
    case 'g': case 'home': move(0); return true;
    case 'G': case 'end': move(list.length - 1); return true;
    case 'tab': case 'shift+tab': flipScope(); return true;
    case '/': SET.typing = true; return true;
    case 'c': setOnly(SET.only === 'changed' ? 'all' : 'changed'); return true;
    case 'R': if (SET.data && SET.data.pending.length) reopen(); return true;
    case 'enter':
      if (!cur) return true;
      if (cur.kind === 'choice' || cur.kind === 'bool') cycle(cur, 1);
      else if (!locked(cur)) SET.editing = cur.key;
      return true;
    case 'l': case 'right': if (cur) cycle(cur, 1); return true;
    case 'h': case 'left': if (cur) cycle(cur, -1); return true;
    case 'x': case 'backspace': if (cur && fileValue(cur) !== null) save(cur, '', true); return true;
  }
  return true; // the page has the keys
}

// ---- the JSON view ----

// The view has one editor (settings-view.js makes it), showing the
// scope's file: its draft, else the text last loaded or saved.
let jsonEditor = null;
export function setJSONEditor(ed) { jsonEditor = ed; showFile(); }

// emptyFile is what the view shows of a file not made yet.
const emptyFile = '{\n}\n';

// showFile puts the scope's file in the editor, with its marks.
export function showFile(keepCursor) {
  if (!jsonEditor) return;
  const j = jsonOf();
  if (!j.loaded) return;
  const text = j.draft !== null ? j.draft : j.base;
  if (jsonEditor.doc() !== text) jsonEditor.set(text, keepCursor);
  jsonEditor.mark(j.marks);
}

// loadFile reads level's file, once; later reads are checkFile's.
export async function loadFile(level) {
  const j = fileState(level);
  if (j.loaded || j.loading) return;
  j.loading = true;
  try {
    const f = await get('/api/settings/file?level=' + level);
    Object.assign(j, { loaded: true, base: f.found ? f.text : emptyFile, hash: f.hash, found: f.found, marks: [], status: '' });
  } catch (e) {
    j.status = e.message;
    j.bad = true;
  }
  j.loading = false;
  if (level === levelName()) showFile();
  redraw();
}

// checkFile looks at level's file on disk: changed and no draft, the view
// takes it, the cursor kept; changed under a draft, it asks.
async function checkFile(level) {
  const j = fileState(level);
  if (!j.loaded || j.saving) return;
  let f;
  try { f = await get('/api/settings/file?level=' + level); } catch { return; }
  if (f.hash === j.hash || j.saving) return;
  if (j.draft === null) {
    Object.assign(j, { base: f.found ? f.text : emptyFile, hash: f.hash, found: f.found, marks: [], status: 'reloaded: the file changed on disk', bad: false, conflict: null });
    if (level === levelName()) showFile(true);
  } else if (!j.conflict || j.conflict.hash !== f.hash) {
    j.conflict = f;
  }
  redraw();
}

function checkFiles() {
  for (const level of Object.keys(SET.json)) checkFile(level);
}

// reloadFile takes the file on disk over the draft.
export function reloadFile() {
  const j = jsonOf();
  if (!j.conflict) return;
  const f = j.conflict;
  Object.assign(j, { base: f.found ? f.text : emptyFile, hash: f.hash, found: f.found, draft: null, marks: [], status: 'reloaded', bad: false, conflict: null });
  if (SET.failed === draftRefusal) SET.failed = '';
  showFile(true);
  redraw();
}

// keepMine keeps the draft: the next save writes over the file on disk.
export function keepMine() {
  const j = jsonOf();
  if (!j.conflict) return;
  j.hash = j.conflict.hash;
  j.conflict = null;
  j.status = 'keeping yours: the next save writes over the file on disk';
  redraw();
  scheduleSave();
}

// autosaveMs is the pause after typing before the view saves: the source
// pane's sourceAutosaveMs, at least 500; 0 is off, and only :w and cmd+s
// save.
function autosaveMs() {
  const k = SET.data && SET.data.keys.find((x) => x.key === 'sourceAutosaveMs');
  const ms = k ? parseInt(k.value, 10) || 0 : 0;
  return ms > 0 ? Math.max(ms, 500) : 0;
}
export const jsonAutosave = () => autosaveMs() > 0;

let autoTimer = 0;

// onJSONEdit takes the editor's text as the scope's draft, and waits for
// a pause to check it and, with autosave, save it.
export function onJSONEdit(text) {
  const j = jsonOf();
  if (!j.loaded) return;
  const had = j.draft !== null;
  j.draft = text === j.base ? null : text;
  if (had !== (j.draft !== null)) redraw();
  scheduleSave();
}

function scheduleSave() {
  clearTimeout(autoTimer);
  const level = levelName();
  const ms = autosaveMs();
  autoTimer = setTimeout(() => {
    const j = fileState(level);
    if (j.draft === null) return;
    const bad = syntaxMarks(j.draft);
    if (bad.length) { setMarks(level, bad, 'not saved: line ' + bad[0].line + ': ' + bad[0].message); return; }
    if (ms > 0 && !j.conflict) saveFile(level);
    else if (j.marks.some((m) => m.severity === 'error' && !m.key)) setMarks(level, j.marks.filter((m) => m.key), '');
  }, ms || 400);
}

function setMarks(level, marks, status) {
  const j = fileState(level);
  j.marks = marks;
  j.status = status;
  j.bad = marks.some((m) => m.severity === 'error');
  if (level === levelName() && jsonEditor) jsonEditor.mark(marks);
  redraw();
}

// syntaxMarks are the marks of text that isn't JSON: where the parse
// stopped, as the browser says it.
function syntaxMarks(text) {
  try { JSON.parse(text); return []; } catch (e) {
    const msg = String(e.message || e);
    let line = 0;
    const lc = /line (\d+)/.exec(msg);
    const pos = /position (\d+)/.exec(msg);
    if (lc) line = +lc[1];
    else if (pos) line = text.slice(0, +pos[1]).split('\n').length;
    else line = text.split('\n').length;
    return [{ line, severity: 'error', message: msg.replace(/^JSON\.parse: /, '') }];
  }
}

// saveFile writes level's draft (the scope's when level is unset), once
// it parses; the server checks each value and refuses, with the lines,
// or answers with the settings.
export async function saveFile(level) {
  level = level || levelName();
  const j = fileState(level);
  clearTimeout(autoTimer);
  if (!j.loaded || j.saving) return;
  const text = j.draft !== null ? j.draft : j.base;
  if (j.draft === null && j.found) { j.status = 'no changes to save'; redraw(); return; }
  if (j.conflict) { j.status = 'the file changed on disk: reload, or keep yours'; redraw(); return; }
  const bad = syntaxMarks(text);
  if (bad.length) { setMarks(level, bad, 'not saved: line ' + bad[0].line + ': ' + bad[0].message); return; }
  j.saving = true;
  redraw();
  try {
    const a = await post('/api/settings/file', { level, text, hash: j.hash });
    const { file, marks, ...answer } = a;
    SET.data = answer;
    Object.assign(j, { base: file.text, hash: file.hash, found: true, conflict: null });
    if (j.draft === text) j.draft = null;
    const warns = marks || [];
    setMarks(level, warns, warns.length ? 'saved, with ' + warns.length + ' warning' + (warns.length === 1 ? '' : 's') : 'saved');
    SET.saved = 'saved ' + answer.saved;
    SET.failed = '';
    // Asked to close while it saved: nothing is left to ask about.
    if (SET.closing && !hasDraft()) closeSettings(true);
  } catch (e) {
    if (e.kind === 'conflict' && e.data && e.data.current) {
      j.conflict = e.data.current;
      j.status = 'not saved: the file changed on disk';
    } else if (e.kind === 'invalid' && e.data) {
      setMarks(level, e.data.marks || [], 'not saved: ' + e.message);
    } else {
      j.status = 'not saved: ' + e.message;
    }
  } finally {
    j.saving = false;
  }
  redraw();
}

// discardFile drops the scope's draft.
export function discardFile() {
  const j = jsonOf();
  j.draft = null;
  j.conflict = null;
  j.marks = [];
  j.bad = false;
  j.status = 'discarded';
  if (SET.failed === draftRefusal) SET.failed = '';
  showFile(true);
  redraw();
}

// Leaving xf web with a draft asks, as the source pane does.
window.addEventListener('beforeunload', (e) => {
  if (!SET.open || !hasDraft() || S.switching) return;
  e.preventDefault();
  e.returnValue = '';
});

hooks.settingsOpen = () => SET.open;
hooks.settingsKey = settingsKey;
hooks.openSettings = openSettings;
// settingsShowAll shows every setting on the Settings tab: Git hosting
// opens there (forge.js).
hooks.settingsShowAll = () => { setView('settings'); setOnly('all'); };
hooks.refreshSettingsInfo = refreshInfo;
hooks.vimKeys = setVimKeys;
hooks.vimMode = setVimMode;
hooks.wrapLines = setWrap;
hooks.settingsChanged = (ev) => {
  if (S.boot) {
    if (ev.pane2Tab !== S.boot.pane2Tab) pane2Default(ev.pane2Tab);
    S.boot.sourceAutosaveMs = ev.sourceAutosaveMs; S.boot.vimKeys = ev.vimKeys; S.boot.diffStyle = ev.diffStyle; S.boot.autoHidePane2 = ev.autoHidePane2; S.boot.pane2Tab = ev.pane2Tab; S.boot.wrapLines = ev.wrapLines; S.boot.vimMode = ev.vimMode;
    fitFocus();
  }
  if (ev.openIn) S.openIn = ev.openIn;
  setVimKeys(ev.vimKeys);
  setVimMode(ev.vimMode);
  setWrap(ev.wrapLines);
  if (SET.open) { load(); checkFiles(); }
};
