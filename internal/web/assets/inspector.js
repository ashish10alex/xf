// The dependency inspector (ADR 0033): a page that lists a model and the
// tables upstream of it, each with a filter, and dry runs or runs
// SELECT * FROM <table> WHERE <filter> on them. It is for the mouse; its
// keys are tab (rows ↔ graph), ctrl+c (cancel the runs), esc (close what
// is open on the page) and enter in a filter box (dry run it). ctrl+q
// leaves it, as it leaves every page (ADR 0030), but the page is kept as
// it was left: the palette goes back to it, and the tab's workspace keeps
// it over a reload. The server walks the graph, queues the runs and keeps
// the presets; this module keeps what the page shows. state.js doesn't
// import it: the palette, or a workspace that kept the page, loads it.

import { S, get, post, redraw, hooks, say, copy, openLink, selectAction } from './state.js';
import * as sc from './scratch.js';

export const IN = {
  open: false,
  model: '',        // the model's action ID
  shown: '',        // the model whose dry runs, results and selection these are
  v: null,          // inspectorView from the server
  loading: false,
  error: '',
  tab: 'rows',      // rows or graph
  sel: '',          // the selected row's key
  search: '',       // shows only the tables whose name or table has it
  tickedOnly: false, // shows only the ticked rows
  global: '',
  applyAll: true,   // the global filter is every row's
  preset: '',       // the saved preset being edited, '' for none
  description: '',
  rows: {},         // row key → { on, filter }
  dry: {},          // row key → the dry run: { pending, sql, ok, error, text, cost, bytes, accuracy }
  results: {},      // row key → its result's view (sc.resultView)
  presets: [],      // the model's presets, newest first
  presetsPath: '',
  presetsOpen: false,
  presetsAt: '',    // where the presets menu is, as a style
  graph: null,      // the graph's pan and zoom: { x, y, k, fit }
  confirm: null,    // Run all's or a row's question: { keys, pending, total, cost, noFilter, failed }
  schema: null,     // the side panel: { key, table, loading, fields, error }
  pick: null,       // the model picker: { query, items, cursor }
  focus: 'top',     // the section last clicked: top (rows or graph), result or schema
  zoom: '',         // the section z zoomed to fill the page, '' for none
};

// ---- opening and leaving ----

// openInspector opens the page on the model id; with none, it goes back
// to the page as it was left, else opens on the model selected.
export async function openInspector(id) {
  if (!S.caps.inspector) { say('the dependency inspector needs BigQuery access, which this project lacks'); return; }
  const model = id || IN.model || S.sel;
  if (!model) { say('select a model first'); return; }
  IN.open = true;
  S.inspector = IN;
  S.overlay = null; // the page draws none, and one left open would take its keys
  redraw();
  if (model === IN.model && IN.v) refresh();
  else await load(model);
}

export function closeInspector() {
  saveStateNow();
  IN.open = false;
  IN.confirm = null; IN.pick = null; IN.presetsOpen = false;
  S.inspector = null;
  redraw();
}

hooks.closeInspector = closeInspector;
hooks.inspectorKept = () => (IN.model ? { model: IN.model, name: IN.v ? IN.v.name : IN.model } : null);
hooks.inspectorSave = snapshot;
hooks.inspectorKey = inspectorKey;
hooks.inspectorEvent = (ev) => { if (IN.open && IN.v && ev && ev.key === IN.v.key) refresh(); };
hooks.inspectorDetail = () => { if (IN.open && IN.v && IN.v.defer) refresh(); };

// ---- the server's view ----

// load shows model at depth (its last, else 3), starting from its last
// state, else its latest preset, else nothing typed.
export async function load(model, depth) {
  const fresh = model !== IN.shown;
  if (fresh) saveStateNow();
  IN.loading = true;
  IN.error = '';
  redraw();
  let v;
  try {
    v = await get('/api/inspector?model=' + encodeURIComponent(model) + (depth ? '&depth=' + depth : ''));
  } catch (e) {
    IN.loading = false; IN.error = e.message; redraw();
    return;
  }
  IN.loading = false;
  if (v.error) { IN.error = v.error; IN.v = null; IN.model = model; redraw(); return; }
  IN.model = model;
  IN.v = v;
  if (fresh) {
    IN.confirm = null;
    pageOf(model);
    await loadPresets();
    if (v.state) fromState(v.state);
    else if (IN.presets.length) applyPreset(IN.presets[0], true);
    else { IN.global = ''; IN.applyAll = true; IN.preset = ''; IN.description = ''; IN.rows = {}; }
  }
  ensureRows();
  if (!IN.sel || !tableNodes().some((n) => n.key === IN.sel)) IN.sel = tableNodes().length ? tableNodes()[0].key : '';
  showResult(IN.sel);
  if (fresh || depth) saveState();
  redraw();
}

