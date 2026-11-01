// The log pane's tabs: the run's log, its jobs and the kept runs.

import { S, copy, get, openLink, post, redraw, say } from './state.js';

export const hasTabs = () => S.caps.history;

export function jobsShown() {
  if (S.run && S.run.ran) return S.run.jobs;
  return S.history.last;
}

// logTabs are the log pane's tabs, in the order ⌥⌘→ and t go round them.
export function logTabs() {
  const tabs = hasTabs() ? ['log', 'jobs', 'history'] : ['log'];
  if (S.caps.terminal) tabs.push('term');
  return tabs;
}

// nextTab shows the log pane's next tab (dir -1: the one before). Leaving
// the terminal takes the keys out of it, so the log pane's keys work again;
// the terminal takes them when it shows (TermBody).
export function nextTab(dir = 1) {
  const tabs = logTabs();
  if (tabs.length < 2) return false;
  if (S.logTab === 'term' && typeof document !== 'undefined' && document.activeElement instanceof HTMLElement &&
    document.activeElement.closest('.xterm')) document.activeElement.blur();
  S.logTab = tabs[(tabs.indexOf(S.logTab) + dir + tabs.length) % tabs.length];
  if (S.logTab === 'history') get('/api/history').then((h) => { S.history = h; redraw(); }).catch(() => {});
  return true;
}

// ---- watching a workflow xf did not start ----

// openWatch asks for the workflow to watch: the ID of a workflow invocation,
// or the URL of its page in the Cloud console.
export function openWatch() {
  S.watch = { input: '', busy: false, err: '' };
  S.overlay = 'watch';
}

export function closeWatch() {
  S.watch = null;
  if (S.overlay === 'watch') S.overlay = null;
}

// submitWatch has the server follow the workflow. It takes the run's place,
// so its cost table is the jobs tab's, and it is kept in the history as
// external; a refusal (a bad ID, a run in progress) stays in the prompt.
export async function submitWatch() {
  const w = S.watch;
  if (!w || w.busy) return;
  if (!w.input.trim()) { w.err = 'paste a workflow ID, or the URL of its page in the console'; redraw(); return; }
  w.busy = true;
  w.err = '';
  redraw();
  try {
    const r = await post('/api/watch', { input: w.input });
    if (!r.ok) { w.busy = false; w.err = r.message; redraw(); return; }
    closeWatch();
    S.showLog = true;
    S.logTab = hasTabs() ? 'jobs' : 'log';
    S.jobsCursor = 0;
  } catch (e) { w.busy = false; w.err = e.message; }
  redraw();
}

export async function openKept(row) {
  try {
    S.historyJobs = await get('/api/history/jobs?id=' + encodeURIComponent(row.id));
    S.historyOpen = row.id;
    S.jobsCursor = 0;
  } catch (e) { say(e.message); }
  redraw();
}

export function openURL(url, what) {
  if (!url) return false;
  openLink(url);
  say('opening ' + what + ' in the browser');
  return true;
}

// shownJobs is the table of jobs the log pane shows: the jobs tab's, or
// that of the kept run opened in the history.
export function shownJobs() {
  if (S.logTab === 'jobs') return jobsShown();
  if (S.logTab === 'history' && S.historyOpen) return S.historyJobs;
  return null;
}

// The jobs table's columns, in the order S goes round them. A number
// column sorts biggest first and a text one A to Z; a second click on its
// header turns that round, and a third goes back to the run's order.
export const JOB_COLS = [
  { id: 'target', label: 'Target' },
  { id: 'state', label: 'State' },
  { id: 'duration', label: 'Duration', num: (r) => (r.hasDuration ? r.durationMs || 0 : undefined) },
  { id: 'slot', label: 'Slot time', num: (r) => (r.hasJob ? r.slotMs || 0 : undefined) },
  { id: 'billed', label: 'Billed', num: (r) => (r.hasJob ? r.bytes || 0 : undefined) },
  { id: 'cost', label: 'Est. cost', num: (r) => (r.hasJob ? r.costUsd || 0 : undefined) },
];
const SORT_ORDER = ['duration', 'slot', 'billed', 'cost', 'target', 'state'];

