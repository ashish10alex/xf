// The page's state and everything that changes it: the server's answers
// and events, and every key, as the TUI handles them (internal/ui/app.go).
// The state is one mutable object; changing it and calling redraw() draws
// the page again.

import * as vim from './vim.js';
import { diagSetup, drew, drawFailed, note, requestStart } from './diag.js';
import { T, applyTheme, themeHooks, openThemePicker, themeName } from './theme.js';
import { part, setWorkspaceRoot, saveWorkspace, workspaceOf, restoreParts, unsavedNow } from './workspace.js';
import { baseKey, branchesKey, closeBase, onBase, onBranch, openBase, openBranches, readBranch } from './branches.js';
import { answerProd, askDefer, changedKey, checkKey, deferAskKey, deferOn, onProgress, openChanged, openCost, openImpact, openTags, recheckShowing, removeProxies, requestChanged, runTags, scrollOverlay, toggleDefer, toggleTag } from './checks.js';
import { errorsTabKey, errorsTabShown, nextSQLTab, pickSQLTab, readErrors, showErrorsTab, sqlTabOn } from './errors-tab.js';
import { clearHistory, closeWatch, hasTabs, jobsTableKey, logKey, nextTab, openKept, openURL, openWatch } from './log-tabs.js';
import { askRemoveWorktreeHere, cycleProject, nextOpenProject, openByName, pickerKey, readOpenWorktrees, setPickerQuery, stopFind, switchProject } from './picker.js';
import { columnsKey, searchSQLFromPalette, sqlKey } from './sql-pane.js';
import { unsavedIn, noteVimMode } from './vim-mode.js';

// hooks are what modules that import this one (scratch.js) fill in, for the
// keys and commands here to call.
export const hooks = {};

export const S = {
  mode: 'loading', // loading, picker, project, gone (xf web quit)
  picker: [], pickerCursor: 0, pickerQuery: '', pickerTotal: 0,
  // paths are the picker's rows while its query is a path (~/… or /…),
  // which opens a project xf has not opened before; null otherwise.
  paths: null,
  // find is the picker's folder search (picker.js), null when none shows;
  // canFind says this xf web can add the projects it finds.
  find: null, canFind: false,
  // pickerNew are the roots the picker just added, marked new until its
  // query changes.
  pickerNew: null,
  boot: null, caps: {}, state: null,
  sel: null,        // the selected action's ID
  anchor: null,     // the action whose siblings the Actions pane lists
  view: null,       // the selected action as the server sees it
  // plainFile is a file the source pane shows that is no action's (from
  // the file tree, or the editor followed into it): nothing is selected
  // then, and lastPane is the Actions pane's list it keeps showing,
  // {pane, anchor, file}.
  plainFile: '', lastPane: null,
  recent: [],
  // commandUses are how often the palette's commands were done, by title
  // ({count, last}). It lists the last used first; the TUI lists the most
  // used first.
  commandUses: {},
  back: [],         // the actions backspace goes back to
  focus: 'source',  // source, tree, sql, actions, lineage, run, log
  zoomed: false,
  rightHidden: false, // pane 2 hidden (⌥⌘b), kept per project
  // rightPeek is the file with no action that pane 2 shows beside anyway
  // (⌥⌘b), though autoHidePane2 hides it.
  rightPeek: '',
  // errHidden says ⌥⌘b hid pane 2 while the compile fails, which shows it
  // otherwise: its errors tab is where the errors are.
  errHidden: false,
  lineageOpen: false,
  lineageCursor: 0,
  showLog: false,
  incremental: true,
  cols: { shown: false, name: '', type: '', typing: false, cursor: 0, of: null },
  flags: { deps: false, dependents: false, full: false, api: false },
  following: false,
  lastEditor: '', pendingEditor: '',
  flash: '',
  run: null,        // the server's run, with its whole log
  logFollow: true,
  logTab: 'log',    // log, jobs, history
  jobsCursor: 0, historyCursor: 0,
  // The jobs table's search and sort: / types the query, S or a column's
  // header sorts (col '' is the run's own order).
  jobsQuery: '', jobsTyping: false, jobsSort: { col: '', desc: false },
  history: { rows: [], last: null },
  historyOpen: null, historyJobs: null,
  overlay: null,    // palette, help, errors, changed, tags, prod, confirmFull, confirmQuit, confirmClear, confirmRemoveWorktree
  // checks is the Checks page's tab while the page shows, impact or cost:
  // I and C fill the window, as settings does (ADR 0010).
  checks: null,
  // inspector is the dependency inspector's state (inspector.js) while
  // its page shows (ADR 0033), else null.
  inspector: null,
  palette: { query: '', items: [], found: 0, total: 0, badKind: '', cursor: 0, commands: [], symbols: null, seq: 0 },
  help: { query: '', typing: false, cur: 0 },
  errors: null,
  // errTab is set while pane 2's errors tab is picked over sql and
  // columns; it shows while the compile fails. errCursor is its row and
  // errOpen its open groups, by diagnose kind.
  errTab: false, errCursor: 0, errOpen: {},
  changed: null, tags: null, impact: null, cost: null, prod: null, watch: null, confirm: null,
  buf: vim.newBuffer(),
  sqlSearch: { typing: false, query: '', from: null, prev: null, prevHl: false },
  copiedRel: null,
  connected: false,
  // lost is set while xf web can't be reached, which covers the page
  // (lostShown): 'stopped' once it said it was stopping (ctrl+c in its
  // terminal), 'lost' once the stream dropped twice without a word.
  lost: '',
  // reopening is set while the project opens again with new settings (reopen.go).
  reopening: false,
  compileStarted: 0,
  // git is the line under the toggles while api is on, as /api/git words
  // it: { run, check }, null until read.
  git: null,
  // branch is the status line's branch, as /api/branch words it: its
  // parts, none outside a repository; null until read.
  branch: null,
  // branches is the B switcher's state while it is open.
  branches: null,
  // pickerBranches are the picker's projects' branches by root, as read.
  pickerBranches: null,
  // removeWorktree is the worktree confirmRemoveWorktree asks about.
  removeWorktree: null,
  // pickerWorktrees are, by a project's root, its repository's worktrees
  // xf hasn't opened yet (ADR 0032), as read.
  pickerWorktrees: null,
  // openWorktrees are, by an open project's root, its repository's
  // worktrees that aren't open, for the column's projects tab, as read.
  openWorktrees: null,
  // pickerAuth are the sign-in problems the picker warns of
  // (gcpauth.Alerts), null until the server has looked.
  pickerAuth: null,
  // key is the project this tab shows, as its URL names it (?project=),
  // '' on the projects; every request names it (ADR 0008).
  key: '',
  // openIn is where a project picked over this one opens: same-tab or
  // new-tab (webOpenProjectIn).
  openIn: 'same-tab',
  // pickerConfirm is the open project del asks to close: { key, name,
  // unsaved }. quitUnsaved are the files other tabs have unsaved changes
  // in, which q's confirm names.
  pickerConfirm: null, quitUnsaved: [], quitTerm: '',
  // open sums up the projects xf web has open, the last shown first
  // (projectSummary): the projects list's badges and the status line's
  // count.
  open: [],
};

let drawFn = () => {};
export function onRedraw(f) { drawFn = f; }
let pending = false;
export function redraw() {
  if (pending) return;
  pending = true;
  queueMicrotask(() => {
    pending = false;
    if (holding && !firstReady()) return;
    holding = false;
    try {
      watchAPI(); drawFn(); returnAfterLayer();
    } catch (err) {
      drawFailed(err); // noted, with a banner: the page stays as it was
      return;
    }
    drew();
  });
}

// ---- the first draw ----

// A project page's first draw waits until what its panes show first has
// come: the selected action's view and file, the file tree's list and the
// fonts. Until a page draws, the browser keeps the page it came from on
// screen (paint holding), so a switch from another project goes from that
// one to this one whole, without a frame of empty panes. It waits holdMs
// at most.
const holdMs = 400;
let holding = false, fontsIn = false;
function holdFirstDraw() {
  holding = true;
  setTimeout(() => { holding = false; redraw(); }, holdMs);
  // The fonts load only once text uses them, after the draw: asked for
  // now, they come with the rest.
  Promise.all(['"Martian Mono"', '"Fragment Mono"'].map((f) => document.fonts.load('12px ' + f)))
    .catch(() => {}).then(() => { fontsIn = true; redraw(); });
}
function firstReady() {
  if (!fontsIn) return false;
  if (S.mode !== 'project' || S.overlay) return true;
  if (S.sel && !(S.view && S.view.id === S.sel)) return false;
  return !(hooks.sourceLoading && hooks.sourceLoading()) && !(hooks.treeLoading && hooks.treeLoading());
}

// keysLayer names what has the keys over the project's panes: an overlay,
// or the Scratch, Settings or Checks page; '' while the panes have them.
const keysLayer = () => S.overlay || (S.checks ? 'checks' : '') || (S.inspector ? 'inspector' : '')
  || (hooks.scratchOpen && hooks.scratchOpen() ? 'scratch' : '')
  || (hooks.settingsOpen && hooks.settingsOpen() ? 'settings' : '');
let layerWas = '', focusWas = '';

// returnAfterLayer gives the keys to the source editor, in vim's normal
// mode, when an overlay or page closes, whether what was picked there ran
// or not: the focus return rule. What ran there may have given them to the
// file tree, Search or the log, which keep them. The editor may have just
// been drawn again, so it gets them after this draw.
function returnAfterLayer() {
  if (S.mode !== 'project') { layerWas = ''; return; }
  const layer = keysLayer();
  if (layer && !layerWas) focusWas = S.focus;
  const closed = layerWas && !layer;
  layerWas = layer;
  if (!closed || !sourceShown()) return;
  if (S.focus !== focusWas && S.focus !== 'source' && !rightPanes.includes(S.focus)) return;
  setTimeout(() => { if (!keysLayer() && sourceShown() && hooks.sourceReturn) hooks.sourceReturn(); }, 0);
}

// ---- what an API run runs ----

// The checkout is read again while api is on: when it turns on, whichever
// key or click did it; when a compile starts (a file changed); when the
// page gets focus back (a commit or push elsewhere); and when a run starts.
let apiWas = false, gitSeq = 0;
function watchAPI() {
  if (S.flags.api && !apiWas) readGit();
  apiWas = S.flags.api;
}

function readGit() {
  if (!S.flags.api || !S.caps.api || S.mode !== 'project') return;
  const seq = ++gitSeq;
  get('/api/git').then((v) => { if (seq === gitSeq) { S.git = v; redraw(); } }).catch((e) => {
    if (seq !== gitSeq) return;
    const line = [{ text: e.message, tone: 'error' }];
    S.git = { run: line, check: line };
    redraw();
  });
}
window.addEventListener('focus', readGit);
document.addEventListener('visibilitychange', () => { if (!document.hidden) readGit(); });

// ---- talking to xf web ----

async function call(method, path, body, key) {
  const ended = requestStart(method, path);
  try {
    const data = await request(method, path, body, key);
    ended();
    return data;
  } catch (err) {
    ended(err);
    throw err;
  }
}

async function request(method, path, body, key = S.key) {
  const headers = body ? { 'Content-Type': 'application/json' } : {};
  if (key) headers['X-Xf-Project'] = key;
  const res = await fetch(path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text.trim() }; }
  if (!res.ok) {
    const err = new Error((data && data.error) || text.trim() || res.statusText);
    err.kind = data && data.kind;
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}
// getLines asks for path's lines of JSON and gives each to each as it
// comes; it ends after the last. With body, it posts body.
export async function getLines(path, each, signal, body) {
  const headers = body ? { 'Content-Type': 'application/json' } : {};
  if (S.key) headers['X-Xf-Project'] = S.key;
  const res = await fetch(path, { method: body ? 'POST' : 'GET', headers, body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin', signal });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (value) buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line) each(JSON.parse(line));
    }
    if (done) break;
  }
  if (buf.trim()) each(JSON.parse(buf));
}

export const get = (path) => call('GET', path);
export const post = (path, body) => call('POST', path, body || {});
// getAs and postAs ask of the open project key rather than this tab's,
// as the projects tab's menu does of another project (ADR 0032).
export const getAs = (key, path) => call('GET', path, undefined, key);
export const postAs = (key, path, body) => call('POST', path, body || {}, key);

// say puts msg in the footer. It goes after sayMs, or at the next key, so
// a notice about something done by a click (opening a page, copying) does
// not stay. Warnings and errors set S.flash themselves and stay until a key.
export const sayMs = 5000;
export function say(msg) { S.flash = msg || ''; fadeFlash(S.flash); redraw(); }

// fader returns a function that, given the msg just said, clears it after
// sayMs if shown() is still msg.
export function fader(shown, clear) {
  let t = 0;
  return (msg) => {
    clearTimeout(t);
    if (!msg) return;
    t = setTimeout(() => { if (shown() === msg) { clear(); redraw(); } }, sayMs);
    t.unref?.(); // in node's tests, a pending fade doesn't hold the run open
  };
}
const fadeFlash = fader(() => S.flash, () => { S.flash = ''; });
themeHooks({ post, redraw, say, inProject: () => S.mode === 'project' });

// ---- starting ----

// projectURL is the page of the project key names.
export const projectURL = (key) => '/?project=' + encodeURIComponent(key).replace(/%2F/g, '/').replace(/%7E/g, '~');

// bootPath asks for the project key names, which opens it.
const bootPath = (key) => '/api/boot' + (key ? '?project=' + encodeURIComponent(key) : '');

// ---- moving between projects (ADR 0008) ----

// Each tab keeps the projects it showed, the last first, so a closed
// project's tab goes back to the one before; and a word to say on the
// page it goes to.
const MRU_KEY = 'xf-web-shown', CARRY_KEY = 'xf-web-carry';
export function shownOrder() {
  try { const l = JSON.parse(sessionStorage.getItem(MRU_KEY) || '[]'); return Array.isArray(l) ? l : []; } catch { return []; }
}
function setShown(l) {
  try { sessionStorage.setItem(MRU_KEY, JSON.stringify(l.slice(0, 30))); } catch { /* this load only */ }
}
const noteShown = (key) => setShown([key, ...shownOrder().filter((k) => k !== key)]);

// carried is the word the page before left for this one, once.
function carried() {
  try { const v = sessionStorage.getItem(CARRY_KEY) || ''; sessionStorage.removeItem(CARRY_KEY); return v; } catch { return ''; }
}

// windowWord is this browser window as the windows list numbers it.
function windowWord() {
  const n = hooks.windowsOn ? hooks.windowsOn('').self : 0;
  return n ? 'Window ' + n : '';
}

