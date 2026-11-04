// The branch's merge request and its last pipeline, on GitLab or GitHub
// (ADR 0020): the status line's pill, the region docked at the bottom of Source
// Control, the palette's commands and Create MR. The server asks the host
// and sends what it found as the "forge" event; the page asks it to ask
// again on focus and on Refresh MR. Settings › Git hosting lists the
// hosts and their tokens.

import { S, get, post, redraw, hooks, copy, say } from './state.js';
import { openURL } from './log-tabs.js';

export const F = {
  v: null,       // forgeView from the server
  // ask is Create MR's question, while it shows: push first?
  ask: null,
  open: {},      // a stage's or trigger job's row key → open, when toggled
  hosts: null,   // Settings › Git hosting: hostsView
  hostsError: '',
  // add is the host being added or changed: {host, kind, token, test}.
  add: null,
};

// ---- reading ----

export async function load() {
  if (S.mode !== 'project') return;
  try { F.v = await get('/api/forge'); } catch { /* the event comes */ }
  redraw();
}

// check asks the server to ask the host again; soft only when its answer
// is old.
export function check(soft) {
  if (S.mode !== 'project') return;
  post('/api/forge/check', { soft: !!soft }).catch(() => {});
}

// on says the section has something to show.
export const on = () => !!(F.v && F.v.state && F.v.state !== 'off');

const github = () => !!(F.v && F.v.kind === 'github');
// word is what the host calls a merge request, short and long.
export const word = () => (github() ? 'PR' : 'MR');
export const longWord = () => (github() ? 'pull request' : 'merge request');
export const sigil = () => (github() ? '#' : '!');
export const mrName = (m) => sigil() + m.number;

// shownTitle is a merge request's title as its row says it: a draft's
// without the "Draft:" (or [Draft], (Draft), WIP:) its draft pill says.
export const shownTitle = (m) => (m.draft ? m.title.replace(/^\s*(?:\[draft\]|\(draft\)|draft:|wip:)[\s:]*/i, '') || m.title : m.title);
const hostName = () => (F.v && F.v.host) || 'the host';

export const going = (s) => s === 'pending' || s === 'running';
export const statusIcon = { pending: '●', running: '●', success: '✓', failed: '✗', canceled: '⊘', skipped: '»', manual: '▶' };
export const statusTone = { pending: 'warn', running: 'warn', success: 'ok', failed: 'error', canceled: 'dim', skipped: 'dim', manual: 'warn' };

// openMR is the open merge request, or the branch's last one.
const mr = () => (F.v && F.v.state === 'ok' ? F.v.mr : null);
const openOne = () => { const m = mr(); return m && m.state === 'open' ? m : null; };
const onBase = () => !!(F.v && F.v.branch && F.v.branch === F.v.base);

// ago is how long ago ms was, in words.
export function ago(ms) {
  if (!ms) return 'never';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return s + ' s ago';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  if (s < 48 * 3600) return Math.round(s / 3600) + ' h ago';
  return Math.round(s / 86400) + ' d ago';
}

// ---- the pill ----

