// R (the Changed Actions), T (tags), and the I and C checks with the
// prod compile options the first check asks for.

import { openBase } from './branches.js';
import { showErrorsTab } from './errors-tab.js';
import { joinOptions } from './prod-options.js';
import { S, checksPopoutURL, compile, copy, get, hasErrors, hooks, launch, popoutFeatures, post, projectURL, redraw, refreshView, running, say, termFocused, toggles } from './state.js';

export { popoutFeatures };

// R: the Changed Actions.
export async function openChanged() {
  if (!S.caps.changed) { say('run changed is not available'); return; }
  if (running()) { say('a run is already in progress (ctrl+c cancels it)'); return; }
  S.zoomed = false;
  S.overlay = 'changed';
  const skip = S.changed ? S.changed.skip : {};
  S.changed = { loading: true, res: S.changed && S.changed.res, err: null, skip, cursor: 0, preview: null };
  requestChanged();
}

let changedSeq = 0;
export async function requestChanged() {
  const seq = ++changedSeq;
  const c = S.changed;
  c.loading = true; c.err = null;
  redraw();
  try {
    const res = await post('/api/changed', { api: S.flags.api });
    if (seq !== changedSeq || !S.changed) return;
    Object.assign(S.changed, { loading: false, res });
    const rows = changedCursorRows();
    S.changed.cursor = Math.min(S.changed.cursor, Math.max(rows.length - 1, 0));
    changedPreview();
  } catch (e) {
    if (seq !== changedSeq || !S.changed) return;
    Object.assign(S.changed, { loading: false, err: e, res: null });
  }
  redraw();
}

export function changedListed() {
  const c = S.changed;
  return c && !c.loading && !c.err && c.res && c.res.count > 0;
}

// changedCursorRows are the rows the cursor moves over: the Changed
// Actions', not the removed ones'.
export function changedCursorRows() {
  if (!changedListed()) return [];
  return S.changed.res.rows.filter((r) => !r.removed);
}

export async function changedPreview() {
  const c = S.changed;
  if (!c || !c.res) return;
  const skip = Object.keys(c.skip).filter((f) => c.skip[f]);
  try {
    c.preview = await post('/api/changed/preview', { skip, toggles: toggles() });
  } catch (e) { say(e.message); }
  redraw();
}

export function changedKey(k) {
  const c = S.changed;
  const rows = changedCursorRows();
  switch (k) {
    case 'esc': case 'q': S.overlay = null; return;
    case 'r': case 'enter': case 'y': {
      if (!changedListed() || !c.preview || !c.preview.run) return;
      const skip = Object.keys(c.skip).filter((f) => c.skip[f]);
      const req = { kind: 'changed', skip, toggles: toggles() };
      S.overlay = null;
      if (S.flags.full) {
        const names = c.res.rows.filter((r) => r.kind === 'action' && r.mark === '●' && !r.removed && !c.skip[r.file]).map((r) => r.label);
        S.overlay = 'confirmFull';
        S.confirm = { names, req };
        return;
      }
      launch(req);
      return;
    }
    case 'space': {
      const row = rows[c.cursor];
      if (row) { c.skip[row.file] = !c.skip[row.file]; changedPreview(); }
      return;
    }
    case 'x': {
      if (!changedListed()) return;
      const files = [...new Set(c.res.rows.filter((r) => r.kind === 'file' && !r.removed).map((r) => r.file))];
      const anyChecked = files.some((f) => !c.skip[f]);
      c.skip = {};
      if (anyChecked) files.forEach((f) => { c.skip[f] = true; });
      changedPreview();
      return;
    }
    case 'd': S.flags.deps = !S.flags.deps; changedPreview(); refreshView(); return;
    case 'D': S.flags.dependents = !S.flags.dependents; changedPreview(); refreshView(); return;
    case 'f': S.flags.full = !S.flags.full; changedPreview(); refreshView(); return;
    case 'a':
      if (!S.caps.api) { say('API runs are not available'); return; }
      S.flags.api = !S.flags.api; refreshView(); requestChanged(); return;
    // v shows the command the run would be, hidden at first; it stays as
    // it was the next time R opens.
    case 'v': S.changedCommand = !S.changedCommand; return;
    case 'b': openBase(); return;
    case 'e': if (hasErrors()) { S.overlay = null; showErrorsTab(); } return;
    case 'j': case 'down': c.cursor = Math.min(c.cursor + 1, Math.max(rows.length - 1, 0)); return;
    case 'k': case 'up': c.cursor = Math.max(c.cursor - 1, 0); return;
    case 'ctrl+d': case 'pgdown': c.cursor = Math.min(c.cursor + 5, Math.max(rows.length - 1, 0)); return;
    case 'ctrl+u': case 'pgup': c.cursor = Math.max(c.cursor - 5, 0); return;
    case 'g': case 'home': c.cursor = 0; return;
    case 'G': case 'end': c.cursor = Math.max(rows.length - 1, 0); return;
  }
}