// leaving is set once this tab is on its way to another page, so the
// closed project's last event doesn't send it twice.
let leaving = false;

// goAfterClose sends this tab, whose project closed, to the project it
// showed before that is still open, else the one any tab showed last,
// else the projects; flash is said there.
function goAfterClose(closedKey, open, flash) {
  leaving = true;
  closeEvents();
  const still = (open || []).filter((k) => k !== closedKey);
  const order = shownOrder().filter((k) => k !== closedKey);
  setShown(order);
  const to = order.find((k) => still.includes(k)) || still[0] || '';
  try { sessionStorage.setItem(CARRY_KEY, flash); } catch { /* said nowhere */ }
  S.switching = true;
  location.href = to ? projectURL(to) : '/';
}

// goToProject shows the project key names in this tab. Unsaved text in
// the source pane is saved first when autosave is on; otherwise the
// workspace keeps it, and the page goes without asking.
export async function goToProject(key) {
  if (hooks.sourceFlush) await hooks.sourceFlush();
  if (hooks.scratchFlush) await hooks.scratchFlush();
  S.switching = true;
  location.href = projectURL(key);
}

// ---- the workspace (workspace.js) ----

// tabID names this tab to xf web, for the unsaved files it reports. It
// is kept in window.name, which lasts while the tab goes from page to
// page and isn't copied to a tab it opens, as sessionStorage would be.
// newTab is whether this is the tab's first xf web page.
export let newTab = false;
const tabID = (() => {
  if (/^xf-tab-\w+$/.test(window.name)) return window.name;
  newTab = true;
  const id = 'xf-tab-' + Math.random().toString(36).slice(2) + Date.now().toString(36);
  try { window.name = id; } catch { /* this page only */ }
  return id;
})();

// The page log (diag.js) names this tab and project, and with each key
// and error what has the keys.
diagSetup({
  tab: tabID,
  project: () => S.key,
  state: () => [
    S.mode,
    S.overlay && 'overlay=' + S.overlay,
    S.checks && 'checks=' + S.checks,
    S.mode === 'project' && 'focus=' + S.focus,
    hooks.settingsOpen && hooks.settingsOpen() && 'settings',
    hooks.scratchOpen && hooks.scratchOpen() && 'scratch',
    T.pick && 'themes',
    S.lost && 'lost=' + S.lost,
    S.flash && 'flash=' + JSON.stringify(String(S.flash).slice(0, 60)),
  ].filter(Boolean).join(' '),
});
note(`page loaded ${location.pathname}${location.search} · ${navigator.userAgent.replace(/^.*\) /, '')}`);

// viewPart is what the page shows of the project: the selection, the
// panes and their places, and the run's flags.
// movedIDs are the actions a reopen gave new IDs, old to new, saved
// under the new.
let movedIDs = {};
const moved = (id) => (id && movedIDs[id]) || id;
part('view', () => {
  if (S.mode !== 'project') return null;
  return {
    sel: moved(S.sel), anchor: moved(S.anchor), plainFile: S.plainFile, lastPane: S.lastPane, back: S.back.slice(-50).map(moved),
    focus: S.focus, zoomed: S.zoomed, lineageOpen: S.lineageOpen, lineageCursor: S.lineageCursor,
    showLog: S.showLog, logTab: S.logTab, jobsCursor: S.jobsCursor, jobsSort: S.jobsSort, historyCursor: S.historyCursor,
    cols: { shown: S.cols.shown, name: S.cols.name, type: S.cols.type, cursor: S.cols.cursor },
    flags: { ...S.flags }, incremental: S.incremental, errTab: S.errTab, errCursor: S.errCursor, errOpen: S.errOpen,
  };
});

// The dependency inspector's page (inspector.js, not in the first load)
// is kept as it was left, open or not, and opened again if it was.
let inspectorKept = null;
part('inspector', (light) => (hooks.inspectorSave ? hooks.inspectorSave(light) : inspectorKept), (v) => {
  inspectorKept = v;
  if (v && v.model && S.caps.inspector) import('./inspector.js').then((m) => m.restoreInspector(v));
});

// restoreView puts back what a page starts from; the selection is made
// once the compile has a graph (takeRestoring).
function restoreView(v) {
  S.restoring = v;
  S.lineageOpen = !!v.lineageOpen;
  S.showLog = S.showLog || !!v.showLog;
  if (['log', 'jobs', 'history'].includes(v.logTab)) S.logTab = v.logTab;
  S.jobsCursor = v.jobsCursor || 0;
  if (v.jobsSort && typeof v.jobsSort.col === 'string') S.jobsSort = { col: v.jobsSort.col, desc: !!v.jobsSort.desc };
  S.historyCursor = v.historyCursor || 0;
  if (v.cols) S.cols = { ...S.cols, ...v.cols, typing: false };
  if (v.flags) S.flags = { ...S.flags, ...v.flags };
  if (typeof v.incremental === 'boolean') S.incremental = v.incremental;
  S.errTab = !!v.errTab;
  S.errCursor = v.errCursor || 0;
  S.errOpen = v.errOpen || {};
}

// takeRestoring selects what the workspace had, once there is a graph,
// and reports whether it did.
function takeRestoring(st) {
  const v = S.restoring;
  S.restoring = null;
  if (v.sel) {
    S.anchor = v.anchor || v.sel;
    selectAction(v.sel, { fallback: st.focus, keepAnchor: true });
    S.lineageCursor = v.lineageCursor || 0;
  } else if (v.plainFile) {
    showPlainFile(v.plainFile);
    S.lastPane = v.lastPane || null;
  } else return false;
  S.back = Array.isArray(v.back) ? v.back : [];
  return true;
}

// restoreFocus gives back the pane that had the keys, and the zoom.
function restoreFocus(v) {
  const panes = ['source', 'tree', 'sql', 'actions', 'lineage', 'run', 'defer', 'log'];
  if (!panes.includes(v.focus) || v.focus === S.focus) { S.zoomed = !!v.zoomed; return; }
  if (v.focus === 'source' && !sourceShown()) return;
  if (v.focus === 'log' && !S.showLog) return;
  if (v.focus === 'run' && !S.caps.run) return;
  if (v.focus === 'defer' && !(S.view && S.view.defer)) return;
  if (rightPanes.includes(v.focus) && !rightShown()) return;
  if (v.focus === 'tree') { if (hooks.treeShown && hooks.treeShown()) S.focus = 'tree'; return; }
  setFocus(v.focus);
  if (S.focus === v.focus) S.zoomed = !!v.zoomed;
}

// The workspace is kept as the page goes, a switch or a reload; not
// once xf web has quit.
window.addEventListener('pagehide', () => { if (S.mode === 'project') saveWorkspace(); });

// The tab tells xf web which files it has unsaved changes in, its own
// project's and those its workspaces keep, whenever that changes.
let unsavedSent = '';
function reportUnsaved() {
  if (S.mode !== 'project' && S.mode !== 'picker') return;
  const live = S.mode === 'project' && hooks.liveUnsaved ? hooks.liveUnsaved() : [];
  const now = JSON.stringify(unsavedNow(live));
  if (now === unsavedSent) return;
  post('/api/unsaved', { tab: tabID, unsaved: JSON.parse(now) }).then(() => { unsavedSent = now; }).catch(() => {});
}
setInterval(reportUnsaved, 1000);

export async function start() {
  const want = new URLSearchParams(location.search).get('project') || '';
  const said = carried();
  try {
    const b = await get(bootPath(want));
    applyTheme(b.theme, b.themes, b.themeOwn);
    S.openIn = b.openIn || 'same-tab';
    S.canFind = !!b.find;
    S.open = b.open || [];
    if (b.project) {
      holdFirstDraw();
      S.key = b.key;
      if (b.key !== want) history.replaceState(null, '', projectURL(b.key));
      noteShown(b.key);
      setWorkspaceRoot(b.project.root);
      const ws = workspaceOf(b.project.root);
      startProject(b.project, ws && ws.parts.view);
      restoreParts(ws, ['view']);
      if (said) S.flash = said;
    } else {
      // A project that didn't open (forgotten, moved) leaves the
      // projects, saying why.
      if (want) history.replaceState(null, '', '/');
      S.flash = b.missing || said;
      S.mode = 'picker';
      S.picker = b.picker || [];
      arrangePicker(false);
      S.pickerTotal = b.total ?? S.picker.length;
      // Each project's branch, read at once on the server; "…" until then.
      get('/api/picker/branches').then((m) => { S.pickerBranches = m; redraw(); }).catch(() => { S.pickerBranches = {}; redraw(); });
      readPickerWorktrees();
      checkPickerAuth();
    }
  } catch (e) {
    S.mode = 'error';
    S.flash = e.message;
  }
  redraw();
  connect();
}

// checkPickerAuth asks the server, after the picker shows, whether gcloud
// or xf's own Google sign-in needs attention: it runs gcloud, which takes
// a second or two. With a warning up, coming back to the page (from
// running gcloud auth login, say) asks again.
let authSeq = 0;
export function checkPickerAuth() {
  const seq = ++authSeq;
  get('/api/picker/auth').then((a) => { if (seq === authSeq) { S.pickerAuth = a; redraw(); } }).catch(() => {});
}
window.addEventListener('focus', () => {
  // Back from a terminal that may have logged in again: xf web looks for
  // a new Google login, and on one every page retries its failed dry runs
  // and says so (credentials.go).
  if (S.mode === 'project') post('/api/credentials').catch(() => {});
  if (S.mode === 'picker' && S.pickerAuth && S.pickerAuth.length) checkPickerAuth();
  if (hooks.settingsOpen && hooks.settingsOpen() && hooks.refreshSettingsInfo) hooks.refreshSettingsInfo();
});

// startProject starts the page on project p, and with ws the workspace
// view this tab kept for it (see viewPart).
function startProject(p, ws) {
  S.mode = 'project';
  S.boot = p;
  if (hooks.vimKeys) hooks.vimKeys(p.vimKeys);
  noteVimMode(p.vimMode);
  if (hooks.vimMode) hooks.vimMode(p.vimMode);
  if (hooks.wrapLines) hooks.wrapLines(p.wrapLines);
  S.caps = p.caps;
  S.flags.api = p.caps.apiDefault;
  S.following = p.caps.follow;
  S.recent = p.resume.recent || [];
  S.commandUses = p.commandUses || {};
  S.flash = p.notice || '';
  S.history = p.history;
  S.run = p.run;
  S.showLog = !!(p.run && p.run.seq);
  S.lastEditor = p.editorFile.file || '';
  S.editorActions = p.editorFile.actions || [];
  document.title = 'xf · ' + p.name;
  S.rightHidden = readRightHidden();
  S.errHidden = false;
  if (!rightShown() && rightPanes.includes(S.focus)) S.focus = 'source';
  S.checks = null; S.impact = null; S.cost = null; // another project's checks
  if (S.inspector && hooks.closeInspector) hooks.closeInspector();
  if (ws) restoreView(ws);
  else pane2Default(p.pane2Tab);
  setState(p.state, true);
  if (sourceShown()) setFocus('source');
  else S.focus = 'sql';
  if (ws) restoreFocus(ws);
  readBranch();
  if (hooks.forgeStart) hooks.forgeStart();
  if (p.scratch && (p.scratch.start || p.scratch.standalone) && hooks.startScratch) hooks.startScratch(p.scratch.standalone);
  if (p.check === 'impact') openImpact(); // xf impact --web
}

// The events come over a WebSocket (ADR 0011), each {event, data}: an
// event source would hold one of the browser's six connections to the
// server for as long as the tab is open, shared by every tab. A socket
// that drops is opened again after a second, then less often; one this
// page closed (another project, the project closed, xf web quit) is not.
// A second drop without a hello between them covers the page (S.lost):
// one drop alone is often a laptop waking, back within the second.
let events = null;
let eventsFirst = true; // no hello yet since connect
let retryMs = 1000, retryTimer = 0, drops = 0;
function connect() {
  eventsFirst = true;
  retryMs = 1000;
  drops = 0;
  openEvents();
}

// lostShown is whether the page is covered because xf web can't be
// reached: its keys and clicks would only fail.
export const lostShown = () => !!S.lost && (S.mode === 'project' || S.mode === 'picker');

// unsavedHere is every file this tab has unsaved changes in, which go
// with it if it is closed while xf web can't save them.
export function unsavedHere() {
  const live = S.mode === 'project' && hooks.liveUnsaved ? hooks.liveUnsaved() : [];
  return Object.values(unsavedNow(live)).flat();
}

function openEvents() {
  closeEvents();
  const q = new URLSearchParams({ tab: tabID });
  if (S.key) q.set('project', S.key);
  const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/events?' + q);
  events = ws;
  const handlers = {};
  const on = (name, f) => { handlers[name] = (data) => { f(data); redraw(); }; };
  ws.onmessage = (m) => {
    if (ws !== events) return;
    let msg;
    try { msg = JSON.parse(m.data); } catch { return; }
    const f = handlers[msg.event];
    if (f) f(msg.data);
  };
  ws.onclose = () => {
    if (ws !== events) return; // closed here
    events = null;
    S.connected = false;
    if (++drops >= 2 && !S.lost) S.lost = 'lost';
    note(`events dropped (${drops} in a row)${S.lost ? ' · page covered: ' + S.lost : ''}`);
    redraw();
    retryTimer = setTimeout(openEvents, retryMs);
    retryMs = Math.min(retryMs * 2, 10000);
  };
  on('hello', () => {
    if (!eventsFirst || drops) note('events back');
    retryMs = 1000;
    drops = 0;
    S.connected = true;
    S.lost = '';
    if (!eventsFirst) unsavedSent = ''; // xf web may have started again
    if (!eventsFirst && S.mode === 'project') resync();
    eventsFirst = false;
  });
  on('state', (st) => S.mode === 'project' && setState(st, false));
  on('detail', () => {
    if (S.mode !== 'project') return;
    refreshView();
    if (hooks.sourceDetail) hooks.sourceDetail();
    if (hooks.inspectorDetail) hooks.inspectorDetail();
  });
  on('inspector', (ev) => S.mode === 'project' && hooks.inspectorEvent && hooks.inspectorEvent(ev));
  on('credentials', (m) => S.mode === 'project' && say(m.text));
  on('run', onRun);
  on('log', onLog);
  on('history', (h) => { S.history = h; if (S.historyCursor >= h.rows.length) S.historyCursor = Math.max(h.rows.length - 1, 0); });
  on('editor', onEditor);
  on('files', () => hooks.filesChanged && hooks.filesChanged());
  on('scm', (ev) => hooks.scmEvent && hooks.scmEvent(ev));
  on('forge', (v) => S.mode === 'project' && hooks.forgeEvent && hooks.forgeEvent(v));
  on('python', (ev) => hooks.pythonEvent && hooks.pythonEvent(ev));
  on('progress', onProgress);
  on('branch', (v) => { if (S.mode === 'project') onBranch(v.parts); });
  on('base', (v) => S.mode === 'project' && onBase(v.defaultBranch));
  // The user's theme is not a project page's when its file sets its own,
  // which its own events tell.
  on('theme', (t) => { if (!(t.global && S.mode === 'project')) applyTheme(t, null, t.own); });
  on('settings', (ev) => { if (hooks.settingsChanged) hooks.settingsChanged(ev); if (hooks.targetSettings) hooks.targetSettings(); });
  // A reopen (reopen.go) compiles the project again beside the one on
  // show, which stays drawn; once it has, the page loads again on it. A
  // replace, not a reload, so the view transition (index.html) keeps this
  // page on screen until the new one has drawn: a reload gets none.
  // The reopen may have changed the actions' IDs (a prefix is in them):
  // this tab's view is saved under the new ones, so the new page draws
  // the same action at once. Unsaved text goes with the workspace, as on
  // a switch to another project.
  on('reopening', (b) => { S.reopening = !!b; });
  on('reload', (r) => {
    movedIDs = (r && r.moved) || {};
    S.switching = true;
    location.replace(location.href);
  });
  on('projects', (l) => { S.open = l || []; if (S.mode === 'picker' || S.overlay === 'projects') arrangePicker(true); });
  // The project closed (q or del in another tab, or this one): the
  // stream ends, and must not reconnect, which would open it again.
  on('closed', (ev) => {
    closeEvents();
    if (!leaving) goAfterClose(ev.key, ev.open, (S.boot ? S.boot.name : ev.key) + ' was closed' + (ev.by ? ' in ' + ev.by : '') + (ev.why ? ': ' + ev.why : ''));
  });
  on('quit', () => { S.mode = 'gone'; closeEvents(); });
  // xf web is stopping (ctrl+c in its terminal): the stream ends next,
  // and is opened again in case it comes back on the same port.
  on('stopping', () => { S.lost = 'stopped'; });
}