// pill is the status line's: {text, tone, title, stale, run}, or null
// for nothing to show.
export function pill() {
  const v = F.v;
  if (!on()) return null;
  const where = v.host + (v.path ? '/' + v.path : '');
  switch (v.state) {
    case 'connect':
      return { text: '⚿ connect', tone: 'dim', title: 'no token for ' + v.host + ': add one in Settings › Git hosting', run: openHosts };
    case 'token':
      return { text: '⚿ token', tone: 'warn', title: v.host + ' refused the token: change it in Settings › Git hosting', run: openHosts };
    case 'error':
      return { text: '⚠ ' + word(), tone: 'warn', title: v.error + '\nclick: Source Control', run: showSection };
  }
  const m = v.mr;
  const p = v.pipeline;
  const stale = v.stale ? '\ncouldn\'t reach ' + v.host + ' · last checked ' + ago(v.checkedAt) : '';
  if (!m || m.state !== 'open' && !p) {
    if (onBase()) return null;
    if (m) return { text: mrName(m) + ' ' + m.state, tone: 'dim', stale: v.stale, title: mrName(m) + ' ' + m.title + ' (' + m.state + ')' + stale + '\nclick: Source Control', run: showSection };
    return { text: '+ ' + word(), tone: 'dim', stale: v.stale, title: 'no ' + longWord() + ' for ' + v.branch + ' on ' + where + stale + '\nclick: create one into ' + v.base, run: () => create(false) };
  }
  let text = mrName(m);
  let tone = 'dim';
  if (p) { text += ' ' + statusIcon[p.status]; tone = statusTone[p.status] || 'dim'; }
  if (m.state !== 'open') text += ' ' + m.state;
  else if (m.draft) text += ' draft';
  const title = [mrName(m) + ' ' + m.title, p ? 'pipeline ' + p.status : 'no pipeline', stale.trim(), 'click: Source Control'].filter(Boolean).join('\n');
  return { text, tone, stale: v.stale, title, run: showSection, going: p && going(p.status) };
}

// showSection shows Source Control at the merge request's section.
export function showSection() {
  if (hooks.scmShowMR) hooks.scmShowMR();
  check(true);
  redraw();
}

export function openHosts() {
  if (hooks.openSettings) hooks.openSettings();
  if (hooks.settingsShowAll) hooks.settingsShowAll();
  const started = Date.now();
  const go = () => {
    const el = document.getElementById('set-sec-git-hosting');
    if (el) el.scrollIntoView({ block: 'start' });
    else if (Date.now() - started < 3000) setTimeout(go, 100);
  };
  setTimeout(go, 100);
}

// ---- acting ----

export function openMR() {
  const m = mr();
  if (!on()) { say('no merge request here: the branch\'s remote isn\'t on a host xf knows (Settings › Git hosting)'); return; }
  if (!m) { say('no ' + longWord() + ' for ' + (F.v.branch || 'this branch') + ': m creates one'); return; }
  openURL(m.url, mrName(m));
}

export function openPipeline() {
  const p = F.v && F.v.state === 'ok' ? F.v.pipeline : null;
  if (!p) { say(on() ? 'no pipeline for ' + (F.v.branch || 'this branch') + ' on ' + hostName() : 'no pipeline: the branch\'s remote isn\'t on a host xf knows'); return; }
  openURL(p.url, 'the pipeline');
}

export function copyLink() {
  const m = mr();
  if (!m) { say('no ' + (on() ? longWord() : 'merge request') + ' to copy'); return; }
  copy(m.url, 'copied ' + m.url);
}

export function refresh() {
  if (!on()) { say('no merge request here: the branch\'s remote isn\'t on a host xf knows (Settings › Git hosting)'); load(); return; }
  check(false);
  say('asking ' + hostName() + ' again');
}

// create is Create MR: the host's page for a new merge request, once the
// branch is pushed; skip opens it without pushing.
export async function create(skip) {
  if (F.v && F.v.state === 'connect') { openHosts(); say('add a token for ' + F.v.host + ' first'); return; }
  let r;
  try { r = await post('/api/forge/create', { skip: !!skip }); } catch (e) { say(e.message); return; }
  if (r.push) {
    F.ask = r;
    showSection();
    return;
  }
  F.ask = null;
  if (r.url) {
    openURL(r.url, r.exists ? mrName(mr()) : 'the new ' + longWord() + ' page');
    if (r.message) say(r.message);
    // The new one shows once it is made: ask again when the page is back.
    return;
  }
  say(r.message || 'can\'t create one here');
}

// askKey answers Create MR's question: y pushes then opens, s opens
// without pushing, anything else keeps things as they are.
export async function askKey(k) {
  if (!F.ask) return false;
  F.ask = null;
  if (k === 'y' || k === 'enter') {
    const ok = hooks.scmPush ? await hooks.scmPush() : false;
    if (ok) create(true);
    return true;
  }
  if (k === 's') { create(true); return true; }
  say('kept');
  return true;
}

