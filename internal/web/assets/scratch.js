// The Scratch view's state and what changes it: the scratch files, the
// dry run of the SQL being edited, and the query job with its pages of
// rows. The view itself is scratch-view.js; the editor is scratch-editor.js.

import { S, redraw, get, post, copy, hooks, fader, onMac, openLink, openProjects } from './state.js';
import { openThemePicker, themeLabel } from './theme.js';
import { part } from './workspace.js';

export const GiB = 1024 ** 3;
const ROW_H = 22;
export { ROW_H };

export const SC = {
  open: false,
  list: [],
  cur: null,         // the scratch being edited: { id, from, project, location, sql }
  listCursor: 0,
  delAsk: '',        // the scratch d asked to delete
  focus: 'results',  // list, editor, results
  dry: null,         // the dry run of the scope: { sql, pending, ... }
  run: null,         // the query run: see newRun
  confirm: null,     // what the run is asked to confirm: { sql, why: [...] }
  newForm: null,     // { busy, error } while the New scratch box is up
  flash: '',
  sel: null,         // the selected cell: { r, c }
  scrollTop: 0,
  listOpen: false,     // the scratch selector, cmd+b
  hideResults: false,  // cmd+j hides the results as a terminal panel hides
  defaults: null,      // the project and location a scratch with no model runs in
  standalone: false,   // no project behind the page: Scratch is all of it
  filters: {},         // the results' column filters: column index -> text
  sort: null,          // the results' sort: { c: column index, desc }
  recent: [],          // the last runs that returned rows, newest first (scratchruns.go)
  recentOpen: '',      // set while the recent results menu is up: where the keys go after ('editor', 'results')
  recentCursor: 0,
  size: loadSize(),    // the panels as dragged, in px (0: its own size): list width, editor height, detail width
};
S.scratch = SC;

const sizeKey = 'xf-web:scratch:size';
function loadSize() {
  const z = { list: 0, editor: 0, detail: 0 };
  try {
    const v = JSON.parse(localStorage.getItem(sizeKey) || '{}');
    for (const k of Object.keys(z)) if (v[k] > 0) z[k] = v[k];
  } catch { /* no storage */ }
  return z;
}

// setSize keeps a panel's dragged size, 0 to give it its own again.
export function setSize(which, px) {
  SC.size[which] = px;
  try { localStorage.setItem(sizeKey, JSON.stringify(SC.size)); } catch { /* no storage */ }
}

let ed = null; // the editor: { doc(), scope(), block(), set(text), focus(), blur() }
export function setEditor(e) { ed = e; }
export function editor() { return ed; }

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
// the newest. With draft it opens on a new empty one, whose file is made
// when it is first typed in.
export async function openScratchView(id, opts = {}) {
  if (!S.caps.scratch) { S.flash = 'scratch queries need BigQuery access, which this project lacks'; redraw(); return; }
  SC.open = true;
  SC.focus = 'results';
  await Promise.all([loadList(), loadDefaults(), loadRecent()]);
  if (opts.draft) {
    await newDraft();
    return;
  }
  const want = id || (SC.cur && SC.cur.id) || (SC.list[0] && SC.list[0].id);
  if (want) await selectScratch(want, true);
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

// selectScratch opens a scratch in the editor. Opening the one already
// open reads it again from disk only when force says to, since a file
// edited elsewhere is read on opening the view, and keeps the result it
// shows. Another scratch shows its latest recent result, if it has one,
// or the recent run given.
export async function selectScratch(id, force, show) {
  if (SC.cur && SC.cur.id === id && !force) return;
  await flushSave();
  try {
    const same = SC.cur && SC.cur.id === id && SC.run;
    const f = await get('/api/scratch/file?id=' + encodeURIComponent(id));
    SC.cur = f;
    SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === id));
    SC.dry = null;
    if (!same) {
      SC.run = null;
      SC.sel = null;
      SC.filters = {}; SC.sort = null;
      const saved = show || SC.recent.find((r) => r.scratch === id);
      if (saved) showRecent(saved);
    }
    if (ed) ed.set(f.sql);
    scheduleDry(0);
  } catch (e) { say(e.message); }
  redraw();
}

