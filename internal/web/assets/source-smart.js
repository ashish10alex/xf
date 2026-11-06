// What the source pane knows of its file, from xf's language tooling on
// the server (internal/navigate and internal/diagnose, which xf lsp uses):
//
//   - completion of ${ref("…")} after a $, of names in a config's
//     dependencies and tags, of a config's keys (navigate's config.go)
//     and the values of those that take a fixed set, and in a dbt model
//     of refs, sources and macros
//   - completion of the columns of the tables the file refers to, from the
//     Scratch view's table cards: after alias. only that table's
//   - K (or the mouse) on a ref shows its table's card, or says why it
//     has no schema
//   - K (or the mouse) on a config key shows what it does (navigate's
//     config.go), its values and the action types that read it
//   - K (or the mouse) on an includes name in JavaScript (parameters.X,
//     X declared in includes/parameters.js) shows its declaration as
//     written, and the comment above it; the server reads that one file
//   - gd goes to where what is under the cursor is defined, as xf lsp's
//     go-to-definition does: a ref's action (its file shown in the pane),
//     an includes name, a require()'s file, a CTE
//   - in a .sqlx file's SQL, BigQuery's functions as in the Scratch view
//     (scratch-functions.js): completion below the columns, the values an
//     argument takes, K (or the mouse) on a call shows its card, and the
//     signature help above a call being written
//   - the last compile's errors and the file's dry run errors, marked where
//     they are in the file, as saved; edits made since move them along
//   - with defer to prod on (ADR 0031), a label after each ref() saying
//     whether its table is read from prod or from dev, as saved
//
// A Python file has pyrefly's instead (python.js, loaded the first time
// the pane shows one), and the compile's errors beside them.

import * as cm from './vendor/codemirror.mjs';
import { S, get, post, redraw, hooks, setFocus, say, onMac } from './state.js';
import { SRC, sourceEditor, goToPlace, isLibraryFile } from './source.js';
import { wordMarks } from './diff.js';
import { jumpWithin } from './editor.js';
import { tableCard, cardDOM, keyedCard, keyOnFocus, columnsOf, mergeColumns, quoteIdent, words, NOT_ALIAS, blank, inFromList, byParts, afterTable, madeColumns } from './scratch-complete.js';
import { loadFunctions, functionOptions, clauseOptions, inPivotAggregate, valueOptions, functionAt, functionCard, functionExtensions } from './scratch-functions.js';
import { sqlText, inJavaScript } from './sqlx.js';
import { pathSource, unlessPaths, pathExtensions } from './source-paths.js';

// ---- the file's tables ----

// refsOf asks which tables text refers to, once for each text.
let lastRefs = { text: null, p: null };
function refsOf(text) {
  if (lastRefs.text === text && lastRefs.p) return lastRefs.p;
  const p = post('/api/source/refs', { file: SRC.file, text }).catch(() => null);
  lastRefs = { text, p };
  return p;
}

