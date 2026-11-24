// The Scratch view's state and what changes it: the scratch files, the
// dry run of the SQL being edited, and the query job with its pages of
// rows. The view itself is scratch-view.js; the editor is scratch-editor.js.

import { S, redraw, get, post, copy, hooks, fader, onMac, openLink, openProjects, openWarehousePicker, openProfilePicker, openScratchWarehouse, askQuit, projectURL, scratchPopoutURL } from './state.js';
import { openThemePicker, themeLabel } from './theme.js';
import { part } from './workspace.js';
import { vimOn } from './vim-mode.js';

export const GiB = 1024 ** 3;
const ROW_H = 22;
export { ROW_H };

const sizeKey = 'xf-web:scratch:size';
function loadSize() {
  const z = { list: 0, editor: 0, detail: 0 };
  try {
    const v = JSON.parse(localStorage.getItem(sizeKey) || '{}');
    for (const k of Object.keys(z)) if (v[k] > 0) z[k] = v[k];
  } catch { /* no storage */ }
  return z;
}

const scSize = loadSize();
let tabSeq = 0;

function makeTab(cur = null) {
  return {
    key: 't' + (++tabSeq),
    cur,               // the scratch being edited: { id, draft?, from, project, location, sql }
    savedSql: cur ? (cur.sql || '') : '',
    dry: null,         // the dry run of the scope: { sql, pending, ... }
    dryPromise: null,
    drySeq: 0,
    dryTimer: 0,
    run: null,         // the query run: see newRun
    confirm: null,     // what the run or close is asked to confirm: { sql, why: [...], action?, tabKey? }
    sel: null,         // the selected cell: { r, c }
    scrollTop: 0,
    filters: {},       // the results' column filters: column index -> text
    sort: null,        // the results' sort: { c: column index, desc }
    hideResults: false, // cmd+j hides the results as a terminal panel hides
    editor: null,      // saved CodeMirror state while another tab is shown
    editorScroll: 0,
    saveTimer: 0,
    saveSeq: 0,
    labelLine: cur ? firstLine(cur.sql || '') : '',
    closed: false,
    size: scSize,
  };
}

export const SC = {
  open: false,
  list: [],
  tabs: [],          // open scratch tabs (makeTab)
  activeTab: '',     // key of the active tab in tabs
  listCursor: 0,
  delAsk: '',        // the scratch d asked to delete
  focus: 'results',  // list, editor, results
  newForm: null,     // { busy, error } while the New scratch box is up
  flash: '',
  listOpen: false,     // the scratch selector, cmd+b
  defaults: null,      // the project and location a scratch with no model runs in
  standalone: false,   // no project behind the page: Scratch is all of it
  popout: false,       // separate Scratch window opened beside the project window
  dbx: null,           // standalone Databricks connection: { profile, host, warehouse, hasProfiles, needsProfile }
  projects: null,      // BigQuery projects or Databricks catalogs for the target box's datalist
  whOpen: false,       // standalone warehouse switcher menu
  whCursor: 0,
  recent: [],          // the last runs that returned rows, newest first (scratchruns.go)
  recentOpen: '',      // set while the recent results menu is up: where the keys go after ('editor', 'results')
  recentCursor: 0,
  size: scSize,        // the panels as dragged, in px (0: its own size): list width, editor height, detail width
};

// activeTab is the tab shown now, creating one when none is open yet so
// direct writes to SC.cur / SC.run / … always land on a tab.
export function activeTab() {
  let t = SC.tabs.find((x) => x.key === SC.activeTab);
  if (!t) {
    if (!SC.tabs.length) SC.tabs.push(makeTab(null));
    t = SC.tabs[0];
    SC.activeTab = t.key;
  }
  return t;
}

// Per-tab fields stay directly reachable on SC for the active tab.
for (const prop of ['cur', 'dry', 'dryPromise', 'run', 'confirm', 'sel', 'scrollTop', 'filters', 'sort', 'hideResults']) {
  Object.defineProperty(SC, prop, {
    enumerable: true,
    configurable: true,
    get: () => activeTab()[prop],
    set: (v) => { activeTab()[prop] = v; },
  });
}

S.scratch = SC;

// resetTabs clears open tabs back to one empty slot (for tests).
export function resetTabs() {
  for (const t of SC.tabs) {
    clearTimeout(t.dryTimer);
    clearTimeout(t.saveTimer);
    t.closed = true;
  }
  SC.tabs = [];
  SC.activeTab = '';
}

// setSize keeps a panel's dragged size, 0 to give it its own again.
export function setSize(which, px) {
  SC.size[which] = px;
  try { localStorage.setItem(sizeKey, JSON.stringify(SC.size)); } catch { /* no storage */ }
}

let ed = null; // the editor: { doc(), scope(), block(), set(text), focus(), blur() }
export function setEditor(e) { ed = e; }
export function editor() { return ed; }

function isEmptyDraft(tab) {
  if (!tab || !tab.cur) return true;
  if (!tab.cur.draft) return false;
  const sql = tab === activeTab() && ed && ed.doc ? ed.doc() : (tab.cur.sql || '');
  return !sql.trim() && !tab.run;
}

function rawTabLabel(tab) {
  const cur = tab && tab.cur;
  if (!cur) return 'scratch';
  if (cur.from) return cur.from;
  const sql = (tab === activeTab() && ed && ed.doc ? ed.doc() : cur.sql) || '';
  const line = firstLine(sql);
  if (line) return line.length > 28 ? line.slice(0, 28) + '…' : line;
  return cur.id || 'new scratch';
}

// tabLabel is tab's name in the tab bar, numbered where two share one.
export function tabLabel(tab) {
  const base = rawTabLabel(tab);
  if (SC.tabs.filter((t) => rawTabLabel(t) === base).length < 2) return base;
  return base + ' ' + (SC.tabs.indexOf(tab) + 1);
}

// canCloseTab says whether closing tab would do anything (the sole untouched
// empty draft stays put).
export function canCloseTab(tab = activeTab()) {
  return SC.tabs.length > 1 || !isEmptyDraft(tab);
}

function captureEditor(tab) {
  if (!tab || !ed) return;
  if (tab.cur && ed.doc) {
    const sql = ed.doc();
    tab.cur.sql = sql;
    tab.labelLine = firstLine(sql);
  }
  if (ed.save) tab.editor = ed.save(false);
  if (ed.view && ed.view.scrollDOM) tab.editorScroll = ed.view.scrollDOM.scrollTop;
}

function applyEditor(tab) {
  if (!ed) return;
  const sql = (tab && tab.cur && tab.cur.sql) || '';
  if (tab && tab.editor && ed.restore && tab.editor.doc === sql) {
    ed.restore(tab.editor);
    if (ed.view && ed.view.scrollDOM) ed.view.scrollDOM.scrollTop = tab.editorScroll || 0;
  } else if (ed.set) {
    ed.set(sql);
  }
  const scope = ed.scope ? ed.scope() : sql;
  if (tab && tab.dry && !tab.dry.ok && tab.dry.error && scope === tab.dry.sql) {
    markError(tab.dry.error, tab.dry.sql, 0);
  } else if (tab && tab.run && tab.run.phase === 'failed' && tab.run.error && scope === tab.run.sql) {
    markError(tab.run.error, tab.run.sql, tab.run.base);
  } else if (ed.mark) {
    ed.mark(null);
  }
}

// restoreActiveEditor loads the active tab into a freshly mounted editor.
export function restoreActiveEditor() {
  const tab = activeTab();
  if (tab.cur) applyEditor(tab);
  scheduleDry(0, tab);
}

let fadeFlash = null;
function say(msg) {
  SC.flash = msg || '';
  fadeFlash ||= fader(() => SC.flash, () => { SC.flash = ''; });
  fadeFlash(SC.flash);
  redraw();
}
export { say as scratchSay };

// ---- the scratch files ----

export async function loadList() {
  try {
    SC.list = await get('/api/scratch');
  } catch (e) { say(e.message); }
  redraw();
}

async function loadDefaults() {
  try { SC.defaults = await get('/api/scratch/defaults'); } catch { SC.defaults = SC.defaults || { project: '', location: '' }; }
}

// openScratchView shows the Scratch view on a scratch, the one open, or
// the newest. With draft it opens a new empty tab, whose file is made
// when it is first typed in.
let openSeq = 0;
export async function openScratchView(id, opts = {}) {
  if (!S.caps.scratch) { S.flash = 'scratch queries need BigQuery access, which this project lacks'; redraw(); return; }
  const seq = ++openSeq;
  SC.open = true;
  SC.focus = 'results';
  await Promise.all([loadList(), loadDefaults(), loadRecent()]);
  if (seq !== openSeq) return;
  if (opts.draft) {
    if (SC.tabs.length === 1 && isEmptyDraft(SC.tabs[0]) && SC.tabs[0].cur) {
      SC.activeTab = SC.tabs[0].key;
      focusEditor();
      redraw();
      return;
    }
    await newDraft();
    return;
  }
  if (id) {
    await selectScratch(id, true);
    redraw();
    return;
  }
  const cur = SC.tabs.length ? activeTab() : null;
  if (cur && cur.cur) {
    if (!cur.cur.draft && cur.cur.id) await selectScratch(cur.cur.id, true);
    redraw();
    return;
  }
  const want = SC.list[0] && SC.list[0].id;
  if (want) await selectScratch(want, true);
  else await newDraft();
  redraw();
}

// openLastScratch is the palette's "go to last query": the scratch open
// before, else the one edited last, with the keys in its editor. With no
// scratch yet it opens a new empty one.
export async function openLastScratch() {
  await openScratchView();
  if (!SC.open) return;
  if (!SC.cur) { await newDraft(); return; }
  // The editor mounts as the view draws.
  SC.focus = 'editor';
  redraw();
  let tries = 0;
  const put = () => {
    if (!ed) { if (++tries < 40) setTimeout(put, 25); return; }
    if (SC.focus === 'editor') ed.focus();
  };
  setTimeout(put, 0);
}

// switchTab activates an open tab by key, keeping the outgoing tab's
// editor state and starting its save in the background.
export function switchTab(key) {
  const next = SC.tabs.find((t) => t.key === key);
  if (!next) return false;
  const prev = activeTab();
  if (next === prev) return false;
  SC.delAsk = '';
  SC.recentOpen = '';
  captureEditor(prev);
  flushSave(prev);
  SC.activeTab = next.key;
  if (next.cur && next.cur.id) {
    SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === next.cur.id));
  }
  applyEditor(next);
  if (!next.dry || next.dry.stale) scheduleDry(0, next);
  redraw();
  return true;
}

// nextScratchTab steps to the next (dir 1) or previous (dir -1) open tab.
export function nextScratchTab(dir = 1) {
  if (SC.tabs.length < 2) return false;
  const i = SC.tabs.indexOf(activeTab());
  const next = SC.tabs[(i + dir + SC.tabs.length) % SC.tabs.length];
  return switchTab(next.key);
}

// switchTabIndex activates the 0-based tab index if present.
export function switchTabIndex(i) {
  if (i < 0 || i >= SC.tabs.length) return false;
  return switchTab(SC.tabs[i].key);
}