// Each model shown since the page loaded keeps what its page showed, so
// going back to it from another shows its dry runs and results again.
const pages = new Map();

// pageOf puts back model's page, or an empty one, keeping the one shown.
function pageOf(model) {
  const keep = (({ dry, results, sel, schema, search, graph }) => ({ dry, results, sel, schema, search, graph }))(IN);
  if (IN.shown) pages.set(IN.shown, keep);
  Object.assign(IN, pages.get(model) || { dry: {}, results: {}, sel: '', schema: null, search: '', graph: null });
  IN.shown = model;
}

// refresh asks again for the view of the model on show: a run moved on,
// or defer to prod worked out what it reads from prod.
let refreshing = null;
async function refresh() {
  if (refreshing) return;
  refreshing = (async () => {
    try {
      const v = await get('/api/inspector?model=' + encodeURIComponent(IN.model) + '&depth=' + IN.v.depth);
      if (!v.error && IN.v && v.key === IN.v.key) { IN.v = v; ensureRows(); showResult(IN.sel); }
    } catch { /* the next event asks again */ }
    refreshing = null;
    redraw();
  })();
}

// ensureRows gives each table a row that has none yet: ticked, with the
// global filter while it is every row's.
function ensureRows() {
  for (const n of tableNodes()) if (!IN.rows[n.key]) IN.rows[n.key] = { on: true, filter: IN.applyAll ? IN.global : '' };
}

// ---- tables added by hand ----

// addedKeys are the tables added to the page by hand, by their names
// before dev settings.
export const addedKeys = () => (IN.v && IN.v.state && IN.v.state.added) || [];

// setAdded sets them, with add (an action ID) too, and shows the page
// again; it reports the server's refusal, if any.
async function setAdded(added, add) {
  if (!IN.v) return '';
  let r;
  try { r = await post('/api/inspector/added', { key: IN.v.key, added, add: add || '' }); } catch (e) { r = { error: e.message }; }
  if (r.error) { say(r.error); return r.error; }
  try {
    const v = await get('/api/inspector?model=' + encodeURIComponent(IN.model) + '&depth=' + IN.v.depth);
    if (!v.error && IN.v && v.key === IN.v.key) { IN.v = v; ensureRows(); }
  } catch (e) { say(e.message); }
  redraw();
  return '';
}

// addTable adds the action of id to the page and selects its row; one
// listed already is ticked and selected, shown if the search hid it.
export async function addTable(id) {
  const had = nodes().find((n) => n.id === id);
  if (had) {
    if (!had.table) { say(had.name + ' has no table to query'); return; }
    setOn(had.key, true);
    if (!matches(had)) setSearch('');
    select(had.key);
    say('ticked ' + had.name + ', listed already');
    scrollToSel();
    return;
  }
  if (await setAdded(addedKeys(), id)) return;
  const n = nodes().find((x) => x.id === id);
  if (n) { select(n.key); say('added ' + n.name); scrollToSel(); }
}

// scrollToSel brings the selected row into view, once drawn.
function scrollToSel() {
  requestAnimationFrame(() => { const tr = document.querySelector('.ins-table tr.sel'); if (tr) tr.scrollIntoView({ block: 'nearest' }); });
}

export async function removeTable(key) {
  await setAdded(addedKeys().filter((k) => k !== key));
  if (IN.sel === key) select(tableNodes().length ? tableNodes()[0].key : '');
}

// forgetMissing drops the added tables no action names now.
export const forgetMissing = () => setAdded(addedKeys().filter((k) => !(IN.v.missing || []).includes(k)));

// nodes are the walk's, the model first; tableNodes those with a row.
export const nodes = () => (IN.v ? IN.v.nodes : []);
export const tableNodes = () => nodes().filter((n) => n.table);
export const nodeOf = (key) => nodes().find((n) => n.key === key);

