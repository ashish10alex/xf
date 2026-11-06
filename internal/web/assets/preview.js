// The preview: cmd+enter (ctrl+enter off a Mac) runs the selected
// action's query in BigQuery, its file saved and compiled first, and the
// log pane's preview tab shows the rows in Scratch's grid (scratch.js
// follows the job as it does Scratch's own).

import { S, post, redraw, say, setFocus, hooks, selFile } from './state.js';
import * as sc from './scratch.js';

// PV is the preview tab's result view (sc.resultView), with what it is a
// preview of (name), what it waits for (waiting: 'saving', 'compiling',
// 'starting'), why it could not start (error) and what it asks first
// (ask: { why, danger, req, from }).
export const PV = Object.assign(sc.resultView(), { name: '', waiting: '', error: '', ask: null });

let seq = 0;

// COMPILE_WAIT is how long a preview waits for the compile its save
// starts, before asking for the SQL there is.
const COMPILE_WAIT = 120000;

const isRunning = () => !!PV.run && ['starting', 'running'].includes(PV.run.phase);

// showTab shows the log pane on the preview tab, leaving the keys where
// they are: the editor keeps them, as VS Code's panel does.
function showTab() {
  S.showLog = true;
  S.logTab = 'preview';
}

// compiled waits for the compile that follows a save (one that ends after
// compiles), or for the compile under way when nothing was saved.
function compiled(after) {
  const done = () => S.state && !S.state.compiling && (after === undefined || S.state.compiles > after);
  if (done()) return Promise.resolve(true);
  return new Promise((resolve) => {
    const t0 = Date.now();
    const tick = () => {
      if (done()) resolve(true);
      else if (Date.now() - t0 > COMPILE_WAIT) resolve(false);
      else setTimeout(tick, 100);
    };
    tick();
  });
}

// previewSelected is cmd+enter: it previews the selected action, saving
// its file first when the source pane holds unsaved changes.
export async function previewSelected() {
  if (!S.caps.scratch) { say('previews need BigQuery access'); return; }
  if (!S.sel) { say('select an action to preview'); return; }
  const my = ++seq;
  if (isRunning()) cancelPreview(true);
  PV.error = ''; PV.ask = null;
  PV.name = S.view && S.view.id === S.sel ? S.view.label || S.view.name || S.sel : S.sel;
  showTab();
  let after;
  if (hooks.sourceHolds && hooks.sourceHolds() && hooks.sourceSave) {
    after = S.state ? S.state.compiles : 0;
    PV.waiting = 'saving';
    redraw();
    if (!(await hooks.sourceSave())) { if (my === seq) { PV.waiting = ''; redraw(); } return; }
    if (S.state && S.state.watchErr) post('/api/compile').catch(() => {});
  }
  if (my !== seq) return;
  if (after !== undefined || (S.state && S.state.compiling)) {
    PV.waiting = 'compiling';
    redraw();
    await compiled(after);
    if (my !== seq) return;
  }
  await start({ id: S.sel, file: selFile(S.sel), incremental: !!S.incremental }, my);
}

async function start(req, my) {
  PV.waiting = 'starting';
  redraw();
  let v;
  try {
    v = await post('/api/preview', req);
  } catch (e) {
    if (my !== seq) return;
    PV.waiting = ''; PV.error = e.message; PV.run = null;
    redraw();
    return;
  }
  if (my !== seq) return;
  PV.waiting = '';
  PV.name = v.name || PV.name;
  if (v.ask && v.ask.length) {
    // The question takes its answer from the page's keys, which the
    // editor would otherwise keep.
    PV.ask = { why: v.ask, danger: !!v.danger, req, from: S.focus };
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    setFocus('log');
    redraw();
    return;
  }
  sc.showJob(PV, v.job, v.sql, true);
  redraw();
}

// answer is the yes or no to what the preview asks; either gives the keys
// back to the pane that had them.
export function answer(yes) {
  const a = PV.ask;
  if (!a) return;
  PV.ask = null;
  if (a.from === 'source') {
    setFocus('source');
    if (hooks.sourceReturn) hooks.sourceReturn();
  } else if (a.from && a.from !== 'log') setFocus(a.from);
  if (yes) start({ ...a.req, force: true }, ++seq);
  else say('preview not run');
  redraw();
}

// cancelPreview cancels the running preview's job; quiet when a new one
// takes its place. It says whether there was one.
export function cancelPreview(quiet) {
  if (!isRunning() || !PV.run.job) return false;
  post('/api/scratch/cancel', PV.run.job).then(() => { if (!quiet) say('cancelling…'); }).catch((e) => say(e.message));
  return true;
}

// previewKey has the preview tab's keys, while the log pane has them.
export function previewKey(k) {
  if (PV.ask) {
    if (k === 'y' || k === 'enter') { answer(true); return true; }
    if (k === 'n' || k === 'esc') { answer(false); return true; }
    return true;
  }
  switch (k) {
    case 'esc':
      if (PV.sel) { PV.sel = null; return true; }
      if (sc.activeFilters(PV).length) { sc.clearFilters(PV); return true; }
      return false;
  }
  return false;
}

hooks.preview = previewSelected;
hooks.previewKey = previewKey;
hooks.previewCancel = () => cancelPreview(false);
hooks.previewRunning = isRunning;
