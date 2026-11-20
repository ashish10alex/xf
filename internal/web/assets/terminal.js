// The log pane's terminal tab: an xterm.js for each of the project's
// shells, which the server owns. Each shell's output comes over a
// WebSocket of its own; keys and the size go as POSTs, naming the shell.
// xterm loads the first time the tab shows.

import { S, hooks, post, redraw, say, setFocus, termReserved, onMac } from './state.js';
import { activeTerm } from './log-tabs.js';

let loading = null, Terminal = null, FitAddon = null;

// sessions are the shells this page has shown or streamed, by id: the
// stream and keys of each, and its xterm once xterm has loaded.
const sessions = new Map();

function session(id) {
  let s = sessions.get(id);
  if (!s) {
    s = {
      id, term: null, fit: null, host: null,
      ws: null, wsKey: '', retry: null,
      queued: '', sending: false, sized: '',
      // replaying is set while xterm writes the screen so far. A
      // program's queries in it (a TUI's mode or colour query) were
      // answered when they were new: answering again would type the
      // replies at the shell's prompt.
      replaying: false,
    };
    sessions.set(id, s);
  }
  return s;
}

// mountTerm puts shell id's terminal into el, the tab's box, loading
// xterm the first time; focused gives it the keys.
export function mountTerm(el, focused, id = activeTerm()) {
  if (!loading) loading = load();
  loading.then(() => {
    if (!el.isConnected) return;
    const s = session(id);
    if (!s.term) open(s);
    if (s.host.parentNode !== el) el.replaceChildren(s.host);
    keepStream(id);
    fitNow(s);
    if (focused && !s.host.contains(document.activeElement)) s.term.focus();
  }).catch((e) => say('could not load the terminal: ' + e.message));
}

async function load() {
  const css = document.createElement('link');
  css.rel = 'stylesheet';
  css.href = 'vendor/xterm.css';
  document.head.appendChild(css);
  ({ Terminal, FitAddon } = await import('./vendor/xterm.mjs'));
  // Each terminal takes on each theme shown: one picked, one the picker
  // previews, or auto's other one.
  new MutationObserver(() => { for (const s of sessions.values()) if (s.term) s.term.options.theme = termTheme(); })
    .observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  hooks.termsChanged = prune;
}

// open makes s its xterm.
function open(s) {
  const term = new Terminal({
    fontFamily: cssVar('--mono') || 'monospace', fontSize: 13, cursorBlink: true, scrollback: 5000,
    macOptionIsMeta: true, theme: termTheme(),
  });
  s.term = term;
  s.fit = new FitAddon();
  term.loadAddon(s.fit);
  s.host = document.createElement('div');
  s.host.className = 'term-screen';
  term.open(s.host);
  // xterm's hidden textarea takes the keys. Password managers (iCloud
  // Passwords' "Enable Password AutoFill" bubble, 1Password, LastPass,
  // Bitwarden) look for a field to fill: tell them this is not one.
  for (const [k, v] of Object.entries({ autocomplete: 'off', 'data-form-type': 'other', 'data-1p-ignore': 'true',
    'data-lpignore': 'true', 'data-bwignore': 'true', 'data-protonpass-ignore': 'true' })) term.textarea.setAttribute(k, v);
  // xterm leaves xf's own keys to xf.
  term.attachCustomKeyEventHandler((e) => {
    if (termReserved(e)) return false;
    // ⌘⌫ clears the line as in a Mac's terminals: xterm ignores ⌘ and
    // would send one backspace, so send ctrl+u instead.
    if (isCmdBackspace(e)) {
      if (e.type === 'keydown') { e.preventDefault(); type(s, '\x15'); }
      return false;
    }
    return true;
  });
  term.onData((d) => { if (!s.replaying) type(s, d); });
  s.host.addEventListener('focusin', () => {
    if (S.focus !== 'log' || S.termId !== s.id) { S.termId = s.id; setFocus('log'); redraw(); }
  });
  new ResizeObserver(() => fitNow(s)).observe(s.host);
}

// prune lets go of the shells that were closed, here or in another page.
function prune() {
  for (const [id, s] of sessions) {
    if (S.terms.includes(id)) continue;
    sessions.delete(id);
    clearTimeout(s.retry);
    if (s.ws) { s.ws.onclose = null; s.ws.close(); }
    if (s.term) { s.host.remove(); s.term.dispose(); }
  }
}

const cssVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim() || undefined;

// termTheme is xterm's colours from the theme shown. The cursor is xf's
// own, --focus, which stands out on a light or a dark screen (vim's may
// not be the theme's), and the letter under it is the background.
function termTheme() {
  const bg = cssVar('--bg');
  return { background: bg, foreground: cssVar('--fg'), cursor: cssVar('--focus'), cursorAccent: bg,
    selectionBackground: cssVar('--sel'),
    red: cssVar('--red'), green: cssVar('--green'), yellow: cssVar('--amber'), blue: cssVar('--blue'), magenta: cssVar('--purple') };
}

const isCmdBackspace = (e) => onMac && e.metaKey && e.key === 'Backspace' && !e.ctrlKey && !e.altKey && !e.shiftKey;

function fitNow(s) {
  const { host, term } = s;
  if (!host || !host.isConnected || !host.offsetWidth || !host.offsetHeight) return;
  try { s.fit.fit(); } catch { return; }
  const size = term.rows + 'x' + term.cols;
  if (size === s.sized) return;
  s.sized = size;
  post('/api/term/resize', { term: s.id, rows: term.rows, cols: term.cols }).catch(() => {});
}

// type sends what was typed, in order: keys typed while a POST is out go
// together in the next.
function type(s, d) {
  s.queued += d;
  if (s.sending) return;
  s.sending = true;
  (async () => {
    while (s.queued) {
      const data = s.queued;
      s.queued = '';
      try { await post('/api/term/input', { term: s.id, data }); } catch (e) { say('terminal: ' + e.message); }
    }
    s.sending = false;
  })();
}

// keepStream opens shell id's output stream unless one is open for this
// page's project. A stream that closed while the tab was hidden (xf web
// restarted, say) wasn't retried, so showing the tab opens it again:
// without it, keys reach the shell and nothing comes back.
export function keepStream(id = activeTerm()) {
  const s = session(id);
  if (!s.ws || s.wsKey !== S.key) connect(s);
}

// connect opens s's output stream for this page's project. The server
// sends the screen so far first, so a reconnect starts from a clean one.
function connect(s) {
  clearTimeout(s.retry);
  if (s.ws) { s.ws.onclose = null; s.ws.close(); }
  s.wsKey = S.key;
  s.sized = '';
  const q = new URLSearchParams();
  if (S.key) q.set('project', S.key);
  q.set('term', String(s.id));
  const sock = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/term/stream?' + q);
  sock.binaryType = 'arraybuffer';
  s.ws = sock;
  let first = true;
  sock.onmessage = (m) => {
    if (!s.term) return;
    const data = new Uint8Array(m.data);
    if (!first) { s.term.write(data); return; }
    first = false;
    s.term.reset();
    s.replaying = true;
    s.term.write(data, () => { s.replaying = false; });
  };
  sock.onopen = () => fitNow(s);
  sock.onclose = () => {
    if (s.ws !== sock) return;
    s.ws = null;
    // Back after a second while the project is still this page's and
    // the shell still open.
    s.retry = setTimeout(() => {
      if (S.mode === 'project' && s.wsKey === S.key && sessions.get(s.id) === s && s.host && s.host.isConnected) connect(s);
    }, 1000);
  };
}