// useTarget makes the merge request's target the base, as b does.
export async function useTarget() {
  const m = openOne();
  if (!m) return;
  try {
    const r = await post('/api/base', { name: m.target });
    say(r.message || 'R, I and C compare with ' + m.target + ' now');
  } catch (e) { say(e.message); }
}

// ---- the section's rows (Source Control lists them: scm.js) ----

// rows are the section's rows below its header: kind 'mr' the merge
// request, 'mrnote' a line about it, 'mrbase' the target ≠ base hint,
// 'mrcreate' Create MR, 'pipe' the pipeline, 'stage' and 'job', each
// with its depth: 1 for the stages and jobs of a pipeline a trigger job
// started, and so on.
export function rows() {
  const v = F.v;
  const out = [];
  const note = (key, text, tone, more) => out.push({ key: 'f:' + key, kind: 'mrnote', text, tone: tone || 'dim', ...more });
  if (F.ask) out.push({ key: 'f:ask', kind: 'mrask', ask: F.ask });
  if (v.state === 'connect') {
    out.push({ key: 'f:hosts', kind: 'mrcreate', text: 'add a token for ' + v.host, run: openHosts });
    return out;
  }
  if (v.state === 'token') {
    out.push({ key: 'f:hosts', kind: 'mrcreate', text: v.host + ' refused the token: change it', run: openHosts, tone: 'warn' });
    return out;
  }
  if (v.state === 'error') {
    note('err', v.error, 'error');
    return out;
  }
  if (v.stale) note('stale', 'couldn\'t reach ' + v.host + ' · last checked ' + ago(v.checkedAt), 'warn');
  const m = v.mr;
  if (!m || m.state !== 'open') {
    if (!onBase()) out.push({ key: 'f:create', kind: 'mrcreate', text: 'create ' + longWord() + ' into ' + v.base, run: () => create(false) });
    else note('base', v.branch + ' is the base: switch to a branch to merge');
  }
  if (m) {
    out.push({ key: 'f:mr', kind: 'mr', mr: m });
    if (m.state === 'open') {
      if (m.target !== v.base) out.push({ key: 'f:base', kind: 'mrbase', target: m.target, base: v.base });
      const bits = [];
      bits.push(m.approvals + (m.required ? '/' + m.required : '') + ' approval' + (m.approvals === 1 && !m.required ? '' : 's'));
      if (m.unresolved >= 0) bits.push(m.unresolved + ' unresolved');
      note('rev', bits.join(' · '));
      for (const [i, b] of (m.blockers || []).entries()) note('block' + i, '⚠ ' + b, 'warn');
    }
  }
  const p = v.pipeline;
  if (!p) { note('nopipe', v.checking && !v.checkedAt ? 'asking ' + v.host + '…' : 'no pipeline'); return out; }
  out.push({ key: 'f:pipe', kind: 'pipe', p });
  addStages(out, p, 'f:st:', 0);
  return out;
}

// isOpen says the stage or trigger job at row key k shows what is in it:
// as toggled, else while it runs or once it failed.
const isOpen = (k, status) => (F.open[k] !== undefined ? F.open[k] : status === 'failed' || going(status));

// addStages adds pipeline p's stages, each open one's jobs below it; a
// trigger job opens like a stage, to the pipeline it started.
function addStages(out, p, prefix, depth) {
  for (const s of p.stages || []) {
    const k = prefix + s.name;
    const open = isOpen(k, s.status);
    out.push({ key: k, kind: 'stage', stage: s, open, depth });
    if (!open) continue;
    for (const [i, j] of (s.jobs || []).entries()) {
      const jk = k + ':' + i;
      const d = j.downstream;
      const jopen = !!d && isOpen(jk, j.status);
      out.push({ key: jk, kind: 'job', job: j, depth, down: !!d, open: jopen });
      if (jopen) addStages(out, d, jk + '>', depth + 1);
    }
  }
}

// opens says enter on row r opens or closes it, not a page.
const opens = (r) => !!r && (r.kind === 'stage' || (r.kind === 'job' && r.down));