// closeEvents closes the events' socket for good: it is not opened again.
function closeEvents() {
  clearTimeout(retryTimer);
  const ws = events;
  events = null;
  if (ws) ws.close();
}

// resync asks again for what may have changed while the stream was down.
async function resync() {
  try {
    const b = await get(bootPath(S.key));
    applyTheme(b.theme, b.themes, b.themeOwn);
    if (!b.project) return;
    S.run = b.project.run;
    S.history = b.project.history;
    setState(b.project.state, false);
    readBranch();
    if (hooks.forgeStart) hooks.forgeStart();
    redraw();
  } catch { /* the stream retries */ }
}

// ---- the engine's state ----

// setState takes the engine's state, from the boot or a state event.
export function setState(st, first) {
  const prev = S.state;
  S.state = st;
  if (st.compiling && (!prev || !prev.compiling)) {
    S.compileStarted = Date.now();
    if (prev) { readGit(); readBranch(); }
  }
  if (st.watchErr && !S.watchNoticed) {
    S.watchNoticed = true;
    S.flash = 'file watching is off (' + st.watchErr + '); press ' + saveKey + ' to recompile';
  }
  if (st.notice && (!prev || prev.notice !== st.notice)) S.flash = st.notice;
  if (prev && st.impactSig !== prev.impactSig) recheckShowing();
  const hadErrors = prev ? prev.errors > 0 : false;
  const hadGraph = prev ? prev.hasGraph : false;
  // A failing compile's errors show in pane 2's errors tab, beside the
  // keys rather than over them; they are read again after each compile.
  if (st.errors > 0 && (!hadErrors || st.compiles !== prev.compiles)) readErrors(!hadErrors);
  if (st.errors > 0 && !hadErrors) S.errHidden = false;
  if (hadErrors && st.errors === 0) {
    S.errTab = false;
    if (S.overlay === 'errors') S.overlay = null;
    fitFocus();
  }
  // Opening e asks again for old failed dry runs: read their answers.
  if (S.overlay === 'errors' && !st.errors && prev && st.dryRunErrors !== prev.dryRunErrors) refreshErrorsOverlay();
  if (!prev || st.compiles !== prev.compiles) {
    if (S.sel) refreshView();
    else if (prev && S.plainFile) adoptPlainFile();
    if (hooks.targetCompiled) hooks.targetCompiled();
    if (prev && hooks.sourceCompiled) hooks.sourceCompiled();
    if (prev && hooks.filesChanged) hooks.filesChanged();
  }
  if (st.hasGraph && (first || !hadGraph) && !S.sel) {
    if (S.restoring && takeRestoring(st)) return;
    const resume = S.boot.resume.selected;
    if (resume) { selectAction(resume, { fallback: st.focus }); return; }
    if (st.focus) { selectAction(st.focus); return; }
    if (S.following && S.pendingEditor) { showEditorFile(S.pendingEditor); return; }
    if (S.overlay !== 'errors' && !scratchShowing()) openPalette();
  }
  if (st.hasGraph && S.following && S.pendingEditor) showEditorFile(S.pendingEditor);
}

export const hasErrors = () => S.state && S.state.errors > 0;

// ---- the selection ----

let viewSeq = 0;

// picked is the file an action was picked by (the tree, the palette's
// files), which may have listed it before a compile renamed it.
let picked = { id: '', file: '' };

// selectAction selects id, its file's actions in the Actions pane. With
// keepAnchor the pane stays on the actions it lists; opts.file is the
// file id was picked by.
export function selectAction(id, opts = {}) {
  if (!id) return;
  // The source pane's unsaved changes keep its file.
  if (hooks.sourceBlocks && hooks.sourceBlocks(id, opts)) return;
  picked = { id, file: opts.file || '' };
  S.plainFile = '';
  S.lastPane = null;
  S.rightPeek = '';
  const changedAction = S.sel !== id;
  S.sel = id;
  if (!opts.keepAnchor) S.anchor = id;
  S.recent = [id, ...S.recent.filter((x) => x !== id)].slice(0, 50);
  if (changedAction) {
    S.lineageCursor = 0;
  }
  saveSession();
  fetchView(changedAction, opts.fallback);
  redraw();
}

// showPlainFile shows file, which has no action, in the source pane, and
// selects nothing; '' shows no file. backspace goes back to the action
// selected before.
export function showPlainFile(file) {
  if (S.view) S.lastPane = { pane: S.view.pane, anchor: S.view.anchor, file: S.view.file };
  if (S.sel) S.back.push(S.sel);
  if (S.plainFile !== file) S.rightPeek = '';
  S.plainFile = file;
  S.sel = null;
  S.view = null;
  viewSeq++; // a view on its way is the old action's
  vim.setText(S.buf, [], true);
  fitFocus();
  if (hooks.sourceView) hooks.sourceView();
  redraw();
}

// adoptPlainFile selects the action of the plain file shown, once a
// compile gives it one: a file opened while the compile that defines it
// ran (just after a branch switch), a new action's file, or one just
// renamed. Unsaved changes keep the file as it is.
async function adoptPlainFile() {
  const file = S.plainFile;
  if (hooks.sourceHolds && hooks.sourceHolds()) return;
  try {
    const r = await get('/api/files/action?file=' + encodeURIComponent(file));
    if (r.action && S.plainFile === file && !S.sel) selectAction(r.action);
  } catch { /* the next compile asks again */ }
}

let sessionTimer = null;
function saveSession() {
  if (!S.sel) return;
  clearTimeout(sessionTimer);
  sessionTimer = setTimeout(() => post('/api/session', { selected: S.sel, recent: S.recent }).catch(() => {}), 400);
}

function viewQuery() {
  const f = S.flags;
  const q = new URLSearchParams({
    id: S.sel, anchor: S.anchor || S.sel, incremental: S.incremental ? '1' : '0',
    deps: f.deps, dependents: f.dependents, full: f.full, api: f.api,
  });
  const file = selFile(S.sel);
  if (file) q.set('file', file);
  return '/api/action?' + q;
}

// selFile is the file action id was last seen in: the view's, or its
// buffer's. Should an edit to that file's config rename the action, the
// server answers with the action the file defines now.
function selFile(id) {
  if (S.view && S.view.id === id && S.view.file) return S.view.file;
  if (picked.id === id && picked.file) return picked.file;
  return (hooks.bufferFile && hooks.bufferFile(id)) || '';
}

// renamed puts action now in place of id, which an edit renamed, as the
// selection and in the lists that name it.
function renamed(id, now) {
  if (S.sel === id) S.sel = now;
  const swap = (x) => (x === id ? now : x);
  S.recent = [...new Set(S.recent.map(swap))];
  S.back = S.back.map(swap);
  saveSession();
}

let refreshTimer = null;
// refreshView fetches the selected action again, soon: dry runs and table
// lookups arrive in bursts.
export function refreshView() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => fetchView(false), 80);
}

async function fetchView(reset, fallback) {
  if (!S.sel) return;
  const seq = ++viewSeq;
  const id = S.sel;
  try {
    const v = await get(viewQuery());
    if (seq !== viewSeq) return;
    // The same action under a new name keeps its place in the panes.
    const sameAction = S.view && (S.view.id === v.id || (S.view.id === id && S.view.file === v.file));
    if (v.id !== id) renamed(id, v.id);
    externalTab(S.view, v);
    if (v.external && S.focus === 'run') S.focus = 'actions'; // a declaration has no run pane
    if (!v.defer && S.focus === 'defer') { S.focus = 'sql'; S.zoomed = false; } // defer to prod went off
    S.view = v;
    if (S.anchor !== v.anchor) S.anchor = v.anchor;
    vim.setText(S.buf, v.sql.plain, reset || !sameAction);
    if (hooks.sourceView) hooks.sourceView();
    if (S.cols.of !== v.id) {
      S.cols = { ...S.cols, name: '', type: '', typing: false, cursor: 0, of: v.id };
    }
    const rows = lineageRows();
    if (reset || !sameAction || S.lineageCursor >= rows.length || !rows[S.lineageCursor] || !rows[S.lineageCursor].ref) {
      S.lineageCursor = rows.findIndex((r) => r.ref);
      if (S.lineageCursor < 0) S.lineageCursor = 0;
    }
  } catch (e) {
    if (seq !== viewSeq) return;
    if (e.status === 404) {
      // Gone in a recompile: nothing is selected, as in the TUI.
      if (fallback && fallback !== id) { selectAction(fallback); return; }
      // A file picked whose action is gone still opens, with nothing
      // selected until a compile gives it one.
      const file = picked.id === id && picked.file;
      if (S.sel === id && file) { S.sel = null; showPlainFile(file); return; }
      if (S.sel === id) { S.sel = null; S.view = null; vim.setText(S.buf, [], true); }
    } else if (e.status !== 409) {
      S.flash = e.message;
    }
  }
  redraw();
}

// externalTab opens pane 2 on a declaration's columns, or on a declared
// view's SQL once its table's type is known (ADR 0015), each once per
// action and type, so tab still switches. The tab the other actions had
// comes back when one of them is selected.
export function externalTab(prev, v) {
  const was = !!(prev && prev.external);
  if (!v.external) {
    if (was && S.cols.ownShown != null) { S.cols.shown = S.cols.ownShown; S.cols.ownShown = null; }
    S.cols.autoFor = null;
    return;
  }
  if (!was) S.cols.ownShown = S.cols.shown;
  const key = v.id + ' ' + (v.tableType || '');
  if (S.cols.autoFor === key) return;
  S.cols.autoFor = key;
  S.cols.shown = !/VIEW$/.test(v.tableType || '');
}

// paneActions are the Actions pane's actions.
export const paneActions = () => (S.view ? S.view.pane : S.lastPane ? S.lastPane.pane : []);

export function selectInPane(id) {
  const anchor = S.view ? S.view.anchor : S.lastPane ? S.lastPane.anchor : S.sel;
  selectAction(id, { keepAnchor: true });
  S.anchor = anchor;
}

function moveInActions(dir) {
  if (S.view && S.view.externals && hooks.moveInExternals) { hooks.moveInExternals(dir); return; }
  const acts = paneActions();
  if (!acts.length) return;
  let i = Math.max(acts.findIndex((a) => a.id === S.sel), 0);
  // With nothing selected (a plain file shown), j picks the first and k
  // the last.
  if (!S.sel) i = dir < 0 ? acts.length - 1 : 0;
  else if (dir === -2) i = 0;
  else if (dir === 2) i = acts.length - 1;
  else i = Math.min(Math.max(i + dir, 0), acts.length - 1);
  if (acts[i].id !== S.sel) selectInPane(acts[i].id);
}

function cycleInFile(dir) {
  const acts = S.view && S.view.externals && hooks.externalRows ? hooks.externalRows() : paneActions();
  if (acts.length < 2) { say((S.view && S.view.alone) || 'no other actions in this file'); return; }
  const i = acts.findIndex((a) => a.id === S.sel);
  selectInPane(acts[(i + dir + acts.length) % acts.length].id);
}

// lineageRows are the Lineage pane's rows: a header per section, its
// entries, or a "none".
export function lineageRows() {
  const v = S.view;
  if (!v) return [];
  const rows = [];
  const section = (title, list) => {
    rows.push({ header: `${title} (${list.length})` });
    if (!list.length) rows.push({ none: true });
    list.forEach((ref, i) => rows.push({ ref, last: i === list.length - 1 }));
  };
  section('upstream', v.lineage.up);
  section('downstream', v.lineage.down);
  return rows;
}

const lineageSelected = () => { const r = lineageRows()[S.lineageCursor]; return r && r.ref; };

function moveLineage(dir) {
  const rows = lineageRows();
  for (let i = S.lineageCursor + dir; i >= 0 && i < rows.length; i += dir) {
    if (rows[i].ref) { S.lineageCursor = i; return; }
  }
}

function lineageToEnd(dir) {
  const rows = lineageRows();
  const idx = rows.map((r, i) => (r.ref ? i : -1)).filter((i) => i >= 0);
  if (idx.length) S.lineageCursor = dir < 0 ? idx[0] : idx[idx.length - 1];
}

export function jumpTo(ref) {
  if (!ref) return;
  if (ref.unknown) { say(ref.id + ' is not defined in this project'); return; }
  if (S.sel) S.back.push(S.sel);
  selectAction(ref.id);
}

// goBack selects the action before the last jump (enter in lineage, gd).
export function goBack() {
  const id = S.back.pop();
  if (!id) { say('no earlier action to go back to'); return; }
  selectAction(id, { fallback: null });
}

// ---- the editor ----