// matches is whether node n has the search in its name, the table it is
// read from or its key, in any case, and with ticked only on, is ticked.
export function matches(n) {
  if (IN.tickedOnly && !(n.table && IN.rows[n.key] && IN.rows[n.key].on)) return false;
  const q = IN.search.trim().toLowerCase();
  return !q || [n.name, n.read, n.key].some((s) => (s || '').toLowerCase().includes(q));
}
export const shownNodes = () => tableNodes().filter(matches);

// narrowed is whether the search or ticked only leaves rows out.
export const narrowed = () => !!IN.search.trim() || IN.tickedOnly;

export function setSearch(text) { IN.search = text; redraw(); }

export function setTickedOnly(on) { IN.tickedOnly = on; redraw(); }

// setDepth walks again, deeper or shallower (1 to 20).
export function setDepth(d) {
  const depth = Math.min(Math.max(Number(d) || 1, 1), 20);
  if (IN.v && depth !== IN.v.depth) load(IN.model, depth);
}

// ---- what is typed ----

// filterOf is the filter a row's query uses.
export const filterOf = (key) => (IN.applyAll ? IN.global : (IN.rows[key] || {}).filter || '');

export const sqlOf = (key) => {
  const n = nodeOf(key);
  if (!n) return '';
  const f = filterOf(key).trim();
  return 'SELECT * FROM `' + n.read + '`' + (f ? ' WHERE ' + f : '');
};

// tidyFilter makes a filter easier to read: outside quotes, each run of
// spaces and tabs (as a list copied from a spreadsheet has) becomes one
// space, and a run with line breaks one line break, with no space inside
// brackets or before a comma and one after it. A filter with a comment is
// left as it is; trim takes the spaces off the ends too.
export function tidyFilter(text, trim) {
  let out = '', q = '';
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) {
      out += c;
      if (c === '\\' && i + 1 < text.length) out += text[++i];
      else if (c === q) q = '';
      continue;
    }
    if ((c === '-' && text[i + 1] === '-') || c === '#' || (c === '/' && text[i + 1] === '*')) return text;
    if (/\s/.test(c)) {
      const brk = c === '\n' || c === '\r';
      if (!out || out.endsWith('\n')) continue;
      if (out.endsWith(' ')) out = out.slice(0, -1);
      out += brk ? '\n' : ' ';
      continue;
    }
    if (c === ',' || c === ')') out = out.replace(/ $/, '');
    else if (out.endsWith('( ')) out = out.slice(0, -1);
    if (out.endsWith(',')) out += ' ';
    if (c === "'" || c === '"' || c === '`') q = c;
    out += c;
  }
  return trim ? out.trim() : out;
}

// pasteFilter puts the clipboard's text, tidied, in the box in place of
// what is selected, and gives the box's new text to set.
export function pasteFilter(e, set) {
  const text = e.clipboardData && e.clipboardData.getData('text/plain');
  if (!text) return;
  e.preventDefault();
  const el = e.currentTarget, a = el.selectionStart, b = el.selectionEnd;
  const before = tidyFilter(el.value.slice(0, a) + text, false);
  set(tidyFilter(before + el.value.slice(b), false));
  requestAnimationFrame(() => { const at = Math.min(el.value.length, before.length); el.setSelectionRange(at, at); });
}

// tidyAll tidies the global filter and each row's, as the global box is
// left: rows that took a copy of it are tidied with it.
export function tidyAll() {
  let changed = false;
  const tidy = (t) => { const n = tidyFilter(t, true); changed = changed || n !== t; return n; };
  IN.global = tidy(IN.global);
  for (const r of Object.values(IN.rows)) r.filter = tidy(r.filter);
  if (!changed) return;
  saveState();
  redraw();
}

export function setGlobal(text) {
  IN.global = text;
  if (IN.applyAll) for (const k of Object.keys(IN.rows)) IN.rows[k].filter = text;
  saveState();
  redraw();
}

// setApplyAll turns apply to all on or off; off, each row keeps the
// global filter it had, to change on its own.
export function setApplyAll(on) {
  IN.applyAll = on;
  for (const k of Object.keys(IN.rows)) IN.rows[k].filter = IN.global;
  saveState();
  redraw();
}

export function setRowFilter(key, text) {
  if (IN.applyAll || !IN.rows[key]) return;
  IN.rows[key].filter = text;
  saveState();
  redraw();
}