export const isRow = (r) => !!r && r.key.startsWith('f:');

// act is enter (or a click) on one of the section's rows.
export function act(r) {
  switch (r.kind) {
    case 'mr': openURL(r.mr.url, mrName(r.mr)); return;
    case 'mrbase': useTarget(); return;
    case 'mrcreate': r.run(); return;
    case 'pipe': openPipeline(); return;
    case 'stage': case 'job':
      if (opens(r)) { F.open[r.key] = !r.open; redraw(); return; }
      openURL(r.job.url, r.job.name); return;
  }
}

// key is a key of Source Control's that the section takes: its question,
// o, O and m.
export function key(k, r) {
  if (F.ask) { askKey(k); return true; }
  switch (k) {
    case 'o':
      if (!isRow(r)) return false;
      if (r.kind === 'job') openURL(r.job.url, r.job.name);
      else if (r.kind === 'pipe') act(r);
      else openMR();
      return true;
    case 'O': openPipeline(); return true;
    case 'm': if (!on()) return false; if (openOne()) say(mrName(openOne()) + ' is open: o opens it'); else create(false); return true;
    case 'enter': if (!isRow(r) || r.kind === 'mrnote') return false; act(r); return true;
    case 'l': case 'right': if (opens(r) && !r.open) { act(r); return true; } return false;
    case 'h': case 'left': if (opens(r) && r.open) { act(r); return true; } return false;
  }
  return false;
}

// ---- the palette ----

function commands() {
  if (!on()) return [];
  const w = word();
  const out = [];
  const v = F.v;
  if (v.state === 'connect' || v.state === 'token') out.push(['Git: connect ' + v.host + ' (Settings › Git hosting)', '', openHosts]);
  if (!openOne() && !onBase()) out.push(['Git: create ' + w, 'm', () => create(false)]);
  if (mr()) {
    out.push(['Git: open ' + w, 'o', openMR]);
    out.push(['Git: copy ' + w + ' link', '', copyLink]);
  }
  out.push(['Git: open last pipeline', 'O', openPipeline]);
  out.push(['Git: refresh ' + w, '', refresh]);
  return out;
}

// ---- Settings › Git hosting ----

export async function loadHosts() {
  try {
    F.hosts = await get('/api/forge/hosts');
    F.hostsError = F.hosts.error || '';
  } catch (e) { F.hostsError = e.message; }
  redraw();
}

export function startAdd(h) {
  F.add = h ? { host: h.host, kind: h.kind, token: '', test: '', known: true } : { host: '', kind: 'gitlab', token: '', test: '', known: false };
  redraw();
}

export async function saveHost() {
  const a = F.add;
  if (!a || !a.host.trim()) { F.hostsError = 'type the host, e.g. gitlab.example.com'; redraw(); return; }
  try {
    F.hosts = await post('/api/forge/hosts', { host: a.host, kind: a.kind, token: a.token });
    F.hostsError = '';
    F.add = null;
  } catch (e) { F.hostsError = e.message; }
  redraw();
}

export async function deleteHost(h) {
  try {
    F.hosts = await post('/api/forge/hosts/delete', { host: h.host });
    F.hostsError = '';
  } catch (e) { F.hostsError = e.message; }
  redraw();
}

// testHost says who a token signs in as: the one typed, else the saved.
export async function testHost(h, typed) {
  const target = typed || h;
  target.test = 'testing…';
  redraw();
  try {
    const r = await post('/api/forge/hosts/test', { host: (typed || h).host, kind: (typed || h).kind, token: typed ? typed.token : '' });
    target.test = (r.ok ? '✓ ' : '✗ ') + r.message;
  } catch (e) { target.test = '✗ ' + e.message; }
  redraw();
}

// ---- wiring ----

hooks.forgeEvent = (v) => { F.v = v; redraw(); };
hooks.forgeStart = () => { F.v = null; F.ask = null; load(); };
hooks.forgeCommands = commands;
window.addEventListener('focus', () => check(true));