// closeTab closes the given tab (default: active). If a query is running
// in it, asks before cancelling unless force is true. Closing the last
// tab leaves a fresh empty draft.
export async function closeTab(key = SC.activeTab, force = false) {
  const idx = SC.tabs.findIndex((t) => t.key === key);
  if (idx < 0) return false;
  const tab = SC.tabs[idx];
  if (!force && isRunning(tab)) {
    if (tab !== activeTab()) switchTab(tab.key);
    tab.confirm = { action: 'closeTab', tabKey: tab.key, why: ['A query is running in this tab. Closing it cancels the query.'], danger: true };
    if (ed) ed.blur();
    SC.focus = 'results';
    redraw();
    return false;
  }
  if (isRunning(tab)) await cancelRun(tab);
  const wasActive = tab === activeTab();
  if (wasActive) {
    captureEditor(tab);
    await flushSave(tab);
  }
  clearTimeout(tab.dryTimer);
  clearTimeout(tab.saveTimer);
  tab.closed = true;
  if (SC.tabs.length === 1) {
    if (isEmptyDraft(tab)) { tab.closed = false; return false; }
    if (!SC.defaults) await loadDefaults();
    const fresh = makeTab({ id: '', draft: true, from: '', project: SC.defaults.project, location: SC.defaults.location, sql: '' });
    SC.tabs = [fresh];
    SC.activeTab = fresh.key;
    applyEditor(fresh);
    redraw();
    return true;
  }
  SC.tabs.splice(idx, 1);
  if (wasActive) {
    const next = SC.tabs[Math.min(idx, SC.tabs.length - 1)];
    SC.activeTab = next.key;
    if (next.cur && next.cur.id) {
      SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === next.cur.id));
    }
    applyEditor(next);
    if (!next.dry || next.dry.stale) scheduleDry(0, next);
  }
  redraw();
  return true;
}

// closeOtherTabs closes every tab except keepKey, asking first if any of
// them has a running query.
export async function closeOtherTabs(keepKey = SC.activeTab, force = false) {
  const others = SC.tabs.filter((t) => t.key !== keepKey);
  if (!others.length) return false;
  const runningOthers = others.filter((t) => isRunning(t));
  if (!force && runningOthers.length) {
    const n = runningOthers.length;
    activeTab().confirm = {
      action: 'closeOtherTabs',
      tabKey: keepKey,
      why: [`${n === 1 ? 'A query is' : n + ' queries are'} running in other tabs. Closing them cancels ${n === 1 ? 'it' : 'them'}.`],
      danger: true,
    };
    if (ed) ed.blur();
    SC.focus = 'results';
    redraw();
    return false;
  }
  for (const t of others) {
    if (isRunning(t)) cancelRun(t);
    clearTimeout(t.dryTimer);
    clearTimeout(t.saveTimer);
    t.closed = true;
  }
  const keep = SC.tabs.find((t) => t.key === keepKey) || SC.tabs[0];
  SC.tabs = keep ? [keep] : [];
  if (keep) SC.activeTab = keep.key;
  redraw();
  return true;
}

// selectScratch opens a scratch in a tab. If it is already open in a tab,
// switches to that tab (reading it again from disk only when force says
// to, keeping its result). Otherwise opens it in a new tab — or reuses an
// untouched empty draft, or replaces the current tab when opts.replace is
// true (j/k list browsing) and the current tab is not running a query.
export async function selectScratch(id, force, show, opts = {}) {
  const existing = SC.tabs.find((t) => t.cur && t.cur.id === id);
  if (existing) {
    const wasActive = existing === activeTab();
    if (!wasActive) switchTab(existing.key);
    if (show && (!existing.run || !existing.run.job || existing.run.job.id !== show.job.id)) {
      showRecent(show, existing);
    }
    if (!force) { redraw(); return; }
    await flushSave(existing);
    try {
      const f = await get('/api/scratch/file?id=' + encodeURIComponent(id));
      const changed = !existing.cur || existing.cur.sql !== f.sql;
      existing.cur = f;
      existing.savedSql = f.sql;
      existing.labelLine = firstLine(f.sql);
      SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === id));
      if (changed) {
        existing.editor = null;
        existing.dry = null;
        if (existing === activeTab()) applyEditor(existing);
      }
      if (!existing.run) {
        const saved = show || SC.recent.find((r) => r.scratch === id);
        if (saved) showRecent(saved, existing);
      }
      if (existing === activeTab()) scheduleDry(0, existing);
    } catch (e) { say(e.message); }
    redraw();
    return;
  }
  const prev = SC.tabs.length ? activeTab() : null;
  if (prev) {
    captureEditor(prev);
    await flushSave(prev);
  }
  try {
    const f = await get('/api/scratch/file?id=' + encodeURIComponent(id));
    let target;
    const reuse = prev && (!prev.cur || isEmptyDraft(prev) || (opts.replace && !isRunning(prev)));
    if (reuse) {
      target = prev;
      clearTimeout(target.dryTimer);
      target.cur = f;
      target.savedSql = f.sql;
      target.labelLine = firstLine(f.sql);
      target.editor = null;
      target.editorScroll = 0;
      target.dry = null;
      target.run = null;
      target.confirm = null;
      target.sel = null;
      target.scrollTop = 0;
      target.filters = {};
      target.sort = null;
      target.hideResults = false;
    } else {
      target = makeTab(f);
      SC.tabs.push(target);
      SC.activeTab = target.key;
    }
    SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === id));
    const saved = show || SC.recent.find((r) => r.scratch === id);
    if (saved) showRecent(saved, target);
    applyEditor(target);
    scheduleDry(0, target);
  } catch (e) { say(e.message); }
  redraw();
}

export async function closeScratchView() {
  if (SC.standalone) return; // there is no project page to go back to
  if (SC.popout) { await closePopout(); return; }
  const tab = SC.tabs.length ? activeTab() : null;
  if (tab) captureEditor(tab);
  await flushAllSaves();
  SC.open = false;
  if (tab) tab.confirm = null;
  if (ed) ed.blur();
  redraw();
}

// ---- pop-out windows and docking ----

const handoffPrefix = 'xf-web:scratch-handoff:';
const handoffTTL = 60_000;
const MAX_HANDOFF_RUN_BYTES = 1_000_000;

function serializeRun(run) {
  if (!run) return null;
  if (run.phase === 'starting' || run.phase === 'running' || run.phase === 'loading') {
    if (!run.job) return null;
    return {
      sql: run.sql, phase: 'running', job: run.job, started: run.started,
      scratch: run.scratch || '', base: run.base || 0, record: !!run.record,
      restored: run.restored || 0, tab: run.tab || 'rows',
    };
  }
  if (run.phase === 'done') {
    const out = {
      sql: run.sql, phase: 'done', job: run.job, status: run.status,
      started: run.started, ended: run.ended, restored: run.restored || 0,
      scratch: run.scratch || '', base: run.base || 0,
      schema: run.schema, rows: run.rows, next: run.next, total: run.total,
      children: run.children, child: run.child, rowsJob: run.rowsJob, tab: run.tab || 'rows',
    };
    try {
      if (JSON.stringify(out).length > MAX_HANDOFF_RUN_BYTES && run.job) {
        return {
          sql: run.sql, phase: 'running', job: run.job, started: run.started,
          restored: run.ended || run.started, scratch: run.scratch || '',
          base: run.base || 0, tab: run.tab || 'rows',
        };
      }
    } catch { /* keep out */ }
    return out;
  }
  if (run.phase === 'failed' || run.phase === 'cancelled') {
    return {
      sql: run.sql, phase: run.phase, job: run.job, status: run.status,
      started: run.started, ended: run.ended, error: run.error,
      scratch: run.scratch || '', base: run.base || 0, tab: run.tab || 'rows',
    };
  }
  return null;
}

function serializeTabForHandoff(t, light = false) {
  const cur = t.cur;
  if (!cur) return null;
  const run = serializeRun(t.run);
  return {
    id: cur.id || '',
    draft: !!cur.draft,
    from: cur.from || '',
    project: cur.project || '',
    location: cur.location || '',
    sql: cur.sql || '',
    savedSql: t.savedSql || '',
    hideResults: !!t.hideResults,
    filters: t.filters || {},
    sort: t.sort || null,
    sel: t.sel || null,
    scrollTop: t.scrollTop || 0,
    editor: light ? null : (t.editor || null),
    editorScroll: t.editorScroll || 0,
    run: light && run && run.phase === 'done' && run.job
      ? { sql: run.sql, phase: 'running', job: run.job, started: run.started, restored: run.ended || run.started, scratch: run.scratch, base: run.base, tab: run.tab }
      : run,
  };
}

function snapshotTabsForHandoff(tabs, activeKey, light = false) {
  const list = tabs.filter((t) => t && t.cur);
  const activeIdx = Math.max(0, list.findIndex((t) => t.key === activeKey));
  return {
    at: Date.now(),
    project: S.key,
    listOpen: SC.listOpen,
    focus: SC.focus,
    activeIndex: activeIdx,
    tabs: list.map((t) => serializeTabForHandoff(t, light)).filter(Boolean),
  };
}

function pruneHandoffs() {
  try {
    const cutoff = Date.now() - handoffTTL;
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (!k || !k.startsWith(handoffPrefix)) continue;
      let stale = true;
      try { const v = JSON.parse(localStorage.getItem(k) || 'null'); stale = !v || (v.at || 0) < cutoff; } catch { stale = true; }
      if (stale) localStorage.removeItem(k);
    }
  } catch { /* no storage */ }
}

function writeHandoff(payload, fallbackPayload) {
  pruneHandoffs();
  const token = Math.random().toString(36).slice(2) + Date.now().toString(36);
  try {
    localStorage.setItem(handoffPrefix + token, JSON.stringify(payload));
    return token;
  } catch {
    if (fallbackPayload) {
      try { localStorage.setItem(handoffPrefix + token, JSON.stringify(fallbackPayload)); return token; } catch { /* ignore */ }
    }
    return '';
  }
}