// T: tags.
export async function openTags() {
  if (!S.caps.run) { say('running is not available'); return; }
  if (running()) { say('a run is already in progress (ctrl+c cancels it)'); return; }
  if (!S.state || !S.state.hasGraph) { say('no actions yet: waiting for a compile'); return; }
  try {
    const r = await get('/api/tags');
    if (!r.total) { say('no tags in this project'); return; }
    S.zoomed = false;
    S.tags = { query: '', list: r.tags, all: r.tags, total: r.total, checked: {}, cursor: 0, options: false };
    S.overlay = 'tags';
  } catch (e) { say(e.message); }
  redraw();
}

let tagsSeq = 0;
export async function setTagsQuery(q) {
  const t = S.tags;
  t.query = q; t.cursor = 0;
  const seq = ++tagsSeq;
  redraw();
  try {
    const r = await get('/api/tags?q=' + encodeURIComponent(q));
    if (seq === tagsSeq && S.tags) S.tags.list = r.tags;
  } catch (e) { say(e.message); }
  redraw();
}

// tagsRun is the checked tags and how many actions they run between them.
export function tagsRun() {
  const t = S.tags;
  const tags = [], seen = new Set();
  for (const tag of t.all) {
    if (!t.checked[tag.name]) continue;
    tags.push(tag.name);
    tag.actions.forEach((a) => seen.add(a));
  }
  return [tags, seen.size];
}

export function runTags() {
  const [tags] = tagsRun();
  if (!tags.length) { say('nothing checked · space checks a tag'); return; }
  S.overlay = null;
  const req = { kind: 'tags', tags, toggles: toggles() };
  if (S.flags.full) { S.overlay = 'confirmFull'; S.confirm = { names: tags.map((t) => 'tag ' + t), req }; return; }
  launch(req);
}

export function toggleTag() {
  const t = S.tags;
  const tag = t.list[t.cursor];
  if (tag) t.checked[tag.name] = !t.checked[tag.name];
}

// I and C: the column impact and the query cost, the Checks page's two
// tabs (ADR 0010).
let checkToken = 0;
export function openImpact() { openCheck('impact'); }
export function openCost() { openCheck('cost'); }

// autoRefreshAllowed says whether a stale report may be rechecked without
// the user asking. Column impact on Databricks and both checks on
// Snowflake wake a warehouse that bills, so stale reports stay put until c
// (ADR 0042, ADR 0044).
function autoRefreshAllowed(which) {
  return !((which === 'impact' && S.caps.warehouse === 'databricks') || S.caps.warehouse === 'snowflake');
}

// openCheck shows which's tab. A check still running, one that failed,
// or one done that still matches the branch is shown as it is; a report
// the branch has moved on from, or c, checks afresh, the report showing
// dimmed meanwhile. The server keeps a report per branch: asked without
// refresh it returns the kept one, stale or not, so a stale one is
// checked again with refresh unless auto-refresh would wake a billed
// warehouse.
export async function openCheck(which, refresh) {
  if (which === 'impact' && !S.caps.impact) { say('column impact is not available'); return; }
  if (which === 'cost' && !S.caps.cost) { say('query cost is not available'); return; }
  S.zoomed = false;
  S.overlay = null;
  if (S.checks !== which) S.checksTermZoomed = false;
  S.checks = which;
  if (S.checksPopout && S.key && typeof history !== 'undefined' && history.replaceState) {
    history.replaceState(null, '', checksPopoutURL(S.key, which));
  }
  const prodOnly = which === 'cost' ? !!S.costProdOnly : false;
  const o = S[which] && S[which].api === S.flags.api && S[which].base === S.boot.defaultBranch && !!S[which].prodOnly === prodOnly ? S[which] : { report: null };
  const stale = !!o.report && checkStale(o);
  const auto = autoRefreshAllowed(which);
  if (!refresh && o.token && (o.loading || o.err || (o.report && (!stale || !auto)))) { S[which] = o; redraw(); return; }
  refresh = refresh || (stale && auto);
  const token = String(++checkToken);
  S[which] = { ...o, loading: true, err: null, progress: '', token, api: S.flags.api, base: S.boot.defaultBranch, prodOnly, offset: o.offset || 0 };
  redraw();
  try {
    const body = { api: S.flags.api, refresh: !!refresh, token };
    if (which === 'cost' && prodOnly) body.prodOnly = true;
    const r = await post('/api/' + which, body);
    if (!S[which] || S[which].token !== token) return;
    if (r.prod) {
      S[which].loading = false;
      S[which].token = ''; // nothing to reuse: the next look checks again
      // Only asked on the page: left meanwhile, it asks when you come back.
      if (S.checks === which) {
        S.prod = { after: which, saving: false, err: '' };
        S.overlay = 'prod';
      }
      redraw();
      return;
    }
    Object.assign(S[which], { loading: false, report: r, progress: '' });
    // The kept report is older than the branch: check it afresh when safe.
    if (r.stale && !refresh && auto) { openCheck(which, true); return; }
  } catch (e) {
    if (!S[which] || S[which].token !== token) return;
    Object.assign(S[which], { loading: false, err: e });
  }
  redraw();
}