export async function closeScratchView() {
  if (SC.standalone) return; // there is no project page to go back to
  await flushSave();
  SC.open = false;
  SC.confirm = null;
  if (ed) ed.blur();
  redraw();
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
  if (!SC.run) { say('nothing to show yet: run a query first'); return; }
  SC.hideResults = !SC.hideResults;
  // Hiding the results gives the keys back to the editor, in normal mode,
  // wherever they were: the results, the SQL tab or insert mode.
  if (SC.hideResults && ed) {
    ed.normal();
    focusEditor();
  }
  redraw();
}

// setResultTab shows the run's rows ('rows') or the SQL it ran ('sql').
// The tab is the run's own, so a new run opens on its rows.
export function setResultTab(tab) {
  if (!SC.run) return;
  SC.run.tab = tab;
  redraw();
}

// newDraft opens an empty scratch with no file yet: the file is made when
// there is something in it.
export async function newDraft() {
  await flushSave();
  if (!SC.defaults) await loadDefaults();
  SC.cur = { id: '', draft: true, from: '', project: SC.defaults.project, location: SC.defaults.location, sql: '' };
  SC.dry = null; SC.run = null; SC.sel = null; SC.filters = {}; SC.sort = null;
  if (ed) ed.set('');
  focusEditor();
  redraw();
}

// ensureFile makes a draft's file, and resolves once it is there.
export function ensureFile() {
  const cur = SC.cur;
  if (!cur || !cur.draft) return Promise.resolve(cur);
  if (!cur.making) {
    cur.making = post('/api/scratch/create', { form: 'empty', project: cur.project, location: cur.location }).then(async (f) => {
      cur.id = f.id; cur.project = f.project; cur.location = f.location; cur.draft = false;
      await loadList();
      SC.listCursor = Math.max(0, SC.list.findIndex((s) => s.id === f.id));
      return cur;
    }).catch((e) => { cur.making = null; say('could not make the scratch: ' + e.message); throw e; });
  }
  return cur.making;
}

// setTarget changes the project and location a scratch runs in.
export async function setTarget(project, location) {
  const cur = SC.cur;
  if (!cur || (cur.project === project && cur.location === location)) return;
  cur.project = project; cur.location = location;
  if (!cur.draft) {
    try { await post('/api/scratch/target', { id: cur.id, project, location }); } catch (e) { say(e.message); }
  }
  clearMeta();
  SC.dry = null;
  scheduleDry(0);
  redraw();
}

// refreshMeta forgets what is known of datasets and tables, here and in xf.
export async function refreshMeta() {
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
    SC.cur = null;
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
    if (SC.cur && SC.cur.id === id) { SC.cur = null; SC.dry = null; SC.run = null; if (ed) ed.set(''); }
    SC.delAsk = '';
    SC.recent = SC.recent.filter((r) => r.scratch !== id);
    await loadList();
    if (!SC.cur && SC.list[0]) await selectScratch(SC.list[0].id);
  } catch (e) { say(e.message); }
  redraw();
}

// ---- saving ----

let saveTimer = 0, saveSeq = 0;
export function onEdit() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flushSave, 600);
  scheduleDry(500);
}

export async function flushSave() {
  clearTimeout(saveTimer);
  saveTimer = 0;
  if (!SC.cur || !ed) return;
  const sql = ed.doc();
  if (sql === SC.cur.sql) return;
  if (SC.cur.draft) { // the file is made when there is something to keep
    if (!sql.trim()) return;
    try { await ensureFile(); } catch { return; }
  }
  const id = SC.cur.id, seq = ++saveSeq;
  SC.cur.sql = sql;
  try {
    await post('/api/scratch/save', { id, sql });
    const row = SC.list.find((s) => s.id === id);
    if (row && seq === saveSeq) row.preview = firstLine(sql);
  } catch (e) { say('could not save: ' + e.message); }
}

