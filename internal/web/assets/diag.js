// diag is the page's own log, for when the page stops answering: each key
// and where it went, draws that failed, uncaught errors, requests that
// failed or hang, focus moves, the event stream dropping and stalls. Its
// lines go to xf web every few seconds, which appends them to its page
// log (pagelog.go: ~/.local/state/xf/web-page.log); xfLog() in the
// console prints this page's. It imports nothing, so any module can note.

const keepMax = 1000;
const kept = []; // this page's last lines, for xfLog()
let unsent = [];
let tab = '', projectOf = () => '', stateOf = () => '';

// diagSetup names this page to the log: its tab, its project and a line
// of what has the keys, read as each key or error is noted.
export function diagSetup(o) {
  tab = o.tab;
  projectOf = o.project;
  stateOf = o.state;
}

// note adds a line. The same line again counts up on the last one, so
// holding j or typing in an editor is a line, not hundreds.
export function note(text, now) {
  const last = kept[kept.length - 1];
  if (last && last.base === text && unsent.includes(last)) {
    last.n++;
    last.text = `${text} ×${last.n}`;
  } else {
    const l = { t: Date.now(), base: text, text, n: 1 };
    kept.push(l);
    unsent.push(l);
    if (kept.length > keepMax) kept.shift();
    if (unsent.length > keepMax) unsent.shift();
  }
  schedule(now ? 0 : 2000);
}

export const diagLines = () => kept.map((l) => `${new Date(l.t).toISOString().slice(11, 23)} ${l.text}`);

// ---- sending ----

// logPath is where xf web keeps the log, once it has said.
export let logPath = '';
let timer = 0, timerAt = 0, sending = false;
function schedule(ms) {
  if (typeof fetch === 'undefined' || !tab) return;
  const at = Date.now() + ms;
  if (timer && timerAt <= at) return;
  clearTimeout(timer);
  timerAt = at;
  timer = setTimeout(flush, ms);
  if (timer.unref) timer.unref(); // node, in the tests: never what keeps it going
}

const body = (lines) => JSON.stringify({ tab, project: projectOf() || '', lines: lines.map((l) => ({ t: l.t, text: l.text })) });

async function flush() {
  timer = 0;
  if (sending || !unsent.length) return;
  const lines = unsent;
  unsent = [];
  sending = true;
  let retry = 2000;
  try {
    const res = await fetch('/api/pagelog', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: body(lines), credentials: 'same-origin' });
    if (!res.ok) throw new Error(res.statusText);
    logPath = (await res.json()).path || '';
  } catch {
    unsent = lines.concat(unsent).slice(-keepMax); // xf web is away: kept for later
    retry = 10000;
  } finally {
    sending = false;
    if (unsent.length) schedule(retry);
  }
}

// ---- keys ----

const KEY = Symbol('diag');
let draws = 0, drawFails = 0;