function takeHandoff(token) {
  if (!token) return null;
  try {
    const raw = localStorage.getItem(handoffPrefix + token);
    localStorage.removeItem(handoffPrefix + token);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

// popoutFeatures builds the window.open feature string for a popped-out Scratch
// window: placed at dropPos when dragged outside the current window (preserving
// multi-monitor coordinates), or side-by-side beside the current window otherwise.
export function popoutFeatures(dropPos, env = {}) {
  const scr = env.screen || (typeof screen !== 'undefined' ? screen : {});
  const win = env.window || (typeof window !== 'undefined' ? window : {});
  const availLeft = Number.isFinite(scr.availLeft) ? scr.availLeft : 0;
  const availTop = Number.isFinite(scr.availTop) ? scr.availTop : 0;
  const availW = scr.availWidth || 1440;
  const availH = scr.availHeight || 900;
  const winX = Number.isFinite(win.screenX) ? win.screenX : availLeft;
  const winY = Number.isFinite(win.screenY) ? win.screenY : availTop;
  const winW = win.outerWidth || availW;
  const winH = win.outerHeight || Math.round(availH * 0.85);
  const width = Math.min(Math.max(Math.round(availW * 0.48), 640), Math.min(availW, 1100));
  const height = Math.min(Math.max(winH, 560), availH);

  let left, top;
  const outside = dropPos && Number.isFinite(dropPos.screenX) && Number.isFinite(dropPos.screenY) && (
    dropPos.screenX < winX || dropPos.screenX > winX + winW ||
    dropPos.screenY < winY || dropPos.screenY > winY + winH
  );
  if (outside) {
    left = Math.round(dropPos.screenX - width / 2);
    top = Math.round(dropPos.screenY - 24);
    const onThisScreen = dropPos.screenX >= availLeft && dropPos.screenX <= availLeft + availW &&
      dropPos.screenY >= availTop && dropPos.screenY <= availTop + availH;
    if (onThisScreen) {
      left = Math.min(Math.max(left, availLeft), availLeft + Math.max(availW - width, 0));
      top = Math.min(Math.max(top, availTop), availTop + Math.max(availH - height, 0));
    }
  } else {
    top = Math.min(Math.max(winY, availTop), availTop + Math.max(availH - height, 0));
    if (winX + winW + 420 <= availLeft + availW) {
      left = winX + winW + 8;
    } else if (winX - availLeft >= 420) {
      left = availLeft;
    } else {
      left = availLeft + Math.max(availW - width, 0);
    }
  }
  return `popup,width=${width},height=${height},left=${left},top=${top}`;
}

function hydrateTab(st) {
  const cur = {
    id: st.id || '',
    draft: !!st.draft,
    from: st.from || '',
    project: st.project || (SC.defaults && SC.defaults.project) || '',
    location: st.location || (SC.defaults && SC.defaults.location) || '',
    sql: st.sql || '',
  };
  const t = makeTab(cur);
  t.savedSql = st.savedSql !== undefined ? st.savedSql : cur.sql;
  t.hideResults = !!st.hideResults;
  t.filters = st.filters || {};
  t.sort = st.sort || null;
  t.sel = st.sel || null;
  t.scrollTop = st.scrollTop || 0;
  t.editorScroll = st.editorScroll || 0;
  if (st.editor && st.editor.doc === cur.sql) t.editor = st.editor;
  if (st.run && st.run.sql) {
    t.run = {
      sql: st.run.sql,
      phase: st.run.phase || 'done',
      job: st.run.job || null,
      status: st.run.status || null,
      started: st.run.started || Date.now(),
      ended: st.run.ended || 0,
      restored: st.run.restored || 0,
      error: st.run.error || '',
      schema: st.run.schema || null,
      rows: st.run.rows || [],
      next: st.run.next || '',
      total: st.run.total || 0,
      loadingMore: false,
      children: st.run.children || [],
      child: st.run.child || '',
      rowsJob: st.run.rowsJob || null,
      scratch: st.run.scratch || cur.id || '',
      base: st.run.base || 0,
      record: !!st.run.record,
      tab: st.run.tab || 'rows',
      seq: ++runSeq,
    };
    ctxOf.set(t.run, t);
  }
  return t;
}

// popoutScratch moves the Scratch view (or one tab via opts.tabKey) into a
// separate browser window so it can sit side-by-side with the project.
// Called synchronously from a click/pointerup/command so window.open keeps
// browser user activation.
export function popoutScratch(opts = {}) {
  if (!S.caps.scratch || !S.key) {
    S.flash = 'scratch queries need BigQuery access, which this project lacks';
    redraw();
    return false;
  }
  if (opts.fresh) {
    const win = window.open(scratchPopoutURL(S.key), '_blank', popoutFeatures(opts.dropPos));
    if (!win) { S.flash = 'the browser blocked the new window: allow pop-ups for xf web'; redraw(); return false; }
    try { win.opener = null; } catch { /* cross-context */ }
    return true;
  }
  const act = SC.tabs.length ? activeTab() : null;
  if (act) {
    captureEditor(act);
    flushSave(act);
  }
  let moving = [];
  let movingAll = true;
  if (opts.tabKey) {
    const tab = SC.tabs.find((t) => t.key === opts.tabKey);
    if (!tab || !tab.cur) return false;
    if (SC.tabs.length <= 1 && (SC.popout || SC.standalone)) return false;
    moving = [tab];
    movingAll = SC.tabs.length <= 1;
  } else {
    if (SC.standalone || SC.popout) return false;
    moving = SC.tabs.filter((t) => t.cur);
    movingAll = true;
  }
  const activeKey = opts.tabKey || SC.activeTab;
  const token = moving.length
    ? writeHandoff(snapshotTabsForHandoff(moving, activeKey, false), snapshotTabsForHandoff(moving, activeKey, true))
    : '';
  const win = window.open(scratchPopoutURL(S.key, token), '_blank', popoutFeatures(opts.dropPos));
  if (!win) {
    if (token) takeHandoff(token);
    say('the browser blocked the new window: allow pop-ups for xf web');
    return false;
  }
  try { win.opener = null; } catch { /* cross-context */ }

  if (movingAll) {
    for (const t of SC.tabs) {
      clearTimeout(t.dryTimer);
      clearTimeout(t.saveTimer);
      t.closed = true;
    }
    SC.tabs = [];
    SC.activeTab = '';
    SC.open = false;
    if (ed) ed.blur();
    redraw();
    return true;
  }
  const tab = moving[0];
  const idx = SC.tabs.indexOf(tab);
  const wasActive = tab === activeTab();
  clearTimeout(tab.dryTimer);
  clearTimeout(tab.saveTimer);
  tab.closed = true;
  SC.tabs.splice(idx, 1);
  if (wasActive && SC.tabs.length) {
    const next = SC.tabs[Math.min(idx, SC.tabs.length - 1)];
    SC.activeTab = next.key;
    if (next.cur && next.cur.id) SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === next.cur.id));
    applyEditor(next);
    if (!next.dry || next.dry.stale) scheduleDry(0, next);
  }
  redraw();
  return true;
}

// openScratchPopout boots a popped-out Scratch window from a one-time handoff
// token, its own saved workspace on reload, or a fresh Scratch view.
export async function openScratchPopout(handoffToken, savedWorkspace) {
  if (!S.caps.scratch) return;
  SC.popout = true;
  SC.standalone = false;
  const data = handoffToken ? takeHandoff(handoffToken) : null;
  if (data && Array.isArray(data.tabs) && data.tabs.length) {
    const seq = ++openSeq;
    SC.open = true;
    SC.focus = data.focus || 'editor';
    SC.listOpen = !!data.listOpen;
    await Promise.all([loadList(), loadDefaults(), loadRecent()]);
    if (seq !== openSeq) return;
    const built = data.tabs.map(hydrateTab);
    resetTabs();
    SC.tabs = built;
    const activeIdx = Math.min(Math.max(data.activeIndex || 0, 0), built.length - 1);
    const act = built[activeIdx];
    SC.activeTab = act.key;
    if (act.cur && act.cur.id) SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === act.cur.id));
    for (const t of built) {
      if (t.run && t.run.phase === 'running' && t.run.job) poll(t.run);
      else if (!t.run && t.cur.id) {
        const recent = SC.recent.find((r) => r.scratch === t.cur.id);
        if (recent) showRecent(recent, t);
      }
      if (t.cur.sql !== t.savedSql) flushSave(t);
    }
    redraw();
    let tries = 0;
    const put = () => {
      if (!ed) { if (++tries < 40) setTimeout(put, 25); return; }
      if (activeTab() === act) {
        applyEditor(act);
        scheduleDry(0, act);
        if (SC.focus === 'editor') ed.focus();
      }
    };
    put();
    return;
  }
  if (savedWorkspace) {
    await restoreScratchWorkspace(savedWorkspace);
    return;
  }
  await openScratchView();
  focusEditor();
}

// importDockedTabs receives tabs sent back from a popped-out Scratch window
// over BroadcastChannel and opens them in this window's Scratch view.
export async function importDockedTabs(handoffToken) {
  const data = takeHandoff(handoffToken);
  if (!data || !Array.isArray(data.tabs) || !data.tabs.length) return;
  const wasOpen = SC.open;
  SC.open = true;
  if (!wasOpen) await Promise.all([loadList(), loadDefaults(), loadRecent()]);
  const incoming = data.tabs.map(hydrateTab);
  // Drop a lone empty placeholder slot before merging docked tabs.
  if (SC.tabs.length === 1 && (!SC.tabs[0].cur || isEmptyDraft(SC.tabs[0]))) {
    SC.tabs = [];
    SC.activeTab = '';
  }
  let targetActive = null;
  incoming.forEach((t, i) => {
    const dup = t.cur.id ? SC.tabs.find((x) => x.cur && x.cur.id === t.cur.id) : null;
    if (dup) {
      dup.cur = t.cur;
      dup.savedSql = t.savedSql;
      dup.editor = t.editor;
      dup.editorScroll = t.editorScroll;
      dup.filters = t.filters;
      dup.sort = t.sort;
      dup.sel = t.sel;
      dup.scrollTop = t.scrollTop;
      dup.hideResults = t.hideResults;
      if (t.run) { dup.run = t.run; ctxOf.set(dup.run, dup); }
      if (i === (data.activeIndex || 0)) targetActive = dup;
    } else {
      SC.tabs.push(t);
      if (i === (data.activeIndex || 0)) targetActive = t;
    }
    const liveTab = dup || t;
    if (liveTab.run && liveTab.run.phase === 'running' && liveTab.run.job) poll(liveTab.run);
  });
  if (targetActive) {
    SC.activeTab = targetActive.key;
    if (targetActive.cur && targetActive.cur.id) SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === targetActive.cur.id));
    applyEditor(targetActive);
    scheduleDry(0, targetActive);
  }
  redraw();
}

// dockScratch moves a popped-out Scratch window's tabs back into an open
// project window and closes the pop-out, or switches to the project view here
// if no other window has the project open.
export async function dockScratch() {
  if (!SC.popout) return;
  const act = SC.tabs.length ? activeTab() : null;
  if (act) {
    captureEditor(act);
    flushSave(act);
  }
  const moving = SC.tabs.filter((t) => t.cur);
  if (moving.length && hooks.dockScratchToPeer && S.key) {
    const token = writeHandoff(snapshotTabsForHandoff(moving, SC.activeTab, false), snapshotTabsForHandoff(moving, SC.activeTab, true));
    if (token) {
      const ok = await hooks.dockScratchToPeer(S.key, token);
      if (ok) {
        for (const t of SC.tabs) { clearTimeout(t.dryTimer); clearTimeout(t.saveTimer); t.closed = true; }
        SC.tabs = [];
        SC.activeTab = '';
        window.close();
        return;
      }
      takeHandoff(token); // clean up unclaimed handoff
    }
  }
  SC.popout = false;
  if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
  await closeScratchView();
}

// closePopout closes a popped-out Scratch window without closing the project in
// other windows, asking first if any scratch tab has a query running.
export async function closePopout(force = false) {
  if (!SC.popout) return false;
  if (!force && anyRunning()) {
    activeTab().confirm = {
      action: 'closePopout',
      why: ['A query is running in this Scratch window. Closing it cancels the query.'],
      danger: true,
    };
    if (ed) ed.blur();
    SC.focus = 'results';
    redraw();
    return false;
  }
  if (anyRunning()) await cancelAllRuns();
  const act = SC.tabs.length ? activeTab() : null;
  if (act) captureEditor(act);
  await flushAllSaves();
  if (act) act.confirm = null;
  for (const t of SC.tabs) { clearTimeout(t.dryTimer); clearTimeout(t.saveTimer); t.closed = true; }
  if (typeof window !== 'undefined' && window.close) window.close();
  // If the browser refused window.close() (e.g. opened as a normal tab), fall
  // back to showing the project here so ctrl+q never leaves the page stuck.
  if (typeof window === 'undefined' || !window.closed) {
    SC.popout = false;
    SC.open = false;
    if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
    if (ed) ed.blur();
    redraw();
  }
  return true;
}

// ---- making and deleting ----

// focusEditor gives the editor focus after the key that asked for it is
// done: it would otherwise be typed into the editor, which vim reads as a
// command.
export function focusEditor() {
  SC.focus = 'editor';
  setTimeout(() => { if (ed) ed.focus(); }, 0);
}

export function toggleList() { SC.listOpen = !SC.listOpen; redraw(); }

export function toggleResults() {
  const tab = activeTab();
  if (!tab.run) { say('nothing to show yet: run a query first'); return; }
  tab.hideResults = !tab.hideResults;
  // Hiding the results gives the keys back to the editor, in normal mode,
  // wherever they were: the results, the SQL tab or insert mode.
  if (tab.hideResults && ed) {
    ed.normal();
    focusEditor();
  }
  redraw();
}

// setResultTab shows the run's rows ('rows') or the SQL it ran ('sql').
// The tab is the run's own, so a new run opens on its rows.
export function setResultTab(tab, ctx = SC) {
  const c = resolveCtx(ctx);
  if (!c.run) return;
  c.run.tab = tab;
  redraw();
}

