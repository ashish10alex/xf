// A Python file in the source pane, checked by pyrefly's language server
// on the server (internal/web/python.go, ADR 0018):
//
//   - its errors, marked where they are, for the text as typed: the text
//     goes to pyrefly a moment after each edit
//   - K (or the mouse) shows pyrefly's hover in a card, whose links go to
//     the places they name
//   - gd goes to the definition, in the file, another of the project's, or
//     a library's, which the pane shows read-only
//   - completion, and the signature of a call being written above it
//   - the interpreter pyrefly runs with: the status line shows it, and
//     Python: select interpreter picks another (python-view.js)
//   - the first time a project without a pyrefly config shows a Python
//     file, an offer to write one
//
// source-smart.js loads this module the first time the pane shows a
// Python file, and gives the editor pythonExtensions().

import * as cm from './vendor/codemirror.mjs';
import { S, get, post, redraw, hooks, commandsKey, say } from './state.js';
import { SRC, sourceEditor, goToPlace } from './source.js';
import { jumpWithin } from './editor.js';

export const PY = {
  file: '',      // the Python file the pane shows, '' for none
  status: null,  // /api/python/status for it
  // unavailable is why pyrefly can't check the file now (it failed to
  // start, or is off), '' when it can.
  unavailable: '',
  said: new Set(),    // the projects told pyrefly is missing, by root
  offered: new Set(), // the projects offered a config while the page lives
  // The overlay ('python'): kind 'interpreter' is the picker, with its
  // rows; 'config' is the offer to write pyrefly.toml.
  overlay: null,
};

// syncMs is how long after an edit the text goes to pyrefly.
const syncMs = 300;

const isPy = () => !!(PY.file && SRC.view && SRC.view.lang === 'python' && SRC.file === PY.file);

// placeOf is the LSP place of offset off: its line and UTF-16 column.
function placeOf(doc, off) {
  const line = doc.lineAt(off);
  return { line: line.number - 1, character: off - line.from };
}

// offsetOf is the offset of an LSP place in doc.
function offsetOf(doc, p) {
  if (p.line >= doc.lines) return doc.length;
  const line = doc.line(p.line + 1);
  return line.from + Math.min(p.character, line.length);
}

// ---- the text and its errors ----

// The versions of the text pyrefly has, by version: its errors arrive as
// "python" events for a version, and are shown while the editor still has
// that text. Errors for a version not known yet wait for it.
const texts = new Map();
const early = new Map();
let syncTimer = 0;

function noteVersion(v, text, diagnostics) {
  if (!v) return;
  texts.set(v, text);
  while (texts.size > 8) texts.delete(texts.keys().next().value);
  if (diagnostics) showErrors(v, diagnostics);
  else if (early.has(v)) showErrors(v, early.get(v));
  early.delete(v);
}

function showErrors(version, diagnostics) {
  const ed = sourceEditor();
  if (!ed || !isPy()) return;
  const text = texts.get(version);
  const doc = ed.view.state.doc;
  if (text === undefined || doc.toString() !== text) return; // typed since: the next sync's come
  // A library's errors aren't yours to fix: none are marked.
  if (SRC.view.readOnly && SRC.view.readOnly.startsWith('a library file')) diagnostics = [];
  const marks = diagnostics.map((d) => {
    const from = offsetOf(doc, d.range.start);
    const to = Math.max(offsetOf(doc, d.range.end), from);
    const severity = d.severity === 'hint' || d.severity === 'info' ? 'info' : d.severity;
    // The line's end shows the message's first line; its hover, all of it.
    const code = d.code ? ' [' + d.code + ']' : '';
    const first = d.message.split('\n')[0];
    const mark = { from, to: to > from ? to : Math.min(from + 1, doc.length), severity, message: first + code, source: 'pyrefly' };
    if (first !== d.message) {
      mark.full = d.message + code;
      mark.renderMessage = () => el('div', 'py-diag', mark.full);
    }
    return mark;
  });
  if (hooks.sourcePythonMarks) hooks.sourcePythonMarks(marks);
}

hooks.pythonEvent = (ev) => {
  if (!isPy() || ev.file !== PY.file) return;
  if (texts.has(ev.version)) showErrors(ev.version, ev.diagnostics || []);
  else early.set(ev.version, ev.diagnostics || []);
};