// closeChecks leaves the Checks page for the project as it was, a check
// still running carrying on; pane 1 gets its editor's keys back.
export function closeChecks() {
  if (termFocused()) document.activeElement.blur();
  S.checks = null;
  S.checksTermZoomed = false;
  if (S.overlay === 'prod') S.overlay = null;
  if (S.focus === 'source' && hooks.sourceReturn) queueMicrotask(hooks.sourceReturn);
}

// toggleChecksTerm is ctrl+` on the Checks page: it shows the project's
// shells under the report, with the keys, as the log pane's terminal tab
// does in the project view; again, while the terminal has the keys, it
// hides them. ⌘j (panel) hides them whether they have the keys or not.
// checksTermFocus gives the terminal the keys at its next draw.
export function toggleChecksTerm(panel) {
  if (S.checksTerm && (panel || termFocused())) {
    if (termFocused()) document.activeElement.blur();
    S.checksTerm = false;
    S.checksTermZoomed = false;
    return;
  }
  S.checksTerm = true;
  S.checksTermFocus = true;
}

// toggleChecksTermZoom is ⌘z (alt+z) in the Checks page's terminal:
// fill the page with it, or put the report back above it.
export function toggleChecksTermZoom() {
  if (S.checksTerm && S.checksTermZoomed) {
    S.checksTermZoomed = false;
  } else {
    S.checksTerm = true;
    S.checksTermZoomed = true;
  }
  S.checksTermFocus = true;
}

// takeChecksTermFocus says the Checks page's terminal takes the keys
// this draw, once.
export function takeChecksTermFocus() {
  const f = S.checksTermFocus;
  S.checksTermFocus = false;
  return !!f;
}

// checksKey is the Checks page's key k, its buttons' too.
export function checksKey(k) { if (S.checks) checkKey(S.checks, k); }

// checksTabs are the Checks page's tabs this project has.
export const checksTabs = () => ['impact', 'cost'].filter((w) => S.caps[w]);

export function onProgress(ev) {
  for (const which of ['impact', 'cost']) {
    const o = S[which];
    if (o && o.token === ev.token && o.loading) {
      o.progress = ev.text;
      if (ev.partial) o.report = ev.partial;
    }
  }
}

// recheckShowing checks the shown tab afresh when a compile has moved the
// branch on from its report, rather than leaving a warning. Column impact
// on Databricks is left stale instead: it starts the SQL warehouse, which
// bills, so it runs only when asked (ADR 0042).
export function recheckShowing() {
  const which = S.checks;
  const o = which && S[which];
  if (!o || S.overlay || o.loading || o.err || !checkStale(o)) return;
  if (!autoRefreshAllowed(which)) { redraw(); return; }
  openCheck(which, true);
}

export function checkStale(o) {
  const r = o && o.report;
  if (!r || !r.signature) return false;
  return r.stale || (!o.api && S.state.impactSig && S.state.impactSig !== r.signature);
}