// newDraft opens an empty scratch in a new tab with no file yet: the file
// is made when there is something in it.
export async function newDraft() {
  const prev = SC.tabs.length ? activeTab() : null;
  if (prev && prev.cur) {
    captureEditor(prev);
    flushSave(prev);
  }
  if (!SC.defaults) await loadDefaults();
  const cur = { id: '', draft: true, from: '', project: SC.defaults.project, location: SC.defaults.location, sql: '' };
  let tab;
  if (prev && !prev.cur) {
    tab = prev;
    tab.cur = cur;
    tab.savedSql = '';
    tab.labelLine = '';
    tab.dry = null; tab.run = null; tab.confirm = null; tab.sel = null;
    tab.filters = {}; tab.sort = null; tab.scrollTop = 0; tab.hideResults = false;
    tab.editor = null; tab.editorScroll = 0;
  } else {
    tab = makeTab(cur);
    SC.tabs.push(tab);
    SC.activeTab = tab.key;
  }
  applyEditor(tab);
  focusEditor();
  redraw();
}

// ensureFile makes a draft's file, and resolves once it is there.
export function ensureFile(tab = activeTab()) {
  const cur = tab && tab.cur;
  if (!cur || !cur.draft) return Promise.resolve(cur);
  if (!cur.making) {
    cur.making = post('/api/scratch/create', { form: 'empty', project: cur.project, location: cur.location }).then(async (f) => {
      cur.id = f.id; cur.project = f.project; cur.location = f.location; cur.draft = false;
      await loadList();
      if (tab === activeTab()) SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === f.id));
      redraw();
      return cur;
    }).catch((e) => { cur.making = null; say('could not make the scratch: ' + e.message); throw e; });
  }
  return cur.making;
}

// setTarget changes the project and location a scratch runs in.
export async function setTarget(project, location) {
  const tab = activeTab();
  const cur = tab.cur;
  if (!cur || (cur.project === project && cur.location === location)) return;
  cur.project = project; cur.location = location;
  if (!cur.draft) {
    try { await post('/api/scratch/target', { id: cur.id, project, location }); } catch (e) { say(e.message); }
  }
  clearMeta();
  tab.dry = null;
  scheduleDry(0, tab);
  redraw();
}

// loadProjects lists BigQuery projects or Databricks catalogs for the toolbar
// target box's datalist, once per session (cleared on refresh or profile switch).
let projectsSeq = 0;
let projectsLoading = false;
export async function loadProjects() {
  if (SC.projects || projectsLoading) return;
  projectsLoading = true;
  const seq = ++projectsSeq;
  try {
    const list = await get('/api/scratch/meta/projects');
    if (seq === projectsSeq) {
      SC.projects = Array.isArray(list) ? list : [];
      redraw();
    }
  } catch { /* ignore */ }
  if (seq === projectsSeq) projectsLoading = false;
}

// refreshMeta forgets what is known of datasets and tables, here and in xf.
export async function refreshMeta() {
  projectsSeq++;
  projectsLoading = false;
  SC.projects = null;
  clearMeta();
  try { await post('/api/scratch/meta/refresh'); } catch (e) { say(e.message); return; }
  say('table metadata refreshed');
}

let clearMetaFn = () => {};
export function onClearMeta(f) { clearMetaFn = f; }
function clearMeta() { clearMetaFn(); }

export function askNew() {
  if (S.caps.scratch && SC.standalone) { newDraft(); return; }
  const v = S.view;
  if (!S.caps.scratch) { S.flash = 'scratch queries need BigQuery access, which this project lacks'; return; }
  if (!v || !S.sel) { S.flash = 'select a model to make a scratch query from'; return; }
  SC.newForm = { busy: false, error: '' };
  S.overlay = 'scratchNew';
}

export async function createScratch(form) {
  const n = SC.newForm;
  if (!n || n.busy) return;
  n.busy = true; n.error = '';
  redraw();
  try {
    const f = await post('/api/scratch/create', { id: S.sel, form, incremental: S.incremental });
    S.overlay = null;
    SC.newForm = null;
    await openScratchView(f.id);
    focusEditor();
  } catch (e) {
    n.busy = false;
    n.error = e.message;
  }
  redraw();
}

export function cancelNew() { S.overlay = null; SC.newForm = null; }

export async function deleteScratch(id) {
  try {
    await post('/api/scratch/delete', { id });
    SC.delAsk = '';
    SC.recent = SC.recent.filter((r) => r.scratch !== id);
    await loadList();
    const idx = SC.tabs.findIndex((t) => t.cur && t.cur.id === id);
    if (idx >= 0) {
      const tab = SC.tabs[idx];
      if (isRunning(tab)) cancelRun(tab);
      clearTimeout(tab.dryTimer);
      clearTimeout(tab.saveTimer);
      tab.closed = true;
      if (SC.tabs.length > 1) {
        const wasActive = tab === activeTab();
        SC.tabs.splice(idx, 1);
        if (wasActive) {
          const next = SC.tabs[Math.min(idx, SC.tabs.length - 1)];
          SC.activeTab = next.key;
          applyEditor(next);
          if (!next.dry || next.dry.stale) scheduleDry(0, next);
        }
      } else {
        tab.closed = false;
        tab.cur = null; tab.savedSql = ''; tab.dry = null; tab.run = null;
        tab.sel = null; tab.filters = {}; tab.sort = null; tab.editor = null;
        if (ed) ed.set('');
        if (SC.list[0]) await selectScratch(SC.list[0].id);
        else await newDraft();
      }
    }
  } catch (e) { say(e.message); }
  redraw();
}

// ---- saving ----

const pendingSaves = new Set();

export function onEdit() {
  const tab = activeTab();
  clearTimeout(tab.saveTimer);
  tab.saveTimer = setTimeout(() => flushSave(tab), 600);
  if (tab.cur && !tab.cur.from && ed && ed.doc) {
    const line = firstLine(ed.doc());
    if (line !== tab.labelLine) { tab.labelLine = line; redraw(); }
  }
  scheduleDry(500, tab);
}

export function flushSave(tab = activeTab()) {
  if (!tab) return Promise.resolve();
  clearTimeout(tab.saveTimer);
  tab.saveTimer = 0;
  if (!tab.cur) return Promise.resolve();
  const isAct = tab === activeTab();
  if (isAct && !ed) return Promise.resolve();
  const sql = isAct && ed && ed.doc ? ed.doc() : (tab.cur.sql || '');
  if (sql === tab.savedSql && !tab.cur.draft) return Promise.resolve();
  if (tab.cur.draft && !sql.trim()) return Promise.resolve();
  tab.cur.sql = sql;
  const p = (async () => {
    if (tab.cur.draft) {
      try { await ensureFile(tab); } catch { return; }
    }
    const id = tab.cur.id, seq = ++tab.saveSeq;
    try {
      await post('/api/scratch/save', { id, sql });
      if (seq === tab.saveSeq) tab.savedSql = sql;
      const row = SC.list.find((s) => s.id === id);
      if (row && seq === tab.saveSeq) row.preview = firstLine(sql);
    } catch (e) { say('could not save: ' + e.message); }
  })();
  pendingSaves.add(p);
  p.finally(() => pendingSaves.delete(p));
  return p;
}

export async function flushAllSaves() {
  if (SC.tabs.length) await flushSave(activeTab());
  if (pendingSaves.size) await Promise.all([...pendingSaves]);
}

function firstLine(sql) {
  for (const l of sql.split('\n')) {
    const t = l.trim();
    if (t && !t.startsWith('--')) return t.length > 100 ? t.slice(0, 100) + '…' : t;
  }
  return '';
}

// ---- the dry run ----

export function scheduleDry(ms, tab = activeTab()) {
  if (!tab) return;
  clearTimeout(tab.dryTimer);
  tab.dryTimer = setTimeout(() => { tab.dryTimer = 0; dryRunNow(tab); }, ms);
}

// dryRunNow dry-runs the scope, the SQL the next run would run, and
// resolves with the answer. Edits made while it runs start a later one;
// this one's answer is then not shown.
export async function dryRunNow(tab = activeTab()) {
  if (!tab || tab.closed || !tab.cur || tab !== activeTab() || !ed || ed.block()) return null;
  const sql = ed.scope();
  if (!sql.trim()) { tab.dry = null; redraw(); return null; }
  if (tab.dry && tab.dry.sql === sql && !tab.dry.stale) return tab.dryPromise || tab.dry;
  const seq = ++tab.drySeq;
  const keep = tab.dry && tab.dry.sql !== sql ? { ...tab.dry, stale: true } : tab.dry;
  tab.dry = { ...(keep || {}), sql, pending: true };
  redraw();
  const base = ed.scopeOffset();
  const p = post('/api/scratch/dryrun', { id: tab.cur.id, sql, project: tab.cur.project, location: tab.cur.location }).then((r) => {
    const out = { ...r, sql, pending: false };
    if (!tab.closed && seq === tab.drySeq) {
      tab.dry = out;
      if (tab === activeTab() && ed && ed.scope() === sql) markError(out.ok ? '' : out.error, sql, base);
      redraw();
    }
    return out;
  }).catch((e) => {
    const out = { ok: false, error: e.message, sql, pending: false, bytes: 0, writes: false };
    if (!tab.closed && seq === tab.drySeq) { tab.dry = out; redraw(); }
    return out;
  });
  tab.dryPromise = p;
  return p;
}

export const dryTone = (d) => {
  if (!d || d.pending || !d.ok) return d && d.error ? 'error' : 'dim';
  if (d.bytes >= d.confirmBytes) return 'red';
  if (d.bytes >= d.warnBytes) return 'amber';
  return '';
};

// markError underlines in the editor the place BigQuery names in a message,
// "[line:column]", in sql, the text it was sent, which begins base
// characters into the editor's. A message with no place clears the marks.
export function markError(message, sql, base) {
  if (!ed) return;
  const m = /\[(\d+):(\d+)\]/.exec(message || '');
  if (!m) { ed.mark(null); return; }
  ed.mark({ sql, base: base || 0, line: Number(m[1]), col: Number(m[2]), message: message.split('\n')[0] });
}

// ---- running ----

let runSeq = 0;
// ---- recent results ----

export async function loadRecent() {
  try { SC.recent = await get('/api/scratch/runs'); } catch { SC.recent = SC.recent || []; }
}

// recordRun keeps a run that returned rows among the recent ones.
async function recordRun(run) {
  if (!run.scratch || !run.job) return;
  try {
    SC.recent = await post('/api/scratch/runs', {
      scratch: run.scratch, job: run.job, sql: run.sql, rows: run.total,
      started: new Date(run.started).toISOString(), ended: new Date(run.ended || Date.now()).toISOString(),
      statementType: (run.status && run.status.statementType) || '',
    });
    redraw();
  } catch { /* the result still shows; it just isn't kept */ }
}

// showRecent shows a recent run's result, read again from BigQuery, which
// keeps it about a day. It is polled as a fresh run is, from its job.
function showRecent(saved, tab = activeTab()) {
  const run = tab.run = newRun(saved.sql, tab);
  run.job = saved.job;
  run.scratch = saved.scratch;
  run.started = Date.parse(saved.started) || Date.now();
  run.restored = Date.parse(saved.ended) || run.started;
  run.phase = 'running';
  tab.hideResults = false;
  tab.sel = null; tab.filters = {}; tab.sort = null; tab.scrollTop = 0;
  poll(run);
}

// openRecent opens the scratch a recent run came from, on that run.
export async function openRecent(saved) {
  SC.recentOpen = '';
  if (!saved) { redraw(); return; }
  await selectScratch(saved.scratch, false, saved);
}

// toggleRecent shows or hides the menu, which takes the keys from the
// editor while it is up, as the run's question does.
export function toggleRecent() {
  if (SC.recentOpen) { closeRecent(); return; }
  if (!SC.recent.length) { say('no recent results yet: run a query'); return; }
  SC.recentOpen = SC.focus === 'editor' ? 'editor' : 'results';
  SC.recentCursor = 0;
  SC.focus = 'results';
  if (ed) ed.blur();
  redraw();
}