// send posts a request about the editor's text at offset off (sync has
// none), and notes the version of the text pyrefly then has.
async function send(path, off) {
  const ed = sourceEditor();
  if (!ed || !isPy()) return null;
  const text = ed.view.state.doc.toString();
  const body = { file: PY.file, text };
  if (off != null) body.pos = placeOf(ed.view.state.doc, off);
  const file = PY.file;
  let res;
  try {
    res = await post(path, body);
  } catch (e) {
    if (e.kind === 'python' && file === PY.file) { PY.unavailable = e.message; redraw(); }
    throw e;
  }
  if (file !== PY.file) return null;
  if (PY.unavailable) { PY.unavailable = ''; redraw(); }
  noteVersion(res.version, text, path === '/api/python/sync' ? res.diagnostics || [] : null);
  return res;
}

async function sync() {
  clearTimeout(syncTimer);
  try { await send('/api/python/sync'); } catch { /* said in the status line */ }
}

function syncSoon() {
  clearTimeout(syncTimer);
  syncTimer = setTimeout(sync, syncMs);
}

// ---- showing a file ----

// pythonShown follows the file the pane loaded (v, or none): the Python
// file before is closed in pyrefly, a Python one is synced, and its
// project offered a config or told pyrefly is missing.
export async function pythonShown(v) {
  const file = v && v.lang === 'python' ? v.file : '';
  if (PY.file && PY.file !== file) post('/api/python/close', { file: PY.file }).catch(() => {});
  if (PY.file !== file) { texts.clear(); early.clear(); }
  PY.file = file;
  PY.status = null;
  PY.unavailable = '';
  clearTimeout(syncTimer);
  if (!file) { redraw(); return; }
  await readStatus();
  const st = PY.status;
  if (!st || st.off || PY.file !== file) return;
  const root = (S.boot && S.boot.root) || '';
  if (st.missing) {
    if (!PY.said.has(root)) { PY.said.add(root); say(st.missing); }
    return;
  }
  if (st.offer && !PY.offered.has(root) && !S.overlay) {
    PY.offered.add(root);
    openConfigOffer();
  }
  sync();
}

async function readStatus() {
  const file = PY.file;
  try {
    const st = await get('/api/python/status?file=' + encodeURIComponent(file));
    if (file === PY.file) PY.status = st;
  } catch { /* none */ }
  redraw();
}

// ---- hover and K ----

const setCard = cm.StateEffect.define();
const cardField = cm.StateField.define({
  create: () => null,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setCard)) return e.value;
    return v && (tr.docChanged || tr.selection) ? null : v;
  },
  provide: (f) => cm.showTooltip.compute([f], (state) => {
    const v = state.field(f);
    return v ? { pos: v.pos, end: v.end, above: true, create: () => ({ dom: v.make() }) } : null;
  }),
});

// hoverAt is pyrefly's hover at pos: {from, to, make} for its card, or
// null for nothing.
async function hoverAt(view, pos) {
  let res;
  try { res = await send('/api/python/hover', pos); } catch { return null; }
  if (!res || !res.markdown) return null;
  const doc = view.state.doc;
  const from = res.range ? offsetOf(doc, res.range.start) : pos;
  const to = res.range ? offsetOf(doc, res.range.end) : pos;
  return { from, to, make: () => hoverCard(res.markdown, res.links || {}) };
}

const hover = cm.hoverTooltip(async (view, pos) => {
  if (!isPy()) return null;
  const h = await hoverAt(view, pos);
  return h ? { pos: h.from, end: h.to, above: true, create: () => ({ dom: h.make() }) } : null;
}, { hoverTime: 350 });

// pythonCard is K: the hover at the cursor in a card, or off again.
export async function pythonCard(view) {
  if (view.state.field(cardField, false)) { view.dispatch({ effects: setCard.of(null) }); return; }
  if (PY.unavailable) { say(PY.unavailable); return; }
  const h = await hoverAt(view, view.state.selection.main.head);
  if (!h) { say('nothing under the cursor'); return; }
  view.dispatch({ effects: setCard.of({ pos: h.from, end: h.to, make: h.make }) });
}

const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
};

// hoverCard is pyrefly's Markdown as a card: its code blocks as code, the
// rest as lines with `code`, **bold** and links; a link to a file goes
// there (links has where, by its URI).
export function hoverCard(md, links) {
  const root = el('div', 'sc-card py-card');
  for (const block of mdBlocks(md)) {
    if (block.code !== undefined) {
      root.append(el('pre', 'sc-fn-sig', block.code));
    } else if (block.rule) {
      root.append(el('hr', 'py-rule'));
    } else {
      const line = el('div', 'sc-card-line');
      inline(line, block.text, links);
      root.append(line);
    }
  }
  return root;
}

