// Pane 2's errors tab: the compile's errors grouped by kind, which
// enter jumps to.

import { S, copy, errorsText, get, hasErrors, hooks, post, redraw, say, setFocus, showTerm, toggleColumns } from './state.js';

// The kinds of compile error, as diagnose.Kind names them, in the order
// the tab groups them: a syntax error usually causes the rest.
export const errorKinds = [
  ['root', 'Syntax / root errors'],
  ['unresolved', 'Unresolved references'],
  ['missing', 'Missing dependencies'],
];

let errorsSeq = 0;
// readErrors reads the compile's errors; fresh, from a compile that was
// clean, picks the errors tab with its first group open.
export async function readErrors(fresh) {
  const seq = ++errorsSeq;
  let e;
  try { e = await get('/api/errors'); } catch (err) { say(err.message); return; }
  if (seq !== errorsSeq) return;
  S.errors = e;
  if (fresh) {
    S.errTab = true;
    leaveLineage();
    S.errCursor = 0;
    const first = errorGroups()[0];
    S.errOpen = first ? { [first.kind]: true } : {};
  }
  redraw();
}

export const errorsTabShown = () => S.errTab && hasErrors();

// showErrorsTab is e, and a click on the status line's errors: it shows
// pane 2 if ⌥⌘b hid it.
export function showErrorsTab() {
  S.errHidden = false;
  S.errTab = true;
  leaveLineage();
  S.zoomed = false;
  setFocus('sql');
  if (!S.errors) readErrors(false);
}

// leaveLineage puts away the lineage tab, its keys going to the tab
// that takes its place.
function leaveLineage() {
  S.lineageOpen = false;
  if (S.focus === 'lineage') S.focus = 'sql';
}

// sqlTabOn is the tab pane 2's lower box shows: lineage, errors, columns
// or sql.
export const sqlTabOn = () => (S.lineageOpen ? 'lineage' : errorsTabShown() ? 'errors' : S.cols.shown ? 'columns' : 'sql');

// pickSQLTab picks one of the tabs of pane 2's lower box: errors, sql,
// columns or lineage. The keys stay in the box, with the tab.
export function pickSQLTab(tab) {
  const focused = S.focus === 'sql' || S.focus === 'lineage';
  if (tab === 'lineage') {
    S.lineageOpen = true;
    S.errTab = false;
    if (focused) S.focus = 'lineage';
    return;
  }
  leaveLineage();
  if (tab === 'errors') { S.errTab = true; return; }
  S.errTab = false;
  if ((tab === 'columns') !== S.cols.shown) toggleColumns();
}

// nextSQLTab goes round the tabs as the title lists them: tab forward,
// shift+tab back.
export function nextSQLTab(dir) {
  const tabs = hasErrors() ? ['errors', 'sql', 'columns', 'lineage'] : ['sql', 'columns', 'lineage'];
  const on = sqlTabOn();
  pickSQLTab(tabs[(tabs.indexOf(on) + dir + tabs.length) % tabs.length]);
}

// errorGroups are the compile's errors by kind, the file pane 1 shows
// first in each; a compile that could not run is a root error.
export function errorGroups() {
  const e = S.errors;
  if (!e) return [];
  const all = e.errors.slice();
  if (e.compileErr) all.unshift({ file: '', message: e.compileErr, kind: 'root' });
  const shown = S.view && S.view.file;
  const out = [];
  for (const [kind, title] of errorKinds) {
    const list = all.filter((x) => (x.kind || 'root') === kind);
    list.sort((a, b) => (b.file === shown) - (a.file === shown));
    if (list.length) out.push({ kind, title, list });
  }
  return out;
}

// errorRows are the tab's rows: the command that may fix the compile,
// such as one that installs the CLI it couldn't find, then each group's
// header, and its errors when it is open.
export function errorRows() {
  const rows = [];
  if (S.errors && S.errors.fix) rows.push({ fix: S.errors.fix });
  for (const g of errorGroups()) {
    rows.push({ group: g });
    if (S.errOpen[g.kind]) for (const x of g.list) rows.push({ err: x });
  }
  return rows;
}

// actOnErrorRow is enter or a click: a header opens or closes its group,
// an error takes pane 1 to its line.
export function actOnErrorRow(i) {
  const row = errorRows()[i];
  if (!row) return;
  S.errCursor = i;
  if (row.fix) { runFix(); return; }
  if (row.group) { S.errOpen = { ...S.errOpen, [row.group.kind]: !S.errOpen[row.group.kind] }; return; }
  const x = row.err;
  if (!x.file) { say('this error names no file'); return; }
  hooks.goToLine(x.file, x.action, Math.max((x.line || 1) - 1, 0));
}

// runFix runs a fix's command in the terminal tab, which it shows with
// the keys: the errors tab's, such as one that installs the missing CLI,
// after which the project compiles again; with run, the log's, such as
// dataform init-creds. Without a terminal tab it copies it.
export async function runFix(run = false) {
  const f = run ? runLogFix() : S.errors && S.errors.fix;
  const cmd = f && f.command;
  if (!cmd) return;
  if (!S.caps.terminal) { copy(cmd, 'copied the command: run it in a terminal' + (run ? '' : ', then compile')); return; }
  try { await post('/api/term/fix', { run }); } catch (e) { say(e.message); return; }
  showTerm();
  redraw();
}

export function errorsTabKey(k) {
  const n = errorRows().length;
  switch (k) {
    case 'j': case 'down': S.errCursor = Math.min(S.errCursor + 1, Math.max(n - 1, 0)); return true;
    case 'k': case 'up': S.errCursor = Math.max(S.errCursor - 1, 0); return true;
    case 'g': case 'home': S.errCursor = 0; return true;
    case 'G': case 'end': S.errCursor = Math.max(n - 1, 0); return true;
    case 'enter': case 'space': actOnErrorRow(S.errCursor); return true;
    case 'y': copy(errorsText(), 'copied the errors'); return true;
  }
  return false;
}

// runLogFix is the fix the failed run's log offers, null for none.
export function runLogFix() {
  const lines = (S.run && S.run.lines) || [];
  for (let i = lines.length - 1; i >= 0; i--) if (lines[i].fix) return lines[i].fix;
  return null;
}