export function closeRecent() {
  const back = SC.recentOpen;
  SC.recentOpen = '';
  if (back === 'editor') focusEditor();
  redraw();
}

function recentKey(k) {
  const n = SC.recent.length;
  if (k === 'esc' || k === 'h' || !n) { closeRecent(); return; }
  if (k === 'j' || k === 'down') SC.recentCursor = Math.min(SC.recentCursor + 1, n - 1);
  else if (k === 'k' || k === 'up') SC.recentCursor = Math.max(SC.recentCursor - 1, 0);
  else if (k === 'enter') openRecent(SC.recent[SC.recentCursor]);
  else if (/^[1-9]$/.test(k) && Number(k) <= n) openRecent(SC.recent[Number(k) - 1]);
}

// ago words how long ago t (ms) was.
export function ago(t) {
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

// recentGone says BigQuery has likely dropped the result: it keeps one
// about 24 hours.
export const recentGone = (saved) => Date.now() - (Date.parse(saved.ended) || 0) > 24 * 3600 * 1000;

// A result's view: a Scratch tab, or one the dependency inspector keeps
// per row (resultView). Each holds the run it shows and the grid's
// filters, sort, scroll and selected cell; the functions below take one,
// the active Scratch tab when SC (the default) is given. ctxOf is the
// view a run belongs to, outside the run, which a snapshot turns into JSON.
const ctxOf = new WeakMap();
const resolveCtx = (ctx) => (!ctx || ctx === SC ? activeTab() : ctx);

// resultView is a new view of no run, for a result shown outside Scratch.
export const resultView = () => ({ run: null, filters: {}, sort: null, scrollTop: 0, sel: null, confirm: null, size: SC.size });

function newRun(sql, ctx = SC) {
  const target = resolveCtx(ctx);
  const run = {
    sql, phase: 'starting', job: null, status: null, started: Date.now(), ended: 0, error: '',
    schema: null, rows: [], next: '', total: 0, loadingMore: false, children: [], child: '', seq: ++runSeq,
  };
  ctxOf.set(run, target);
  return run;
}

// showJob shows the rows of a job that has run, or is running, in ctx,
// read from BigQuery as a recent run's are: the dependency inspector's
// rows' runs. fresh is a job just started (a preview), which the result's
// line times from now rather than calling it a recent result.
export function showJob(ctx, job, sql, fresh) {
  const c = resolveCtx(ctx);
  const run = c.run = newRun(sql, c);
  run.job = job;
  if (!fresh) run.restored = Date.now();
  run.phase = 'running';
  c.sel = null; c.filters = {}; c.sort = null; c.scrollTop = 0;
  poll(run);
}

// runScratch runs the scope, asking first when the dry run says it costs a
// lot or changes data. force skips the asking.
export async function runScratch(force) {
  const tab = activeTab();
  if (!tab.cur || !ed) return;
  if (isRunning(tab)) { say('a query is running: cancel it first (ctrl+c)'); return; }
  if (ed.block()) { say('a ctrl+v block can\'t run: select with v or V to run part of the file'); return; }
  const sql = ed.scope();
  if (!sql.trim()) { say('nothing to run'); return; }
  const base = ed.scopeOffset();
  try { await ensureFile(tab); } catch { return; }
  await flushSave(tab);
  if (tab.closed) return;
  if (!force) {
    const d = await dryRunNow(tab);
    if (tab.closed) return;
    if (d && d.ok) {
      const why = [];
      if (d.bytes >= d.confirmBytes) why.push(`The dry run says this scans ${fmtBytes(d.bytes)}${d.cost ? ` (≈ ${d.cost} on demand)` : ''}.`);
      if (d.writes) why.push(`This is ${d.statementType === 'SCRIPT' ? 'a script, which may change data' : 'a ' + d.statementType + ' statement, which changes data'}${d.tables && d.tables.length ? ': it reads ' + d.tables.slice(0, 3).join(', ') + (d.tables.length > 3 ? '…' : '') : ''}.`);
      if (why.length) {
        // The prompt takes its answer from the page's keys, which the
        // editor would otherwise swallow.
        if (tab === activeTab()) { ed.blur(); SC.focus = 'results'; }
        tab.confirm = { sql, why, danger: d.bytes >= d.confirmBytes || d.writes };
        redraw();
        return;
      }
    }
  }
  tab.confirm = null;
  tab.hideResults = false;
  const run = tab.run = newRun(sql, tab);
  run.base = base;
  run.scratch = tab.cur.id;
  run.record = true;
  tab.sel = null;
  tab.filters = {}; tab.sort = null;
  tab.scrollTop = 0;
  redraw();
  try {
    run.job = await post('/api/scratch/run', { id: tab.cur.id, sql });
  } catch (e) {
    run.phase = 'failed'; run.error = e.message; run.ended = Date.now();
    // A Databricks or Snowflake project with no warehouse/profile yet: pick one, then run.
    if (onDemand() && /^no (Databricks workspace|Snowflake account)/.test(e.message) && SC.dbx && SC.dbx.hasProfiles) openProfilePicker(() => runScratch(true));
    else if (onDemand() && /^no (SQL|virtual) warehouse chosen/.test(e.message)) openWarehousePicker(() => runScratch(true));
    redraw();
    return;
  }
  if (!live(run)) return;
  run.phase = 'running';
  redraw();
  poll(run);
}

// live says run is still what its tab or view shows: the run, or the
// search of all its rows.
const live = (run) => {
  const c = ctxOf.get(run) || activeTab();
  if (c.closed) return false;
  return c.run === run || (!!c.run && c.run.sub === run);
};

async function poll(run) {
  let wait = 400;
  while (live(run) && run.phase === 'running') {
    let st;
    try {
      st = await get(jobQuery('/api/scratch/job', run.job));
    } catch (e) {
      if (!live(run)) return;
      run.phase = 'failed'; run.error = e.message; run.ended = Date.now();
      redraw();
      return;
    }
    if (!live(run)) return;
    run.status = st;
    if (st.state === 'DONE') {
      const owner = ctxOf.get(run);
      if (!owner || owner === activeTab()) SC.flash = '';
      if (!run.restored && /^(CREATE_|ALTER_|DROP_|SCRIPT)/.test(st.statementType || '')) clearMeta(); // tables or columns may have changed
      const ended = st.ended ? Date.parse(st.ended) : 0;
      run.ended = ended > 1e12 ? ended : Date.now();
      if (st.error) {
        run.phase = st.cancelled ? 'cancelled' : 'failed';
        run.error = st.error;
        if (run === SC.run && run.phase === 'failed' && ed && ed.scope() === run.sql) markError(st.error, run.sql, run.base);
      } else {
        run.children = st.children || [];
        run.phase = 'loading';
        redraw();
        // A script's rows are those of its last SELECT.
        const last = [...run.children].reverse().find((c) => c.hasRows);
        if (last) { run.child = last.id; await loadFirst(run, { ...run.job, id: last.id }); } else if (!run.children.length) await loadFirst(run, run.job);
        if (!live(run)) return;
        if (run.phase === 'loading') run.phase = 'done';
        if (run.record && run.phase === 'done' && run.schema && run.schema.length) recordRun(run);
      }
      redraw();
      return;
    }
    redraw();
    await new Promise((r) => setTimeout(r, wait));
    wait = Math.min(wait * 1.3, 1500);
  }
}

function jobQuery(path, job, extra) {
  const q = new URLSearchParams({ project: job.project, id: job.id });
  if (job.location) q.set('location', job.location);
  for (const [k, v] of Object.entries(extra || {})) if (v !== undefined && v !== '') q.set(k, v);
  return path + '?' + q.toString();
}

// loadFirst reads the first page of job's rows into run.
async function loadFirst(run, job) {
  try {
    const page = await get(jobQuery('/api/scratch/rows', job, { size: 500 }));
    if (!live(run)) return;
    run.rowsJob = job;
    run.schema = page.schema || [];
    run.rows = page.rows || [];
    run.next = page.nextToken || '';
    run.total = page.total || run.rows.length;
  } catch (e) {
    run.phase = 'failed'; run.error = e.message;
    if (!run.restored || !run.ended) run.ended = Date.now(); // a recent run keeps its job's times
  }
}

// loadMore reads the next page when the grid scrolls near the end of the
// rows it has.
export async function loadMore(ctx = SC) {
  const c = resolveCtx(ctx);
  const run = viewRun(c);
  if (!run || run.phase !== 'done' || !run.next || run.loadingMore) return;
  run.loadingMore = true;
  redraw();
  try {
    const page = await get(jobQuery('/api/scratch/rows', run.rowsJob, { size: 500, token: run.next }));
    if (!live(run)) return;
    run.rows = run.rows.concat(page.rows || []);
    run.next = page.nextToken || '';
  } catch (e) {
    say('could not read more rows: ' + e.message);
  }
  run.loadingMore = false;
  redraw();
}

// showChild shows the rows of one statement of a script.
export async function showChild(child, ctx = SC) {
  const c = resolveCtx(ctx);
  const run = c.run;
  if (!run || run.phase !== 'done') return;
  run.child = child.id;
  run.schema = null; run.rows = []; run.next = ''; run.total = 0;
  run.sub = null; run.subOn = false; run.lc = null;
  c.sel = null; c.scrollTop = 0; c.filters = {}; c.sort = null;
  redraw();
  if (child.hasRows) {
    run.phase = 'loading';
    await loadFirst(run, { ...run.job, id: child.id });
    if (c.run === run && run.phase === 'loading') run.phase = 'done';
  }
  redraw();
}

export const isRunning = (tab = activeTab()) => !!tab && !!tab.run && ['starting', 'running'].includes(tab.run.phase);
export const anyRunning = () => SC.tabs.some((t) => isRunning(t));

export async function cancelRun(tab = activeTab()) {
  const run = tab && tab.run;
  if (!run || !run.job || !['starting', 'running'].includes(run.phase)) { return false; }
  try {
    await post('/api/scratch/cancel', run.job);
    if (tab === activeTab()) say('cancelling…');
  } catch (e) { say(e.message); }
  return true;
}

export async function cancelAllRuns() {
  const running = SC.tabs.filter((t) => isRunning(t));
  if (!running.length) return false;
  await Promise.all(running.map((t) => cancelRun(t)));
  return true;
}

export function confirmRun() {
  const c = SC.confirm;
  if (c && c.action === 'searchAll') { SC.confirm = null; return searchAll(true); }
  if (c && c.action === 'closeTab') { const k = c.tabKey; SC.confirm = null; return closeTab(k, true); }
  if (c && c.action === 'closeOtherTabs') { const k = c.tabKey; SC.confirm = null; return closeOtherTabs(k, true); }
  if (c && c.action === 'closePopout') { SC.confirm = null; return closePopout(true); }
  return runScratch(true);
}
export function dismissConfirm() { SC.confirm = null; redraw(); }

// ---- the result ----

// onDbx and onSf identify on-demand warehouses (ADR 0042, ADR 0044): their
// queries run on a SQL or virtual warehouse, with no dry run, job page or
// result table to filter server-side.
export const onDbx = () => !!S.caps && S.caps.warehouse === 'databricks';
export const onSf = () => !!S.caps && S.caps.warehouse === 'snowflake';
export const onDemand = () => onDbx() || onSf();
// site is the warehouse's name, as the page words where a query runs.
export const site = () => onDbx() ? 'Databricks' : onSf() ? 'Snowflake' : 'BigQuery';

// jobURL is a job's page in the BigQuery console; '' on Databricks or Snowflake.
export function jobURL(job) {
  if (!job || onDemand()) return '';
  const loc = job.location ? job.location : '';
  return `https://console.cloud.google.com/bigquery?project=${encodeURIComponent(job.project)}&j=bq:${encodeURIComponent(loc)}:${encodeURIComponent(job.id)}&page=queryresults`;
}

export function csvURL(ctx = SC, name = '') {
  const c = resolveCtx(ctx);
  const run = viewRun(c);
  if (!run || !run.rowsJob || !run.schema || !run.schema.length) return '';
  return jobQuery('/api/scratch/csv', run.rowsJob, { name: name || (c.cur ? c.cur.id : 'result') });
}

export function fmtBytes(n) {
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0, v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (i === 0 ? String(n) : v.toFixed(2)) + ' ' + u[i];
}

export function fmtDuration(ms) {
  if (ms < 1000) return ms + ' ms';
  const s = ms / 1000;
  if (s < 60) return s.toFixed(1) + ' s';
  return Math.floor(s / 60) + 'm ' + String(Math.round(s % 60)).padStart(2, '0') + 's';
}

// ---- cell shapes (see bigquery.Cell) ----

const ALIAS = { INTEGER: 'INT64', FLOAT: 'FLOAT64', BOOLEAN: 'BOOL', RECORD: 'STRUCT' };
export const baseType = (f) => ALIAS[f.type] || f.type;
export const isNumeric = (f) => f.mode !== 'REPEATED' && ['INT64', 'FLOAT64', 'NUMERIC', 'BIGNUMERIC'].includes(baseType(f));
export const isNested = (f) => f.mode === 'REPEATED' || baseType(f) === 'STRUCT';

export function typeLabel(f) {
  let t = baseType(f);
  if (t === 'STRUCT') t = 'STRUCT<' + (f.fields || []).map((s) => s.name + ' ' + typeLabel(s)).join(', ') + '>';
  if (t === 'RANGE' && f.rangeElementType) t = 'RANGE<' + f.rangeElementType.type + '>';
  return f.mode === 'REPEATED' ? 'ARRAY<' + t + '>' : t;
}

function hexOf(b64, n) {
  try {
    const raw = atob(b64);
    let s = '';
    for (let i = 0; i < Math.min(raw.length, n); i++) s += raw.charCodeAt(i).toString(16).padStart(2, '0');
    return '0x' + s + (raw.length > n ? '…' : '');
  } catch { return b64; }
}

const element = (f) => ({ ...f, mode: f.mode === 'REPEATED' ? '' : f.mode });

// preview is a cell as one line of text, short for a nested one.
export function preview(f, v, depth = 0) {
  if (v === null || v === undefined) return 'NULL';
  const t = baseType(f);
  if (f.mode === 'REPEATED') {
    const e = element(f);
    if (!v.length) return '[]';
    if (depth > 0) return `[…${v.length}]`;
    const shown = [];
    let len = 0;
    for (const x of v) {
      const s = preview(e, x, depth + 1);
      if (len + s.length > 60 && shown.length) break;
      shown.push(s); len += s.length + 2;
    }
    return '[' + shown.join(', ') + (shown.length < v.length ? `, …${v.length - shown.length} more` : '') + ']';
  }
  if (t === 'STRUCT') {
    if (depth > 0) return '{…}';
    const fs = f.fields || [];
    let out = '', n = 0;
    for (let i = 0; i < fs.length; i++) {
      const s = fs[i].name + ': ' + preview(fs[i], v[i], depth + 1);
      if (out.length + s.length > 70 && n) { out += `, …${fs.length - n} more`; break; }
      out += (n ? ', ' : '') + s; n++;
    }
    return '{' + out + '}';
  }
  if (t === 'RANGE' && Array.isArray(v)) {
    const e = { type: (f.rangeElementType && f.rangeElementType.type) || 'STRING' };
    return `[${v[0] === null ? 'UNBOUNDED' : preview(e, v[0], 1)}, ${v[1] === null ? 'UNBOUNDED' : preview(e, v[1], 1)})`;
  }
  if (t === 'BYTES' && typeof v === 'object') return `${v.len} bytes · ${hexOf(v.b64, 8)}`;
  return String(v);
}

// plain is a cell as the text copy gives: a string as it is, a nested
// value as JSON with its field names.
export function plain(f, v) {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  if (baseType(f) === 'BYTES' && f.mode !== 'REPEATED') return v.b64;
  return JSON.stringify(named(f, v), null, 2);
}

function named(f, v) {
  if (v === null || v === undefined) return null;
  if (f.mode === 'REPEATED') return v.map((x) => named(element(f), x));
  const t = baseType(f);
  if (t === 'STRUCT') {
    const o = {};
    (f.fields || []).forEach((s, i) => { o[s.name] = named(s, v[i]); });
    return o;
  }
  if (t === 'RANGE' && Array.isArray(v)) return { start: v[0], end: v[1] };
  if (t === 'BYTES') return v.b64;
  if (t === 'JSON' && typeof v === 'string') { try { return JSON.parse(v); } catch { return v; } }
  return v;
}
export { named };

// ---- nested cells laid out as BigQuery's console does ----
//
// A STRUCT column shows as a column per field (EVENT.ID, EVENT.AT), and an
// array's elements as lines down its row, so a row with an array of 3
// structs is 3 lines tall; columns outside the array show on its first
// line. Two arrays side by side share lines, the taller setting the
// height.

const isStruct = (f) => baseType(f) === 'STRUCT' && (f.fields || []).length > 0;

// leavesOf is the grid's columns: a column per field a STRUCT holds, at
// any depth, and one for every other column. Each says which result
// column it is part of (col), its name below that column's (sub, '' for a
// column that isn't a STRUCT), its field (f, without REPEATED) and
// whether it is in an array, so its lines are its own (arr).
export function leavesOf(schema) {
  const out = [];
  const walk = (f, col, sub, arr) => {
    arr = arr || f.mode === 'REPEATED';
    if (!isStruct(f)) { out.push({ col, sub, f: element(f), arr }); return; }
    for (const s of f.fields) walk(s, col, sub ? sub + '.' + s.name : s.name, arr);
  };
  schema.forEach((f, c) => walk(f, c, '', false));
  return out;
}

const widthOf = (f) => (isStruct(f) ? f.fields.reduce((n, s) => n + widthOf(s), 0) : 1);

// put lays out value v of field f from leaf `at` and line `line` of
// lines, an array of lines each an array of cells by leaf, and gives how
// many lines it took. A cell is { v, f }: its value and its field; a
// leaf with nothing on a line has no cell there.
function put(f, v, at, line, lines) {
  const cell = (i, x, g) => { (lines[line + i] ||= [])[at] = { v: x, f: g }; };
  if (f.mode === 'REPEATED') {
    if (!v || !v.length) {
      if (!isStruct(f)) cell(0, v === null || v === undefined ? null : v, f);
      return 1;
    }
    const e = element(f);
    let n = 0;
    for (const x of v) n += put(e, x, at, line + n, lines);
    return n;
  }
  if (!isStruct(f)) { cell(0, v === undefined ? null : v, f); return 1; }
  let n = 1, a = at;
  f.fields.forEach((s, i) => {
    n = Math.max(n, put(s, v === null || v === undefined ? null : v[i], a, line, lines));
    a += widthOf(s);
  });
  return n;
}

// rowLines is row as the grid's lines, by leavesOf(schema).
export function rowLines(schema, row) {
  const lines = [];
  let a = 0;
  schema.forEach((f, c) => { put(f, row[c], a, 0, lines); a += widthOf(f); });
  for (let i = 0; i < lines.length; i++) lines[i] ||= [];
  return lines;
}

const hasArrays = (schema) => schema.some((f) => f.mode === 'REPEATED' || (isStruct(f) && hasArrays(f.fields)));

// linesOf is row's lines, kept with the row while it lives.
const linesKept = new WeakMap();
export function linesOf(schema, row) {
  let l = linesKept.get(row);
  if (!l) { l = rowLines(schema, row); linesKept.set(row, l); }
  return l;
}

// heightOf is how many lines row takes.
export const heightOf = (schema, row) => (anyArrays(schema) ? linesOf(schema, row).length : 1);

// anyArrays says whether a row of schema can take more than one line.
const arraysOf = new WeakMap();
export function anyArrays(schema) {
  let b = arraysOf.get(schema);
  if (b === undefined) { b = hasArrays(schema); arraysOf.set(schema, b); }
  return b;
}

export function copyCell(r, c, ctx = SC) {
  const run = viewRun(resolveCtx(ctx));
  if (!run || !run.schema) return;
  copy(plain(run.schema[c], run.rows[r][c]), 'copied cell');
}

export function copyRow(r, ctx = SC) {
  const run = viewRun(resolveCtx(ctx));
  if (!run || !run.schema) return;
  const o = {};
  run.schema.forEach((f, i) => { o[f.name] = named(f, run.rows[r][i]); });
  copy(JSON.stringify(o, null, 2), 'copied row as JSON');
}

// ---- keys ----

export function scratchKey(k) {
  if (SC.confirm) {
    if (k === 'y' || k === 'enter') confirmRun();
    else dismissConfirm();
    return true;
  }
  if (SC.delAsk) {
    if (k === 'y') deleteScratch(SC.delAsk); else { SC.delAsk = ''; SC.flash = 'kept'; }
    return true;
  }
  if (SC.recentOpen) { recentKey(k); return true; }
  if (SC.whOpen) { warehouseMenuKey(k); return true; }
  SC.flash = '';
  if (/^[1-9]$/.test(k)) { switchTabIndex(Number(k) - 1); return true; }
  switch (k) {
    case 'ctrl+c': if (isRunning()) cancelRun(); return true;
    case 'i': case 'enter': focusEditor(); return true;
    case 'r': runScratch(); return true;
    case 'j': case 'down': moveScratch(1); return true;
    case 'k': case 'up': moveScratch(-1); return true;
    case 't': newDraft(); return true;
    case 'x': closeTab(); return true;
    case '[': nextScratchTab(-1); return true;
    case ']': nextScratchTab(1); return true;
    case 'n': askNew(); return true;
    case 'b': toggleList(); return true;
    case 'h': toggleRecent(); return true;
    case 'd': if (SC.cur && !SC.cur.draft) { SC.delAsk = SC.cur.id; } return true;
    case 'y': if (SC.sel) copyCell(SC.sel.r, SC.sel.c); return true;
    case 'Y': if (SC.sel) copyRow(SC.sel.r); return true;
  }
  return true; // the project's keys don't apply here
}

// ---- the palette's commands ----

// scratchCommands are what the palette lists in the Scratch view (cmd+shift+p):
// Scratch's own, as the project's commands don't apply here.
export function scratchCommands() {
  const out = [];
  const add = (title, key, run, state) => out.push({ title, key, run, state });
  const toggle = (title, on, key, f) => add(title, key, f, on ? 'on' : 'off');
  const cur = SC.cur, run = SC.run, shown = viewRun();
  const toResults = () => { SC.focus = 'results'; if (ed) ed.blur(); };
  const tabPrevKey = onMac ? '⌥⌘←' : 'ctrl+alt+←';
  const tabNextKey = onMac ? '⌥⌘→' : 'ctrl+alt+→';
  const tabNewKey = onMac ? '⌥t' : 'alt+t';
  const tabCloseKey = onMac ? '⌥w' : 'alt+w';
  if (cur && !isRunning()) add('Run: query (the selection, else the file)', onMac ? '⌘↵' : 'ctrl+↵', () => runScratch());
  if (isRunning()) add('Run: cancel', 'ctrl+c', () => cancelRun());
  add('Scratch: new empty query', SC.standalone ? 'n' : tabNewKey, newDraft);
  if (!SC.standalone && S.view && S.sel) add('Scratch: new query from the selected model', 'n', askNew, S.view.name);
  if (SC.tabs.length > 1) {
    add('Scratch: next tab', tabNextKey, () => nextScratchTab(1), SC.tabs.length + ' open');
    add('Scratch: previous tab', tabPrevKey, () => nextScratchTab(-1), SC.tabs.length + ' open');
  }
  if (canCloseTab()) add('Scratch: close tab', tabCloseKey, () => closeTab());
  if (SC.tabs.length > 1) add('Scratch: close other tabs', '', () => closeOtherTabs(), (SC.tabs.length - 1) + ' others');
  if (!SC.standalone && !SC.popout) add('Scratch: pop out in new window', '', () => popoutScratch());
  if (SC.tabs.length > 1 || (!SC.standalone && !SC.popout)) add('Scratch: move tab to new window', '', () => popoutScratch({ tabKey: SC.activeTab }));
  if (SC.popout) add('Scratch: dock back to project', '', () => dockScratch(), S.boot && S.boot.name);
  if (cur && !cur.draft) add('Scratch: delete this scratch', 'd', () => { toResults(); SC.delAsk = cur.id; }, cur.id);
  if (cur) add(onDbx() ? 'Scratch: change its catalog' : onSf() ? 'Scratch: change its database' : 'Scratch: change its project or location', '', () => {
    toResults();
    setTimeout(() => { const el = document.querySelector('.sc-target-in'); if (el) { el.focus(); el.select(); } }, 0);
  }, [cur.project, cur.location].filter(Boolean).join(' · '));
  if (onDbx() && SC.dbx && SC.dbx.hasProfiles) {
    add('Databricks: choose profile (~/.databrickscfg)', '', () => openProfilePicker(), SC.dbx.profile || '');
  }
  if (onSf() && SC.dbx && SC.dbx.hasProfiles) {
    add('Snowflake: choose connection (~/.snowflake/connections.toml)', '', () => openProfilePicker(), SC.dbx.profile || '');
  }
  if (onDbx()) add('Databricks: choose the SQL warehouse queries run on', '', () => openWarehousePicker(() => {}), SC.dbx && SC.dbx.warehouse ? SC.dbx.warehouse : '');
  if (onSf()) add('Snowflake: choose the virtual warehouse queries run on', '', () => openWarehousePicker(() => {}), SC.dbx && SC.dbx.warehouse ? SC.dbx.warehouse : '');
  if (cur && ed && ed.format) add('Format: query with sqlfluff', vimOn() ? ':format' : '', () => ed.format());
  add('Tables: refresh what is known of tables and columns', ':refresh', refreshMeta);
  if (SC.recent.length) add('Results: recent results', 'h', toggleRecent, SC.recent.length + ' kept');
  if (shown && shown.phase === 'done' && csvURL()) add('Results: export CSV', '', downloadCSV);
  if (run && run.job) add('Results: open the job in BigQuery', '', () => openLink(jobURL(run.job)));
  if (run && run.next && run.rowsJob && (activeFilters().length || SC.sort)) add('Results: search all rows', '', () => searchAll(false), run.total ? run.total.toLocaleString() + ' rows' : '');
  if (activeFilters().length) add('Results: clear filters', '', () => clearFilters());
  if (SC.sort) add('Results: clear sort', '', () => clearSort());
  if (SC.sel) {
    add('Copy: cell', 'y', () => copyCell(SC.sel.r, SC.sel.c));
    add('Copy: row as JSON', 'Y', () => copyRow(SC.sel.r));
  }
  toggle('View: scratch list', SC.listOpen, onMac ? '⌘b' : 'ctrl+b', toggleList);
  if (run) toggle('View: results', !SC.hideResults, onMac ? '⌘j' : 'ctrl+j', toggleResults);
  add('View: colour theme', '', () => openThemePicker('theme'), themeLabel());
  if (hooks.openSettings) add('View: settings', 'ctrl+,', () => { flushAllSaves().then(() => { hooks.openSettings(); redraw(); }); });
  if (cur) add('Go to: editor', 'i', focusEditor);
  if (run && !SC.hideResults) add('Go to: results', 'ctrl+w', toResults);
  if (SC.standalone) {
    if (S.scratchWarehouses && S.scratchWarehouses.length > 1) {
      add('Scratch: switch warehouse', '', toggleWarehouseMenu, site());
    }
    add('Project: switch or add', '', openProjects);
    add('Close scratch: back to the projects', 'ctrl+q', () => { flushAllSaves().then(() => askQuit()); });
  } else if (SC.popout) {
    add('Close scratch window', 'ctrl+q', () => closePopout());
  } else {
    add('Project: switch or add', '', openProjects);
    add('Close scratch: back to the project', 'ctrl+q', closeScratchView, S.boot && S.boot.name);
  }
  return out;
}

// downloadCSV saves every row of the result shown, as its export CSV link does.
function downloadCSV() {
  const a = document.createElement('a');
  a.href = csvURL();
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// scratchReturn gives the keys back to the editor the palette was opened
// from, unless what the palette ran took them, or asks something.
function scratchReturn() {
  if (!SC.open || SC.focus !== 'editor' || S.overlay || SC.delAsk || SC.confirm || SC.recentOpen || (hooks.settingsOpen && hooks.settingsOpen())) return;
  focusEditor();
}

function moveScratch(d) {
  if (!SC.list.length) return;
  const i = Math.min(Math.max(SC.listCursor + d, 0), SC.list.length - 1);
  SC.listCursor = i;
  selectScratch(SC.list[i].id, false, null, { replace: true });
}

export function scratchNewKey(k) {
  if (!SC.newForm || SC.newForm.busy) return k !== 'esc' ? true : (cancelNew(), true);
  if (k === 'c' || k === '1') createScratch('compiled');
  else if (k === 's' || k === '2') createScratch('columns');
  else if (k === 'esc' || k === 'q') cancelNew();
  return true;
}

// The Scratch view goes into the tab's workspace (workspace.js) while it
// is open: every open tab (its scratch, editor text, cursor and undo
// history, results' place, filters and sort, and a finished run's results
// unless they are big).
export function saveScratchWorkspace(light) {
  if (!SC.open || !SC.tabs.length) return null;
  const active = activeTab();
  const tabs = SC.tabs.map((t) => {
    const cur = t.cur;
    if (!cur) return null;
    const isAct = t === active;
    const done = t.run && t.run.phase === 'done' && !light ? t.run : null;
    const edState = isAct && ed && ed.save ? (!cur.draft ? ed.save(light) : null)
      : (!cur.draft && t.editor ? (light ? { doc: t.editor.doc, selection: t.editor.selection } : t.editor) : null);
    return {
      id: cur.id || '',
      draft: !!cur.draft,
      project: cur.project || '',
      location: cur.location || '',
      hideResults: !!t.hideResults,
      filters: t.filters,
      sort: t.sort,
      sel: t.sel,
      scrollTop: t.scrollTop,
      run: done,
      editor: edState,
    };
  }).filter((t) => t && (t.id || t.draft));
  if (!tabs.length) return null;
  const activeIndex = Math.max(0, SC.tabs.indexOf(active));
  return {
    tabs,
    activeIndex,
    id: active && active.cur ? active.cur.id : '',
    focus: SC.focus,
    listOpen: SC.listOpen,
  };
}

export async function restoreScratchWorkspace(v) {
  if (!v) return;
  const savedTabs = Array.isArray(v.tabs) && v.tabs.length
    ? v.tabs
    : (v.id ? [{ id: v.id, hideResults: v.hideResults, filters: v.filters, sort: v.sort, sel: v.sel, scrollTop: v.scrollTop, run: v.run, editor: v.editor }] : []);
  if (!savedTabs.length) return;
  if (!S.caps.scratch) return;
  const seq = ++openSeq;
  SC.open = true;
  SC.focus = v.focus || 'results';
  SC.listOpen = !!v.listOpen;
  await Promise.all([loadList(), loadDefaults(), loadRecent()]);
  if (seq !== openSeq) return;
  const built = [];
  for (const st of savedTabs) {
    let cur = null;
    if (st.id) {
      try { cur = await get('/api/scratch/file?id=' + encodeURIComponent(st.id)); } catch { continue; }
    } else if (st.draft) {
      cur = { id: '', draft: true, from: '', project: st.project || (SC.defaults && SC.defaults.project) || '', location: st.location || (SC.defaults && SC.defaults.location) || '', sql: '' };
    }
    if (!cur) continue;
    const t = makeTab(cur);
    t.hideResults = !!st.hideResults;
    t.filters = st.filters || {};
    t.sort = st.sort || null;
    t.sel = st.sel || null;
    t.scrollTop = st.scrollTop || 0;
    if (st.run && st.run.sql) {
      t.run = st.run;
      ctxOf.set(t.run, t);
    } else if (cur.id) {
      const recent = SC.recent.find((r) => r.scratch === cur.id);
      if (recent) showRecent(recent, t);
    }
    if (st.editor && st.editor.doc === cur.sql) t.editor = st.editor;
    built.push(t);
  }
  if (seq !== openSeq || !built.length) return;
  resetTabs();
  SC.tabs = built;
  const activeIdx = Math.min(Math.max(v.activeIndex || 0, 0), built.length - 1);
  SC.activeTab = built[activeIdx].key;
  const act = built[activeIdx];
  if (act.cur && act.cur.id) SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === act.cur.id));
  redraw();
  let tries = 0;
  const put = () => {
    if (!ed) { if (++tries < 40) setTimeout(put, 25); return; }
    if (activeTab() === act) {
      applyEditor(act);
      scheduleDry(0, act);
      if (SC.focus === 'editor') ed.focus();
    }
  };
  put();
}