// mdBlocks splits Markdown into code blocks, rules and paragraphs; a
// paragraph keeps its line breaks (a line ending in two spaces or a
// backslash, as pyrefly's docstrings are written).
export function mdBlocks(md) {
  const out = [];
  const lines = md.replace(/\r\n/g, '\n').split('\n');
  let para = [];
  const endPara = () => {
    const text = para.join('\n').replace(/(?: {2,}|\\)\n/g, '\n').replace(/&nbsp;/g, ' ').trim();
    if (text) out.push({ text });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    if (/^\s*```/.test(l)) {
      endPara();
      const code = [];
      for (i++; i < lines.length && !/^\s*```/.test(lines[i]); i++) code.push(lines[i]);
      out.push({ code: code.join('\n') });
    } else if (/^\s*(?:---+|\*\*\*+)\s*$/.test(l)) {
      endPara();
      out.push({ rule: true });
    } else if (!l.trim()) {
      endPara();
    } else para.push(l);
  }
  endPara();
  return out;
}

// inline fills parent with text's `code`, **bold** and [links](…).
function inline(parent, text, links) {
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)\s]+)\)/g;
  let at = 0;
  for (let m; (m = re.exec(text));) {
    if (m.index > at) parent.append(document.createTextNode(text.slice(at, m.index)));
    if (m[1] !== undefined) parent.append(el('code', '', m[1]));
    else if (m[2] !== undefined) parent.append(el('b', '', m[2]));
    else {
      const href = m[4];
      const a = el('a', 'sc-card-link', m[3]);
      const to = links[href];
      if (to) {
        a.href = '#';
        a.title = to.file + ':' + (to.line + 1);
        a.addEventListener('mousedown', (e) => { e.preventDefault(); goTo(to); });
      } else if (/^https?:/.test(href)) {
        a.href = href; a.target = '_blank'; a.rel = 'noopener';
      }
      parent.append(a);
    }
    at = m.index + m[0].length;
  }
  if (at < text.length) parent.append(document.createTextNode(text.slice(at)));
}

// ---- go to definition ----

// goTo goes to a place pyrefly named: in the file shown, the cursor moves;
// else the pane shows the file there (a library's read-only), and ctrl+o
// comes back.
function goTo(l) {
  const ed = sourceEditor();
  if (!ed) return;
  const view = ed.view;
  if (view.state.field(cardField, false)) view.dispatch({ effects: setCard.of(null) });
  if (l.file === SRC.file) {
    jumpWithin(view, offsetOf(view.state.doc, { line: l.line, character: l.col }));
    return;
  }
  goToPlace({ file: l.file, action: '', line: l.line, col: l.col });
  if (l.library) say('a library file: read-only (ctrl+o comes back)');
}

// pythonDefinition is gd.
export async function pythonDefinition(view) {
  if (PY.unavailable) { say(PY.unavailable); return; }
  const file = SRC.file;
  let res;
  try { res = await send('/api/python/definition', view.state.selection.main.head); } catch (e) { say(e.message); return; }
  if (!res || file !== SRC.file) return;
  if (!res.locations.length) { say('no definition under the cursor'); return; }
  goTo(res.locations[0]);
  if (res.locations.length > 1) say(res.locations[0].file + ' (1 of ' + res.locations.length + ' definitions)');
}

// ---- completion ----

const cmTypes = {
  function: 'function', method: 'method', constructor: 'function', class: 'class', interface: 'interface',
  struct: 'class', enum: 'enum', module: 'namespace', keyword: 'keyword', variable: 'variable',
  field: 'property', property: 'property', constant: 'constant', 'enum-member': 'constant', value: 'variable',
  'type-parameter': 'type',
};

// pythonComplete is the editor's completion in a Python file: after a
// name's first letter or a dot, or when asked (ctrl+space).
export async function pythonComplete(context) {
  if (!isPy()) return null;
  const word = context.matchBefore(/[A-Za-z_]\w*$/);
  const dot = context.matchBefore(/\.$/);
  if (!context.explicit && !word && !dot) return null;
  let res;
  try { res = await send('/api/python/complete', context.pos); } catch { return null; }
  if (!res || !res.items.length || context.aborted) return null;
  const from = word ? word.from : context.pos;
  // pyrefly's order, by its sort text, breaks ties between equal matches.
  const order = res.items.map((it, i) => ({ it, i })).sort((a, b) => (a.it.sort || '').localeCompare(b.it.sort || '') || a.i - b.i);
  const n = order.length;
  const options = order.map(({ it }, rank) => {
    const o = { label: it.label, type: cmTypes[it.kind] || 'text', boost: Math.round(4 - (8 * rank) / n) };
    if (it.detail) o.detail = it.detail;
    if (it.insert && it.insert !== it.label) o.apply = it.insert;
    return o;
  });
  return { from, options, validFor: res.incomplete ? undefined : /^\w*$/ };
}