export function checkKey(which, k) {
  const o = S[which];
  switch (k) {
    case 'esc': if (S.checksTermZoomed) S.checksTermZoomed = false; return;
    case 'I': S.checksTermZoomed = false; openCheck('impact'); return;
    case 'C': S.checksTermZoomed = false; openCheck('cost'); return;
    case 'tab': case 'shift+tab': {
      const tabs = checksTabs();
      const i = tabs.indexOf(which);
      if (tabs.length > 1) openCheck(tabs[(i + (k === 'tab' ? 1 : tabs.length - 1)) % tabs.length]);
      return;
    }
    case 'y': {
      const md = o.report && (which === 'impact' ? o.report.markdown : o.report.report && o.report.report.markdown);
      if (!md || o.loading) { say('no report to copy yet'); return; }
      copy(md, which === 'impact' ? 'copied the column impact report as Markdown' : 'copied the query cost report as Markdown');
      return;
    }
    case 'c':
      S.checksTermZoomed = false;
      if (o.err && o.err.kind === 'compile') compile();
      openCheck(which, true);
      return;
    case 'a':
      if (!S.caps.api) { say('API runs are not available'); return; }
      S.checksTermZoomed = false;
      S.flags.api = !S.flags.api;
      refreshView();
      openCheck(which);
      return;
    case 'p':
      if (which !== 'cost' || S.caps.warehouse === 'databricks' || S.caps.warehouse === 'snowflake') return;
      S.checksTermZoomed = false;
      S.costProdOnly = !S.costProdOnly;
      openCheck('cost');
      return;
    case 'e': if (!S.checksPopout && hasErrors()) { closeChecks(); showErrorsTab(); } return;
    case 'b': openBase(); return;
    case 'j': case 'down': scrollOverlay(40); return;
    case 'k': case 'up': scrollOverlay(-40); return;
    case 'ctrl+d': case 'pgdown': scrollOverlay(300); return;
    case 'ctrl+u': case 'pgup': scrollOverlay(-300); return;
    case 'g': case 'home': scrollOverlay(-1e9); return;
    case 'G': case 'end': scrollOverlay(1e9); return;
  }
}

// scrollOverlay scrolls the open overlay's body, or the Checks page's
// report, by px.
export function scrollOverlay(px) {
  const el = document.querySelector('.overlay .scroll') || document.querySelector('.checks-page .checks-main');
  if (el) el.scrollTop += px;
}

// ---- popping out Checks into its own window ----

const checksHandoffPrefix = 'xf-web-checks-handoff:';
const checksHandoffTTL = 60 * 1000;

function pruneChecksHandoffs() {
  try {
    const cutoff = Date.now() - checksHandoffTTL;
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (!k || !k.startsWith(checksHandoffPrefix)) continue;
      let stale = true;
      try { const v = JSON.parse(localStorage.getItem(k) || 'null'); stale = !v || (v.at || 0) < cutoff; } catch { stale = true; }
      if (stale) localStorage.removeItem(k);
    }
  } catch { /* no storage */ }
}

function writeChecksHandoff(payload, fallbackPayload) {
  pruneChecksHandoffs();
  const token = Math.random().toString(36).slice(2) + Date.now().toString(36);
  try {
    localStorage.setItem(checksHandoffPrefix + token, JSON.stringify(payload));
    return token;
  } catch {
    if (fallbackPayload) {
      try { localStorage.setItem(checksHandoffPrefix + token, JSON.stringify(fallbackPayload)); return token; } catch { /* ignore */ }
    }
    return '';
  }
}