function onEditor(ev) {
  S.lastEditor = ev.file;
  S.editorActions = ev.actions || [];
  if (!S.following || !S.state || !S.state.hasGraph || followHeld()) {
    S.pendingEditor = ev.file;
    // With autosave on, the source pane saves and following goes on.
    if (S.following && followHeld() && hooks.sourceFlush) hooks.sourceFlush();
    return;
  }
  showEditorFile(ev.file, ev.actions);
}

// followHeld says the source pane's unsaved changes hold following the
// editor: it goes on once they are saved or discarded.
export const followHeld = () => !!(hooks.sourceHolds && hooks.sourceHolds());

// followWaits says the hold is one to show: autosave saves the changes
// as soon as the editor moves, so with it on following never waits.
export const followWaits = () => followHeld() && !(hooks.sourceAutosaves && hooks.sourceAutosaves());

// followPending shows the file the editor moved to while following was
// held.
export function followPending() {
  if (S.following && S.pendingEditor && S.state && S.state.hasGraph && !followHeld()) showEditorFile(S.pendingEditor);
}

async function showEditorFile(file, actions) {
  S.pendingEditor = '';
  if (!actions) {
    try {
      const b = await get('/api/boot');
      actions = b.project && b.project.editorFile.file === file ? b.project.editorFile.actions : null;
    } catch { actions = null; }
  }
  // A file with no action is shown on its own (ADR 0006).
  if (!actions || !actions.length) { if (S.plainFile !== file) showPlainFile(file); return; }
  if (S.view && S.view.file === file) return; // keep whichever of its actions was picked
  if (S.sel) S.back.push(S.sel);
  selectAction(actions[0]);
}

function toggleFollow() {
  if (!S.caps.follow) { say('not following an editor: open the project in an editor running xf lsp'); return; }
  S.following = !S.following;
  if (!S.following) { say('following the editor paused (F resumes)'); return; }
  say('following the editor');
  if (S.pendingEditor || S.lastEditor) showEditorFile(S.pendingEditor || S.lastEditor);
}

async function openInEditor() {
  if (!S.caps.open) { say('o needs an editor'); return; }
  if (!S.view || !S.view.file) { say('no file to open'); return; }
  try {
    const r = await post('/api/open', { id: S.view.id });
    // A terminal editor can't open from the page: pick another, which
    // then opens the file.
    if (r.kind === 'editor') { openEditorPicker(S.view.id, true); return; }
    say(r.message);
  } catch (e) { say(e.message); }
}

// ---- the editor o opens files with ----

// openEditorPicker lists the editors o could open files with; open is the
// action whose file to open once one is picked, and blocked says o just
// failed with the editor it has.
export async function openEditorPicker(open, blocked) {
  S.zoomed = false;
  S.overlay = 'editor';
  S.editor = { loading: true, open: open || null, blocked: !!blocked, cursor: 0, custom: '', project: false, saving: false, err: '' };
  redraw();
  try {
    Object.assign(S.editor, await get('/api/editor'), { loading: false });
    const rows = editorRows();
    const cur = rows.findIndex((r) => r.command === S.editor.current);
    S.editor.cursor = Math.max(cur, rows.findIndex((r) => r.found), 0);
  } catch (e) { S.overlay = null; say(e.message); }
  redraw();
}

// editorRows are the picker's rows: the editors on PATH, then the ones
// that aren't (shown, not picked), then a command of your own.
export function editorRows() {
  const e = S.editor;
  if (!e || !e.choices) return [];
  const found = e.choices.filter((c) => c.found), missing = e.choices.filter((c) => !c.found);
  return [...found, { custom: true, found: true }, ...missing];
}

export async function pickEditor() {
  const e = S.editor;
  const row = editorRows()[e.cursor];
  if (!row || !row.found || e.saving) return;
  const command = row.custom ? e.custom.trim() : row.command;
  if (!command) { e.err = 'type the command that opens your editor, e.g. code'; redraw(); return; }
  e.saving = true; e.err = '';
  redraw();
  try {
    const r = await post('/api/editor', { command, project: e.project, open: e.open || '' });
    S.overlay = null;
    S.editor = null;
    say(r.message);
  } catch (err) {
    e.saving = false;
    e.err = err.message;
  }
  redraw();
}

export function editorKey(k) {
  const e = S.editor;
  const rows = editorRows();
  const move = (d) => {
    let i = e.cursor;
    do { i += d; } while (i >= 0 && i < rows.length && !rows[i].found);
    if (i >= 0 && i < rows.length) e.cursor = i;
  };
  switch (k) {
    case 'esc': case 'q': S.overlay = null; S.editor = null; return true;
    case 'j': case 'down': move(1); return true;
    case 'k': case 'up': move(-1); return true;
    case 'tab': case 'p': e.project = !e.project; return true;
    case 'enter': pickEditor(); return true;
  }
  return true;
}

// ---- copying ----

export async function copy(text, what) {
  let ok = false;
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {}
  }
  if (!ok) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    ta.style.top = '0';
    document.body.append(ta);
    ta.focus();
    ta.select();
    try {
      ok = document.execCommand('copy');
    } catch {}
    ta.remove();
  }
  if (what) say(what);
}

export function yank(text, lines, linewise) {
  let msg;
  if (linewise || lines > 1) msg = `copied ${lines} line${lines === 1 ? '' : 's'}`;
  else { const n = Array.from(text).length; msg = `copied ${n} character${n === 1 ? '' : 's'}`; }
  copy(text, msg);
}

function copyTarget(ref) {
  if (!ref) { say('no action selected'); return; }
  const name = '`' + ref.id + '`';
  copy(name, 'copied ' + name);
}

// absPath is file (relative to the root, with /) as the system writes it.
export const absPath = (file) => S.boot.root + S.boot.sep + file.split('/').join(S.boot.sep);

function copyPath(ref) {
  if (!ref) { say('no action selected'); return; }
  if (!ref.file) { say(ref.id + ' is not defined in a file of this project'); return; }
  if (S.copiedRel === ref.id) {
    S.copiedRel = null;
    const abs = absPath(ref.file);
    copy(abs, 'copied ' + abs);
    return;
  }
  copy(ref.file, 'copied ' + ref.file + ' · Y again copies the absolute path');
  S.copiedRel = ref.id;
}

function openTable(ref) {
  if (!ref) { say('no action selected'); return; }
  if (!ref.consoleUrl) { say('u opens a table or view in BigQuery; ' + ref.id + ' has none'); return; }
  openLink(ref.consoleUrl);
  say('opening ' + ref.id + ' in BigQuery');
}

// ---- panes ----

// rightPanes are the panes inside pane 2, the right-hand column.
const rightPanes = ['actions', 'lineage', 'run', 'defer', 'sql'];

export function setFocus(p) {
  // A hidden pane 2 comes back only with its own key.
  if (rightPanes.includes(p) && !rightShown()) { sayRightHidden(); return; }
  // Beside a Markdown file pane 2 is the preview alone.
  if (rightPanes.includes(p) && previewShown()) p = 'sql';
  // Lineage is a tab of the box the SQL is in: while it shows, that box's
  // keys are lineage's.
  else if (p === 'sql' && S.lineageOpen) p = 'lineage';
  S.focus = p;
  if (p === 'lineage') { S.lineageOpen = true; S.errTab = false; }
  if (p === 'source') {
    if (hooks.sourceFocus) hooks.sourceFocus();
  } else {
    if (hooks.sourceBlur) hooks.sourceBlur();
  }
}

// rightKey is pane 2's key, named as this machine has it.
export const rightKey = () => (onMac ? '⌥⌘b' : 'ctrl+alt+b');
const rightStoreKey = () => 'xf-web:right-hidden:' + (S.boot ? S.boot.root : '');

function readRightHidden() {
  try { return localStorage.getItem(rightStoreKey()) === '1'; } catch { return false; }
}

// autoHides says autoHidePane2 is on and the source pane shows a file
// with no action, such as a Python script or a CI file: pane 2 has
// nothing of it to show.
const autoHides = () => !!(S.boot && S.boot.autoHidePane2 && S.plainFile && !S.view && !previewShown());

// previewShown says pane 2 shows the source pane's file rendered: a
// Markdown file (markdown.js, ADR 0027).
export const previewShown = () => !!(S.plainFile && !S.view && hooks.sourceLang && hooks.sourceLang() === 'markdown');
const autoHidden = () => autoHides() && S.rightPeek !== S.plainFile;

// rightShown is whether pane 2 shows: it does unless it was hidden, or
// autoHidePane2 hides it, and always while the source pane is hidden, so
// the page is never empty (the source pane's choice is the same for every
// project, pane 2's is not). While the compile fails it shows, for its
// errors tab, unless ⌥⌘b hid it during that failure: no file has an
// action then, so autoHidePane2 would hide it beside every file.
export const rightShown = () => !sourceShown() || (hasErrors() ? !S.errHidden : !(S.rightHidden || autoHidden()));

// fitFocus gives the keys to the source pane when pane 2, which had them,
// has gone.
export function fitFocus() {
  if (rightShown() || !rightPanes.includes(S.focus)) return;
  S.zoomed = false;
  setFocus('source');
}

function sayRightHidden() { say('pane 2 hidden: ' + rightKey() + ' shows it'); }

// homeFocus is where the keys go when the pane that had them goes away.
const homeFocus = () => (rightShown() ? 'sql' : 'source');

// toggleRight is ⌥⌘b (ctrl+alt+b off a Mac): it hides and shows pane 2,
// as VS Code's secondary side bar, leaving the source pane the width, for
// a side by side difference. The keys stay where they are, unless they
// were in pane 2. Beside a file autoHidePane2 hides it for, it shows pane
// 2 for that file alone, and hides it again. While the compile fails it
// hides and shows pane 2 for that failure alone, keeping the choice made
// for a clean compile.
export function toggleRight() {
  if (!sourceShown()) { say('source is hidden: E shows it'); return; }
  if (hasErrors()) { S.errHidden = !S.errHidden; fitFocus(); return; }
  if (!rightShown()) {
    setRightHidden(false);
    if (autoHides()) S.rightPeek = S.plainFile;
  } else if (autoHides()) S.rightPeek = '';
  else setRightHidden(true);
  fitFocus();
}

function setRightHidden(hidden) {
  S.rightHidden = hidden;
  try { localStorage.setItem(rightStoreKey(), hidden ? '1' : '0'); } catch { /* hidden this page only */ }
}

// toggleLineage is O: it shows pane 2's lineage tab, with the keys, and
// from the lineage tab goes back to the SQL.
function toggleLineage() {
  if (!rightShown()) { sayRightHidden(); return; }
  if (S.lineageOpen && S.focus === 'lineage') {
    S.lineageOpen = false;
    S.focus = 'sql';
    return;
  }
  if (S.zoomed && S.focus !== 'sql' && S.focus !== 'lineage') S.zoomed = false;
  setFocus('lineage');
}

// toggleLog is L, and ctrl+j/cmd+j where there is no terminal tab. From
// the terminal tab it goes to the log tab. Closing the log while it has the keys gives
// them to the source editor in vim's normal mode, as VS Code's terminal
// hands them back to the editor.
export function toggleLog() {
  if (S.logTab === 'term') {
    S.logTab = 'log';
    if (S.showLog) { setFocus('log'); return; }
  }
  S.showLog = !S.showLog;
  if (S.showLog) { setFocus('log'); return; }
  if (S.focus === 'log') S.zoomed = false;
  leaveLog();
}

function leaveLog() {
  if (S.focus !== 'log') return;
  if (!sourceShown()) { S.focus = homeFocus(); return; }
  setFocus('source');
  if (hooks.sourceReturn) hooks.sourceReturn();
}

// toggleTerm is ctrl+j: it shows the log pane's terminal tab and
// gives it the keys, or hides the pane when the terminal already has them.
export function toggleTerm() {
  if (S.showLog && S.logTab === 'term' && S.focus === 'log') {
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    S.showLog = false;
    S.zoomed = false;
    leaveLog();
    return;
  }
  S.showLog = true;
  S.logTab = 'term';
  setFocus('log');
}

// showTerm shows the log pane's terminal tab and gives it the keys.
export function showTerm() {
  S.showLog = true;
  S.logTab = 'term';
  setFocus('log');
}

// termReserved says xf keeps a key while the terminal has the keys:
// ctrl+0…3 (go to a pane), ctrl+j and ⌘j (the terminal), alt+z and ⌘z
// (zoom it), ⌥⌘←/→ (the log pane's next tab) and the palette.
export function termReserved(e) {
  if (isTermZoomKey(e) || logTabKey(e)) return true;
  if (e.altKey) return false;
  const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
  if (e.ctrlKey && !e.metaKey && !e.shiftKey && k.length === 1 && '0123'.includes(k)) return true;
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && k === 'j') return true;
  return isP(e, false) || isP(e, true);
}

// termZoomed says the terminal tab fills the window.
const termZoomed = () => S.showLog && S.logTab === 'term' && S.focus === 'log' && S.zoomed;

// zoomTerm shows the terminal tab zoomed, with the keys.
function zoomTerm() {
  S.showLog = true;
  S.logTab = 'term';
  setFocus('log');
  S.zoomed = true;
}

// toggleTermZoom is ⌘z (alt+z) in the terminal, and the palette's "View:
// zoom terminal": zoom it, or put it back.
export function toggleTermZoom() {
  if (termZoomed()) S.zoomed = false;
  else zoomTerm();
}

// isTermZoomKey is alt+z everywhere (⌥z on a Mac) and ⌘z on a Mac too,
// which zooms the terminal as z zooms a pane. A terminal never sees ⌘, so
// ⌘z costs the shell nothing; ctrl+z stays the shell's (it suspends a
// command). Read by code: option changes the key a Mac reports.
export const isTermZoomKey = (e) => e.code === 'KeyZ' && !e.ctrlKey && !e.shiftKey &&
  (e.altKey ? !e.metaKey : onMac && e.metaKey);

// logTabKey is ⌥⌘→ (ctrl+alt+→ off a Mac), the log pane's next tab, as 1,
// and ⌥⌘← its tab before, as -1; any other key is 0. Read from inside
// the terminal too, where tab is the shell's.
export function logTabKey(e) {
  if (e.shiftKey || !e.altKey || !(onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey)) return 0;
  return e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
}

// termHasKey says a key event is the terminal's: typed in it, and not
// one xf keeps. xf's key handlers leave such a key alone.
export function termHasKey(e) {
  return e.target instanceof Element && !!e.target.closest('.xterm') && !termReserved(e);
}

// termFocused says the terminal has the keys.
export const termFocused = () => typeof document !== 'undefined' && document.activeElement instanceof Element && !!document.activeElement.closest('.xterm');