// By state, what needs looking at comes first.
const STATE_RANK = { RUNNING: 0, FAILED: 1, CANCELLED: 1, PENDING: 2, SUCCEEDED: 3, SKIPPED: 4, DISABLED: 4 };

// jobsRows are t's rows as the table lists them: those matching the
// search, in the chosen order. Every word of the query must be in the
// target, the state or the failure.
export function jobsRows(t) {
  if (!t) return [];
  const words = S.jobsQuery.toLowerCase().split(/\s+/).filter(Boolean);
  let rows = !words.length ? t.rows : t.rows.filter((r) => {
    const hay = (r.target + ' ' + r.state + ' ' + (r.failure || '')).toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const col = JOB_COLS.find((c) => c.id === S.jobsSort.col);
  if (!col) return rows;
  const dir = S.jobsSort.desc ? -1 : 1;
  const key = col.num || (col.id === 'state' ? (r) => STATE_RANK[r.raw] ?? 2 : (r) => r.target);
  // Rows with no number yet (a job not looked up) stay last either way; a
  // number that is 0 is a number.
  return rows.slice().sort((a, b) => {
    const x = key(a), y = key(b);
    if (col.num && (x === undefined) !== (y === undefined)) return x === undefined ? 1 : -1;
    if (col.num && x === undefined) return a.target.localeCompare(b.target);
    return (x < y ? -1 : x > y ? 1 : 0) * dir || a.target.localeCompare(b.target);
  });
}

// sortJobsBy sorts by col as a click on its header does: its natural way
// first, then the other way, then back to the run's order.
export function sortJobsBy(col) {
  const c = JOB_COLS.find((x) => x.id === col);
  const s = S.jobsSort;
  if (s.col !== col) S.jobsSort = { col, desc: !!c.num };
  else if (s.desc === !!c.num) S.jobsSort = { col, desc: !s.desc };
  else S.jobsSort = { col: '', desc: false };
  showJobsTop();
  say(sortSaid());
}

// nextJobsSort is S: the next column, each its natural way, and after the
// last the run's order again.
export function nextJobsSort() {
  const i = SORT_ORDER.indexOf(S.jobsSort.col);
  const col = SORT_ORDER[i + 1] || '';
  S.jobsSort = { col, desc: !!(JOB_COLS.find((c) => c.id === col) || {}).num };
  showJobsTop();
  say(sortSaid());
}

function sortSaid() {
  const c = JOB_COLS.find((x) => x.id === S.jobsSort.col);
  if (!c) return 'jobs in the run\'s order';
  return 'jobs by ' + c.label.toLowerCase() + (c.num ? (S.jobsSort.desc ? ', biggest first' : ', smallest first') : (S.jobsSort.desc ? ', reversed' : ''));
}

// showJobsTop puts the cursor on the first row of the new order and
// scrolls the table to it: the biggest or smallest is what a sort is for,
// and the table was left scrolled to where the cursor's job used to be.
function showJobsTop() {
  S.jobsCursor = 0;
  if (typeof document !== 'undefined') {
    const box = document.querySelector('.jobs-view .table-scroll');
    if (box) box.scrollTop = 0;
  }
}

export function setJobsQuery(q) {
  S.jobsQuery = q;
  S.jobsCursor = 0;
}

// jobsTableKey has the keys of a table of jobs that come before the
// global ones: / searches it, esc clears the search, S sorts.
export function jobsTableKey(k) {
  if (!shownJobs()) return false;
  switch (k) {
    case '/': S.jobsTyping = true; return true;
    case 'S': nextJobsSort(); return true;
    case 'esc':
      if (!S.jobsQuery) return false;
      setJobsQuery('');
      return true;
  }
  return false;
}

function openJob(row) {
  if (!openURL(row && row.jobUrl, 'the BigQuery job')) say('no BigQuery job for this action');
}

export function logKey(k) {
  const tableRows = () => {
    const t = shownJobs();
    if (t) return jobsRows(t);
    return S.logTab === 'jobs' || (S.logTab === 'history' && S.historyOpen) ? [] : null;
  };
  if (k === 't' && nextTab()) return true;
  const rows = tableRows();
  if (k === 'u') {
    // As the TUI's u: in a table of jobs, an API run's page, or else the
    // selected job in BigQuery; in the history, the run's page.
    const table = rows && shownJobs();
    if (table && table.url) openURL(table.url, 'the workflow invocation');
    else if (rows) openJob(rows[S.jobsCursor]);
    else if (S.logTab === 'history') {
      const row = S.history.rows[S.historyCursor];
      if (!openURL(row && row.url, 'the workflow invocation')) say('a CLI run has no page of its own: enter lists its jobs');
    } else if (S.run && S.run.url) openURL(S.run.url, 'the workflow invocation');
    else say(S.run && S.run.api && S.run.running ? 'no workflow invocation yet: Dataform is still compiling' : 'u opens an API run\'s workflow invocation; this run has none');
    return true;
  }
  if (rows) {
    const move = (n) => { S.jobsCursor = Math.min(Math.max(S.jobsCursor + n, 0), Math.max(rows.length - 1, 0)); };
    switch (k) {
      case 'j': case 'down': move(1); return true;
      case 'k': case 'up': move(-1); return true;
      case 'g': case 'home': S.jobsCursor = 0; return true;
      case 'G': case 'end': S.jobsCursor = Math.max(rows.length - 1, 0); return true;
      case 'ctrl+d': case 'pgdown': move(10); return true;
      case 'ctrl+u': case 'pgup': move(-10); return true;
      case 'enter': openJob(rows[S.jobsCursor]); return true;
      case 'backspace':
        if (S.logTab === 'history') { S.historyOpen = null; S.historyJobs = null; }
        return true;
    }
    return true;
  }
  if (S.logTab === 'history') {
    const n = S.history.rows.length;
    const move = (d) => {
      S.historyCursor = Math.min(Math.max(S.historyCursor + d, 0), Math.max(n - 1, 0));
      const row = S.history.rows[S.historyCursor];
      if (row) post('/api/history/refresh', { id: row.id }).catch(() => {});
    };
    switch (k) {
      case 'j': case 'down': move(1); return true;
      case 'k': case 'up': move(-1); return true;
      case 'g': case 'home': S.historyCursor = 0; return true;
      case 'G': case 'end': move(n); return true;
      case 'enter': { const row = S.history.rows[S.historyCursor]; if (row) openKept(row); return true; }
      case 'X':
        if (!n) { say('no kept runs to clear'); return true; }
        S.overlay = 'confirmClear';
        return true;
    }
    return true;
  }
  // The log itself: it follows the tail until scrolled up, and G
  // follows again.
  const lh = 20;
  switch (k) {
    case 'j': case 'down': scrollLog(lh); return true;
    case 'k': case 'up': scrollLog(-lh); return true;
    case 'ctrl+d': case 'pgdown': scrollLog(10 * lh); return true;
    case 'ctrl+u': case 'pgup': scrollLog(-10 * lh); return true;
    case 'g': case 'home': scrollLog(-1e9); return true;
    case 'G': case 'end': S.logFollow = true; scrollLog(1e9); return true;
    case 'y':
      if (S.run && S.run.command) copy(S.run.command, 'copied the command: paste it into a terminal');
      else say(S.run && S.run.api ? 'a run in GCP has no command to copy' : 'no run yet: r runs the selected action');
      return true;
  }
  return false;
}

export function scrollLog(px) {
  const el = document.querySelector('.log-scroll');
  if (!el) return;
  el.scrollTop += px;
  S.logFollow = el.scrollTop + el.clientHeight >= el.scrollHeight - 2;
}

export async function clearHistory() {
  try {
    const r = await post('/api/history/clear');
    say(r.message);
    S.historyOpen = null; S.historyJobs = null; S.historyCursor = 0;
  } catch (e) { say(e.message); }
}
