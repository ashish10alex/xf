// The declarations table (ADR 0015): the Actions pane of a file that
// declares only external tables (Dataform declarations, dbt sources), two
// or more, lists them all, in file order. The cursor in the source pane
// picks the row on its line, j/k pick one without moving the cursor, and
// a click puts the cursor on the row's line. Only the selected row's
// table is looked up; a row keeps what an earlier selection found.

import { S, hooks, post, redraw, say, selectInPane, setFocus } from './state.js';

const COLLAPSED = 'xf.declarationsCollapsed';

function stored() {
  try { return localStorage.getItem(COLLAPSED) === '1'; } catch { return false; }
}

// EXT is the table's view state: its filter, and whether it is folded
// away, which is kept for the browser.
export const EXT = { filter: '', collapsed: stored() };

export function toggleCollapsed() {
  EXT.collapsed = !EXT.collapsed;
  try { localStorage.setItem(COLLAPSED, EXT.collapsed ? '1' : '0'); } catch {}
  redraw();
}

export function setFilter(text) {
  EXT.filter = text;
  redraw();
}

// externalRows are the table's rows the filter lets through.
export function externalRows() {
  const rows = (S.view && S.view.externals) || [];
  const q = EXT.filter.trim().toLowerCase();
  return q ? rows.filter((r) => r.id.toLowerCase().includes(q)) : rows;
}

// groupOf is the project.dataset a row's table is in: its id without the
// name. A row dims it before the name.
export const groupOf = (r) => (r.id.endsWith('.' + r.name) ? r.id.slice(0, -(r.name.length + 1)) : '');

// freshness is what a row's "updated" says as a dot and a few words:
// tone is "none" before the table was looked up, "wait" while it is,
// "err" and "warn" for a lookup that failed, else how old the table is
// ("fresh" under two days, "ok" under a month, "idle" under six, "old"
// beyond). A source that updates monthly isn't a fault, so only an
// error is red.
export function freshness(u, now = Date.now()) {
  if (!u) return { tone: 'none', text: '' };
  const v = u.value || '';
  if (v.startsWith('✗')) return { tone: 'err', text: v.split(':')[0] };
  if (v === 'checking…') return { tone: 'wait', text: '…' };
  if (u.tone === 'warn') return { tone: 'warn', text: v };
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(v);
  if (!m) return { tone: 'none', text: v };
  const days = (now - new Date(+m[1], +m[2] - 1, +m[3]).getTime()) / 864e5;
  const tone = days < 2 ? 'fresh' : days < 31 ? 'ok' : days < 180 ? 'idle' : 'old';
  return { tone, text: v.split(' · ')[1] || v, when: v.split(' · ')[0] };
}

// moveInExternals is j/k (±1) and g/G (±2) in the table: the selection
// changes, the source pane's cursor stays.
export function moveInExternals(dir) {
  const rows = externalRows();
  if (!rows.length) return;
  let i = rows.findIndex((r) => r.id === S.sel);
  if (i < 0) i = dir < 0 ? rows.length - 1 : 0;
  else if (dir === -2) i = 0;
  else if (dir === 2) i = rows.length - 1;
  else i = Math.min(Math.max(i + dir, 0), rows.length - 1);
  if (rows[i].id !== S.sel) selectInPane(rows[i].id);
}

// clickExternal picks a row, and puts the source pane's cursor on the
// line naming it: a row no line names is only picked.
export function clickExternal(row) {
  if (row.id !== S.sel) selectInPane(row.id);
  if (row.at && hooks.goToLine) hooks.goToLine(row.file, row.id, row.at.line, row.at.col);
  else { setFocus('actions'); redraw(); }
}

// previewExternal opens Scratch on a SELECT * … LIMIT 100 of the row's
// table, to run once its dry run says what it costs.
export async function previewExternal(row) {
  if (!S.caps.scratch) { say('scratch queries need BigQuery access, which this project lacks'); return; }
  try {
    const f = await post('/api/scratch/create', { id: row.id, form: 'preview' });
    if (hooks.openScratch) await hooks.openScratch(f.id);
  } catch (e) { say(e.message); }
}

// state.js, which doesn't import this module, moves with j/k and ]/[
// through these.
hooks.externalRows = externalRows;
hooks.moveInExternals = moveInExternals;

// rowAt is the row named on line at or before col: of names sharing a
// line (["customers", "orders"]) the one the cursor is on, or the first
// when the cursor is before them all.
export function rowAt(rows, line, col) {
  const on = rows.filter((r) => r.at && r.at.line === line);
  return on.filter((r) => r.at.col <= col).pop() || on[0];
}

// The source pane's cursor picks the row at it, a moment after it stops:
// holding j doesn't fetch every row it passes.
let followTimer = null;
hooks.sourceCursor = (file, line, col = 0) => {
  clearTimeout(followTimer);
  followTimer = setTimeout(() => {
    const v = S.view;
    if (!v || !v.externals || v.file !== file) return;
    const row = rowAt(v.externals, line, col);
    if (row && row.id !== S.sel) selectInPane(row.id);
  }, 80);
};