part('scratch', saveScratchWorkspace, restoreScratchWorkspace);

hooks.scratchFlush = () => (SC.open ? flushAllSaves() : Promise.resolve());
hooks.scratchRunning = () => SC.open && anyRunning();
hooks.scratchCancel = () => cancelAllRuns();

hooks.startScratch = (standalone) => {
  SC.standalone = !!standalone;
  SC.whOpen = false;
  const sb = S.boot && S.boot.scratch;
  SC.dbx = sb && onDemand()
    ? { profile: sb.profile || '', host: sb.host || '', warehouse: sb.warehouse || '', hasProfiles: !!sb.hasProfiles, needsProfile: !!sb.needsProfile }
    : null;
  S.overlay = null;
  openScratchView(null, { draft: true }).then(() => {
    if (SC.standalone && onDemand() && SC.dbx && SC.dbx.needsProfile) openProfilePicker();
  });
};
hooks.dbxUpdated = (u) => {
  if (!u) return;
  const workspaceChanged = u.profile !== undefined || u.host !== undefined;
  if (!SC.dbx) SC.dbx = { profile: '', host: '', warehouse: '', hasProfiles: false, needsProfile: false };
  if (u.profile !== undefined) SC.dbx.profile = u.profile;
  if (u.host !== undefined) SC.dbx.host = u.host;
  if (u.warehouse !== undefined) SC.dbx.warehouse = u.warehouse;
  if (u.needsProfile !== undefined) SC.dbx.needsProfile = u.needsProfile;
  if (workspaceChanged) {
    projectsSeq++;
    projectsLoading = false;
    SC.projects = null;
    clearMeta();
    for (const t of SC.tabs) {
      if (!isRunning(t)) {
        t.run = null;
        t.sel = null;
        t.filters = {};
        t.sort = null;
      }
    }
    loadRecent();
  }
  redraw();
};

