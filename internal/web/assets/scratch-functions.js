// BigQuery's functions in the Scratch editor: their completion as snippets,
// the values an argument takes, the card the mouse and K show of one, and
// the signature help above a call while its arguments are typed.
//
// The server lists them (internal/bqfuncs): vscode-dataform-tools' catalog,
// each function's typed arguments, and the snippets it borrowed for the rest.
// The call and argument the cursor is in are read as the extension reads
// them (its src/bigquery/functionCall.ts).
//
// The source pane uses them too, in a .sqlx file's SQL: what takes a textOf
// reads the editor's text through it, as sqlx.js's sqlText with the
// JavaScript blanked, and does nothing when it gives null.

import * as cm from './vendor/codemirror.mjs';
import { get } from './state.js';

// F is the functions once they are here: { list, byName }.
let F = null;
let loading = null;

// loadFunctions asks the server for the functions, once.
export function loadFunctions() {
  if (!loading) {
    loading = get('/api/scratch/functions').then((list) => {
      F = { list, byName: new Map(list.map((f) => [f.name, f])) };
      return F;
    }).catch(() => { loading = null; return null; });
  }
  return loading;
}

// functionNamed is the function of a name as written, in any case and with
// or without SAFE. before it.
export function functionNamed(name) {
  if (!F) return undefined;
  const upper = name.toUpperCase();
  return F.byName.get(upper) || (upper.startsWith('SAFE.') ? F.byName.get(upper.slice(5)) : undefined);
}

export const isFunctionName = (word) => !!F && F.byName.has(word.toUpperCase());

// ---- the call the cursor is in ----

// scan is each bracket open at the end of text, with the commas and words
// written at its own level, and whether the end is in code rather than in a
// string or a comment.
function scan(text) {
  const frames = [];
  let i = 0;
  const skipTo = (end, from) => {
    const at = text.indexOf(end, from);
    i = at < 0 ? text.length : at + end.length;
    return at >= 0;
  };
  while (i < text.length) {
    const c = text[i];
    const two = text.slice(i, i + 2);
    if (two === '--' || c === '#') {
      if (!skipTo('\n', i)) return { frames, inCode: false };
      continue;
    }
    if (two === '/*') {
      if (!skipTo('*/', i + 2)) return { frames, inCode: false };
      continue;
    }
    if (c === "'" || c === '"' || c === '`') {
      const quote = c !== '`' && text.startsWith(c.repeat(3), i) ? c.repeat(3) : c;
      let j = i + quote.length;
      while (j < text.length && !text.startsWith(quote, j)) j += text[j] === '\\' ? 2 : 1;
      if (j >= text.length) return { frames, inCode: false };
      i = j + quote.length;
      continue;
    }
    const top = frames[frames.length - 1];
    if (c === '(') frames.push({ name: nameBefore(text, i), separators: [] });
    else if (c === '[') frames.push({ name: '', separators: [] });
    else if (c === ')' || c === ']') frames.pop();
    else if (c === ',') { if (top) top.separators.push(','); }
    else if (/[A-Za-z_]/.test(c) && !/[\w.]/.test(text[i - 1] || '')) {
      const word = /^\w+/.exec(text.slice(i, i + 200))[0];
      if (top) top.separators.push(word.toUpperCase());
      i += word.length;
      continue;
    } else if (/\d/.test(c)) {
      i += /^[\w.]+/.exec(text.slice(i, i + 200))[0].length;
      continue;
    }
    i++;
  }
  return { frames, inCode: true };
}

// nameBefore is the name a bracket at `at` follows: a word, a dotted name,
// or '?' for a quoted one, which names no function.
function nameBefore(text, at) {
  let end = at;
  while (end > 0 && /\s/.test(text[end - 1])) end--;
  if (text[end - 1] === '`') return '?';
  let start = end;
  while (start > 0 && /[\w.]/.test(text[start - 1])) start--;
  const name = text.slice(start, end).replace(/^\.+/, '');
  // UNPIVOT INCLUDE NULLS ( is UNPIVOT's bracket.
  if (/^nulls$/i.test(name) && /\bunpivot\s+(?:include|exclude)\s+$/i.test(text.slice(Math.max(0, start - 40), start))) return 'UNPIVOT';
  return /^[A-Za-z_]/.test(name) ? name : '';
}

// callAt is the call the end of text is in: the innermost named bracket,
// looking through those that only group, as an IN list does (PIVOT's too).
function callAt(text) {
  const { frames, inCode } = scan(text);
  if (!inCode) return null;
  for (let k = frames.length - 1; k >= 0; k--) if (frames[k].name && !/^in$/i.test(frames[k].name)) return frames[k];
  return null;
}

const separatorOf = (p) => (p.before ? p.before.split(' ')[0].toUpperCase() : ',');