// ---- signature help ----

// The signature of the call being written shows above the cursor, its
// parameter at the cursor in bold: on once a ( or , is typed, asked
// again as the cursor moves, off when pyrefly has none, or on esc.
const setSig = cm.StateEffect.define();
const sigField = cm.StateField.define({
  create: () => null,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setSig)) return e.value;
    return v;
  },
  provide: (f) => cm.showTooltip.compute([f, 'selection'], (state) => {
    const v = state.field(f);
    return v ? { pos: state.selection.main.head, above: true, create: () => ({ dom: sigDOM(v) }) } : null;
  }),
});

function sigDOM(s) {
  const root = el('div', 'sc-sig');
  const line = el('div', 'sc-sig-line');
  const p = s.active >= 0 ? s.params[s.active] : null;
  if (p && p[0] >= 0) {
    line.append(document.createTextNode(s.label.slice(0, p[0])), el('b', '', s.label.slice(p[0], p[1])), document.createTextNode(s.label.slice(p[1])));
  } else line.textContent = s.label;
  root.append(line);
  if (s.doc) {
    const doc = el('div', 'sc-sig-doc');
    const first = mdBlocks(s.doc).find((b) => b.text);
    if (first) inline(doc, first.text, {});
    if (doc.childNodes.length) root.append(doc);
  }
  return root;
}