function focusLog() {
  if (S.showLog) setFocus('log');
  else say('no log yet: r runs the selected action (L shows the log)');
}

function toggleIncremental() {
  S.incremental = !S.incremental;
  if (!S.view || !S.view.hasIncremental) say('v only applies to incremental tables with an incremental variant');
  fetchView(false);
}

function toggleColumns() {
  S.cols.shown = !S.cols.shown;
  S.cols.typing = false;
  S.buf.visual = vim.VISUAL_NONE;
  if (S.cols.shown) setFocus('sql');
}

// columnsOrSQL is c: pane 2's lower box goes round sql, columns and
// lineage, from pane 2 or the log, and it gives that section the keys, so
// z zooms it (ADR 0017). From the errors tab it goes to the columns.
function columnsOrSQL() {
  if (!rightShown()) { sayRightHidden(); return; }
  if (S.zoomed && S.focus !== 'sql' && S.focus !== 'lineage') S.zoomed = false;
  const on = sqlTabOn();
  const next = on === 'columns' ? 'lineage' : on === 'lineage' ? 'sql' : 'columns';
  pickSQLTab(next);
  S.cols.typing = false;
  S.buf.visual = vim.VISUAL_NONE;
  setFocus(next === 'lineage' ? 'lineage' : 'sql');
}

// pane2Default puts pane 2's lower box on the pane2Tab setting's tab, sql
// or columns: as a new browser tab starts, and once the setting changes.
// A declaration shown keeps its own tab (ADR 0015) and the setting's
// comes back with the next action.
export function pane2Default(tab) {
  const shown = tab === 'columns';
  if (S.cols.ownShown != null) S.cols.ownShown = shown;
  else S.cols.shown = shown;
}

export const columnsFocused = () => S.cols.shown && !errorsTabShown() && S.focus === 'sql';

const sourceShown = () => !!(hooks.sourceShownNow && hooks.sourceShownNow());

export function filteredColumns() {
  const fields = S.view ? S.view.columns.fields : [];
  const name = S.cols.name.trim().toLowerCase(), type = S.cols.type.trim().toLowerCase();
  if (!name && !type) return fields;
  return fields.filter((f) => (f.name.toLowerCase().includes(name) || (f.description || '').toLowerCase().includes(name)) && f.type.toLowerCase().includes(type));
}

// ---- runs ----

function onRun(v) {
  const had = S.run;
  if (!had || v.seq !== had.seq) {
    // A new run (maybe from another page): its log is fetched whole.
    S.run = { ...v, lines: v.lines || [] };
    if (v.seq && !S.showLog) S.showLog = true;
    if (v.seq && (S.logTab === 'history' || (S.logTab === 'term' && !termFocused()))) S.logTab = 'log';
    S.logFollow = true;
    if (!v.lines) get('/api/run').then((r) => { S.run = r; redraw(); }).catch(() => {});
    return;
  }
  S.run = { ...v, lines: had.lines };
  if (!v.running && had.running) refreshView();
}

function onLog(ev) {
  const r = S.run;
  if (!r || r.seq !== ev.seq) return;
  if (ev.from !== r.lines.length) {
    get('/api/run').then((v) => { S.run = v; redraw(); }).catch(() => {});
    return;
  }
  r.lines = r.lines.concat(ev.lines);
  r.lineCount = r.lines.length;
}

export const running = () => !!(S.run && S.run.running);

function runPreview() { return S.view ? S.view.run : null; }

function startRun() {
  if (running()) { say('a run is already in progress (ctrl+c cancels it)'); return; }
  if (!S.sel || !S.view) { say('select an action to run'); return; }
  const p = runPreview();
  if (!S.caps.run) { say('running is not available'); return; }
  if (!p.actions.length) { say(p.why); return; }
  // A run never takes code that isn't saved: unsaved source is saved
  // first. The tool compiles the files on disk as it starts, so the run
  // needn't wait for xf's own compile; a refused save (the file changed
  // on disk) asks what to keep and runs nothing.
  if (hooks.sourceHolds && hooks.sourceHolds() && hooks.sourceSave) {
    hooks.sourceSave().then((ok) => { if (ok) { startRun(); redraw(); } });
    return;
  }
  const req = { kind: 'actions', ids: p.actions, toggles: toggles() };
  if (S.flags.full) {
    S.overlay = 'confirmFull';
    S.confirm = { names: p.actions.map(shortID), req };
    return;
  }
  launch(req);
}

export function shortID(id) { const parts = id.split('.'); return parts.slice(-2).join('.'); }

export const toggles = () => ({ deps: S.flags.deps, dependents: S.flags.dependents, full: S.flags.full, api: S.flags.api });

export async function launch(req) {
  try {
    if (deferOn()) { say('checking defer to prod…'); redraw(); }
    const r = await post('/api/run', req);
    if (req.toggles && req.toggles.api) readGit(); // the line says why it couldn't, too
    if (r.ask) { say(''); askDefer(r.ask, req); return; }
    if (!r.ok) { say(r.message); return; }
    if (deferOn()) say('');
    if (!S.showLog) S.showLog = true;
    if (S.logTab === 'history' || (S.logTab === 'term' && !termFocused())) S.logTab = 'log';
  } catch (e) { say(e.message); }
}

export async function cancelRun() {
  try { await post('/api/run/cancel'); } catch (e) { say(e.message); }
}

function toggleFlag(name) {
  if (name === 'api' && !S.caps.api) { say('API runs are not available'); return; }
  S.flags[name] = !S.flags[name];
  refreshView();
}

// ---- the palette ----