function takeChecksHandoff(token) {
  if (!token) return null;
  try {
    const raw = localStorage.getItem(checksHandoffPrefix + token);
    localStorage.removeItem(checksHandoffPrefix + token);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function snapshotCheckState(o, light) {
  if (!o) return null;
  return {
    report: light || o.loading || o.err ? null : (o.report || null),
    api: !!o.api,
    base: o.base || '',
    prodOnly: !!o.prodOnly,
    offset: o.offset || 0,
  };
}

function snapshotChecksForHandoff(which, light) {
  return {
    at: Date.now(),
    project: S.key,
    which,
    api: !!S.flags.api,
    costProdOnly: !!S.costProdOnly,
    checksTerm: !!S.checksTerm,
    impact: snapshotCheckState(S.impact, light),
    cost: snapshotCheckState(S.cost, light),
  };
}

function applyChecksHandoff(data) {
  if (!data) return;
  if (typeof data.api === 'boolean' && S.caps.api) S.flags.api = data.api;
  if (typeof data.costProdOnly === 'boolean') S.costProdOnly = data.costProdOnly;
  if (typeof data.checksTerm === 'boolean' && S.caps.terminal) S.checksTerm = data.checksTerm;
  for (const w of ['impact', 'cost']) {
    const snap = data[w];
    if (snap && snap.report) {
      S[w] = {
        report: snap.report,
        loading: false,
        err: null,
        progress: '',
        token: String(++checkToken),
        api: !!snap.api,
        base: snap.base || (S.boot && S.boot.defaultBranch) || '',
        prodOnly: !!snap.prodOnly,
        offset: snap.offset || 0,
      };
    }
  }
}

// popoutChecks moves the Checks page (or opens a tab fresh via opts.fresh) into
// a separate side-by-side browser window. Called synchronously from a
// click/pointerup/command so window.open keeps browser user activation.
export function popoutChecks(opts = {}) {
  const which = opts.which || S.checks || (S.caps.cost ? 'cost' : 'impact');
  if (!S.key) return false;
  if (which === 'impact' && !S.caps.impact) { say('column impact is not available'); redraw(); return false; }
  if (which === 'cost' && !S.caps.cost) { say('query cost is not available'); redraw(); return false; }
  if (opts.fresh) {
    const win = window.open(checksPopoutURL(S.key, which), '_blank', popoutFeatures(opts.dropPos));
    if (!win) { say('the browser blocked the new window: allow pop-ups for xf web'); redraw(); return false; }
    try { win.opener = null; } catch { /* cross-context */ }
    return true;
  }
  if (S.checksPopout) return false;
  const token = writeChecksHandoff(snapshotChecksForHandoff(which, false), snapshotChecksForHandoff(which, true));
  const win = window.open(checksPopoutURL(S.key, which, token), '_blank', popoutFeatures(opts.dropPos));
  if (!win) {
    if (token) takeChecksHandoff(token);
    say('the browser blocked the new window: allow pop-ups for xf web');
    redraw();
    return false;
  }
  try { win.opener = null; } catch { /* cross-context */ }
  closeChecks();
  redraw();
  return true;
}

// openChecksPopout boots a popped-out Checks window from a one-time handoff
// token or opens the requested check tab directly on reload / fresh open.
export async function openChecksPopout(which, handoffToken) {
  const data = handoffToken ? takeChecksHandoff(handoffToken) : null;
  const want = (data && (data.which === 'impact' || data.which === 'cost') ? data.which : which) || 'cost';
  const tab = S.caps[want] ? want : checksTabs()[0];
  if (!tab) {
    S.checksPopout = false;
    if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
    return;
  }
  S.checksPopout = true;
  if (data) applyChecksHandoff(data);
  await openCheck(tab, false);
}

// importDockedChecks receives Checks state sent back from a popped-out Checks
// window over BroadcastChannel and opens the Checks page in this window.
export async function importDockedChecks(handoffToken) {
  const data = takeChecksHandoff(handoffToken);
  if (!data) return;
  applyChecksHandoff(data);
  const want = data.which === 'impact' || data.which === 'cost' ? data.which : checksTabs()[0];
  if (want && S.caps[want]) await openCheck(want, false);
}

// dockChecks moves a popped-out Checks window back into an open project window
// and closes the pop-out, or switches this window back to the project view if
// no other window has the project open.
export async function dockChecks() {
  if (!S.checksPopout) return;
  const which = S.checks || checksTabs()[0] || 'cost';
  if (hooks.dockChecksToPeer && S.key) {
    const token = writeChecksHandoff(snapshotChecksForHandoff(which, false), snapshotChecksForHandoff(which, true));
    if (token) {
      const ok = await hooks.dockChecksToPeer(S.key, token);
      if (ok) {
        if (termFocused()) document.activeElement.blur();
        if (typeof window !== 'undefined' && window.close) window.close();
        if (typeof window === 'undefined' || !window.closed) {
          S.checksPopout = false;
          if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
          closeChecks();
          redraw();
        }
        return;
      }
      takeChecksHandoff(token); // clean up unclaimed handoff
    }
  }
  S.checksPopout = false;
  if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
  closeChecks();
  redraw();
}

// closeChecksPopout closes a popped-out Checks window without closing the
// project in other windows.
export function closeChecksPopout() {
  if (!S.checksPopout) { closeChecks(); redraw(); return true; }
  if (termFocused()) document.activeElement.blur();
  if (typeof window !== 'undefined' && window.close) window.close();
  // If the browser refused window.close() (e.g. opened as a normal tab), fall
  // back to showing the project here so ctrl+q never leaves the page stuck.
  if (typeof window === 'undefined' || !window.closed) {
    S.checksPopout = false;
    if (S.key && typeof history !== 'undefined' && history.replaceState) history.replaceState(null, '', projectURL(S.key));
    closeChecks();
    redraw();
  }
  return true;
}

// The prodCompilerOptions prompt.
export async function answerProd(dismiss) {
  const p = S.prod;
  const after = p.after;
  if (dismiss) {
    await post('/api/prod', { dismiss: true }).catch(() => {});
    S.prod = null;
    if (after === 'defer') setDefer(true); else openCheck(after);
    return;
  }
  p.saving = true; p.err = '';
  redraw();
  try {
    const options = joinOptions(p.fields || {});
    const r = await post('/api/prod', { options, default: !options });
    S.prod = null;
    S.flash = r.message || '';
    if (after === 'defer') setDefer(true); else openCheck(after);
  } catch (e) {
    if (S.prod) { S.prod.saving = false; S.prod.err = e.message; }
  }
  redraw();
}

// ---- defer to prod (ADR 0031) ----

// deferGroups groups defer to prod's rows by the label's head ("→ prod",
// "✓ dev", before any " · ") and the dataset of the table read, in the
// order the server sent them; each row keeps the rest of its label as note.
export function deferGroups(rows) {
  const groups = [];
  const byKey = new Map();
  for (const r of rows) {
    const i = r.label.indexOf(' · ');
    const head = i < 0 ? r.label : r.label.slice(0, i);
    const note = i < 0 ? '' : r.label.slice(i + 3);
    const name = r.prod || r.dev;
    const ds = name.slice(0, Math.max(0, name.lastIndexOf('.')));
    const key = r.tone + '\n' + head + '\n' + ds;
    let g = byKey.get(key);
    if (!g) { g = { tone: r.tone, head, ds, rows: [] }; byKey.set(key, g); groups.push(g); }
    g.rows.push({ ...r, note });
  }
  return groups;
}

// deferOn says defer to prod is on for the project's engine.
export const deferOn = () => !!(S.state && S.state.defer);

// setDefer turns defer to prod on or off. Turning it on where nothing
// sets prodCompilerOptions asks for them first, as I and C do; turning it
// off offers to remove the Proxy Views left in dev datasets.
export async function setDefer(on) {
  if (!S.caps.defer) { say('defer to prod is not available in this project'); return; }
  try {
    const r = await post('/api/defer', { on });
    if (r.prod) {
      S.prod = { after: 'defer', saving: false, err: '' };
      S.overlay = 'prod';
      redraw();
      return;
    }
    say(r.message || '');
    if (!on && r.proxies && r.proxies.length) {
      const n = r.proxies.length;
      S.deferAsk = {
        ask: { title: `${n} proxy view${n === 1 ? ' is' : 's are'} left in dev datasets from deferred runs: a run reads prod through ${n === 1 ? 'it' : 'them'} even with defer to prod off.`,
          detail: r.proxies, choices: [{ id: 'remove', label: 'Remove them' }] },
        remove: true,
      };
      showDeferAsk();
    }
  } catch (e) { say(e.message); }
  redraw();
}

export const toggleDefer = () => setDefer(!deferOn());

export async function removeProxies() {
  say('removing proxy views…');
  redraw();
  try { const r = await post('/api/defer/proxies/remove'); say(r.message || ''); } catch (e) { say(e.message); }
  redraw();
}

// askDefer shows a question a run asked before it starts; an answer
// sends the run again with it.
export function askDefer(ask, req) {
  S.deferAsk = { ask, req };
  showDeferAsk();
}

// showDeferAsk opens the question, which takes the keys from an editor:
// it comes after the server answers, the cursor perhaps in the source.
function showDeferAsk() {
  S.overlay = 'deferAsk';
  if (document.activeElement && document.activeElement.closest && document.activeElement.closest('.cm-editor')) document.activeElement.blur();
  redraw();
}

// answerDefer answers with choice i (0-based); -1 cancels.
export function answerDefer(i) {
  const d = S.deferAsk;
  S.overlay = null;
  S.deferAsk = null;
  if (!d) return;
  const c = d.ask.choices[i];
  if (!c) { say(d.remove ? 'proxy views kept' : 'run cancelled'); redraw(); return; }
  if (d.remove) { removeProxies(); return; }
  launch({ ...d.req, deferOK: [...(d.req.deferOK || []), c.id] });
}

// deferAskKey: a digit picks that choice, enter the first, any other key
// cancels.
export function deferAskKey(k) {
  if (!S.deferAsk) { S.overlay = null; return true; }
  if (/^[1-9]$/.test(k)) answerDefer(Number(k) - 1);
  else if (k === 'enter') answerDefer(0);
  else answerDefer(-1);
  return true;
}