export function setOn(key, on) {
  if (!IN.rows[key]) return;
  IN.rows[key].on = on;
  saveState();
  redraw();
}

// setAllOn ticks or unticks the rows the search shows.
export function setAllOn(on) {
  for (const n of shownNodes()) IN.rows[n.key].on = on;
  saveState();
  redraw();
}

export function select(key) {
  IN.sel = key;
  if (IN.schema && IN.schema.key !== key) openSchema(key);
  showResult(key);
  redraw();
}

// ---- the session ----

let saveTimer = 0;
function saveState() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveStateNow, 400);
}

function saveStateNow() {
  clearTimeout(saveTimer);
  saveTimer = 0;
  if (!IN.v) return;
  const rows = {};
  for (const [k, r] of Object.entries(IN.rows)) rows[k] = { off: !r.on, filter: r.filter };
  post('/api/inspector/state', {
    key: IN.v.key,
    state: { depth: IN.v.depth, global: IN.global, applyAll: IN.applyAll, preset: IN.preset, rows },
  }).catch(() => {});
}

function fromState(st) {
  IN.global = st.global || '';
  IN.applyAll = !!st.applyAll;
  IN.preset = st.preset || '';
  const p = IN.presets.find((x) => x.id === IN.preset);
  IN.description = p ? p.description || '' : '';
  if (!p) IN.preset = '';
  IN.rows = {};
  for (const [k, r] of Object.entries(st.rows || {})) IN.rows[k] = { on: !r.off, filter: r.filter || '' };
}

// ---- the tab's workspace ----

// snapshot is what the workspace keeps of the page (state.js's inspector
// part): the results are read back from their jobs, and light, the dry
// runs are left to run again.
function snapshot(light) {
  if (!IN.model) return null;
  const dry = {};
  if (!light) for (const [k, d] of Object.entries(IN.dry)) if (!d.pending) dry[k] = d;
  return { open: IN.open, model: IN.model, tab: IN.tab, sel: IN.sel, search: IN.search, tickedOnly: IN.tickedOnly, graph: IN.graph, dry };
}

// restoreInspector puts back the page a workspace kept, open if it was;
// while the project compiles it waits for its graph.
export async function restoreInspector(w) {
  if (!S.caps.inspector || !w || !w.model) return;
  if (w.open) { IN.open = true; S.inspector = IN; }
  IN.tab = w.tab === 'graph' ? 'graph' : 'rows';
  IN.tickedOnly = !!w.tickedOnly;
  pages.set(w.model, { dry: w.dry || {}, results: {}, sel: w.sel || '', schema: null, search: w.search || '', graph: w.graph || null });
  for (let i = 0; i < 60; i++) {
    await load(w.model);
    if (IN.v || !/compiled yet/.test(IN.error)) break;
    await new Promise((ok) => setTimeout(ok, 1000));
  }
}

// ---- dry runs ----

export async function dryRun(key) {
  const n = nodeOf(key);
  if (!n) return null;
  const sql = sqlOf(key);
  const d = IN.dry[key] = { pending: true, sql };
  redraw();
  try {
    Object.assign(d, await post('/api/inspector/dryrun', { table: n.read, filter: filterOf(key) }));
  } catch (e) {
    d.ok = false; d.error = e.message;
  }
  d.pending = false;
  redraw();
  return d;
}

// dryOf is a row's dry run, while it is of the row's query as it is now.
export const dryOf = (key) => {
  const d = IN.dry[key];
  return d && d.sql === sqlOf(key) ? d : null;
};

export const ticked = () => tableNodes().filter((n) => IN.rows[n.key] && IN.rows[n.key].on).map((n) => n.key);

export async function dryRunAll() {
  const keys = ticked();
  if (!keys.length) { say('tick a row first'); return; }
  await Promise.all(keys.map(dryRun));
}

// ---- runs ----

export const runOf = (key) => (IN.v && IN.v.runs[key]) || null;
export const isRunning = (r) => !!r && (r.phase === 'queued' || r.phase === 'running');
export const anyRunning = () => !!IN.v && Object.values(IN.v.runs).some(isRunning);