// aliasOf is the name text gives the table it refers to at r: the word
// after it (after the } or }} that ends its ${ } or {{ }}), AS or not.
function aliasOf(text, r) {
  const after = text.slice(r.to, r.to + 120);
  const m = (r.source === 'raw' ? /^`?\s*(?:as\s+)?([A-Za-z_]\w*)/i : /^\s*\)?\s*\}{1,2}\s*(?:as\s+)?([A-Za-z_]\w*)/i).exec(after);
  if (!m || NOT_ALIAS.has(m[1].toLowerCase())) return '';
  return m[1];
}

const inSpans = (spans, pos) => spans.some(([a, b]) => pos > a && pos <= b);

// sqlOf is the editor's text as SQL, its JavaScript blanked, in a .sqlx
// file, where BigQuery's functions are offered; null in any other.
const sqlOf = (state) => (SRC.view && SRC.view.lang === 'sqlx' ? sqlText(state) : null);

// isPy says the pane shows a Python file, which pyrefly knows of.
const isPy = () => !!(SRC.view && SRC.view.lang === 'python');

// ---- Python ----

// python.js comes with the first Python file, and puts its editor
// extensions in pyExtensions.
const pyExtensions = new cm.Compartment();
let pyLoad = null;
function loadPython() {
  if (!pyLoad) {
    pyLoad = Promise.all([import('./python.js'), import('./python-view.js')]).then(([m]) => {
      const ed = sourceEditor();
      if (ed) ed.view.dispatch({ effects: pyExtensions.reconfigure(m.pythonExtensions()) });
      return m;
    });
    pyLoad.catch(() => { pyLoad = null; });
  }
  return pyLoad;
}

// pythonShown tells python.js which file loaded, once it is here.
function pythonShown(v) {
  if (v && v.lang === 'python') loadPython().then((m) => m.pythonShown(v)).catch((e) => say(e.message));
  else if (hooks.pythonShown) hooks.pythonShown(v);
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
const closeCard = (view) => () => { if (view.state.field(cardField, false)) view.dispatch({ effects: setCard.of(null) }); };

// pinHover is a card the mouse showed, which a click in it makes K's,
// with K's keys (f filters its columns, …).
const pinHover = (view, pos, end, dom) => keyOnFocus(dom, (d) => view.dispatch({ effects: [cm.closeHoverTooltips, setCard.of({ pos, end, make: () => keyedCard(d, view, closeCard(view)) })] }));

// noSchema is why a ref's table has no schema to show: an operation
// without hasOutput, an ephemeral model and the like build no table.
function noSchema(hit) {
  if (hit.kind === 'operation') return 'no schema: this operation has no output table (set hasOutput: true to give it one)';
  return 'no schema: ' + (hit.kind ? 'a ' + hit.kind : hit.table) + ' has no table of its own';
}

// noteDOM is the card of a ref with no schema to show, saying so, so a
// hover never looks like it did nothing.
function noteDOM(hit, note) {
  const root = document.createElement('div');
  root.className = 'sc-card';
  const head = document.createElement('div');
  head.className = 'sc-card-head';
  if (hit.kind) {
    const k = document.createElement('span');
    k.className = 'sc-card-kind';
    k.textContent = hit.kind;
    head.append(k);
  }
  const title = document.createElement('div');
  title.className = 'sc-card-title';
  title.textContent = [hit.project, hit.dataset, hit.table].filter(Boolean).join('.');
  head.append(title);
  const warn = document.createElement('div');
  warn.className = 'sc-card-warn';
  warn.textContent = note;
  root.append(head, warn);
  return root;
}

// refAt is the table referred to at pos and a make for its card: the
// table's, or a note saying why it has no schema; a reason when there is
// no ref there. With defer to prod on, the card says what the ref reads,
// and shows prod's table when that is the one read.
async function refAt(state, pos) {
  const res = await refsOf(state.doc.toString());
  const hit = res && res.refs.find((r) => pos >= r.from && pos <= r.to);
  if (!hit) return { why: 'no table under the cursor' };
  const h = labelOf(state, hit);
  const withDefer = (make) => (h ? () => addDefer(make(), h) : make);
  if (!hit.hasTable) return { hit, make: withDefer(() => noteDOM(hit, noSchema(hit))) };
  const read = (h && readsProd(h) && prodTable(h)) || hit;
  const card = await tableCard(read, true);
  if (!card) return { hit, make: withDefer(() => noteDOM(read, 'no schema: the table could not be looked up (is BigQuery signed in?)')) };
  return { hit, make: withDefer(() => cardDOM(card)) };
}

// refUnder is the ref at the cursor ({project, dataset, table, …}), or
// null: what the context menu's Copy Table Name copies.
export async function refUnder(view) {
  const res = await refsOf(view.state.doc.toString());
  const head = view.state.selection.main.head;
  return (res && res.refs.find((r) => head >= r.from && head <= r.to)) || null;
}

// ---- includes ----

// includeWord is where Name starts when pos is on it in name.Name in the
// file's JavaScript (a .js file's, a .sqlx file's config { }, js { } or
// ${ }), else -1. It reads the line only, so a hover anywhere else asks
// the server nothing.
function includeWord(state, pos) {
  const lang = SRC.view && SRC.view.lang;
  if (lang !== 'sqlx' && lang !== 'js') return -1;
  const line = state.doc.lineAt(pos);
  const t = line.text;
  let a = pos - line.from, b = a;
  while (a > 0 && /[\w$]/.test(t[a - 1])) a--;
  while (b < t.length && /[\w$]/.test(t[b])) b++;
  if (a === b || t[a - 1] !== '.' || !/(^|[^\w$.])[A-Za-z_$][\w$]*$/.test(t.slice(0, a - 1))) return -1;
  const at = line.from + a;
  return lang === 'js' || inJavaScript(state, at + 1) ? at : -1;
}

// includeAt asks for the includes name at pos: {from, to, code, doc,
// file, line, col, action}, or null; once for each text and word.
let lastInclude = { text: null, at: -1, p: null };
function includeAt(state, pos) {
  const at = includeWord(state, pos);
  if (at < 0) return null;
  const text = state.doc.toString();
  if (lastInclude.text === text && lastInclude.at === at) return lastInclude.p;
  const p = post('/api/source/include', { file: SRC.file, text, offset: at }).then((r) => r.include, () => null);
  lastInclude = { text, at, p };
  return p;
}

// includeCard shows an includes name's declaration, its comment and a
// link to where it is.
function includeCard(inc) {
  const root = document.createElement('div');
  root.className = 'sc-card sc-fn sc-inc';
  const code = document.createElement('pre');
  code.className = 'sc-fn-sig';
  code.textContent = inc.code;
  root.append(code);
  if (inc.doc) {
    const doc = document.createElement('div');
    doc.className = 'sc-card-line';
    doc.textContent = inc.doc;
    root.append(doc);
  }
  const link = document.createElement('button');
  link.className = 'sc-card-link';
  link.textContent = inc.file + ':' + (inc.line + 1);
  link.title = 'go to the definition (gd)';
  link.onclick = () => goToPlace({ file: inc.file, action: inc.action || '', line: inc.line, col: inc.col });
  const line = document.createElement('div');
  line.className = 'sc-card-line';
  line.append(link);
  root.append(line);
  return root;
}

// ---- config keys ----

// configKeyWord is where the key starts when pos is on one in a .sqlx
// file's JavaScript: a word, quoted or not, before a colon on its line;
// else -1. It reads the line only, so a hover anywhere else asks the
// server nothing; the server says whether it is a config's key.
function configKeyWord(state, pos) {
  if (!SRC.view || SRC.view.lang !== 'sqlx') return -1;
  const line = state.doc.lineAt(pos);
  const t = line.text;
  let a = pos - line.from, b = a;
  while (a > 0 && /\w/.test(t[a - 1])) a--;
  while (b < t.length && /\w/.test(t[b])) b++;
  if (a === b || !/^["']?\s*:/.test(t.slice(b))) return -1;
  const at = line.from + a;
  return inJavaScript(state, at + 1) ? at : -1;
}

// configKeyAt asks for the config key at pos: {from, to, path, kind, doc,
// values, for, configType, unread}, or null; once for each text and key.
let lastKey = { text: null, at: -1, p: null };
function configKeyAt(state, pos) {
  const at = configKeyWord(state, pos);
  if (at < 0) return null;
  const text = state.doc.toString();
  if (lastKey.text === text && lastKey.at === at) return lastKey.p;
  const p = post('/api/source/configkey', { file: SRC.file, text, offset: at }).then((r) => r.key, () => null);
  lastKey = { text, at, p };
  return p;
}

// configKeyCard shows a config key: where it goes and its value's kind,
// what it does, the values it takes and the action types that read it.
function configKeyCard(k) {
  const root = document.createElement('div');
  root.className = 'sc-card sc-fn sc-cfg';
  const sig = document.createElement('pre');
  sig.className = 'sc-fn-sig';
  sig.textContent = k.path + ': ' + k.kind;
  root.append(sig);
  const line = (text, cls) => {
    const d = document.createElement('div');
    d.className = cls || 'sc-card-line';
    d.textContent = text;
    root.append(d);
  };
  line(k.doc);
  if (k.values) line('values: ' + k.values.join(' · '));
  if (k.for) line('for: ' + k.for.join(' · '));
  if (k.unread) line('a ' + k.configType + ' doesn\'t read it', 'sc-card-warn');
  return root;
}

// cardAtCursor is K: the card of the ref, function, includes name or
// config key the cursor is on, or off again.
async function cardAtCursor(view) {
  if (isPy()) { if (hooks.pythonCard) hooks.pythonCard(view); return; }
  if (view.state.field(cardField, false)) { view.dispatch({ effects: setCard.of(null) }); return; }
  const head = view.state.selection.main.head;
  const keyed = (make) => () => keyedCard(make(), view, closeCard(view));
  const fn = functionAt(view.state, head, sqlOf);
  if (fn) { view.dispatch({ effects: setCard.of({ pos: fn.from, end: fn.to, make: keyed(() => functionCard(fn.fn)) }) }); return; }
  const ask = includeAt(view.state, head);
  if (ask) {
    const inc = await ask;
    if (inc) view.dispatch({ effects: setCard.of({ pos: inc.from, end: inc.to, make: keyed(() => includeCard(inc)) }) });
    else say('nothing in includes/ declares that');
    return;
  }
  const askKey = configKeyAt(view.state, head);
  if (askKey) {
    const k = await askKey;
    if (k) { view.dispatch({ effects: setCard.of({ pos: k.from, end: k.to, make: keyed(() => configKeyCard(k)) }) }); return; }
  }
  const r = await refAt(view.state, head);
  if (!r.hit) { say(sqlOf(view.state) === null ? r.why : 'no table or function under the cursor'); return; }
  view.dispatch({ effects: setCard.of({ pos: r.hit.from, end: r.hit.to, make: keyed(r.make) }) });
}

const hover = cm.hoverTooltip(async (view, pos, side) => {
  if (isPy()) return null; // python.js's own
  const lab = side > 0 && labelAt(view.state, pos);
  if (lab) return { pos, above: true, create: () => ({ dom: deferCard(lab) }) };
  const fn = functionAt(view.state, pos, sqlOf);
  if (fn) return { pos: fn.from, end: fn.to, above: true, create: () => ({ dom: pinHover(view, fn.from, fn.to, functionCard(fn.fn)) }) };
  const ask = includeAt(view.state, pos);
  if (ask) {
    const inc = await ask;
    return inc ? { pos: inc.from, end: inc.to, above: true, create: () => ({ dom: pinHover(view, inc.from, inc.to, includeCard(inc)) }) } : null;
  }
  const askKey = configKeyAt(view.state, pos);
  if (askKey) {
    const k = await askKey;
    if (k) return { pos: k.from, end: k.to, above: true, create: () => ({ dom: pinHover(view, k.from, k.to, configKeyCard(k)) }) };
  }
  const r = await refAt(view.state, pos);
  return r.hit ? { pos: r.hit.from, end: r.hit.to, above: true, create: () => ({ dom: pinHover(view, r.hit.from, r.hit.to, r.make()) }) } : null;
}, { hoverTime: 350 });

// ---- go to definition ----

// goToDefinition is gd: the cursor moves to the definition in this file,
// or the pane shows the action defined in another; the first of several.
async function goToDefinition(view) {
  if (isPy()) { if (hooks.pythonDefinition) hooks.pythonDefinition(view); else say('pyrefly is still loading'); return; }
  if (S.plainFile) { say('gd needs an action\'s file: ' + S.plainFile + ' has no action'); return; }
  const file = SRC.file;
  let res;
  try { res = await post('/api/source/definition', { file, text: view.state.doc.toString(), offset: view.state.selection.main.head }); } catch (e) { say(e.message); return; }
  if (file !== SRC.file) return;
  const locs = res.locations;
  if (!locs.length) { say('no definition under the cursor'); return; }
  const l = locs[0];
  const more = locs.length > 1 ? ' (1 of ' + locs.length + ' definitions)' : '';
  if (l.file === file) {
    const line = view.state.doc.line(Math.min(l.line + 1, view.state.doc.lines));
    jumpWithin(view, line.from + Math.min(l.col, line.length));
    if (more) say(file + more);
    return;
  }
  if (l.outside) { say('defined in ' + l.file + ':' + (l.line + 1) + ', outside the project'); return; }
  goToPlace({ file: l.file, action: l.action || '', line: l.line, col: l.col });
  if (more) say(l.file + more);
}

// ---- completion ----

// mayComplete says whether the text before the cursor is where navigate
// completes: after a $, in a string (a ref's, a config array's or a
// config key's value), at the start of a {{ }}, or in a .sqlx file's
// JavaScript where a key may be being typed (after a { or a , or at the
// start of a line), which in a config is one of its keys.
const mayComplete = (before, state, pos) => /\$\w*$/.test(before) || /["'][\w.\- ]*$/.test(before) || /\{\{-?\s*\w*$/.test(before)
  || (SRC.view.lang === 'sqlx' && /(^|[{,\n])[ \t]*[A-Za-z_]\w*$/.test(before) && inJavaScript(state, pos));

// KIND_TYPES is the completion type (its class in the list) of each kind
// of server item; a ref or dependency is a class.
const KIND_TYPES = { macro: 'function', tag: 'keyword', configKey: 'property', configValue: 'enum' };

async function serverSource(context) {
  const { state, pos } = context;
  if (!SRC.view || isPy()) return null;
  const before = state.doc.sliceString(Math.max(0, pos - 200), pos);
  if (!context.explicit && !mayComplete(before, state, pos)) return null;
  let res;
  try { res = await post('/api/source/complete', { file: SRC.file, text: state.doc.toString(), offset: pos }); } catch { return null; }
  if (!res || !res.items.length) return null;
  const from = Math.min(...res.items.map((i) => i.from));
  // Names match as Scratch's tables do, by any part or a few words of
  // them ($0300_ROI finds 0300_PROMOTIONS_DATA_ROI), so the list is
  // filtered here and asked for again at each key.
  const typed = state.doc.sliceString(from, pos).replace(/^\$/, '');
  const options = byParts(res.items.map((i) => ({
    label: i.label, detail: i.detail, apply: i.insert,
    type: KIND_TYPES[i.kind] || 'class', boost: 3,
  })), typed);
  if (!options.length) return null;
  return { from, options, filter: false, getMatch: (o) => o.ranges };
}

// inLiteral says whether pos is in a comment or a string.
function inLiteral(state, pos) {
  const node = cm.syntaxTree(state).resolveInner(pos, -1);
  return /comment|string/.test(node.name) && node.to >= pos && node.from < pos;
}

async function columnSource(context) {
  const { state, pos } = context;
  if (!SRC.view || (SRC.view.lang !== 'sqlx' && SRC.view.lang !== 'sql')) return null;
  const tok = context.matchBefore(/[A-Za-z_][\w]*(?:\.[\w]*)*$|[A-Za-z_]?$/);
  if (!tok) return null;
  // After a $ comes a ref, which serverSource offers.
  if (state.doc.sliceString(tok.from - 1, tok.from) === '$') return null;
  if (inLiteral(state, pos)) return null;
  // An argument of fixed values gets them alone, from functionSource, as do
  // PIVOT and UNPIVOT after a table.
  const fn = functionPlace(state, tok);
  if (fn && fn.values) return null;
  if (clauseSpot(state, tok) || (fn && pivotAggregate(state, tok))) return null;
  if (tok.from === tok.to && !context.explicit) return null;
  const text = state.doc.toString();
  const res = await refsOf(text);
  if (!res || inSpans(res.js, pos)) return null;
  const parts = tok.text.split('.');
  const qual = parts.slice(0, -1);
  const from = tok.from + tok.text.length - parts[parts.length - 1].length;

  // Each table once, with the names the file gives it.
  const tables = new Map();
  for (const r of res.refs) {
    if (!r.hasTable) continue;
    const key = r.project + '.' + r.dataset + '.' + r.table;
    const t = tables.get(key) || { ref: r, names: new Set(), aliases: [] };
    const alias = aliasOf(text, r);
    if (alias) t.names.add(alias.toLowerCase());
    if (alias && !t.aliases.includes(alias)) t.aliases.push(alias);
    t.names.add(r.table.toLowerCase());
    tables.set(key, t);
  }
  let shown = [...tables.values()];
  if (qual.length) {
    shown = shown.filter((t) => t.names.has(qual[0].toLowerCase()));
    if (!shown.length) return null;
  }
  const cards = await Promise.all(shown.map(async (t) => ({ t, card: await tableCard(t.ref) })));
  const options = [];
  const names = new Set();
  const merged = cards.map(({ t, card }) => ({ name: t.ref.table, aliases: t.aliases, owner: t.ref.table, cols: columnsOf(card) }));
  for (const o of mergeColumns(merged, true)) {
    names.add(o.label.toLowerCase());
    if (quoteIdent(o.label) !== o.label) o.apply = quoteIdent(o.label);
    options.push(o);
  }
  const sql = sqlOf(state);
  if (!qual.length && sql !== null) {
    // The columns a PIVOT or UNPIVOT makes.
    for (const c of madeColumns(sql, blank(sql))) {
      if (names.has(c.name.toLowerCase())) continue;
      names.add(c.name.toLowerCase());
      options.push({ label: c.name, detail: c.by, type: 'property', boost: 2, apply: quoteIdent(c.name) !== c.name ? quoteIdent(c.name) : undefined });
    }
  }
  if (!qual.length) {
    for (const w of words(text, from, pos)) {
      if (!names.has(w.toLowerCase())) options.push({ label: w, type: 'text', boost: 0 });
    }
  }
  if (!options.length) return null;
  return { from, options, validFor: /^\w*$/ };
}

// functionPlace is what BigQuery's functions offer at tok, when it is in a
// .sqlx file's SQL (not in a config { }, js { } or ${ }) and not where a
// table goes: the values of an argument of fixed values, as DATE_TRUNC's
// granularity, or else whether functions go there.
function functionPlace(state, tok) {
  const sql = sqlOf(state);
  if (sql === null || inJavaScript(state, tok.from === tok.to ? tok.from : tok.to)) return null;
  if (inFromList(blank(sql), tok.from)) return null;
  if (tok.text.includes('.')) return /^safe\.\w*$/i.test(tok.text) ? { from: tok.from + 5 } : null;
  const values = valueOptions(sql.slice(0, tok.from));
  return values ? { from: tok.from, values } : { from: tok.from };
}

// beginsClause is whether text begins PIVOT or UNPIVOT, in any case.
const beginsClause = (text) => /^\w*$/.test(text) && ['PIVOT', 'UNPIVOT'].some((c) => c.startsWith(text.toUpperCase()));
const AFTER_REF = new RegExp('\\b(?:from|join)\\s+\\$\\{[^{}]*\\}(?:\\s+(?:as\\s+)?(?!(?:' + [...NOT_ALIAS].join('|') + ')\\b)[A-Za-z_]\\w*)?\\s+$', 'i');

// clauseSpot is whether tok is where PIVOT and UNPIVOT go, after a table in
// the SQL's FROM or JOIN (a ${ref(…)} too), and begins one of them.
function clauseSpot(state, tok) {
  if (!beginsClause(tok.text)) return false;
  const sql = sqlOf(state);
  if (sql === null || inJavaScript(state, tok.from)) return false;
  return afterTable(blank(sql), tok.from) || AFTER_REF.test(state.doc.sliceString(Math.max(0, tok.from - 400), tok.from));
}

// pivotAggregate is whether tok is where PIVOT( takes its aggregate call,
// which is all functionSource offers there.
const pivotAggregate = (state, tok) => !tok.text.includes('.') && inPivotAggregate(sqlOf(state).slice(0, tok.from));

// functionSource offers BigQuery's functions below the columns, as soon as
// they are here: an argument's values alone, as soon as it begins.
async function functionSource(context) {
  const { state, pos } = context;
  if (!SRC.view || isPy()) return null;
  const tok = context.matchBefore(/[A-Za-z_][\w]*(?:\.[\w]*)*$|[A-Za-z_]?$/);
  if (!tok || state.doc.sliceString(tok.from - 1, tok.from) === '$' || inLiteral(state, pos)) return null;
  if (clauseSpot(state, tok)) {
    if (tok.from === tok.to && !context.explicit) return null;
    const clauses = await clauseOptions(9);
    return clauses.length ? { from: tok.from, options: clauses, validFor: beginsClause } : null;
  }
  // The function list first, so an argument's values are known; in PIVOT(
  // the aggregates alone.
  await loadFunctions();
  const at = functionPlace(state, tok);
  if (!at) return null;
  const fns = await functionOptions(1, !at.values && pivotAggregate(state, tok));
  if (at.values) return { from: at.from, options: at.values, validFor: /^[\w()]*$/ };
  if (tok.from === tok.to && !context.explicit) return null;
  return fns.length ? { from: at.from, options: fns, validFor: /^\w*$/ } : null;
}

// ---- errors ----

// sinceSave is what was typed since the text on disk was loaded or saved:
// errors, found in that text, are moved through it.
let sinceSave = null;
let diagSeq = 0;

function resetSinceSave() {
  const ed = sourceEditor();
  sinceSave = ed ? cm.ChangeSet.empty(ed.view.state.doc.length) : null;
}

async function diagnose() {
  const ed = sourceEditor();
  if (!ed || !SRC.view || !SRC.file || isLibraryFile(SRC.file)) return; // a library's file has no compile
  const seq = ++diagSeq;
  const file = SRC.file;
  let res;
  try { res = await get('/api/source/diagnostics?file=' + encodeURIComponent(file)); } catch { return; }
  if (seq !== diagSeq || file !== SRC.file || !SRC.view || res.hash !== SRC.view.hash || !sinceSave) return;
  const lines = SRC.view.text.split('\n');
  const starts = [];
  let at = 0;
  for (const l of lines) { starts.push(at); at += l.length + 1; }
  const doc = ed.view.state.doc;
  const diags = keptMarks(ed, true);
  for (const d of res.diagnostics) {
    if (d.line >= lines.length) continue;
    const a = starts[d.line] + Math.min(d.start, lines[d.line].length);
    const b = starts[d.line] + Math.min(Math.max(d.end, d.start + 1), lines[d.line].length);
    const from = Math.min(sinceSave.mapPos(a, 1), doc.length);
    const to = Math.min(Math.max(sinceSave.mapPos(b, -1), from), doc.length);
    diags.push({ from, to, severity: 'error', message: d.message, source: d.source });
  }
  ed.setMarks(diags);
}

// keptMarks are the marks shown now, where they are now: pyrefly's (py),
// or the others. Setting one kind keeps the other.
function keptMarks(ed, py) {
  const out = [];
  cm.forEachDiagnostic(ed.view.state, (d, from, to) => {
    if ((d.source === 'pyrefly') === py) out.push({ ...d, from, to });
  });
  return out;
}

hooks.sourcePythonMarks = (marks) => {
  const ed = sourceEditor();
  if (ed) ed.setMarks([...keptMarks(ed, false), ...marks]);
};

// ---- defer to prod's labels (ADR 0031) ----

// RefLabel is the label after a ref(): "→ prod", "✓ dev · built 3h ago",
// "⚠ …"; its hover is deferCard, and a click opens the table read in
// BigQuery.
class RefLabel extends cm.WidgetType {
  constructor(h) { super(); this.h = h; }
  eq(o) { return o.h.label === this.h.label && o.h.hover === this.h.hover && o.h.tone === this.h.tone; }
  toDOM() {
    const span = document.createElement('span');
    span.className = 'cm-defer-ref defer-' + this.h.tone;
    span.textContent = this.h.label;
    if (this.h.url) span.onmousedown = (e) => { e.preventDefault(); window.open(this.h.url, '_blank', 'noopener'); };
    return span;
  }
}

// readsProd says the ref reads prod's table in place of dev's.
const readsProd = (h) => !!(h.prod && h.url);

// prodTable is h's prod table as a ref ({project, dataset, table}), for
// its schema; null when its name isn't project.dataset.table.
function prodTable(h) {
  const p = h.prod.split('.');
  return p.length === 3 ? { project: p[0], dataset: p[1], table: p[2] } : null;
}

// deferLines is defer to prod's part of a card: the label, why the ref
// reads what it does, the dev and prod tables, and a link to the one read.
function deferLines(h) {
  const root = document.createElement('div');
  root.className = 'sc-defer';
  const head = document.createElement('div');
  head.className = 'sc-defer-head';
  const label = document.createElement('span');
  label.className = 'defer-' + h.tone;
  label.textContent = h.label;
  const what = document.createElement('span');
  what.className = 'sc-defer-what';
  what.textContent = 'defer to prod';
  head.append(label, what);
  root.append(head);
  const [why, ...rest] = (h.hover || '').split('\n');
  const line = (cls, text) => {
    const d = document.createElement('div');
    d.className = cls;
    d.textContent = text;
    root.append(d);
    return d;
  };
  line('sc-defer-why', why.replace(/^Defer to prod: /, ''));
  // A table's name, with the words marked that differ from the other's,
  // as git-delta shows a changed line: dev's in red, prod's in green.
  const table = (tag, name, read, marks, side) => {
    const d = line('sc-defer-table' + (read ? ' read' : '') + (marks ? ' ' + side : ''), '');
    const t = document.createElement('span');
    t.className = 'sc-defer-tag';
    t.textContent = tag;
    const n = document.createElement('span');
    let at = 0;
    for (const [from, to] of marks || []) {
      const m = document.createElement('span');
      m.className = 'sc-defer-word';
      m.textContent = name.slice(from, to);
      n.append(name.slice(at, from), m);
      at = to;
    }
    n.append(name.slice(at));
    d.append(t, n);
  };
  const prod = readsProd(h);
  const [devMarks, prodMarks] = h.prod && h.prod !== h.dev ? wordMarks(h.dev, h.prod) : [null, null];
  table('dev', h.dev, !prod && h.tone === 'dev', devMarks, 'gone');
  if (h.prod) table('prod', h.prod, prod, prodMarks, 'came');
  for (const l of rest) if (l !== h.dev && !l.startsWith('→ ')) line('sc-card-meta', l);
  if (h.url) {
    const open = document.createElement('button');
    open.className = 'sc-card-link';
    open.textContent = '↗\u00a0open ' + (prod ? 'prod' : 'dev') + ' in bigquery';
    open.onclick = () => window.open(h.url, '_blank', 'noopener');
    line('sc-card-line', '').append(open);
  }
  return root;
}

// deferCard is the card of the label after a ref().
function deferCard(h) {
  const root = document.createElement('div');
  root.className = 'sc-card';
  root.append(deferLines(h));
  return root;
}

// addDefer puts defer to prod's lines under a ref card's first row.
function addDefer(card, h) {
  const d = deferLines(h);
  if (card.firstChild) card.firstChild.after(d); else card.append(d);
  return card;
}

const setRefLabels = cm.StateEffect.define();
const refLabelField = cm.StateField.define({
  create: () => cm.Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) if (e.is(setRefLabels)) deco = e.value;
    return deco;
  },
  provide: (f) => cm.EditorView.decorations.from(f),
});

// labelAt is the label drawn at pos, or null.
function labelAt(state, pos) {
  let h = null;
  const f = state.field(refLabelField, false);
  if (f) f.between(pos, pos, (from, _to, deco) => { if (from === pos && deco.spec.widget instanceof RefLabel) { h = deco.spec.widget.h; return false; } });
  return h;
}

// labelOf is the label of the ref at hit: the one past its ) and its
// closing } or }} on the same line, or null.
function labelOf(state, hit) {
  let h = null;
  const f = state.field(refLabelField, false);
  if (!f || hit.to > state.doc.length) return null;
  const end = state.doc.lineAt(hit.to).to;
  f.between(hit.to, end, (from, _to, deco) => {
    if (deco.spec.widget instanceof RefLabel && /^['"`\s)}]*$/.test(state.sliceDoc(hit.to, from))) { h = deco.spec.widget.h; return false; }
  });
  return h;
}

