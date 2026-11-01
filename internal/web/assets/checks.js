// R (the Changed Actions), T (tags), and the I and C checks with the
// prod compile options the first check asks for.

import { openBase } from './branches.js';
import { showErrorsTab } from './errors-tab.js';
import { S, compile, copy, get, hasErrors, hooks, launch, post, redraw, refreshView, running, say, toggles } from './state.js';

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

// openCheck shows which's tab. A check still running, one that failed,
// or one done that still matches the branch is shown as it is; a report
// the branch has moved on from, or c, checks afresh, the report showing
// dimmed meanwhile. The server keeps a report per branch: asked without
// refresh it returns the kept one, stale or not, so a stale one is
// checked again with refresh.
export async function openCheck(which, refresh) {
  if (which === 'impact' && !S.caps.impact) { say('column impact is not available'); return; }
  if (which === 'cost' && !S.caps.cost) { say('query cost is not available'); return; }
  S.zoomed = false;
  S.overlay = null;
  S.checks = which;
  const o = S[which] && S[which].api === S.flags.api && S[which].base === S.boot.defaultBranch ? S[which] : { report: null };
  const stale = !!o.report && checkStale(o);
  if (!refresh && o.token && (o.loading || o.err || (o.report && !stale))) { S[which] = o; redraw(); return; }
  refresh = refresh || stale;
  const token = String(++checkToken);
  S[which] = { ...o, loading: true, err: null, progress: '', token, api: S.flags.api, base: S.boot.defaultBranch, offset: o.offset || 0 };
  redraw();
  try {
    const r = await post('/api/' + which, { api: S.flags.api, refresh: !!refresh, token });
    if (!S[which] || S[which].token !== token) return;
    if (r.prod) {
      S[which].loading = false;
      S[which].token = ''; // nothing to reuse: the next look checks again
      // Only asked on the page: left meanwhile, it asks when you come back.
      if (S.checks === which) {
        S.prod = { after: which, options: '', onDefault: false, saving: false, err: '' };
        S.overlay = 'prod';
      }
      redraw();
      return;
    }
    Object.assign(S[which], { loading: false, report: r, progress: '' });
    // The kept report is older than the branch: check it afresh.
    if (r.stale && !refresh) { openCheck(which, true); return; }
  } catch (e) {
    if (!S[which] || S[which].token !== token) return;
    Object.assign(S[which], { loading: false, err: e });
  }
  redraw();
}

// closeChecks leaves the Checks page for the project as it was, a check
// still running carrying on; pane 1 gets its editor's keys back.
export function closeChecks() {
  S.checks = null;
  if (S.overlay === 'prod') S.overlay = null;
  if (S.focus === 'source' && hooks.sourceReturn) queueMicrotask(hooks.sourceReturn);
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
// branch on from its report, rather than leaving a warning.
export function recheckShowing() {
  const which = S.checks;
  const o = which && S[which];
  if (!o || S.overlay || o.loading || o.err || !checkStale(o)) return;
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
    case 'I': openCheck('impact'); return;
    case 'C': openCheck('cost'); return;
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
      if (o.err && o.err.kind === 'compile') compile();
      openCheck(which, true);
      return;
    case 'a':
      if (!S.caps.api) { say('API runs are not available'); return; }
      S.flags.api = !S.flags.api;
      refreshView();
      openCheck(which);
      return;
    case 'e': if (hasErrors()) { closeChecks(); showErrorsTab(); } return;
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
    const r = await post('/api/prod', { options: p.options, default: p.onDefault });
    S.prod = null;
    S.flash = r.message || '';
    if (after === 'defer') setDefer(true); else openCheck(after);
  } catch (e) {
    if (S.prod) { S.prod.saving = false; S.prod.err = e.message; }
  }
  redraw();
}

// ---- defer to prod (ADR 0031) ----

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
      S.prod = { after: 'defer', options: '', onDefault: false, saving: false, err: '' };
      S.overlay = 'prod';
      redraw();
      return;
    }
    say(r.message || '');
    // Query cost reads the tables the branch adds from dev only with defer
    // on: the report kept on the page is for the other setting.
    if (S.cost) S.cost.token = '';
    if (S.checks === 'cost') openCheck('cost');
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