export function toggleWarehouseMenu() {
  if (!SC.standalone || !S.scratchWarehouses || S.scratchWarehouses.length < 2) return;
  SC.whOpen = !SC.whOpen;
  if (SC.whOpen) {
    const cur = (S.caps && S.caps.warehouse) || 'bigquery';
    SC.whCursor = Math.max(0, S.scratchWarehouses.findIndex((w) => w.id !== cur));
    if (ed) ed.blur();
  }
  redraw();
}

export function closeWarehouseMenu() {
  SC.whOpen = false;
  redraw();
}

export function switchStandaloneWarehouse(id, flip) {
  SC.whOpen = false;
  flushAllSaves().then(() => openScratchWarehouse(id, flip));
}

function warehouseMenuKey(k) {
  const list = S.scratchWarehouses || [];
  switch (k) {
    case 'esc': case 'q': closeWarehouseMenu(); break;
    case 'j': case 'down': SC.whCursor = Math.min(SC.whCursor + 1, Math.max(list.length - 1, 0)); break;
    case 'k': case 'up': SC.whCursor = Math.max(SC.whCursor - 1, 0); break;
    case 'enter': if (list[SC.whCursor]) switchStandaloneWarehouse(list[SC.whCursor].id, false); break;
    case '1': case 'b': switchStandaloneWarehouse('bigquery', false); break;
    case '2': case 'd': switchStandaloneWarehouse('databricks', false); break;
  }
}
hooks.openScratch = openScratchView;
hooks.openLastScratch = openLastScratch;
hooks.openScratchPopout = openScratchPopout;
hooks.popoutScratch = popoutScratch;
hooks.importDockedTabs = importDockedTabs;
hooks.scratchPopout = () => SC.popout;
hooks.askNew = askNew;
hooks.scratchKey = scratchKey;
hooks.scratchNewKey = scratchNewKey;
hooks.scratchOpen = () => SC.open;
hooks.scratchCommands = scratchCommands;
hooks.scratchReturn = scratchReturn;
hooks.scratchEditing = () => SC.open && SC.focus === 'editor';