let labelSeq = 0;
let labelsPending = false;
let labelsOn = false;
const deferShown = () => !!(S.caps.defer && S.state && S.state.defer);

async function refLabels() {
  const ed = sourceEditor();
  if (!ed) return;
  const seq = ++labelSeq;
  const file = SRC.file;
  labelsOn = deferShown();
  if (!labelsOn || !SRC.view || !file || isLibraryFile(file) || isPy()) {
    labelsPending = false;
    ed.view.dispatch({ effects: setRefLabels.of(cm.Decoration.none) });
    return;
  }
  let res;
  try { res = await get('/api/defer/refs?file=' + encodeURIComponent(file)); } catch { return; }
  if (seq !== labelSeq || file !== SRC.file || !SRC.view || !sinceSave) return;
  labelsPending = !!res.pending;
  const lines = SRC.view.text.split('\n');
  const starts = [];
  let at = 0;
  for (const l of lines) { starts.push(at); at += l.length + 1; }
  const doc = ed.view.state.doc;
  const ranges = [];
  for (const l of res.labels || []) {
    if (l.line >= lines.length) continue;
    const pos = Math.min(sinceSave.mapPos(starts[l.line] + Math.min(l.col, lines[l.line].length), 1), doc.length);
    ranges.push(cm.Decoration.widget({ widget: new RefLabel(l), side: 1 }).range(pos));
  }
  ranges.sort((a, b) => a.from - b.from);
  ed.view.dispatch({ effects: setRefLabels.of(cm.Decoration.set(ranges, true)) });
}