const inText = (t) => typeof Element !== 'undefined' && t instanceof Element &&
  (!!t.closest('.cm-editor, .xterm') || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA');

// keyLabel names a key: what is typed into an editor, a box or the
// terminal is only a dot, so the log holds no text you typed.
export function keyLabel(e, typing) {
  const mods = (e.ctrlKey ? 'ctrl+' : '') + (e.metaKey ? 'cmd+' : '') + (e.altKey ? 'alt+' : '');
  if (typing && !e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1) return '·';
  return mods + (e.key === ' ' ? 'space' : e.key);
}

// keyStart is the first key handler (main.js): it notes the key, once
// the page has done with it, with where the keys were and what it did.
export function keyStart(e) {
  if (['Shift', 'Control', 'Meta', 'Alt', 'CapsLock'].includes(e.key)) return;
  const r = { label: keyLabel(e, inText(e.target)), target: describe(e.target), state: stateOf(), draws, used: undefined, reached: false };
  e[KEY] = r;
  setTimeout(() => note(keyLine(r, e.defaultPrevented, drawFails)), 0);
}

// keyUsed is what handleKey said of it; keyEnd is the last handler, which
// a key a shortcut stopped never reaches.
export function keyUsed(e, used) { if (e[KEY]) e[KEY].used = used; }
export function keyEnd(e) { if (e[KEY]) e[KEY].reached = true; }

export function keyLine(r, prevented, fails) {
  let what;
  if (r.used === true) what = 'used';
  else if (r.used === false) what = 'not used';
  else if (!r.reached) what = prevented ? 'taken before xf\'s keys' : 'stopped before xf\'s keys';
  else if (prevented) what = 'taken';
  else what = r.target.includes('cm-editor') ? 'to the editor' : 'passed by';
  const drew = draws > r.draws ? (fails ? 'draw failed' : 'drew') : 'no draw';
  return `key ${r.label} in ${r.target} · ${r.state} → ${what}, ${drew}`;
}

// describe names an element by its tag and first class, and those of its
// classed ancestors, saying when it is not on screen: the keys going to
// a hidden editor look like a page that ignores them.
export function describe(el) {
  if (typeof Element === 'undefined' || !(el instanceof Element)) return el === window ? 'window' : String(el);
  if (el === document.body) return 'body';
  const parts = [];
  for (let x = el; x && x !== document.body && parts.length < 4; x = x.parentElement) {
    const cls = x.classList && x.classList[0];
    if (x !== el && !cls) continue;
    if (cls && cls.startsWith('cm-') && cls !== 'cm-editor' && x !== el) continue;
    parts.push(x.tagName.toLowerCase() + (cls ? '.' + cls : ''));
  }
  let s = parts.join(' < ');
  if (!el.isConnected) s += ' (gone)';
  else if (!el.getClientRects().length) s += ' (hidden)';
  return s;
}

// ---- draws ----

// drew and drawFailed are told of each draw (state.js's redraw). A draw
// that throws leaves the page as it was, every key then seeming to do
// nothing, so a banner says so.
export function drew() {
  draws++;
  if (!drawFails) return;
  note(`draw works again after ${drawFails} failed`, true);
  drawFails = 0;
  banner(null);
}

export function drawFailed(err) {
  draws++;
  drawFails++;
  console.error(err);
  if (drawFails === 1 || drawFails % 50 === 0) note(`draw failed${drawFails > 1 ? ` (${drawFails} in a row)` : ''}: ${errText(err)} · ${stateOf()}`, true);
  banner(err);
}

let bannerEl = null;
function banner(err) {
  if (typeof document === 'undefined' || !document.body || !document.body.appendChild) return;
  if (!err) { if (bannerEl) bannerEl.remove(); bannerEl = null; return; }
  if (!bannerEl) {
    bannerEl = document.createElement('div');
    bannerEl.className = 'draw-failed';
    bannerEl.setAttribute('role', 'alert');
    document.body.appendChild(bannerEl);
  }
  const reload = /Mac/.test(navigator.userAgent) ? '⌘R' : 'ctrl+R';
  bannerEl.textContent = `xf web couldn't draw the page, so keys seem to do nothing: ${(err && err.message) || err}. ` +
    `Noted in ${logPath || 'xf web\'s page log'}. ${reload} reloads.`;
}

export function errText(err) {
  if (!err) return String(err);
  const stack = err.stack ? String(err.stack) : '';
  const lines = stack.split('\n').filter(Boolean);
  // Chrome's stack starts with the message, Firefox's and Safari's don't.
  const head = lines[0] && err.message && lines[0].includes(err.message) ? [] : [`${err.name || 'Error'}: ${err.message || err}`];
  return head.concat(lines.slice(0, 8)).join('\n') || String(err);
}

// ---- requests ----

const inflight = new Set();

// requestStart notes a request to xf web, returning what to call when it
// ends: one that fails or takes long is noted, and one still going after
// 15s is noted while it goes.
export function requestStart(method, path) {
  const r = { what: `${method} ${path.split('?')[0]}`, t: Date.now(), warned: false };
  inflight.add(r);
  return (err) => {
    inflight.delete(r);
    const s = ((Date.now() - r.t) / 1000).toFixed(1);
    if (err) note(`${r.what} failed after ${s}s: ${err.status || ''} ${err.message}`.replace(/\s+/g, ' '));
    else if (r.warned || Date.now() - r.t > 5000) note(`${r.what} took ${s}s`);
  };
}

// ---- the rest the page does ----

if (typeof window !== 'undefined' && typeof document !== 'undefined' && window.addEventListener) {
  window.xfLog = () => console.log(diagLines().join('\n'));
  window.addEventListener('error', (e) => note(`error: ${errText(e.error || e.message)}${e.filename ? ` (${e.filename.split('/').pop()}:${e.lineno})` : ''} · ${stateOf()}`, true));
  window.addEventListener('unhandledrejection', (e) => note(`unhandled rejection: ${errText(e.reason)} · ${stateOf()}`, true));
  document.addEventListener('focusin', (e) => note(`focus → ${describe(e.target)}`));
  document.addEventListener('focusout', (e) => {
    if (!e.relatedTarget) setTimeout(() => { if (document.activeElement === document.body) note('focus → body'); }, 0);
  });
  document.addEventListener('visibilitychange', () => note(document.hidden ? 'page hidden' : 'page shown'));
  // pagehide sends what is left, as the page may not get another go.
  window.addEventListener('pagehide', () => {
    note('page left');
    clearTimeout(timer);
    timer = 0;
    if (unsent.length && tab && navigator.sendBeacon) {
      navigator.sendBeacon('/api/pagelog', new Blob([body(unsent)], { type: 'application/json' }));
      unsent = [];
    }
  });
  // A tick a second notes the page not answering for a while (a long
  // task: a slow draw, a loop), while it shows: a hidden page's timers
  // are slowed anyway. Every 5s it notes requests still going after 15s.
  let tickAt = Date.now(), ticks = 0;
  setInterval(() => {
    const now = Date.now();
    const late = now - tickAt - 1000;
    tickAt = now;
    if (late > 1000 && !document.hidden) note(`page did not answer for ${((late + 1000) / 1000).toFixed(1)}s · ${stateOf()}`, true);
    if (++ticks % 5) return;
    for (const r of inflight) {
      if (r.warned || now - r.t < 15000) continue;
      r.warned = true;
      note(`${r.what} still going after ${Math.round((now - r.t) / 1000)}s`, true);
    }
  }, 1000);
}