// ---- column filters ----

// viewRun is the run whose rows the grid shows: the search of all rows
// while it is on, else the run's own.
export function viewRun(ctx = SC) {
  const run = resolveCtx(ctx).run;
  return run && run.sub && run.subOn ? run.sub : run;
}

// cellText is a cell as text to look in: nested values as JSON with their
// field names, BYTES as base64.
function cellText(f, v) {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  if (baseType(f) === 'BYTES' && f.mode !== 'REPEATED') return v.b64;
  return JSON.stringify(named(f, v));
}

// activeFilters are the filters with something typed, as [column, text].
export function activeFilters(ctx = SC) {
  return Object.entries(resolveCtx(ctx).filters).filter(([, t]) => t !== '').map(([c, t]) => [Number(c), t]);
}

const filtersKey = (ctx = SC) => {
  const c = resolveCtx(ctx);
  return JSON.stringify([activeFilters(c), c.sort]);
};

export function setFilter(c, text, ctx = SC) {
  const t = resolveCtx(ctx);
  if (text) t.filters[c] = text; else delete t.filters[c];
  const run = t.run;
  if (run && run.sub && run.sub.key !== filtersKey(t)) run.subOn = false; // the search no longer matches the boxes
  t.scrollTop = 0;
  autoSearch(t);
  redraw();
}

export function clearFilters(ctx = SC) {
  const t = resolveCtx(ctx);
  t.filters = {};
  const run = t.run;
  if (run) { dropSearch(run); run.subOn = false; run.sub = null; }
  t.scrollTop = 0;
  redraw();
}

// ---- searching every row, as the filters change ----

const SEARCH_WAIT = 400; // ms after the last change to a filter

// autoSearch searches every row once the filters stop changing, when only
// some of the rows are loaded: the loaded rows alone would say too little.
// A search of a result table at or over the confirm size waits for the
// user to ask, through the link or enter.
function autoSearch(ctx = SC) {
  const t = resolveCtx(ctx);
  const run = t.run;
  if (!run) return;
  dropSearch(run);
  if (!run.next || !run.rowsJob || !activeFilters(t).length) return;
  if (run.sub && run.sub.key === filtersKey(t) && !['failed', 'cancelled'].includes(run.sub.phase)) { run.subOn = true; return; } // back to what was searched
  if (run.searchBytes !== undefined) return; // too big to search unasked
  run.searchTimer = setTimeout(() => { run.searchTimer = null; if (t.run === run) searchAll(false, true, t); }, SEARCH_WAIT);
  run.searchPending = true;
}

function dropSearch(run) {
  clearTimeout(run.searchTimer);
  run.searchTimer = null;
  run.searchPending = false;
}

// searchNow searches every row at once, as enter in a filter asks.
export function searchNow(ctx = SC) {
  const t = resolveCtx(ctx);
  const run = t.run;
  if (!run || !run.next) return;
  dropSearch(run);
  if (run.sub && run.subOn && run.sub.key === filtersKey(t)) return;
  searchAll(false, false, t);
}

// lowerCol is the lower-cased text of a column of the loaded rows, kept
// and extended as rows load.
function lowerCol(run, c) {
  run.lc = run.lc || {};
  const col = run.lc[c] = run.lc[c] || [];
  const f = run.schema[c];
  for (let i = col.length; i < run.rows.length; i++) col.push(cellText(f, run.rows[i][c]).toLowerCase());
  return col;
}

// ---- sorting ----

const SORTABLE = new Set(['INT64', 'FLOAT64', 'NUMERIC', 'BIGNUMERIC', 'BOOL', 'STRING', 'BYTES', 'DATE', 'TIME', 'DATETIME', 'TIMESTAMP']);
// isSortable says BigQuery can order by a column, so the grid can.
export const isSortable = (f) => f.mode !== 'REPEATED' && SORTABLE.has(baseType(f));

// cmpDecimal orders two decimal strings without turning them into floats,
// so INT64, NUMERIC and BIGNUMERIC keep every digit.
function cmpDecimal(a, b) {
  const na = a[0] === '-', nb = b[0] === '-';
  if (na !== nb) return na ? -1 : 1;
  const parts = (x) => {
    const [i, f = ''] = x.replace(/^[-+]/, '').split('.');
    return [i.replace(/^0+(?=\d)/, ''), f];
  };
  const [ia, fa] = parts(a), [ib, fb] = parts(b);
  let r = ia.length !== ib.length ? (ia.length < ib.length ? -1 : 1) : ia < ib ? -1 : ia > ib ? 1 : 0;
  if (!r) {
    const w = Math.max(fa.length, fb.length), x = fa.padEnd(w, '0'), y = fb.padEnd(w, '0');
    r = x < y ? -1 : x > y ? 1 : 0;
  }
  return na ? -r : r;
}

// compareCells orders two cells of a column as BigQuery's ORDER BY does:
// NULL first, then NaN, then the values.
export function compareCells(f, a, b) {
  if (a === null || a === undefined) return b === null || b === undefined ? 0 : -1;
  if (b === null || b === undefined) return 1;
  switch (baseType(f)) {
    case 'INT64': case 'NUMERIC': case 'BIGNUMERIC': return cmpDecimal(a, b);
    case 'FLOAT64': {
      const x = Number(a), y = Number(b);
      if (Number.isNaN(x) || Number.isNaN(y)) return Number.isNaN(x) === Number.isNaN(y) ? 0 : Number.isNaN(x) ? -1 : 1;
      return x < y ? -1 : x > y ? 1 : 0;
    }
    case 'BYTES': { // byte by byte, not as base64
      const x = atob(a.b64), y = atob(b.b64);
      return x < y ? -1 : x > y ? 1 : 0;
    }
    default: return a < b ? -1 : a > b ? 1 : 0; // dates and times read in order as text
  }
}

// setSort cycles a column's sort: ascending, descending, none.
export function setSort(c, ctx = SC) {
  const t = resolveCtx(ctx);
  const run = viewRun(t) || t.run;
  if (!run || !run.schema) return;
  if (!isSortable(run.schema[c])) { say(`BigQuery can't order by a ${typeLabel(run.schema[c]).toLowerCase()} column`); return; }
  if (!t.sort || t.sort.c !== c) t.sort = { c, desc: false };
  else if (!t.sort.desc) t.sort = { c, desc: true };
  else t.sort = null;
  const main = t.run;
  if (main && main.sub && main.sub.key !== filtersKey(t)) main.subOn = false; // the search no longer matches
  t.scrollTop = 0;
  autoSearch(t);
  redraw();
}

export function clearSort(ctx = SC) {
  const t = resolveCtx(ctx);
  t.sort = null;
  const main = t.run;
  if (main && main.sub && main.sub.key !== filtersKey(t)) main.subOn = false;
  autoSearch(t);
  redraw();
}

// visibleRows lists, as indexes into run.rows, the loaded rows that hold
// every filter's text (any case), in the sort's order; null when there is
// neither, or the rows are the server's, already searched and sorted.
export function visibleRows(run, ctx = SC) {
  const t = resolveCtx(ctx);
  const active = activeFilters(t);
  const sort = t.sort;
  if (!run || !run.schema || (!active.length && !sort) || run === (t.run && t.run.sub)) return null;
  const key = run.rows.length + '|' + filtersKey(t);
  if (run.visKey === key) return run.vis;
  const cols = active.map(([c, txt]) => [lowerCol(run, c), txt.toLowerCase()]);
  const out = [];
  for (let i = 0; i < run.rows.length; i++) if (cols.every(([col, txt]) => col[i].includes(txt))) out.push(i);
  if (sort) {
    const f = run.schema[sort.c], rows = run.rows, sign = sort.desc ? -1 : 1;
    out.sort((x, y) => sign * compareCells(f, rows[x][sort.c], rows[y][sort.c]));
  }
  run.visKey = key; run.vis = out;
  return out;
}

// searchAll searches every row of the result in BigQuery, over the table
// it keeps the result in; confirm says the user was asked, auto that they
// were not: an unasked search too big to run without asking leaves the
// question for when they ask. A reply to a search the filters have since
// moved past is dropped.
export async function searchAll(confirm, auto, ctx = SC) {
  const t = resolveCtx(ctx);
  const run = t.run;
  const terms = activeFilters(t);
  const sort = t.sort;
  if (!run || !run.rowsJob || !run.schema || (!terms.length && !sort)) return;
  if (onDemand()) return; // Databricks and Snowflake keep no table of a result to search
  dropSearch(run);
  const job = run.rowsJob;
  const key = filtersKey(t);
  const seq = run.searchSeq = (run.searchSeq || 0) + 1;
  const stale = () => t.closed || t.run !== run || run.searchSeq !== seq || filtersKey(t) !== key;
  run.searchPending = true;
  let res;
  try {
    res = await post('/api/scratch/filter', {
      project: job.project, location: job.location, id: job.id, confirm: !!confirm,
      filters: terms.map(([c, txt]) => ({ column: run.schema[c].name, value: txt })),
      sort: sort ? [{ column: run.schema[sort.c].name, desc: sort.desc }] : [],
    });
  } catch (e) {
    if (stale()) return;
    run.searchPending = false;
    say(e.message);
    return;
  }
  if (stale()) {
    if (res.job) post('/api/scratch/cancel', res.job).catch(() => {});
    return;
  }
  run.searchPending = false;
  if (res.needsConfirm) run.searchBytes = res.bytes;
  if (res.needsConfirm && auto) { redraw(); return; }
  if (res.needsConfirm) {
    t.confirm = { action: 'searchAll', why: [`${terms.length ? 'Searching' : 'Sorting'} every row scans the result's table, ${fmtBytes(res.bytes)}.`], danger: true };
    if (t === activeTab()) {
      if (ed) ed.blur();
      SC.focus = 'results';
    }
    redraw();
    return;
  }
  const prev = run.sub;
  if (prev && prev.job && ['starting', 'running'].includes(prev.phase)) post('/api/scratch/cancel', prev.job).catch(() => {});
  const sub = newRun('(search)', t);
  sub.job = res.job;
  sub.phase = 'running';
  sub.key = key;
  run.sub = sub; run.subOn = true;
  t.scrollTop = 0; t.sel = null;
  redraw();
  poll(sub);
}

export function showLoadedRows(ctx = SC) {
  const t = resolveCtx(ctx);
  const run = t.run;
  if (run) { dropSearch(run); run.subOn = false; }
  t.scrollTop = 0;
  redraw();
}