// lastJob is a row's last run: this xf web's, else the one the session
// kept, while it is of the row's query as typed now or not.
export function lastJob(key) {
  const r = runOf(key);
  if (r) return r;
  const st = IN.v && IN.v.state && IN.v.state.rows && IN.v.state.rows[key];
  if (st && st.job) return { phase: st.job.error ? 'failed' : 'done', sql: st.job.sql, rows: st.job.rows, error: st.job.error, ended: st.job.ended, job: { project: st.job.project, location: st.job.location, id: st.job.id }, kept: true };
  return null;
}

// askRunAll dry runs the ticked rows and asks before running them, with
// what they would scan and cost.
export async function askRunAll() {
  const keys = ticked().filter((k) => !isRunning(runOf(k)));
  if (!keys.length) { say(ticked().length ? 'the ticked rows are running already' : 'tick a row first'); return; }
  await ask(keys);
}

// runRow runs a row's query at once, unless it has no filter: then it
// asks first, with its dry run.
export async function runRow(key) {
  if (isRunning(runOf(key))) return;
  if (!filterOf(key).trim()) { await ask([key]); return; }
  start([key]);
}

async function ask(keys) {
  const c = IN.confirm = { keys, pending: true };
  redraw();
  const ds = await Promise.all(keys.map((k) => dryOf(k) || dryRun(k)));
  if (IN.confirm !== c) return;
  c.pending = false;
  c.total = ds.reduce((a, d) => a + (d && d.ok ? d.bytes : 0), 0);
  c.unknown = ds.some((d) => d && d.ok && d.accuracy === 'UNKNOWN');
  c.cost = ds.reduce((a, d) => a + (d && d.cost ? Number(d.cost.replace(/[^0-9.]/g, '')) || 0 : 0), 0);
  c.noFilter = keys.filter((k) => !filterOf(k).trim());
  c.failed = keys.filter((_, i) => !ds[i] || !ds[i].ok);
  redraw();
}

export function confirmRun() {
  const c = IN.confirm;
  if (!c || c.pending) return;
  IN.confirm = null;
  const keys = c.keys.filter((k) => !c.failed.includes(k));
  if (keys.length) start(keys); else redraw();
}

export function dismissConfirm() { IN.confirm = null; redraw(); }

async function start(keys) {
  if (!IN.v) return;
  const rows = keys.map((k) => ({ key: k, table: nodeOf(k).read, filter: filterOf(k) }));
  try {
    const runs = await post('/api/inspector/run', { key: IN.v.key, rows });
    Object.assign(IN.v.runs, runs);
  } catch (e) { say(e.message); }
  redraw();
}

export async function cancelRuns(row) {
  if (!IN.v) return;
  try {
    const r = await post('/api/inspector/cancel', { key: IN.v.key, row: row || '' });
    say(r.cancelled ? `cancelling ${r.cancelled} quer${r.cancelled === 1 ? 'y' : 'ies'}…` : 'nothing is running');
  } catch (e) { say(e.message); }
}

// ---- results ----

// resultOf is a row's result's view.
export function resultOf(key) {
  if (!IN.results[key]) IN.results[key] = sc.resultView();
  return IN.results[key];
}

// showResult reads the rows of the row's last run, when it is done and
// its view doesn't show that job yet.
function showResult(key) {
  if (!key) return;
  const r = lastJob(key);
  if (!r || r.phase !== 'done' || !r.job) return;
  const ctx = resultOf(key);
  if (ctx.run && ctx.run.job && ctx.run.job.id === r.job.id) return;
  sc.showJob(ctx, r.job, r.sql);
}

const KEYWORDS = new Set(('select from where and or not in is null like between as on join left right inner outer cross full ' +
  'group by order having limit offset distinct case when then else end true false exists union all with asc desc interval ' +
  'cast safe_cast unnest struct array date datetime timestamp time if qualify over partition except replace').split(' '));