// activeParameter is which param of a signature the cursor is at, and
// whether the call so far fits the signature at all.
function activeParameter(params, separators) {
  let index = 0;
  let fits = true;
  for (const sep of separators) {
    // The next param this separator begins, past optional ones left out,
    // as ARRAY_AGG(x ORDER BY y) leaves out IGNORE NULLS.
    let k = index + 1;
    while (params[k] && params[k].optional && sep !== separatorOf(params[k])) k++;
    if (params[k] && sep === separatorOf(params[k])) index = k;
    else if (sep === ',' && !(params[index] && params[index].repeated)) fits = false;
  }
  return { index, fits };
}

// callInfo is the catalogued call text ends in: the function, the
// signature the call fits first, and the param the cursor is at.
function callInfo(text) {
  const call = callAt(text);
  const fn = call && functionNamed(call.name);
  if (!fn || !fn.signatures) return null;
  const actives = fn.signatures.map((s) => activeParameter(s.params, call.separators));
  const sig = Math.max(actives.findIndex((a) => a.fits), 0);
  return { fn, actives, sig, param: fn.signatures[sig].params[actives[sig].index] };
}

// functionNameAt is the function name at a character of a line, when a (
// follows it: a column called `date` is no call.
function functionNameAt(line, ch) {
  let start = ch, end = ch;
  while (start > 0 && /[\w.]/.test(line[start - 1])) start--;
  while (end < line.length && /[\w.]/.test(line[end])) end++;
  const name = line.slice(start, end);
  if (!/^[A-Za-z_][\w.]*$/.test(name) || !/^\s*\(/.test(line.slice(end))) return null;
  return { name, start, end };
}

const wholeText = (state) => state.doc.toString();

// functionAt is the function a call names at pos, and where its name is.
export function functionAt(state, pos, textOf = wholeText) {
  const text = textOf(state);
  if (text === null) return null;
  const line = state.doc.lineAt(pos);
  const at = functionNameAt(text.slice(line.from, line.to), pos - line.from);
  if (!at) return null;
  const from = line.from + at.start;
  if (!scan(text.slice(0, from)).inCode) return null;
  const fn = functionNamed(at.name);
  return fn ? { fn, from, to: line.from + at.end } : null;
}

// ---- completion ----

// shortArgs is a function's first signature as its names only, beside its
// completion: `(date_value, granularity)`.
function shortArgs(f) {
  if (!f.signatures) return f.detail.slice(f.name.length);
  let s = '(';
  f.signatures[0].params.forEach((p, i) => {
    const sep = i === 0 ? '' : p.before ? ` ${p.before} ` : ', ';
    s += p.optional ? (sep.startsWith(' ') ? ` [${sep.trimStart()}${p.name}]` : `[${sep}${p.name}]`) : sep + p.name;
  });
  return s + ')';
}

const asOption = (f, boost) => cm.snippetCompletion(f.snippet, {
  label: f.name, detail: shortArgs(f), type: 'function', boost, info: () => functionCard(f, true),
});

export const isAggregate = (f) => /aggregate_functions/.test(f.url);

// functionOptions are every function, each inserted as its snippet, or
// the aggregates alone. PIVOT and UNPIVOT are no function: clauseOptions
// has them.
export async function functionOptions(boost, aggregates = false) {
  if (!(await loadFunctions())) return [];
  return F.list.filter((f) => !f.clause && (!aggregates || isAggregate(f))).map((f) => asOption(f, boost));
}

// clauseOptions are PIVOT and UNPIVOT, which go after a table in FROM.
export async function clauseOptions(boost) {
  if (!(await loadFunctions())) return [];
  return F.list.filter((f) => f.clause).map((f) => ({ ...asOption(f, boost), type: 'keyword' }));
}

// inPivotAggregate is whether the end of text is where PIVOT( takes its
// aggregate: in the bracket itself, before FOR, not in a call within it.
export function inPivotAggregate(text) {
  const info = callInfo(text);
  return !!info && info.fn.name === 'PIVOT' && info.actives[info.sig].index === 0;
}

// valueOptions are the values the argument the cursor is at takes, as
// DATE_TRUNC's granularity: text is the SQL before the word being typed.
export function valueOptions(text) {
  const info = callInfo(text);
  const p = info && info.param;
  if (!p || !p.values) return null;
  return p.values.map((v, i) => ({ label: v, detail: p.name, type: 'enum', boost: 50 - i }));
}

// ---- the card ----

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// rich is text with its `code` and its links made so.
function rich(tag, cls, text) {
  const e = el(tag, cls);
  for (const part of text.split(/(`[^`\n]+`|https?:\/\/[^\s)>\]]+)/)) {
    if (!part) continue;
    if (part.startsWith('`')) e.append(el('code', '', part.slice(1, -1)));
    else if (/^https?:/.test(part)) {
      const a = el('a', 'sc-card-link', part);
      a.href = part; a.target = '_blank'; a.rel = 'noopener';
      e.append(a);
    } else e.append(document.createTextNode(part));
  }
  return e;
}

// functionCard is what a function is: its signatures, what it does, its
// params, notes, an example and its docs. compact is the narrower one
// beside a completion.
export function functionCard(f, compact) {
  const root = el('div', 'sc-card sc-fn' + (compact ? ' sc-fn-compact' : ''));
  root.append(el('div', 'sc-card-title', f.name));
  const code = el('pre', 'sc-fn-sig');
  code.textContent = f.signatures ? f.signatures.map((s) => s.label).join('\n') : (f.body || f.detail);
  root.append(code);
  if (f.doc) root.append(rich('div', 'sc-card-line', f.doc));
  if (f.signatures) {
    const seen = new Set();
    const list = el('div', 'sc-fn-params');
    for (const p of f.signatures.flatMap((s) => s.params)) {
      if (seen.has(p.name) || !p.text) continue;
      seen.add(p.name);
      const row = el('div', 'sc-fn-param');
      row.append(el('code', '', p.name), document.createTextNode(' — '), rich('span', '', p.text));
      list.append(row);
    }
    if (list.childNodes.length) root.append(list);
  }
  for (const n of f.notes || []) root.append(rich('div', 'sc-card-line', n));
  if (f.example) {
    const ex = el('div', 'sc-card-line');
    ex.append(el('b', '', 'Example: '), el('code', '', f.example.sql), document.createTextNode(' → ' + f.example.result));
    root.append(ex);
  }
  const a = el('a', 'sc-card-link', f.searched ? 'Search the BigQuery docs ↗' : 'BigQuery docs ↗');
  a.href = f.url; a.target = '_blank'; a.rel = 'noopener';
  const link = el('div', 'sc-card-line');
  link.append(a);
  root.append(link);
  return root;
}

// ---- signature help ----

// The signatures of the call the cursor is in, above it, with the argument
// it is at in bold: on once a (, a comma or a space is typed in a call, or a
// function completed; off when the cursor leaves every call, or on esc.
const setSig = cm.StateEffect.define();

// sigField is the signature help reading the text through textOf.
function sigField(textOf) {
  const sigInfo = (state) => {
    const text = textOf(state);
    return text === null ? null : callInfo(text.slice(0, state.selection.main.head));
  };
  return cm.StateField.define({
    create: () => false,
    update(on, tr) {
      for (const e of tr.effects) if (e.is(setSig)) return e.value;
      if (tr.docChanged && tr.isUserEvent('input')) {
        let typed = '';
        tr.changes.iterChanges((_a, _b, _c, _d, ins) => { typed += ins.toString(); });
        if (/[(, ]/.test(typed) || tr.isUserEvent('input.complete')) on = true;
      }
      if (on && (tr.docChanged || tr.selection)) on = !!sigInfo(tr.state);
      return on;
    },
    provide: (f) => cm.showTooltip.compute([f, 'doc', 'selection'], (state) => {
      if (!state.field(f)) return null;
      const info = sigInfo(state);
      return info ? { pos: state.selection.main.head, above: true, create: () => ({ dom: sigDOM(info) }) } : null;
    }),
  });
}

function sigDOM({ fn, actives, sig, param }) {
  const root = el('div', 'sc-sig');
  fn.signatures.forEach((s, k) => {
    const line = el('div', 'sc-sig-line' + (k === sig ? '' : ' sc-sig-other'));
    const p = k === sig ? s.params[actives[k].index] : null;
    if (p) {
      // Span counts UTF-16 units, as JavaScript does.
      line.append(document.createTextNode(s.label.slice(0, p.span[0])), el('b', '', s.label.slice(p.span[0], p.span[1])), document.createTextNode(s.label.slice(p.span[1])));
    } else line.textContent = s.label;
    root.append(line);
  });
  if (param && param.text) {
    const doc = el('div', 'sc-sig-doc');
    doc.append(el('code', '', param.name), document.createTextNode(' — '), rich('span', '', param.text));
    root.append(doc);
  }
  return root;
}

// functionExtensions are the signature help, and the values offered when a
// snippet's field of fixed values is reached, as DATE_TRUNC's DAY.
export function functionExtensions(textOf = wholeText) {
  const sigOn = sigField(textOf);
  return [
    sigOn,
    cm.EditorView.domEventHandlers({
      keydown(e, view) {
        if (e.key === 'Escape' && view.state.field(sigOn, false)) view.dispatch({ effects: setSig.of(false) });
        return false;
      },
    }),
    cm.EditorView.updateListener.of((u) => {
      if (!u.selectionSet) return;
      const st = u.state;
      const sel = st.selection.main;
      if (sel.empty) return;
      // Reaching a snippet's last field ends it, so the state before counts.
      const fields = (s) => cm.hasNextSnippetField(s) || cm.hasPrevSnippetField(s);
      const inSnippet = fields(st) || fields(u.startState) || u.transactions.some((t) => t.isUserEvent('input.complete'));
      if (!inSnippet) return;
      const text = textOf(st);
      const opts = text !== null && valueOptions(text.slice(0, sel.from));
      if (opts && opts.some((o) => o.label === st.sliceDoc(sel.from, sel.to))) setTimeout(() => cm.startCompletion(u.view), 0);
    }),
  ];
}
