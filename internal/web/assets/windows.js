// The browser windows (tabs) open on this xf web, and bringing one of
// them forward. The server knows nothing of it: which window shows
// what is view state, which is the page's own (see internal/web/web.go).
// The windows of one xf web share its origin, so they find each other
// over a BroadcastChannel, which carries nothing off this browser: those
// of another browser, or of another device through --allow-host, are not
// counted.
//
// Each window has an id for this load and the time it loaded, which
// numbers them (Window 1 is the oldest). It says hello when it starts,
// and then what it shows ("here": its project's key and a label) when
// that changes and when another asks ("who"), and bye when it is closed
// or xf web has quit. A window the
// browser froze, discarded or killed can't say bye: it drops out the
// next time this one asks, which it does when it comes back to the front,
// when the list opens, and every 15 seconds while it is in view.

import { S, redraw, keyName, hooks } from './state.js';
import { SET } from './settings.js';
import { SC, scratchSay } from './scratch.js';

export const W = {
  // peers are the other windows, by id: { id, since, project, label }.
  peers: new Map(),
  // open is whether the list is open, at where it opens (fixed: right,
  // and top or bottom) and cursor its row.
  open: false, at: null, cursor: 0,
};

const me = { id: Math.random().toString(36).slice(2) + Date.now().toString(36), since: performance.timeOrigin || Date.now() };
const ch = typeof BroadcastChannel === 'function' ? new BroadcastChannel('xf-web-windows') : null;
// started is whether this window has said hello, alive whether it
// counts: not once xf web has quit.
let started = false, alive = false, label = '', project = '';

function send(m) {
  if (!ch) return;
  try { ch.postMessage({ ...m, id: me.id }); } catch { /* the channel closed */ }
}
const announce = (t) => send({ t, since: me.since, project, label });

// heard is the windows that answered since this one last asked, while
// it waits for them.
let heard = null, askTimer = 0;

if (ch) {
  ch.onmessage = ({ data: m }) => {
    if (!m || typeof m !== 'object' || m.id === me.id) return;
    switch (m.t) {
      case 'hello':
        if (alive) announce('here');
        // falls through: it is here too
      case 'here':
        W.peers.set(m.id, { id: m.id, since: Number(m.since) || 0, project: String(m.project || ''), label: String(m.label || '') });
        if (heard) heard.add(m.id);
        redraw();
        return;
      case 'who':
        if (alive) announce('here');
        return;
      case 'bye':
        if (W.peers.delete(m.id)) redraw();
        return;
      case 'focus':
        if (m.to === me.id && alive) comeForward(m.id);
        return;
      case 'heard':
        if (m.to === me.id) heardBy(m.id);
        return;
      case 'focused':
        if (m.to === me.id) answered(m.id, !!m.ok);
        return;
    }
  };
  window.addEventListener('pagehide', () => { if (alive) send({ t: 'bye' }); });
  // Back from the back-forward cache, or thawed: the others may have
  // dropped this window meanwhile.
  window.addEventListener('pageshow', (e) => { if (e.persisted && alive) { announce('hello'); ask(); } });
  document.addEventListener('resume', () => { if (alive) { announce('hello'); ask(); } });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) ask(); });
  window.addEventListener('focus', ask);
  setInterval(() => { if (!document.hidden && W.peers.size) ask(); }, 15000);
}

// ask asks every window to answer, and drops those that haven't within a
// second: they were closed without a word.
function ask() {
  if (!alive || heard) return;
  heard = new Set();
  send({ t: 'who' });
  clearTimeout(askTimer);
  askTimer = setTimeout(() => {
    let gone = false;
    for (const id of W.peers.keys()) if (!heard.has(id)) { W.peers.delete(id); gone = true; }
    heard = null;
    if (gone) redraw();
  }, 1000);
}

// labelNow is what this window shows, as the list words it: its
// project first, as tabs may be on several.
function labelNow() {
  if (S.mode === 'loading') return 'starting';
  if (S.mode === 'error') return 'error';
  if (S.mode === 'picker') return SET.open ? 'settings' : 'projects';
  const name = S.boot ? S.boot.name : '';
  const and = (what) => (what ? name + ' · ' + what : name);
  if (SET.open) return and('settings');
  if (SC.open) return and('scratch' + (SC.cur && SC.cur.from ? ' · ' + SC.cur.from : ''));
  const v = S.view;
  if (v) return and(v.schema ? v.schema + '.' + v.name : v.name || v.file || '');
  return and(S.plainFile);
}

// track is called after every draw: it says hello the first time, what
// this window shows when that changes, and bye once xf web has quit.
export function track() {
  if (!ch) return;
  const now = S.mode !== 'gone';
  if (!now) {
    if (alive) { send({ t: 'bye' }); alive = false; W.peers.clear(); W.open = false; }
    return;
  }
  alive = true;
  const l = labelNow();
  if (started && l === label && S.key === project) return;
  label = l;
  project = S.key;
  announce(started ? 'here' : 'hello');
  started = true;
}

// windows lists every window, this one too, oldest first, each with its
// number: the first is Window 1.
export function windows() {
  const all = [...W.peers.values(), { id: me.id, since: me.since, project, label, self: true }];
  all.sort((a, b) => a.since - b.since || (a.id < b.id ? -1 : 1));
  return all.map((w, i) => ({ ...w, n: i + 1 }));
}