// sqlTokens splits a row's query into [class, text] pieces for the result's
// heading, with the classes chroma gives pane 2's SQL (t-k a keyword, t-s1
// a string…): its queries are a SELECT and a filter, so a few rules do.
export function sqlTokens(sql) {
  const out = [];
  const re = /(--[^\n]*|#[^\n]*|\/\*[\s\S]*?(?:\*\/|$))|('(?:\\.|[^'\\])*'?|"(?:\\.|[^"\\])*"?)|(`[^`]*`?)|(\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b)|([A-Za-z_][A-Za-z0-9_]*)(?=\s*\()|([A-Za-z_][A-Za-z0-9_]*)|([<>!=]=?|<>|\|\||[-+*\/%])|(\s+|.)/gy;
  for (let m; (m = re.exec(sql)) && m[0];) {
    const t = m[0];
    const c = m[1] ? 'c1' : m[2] ? 's1' : m[3] ? 'sb' : m[4] ? 'mi' : m[5] ? (KEYWORDS.has(t.toLowerCase()) ? 'k' : 'nf')
      : m[6] ? (KEYWORDS.has(t.toLowerCase()) ? 'k' : 'n') : m[7] ? 'o' : '';
    const last = out[out.length - 1];
    if (last && last[0] === c) last[1] += t; else out.push([c, t]);
  }
  return out;
}

// resultKeys are the rows the search shows that have run, in their order.
export const resultKeys = () => shownNodes().filter((n) => lastJob(n.key)).map((n) => n.key);

// cycleResult selects the next row with a result, or with dir -1 the one
// before, going round: the result's ‹ › and ⌥⌘→/← (ctrl+alt off a Mac).
export function cycleResult(dir) {
  const keys = resultKeys();
  if (!keys.length) { say('no row has run yet'); return; }
  const i = keys.indexOf(IN.sel);
  select(i < 0 ? keys[dir > 0 ? 0 : keys.length - 1] : keys[(i + dir + keys.length) % keys.length]);
  scrollToSel();
}

hooks.inspectorCycle = cycleResult;

// ---- the row's buttons ----

export function openInScratch(key) {
  const sql = sqlOf(key);
  if (!sql) return;
  closeInspector();
  (async () => {
    try {
      const f = await post('/api/scratch/create', { form: 'empty' });
      await post('/api/scratch/save', { id: f.id, sql });
      if (hooks.openScratch) await hooks.openScratch(f.id);
    } catch (e) { say(e.message); }
  })();
}

export const copyQuery = (key) => copy(sqlOf(key), 'copied the query');

export function openTable(key) {
  const n = nodeOf(key);
  if (n && n.url) openLink(n.url);
}

export function openFile(key) {
  const n = nodeOf(key);
  if (!n || !n.file) return;
  closeInspector();
  selectAction(n.id);
}

// ---- the schema panel ----

export async function openSchema(key) {
  const n = nodeOf(key);
  if (!n) return;
  const [project, dataset, ...rest] = n.read.split('.');
  const s = IN.schema = { key, table: n.read, loading: true };
  redraw();
  try {
    const card = await get('/api/scratch/meta/table?' + new URLSearchParams({ project, dataset, table: rest.join('.') }));
    if (IN.schema !== s) return;
    s.fields = card.detail ? card.detail.schema || [] : null;
    s.error = card.error || '';
  } catch (e) {
    if (IN.schema !== s) return;
    s.error = e.message;
  }
  s.loading = false;
  redraw();
}

export function closeSchema() {
  IN.schema = null;
  if (IN.focus === 'schema') IN.focus = 'top';
  if (IN.zoom === 'schema') IN.zoom = '';
  redraw();
}

// ---- zoom ----

// focusSection makes s the section z zooms, as clicking in it does.
export function focusSection(s) {
  if (IN.focus === s) return;
  IN.focus = s;
  redraw();
}

// zoom fills the page with the section last clicked, as z does in the
// project's panes; again, or esc, puts the others back.
export function zoom() {
  IN.zoom = IN.zoom ? '' : IN.focus === 'schema' && !IN.schema ? 'top' : IN.focus;
  redraw();
}

// ---- presets ----

async function loadPresets() {
  if (!IN.v) return;
  try {
    const p = await get('/api/inspector/presets?key=' + encodeURIComponent(IN.v.key));
    IN.presets = p.presets || [];
    IN.presetsPath = p.path || '';
    if (p.error) say(p.error);
  } catch (e) { IN.presets = []; say(e.message); }
}

// applyPreset fills the page from a preset; auto says it was loaded on
// opening the model rather than picked.
export function applyPreset(p, auto) {
  IN.presetsOpen = false;
  if (!p) { IN.preset = ''; IN.description = ''; redraw(); return; }
  IN.preset = p.id;
  IN.description = p.description || '';
  IN.global = p.global || '';
  IN.applyAll = !!p.applyAll;
  IN.rows = {};
  for (const [k, r] of Object.entries(p.rows || {})) IN.rows[k] = { on: !!r.on, filter: r.filter || '' };
  ensureRows();
  // The tables it added come back with it, and those it didn't go.
  if (JSON.stringify(p.added || []) !== JSON.stringify(addedKeys())) setAdded(p.added || []);
  if (!auto) { saveState(); say('loaded ' + presetName(p)); }
  redraw();
}

export const presetName = (p) => p.description || 'preset ' + p.id.slice(0, 6);

export async function savePreset(asNew) {
  if (!IN.v) return;
  const rows = {};
  for (const [k, r] of Object.entries(IN.rows)) rows[k] = { on: r.on, filter: r.filter };
  const over = !asNew && !!IN.preset;
  try {
    const p = await post('/api/inspector/presets', {
      key: IN.v.key,
      preset: { id: over ? IN.preset : '', description: IN.description, global: IN.global, applyAll: IN.applyAll, rows, added: addedKeys() },
    });
    IN.preset = p.id;
    await loadPresets();
    saveState();
    say((over ? 'saved over ' : 'saved ') + presetName(p));
  } catch (e) { say(e.message); }
  redraw();
}

export async function deletePreset(id) {
  if (!IN.v) return;
  try {
    const v = await post('/api/inspector/presets/delete', { key: IN.v.key, id });
    IN.presets = v.presets || [];
    if (IN.preset === id) { IN.preset = ''; saveState(); }
  } catch (e) { say(e.message); }
  redraw();
}

export function setDescription(text) { IN.description = text; redraw(); }

// ---- the model picker ----

// openPicker lists the project's actions as the palette's find action does,
// $kind and all, the model on show first.
// With add, the pick is a table to add to the page rather than the model
// to inspect.
export function openPicker(add) {
  IN.pick = { query: '', items: [], cursor: 0, total: 0, badKind: '', loading: true, add: !!add };
  findModels('');
}

export async function findModels(query) {
  const p = IN.pick;
  if (!p) return;
  p.query = query;
  try {
    const r = await post('/api/find', { query, first: IN.model ? [IN.model] : [], limit: 500 });
    if (IN.pick !== p || p.query !== query) return;
    // An action only referenced has nothing to walk.
    // Adding, assertions and tests aren't offered, as the walk leaves them
    // out; a table the page lists already is, to tick.
    p.items = (r.items || []).filter((a) => !a.unknown && !(p.add && /assertion|test/i.test(a.kind)));
    p.total = r.total || 0;
    p.badKind = r.badKind || '';
    p.cursor = 0;
  } catch { /* keep the last */ }
  p.loading = false;
  redraw();
}

export function pickModel(id) {
  const add = IN.pick && IN.pick.add;
  IN.pick = null;
  if (add) { if (id) addTable(id); }
  else if (id && id !== IN.model) load(id);
  redraw();
}

// ---- keys ----

export function inspectorKey(k) {
  if (IN.pick) {
    const p = IN.pick;
    if (k === 'esc') IN.pick = null;
    else if (k === 'down') p.cursor = Math.min(p.cursor + 1, p.items.length - 1);
    else if (k === 'up') p.cursor = Math.max(p.cursor - 1, 0);
    else if (k === 'enter' && p.items[p.cursor]) pickModel(p.items[p.cursor].id);
    redraw();
    return true;
  }
  if (IN.confirm) {
    if (k === 'esc' || k === 'ctrl+c') dismissConfirm();
    else if (k === 'enter' || k === 'y') confirmRun();
    return true;
  }
  switch (k) {
    case 'z':
      zoom();
      return true;
    case 'esc':
      if (IN.presetsOpen) IN.presetsOpen = false;
      else if (IN.zoom) IN.zoom = '';
      else if (IN.schema) { IN.schema = null; if (IN.focus === 'schema') IN.focus = 'top'; }
      else if (IN.sel && resultOf(IN.sel).sel) resultOf(IN.sel).sel = null;
      redraw();
      return true;
    case 'tab': case 'shift+tab':
      IN.tab = IN.tab === 'rows' ? 'graph' : 'rows';
      redraw();
      return true;
    case 'ctrl+c':
      if (anyRunning()) cancelRuns();
      return true;
  }
  return true;
}