const sigPlugin = cm.ViewPlugin.fromClass(class {
  constructor(view) { this.view = view; this.timer = 0; this.seq = 0; }
  update(u) {
    if (!isPy()) return;
    let typed = '';
    for (const tr of u.transactions) {
      if (tr.docChanged && tr.isUserEvent('input')) tr.changes.iterChanges((_a, _b, _c, _d, ins) => { typed += ins.toString(); });
    }
    const on = !!u.state.field(sigField, false);
    if (/[(,]/.test(typed) || (on && (u.docChanged || u.selectionSet))) this.ask();
  }
  ask() {
    clearTimeout(this.timer);
    this.timer = setTimeout(async () => {
      const seq = ++this.seq;
      const view = this.view;
      let res = null;
      try { res = await send('/api/python/signature', view.state.selection.main.head); } catch { /* none */ }
      if (seq !== this.seq || !isPy()) return;
      const sig = res && res.signature;
      if (sig || view.state.field(sigField, false)) view.dispatch({ effects: setSig.of(sig || null) });
    }, 120);
  }
  destroy() { clearTimeout(this.timer); }
});

// ---- the editor ----

// pythonExtensions are what the source editor gets for Python files: each
// does nothing in another file.
export function pythonExtensions() {
  return [
    hover,
    cardField,
    sigField,
    sigPlugin,
    cm.EditorView.domEventHandlers({
      keydown(e, view) {
        if (e.key !== 'Escape') return false;
        if (view.state.field(cardField, false)) view.dispatch({ effects: setCard.of(null) });
        if (view.state.field(sigField, false)) view.dispatch({ effects: setSig.of(null) });
        return false;
      },
    }),
    cm.EditorView.updateListener.of((u) => {
      if (u.docChanged && isPy()) syncSoon();
    }),
  ];
}

// ---- the interpreter ----

// interpreterLabel is how the status line names the interpreter: the
// environment's folder (.venv), or "python3 (PATH)" and the like.
export function interpreterLabel(st) {
  if (!st || !st.interpreter) return 'pyrefly\'s default';
  if (st.label) return st.label + (st.where === 'PATH' ? ' (PATH)' : '');
  const parts = st.interpreter.split(/[\\/]/);
  const name = parts[parts.length - 1];
  if (st.where === 'PATH') return name + ' (PATH)';
  const bin = parts.length > 2 && /^(bin|Scripts)$/.test(parts[parts.length - 2]);
  return bin ? parts[parts.length - 3] : name;
}

// takeKeys gives an overlay the keys the editor has; closing it gives
// them back (state.js's returnAfterLayer).
function takeKeys() {
  S.zoomed = false;
  S.overlay = 'python';
  if (hooks.sourceBlur) hooks.sourceBlur();
}

export async function openInterpreterPicker() {
  takeKeys();
  PY.overlay = { kind: 'interpreter', loading: true, list: [], current: '', where: '', cursor: 0, custom: '', saving: false, err: '' };
  redraw();
  try {
    const r = await get('/api/python/interpreters');
    const o = PY.overlay;
    if (!o || o.kind !== 'interpreter') return;
    Object.assign(o, r, { loading: false });
    const rows = interpreterRows();
    const at = rows.findIndex((x) => o.where === 'picked' ? x.path === o.current : x.auto);
    o.cursor = Math.max(at, 0);
  } catch (e) { closePython(); say(e.message); }
  redraw();
}

// interpreterRows are the picker's rows: xf's own choice, each Python
// found, and a path of your own.
export function interpreterRows() {
  const o = PY.overlay;
  if (!o || o.kind !== 'interpreter') return [];
  return [{ auto: true }, ...(o.list || []).filter((i) => i.where !== 'picked'), { custom: true }];
}

export async function pickInterpreter() {
  const o = PY.overlay;
  const row = interpreterRows()[o.cursor];
  if (!row || o.saving) return;
  const path = row.auto ? '' : row.custom ? o.custom.trim() : row.path;
  if (row.custom && !path) { o.err = 'type the path of a Python, e.g. ~/envs/dbt/bin/python'; redraw(); return; }
  o.saving = true; o.err = '';
  redraw();
  try {
    PY.status = await post('/api/python/interpreter', { path });
    closePython();
    say('pyrefly checks with ' + (path || interpreterLabel(PY.status) + ' (found by xf)'));
    if (PY.file) { await readStatus(); sync(); }
  } catch (e) {
    o.saving = false;
    o.err = e.message;
  }
  redraw();
}

// ---- the config ----

export function openConfigOffer() {
  takeKeys();
  PY.overlay = { kind: 'config', saving: false, err: '' };
  redraw();
}

// createConfig writes pyrefly.toml, after which pyrefly checks the file
// again.
export async function createConfig() {
  const o = PY.overlay;
  if (o) { o.saving = true; o.err = ''; }
  redraw();
  try {
    const r = await post('/api/python/config', { create: true });
    closePython();
    say(r.message + ': pyrefly now reports type errors');
    if (PY.file) { await readStatus(); sync(); }
  } catch (e) {
    if (o && PY.overlay === o) { o.saving = false; o.err = e.message; } else say(e.message);
  }
  redraw();
}

function declineConfig() {
  closePython();
  post('/api/python/config', { create: false }).catch(() => {});
  say('no pyrefly.toml: ' + commandsKey + ' › Python: create pyrefly config writes one');
}

export function closePython() {
  if (S.overlay === 'python') S.overlay = null;
  PY.overlay = null;
}

// pythonKey is the overlay's keys.
export function pythonKey(k) {
  const o = PY.overlay;
  if (!o) { S.overlay = null; return true; }
  if (o.kind === 'config') {
    if (o.saving) return true;
    if (k === 'y' || k === 'enter') createConfig();
    else if (k === 'n') declineConfig();
    else if (k === 'esc' || k === 'q') { closePython(); say('not now: Python: create pyrefly config writes it'); }
    return true;
  }
  const rows = interpreterRows();
  switch (k) {
    case 'esc': case 'q': closePython(); return true;
    case 'j': case 'down': case 'ctrl+n': o.cursor = Math.min(o.cursor + 1, rows.length - 1); return true;
    case 'k': case 'up': case 'ctrl+p': o.cursor = Math.max(o.cursor - 1, 0); return true;
    case 'enter': pickInterpreter(); return true;
  }
  return true;
}

// ---- the palette's commands and the status line ----

hooks.pythonCommands = () => {
  if (!PY.file) return [];
  const st = PY.status;
  const out = [['Python: select interpreter', '', openInterpreterPicker, interpreterLabel(st)]];
  if (st && !st.off && !st.config) out.push(['Python: create pyrefly config', '', openConfigOffer]);
  return out;
};
hooks.pythonKey = pythonKey;
hooks.pythonShown = pythonShown;
hooks.pythonCard = pythonCard;
hooks.pythonDefinition = pythonDefinition;
hooks.pythonComplete = pythonComplete;