// openPalette opens the palette on query: "" finds a file, "#" (or a
// "$kind") an action, ">" lists commands (docs/adr/0014).
export function openPalette(query = '') {
  const actions = /^ *[#$]/.test(query);
  if (actions && (!S.state || !S.state.hasGraph)) { say('no actions yet: waiting for a compile'); return; }
  const first = [];
  if (S.sel) first.push(S.sel);
  if (S.following && S.lastEditor) first.push(...(S.editorActions || []));
  if (S.state && S.state.changed) first.push(...S.state.changed);
  first.push(...S.recent);
  // A zoomed terminal stays zoomed under the palette, so its zoom command
  // can put it back; any other pane unzooms.
  const keptZoom = termZoomed();
  if (!keptZoom) S.zoomed = false;
  S.overlay = 'palette';
  S.palette = { ...S.palette, scratch: false, keptZoom, query, cursor: 0, first, firstFiles: firstFiles(), commands: byRecent(commands()), symbols: null, symKind: '', items: [], found: 0, fromEditor: editingSource() };
  if (actionQuery() !== null) findActions(true);
  else if (fileQuery() !== null) { startOnPrevious(); fetchFiles(); }
}

// openScratchPalette opens the palette on the Scratch view's own commands
// (scratch.js's scratchCommands): the project's files, actions and commands
// don't apply there. Cancelled, it gives the keys back to the Scratch
// editor it was opened from.
export function openScratchPalette() {
  S.overlay = 'palette';
  S.palette = { ...S.palette, scratch: true, keptZoom: false, query: '>', cursor: 0, commands: byRecent(hooks.scratchCommands()), symbols: null, symKind: '', items: [], found: 0, fromEditor: !!(hooks.scratchEditing && hooks.scratchEditing()) };
}

// editingSource is whether the source editor has the keys.
const editingSource = () => !!(hooks.sourceEditing && hooks.sourceEditing());

// cancelPalette closes the palette with nothing picked: the source editor
// it was opened from gets the keys back, in vim's normal mode.
export function cancelPalette() {
  S.overlay = null;
  if (S.palette.scratch) { if (S.palette.fromEditor && hooks.scratchReturn) hooks.scratchReturn(); return; }
  if (S.palette.fromEditor && S.focus === 'source' && hooks.sourceReturn) hooks.sourceReturn();
}

// symbolQuery is the query after its "@", or null when it lists actions or commands.
export function symbolQuery() {
  if (S.palette.scratch) return null;
  const q = S.palette.query.replace(/^ +/, "");
  return q.startsWith("@") ? q.slice(1) : null;
}

export async function fetchSymbols() {
  if (hooks.sourceSymbols) {
    const syms = await hooks.sourceSymbols();
    S.palette.symbols = syms || [];
    redraw();
  }
}

export async function openSymbols() {
  if (!shownFile()) { say("no source file open for symbols"); return; }
  if (hooks.sourceShownNow && !hooks.sourceShownNow()) {
    if (hooks.toggleSource) hooks.toggleSource();
  }
  setFocus("source");
  // The editor's ctrl+shift+o and the page's both get here; the second,
  // with the palette's box focused, keeps where the first came from.
  const fromEditor = S.overlay === "palette" ? S.palette.fromEditor : editingSource();
  S.zoomed = false;
  S.overlay = "palette";
  S.palette = { ...S.palette, scratch: false, query: "@", cursor: 0, symbols: null, symKind: "", items: [], found: 0, fromEditor };
  redraw();
  await fetchSymbols();
}
hooks.openSymbols = openSymbols;

let findTimer = null;
export function setPaletteQuery(q) {
  S.palette.query = q;
  S.palette.cursor = 0;
  if (symbolQuery() !== null) {
    if (S.palette.symbols === undefined || S.palette.symbols === null) fetchSymbols();
    redraw();
    return;
  }
  if (actionQuery() === null) {
    if (fileQuery() !== null && paletteFiles === null) fetchFiles();
    redraw();
    return;
  }
  clearTimeout(findTimer);
  findTimer = setTimeout(() => findActions(false), 25);
  redraw();
}

async function findActions(opening) {
  const p = S.palette;
  const seq = ++p.seq;
  if (!S.state || !S.state.hasGraph) { Object.assign(p, { items: [], found: 0, total: 0, badKind: '' }); redraw(); return; }
  try {
    const r = await post('/api/find', { query: actionQuery(), first: p.first, limit: 500 });
    if (seq !== S.palette.seq) return;
    Object.assign(S.palette, { items: r.items, found: r.found, total: r.total, badKind: r.badKind || '' });
    if (opening && !actionQuery() && S.sel && r.items.length > 1) S.palette.cursor = 1;
  } catch (e) { say(e.message); }
  redraw();
}

// actionQuery is what the palette asks the server for actions with: the
// query after its "#", or one that starts with a "$kind"; null when it
// lists files, symbols or commands.
export function actionQuery() {
  if (S.palette.scratch) return null;
  const q = S.palette.query.replace(/^ +/, '');
  if (q.startsWith('#')) return q.slice(1);
  return q.startsWith('$') ? q : null;
}

// fileQuery is the query when the palette lists files, else null.
export function fileQuery() {
  if (S.palette.scratch) return null;
  const q = S.palette.query.replace(/^ +/, '');
  return /^[#$>@%]/.test(q) ? null : q;
}

// ---- the palette's files ----

// paletteFiles are the files the palette lists, the file tree's
// (/api/files), kept from one opening to the next so it shows them at
// once while it asks again.
let paletteFiles = null, filesError = '', filesSeq = 0, filesAsked = null;
export const paletteFilesState = () => ({ files: paletteFiles, error: filesError });

function fetchFiles() {
  filesError = '';
  filesAsked = askFiles();
  return filesAsked;
}

async function askFiles() {
  const n = ++filesSeq;
  try {
    const v = await get('/api/files');
    if (n !== filesSeq) return;
    paletteFiles = v.files || [];
    filesError = '';
    startOnPrevious();
  } catch (e) {
    if (n !== filesSeq) return;
    filesError = e.message;
  }
  redraw();
}

// shownFile is the file the source pane is on, or would be.
const shownFile = () => S.plainFile || (S.view && S.view.file) || '';

// firstFiles are the files an empty query lists first: the one shown,
// then the source pane's buffers, the last shown first.
function firstFiles() {
  const out = [];
  const add = (f) => { if (f && !out.includes(f)) out.push(f); };
  add(shownFile());
  for (const f of (hooks.recentFiles ? hooks.recentFiles() : [])) add(f);
  return out;
}

// startOnPrevious puts the cursor on the file before the one shown, as
// the action list does: enter goes back, the key again goes further.
function startOnPrevious() {
  const p = S.palette;
  if (S.overlay !== 'palette' || p.cursor !== 0 || fileQuery() !== '') return;
  const list = matchedFiles();
  if (list.length > 1 && list[0].path === shownFile()) p.cursor = 1;
}

// maxFileRows bounds the rows the palette draws of the files that match.
const maxFileRows = 500;

// matchedFiles lists the files matching the palette's query.
export function matchedFiles() {
  return rankFiles(paletteFiles || [], fileQuery() || '', S.palette.firstFiles || []);
}

// rankFiles lists the files whose path holds every word of query, written
// or with its letters scattered. Those with every word in their name come
// first, then those with every word in their path, then the scattered
// ones in the same order. Within each, and for an empty query, first's
// files lead, then those changed in git, then the rest by path.
export function rankFiles(files, query, first = []) {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  const lead = new Map(first.map((f, i) => [f, i]));
  const order = (a, b) => {
    const la = lead.has(a.path) ? lead.get(a.path) : Infinity, lb = lead.has(b.path) ? lead.get(b.path) : Infinity;
    if (la !== lb) return la - lb;
    if (!!a.git !== !!b.git) return a.git ? -1 : 1;
    return a.path < b.path ? -1 : a.path > b.path ? 1 : 0;
  };
  if (!terms.length) return files.slice().sort(order);
  const tiers = [[], [], [], []];
  for (const f of files) {
    const path = f.path.toLowerCase();
    const name = path.slice(path.lastIndexOf('/') + 1);
    let inName = true, inPath = true, ok = true, looseName = true;
    for (const w of terms) {
      if (!name.includes(w)) inName = false;
      if (!path.includes(w)) {
        inPath = false;
        if (!fuzzy(w, path)) { ok = false; break; }
      }
      if (!name.includes(w) && !fuzzy(w, name)) looseName = false;
    }
    if (!ok) continue;
    tiers[inName ? 0 : inPath ? 1 : looseName ? 2 : 3].push(f);
  }
  return tiers.flatMap((t) => t.sort(order));
}

// projectQuery is the query after its "%", or null when the palette
// lists something else: the open projects.
export function projectQuery() {
  if (S.palette.scratch) return null;
  const q = S.palette.query.replace(/^ +/, '');
  return q.startsWith('%') ? q.slice(1) : null;
}

// matchedProjects lists the open projects holding every word of the
// query in their name or path: the one this tab shows first, then those
// it showed before, the last first, then the rest by name.
export function matchedProjects() {
  const terms = (projectQuery() || '').toLowerCase().split(/\s+/).filter(Boolean);
  const mine = shownOrder();
  const rank = (o) => { const i = mine.indexOf(o.key); return o.key === S.key ? -1 : i >= 0 ? i : 1000; };
  return openByName()
    .filter((o) => terms.every((w) => o.name.toLowerCase().includes(w) || (o.root || '').toLowerCase().includes(w)))
    .sort((a, b) => rank(a) - rank(b));
}

// openProjectList opens the palette on the open projects, to switch to
// one; the cursor starts on the project this tab showed before, so enter
// goes back and forth between two. Open already, it changes to them.
export function openProjectList() {
  if (S.overlay === 'palette') { setPaletteQuery('%'); return; }
  openPalette('%');
  if (matchedProjects().length > 1) S.palette.cursor = 1;
}
hooks.openProjectList = openProjectList;

// commandQuery is the query after its ">", or null when it lists files or
// actions. Scratch's palette lists only commands, with or without it.
export function commandQuery() {
  const q = S.palette.query.replace(/^ +/, '');
  if (S.palette.scratch) return q.replace(/^>/, '');
  return q.startsWith('>') ? q.slice(1) : null;
}

function fuzzy(needle, hay) {
  let i = 0;
  for (const c of hay) if (c === needle[i]) i++;
  return i === needle.length;
}

// matchCommands lists the commands holding every word of the query: those
// with each word as written first, then those with its letters scattered.
export function matchedCommands() {
  const q = commandQuery();
  const terms = (q || '').toLowerCase().split(/\s+/).filter(Boolean);
  const exact = [], loose = [];
  for (const c of S.palette.commands) {
    const t = c.title.toLowerCase();
    let written = true, ok = true;
    for (const w of terms) {
      if (t.includes(w)) continue;
      if (!fuzzy(w, t)) { ok = false; break; }
      written = false;
    }
    if (ok) (written ? exact : loose).push(c);
  }
  return exact.concat(loose);
}

// matchedSymbols lists the symbols holding every word of the query, of
// any kind. A ":15" word matches the symbols on lines starting 15.
export function matchedSymbols() {
  const q = (symbolQuery() || "").trim().toLowerCase();
  if (!q) return S.palette.symbols || [];
  const terms = q.split(/\s+/).filter(Boolean);
  const exact = [], loose = [];
  for (const s of (S.palette.symbols || [])) {
    const t = s.name.toLowerCase();
    const k = (s.kind || "").toLowerCase();
    let written = true, ok = true;
    for (const w of terms) {
      if (/^:\d*$/.test(w)) { if (String(s.line).startsWith(w.slice(1))) continue; ok = false; break; }
      if (t.includes(w) || k.includes(w)) continue;
      if (!fuzzy(w, t) && !fuzzy(w, k)) { ok = false; break; }
      written = false;
    }
    if (ok) (written ? exact : loose).push(s);
  }
  return exact.concat(loose);
}

// symbolKinds is each kind in the model's symbols, in the order they
// come, with how many of them the query matches.
export function symbolKinds() {
  const n = new Map();
  for (const s of (S.palette.symbols || [])) n.set(s.kind, 0);
  for (const s of matchedSymbols()) n.set(s.kind, n.get(s.kind) + 1);
  return [...n];
}

// cycleSymbolKind steps the symbols list's kind filter: all, then each kind.
export function cycleSymbolKind(step) {
  const kinds = ['', ...symbolKinds().map(([k]) => k)];
  const i = Math.max(kinds.indexOf(S.palette.symKind || ''), 0);
  S.palette.symKind = kinds[(i + step + kinds.length) % kinds.length];
  S.palette.cursor = 0;
}

export function paletteList() {
  if (symbolQuery() !== null) {
    const k = S.palette.symKind;
    return k ? matchedSymbols().filter((s) => s.kind === k) : matchedSymbols();
  }
  if (commandQuery() !== null) return matchedCommands();
  if (projectQuery() !== null) return matchedProjects();
  return fileQuery() !== null ? matchedFiles().slice(0, maxFileRows) : S.palette.items;
}

export function pickFromPalette(i, flip) {
  pickPaletteItem(i, flip);
  // A pick that takes the keys from a zoomed terminal unzooms it, rather
  // than zooming the pane they go to.
  if (S.palette.keptZoom && S.overlay !== 'palette' && !termZoomed()) S.zoomed = false;
}

function pickPaletteItem(i, flip) {
  // enter before the files come picks once they have.
  if (fileQuery() !== null && paletteFiles === null && filesAsked && !filesError) {
    const q = S.palette.query;
    filesAsked.then(() => { if (S.overlay === 'palette' && S.palette.query === q) { pickFromPalette(i); redraw(); } });
    return;
  }
  const isSymbols = symbolQuery() !== null;
  const list = paletteList();
  const item = list[i === undefined ? S.palette.cursor : i];
  if (!item) { cancelPalette(); return; }
  S.overlay = null;
  if (projectQuery() !== null) { switchProject(item.key, flip, true); return; }
  if (isSymbols) {
    if (hooks.jumpToSymbol) hooks.jumpToSymbol(item);
    return;
  }
  if (item.run) {
    countUse(item.title);
    item.run();
    if (S.palette.scratch && S.palette.fromEditor && hooks.scratchReturn) hooks.scratchReturn();
    return;
  }
  // A file opens as the file tree opens it, its first action selected.
  if (item.path !== undefined) { if (hooks.openFile) hooks.openFile(item.path, { action: item.action }); return; }
  if (S.sel && S.sel !== item.id) S.back.push(S.sel);
  // The source pane's cursor goes to where the action is defined.
  if (hooks.placeAction) hooks.placeAction(item.id);
  selectAction(item.id);
  // The picked action's file is what pane 1 shows, so the keys go there.
  if (sourceShown()) setFocus('source');
  else S.focus = 'sql';
}

// byRecent puts the commands done before first, the last done first;
// the rest keep their order.
export function byRecent(cmds) {
  const at = (c) => Date.parse(S.commandUses[c.title]?.last || '') || 0;
  return cmds.sort((a, b) => at(b) - at(a));
}

// countUse counts a use of the command titled title, here at once and on
// the server, whose answer has the TUI's uses too.
function countUse(title) {
  const u = S.commandUses[title] || { count: 0 };
  S.commandUses[title] = { count: u.count + 1, last: new Date().toISOString() };
  post('/api/command', { title }).then((r) => { if (r) S.commandUses = r; }).catch(() => {});
}

// commands are what the palette's ">" lists, as the TUI's do.
function commands() {
  const out = [];
  const add = (title, key, run, state) => out.push({ title, key, run, state });
  const toggle = (title, on, key, f) => add(title, key, f, on ? 'on' : 'off');
  const v = S.view;
  const p = v && v.run;
  if (S.caps.run && p && p.actions.length && !running()) {
    const runWith = (d, dd) => () => { S.flags.deps = d; S.flags.dependents = dd; refreshView(); setTimeout(startRun, 150); };
    add('Run', 'r', startRun);
    add('Run with dependencies', '', runWith(true, false));
    add('Run with dependents', '', runWith(false, true));
    add('Run with dependencies and dependents', '', runWith(true, true));
  }
  if (running()) add('Run: cancel', 'ctrl+c', cancelRun);
  if (S.caps.changed) add('Run changed actions', 'R', openChanged);
  if (S.caps.run) add('Run tags', 'T', openTags);
  if (S.caps.watch) add('Run: watch a workflow by ID or URL (cost of each job)', '', openWatch);
  if (S.caps.run) {
    toggle('Run option: full refresh', S.flags.full, 'f', () => toggleFlag('full'));
    if (S.caps.api) toggle('Run option: via Dataform API', S.flags.api, 'a', () => toggleFlag('api'));
    toggle('Run option: include dependencies', S.flags.deps, 'd', () => toggleFlag('deps'));
    toggle('Run option: include dependents', S.flags.dependents, 'D', () => toggleFlag('dependents'));
  }
  if (hasTabs() && S.history.rows.length) add('History: clear kept runs', 'X', () => { S.overlay = 'confirmClear'; });
  if (S.caps.scratch) {
    if (v) add('Scratch: new query from this model', 's', () => hooks.askNew());
    add('Scratch: go to last query', '', () => hooks.openLastScratch());
    add('Scratch: open scratch queries', '', () => hooks.openScratch());
    add('Scratch: new empty query', '', () => hooks.openScratch(null, { draft: true }));
  }
  add('Check: compile', saveKey, saveAndCompile);
  add('Check: show errors', 'e', showErrorsOrSay);
  // The page's module isn't in the first load: the command loads it. A
  // page left open goes back to as it was; another model opens anew.
  if (S.caps.inspector) {
    const kept = hooks.inspectorKept && hooks.inspectorKept();
    if (kept) add(`Dependency inspector: go back to ${kept.name}`, '', () => import('./inspector.js').then((m) => m.openInspector()));
    if (v && (!kept || kept.model !== v.id)) add('Dependency inspector: filter and query this model\'s upstream tables', '', () => import('./inspector.js').then((m) => m.openInspector(v.id)));
  }
  if (S.caps.impact) add('Check: column impact of changes', 'I', openImpact);
  if (S.caps.cost) add('Check: query cost of changes', 'C', openCost);
  if (S.caps.defer) {
    toggle('Defer to prod', deferOn(), '', toggleDefer);
    if (S.boot.backend !== 'dbt') add('Defer to prod: remove proxy views', '', removeProxies);
  }
  if (v) {
    if (S.caps.open && v.file) add('Open: in editor', 'o', openInEditor);
    if (S.caps.editor) add('Open: choose the editor o uses', '', () => openEditorPicker(null, false));
    if (v.consoleUrl) add('Open: in BigQuery', 'u', () => openTable(v));
    add('Copy: `table` name', 'y', () => copyTarget(v));
  }
  // The paths are of the file the source pane shows, a file with no
  // action too; with the pane hidden, of the action's.
  const file = (hooks.sourceFile && hooks.sourceFile()) || (v && v.file) || '';
  if (file) {
    add('Copy: relative path', 'Y', () => copy(file, 'copied ' + file));
    add('Copy: absolute path', '', () => { const abs = absPath(file); copy(abs, 'copied ' + abs); });
  }
  if (S.run && S.run.url) add('Open: run in browser', 'u', () => openURL(S.run.url, 'the workflow invocation'));
  if (v && v.hasIncremental) toggle('View: incremental SQL', S.incremental, 'i', toggleIncremental);
  toggle('View: columns', S.cols.shown, 'c', toggleColumns);
  toggle('View: log', S.showLog && S.logTab !== 'term', S.caps.terminal ? 'L' : 'ctrl+j', toggleLog);
  if (S.caps.terminal) {
    toggle('View: terminal', S.showLog && S.logTab === 'term', 'ctrl+j', toggleTerm);
    toggle('View: zoom terminal', termZoomed(), onMac ? '⌘z' : 'alt+z', toggleTermZoom);
  }
  if (hooks.toggleTree) toggle('View: file tree', hooks.treeShown() && !hooks.scmShown() && !hooks.projectsShown(), onMac ? '⌘b' : 'ctrl+b', hooks.toggleTree);
  if (hooks.revealActive) add('View: show the open file in the file tree', revealKey, hooks.revealActive);
  add('View: open projects', projectsKey, openProjectList);
  if (hooks.toggleSCM) toggle('View: source control', hooks.scmShown(), 'ctrl+g', hooks.toggleSCM);
  if (hooks.searchCommands) for (const [label, key, f] of hooks.searchCommands()) add(label, key, f);
  toggle('View: toggle pane 2', rightShown(), rightKey(), toggleRight);
  toggle('View: lineage', S.lineageOpen, 'O', toggleLineage);
  toggle('View: zoom pane', S.zoomed, 'z', () => { S.zoomed = !S.zoomed; });
  if (S.caps.follow) toggle('View: follow editor', S.following, 'F', toggleFollow);
  add('View: colour theme', '', () => openThemePicker('theme'), T.choice.theme === 'auto' ? 'auto' : themeName(T.choice.theme));
  add('View: settings and project info', 'ctrl+,', () => hooks.openSettings());
  add('Project: switch or add', '', openProjects, S.openIn === 'new-tab' ? 'in a new tab' : 'in this tab');
  const next = nextOpenProject();
  if (next) add('Project: next open project', '', cycleProject, next.name);
  if (hooks.openWindows) add('View: browser windows', '', hooks.openWindows, (hooks.otherWindows() + 1) + ' open');
  if (S.caps.branches) {
    add('Git: switch branch', 'B', openBranches, 'or make one');
    add('Git: checkout branch', 'B', openBranches, 'or make one');
    add('Git: open branch in a worktree', '', openBranches);
  }
  // A worktree can go from its own project (ADR 0032), once a dialog asks.
  const me = S.open.find((o) => o.key === S.key);
  if (me && me.worktree) add('Worktree: remove this worktree', '', () => askRemoveWorktreeHere({ ...me, path: me.root }), me.worktree);
  if (S.caps.branches && S.caps.changed) add('Git: compare with branch (R, I and C)', '', openBase, 'now ' + S.boot.defaultBranch);
  if (hooks.scmCommands) for (const [label, key, f] of hooks.scmCommands()) add(label, key, f);
  if (hooks.forgeCommands) for (const [label, key, f] of hooks.forgeCommands()) add(label, key, f);
  if (hooks.pythonCommands) for (const [label, key, f, state] of hooks.pythonCommands()) add(label, key, f, state);
  if (hooks.formatCommands) for (const [label, key, f] of hooks.formatCommands()) add(label, key, f);
  if (hooks.sourceCommands) for (const [label, key, f, state] of hooks.sourceCommands()) add(label, key, f, state);
  if (hooks.bufferCommands) for (const [label, key, f, state] of hooks.bufferCommands()) add(label, key, f, state);
  if (v) add('SQL: search', '/', searchSQLFromPalette);
  if (shownFile()) add('Go to: symbol in model', 'ctrl+shift+o', openSymbols);
  add('Go to: file', paletteKey, () => openPalette());
  if (S.state && S.state.hasGraph) add('Go to: another action', paletteKey + ' #', () => openPalette('#'));
  if (paneActions().length > 1) {
    add('Go to: next action in file', ']', () => cycleInFile(1));
    add('Go to: previous action in file', '[', () => cycleInFile(-1));
  }
  if (sourceShown()) add('Go to: source', 'ctrl+1', () => setFocus('source'));
  add('Go to: actions', 'ctrl+2', () => setFocus('actions'));
  add('Go to: sql', '', () => setFocus('sql'));
  add('Go to: lineage', '', () => setFocus('lineage'));
  if (S.caps.run && !(v && v.external)) add('Go to: run', '', () => setFocus('run'));
  if (S.showLog) add('Go to: log', 'ctrl+3', focusLog);
  add('Help: every key', '?', openHelp);
  add('Close project: back to the projects', 'ctrl+q', askQuit);
  return out;
}

// ---- checks and overlays ----

function compile() { post('/api/compile').catch((e) => say(e.message)); }

// saveAndCompile is saveKey, the only key that compiles (ADR 0017): it
// saves the source pane's unsaved changes, which the file watcher
// compiles, or else compiles. With watching off it compiles after saving
// too; a save that fails says why and compiles nothing. With R open it
// then asks again what changed.
export async function saveAndCompile() {
  if (hooks.sourceHolds && hooks.sourceHolds() && hooks.sourceSave) {
    if (!(await hooks.sourceSave())) return;
    if (S.state && S.state.watchErr) compile();
  } else compile();
  if (S.overlay === 'changed') requestChanged();
}

// scratchShowing says the Scratch view is what the page shows, which draws
// no overlays: one opened behind it would only swallow its keys.
const scratchShowing = () => !!(S.scratch && S.scratch.open);

async function refreshErrorsOverlay() {
  try { S.errors = await get('/api/errors'); } catch { return; }
  redraw();
}

export async function openErrors() {
  if (scratchShowing()) return;
  S.overlay = 'errors';
  S.errorsOffset = 0;
  try { S.errors = await get('/api/errors'); } catch (e) { say(e.message); }
  redraw();
}

// showErrorsOrSay is e: the compile's errors in pane 2 while it fails,
// else the dry runs' in the errors overlay.
function showErrorsOrSay() {
  const st = S.state;
  if (hasErrors()) showErrorsTab();
  else if (st && (st.dryRunErrors > 0 || st.dryRunAuth)) openErrors();
  else say('no errors');
}

export function openHelp() { S.overlay = 'help'; S.help = { query: '', typing: false, cur: 0 }; }

// askQuit is ctrl+q's: the project closes at once, unless closing would
// lose something (a run in progress, other tabs' unsaved changes to its
// files, a command running in the terminal tab), when the confirm names it.
export async function askQuit() {
  if (followHeld()) {
    // With autosave on, the source pane saves first.
    hooks.sourceFlush().then((ok) => { if (ok) { askQuit(); redraw(); } else say(unsavedIn('the source pane')); });
    return;
  }
  S.quitUnsaved = [];
  S.quitTerm = '';
  let known = true;
  try {
    const [un, term] = await Promise.all([
      get('/api/unsaved?tab=' + tabID),
      S.caps.terminal ? get('/api/term/state') : {},
    ]);
    S.quitUnsaved = un.files || [];
    S.quitTerm = term.command || '';
  } catch (e) { known = false; }
  if (known && !S.quitUnsaved.length && !S.quitTerm && !(S.run && S.run.running)) { closeProject(); return; }
  S.overlay = 'confirmQuit';
  redraw();
}

// quit stops xf web, from the picker.
export async function quit() {
  try { await post('/api/quit'); } catch { /* it went */ }
  S.mode = 'gone';
  redraw();
}

// closeProject is ctrl+q's: the project closes for every tab on it, and each goes
// to the project it showed before, or the projects (ADR 0008).
export async function closeProject() {
  S.overlay = null;
  const name = S.boot.name;
  say('closing ' + name + '…');
  leaving = true;
  try {
    const r = await post('/api/close', { by: windowWord() });
    goAfterClose(S.key, r.open, 'closed ' + name);
  } catch (e) {
    leaving = false;
    say(e.message);
  }
}

// closeListed closes the open project c from the projects list, after
// its confirm; this tab's own is ctrl+q.
export async function closeListed(c) {
  if (S.mode === 'project' && c.key === S.key) { closeProject(); return; }
  try {
    await post('/api/close', { key: c.key, by: windowWord() });
    say('closed ' + c.name);
    setPickerQuery(S.pickerQuery);
  } catch (e) { say(e.message); }
}

// arrangePicker puts the open projects first, those this tab showed
// last first, then the order xf web has; the rest follow as they were.
// With keep, the cursor stays on its project.
export function arrangePicker(keep) {
  const at = keep && S.picker[S.pickerCursor] ? S.picker[S.pickerCursor].key : null;
  const open = new Set(S.open.map((o) => o.key));
  for (const p of S.picker) p.open = open.has(p.key);
  const mine = shownOrder(), theirs = S.open.map((o) => o.key);
  const rank = (k) => { const i = mine.indexOf(k); return i >= 0 ? i : 1000 + theirs.indexOf(k); };
  S.picker = nestWorktrees([...S.picker.filter((p) => p.open && !p.found).sort((a, b) => rank(a.key) - rank(b.key)), ...S.picker.filter((p) => !p.open && !p.found)]);
  if (at) S.pickerCursor = Math.max(0, S.picker.findIndex((p) => p.key === at));
}

// nestWorktrees puts each worktree's project under its main checkout's
// project (ADR 0032), by name, with the worktrees git found that xf
// hasn't opened yet, when the search matches them. A worktree whose main
// isn't listed keeps its place.
function nestWorktrees(list) {
  const roots = new Set(list.map((p) => p.root));
  const kids = {};
  for (const p of list) if (p.main && roots.has(p.main)) (kids[p.main] ||= []).push(p);
  const words = S.pickerQuery.toLowerCase().split(/\s+/).filter(Boolean);
  for (const [main, found] of Object.entries(S.pickerWorktrees || {})) {
    if (!roots.has(main)) continue;
    for (const f of found) {
      const text = [f.name, f.path, f.branch, f.backend].join(' ').toLowerCase();
      if (!roots.has(f.root) && words.every((w) => text.includes(w))) (kids[main] ||= []).push({ ...f, key: '', when: 'not opened yet', found: true });
    }
  }
  const out = [];
  for (const p of list) {
    if (p.main && roots.has(p.main)) continue;
    p.child = false;
    out.push(p);
    for (const k of (kids[p.root] || []).sort((a, b) => a.name.localeCompare(b.name))) { k.child = true; out.push(k); }
  }
  return out;
}

// readPickerWorktrees asks for the worktrees git lists that xf hasn't
// opened, which the list then shows under their projects.
export function readPickerWorktrees() {
  S.pickerWorktrees = null;
  get('/api/picker/worktrees').then((m) => { S.pickerWorktrees = m; arrangePicker(true); redraw(); }).catch(() => {});
}

// removeListedWorktree removes the worktree that c names, once
// confirmed: the server closes its project for every tab first. One with
// work git would lose is asked about again, naming it (c.dirty), and the
// next yes removes it anyway. Asked from the remove worktree dialog, the
// dialog stays while it works and shows a refusal, closing only once the
// worktree is gone; from the switcher's footer, the footer says how it
// went, and a refusal stays until a key.
export async function removeListedWorktree(c) {
  const dialog = () => S.overlay === 'confirmRemoveWorktree' && S.removeWorktree === c;
  const inDialog = dialog();
  if (inDialog) { c.busy = true; c.err = ''; } else S.flash = 'removing the worktree ' + c.name + '…';
  redraw();
  let r;
  try {
    r = await post('/api/worktree/remove', { root: c.root, by: windowWord(), force: !!c.dirty });
  } catch (e) {
    r = { ok: false, message: e.message };
  }
  c.busy = false;
  if (!r.ok && r.kind === 'dirty') {
    c.dirty = r.message;
    if (inDialog) { if (!dialog()) return; } else { S.flash = ''; S.pickerConfirm = c; }
    redraw();
    return;
  }
  if (!r.ok) {
    if (inDialog && dialog()) c.err = r.message; else S.flash = r.message;
    redraw();
    return;
  }
  if (dialog()) { S.overlay = null; S.removeWorktree = null; }
  say(r.message);
  if (S.overlay === 'projects' || S.mode === 'picker') { setPickerQuery(S.pickerQuery); readPickerWorktrees(); }
  readOpenWorktrees(true); // the projects tab lists one not open
}

// openProjects lists the projects over this one, to switch to another or
// open one: in this tab, or a new one, as webOpenProjectIn says
// (shift+enter does the other).
export function openProjects() {
  S.overlay = 'projects';
  S.pickerConfirm = null;
  S.paths = null;
  stopFind(true);
  S.pickerBranches = null;
  setPickerQuery('');
  get('/api/picker/branches').then((m) => { S.pickerBranches = m; redraw(); }).catch(() => { S.pickerBranches = {}; redraw(); });
  readPickerWorktrees();
}

// ---- keys ----

// paletteKey finds a file in xf web, as quick open does in VS Code, and
// "#" in it an action: cmd+p on a Mac, ctrl+p elsewhere. The TUI's / can't
// be it here, where / searches the focused pane as in vim (docs/adr/0005,
// 0014).
export const onMac = /Mac|iPhone|iPad/.test(navigator.platform);
// logTabLabel names the log pane's next-tab key in footers.
export const logTabLabel = onMac ? '⌥⌘→' : 'ctrl+alt+→';
export const paletteKey = onMac ? '⌘p' : 'ctrl+p';

// appWindow is whether the page is in an app window (xf web with
// webBrowser), which a browser hands ctrl+n, ctrl+t and ctrl+w. Off a Mac
// a tab never sees ctrl+n, so ctrl+n/ctrl+p move only where ctrlNMoves.
export const appWindow = matchMedia('(display-mode: standalone)').matches;
export const ctrlNMoves = onMac || appWindow;

// openLink opens a web page in a new tab. An app window asks xf to open
// it with the system's opener, as the TUI's u does, which puts it in a tab
// of the last browser window: Chrome would open a new window for it. An
// app window is on xf's machine, as xf started it; a tab may not be.
export function openLink(url) {
  const inTab = () => window.open(url, '_blank', 'noopener');
  if (!appWindow) { inTab(); return; }
  post('/api/open-url', { url }).then((r) => { if (!r.ok) inTab(); }, inTab);
}

// paletteKeyMovesDown is whether the palette's key, pressed again in the
// open palette, moves down, as in VS Code's quick open. Off a Mac in an
// app window it doesn't: there ctrl+p moves up and ctrl+n down, as in the
// TUI. A tab off a Mac never gets ctrl+n, so there ctrl+p moves down.
export const paletteKeyMovesDown = onMac || !appWindow;

// commandsKey opens the palette on its ">" commands, as VS Code's command
// palette does: cmd+shift+p on a Mac, ctrl+shift+p elsewhere.
export const commandsKey = onMac ? '⇧⌘p' : 'ctrl+shift+p';

// saveKey saves and compiles from anywhere in the project view (see
// saveAndCompile): cmd+s on a Mac, ctrl+s elsewhere, instead of the
// browser saving the page.
export const saveKey = onMac ? '⌘s' : 'ctrl+s';
export function isSaveKey(e) {
  const mod = onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return mod && !e.altKey && !e.shiftKey && (e.key === 's' || e.key === 'S');
}

function isP(e, shift) {
  const mod = onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return mod && !e.altKey && e.shiftKey === shift && (e.key === 'p' || e.key === 'P');
}
export const isPaletteKey = (e) => isP(e, false);
export const isCommandsKey = (e) => isP(e, true);

// projectsKey shows and hides the column's projects tab: cmd+shift+w on a
// Mac, ctrl+shift+w elsewhere.
export const projectsKey = onMac ? '⇧⌘w' : 'ctrl+shift+w';

// revealKey shows the open file in the file tree, as VS Code's Show
// Explorer does: cmd+shift+e on a Mac, ctrl+shift+e elsewhere.
export const revealKey = onMac ? '⇧⌘e' : 'ctrl+shift+e';
export function isRevealKey(e) {
  const mod = onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return mod && !e.altKey && e.shiftKey && (e.key === 'e' || e.key === 'E');
}
export function isProjectsKey(e) {
  const mod = onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return mod && !e.altKey && e.shiftKey && (e.key === 'w' || e.key === 'W');
}

// keyName names a key as the TUI's bindings do: "j", "G", "ctrl+d",
// "enter", "shift+tab", "space".
export function keyName(e) {
  const named = {
    Enter: 'enter', Escape: 'esc', Tab: 'tab', Backspace: 'backspace', ArrowUp: 'up', ArrowDown: 'down',
    ArrowLeft: 'left', ArrowRight: 'right', Home: 'home', End: 'end', PageUp: 'pgup', PageDown: 'pgdown', ' ': 'space',
  };
  let k = named[e.key] || e.key;
  if (k === 'tab' && e.shiftKey) return 'shift+tab';
  if (e.ctrlKey && k.length === 1) return 'ctrl+' + k.toLowerCase();
  return k;
}

// handleKey is the TUI's handleKey: overlays first, then the focused
// pane's keys, then the global ones. It reports whether it used the key.
export function handleKey(k) {
  // ctrl+n and ctrl+p move as ↓ and ↑ do, wherever they do (the TUI's
  // Down and Up bindings).
  if (k === 'ctrl+n') k = 'down';
  if (k === 'ctrl+p') k = 'up';
  if (hooks.settingsOpen && hooks.settingsOpen()) return hooks.settingsKey(k);
  if (S.mode === 'picker') return pickerKey(k);
  if (S.mode !== 'project') return false;
  if (hooks.scratchOpen && hooks.scratchOpen() && !S.overlay) return hooks.scratchKey(k);
  // The dependency inspector has only its own few keys (inspector.js).
  if (S.inspector) return k === 'palette' || k === 'commands' || !hooks.inspectorKey ? true : hooks.inspectorKey(k);
  // The Checks page has only its own keys, and its prod prompt's.
  if (S.checks) {
    if (S.overlay === 'base') return baseKey(k);
    if (S.overlay === 'prod') {
      if (k === 'esc' || k === 'ctrl+c') { answerProd(true); return true; }
      return k === 'palette' || k === 'commands' ? true : false;
    }
    if (k !== 'palette' && k !== 'commands') checkKey(S.checks, k);
    return true;
  }
  // "palette" is paletteKey, named by main.js and the footer's chip;
  // "commands" is commandsKey, named by main.js.
  if (k === 'palette' || k === 'commands') {
    if (['confirmQuit', 'confirmClear', 'confirmFull', 'confirmRemoveWorktree', 'prod', 'sourceConflict', 'scratchNew', 'watch', 'deferAsk'].includes(S.overlay)) return true;
    if (k === 'commands' && S.overlay === 'palette') setPaletteQuery('>');
    else openPalette(k === 'commands' ? '>' : '');
    return true;
  }
  const prevFlash = S.flash;
  S.flash = '';
  if (k !== 'Y') S.copiedRel = null;

  if (k === 'ctrl+c') {
    if (S.overlay && S.overlay !== 'confirmQuit') { closeOverlay(); return true; }
    if (running()) { S.overlay = null; cancelRun(); return true; }
    return true;
  }
  switch (S.overlay) {
    // y, enter or ctrl+q closes; any other key, esc included, stays.
    case 'confirmQuit':
      S.overlay = null;
      if (k === 'y' || k === 'enter' || k === 'ctrl+q') closeProject();
      return true;
    case 'confirmClear':
      S.overlay = null;
      if (k === 'y') clearHistory(); else say('history kept');
      return true;
    // y or enter removes the worktree (ADR 0032), the dialog staying until
    // it is gone, or to say why not; any other key keeps it. While the
    // removal works, keys wait.
    case 'confirmRemoveWorktree': {
      const c = S.removeWorktree;
      if (c && c.busy) return true;
      if (c && (k === 'y' || k === 'enter')) { removeListedWorktree(c); return true; }
      S.overlay = null;
      S.removeWorktree = null;
      say('worktree kept');
      return true;
    }
    case 'confirmFull':
      S.overlay = null;
      if (k === 'y') launch(S.confirm.req); else say('full refresh cancelled');
      S.confirm = null;
      return true;
    case 'prod':
      if (k === 'esc') { answerProd(true); return true; }
      return false; // its own input and buttons
    case 'deferAsk': return deferAskKey(k);
    case 'watch': return false; // its own input
    case 'projects': pickerKey(k); return true;
    case 'changed': changedKey(k); return true;
    case 'errors': return errorsKey(k);
    case 'help': return helpKey(k);
    case 'palette': return false; // its input handles keys
    case 'editor': return editorKey(k);
    case 'python': if (hooks.pythonKey) return hooks.pythonKey(k); S.overlay = null; return true;
    case 'tags': return tagsKey(k);
    case 'branches': return branchesKey(k);
    case 'base': return baseKey(k);
    case 'scratchNew': return hooks.scratchNewKey(k);
    case 'sourceConflict': return hooks.conflictKey(k);
    case 'gitOutput': return hooks.gitOutputKey(k);
  }
  if (S.sqlSearch.typing) return false;

  if (S.focus === 'sql' && previewShown()) {
    if (hooks.markdownKey && hooks.markdownKey(k)) return true;
  } else if (columnsFocused()) {
    if (S.cols.typing) return false;
    if (columnsKey(k)) return true;
  } else if (S.focus === 'sql' && errorsTabShown()) {
    if (errorsTabKey(k)) return true;
  } else if (S.focus === 'sql') {
    if (sqlKey(k)) return true;
  } else if (S.focus === 'source') {
    if (hooks.sourceKey(k)) return true;
  } else if (S.focus === 'tree') {
    if (hooks.treeKey(k)) return true;
  } else if (S.focus === 'log') {
    if (jobsTableKey(k)) return true;
  }

  switch (k) {
    // esc closes what it can first; leaving is ctrl+q's (exit.js).
    case 'esc':
      if (S.zoomed) { S.zoomed = false; return true; }
      S.flash = prevFlash === S.flash ? '' : S.flash;
      return true;
    case '/': say(`/ searches the SQL, columns or source: ${paletteKey} then # goes to an action`); return true;
    case ']': cycleInFile(1); return true;
    case '[': cycleInFile(-1); return true;
    case 'z': S.zoomed = !S.zoomed; return true;
    case 'L': toggleLog(); return true;
    case 'ctrl+j': if (S.caps.terminal) toggleTerm(); else toggleLog(); return true;
    case '?': openHelp(); return true;
    case 'E': hooks.toggleSource(); return true;
    case 'e': showErrorsOrSay(); return true;
    case 'B': openBranches(); return true;
    // Panes are ctrl+0…3, not tab as in the TUI: tab goes round pane 2's
    // errors, sql, columns and lineage tabs (ADR 0009). The log pane's
    // are ⌥⌘←/→ (ADR 0023). Elsewhere tab does nothing, so the browser
    // doesn't move focus either.
    case 'tab': case 'shift+tab':
      if (S.focus === 'sql' || S.focus === 'lineage') nextSQLTab(k === 'tab' ? 1 : -1);
      return true;
    case 'ctrl+0': hooks.treeFocus(); return true;
    case 'ctrl+1': {
      if (!sourceShown()) { say('no source pane: E shows it'); return true; }
      // Out of the file tree or Source Control, in vim's normal mode.
      const fromTree = S.focus === 'tree';
      setFocus('source');
      if (fromTree && hooks.sourceReturn) hooks.sourceReturn();
      return true;
    }
    case 'ctrl+2': setFocus(S.focus === 'actions' ? 'sql' : 'actions'); return true;
    case 'ctrl+3': focusLog(); return true;
  }
  // Pane 2 keys only apply when focused outside pane 1 (source) and the
  // file tree.
  if (S.focus === 'source' || S.focus === 'tree') return false;
  switch (k) {
    // c switches the columns and the sql; only saveKey compiles (ADR 0017).
    case 'c': columnsOrSQL(); return true;
    case 'O': toggleLineage(); return true;
    case 'r': startRun(); return true;
    case 'R': openChanged(); return true;
    case 'T': openTags(); return true;
    case 'I': openImpact(); return true;
    case 'C': openCost(); return true;
    case 'd': toggleFlag('deps'); return true;
    case 'D': toggleFlag('dependents'); return true;
    case 'f': toggleFlag('full'); return true;
    case 'a': toggleFlag('api'); return true;
    case 's': if (S.caps.scratch) hooks.askNew(); return true;
    case 'F': toggleFollow(); return true;
    case 'o': openInEditor(); return true;
    case 'Z': say('Z shows xf full screen in neovim, which xf web isn\'t in'); return true;
    case 'i': case 'v': toggleIncremental(); return true;
  }
  switch (S.focus) {
    case 'actions': return actionsKey(k);
    case 'lineage': return lineageKey(k);
    case 'log': return logKey(k);
  }
  return false;
}

export function closeOverlay() {
  if (S.overlay === 'prod') { answerProd(true); return; }
  if (S.overlay === 'watch') { closeWatch(); return; }
  if (S.overlay === 'confirmFull') say('full refresh cancelled');
  if (S.overlay === 'deferAsk') { deferAskKey('esc'); return; }
  if (S.overlay === 'palette') { cancelPalette(); return; }
  if (S.overlay === 'base') { closeBase(); return; }
  S.overlay = null;
}

// leaveOverlay closes the open overlay as its esc does, so a conflict keeps
// your changes and a confirm says no; one whose esc only clears its box is
// closed anyway.
export function leaveOverlay() {
  if (!S.overlay || S.mode !== 'project') return;
  if (S.overlay === 'palette') { closeOverlay(); return; }
  handleKey('esc');
  if (S.overlay) closeOverlay();
}

function actionsKey(k) {
  const ref = S.view;
  switch (k) {
    case 'u': openTable(ref); return true;
    case 'y': copyTarget(ref); return true;
    case 'Y': copyPath(ref); return true;
    case 'k': case 'up': case 'ctrl+p': moveInActions(-1); return true;
    case 'j': case 'down': case 'ctrl+n': moveInActions(1); return true;
    case 'g': case 'home': moveInActions(-2); return true;
    case 'G': case 'end': moveInActions(2); return true;
    case 'enter': S.focus = 'sql'; S.zoomed = false; return true;
  }
  return false;
}

function lineageKey(k) {
  const ref = lineageSelected();
  switch (k) {
    case 'u': openTable(ref); return true;
    case 'y': copyTarget(ref); return true;
    case 'Y': copyPath(ref); return true;
    case 'k': case 'up': case 'ctrl+p': moveLineage(-1); return true;
    case 'j': case 'down': case 'ctrl+n': moveLineage(1); return true;
    case 'g': case 'home': lineageToEnd(-1); return true;
    case 'G': case 'end': lineageToEnd(1); return true;
    case 'backspace': goBack(); return true;
  }
  return false;
}

function errorsKey(k) {
  switch (k) {
    case 'esc': case 'q': case 'e': S.overlay = null; return true;
    case 'z': S.errorsZoomed = !S.errorsZoomed; return true;
    case 'y': copy(errorsText(), 'copied the errors'); return true;
    case 'j': case 'down': scrollOverlay(40); return true;
    case 'k': case 'up': scrollOverlay(-40); return true;
    case 'ctrl+d': case 'pgdown': scrollOverlay(300); return true;
    case 'ctrl+u': case 'pgup': scrollOverlay(-300); return true;
    case 'g': case 'home': scrollOverlay(-1e9); return true;
    case 'G': case 'end': scrollOverlay(1e9); return true;
  }
  return true;
}

export function errorsText() {
  const e = S.errors;
  if (!e) return '';
  const out = [];
  if (e.compileErr || e.errors.length) {
    out.push('compile errors');
    if (e.compileErr) out.push(e.tool + ' compile failed', e.compileErr);
    if (e.fix) out.push('fix: ' + e.fix.why + ': ' + e.fix.command);
    for (const x of e.errors) out.push(x.file, ...x.message.split('\n').map((l) => '  ' + l));
  }
  if (e.dryRun.length || e.auth) {
    if (out.length) out.push('');
    out.push('dry run errors');
    if (e.auth) out.push(e.auth);
    for (const x of e.dryRun) {
      out.push(x.name + (x.label ? ' (' + x.label + ')' : '') + (x.file ? ' · ' + x.file : ''));
      out.push(...x.msg.split('\n').map((l) => '  ' + l));
    }
  }
  return out.join('\n');
}

// The , and ? overlays search as vim does: / types a query, enter keeps
// it, n and N go between matching rows.
function searchable(o, k, matches, scrollTo) {
  switch (k) {
    case '/': o.typing = true; o.query = ''; o.cur = 0; return true;
    case 'n': case 'N': {
      const m = matches();
      if (!m.length) return true;
      o.cur = (o.cur + (k === 'n' ? 1 : -1) + m.length) % m.length;
      scrollTo(m[o.cur]);
      return true;
    }
    case 'esc':
      if (o.query) { o.query = ''; o.cur = 0; return true; }
      S.overlay = null;
      return true;
    case 'q': S.overlay = null; return true;
    case 'j': case 'down': scrollOverlay(40); return true;
    case 'k': case 'up': scrollOverlay(-40); return true;
    case 'ctrl+d': case 'pgdown': scrollOverlay(300); return true;
    case 'ctrl+u': case 'pgup': scrollOverlay(-300); return true;
    case 'g': case 'home': scrollOverlay(-1e9); return true;
    case 'G': case 'end': scrollOverlay(1e9); return true;
  }
  return true;
}

export function scrollToMatch(i) {
  let el = document.querySelector(`.overlay [data-match="${i}"]`);
  // A row laid out with display: contents (the , overlay's) has no box
  // to scroll to: its first cell has.
  if (el && !el.getClientRects().length) el = el.firstElementChild;
  if (el) el.scrollIntoView({ block: 'center' });
}

function helpKey(k) {
  if (S.help.typing) return false;
  if (k === '?') { S.overlay = null; return true; }
  return searchable(S.help, k, helpMatches, (i) => scrollToMatch(i));
}

export let helpRowsCache = [];
export function helpMatches() {
  const q = S.help.query.trim().toLowerCase();
  if (!q) return [];
  const out = [];
  helpRowsCache.forEach((r, i) => { if ((r.title || (r.key + ' ' + r.desc)).toLowerCase().includes(q)) out.push(i); });
  return out;
}
export function setHelpRows(rows) { helpRowsCache = rows; }

function tagsKey(k) {
  const t = S.tags;
  if (!t.options) return false; // the query has the keys
  switch (k) {
    case 'esc': case 'q': S.overlay = null; return true;
    case '/': t.options = false; return true;
    case 'd': S.flags.deps = !S.flags.deps; return true;
    case 'D': S.flags.dependents = !S.flags.dependents; return true;
    case 'f': S.flags.full = !S.flags.full; return true;
    case 'a': if (S.caps.api) S.flags.api = !S.flags.api; else say('API runs are not available'); return true;
    case 'space': toggleTag(); return true;
    case 'enter': runTags(); return true;
    case 'j': case 'down': t.cursor = Math.min(t.cursor + 1, Math.max(t.list.length - 1, 0)); return true;
    case 'k': case 'up': t.cursor = Math.max(t.cursor - 1, 0); return true;
  }
  return true;
}

// ---- pane sizes ----

// The sizes panes were dragged to are this browser's, kept per project.
const sizesKey = () => 'xf-web:sizes:' + (S.boot ? S.boot.root : '');
let sizes = null;

export function paneSize(id) {
  if (!sizes) {
    try { sizes = JSON.parse(localStorage.getItem(sizesKey()) || '{}'); } catch { sizes = {}; }
  }
  return sizes[id] || 0;
}

export function setPaneSize(id, h) {
  paneSize(id);
  if (h) sizes[id] = h; else delete sizes[id];
  try { localStorage.setItem(sizesKey(), JSON.stringify(sizes)); } catch { /* sizes last this page only */ }
}

// ---- mouse helpers the panes use ----

export function clickAction(id) {
  setFocus('actions');
  if (id !== S.sel) selectInPane(id);
  redraw();
}

export function clickLineage(i, jump) {
  setFocus('lineage');
  S.lineageCursor = i;
  if (jump) jumpTo(lineageSelected());
  redraw();
}

export { vim, toggleColumns, toggleIncremental, toggleFlag, startRun, toggleLineage, focusLog, nextTab, openKept, compile, openTable, copyTarget, copyPath, openInEditor, showErrorsOrSay, openPalette as palette };

// What other modules take from here, now kept in modules of their own.
export { closeBase, fetchBases, fetchBranches, moveBase, moveBranches, newBranchName, openBase, openBranches, openNewWorktree, openWorktree, pickBase, setBaseQuery, setBranchesQuery, switchBranch } from './branches.js';
export { answerDefer, deferOn, toggleDefer } from './checks.js';
export { answerProd, changedCursorRows, changedListed, changedPreview, checkStale, checksKey, checksTabs, closeChecks, openChanged, openCost, openImpact, openTags, requestChanged, runTags, scrollOverlay, setTagsQuery, tagsRun, toggleTag } from './checks.js';
export { actOnErrorRow, errorGroups, errorKinds, errorRows, errorsTabShown, pickSQLTab, runFix, runLogFix, showErrorsTab, sqlTabOn } from './errors-tab.js';
export { clearHistory, closeWatch, JOB_COLS, jobsRows, jobsShown, logTabs, nextJobsSort, scrollLog, setJobsQuery, shownJobs, sortJobsBy, submitWatch } from './log-tabs.js';
export { addFound, leaveFind, pickProject, pickerKey, setPickerQuery, toggleFound } from './picker.js';
export { endSQLSearch, setSQLSearchQuery } from './sql-pane.js';