export const others = () => W.peers.size;

// windowOf is the number of another window on the project key names, 0
// for none.
export function windowOf(key) {
  const w = windows().find((x) => !x.self && x.project === key);
  return w ? w.n : 0;
}

// openWindows opens the list beside el, the button that opens it, above
// it when it is low on the page; without one, at the bottom right. It
// stays 16px inside the page, which on a phone is narrower than the list
// is wide (.win-list's width: min(420px, 100vw - 32px)).
export function openWindows(el) {
  const r = el ? el.getBoundingClientRect() : null;
  const vw = window.innerWidth, width = Math.min(420, vw - 32);
  const right = r ? Math.min(Math.max(vw - r.right, 16), Math.max(vw - width - 16, 16)) : 16;
  W.at = !r ? { right, bottom: 40 } : r.top > window.innerHeight / 2 ? { right, bottom: window.innerHeight - r.top + 6 } : { right, top: r.bottom + 6 };
  W.cursor = Math.max(windows().findIndex((w) => w.self), 0);
  W.open = true;
  ask();
  redraw();
}

export function closeWindows() { W.open = false; redraw(); }

export function moveWindows(n) {
  const len = windows().length;
  W.cursor = Math.min(Math.max(W.cursor + n, 0), len - 1);
}

// The list's keys, ahead of every other while it is open.
window.addEventListener('keydown', (e) => {
  if (!W.open) return;
  const k = keyName(e);
  if (k === 'Shift' || k === 'Control' || k === 'Meta' || k === 'Alt') return;
  e.preventDefault();
  e.stopImmediatePropagation();
  const list = windows();
  if (k === 'esc' || k === 'q' || k === 'ctrl+c') closeWindows();
  else if (k === 'j' || k === 'down' || k === 'ctrl+n') { moveWindows(1); redraw(); }
  else if (k === 'k' || k === 'up' || k === 'ctrl+p') { moveWindows(-1); redraw(); }
  else if (k === 'enter') bringForward(list[W.cursor]);
  else if (/^[1-9]$/.test(k) && list[+k - 1]) bringForward(list[+k - 1]);
}, true);

// ---- bringing a window forward ----

function say(msg) {
  if (SC.open) scratchSay(msg);
  else { S.flash = msg; redraw(); }
}

// asked is the window this one asked to come forward, until it answers.
let asked = null;

// bringForward asks w to come forward; for this window it just closes
// the list.
export function bringForward(w) {
  W.open = false;
  if (!w || w.self) { redraw(); return; }
  send({ t: 'focus', to: w.id });
  const n = w.n;
  asked = { id: w.id, n, timer: setTimeout(() => {
    asked = null;
    W.peers.delete(w.id);
    say(`Window ${n} didn't answer: it has been closed`);
  }, 1500) };
  redraw();
}

function answered(id, ok) {
  if (!asked || asked.id !== id) return;
  clearTimeout(asked.timer);
  const n = asked.n;
  asked = null;
  // When it came forward this window is behind it, and nothing need be
  // said.
  if (!ok) say(`the browser keeps Window ${n} behind: its tab's title is flashing ●`);
}

// comeForward is what a window does when another asks it to come
// forward. Browsers mostly refuse window.focus() to a tab that wasn't
// just clicked, so when it doesn't take the window flashes its title
// until it is looked at, and pulses its page when it is.
function comeForward(from) {
  try { window.focus(); } catch { /* refused */ }
  setTimeout(() => {
    const ok = !document.hidden && document.hasFocus();
    send({ t: 'focused', to: from, ok });
    if (!ok) flashTitle();
    if (document.hidden) pulseOnShow = true;
    else pulse();
  }, 150);
}

const mark = '● ';
let flashTimer = 0, pulseOnShow = false;

// The title flashes by its mark, put on and taken off, whatever the page
// sets the title to meanwhile.
function setMark(on) {
  const t = document.title.startsWith(mark) ? document.title.slice(mark.length) : document.title;
  document.title = on ? mark + t : t;
}

function flashTitle() {
  clearInterval(flashTimer);
  let on = false;
  flashTimer = setInterval(() => { on = !on; setMark(on); }, 800);
  setMark(true);
}

function seen() {
  if (flashTimer) { clearInterval(flashTimer); flashTimer = 0; setMark(false); }
  if (pulseOnShow && !document.hidden) { pulseOnShow = false; pulse(); }
}
window.addEventListener('focus', seen);
document.addEventListener('visibilitychange', () => { if (!document.hidden) seen(); });

function pulse() {
  const el = document.documentElement;
  el.classList.remove('win-pulse');
  void el.offsetWidth; // to start it again
  el.classList.add('win-pulse');
  setTimeout(() => el.classList.remove('win-pulse'), 1300);
}

// windowsOn are this window's number and those of the others on the
// project key names, for saying who closed a project and who else shows
// it.
export function windowsOn(key) {
  const all = windows();
  return { self: (all.find((w) => w.self) || {}).n || 0, others: all.filter((w) => !w.self && w.project === key).map((w) => w.n) };
}

// The palette's command finds the list from anywhere.
hooks.otherWindows = others;
hooks.windowsOn = windowsOn;
hooks.openWindows = () => openWindows(document.querySelector('.win-pill'));