// At each detail event: asked again while the labels wait for a Deferral,
// and when defer to prod was turned on or off.
hooks.sourceDetail = () => { if (labelsPending || labelsOn !== deferShown()) refLabels(); };

// ---- document symbols ----

async function sourceSymbols() {
  const file = (SRC.view && SRC.view.file) || (S.view && S.view.file);
  if (!file) return [];
  const ed = sourceEditor();
  const text = ed ? ed.doc() : (SRC.view ? SRC.view.text : "");
  try {
    const res = await post("/api/source/symbols", { file, text });
    return (res && res.symbols) || [];
  } catch {
    return [];
  }
}

function jumpToSymbol(sym) {
  const ed = sourceEditor();
  if (!ed || !sym) return;
  SRC.editing = true;
  setFocus("source");
  ed.focus();
  jumpWithin(ed.view, sym.from);
  redraw();
}

// ---- wiring ----

// The functions are asked for with the editor, so hover and K know them
// before any completion.
hooks.sourceExtensions = () => (loadFunctions(), [
  cm.autocompletion({ override: [pathSource, ...[serverSource, columnSource, functionSource, (c) => (isPy() && hooks.pythonComplete ? hooks.pythonComplete(c) : null)].map(unlessPaths)], activateOnTyping: true, icons: false, maxRenderedOptions: 60, selectOnOpen: true, filterStrict: true }),
  hover,
  cardField,
  refLabelField,
  ...pathExtensions(),
  ...functionExtensions(sqlOf),
  pyExtensions.of([]),
  cm.EditorView.domEventHandlers({
    keydown(e, view) {
      if (e.key === 'Escape' && view.state.field(cardField, false)) view.dispatch({ effects: setCard.of(null) });
      return false;
    },
    // cmd+click (ctrl+click off a Mac) is gd where the click is, as in
    // VS Code, rather than CodeMirror's second cursor.
    mousedown(e, view) {
      if (e.button !== 0 || e.shiftKey || e.altKey || !(onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey)) return false;
      const pos = view.posAtCoords({ x: e.clientX, y: e.clientY });
      if (pos === null) return false;
      e.preventDefault();
      view.dispatch({ selection: { anchor: pos } });
      view.focus();
      goToDefinition(view);
      return true;
    },
  }),
]);
hooks.sourceHover = (view) => { cardAtCursor(view); };
hooks.sourceDefinition = (view) => { goToDefinition(view); };
hooks.sourceUpdate = (u) => { if (u.docChanged && sinceSave) sinceSave = sinceSave.compose(u.changes); };
hooks.sourceLoaded = () => {
  const ed = sourceEditor();
  if (ed) ed.setMarks([]);
  resetSinceSave();
  diagnose();
  refLabels();
  pythonShown(SRC.view);
};
hooks.sourceSaved = () => { resetSinceSave(); };
hooks.sourceDiagnose = () => { diagnose(); refLabels(); };
hooks.sourceSymbols = sourceSymbols;
hooks.jumpToSymbol = jumpToSymbol;