function firstLine(sql) {
  for (const l of sql.split('\n')) {
    const t = l.trim();
    if (t && !t.startsWith('--')) return t.length > 100 ? t.slice(0, 100) + '…' : t;
  }
  return '';
}

// ---- the dry run ----

let dryTimer = 0, drySeq = 0;
export function scheduleDry(ms) {
  clearTimeout(dryTimer);
  dryTimer = setTimeout(() => { dryTimer = 0; dryRunNow(); }, ms);
}

// dryRunNow dry-runs the scope, the SQL the next run would run, and
// resolves with the answer. Edits made while it runs start a later one;
// this one's answer is then not shown.
export async function dryRunNow() {
  if (!SC.cur || !ed || ed.block()) return null;
  const sql = ed.scope();
  if (!sql.trim()) { SC.dry = null; redraw(); return null; }
  if (SC.dry && SC.dry.sql === sql && !SC.dry.stale) return SC.dryPromise || SC.dry;
  const seq = ++drySeq;
  const keep = SC.dry && SC.dry.sql !== sql ? { ...SC.dry, stale: true } : SC.dry;
  SC.dry = { ...(keep || {}), sql, pending: true };
  redraw();
  const base = ed.scopeOffset();
  const p = post('/api/scratch/dryrun', { id: SC.cur.id, sql, project: SC.cur.project, location: SC.cur.location }).then((r) => {
    const out = { ...r, sql, pending: false };
    if (seq === drySeq) {
      SC.dry = out;
      if (ed && ed.scope() === sql) markError(out.ok ? '' : out.error, sql, base);
      redraw();
    }
    return out;
  }).catch((e) => {
    const out = { ok: false, error: e.message, sql, pending: false, bytes: 0, writes: false };
    if (seq === drySeq) { SC.dry = out; redraw(); }
    return out;
  });
  SC.dryPromise = p;
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
function showRecent(saved) {
  const run = SC.run = newRun(saved.sql);
  run.job = saved.job;
  run.scratch = saved.scratch;
  run.started = Date.parse(saved.started) || Date.now();
  run.restored = Date.parse(saved.ended) || run.started;
  run.phase = 'running';
  SC.hideResults = false;
  SC.sel = null; SC.filters = {}; SC.sort = null; SC.scrollTop = 0;
  poll(run);
}

// openRecent opens the scratch a recent run came from, on that run.
export async function openRecent(saved) {
  SC.recentOpen = '';
  if (!saved) { redraw(); return; }
  if (SC.cur && SC.cur.id === saved.scratch) {
    if (!SC.run || !SC.run.job || SC.run.job.id !== saved.job.id) showRecent(saved);
    redraw();
    return;
  }
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

// A result's view: SC for Scratch's, or one the dependency inspector
// keeps per row (resultView). Each holds the run it shows and the grid's
// filters, sort, scroll and selected cell; the functions below take one,
// Scratch's when not given. ctxOf is the view a run belongs to, outside
// the run, which a snapshot turns into JSON.
const ctxOf = new WeakMap();

// resultView is a new view of no run, for a result shown outside Scratch.
export const resultView = () => ({ run: null, filters: {}, sort: null, scrollTop: 0, sel: null, confirm: null, size: SC.size });

function newRun(sql, ctx = SC) {
  const run = {
    sql, phase: 'starting', job: null, status: null, started: Date.now(), ended: 0, error: '',
    schema: null, rows: [], next: '', total: 0, loadingMore: false, children: [], child: '', seq: ++runSeq,
  };
  ctxOf.set(run, ctx);
  return run;
}

// showJob shows the rows of a job that has run, or is running, in ctx,
// read from BigQuery as a recent run's are: the dependency inspector's
// rows' runs.
export function showJob(ctx, job, sql) {
  const run = ctx.run = newRun(sql, ctx);
  run.job = job;
  run.restored = Date.now();
  run.phase = 'running';
  ctx.sel = null; ctx.filters = {}; ctx.sort = null; ctx.scrollTop = 0;
  poll(run);
}

// runScratch runs the scope, asking first when the dry run says it costs a
// lot or changes data. force skips the asking.
export async function runScratch(force) {
  if (!SC.cur || !ed) return;
  if (SC.run && ['starting', 'running'].includes(SC.run.phase)) { say('a query is running: cancel it first (ctrl+c)'); return; }
  if (ed.block()) { say('a ctrl+v block can\'t run: select with v or V to run part of the file'); return; }
  const sql = ed.scope();
  if (!sql.trim()) { say('nothing to run'); return; }
  try { await ensureFile(); } catch { return; }
  await flushSave();
  if (!force) {
    const d = await dryRunNow();
    if (d && d.ok) {
      const why = [];
      if (d.bytes >= d.confirmBytes) why.push(`The dry run says this scans ${fmtBytes(d.bytes)}${d.cost ? ` (≈ ${d.cost} on demand)` : ''}.`);
      if (d.writes) why.push(`This is ${d.statementType === 'SCRIPT' ? 'a script, which may change data' : 'a ' + d.statementType + ' statement, which changes data'}${d.tables && d.tables.length ? ': it reads ' + d.tables.slice(0, 3).join(', ') + (d.tables.length > 3 ? '…' : '') : ''}.`);
      if (why.length) {
        // The prompt takes its answer from the page's keys, which the
        // editor would otherwise swallow.
        ed.blur();
        SC.focus = 'results';
        SC.confirm = { sql, why, danger: d.bytes >= d.confirmBytes || d.writes };
        redraw();
        return;
      }
    }
  }
  SC.confirm = null;
  SC.hideResults = false;
  const run = SC.run = newRun(sql);
  run.base = ed.scopeOffset();
  run.scratch = SC.cur.id;
  run.record = true;
  SC.sel = null;
  SC.filters = {}; SC.sort = null;
  SC.scrollTop = 0;
  redraw();
  try {
    run.job = await post('/api/scratch/run', { id: SC.cur.id, sql });
  } catch (e) {
    run.phase = 'failed'; run.error = e.message; run.ended = Date.now();
    redraw();
    return;
  }
  run.phase = 'running';
  redraw();
  poll(run);
}

// live says run is still what the page shows: the run, or the search of
// all its rows.
const live = (run) => {
  const c = ctxOf.get(run) || SC;
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
      SC.flash = '';
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
  const run = viewRun(ctx);
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
export async function showChild(child) {
  const run = SC.run;
  if (!run || run.phase !== 'done') return;
  run.child = child.id;
  run.schema = null; run.rows = []; run.next = ''; run.total = 0;
  run.sub = null; run.subOn = false; run.lc = null;
  SC.sel = null; SC.scrollTop = 0; SC.filters = {}; SC.sort = null;
  redraw();
  if (child.hasRows) {
    run.phase = 'loading';
    await loadFirst(run, { ...run.job, id: child.id });
    if (SC.run === run && run.phase === 'loading') run.phase = 'done';
  }
  redraw();
}

export const isRunning = () => !!SC.run && ['starting', 'running'].includes(SC.run.phase);

export async function cancelRun() {
  const run = SC.run;
  if (!run || !run.job || !['starting', 'running'].includes(run.phase)) { return false; }
  try {
    await post('/api/scratch/cancel', run.job);
    say('cancelling…');
  } catch (e) { say(e.message); }
  return true;
}

export function confirmRun() {
  if (SC.confirm && SC.confirm.action === 'searchAll') { SC.confirm = null; return searchAll(true); }
  return runScratch(true);
}
export function dismissConfirm() { SC.confirm = null; redraw(); }

// ---- the result ----

export function jobURL(job) {
  if (!job) return '';
  const loc = job.location ? job.location : '';
  return `https://console.cloud.google.com/bigquery?project=${encodeURIComponent(job.project)}&j=bq:${encodeURIComponent(loc)}:${encodeURIComponent(job.id)}&page=queryresults`;
}

export function csvURL() {
  const run = viewRun();
  if (!run || !run.rowsJob || !run.schema || !run.schema.length) return '';
  return jobQuery('/api/scratch/csv', run.rowsJob, { name: SC.cur ? SC.cur.id : 'result' });
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

export function copyCell(r, c, ctx = SC) {
  const run = viewRun(ctx);
  if (!run || !run.schema) return;
  copy(plain(run.schema[c], run.rows[r][c]), 'copied cell');
}

export function copyRow(r, ctx = SC) {
  const run = viewRun(ctx);
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
  SC.flash = '';
  switch (k) {
    case 'ctrl+c': if (isRunning()) cancelRun(); return true;
    case 'i': case 'enter': focusEditor(); return true;
    case 'r': runScratch(); return true;
    case 'j': case 'down': moveScratch(1); return true;
    case 'k': case 'up': moveScratch(-1); return true;
    case 'n': askNew(); return true;
    case 'b': toggleList(); return true;
    case 'h': toggleRecent(); return true;
    case 'd': if (SC.cur) { SC.delAsk = SC.cur.id; } return true;
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
  if (cur && !isRunning()) add('Run: query (the selection, else the file)', onMac ? '⌘↵' : 'ctrl+↵', () => runScratch());
  if (isRunning()) add('Run: cancel', 'ctrl+c', cancelRun);
  add('Scratch: new empty query', SC.standalone ? 'n' : '', newDraft);
  if (!SC.standalone && S.view && S.sel) add('Scratch: new query from the selected model', 'n', askNew, S.view.name);
  if (cur && !cur.draft) add('Scratch: delete this scratch', 'd', () => { toResults(); SC.delAsk = cur.id; }, cur.id);
  if (cur) add('Scratch: change its project or location', '', () => {
    toResults();
    setTimeout(() => { const el = document.querySelector('.sc-target-in'); if (el) { el.focus(); el.select(); } }, 0);
  }, [cur.project, cur.location].filter(Boolean).join(' · '));
  if (cur && ed && ed.format) add('Format: query with sqlfluff', ':format', () => ed.format());
  add('Tables: refresh what is known of tables and columns', ':refresh', refreshMeta);
  if (SC.recent.length) add('Results: recent results', 'h', toggleRecent, SC.recent.length + ' kept');
  if (shown && shown.phase === 'done' && csvURL()) add('Results: export CSV', '', downloadCSV);
  if (run && run.job) add('Results: open the job in BigQuery', '', () => openLink(jobURL(run.job)));
  if (run && run.next && run.rowsJob && (activeFilters().length || SC.sort)) add('Results: search all rows', '', () => searchAll(false), run.total ? run.total.toLocaleString() + ' rows' : '');
  if (activeFilters().length) add('Results: clear filters', '', clearFilters);
  if (SC.sort) add('Results: clear sort', '', clearSort);
  if (SC.sel) {
    add('Copy: cell', 'y', () => copyCell(SC.sel.r, SC.sel.c));
    add('Copy: row as JSON', 'Y', () => copyRow(SC.sel.r));
  }
  toggle('View: scratch list', SC.listOpen, onMac ? '⌘b' : 'ctrl+b', toggleList);
  if (run) toggle('View: results', !SC.hideResults, onMac ? '⌘j' : 'ctrl+j', toggleResults);
  add('View: colour theme', '', () => openThemePicker('theme'), themeLabel());
  if (hooks.openSettings) add('View: settings', 'ctrl+,', () => { flushSave().then(() => { hooks.openSettings(); redraw(); }); });
  if (cur) add('Go to: editor', 'i', focusEditor);
  if (run && !SC.hideResults) add('Go to: results', 'ctrl+w', toResults);
  if (!SC.standalone) {
    add('Project: switch or add', '', openProjects);
    add('Close scratch: back to the project', 'esc', closeScratchView, S.boot && S.boot.name);
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
  selectScratch(SC.list[i].id);
}

export function scratchNewKey(k) {
  if (!SC.newForm || SC.newForm.busy) return k !== 'esc' ? true : (cancelNew(), true);
  if (k === 'c' || k === '1') createScratch('compiled');
  else if (k === 's' || k === '2') createScratch('columns');
  else if (k === 'esc' || k === 'q') cancelNew();
  return true;
}

// startScratch is how xf scratch starts the page: on a new empty scratch.
// The Scratch view goes into the tab's workspace (workspace.js) while it
// is open: the scratch, its editor's text, cursor and undo history, the
// results' place, filters and sort, and a finished run's results unless
// they are big (light).
part('scratch', (light) => {
  if (!SC.open || SC.standalone || !SC.cur) return null;
  const done = SC.run && SC.run.phase === 'done' && !light ? SC.run : null;
  return {
    id: SC.cur.id, focus: SC.focus, listOpen: SC.listOpen, hideResults: SC.hideResults,
    filters: SC.filters, sort: SC.sort, sel: SC.sel, scrollTop: SC.scrollTop, run: done,
    editor: ed && !SC.cur.draft ? ed.save(light) : null,
  };
}, (v) => {
  if (!v.id) return;
  openScratchView(v.id).then(() => {
    if (!SC.cur || SC.cur.id !== v.id) return;
    Object.assign(SC, { focus: v.focus || SC.focus, listOpen: !!v.listOpen, hideResults: !!v.hideResults,
      filters: v.filters || {}, sort: v.sort || null, sel: v.sel || null, scrollTop: v.scrollTop || 0 });
    if (v.run && v.run.sql) SC.run = v.run;
    redraw();
    // The editor mounts as the view draws.
    let tries = 0;
    const put = () => {
      if (!ed) { if (++tries < 40) setTimeout(put, 25); return; }
      if (v.editor && v.editor.doc === ed.doc()) ed.restore(v.editor);
      if (SC.focus === 'editor') ed.focus();
    };
    put();
  });
});

hooks.scratchFlush = () => (SC.open ? flushSave() : Promise.resolve());

hooks.startScratch = (standalone) => {
  SC.standalone = !!standalone;
  S.overlay = null;
  openScratchView(null, { draft: true });
};
hooks.openScratch = openScratchView;
hooks.openLastScratch = openLastScratch;
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
  const run = ctx.run;
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
  return Object.entries(ctx.filters).filter(([, t]) => t !== '').map(([c, t]) => [Number(c), t]);
}

const filtersKey = (ctx = SC) => JSON.stringify([activeFilters(ctx), ctx.sort]);

export function setFilter(c, text, ctx = SC) {
  if (text) ctx.filters[c] = text; else delete ctx.filters[c];
  const run = ctx.run;
  if (run && run.sub && run.sub.key !== filtersKey(ctx)) run.subOn = false; // the search no longer matches the boxes
  ctx.scrollTop = 0;
  autoSearch(ctx);
  redraw();
}

export function clearFilters(ctx = SC) {
  ctx.filters = {};
  const run = ctx.run;
  if (run) { dropSearch(run); run.subOn = false; run.sub = null; }
  ctx.scrollTop = 0;
  redraw();
}

// ---- searching every row, as the filters change ----

const SEARCH_WAIT = 400; // ms after the last change to a filter

// autoSearch searches every row once the filters stop changing, when only
// some of the rows are loaded: the loaded rows alone would say too little.
// A search of a result table at or over the confirm size waits for the
// user to ask, through the link or enter.
function autoSearch(ctx = SC) {
  const run = ctx.run;
  if (!run) return;
  dropSearch(run);
  if (!run.next || !run.rowsJob || !activeFilters(ctx).length) return;
  if (run.sub && run.sub.key === filtersKey(ctx) && !['failed', 'cancelled'].includes(run.sub.phase)) { run.subOn = true; return; } // back to what was searched
  if (run.searchBytes !== undefined) return; // too big to search unasked
  run.searchTimer = setTimeout(() => { run.searchTimer = null; if (ctx.run === run) searchAll(false, true, ctx); }, SEARCH_WAIT);
  run.searchPending = true;
}

function dropSearch(run) {
  clearTimeout(run.searchTimer);
  run.searchTimer = null;
  run.searchPending = false;
}

// searchNow searches every row at once, as enter in a filter asks.
export function searchNow(ctx = SC) {
  const run = ctx.run;
  if (!run || !run.next) return;
  dropSearch(run);
  if (run.sub && run.subOn && run.sub.key === filtersKey(ctx)) return;
  searchAll(false, false, ctx);
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
  const run = viewRun(ctx) || ctx.run;
  if (!run || !run.schema) return;
  if (!isSortable(run.schema[c])) { say(`BigQuery can't order by a ${typeLabel(run.schema[c]).toLowerCase()} column`); return; }
  if (!ctx.sort || ctx.sort.c !== c) ctx.sort = { c, desc: false };
  else if (!ctx.sort.desc) ctx.sort = { c, desc: true };
  else ctx.sort = null;
  const main = ctx.run;
  if (main && main.sub && main.sub.key !== filtersKey(ctx)) main.subOn = false; // the search no longer matches
  ctx.scrollTop = 0;
  autoSearch(ctx);
  redraw();
}

export function clearSort(ctx = SC) {
  ctx.sort = null;
  const main = ctx.run;
  if (main && main.sub && main.sub.key !== filtersKey(ctx)) main.subOn = false;
  autoSearch(ctx);
  redraw();
}

// visibleRows lists, as indexes into run.rows, the loaded rows that hold
// every filter's text (any case), in the sort's order; null when there is
// neither, or the rows are the server's, already searched and sorted.
export function visibleRows(run, ctx = SC) {
  const active = activeFilters(ctx);
  const sort = ctx.sort;
  if (!run || !run.schema || (!active.length && !sort) || run === (ctx.run && ctx.run.sub)) return null;
  const key = run.rows.length + '|' + filtersKey(ctx);
  if (run.visKey === key) return run.vis;
  const cols = active.map(([c, t]) => [lowerCol(run, c), t.toLowerCase()]);
  const out = [];
  for (let i = 0; i < run.rows.length; i++) if (cols.every(([col, t]) => col[i].includes(t))) out.push(i);
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
  const run = ctx.run;
  const terms = activeFilters(ctx);
  const sort = ctx.sort;
  if (!run || !run.rowsJob || !run.schema || (!terms.length && !sort)) return;
  dropSearch(run);
  const job = run.rowsJob;
  const key = filtersKey(ctx);
  const seq = run.searchSeq = (run.searchSeq || 0) + 1;
  const stale = () => ctx.run !== run || run.searchSeq !== seq || filtersKey(ctx) !== key;
  run.searchPending = true;
  let res;
  try {
    res = await post('/api/scratch/filter', {
      project: job.project, location: job.location, id: job.id, confirm: !!confirm,
      filters: terms.map(([c, t]) => ({ column: run.schema[c].name, value: t })),
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
    ctx.confirm = { action: 'searchAll', why: [`${terms.length ? 'Searching' : 'Sorting'} every row scans the result's table, ${fmtBytes(res.bytes)}.`], danger: true };
    if (ctx === SC) {
      if (ed) ed.blur();
      SC.focus = 'results';
    }
    redraw();
    return;
  }
  const prev = run.sub;
  if (prev && prev.job && ['starting', 'running'].includes(prev.phase)) post('/api/scratch/cancel', prev.job).catch(() => {});
  const sub = newRun('(search)', ctx);
  sub.job = res.job;
  sub.phase = 'running';
  sub.key = key;
  run.sub = sub; run.subOn = true;
  ctx.scrollTop = 0; ctx.sel = null;
  redraw();
  poll(sub);
}

export function showLoadedRows(ctx = SC) {
  const run = ctx.run;
  if (run) { dropSearch(run); run.subOn = false; }
  ctx.scrollTop = 0;
  redraw();
}
