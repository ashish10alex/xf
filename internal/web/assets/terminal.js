// The log pane's terminal tab: xterm.js on the project's shell,
// which the server owns. Output comes over a WebSocket of its own; keys
// and the size go as POSTs. xterm loads the first time the tab shows.

import { S, post, redraw, say, setFocus, termReserved, termFocused, onMac } from './state.js';

let term = null, fit = null, host = null, loading = null;
let ws = null, wsKey = '', retry = null;
let queued = '', sending = false;
let sized = '';
// replaying is set while xterm writes the screen so far. A program's
// queries in it (a TUI's mode or colour query) were answered when they
// were new: answering again would type the replies at the shell's prompt.
let replaying = false;

// mountTerm puts the terminal into el, the tab's box, loading xterm the
// first time; focused gives it the keys.
export function mountTerm(el, focused) {
  if (!loading) loading = load();
  loading.then(() => {
    if (!el.isConnected) return;
    if (host.parentNode !== el) el.appendChild(host);
    keepStream();
    fitNow();
    if (focused && !termFocused()) term.focus();
  }).catch((e) => say('could not load the terminal: ' + e.message));
}

async function load() {
  const css = document.createElement('link');
  css.rel = 'stylesheet';
  css.href = 'vendor/xterm.css';
  document.head.appendChild(css);
  const { Terminal, FitAddon } = await import('./vendor/xterm.mjs');
  term = new Terminal({
    fontFamily: cssVar('--mono') || 'monospace', fontSize: 13, cursorBlink: true, scrollback: 5000,
    macOptionIsMeta: true, theme: termTheme(),
  });
  // The terminal takes on each theme shown: one picked, one the picker
  // previews, or auto's other one.
  new MutationObserver(() => { term.options.theme = termTheme(); })
    .observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  fit = new FitAddon();
  term.loadAddon(fit);
  host = document.createElement('div');
  host.className = 'term-screen';
  term.open(host);
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
      if (e.type === 'keydown') { e.preventDefault(); type('\x15'); }
      return false;
    }
    return true;
  });
  term.onData((d) => { if (!replaying) type(d); });
  host.addEventListener('focusin', () => { if (S.focus !== 'log') { setFocus('log'); redraw(); } });
  new ResizeObserver(() => fitNow()).observe(host);
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

const isCmdBackspace =(e) => onMac && e.metaKey && e.key === 'Backspace' && !e.ctrlKey && !e.altKey && !e.shiftKey;

function fitNow() {
  if (!host || !host.isConnected || !host.offsetWidth || !host.offsetHeight) return;
  try { fit.fit(); } catch { return; }
  const size = term.rows + 'x' + term.cols;
  if (size === sized) return;
  sized = size;
  post('/api/term/resize', { rows: term.rows, cols: term.cols }).catch(() => {});
}

// type sends what was typed, in order: keys typed while a POST is out go
// together in the next.
function type(d) {
  queued += d;
  if (sending) return;
  sending = true;
  (async () => {
    while (queued) {
      const data = queued;
      queued = '';
      try { await post('/api/term/input', { data }); } catch (e) { say('terminal: ' + e.message); }
    }
    sending = false;
  })();
}

// keepStream opens the output stream unless one is open for this page's
// project. A stream that closed while the tab was hidden (xf web
// restarted, say) wasn't retried, so showing the tab opens it again:
// without it, keys reach the shell and nothing comes back.
export function keepStream() {
  if (!ws || wsKey !== S.key) connect();
}

// connect opens the output stream for this page's project. The server
// sends the screen so far first, so a reconnect starts from a clean one.
function connect() {
  clearTimeout(retry);
  if (ws) { ws.onclose = null; ws.close(); }
  wsKey = S.key;
  sized = '';
  const q = new URLSearchParams();
  if (S.key) q.set('project', S.key);
  const sock = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/term/stream?' + q);
  sock.binaryType = 'arraybuffer';
  ws = sock;
  let first = true;
  sock.onmessage = (m) => {
    const data = new Uint8Array(m.data);
    if (!first) { term.write(data); return; }
    first = false;
    term.reset();
    replaying = true;
    term.write(data, () => { replaying = false; });
  };
  sock.onopen = () => fitNow();
  sock.onclose = () => {
    if (ws !== sock) return;
    ws = null;
    // Back after a second while the project is still this page's.
    retry = setTimeout(() => { if (S.mode === 'project' && wsKey === S.key && host && host.isConnected) connect(); }, 1000);
  };
}
