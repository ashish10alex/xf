// Table and column completion and table hover for the Scratch editor.
//
// Tables come first from the project's own models, then from BigQuery as
// the name is typed (`project.` lists datasets, `dataset.` lists tables);
// columns come from the tables the query reads, alias-aware, and with them
// BigQuery's functions (see scratch-functions.js) and every other word in
// the editor, such as a CTE's columns. The server
// caches what BigQuery says (see internal/web/scratchmeta.go); this module
// keeps what it has been told for the same few minutes, so typing never
// waits twice for the same answer.

import * as cm from './vendor/codemirror.mjs';
import { get, copy, openLink } from './state.js';
import { SC, fmtBytes, onClearMeta } from './scratch.js';
import { functionOptions, clauseOptions, inPivotAggregate, valueOptions, isFunctionName, functionAt, functionCard, functionExtensions } from './scratch-functions.js';

const TTL = 5 * 60 * 1000;
const cache = new Map();

// cached is the answer to GET url, asked once per TTL; a failed call is
// not kept, and answers null.
function cached(url) {
  const hit = cache.get(url);
  if (hit && Date.now() - hit.at < TTL) return hit.p;
  const p = get(url).catch(() => { cache.delete(url); return null; });
  cache.set(url, { at: Date.now(), p });
  return p;
}

export function clearMetaCache() { cache.clear(); }
onClearMeta(clearMetaCache);

const q = (o) => new URLSearchParams(o).toString();
const models = () => cached('/api/scratch/meta/models');
const datasets = (project) => cached('/api/scratch/meta/datasets?' + q({ project }));
const tables = (project, dataset) => cached('/api/scratch/meta/tables?' + q({ project, dataset }));
// tableCard is a table's card. Completion reads it on every keystroke and
// takes the kept one; a hover asks fresh, so the last modified time is
// today's, and what it read is kept for completion.
export function tableCard(r, fresh = false) {
  const url = '/api/scratch/meta/table?' + q({ project: r.project, dataset: r.dataset, table: r.table });
  if (!fresh) return cached(url);
  const p = get(url + '&fresh=1').catch(() => { cache.delete(url); return null; });
  cache.set(url, { at: Date.now(), p });
  return p;
}

const defaultProject = () => (SC.cur && SC.cur.project) || (SC.defaults && SC.defaults.project) || '';

// ---- reading the SQL ----

// blank returns sql with its comments and string literals replaced by
// spaces, as long as they were, so offsets are the same and a keyword in a
// string is no keyword. Backticked names stay.
export function blank(sql) {
  const out = sql.split('');
  const n = sql.length;
  const wipe = (a, b) => { for (let i = a; i < b && i < n; i++) if (out[i] !== '\n') out[i] = ' '; };
  for (let i = 0; i < n;) {
    const c = sql[i], d = sql[i + 1];
    if ((c === '-' && d === '-') || c === '#') {
      let j = i; while (j < n && sql[j] !== '\n') j++;
      wipe(i, j); i = j;
    } else if (c === '/' && d === '*') {
      let j = sql.indexOf('*/', i + 2); j = j < 0 ? n : j + 2;
      wipe(i, j); i = j;
    } else if (c === '`') {
      let j = i + 1; while (j < n && sql[j] !== '`' && sql[j] !== '\n') j++;
      i = j + 1;
    } else if (c === "'" || c === '"') {
      const triple = sql.startsWith(c.repeat(3), i);
      let j = i + (triple ? 3 : 1);
      while (j < n) {
        if (sql[j] === '\\') { j += 2; continue; }
        if (triple ? sql.startsWith(c.repeat(3), j) : sql[j] === c) { j += triple ? 3 : 1; break; }
        if (!triple && sql[j] === '\n') break;
        j++;
      }
      wipe(i, j); i = j;
    } else i++;
  }
  return out.join('');
}

export const NOT_ALIAS = new Set('where on join left right inner outer cross full natural lateral group order limit union intersect except using window qualify having tablesample pivot unpivot for set select from partition when then else end and or not in is as'.split(' '));
const REF = '(`[^`\\n]+`|[A-Za-z_][\\w-]*(?:\\.[A-Za-z_0-9][\\w-]*){0,2})(?!\\s*\\()';
// An alias is never a keyword, so FROM a JOIN b leaves JOIN b to be read.
const ALIAS = '(?:\\s+(?:as\\s+)?(?!(?:' + [...NOT_ALIAS].join('|') + ')\\b)([A-Za-z_]\\w*))?';
// TABLE_AT is the words a table's name comes after: FROM and JOIN, and the
// statements that name a table that exists (DROP TABLE, UPDATE, MERGE INTO
// … USING, …). A MERGE's THEN INSERT, UPDATE or DELETE names none.
const TABLE_AT = '(?:\\b(?:from|join|using|clone|copy)' +
  '|\\bdrop\\s+(?:(?:materialized|external|snapshot)\\s+)?(?:table|view)(?:\\s+if\\s+exists)?' +
  '|\\balter\\s+(?:materialized\\s+)?(?:table|view)(?:\\s+if\\s+exists)?' +
  '|\\btruncate\\s+table' +
  '|(?<!\\bthen\\s+)\\b(?:(?:insert|merge)(?:\\s+into)?|delete(?:\\s+from)?|update))\\s+';
const FROM = new RegExp(TABLE_AT + REF + ALIAS, 'gi');
const TABLE_SPOT = new RegExp(TABLE_AT + '$', 'i');
const MORE = new RegExp('\\s*,\\s*' + REF + ALIAS, 'y');

const unquote = (s) => s.replace(/`/g, '');

// parseRefs lists the tables a query reads: where its name is in the SQL,
// the name split into its parts, and the alias it is given.
export function parseRefs(sql) {
  const clean = blank(sql);
  const refs = [];
  const add = (m, from, k) => {
    // m[k] is the name; its offset is where it was matched from.
    const text = m[k];
    const start = clean.indexOf(text, from);
    let alias = m[k + 1] || '';
    if (NOT_ALIAS.has(alias.toLowerCase())) alias = '';
    refs.push({ start, end: start + text.length, text, parts: unquote(text).split('.'), alias });
    return m.index + m[0].length;
  };
  FROM.lastIndex = 0;
  let m;
  while ((m = FROM.exec(clean))) {
    let end = add(m, m.index, 1);
    for (;;) { // FROM a, b, c
      MORE.lastIndex = end;
      const more = MORE.exec(clean);
      if (!more || NOT_ALIAS.has(unquote(more[1]).toLowerCase())) break;
      const start = clean.indexOf(more[1], end);
      let alias = more[2] || '';
      if (NOT_ALIAS.has(alias.toLowerCase())) alias = '';
      refs.push({ start, end: start + more[1].length, text: more[1], parts: unquote(more[1]).split('.'), alias });
      end = more.index + more[0].length;
    }
    FROM.lastIndex = Math.max(FROM.lastIndex, end);
  }
  return refs;
}

// resolve is the table a reference names, or null: a name of one part is a
// CTE or a table that needs a project and dataset.
export function resolve(ref) {
  const p = ref.parts;
  if (p.length === 3) return { project: p[0], dataset: p[1], table: p[2] };
  if (p.length === 2) return { project: defaultProject(), dataset: p[0], table: p[1] };
  return null;
}

// cteNames are the names the WITH clauses of sql give their CTEs.
function cteNames(clean) {
  const out = [];
  for (const m of clean.matchAll(/(?:\bwith(?:\s+recursive)?|,)\s*(`[^`\n]+`|[A-Za-z_]\w*)\s+as\s*\(/gi)) {
    const n = unquote(m[1]);
    if (!out.includes(n)) out.push(n);
  }
  return out;
}

// KEYWORDS are left out of the editor's words.
const KEYWORDS = new Set([...NOT_ALIAS, ...'with recursive distinct by asc desc null true false case if all any exists between like rows range over current row unbounded preceding following interval struct array cast safe_cast extract date datetime time timestamp int64 float64 numeric bignumeric bool string bytes json geography insert into values update delete merge create replace table view temp temporary declare begin commit rollback'.split(' ')]);

// words are the names written anywhere in the editor but the one being
// typed at [from, to): backticked names, keywords and functions left out.
export function words(sql, from, to) {
  const text = sql.slice(0, from) + ' '.repeat(to - from) + sql.slice(to);
  const out = new Map();
  for (const m of text.replace(/`[^`\n]*`?/g, (x) => ' '.repeat(x.length)).matchAll(/[A-Za-z_][A-Za-z0-9_]*/g)) {
    const w = m[0];
    if (w.length < 2 || KEYWORDS.has(w.toLowerCase()) || out.has(w.toLowerCase()) || isFunctionName(w)) continue;
    out.set(w.toLowerCase(), w);
  }
  return [...out.values()];
}

// ---- completion ----

const PLAIN = /^[A-Za-z_][A-Za-z0-9_]*$/;
export const quoteIdent = (s) => (PLAIN.test(s) ? s : '`' + s.replace(/`/g, '\\`') + '`');
const refText = (r) => '`' + r.project + '.' + r.dataset + '.' + r.table + '`';

// field walks path down the fields of a table's schema.
function walk(fields, path) {
  let cur = fields;
  for (const name of path) {
    const f = (cur || []).find((x) => x.name.toLowerCase() === name.toLowerCase());
    if (!f || !f.fields) return null;
    cur = f.fields;
  }
  return cur;
}

// columnsOf are a card's columns: BigQuery's, else those the model's config
// lists, which have no types.
export function columnsOf(card) {
  if (!card) return [];
  const doc = {};
  for (const c of (card.model && card.model.columns) || []) doc[c.name] = c.description;
  if (card.detail && card.detail.schema && card.detail.schema.length) {
    return card.detail.schema.map((f) => ({ ...f, description: f.description || doc[f.name] || '' }));
  }
  return ((card.model && card.model.columns) || []).map((c) => ({ name: c.name, type: '', description: c.description }));
}

export const typeText = (f) => (f.mode === 'REPEATED' ? 'ARRAY<' : '') + (f.type === 'RECORD' ? 'STRUCT' : (f.type || '')) + (f.mode === 'REPEATED' ? '>' : '');

// mergeColumns is one completion per column name, in any case, of tables:
// each table once, in the order the query reads them, as { name, aliases,
// owner, cols }. A name keeps its first table's spelling; one in several
// tables says how many and their types, and its info lists them. owner is
// what the detail calls a column in one table, when showOwner.
export function mergeColumns(tables, showOwner) {
  const byName = new Map();
  for (const t of tables) {
    for (const f of t.cols) {
      const key = f.name.toLowerCase();
      const m = byName.get(key) || { f, from: [] };
      if (!m.from.some((x) => x.t === t)) m.from.push({ t, f });
      byName.set(key, m);
    }
  }
  return [...byName.values()].map(({ f, from }) => {
    const types = [...new Set(from.map((x) => typeText(x.f)).filter(Boolean))].join('/');
    const where = from.length > 1 ? from.length + ' tables' : showOwner ? from[0].t.owner : '';
    const desc = (from.find((x) => x.f.description) || {}).f;
    let info = desc ? desc.description : '';
    if (from.length > 1 || from[0].t.aliases.length > 1) {
      const names = from.map((x) => x.t.name + (x.t.aliases.length ? ' (' + x.t.aliases.join(', ') + ')' : ''));
      const w = Math.max(...names.map((n) => n.length));
      info = from.map((x, i) => (names[i].padEnd(w) + '  ' + typeText(x.f)).trimEnd()).join('\n') + (info ? '\n\n' + info : '');
    }
    return {
      label: f.name, detail: [types, where].filter(Boolean).join(' · '), type: f.type === 'RECORD' ? 'namespace' : 'property',
      info: info || undefined, boost: 2,
    };
  });
}

// replaceFrom makes an apply that puts text over the name being typed,
// from where it began (backtick included) to the cursor.
function applyAt(start, text, then) {
  return (view, _completion, _from, to) => {
    // A closing backtick the cursor stands before goes too.
    const end = view.state.doc.sliceString(to, to + 1) === '`' ? to + 1 : to;
    view.dispatch({ changes: { from: start, to: end, insert: text }, selection: { anchor: start + text.length } });
    if (then) setTimeout(() => cm.startCompletion(view), 0);
  };
}

async function tableOptions(tok, tokStart, clean) {
  const raw = unquote(tok);
  const parts = raw.split('.');
  const ticked = tok.startsWith('`');
  const lead = ticked ? '`' : '';
  const proj = defaultProject();
  const options = [];
  const from = tokStart + (ticked ? 1 : 0) + raw.length - parts[parts.length - 1].length;
  const addTables = async (project, dataset) => {
    const ts = await tables(project, dataset);
    for (const t of ts || []) {
      options.push({ label: t.id, detail: t.type.toLowerCase().replace('_', ' '), type: 'class', boost: 1,
        apply: applyAt(tokStart, refText({ project, dataset, table: t.id })) });
    }
    return !!ts;
  };
  if (parts.length === 1) {
    for (const n of cteNames(clean)) options.push({ label: n, detail: 'cte', type: 'class', boost: 5, apply: applyAt(tokStart, quoteIdent(n)) });
    for (const m of (await models()) || []) {
      options.push({ label: m.dataset + '.' + m.name, detail: m.kind, type: 'class', boost: 3, info: m.description || undefined,
        apply: applyAt(tokStart, refText({ project: m.project, dataset: m.dataset, table: m.name })) });
    }
    for (const d of (proj && (await datasets(proj))) || []) {
      options.push({ label: d, detail: 'dataset', type: 'namespace', boost: 1, apply: applyAt(tokStart, lead + d + '.', true) });
    }
    return { from, options };
  }
  if (parts.length === 2) {
    const ds = (proj && (await datasets(proj))) || [];
    if (ds.includes(parts[0])) {
      await addTables(proj, parts[0]);
    } else { // the first part is a project
      for (const d of (await datasets(parts[0])) || []) {
        options.push({ label: d, detail: 'dataset', type: 'namespace', apply: applyAt(tokStart, lead + parts[0] + '.' + d + '.', true) });
      }
    }
    return { from, options };
  }
  if (parts.length === 3) await addTables(parts[0], parts[1]);
  return { from, options };
}

// partStart is where typed begins label or a part of it after `.`, `_` or
// `-`, any case, or -1: HOL finds 050_HOLIDAYS, as a prefix alone
// never would.
export function partStart(label, typed) {
  const l = label.toLowerCase(), t = typed.toLowerCase();
  if (l.startsWith(t)) return 0;
  for (const m of l.matchAll(/[._-]+/g)) {
    const i = m.index + m[0].length;
    if (i < l.length && l.startsWith(t, i)) return i;
  }
  return -1;
}

// partMatch is how typed matches label, or null: rank 0 when it begins the
// label, 1 when it begins a part of it, 2 when each of its words (split at
// `.`, `_` or `-`) begins a part, in order, so 0300_ROI finds
// 0300_PROMOTIONS_DATA_ROI. ranges are the [from, to, …] it covers.
export function partMatch(label, typed) {
  const at = partStart(label, typed);
  if (at >= 0) return { rank: at > 0 ? 1 : 0, ranges: typed ? [at, at + typed.length] : [] };
  const ws = typed.toLowerCase().split(/[._-]+/).filter(Boolean);
  if (ws.length < 2) return null;
  const l = label.toLowerCase();
  const starts = [0, ...[...l.matchAll(/[._-]+/g)].map((m) => m.index + m[0].length)];
  const ranges = [];
  let k = 0;
  for (const w of ws) {
    while (k < starts.length && !l.startsWith(w, starts[k])) k++;
    if (k === starts.length) return null;
    ranges.push(starts[k], starts[k] + w.length);
    k++;
  }
  return { rank: 2, ranges };
}

// byParts keeps the options typed matches (see partMatch), the best
// matches first, then by boost, as a table name is often known by a word in
// its middle or a few of its words.
export function byParts(options, typed) {
  return options
    .map((o, i) => ({ o, i, m: partMatch(o.label, typed) }))
    .filter((x) => x.m)
    .sort((a, b) => a.m.rank - b.m.rank || (b.o.boost || 0) - (a.o.boost || 0) || a.i - b.i)
    .map((x) => ({ ...x.o, ranges: x.m.ranges }));
}

// inFromList is whether a table is what the text before start asks for:
// after FROM, JOIN or another TABLE_AT word, or after a comma in a FROM list.
export function inFromList(clean, start) {
  const before = clean.slice(0, start);
  if (TABLE_SPOT.test(before)) return true;
  if (!/,\s*$/.test(before)) return false;
  const kw = [...before.matchAll(/\b(from|join|where|select|on|group\s+by|order\s+by|having|qualify|window|limit)\b/gi)].pop();
  return !!kw && /^(from|join)$/i.test(kw[1]);
}

// AFTER_TABLE is the end of a table in FROM or JOIN (or a list of them),
// with its alias: where PIVOT and UNPIVOT go.
const NOT_KEYWORD = '(?!(?:' + [...NOT_ALIAS].join('|') + ')\\b)';
const AFTER_TABLE = new RegExp('\\b(?:from|join)\\s+' + NOT_KEYWORD + REF + ALIAS + '(?:\\s*,\\s*' + REF + ALIAS + ')*\\s+$', 'i');
const TRAILING_ALIAS = new RegExp('(?:\\s+(?:as\\s+)?(?!(?:' + [...NOT_ALIAS].join('|') + ')\\b)[A-Za-z_]\\w*)?\\s+$', 'i');

// afterTable is whether the text before start ends with a table in FROM or
// JOIN, a subquery or UNNEST(…) there, and its alias: where PIVOT and
// UNPIVOT go, as in FROM t PIVOT(…) or FROM (SELECT …) AS s UNPIVOT(…).
export function afterTable(clean, start) {
  const before = clean.slice(Math.max(0, start - 400), start);
  if (AFTER_TABLE.test(before)) return true;
  // A bracket closed after FROM or JOIN.
  const m = TRAILING_ALIAS.exec(clean.slice(0, start));
  let i = (m ? m.index : start) - 1;
  if (clean[i] !== ')') return false;
  for (let depth = 0; i >= 0; i--) {
    if (clean[i] === ')') depth++;
    else if (clean[i] === '(' && --depth === 0) break;
  }
  return i > 0 && /\b(?:from|join)\s*(?:unnest\s*)?$/i.test(clean.slice(Math.max(0, i - 40), i));
}

// madeColumns are the columns the PIVOTs and UNPIVOTs in clean[from, to)
// make, as { name, by }: sql gives the strings' text, which clean blanks.
// A PIVOT names a column for each value as BigQuery does: by its alias or
// the value itself ('Q1' gives Q1, 1 gives _1, -1 minus_1, DATE
// '2024-01-31' _2024_01_31), after the aggregate's prefix and _ when it has
// one; a value of any other kind with no alias names none. An UNPIVOT makes
// its values and name columns.
export function madeColumns(sql, clean, from = 0, to = clean.length) {
  const out = [];
  const add = (name, by) => { if (name && !out.some((c) => c.name === name)) out.push({ name, by }); };
  const re = /\b(un)?pivot(?:\s+(?:include|exclude)\s+nulls)?\s*\(/gi;
  re.lastIndex = from;
  for (let m; (m = re.exec(clean)) && m.index < to;) {
    const open = m.index + m[0].length - 1;
    const close = closeOf(clean, open);
    const forAt = close < 0 ? -1 : keywordAt(clean, open + 1, close, 'for');
    const inAt = forAt < 0 ? -1 : keywordAt(clean, forAt, close, 'in');
    if (inAt < 0) continue;
    if (m[1]) {
      for (const n of clean.slice(open + 1, forAt).match(/[A-Za-z_]\w*/g) || []) add(n, 'unpivot');
      add((/^\s*for\s+([A-Za-z_]\w*)\s*$/i.exec(clean.slice(forAt, inAt)) || [])[1], 'unpivot');
      continue;
    }
    const list = clean.indexOf('(', inAt);
    const listEnd = list < 0 ? -1 : closeOf(clean, list);
    if (listEnd < 0 || listEnd > close) continue;
    const prefixes = splitTop(clean, open + 1, forAt).filter(([a, b]) => clean.slice(a, b).trim()).map(([a, b]) => {
      const p = /(?:\bas\s+|\)\s*)([A-Za-z_]\w*)\s*$/i.exec(clean.slice(a, b));
      return p ? p[1] : '';
    });
    for (const [a, b] of splitTop(clean, list + 1, listEnd)) {
      const name = pivotName(sql.slice(a, b), clean.slice(a, b));
      for (const p of prefixes) if (name) add(p ? p + '_' + name : name, 'pivot');
    }
  }
  return out;
}

// pivotName is the column a value of a PIVOT's IN list names, or ''.
function pivotName(raw, clean) {
  const t = raw.trim();
  const alias = (rest) => (/^\s*(?:as\s+)?([A-Za-z_]\w*)\s*$/i.exec(rest) || [])[1];
  const two = (n) => n.padStart(2, '0');
  let m, lit = null;
  if ((m = /^(['"])((?:\\.|(?!\1)[^\\])*)\1/.exec(t))) lit = m[2];
  else if ((m = /^date\s*(['"])(\d{4})-(\d{1,2})-(\d{1,2})\1/i.exec(t))) lit = '_' + m[2] + '_' + two(m[3]) + '_' + two(m[4]);
  else if ((m = /^(-)?\s*(\d+)(?![\w.])/.exec(t))) lit = (m[1] ? 'minus_' : '_') + m[2];
  else if ((m = /^(true|false|null)\b/i.exec(t))) lit = m[1].toUpperCase();
  if (lit === null) return (/\bas\s+([A-Za-z_]\w*)\s*$/i.exec(clean) || [])[1] || '';
  const rest = t.slice(m[0].length);
  return rest.trim() ? alias(rest) || '' : lit;
}

// keywordAt is where the word kw is in clean[a, b), at the level of a, or -1.
function keywordAt(clean, a, b, kw) {
  const re = new RegExp('\\b' + kw + '\\b', 'iy');
  for (let i = a, depth = 0; i < b; i++) {
    const c = clean[i];
    if (c === '(' || c === '[') depth++;
    else if (c === ')' || c === ']') depth--;
    else if (depth === 0) { re.lastIndex = i; if (re.test(clean)) return i; }
  }
  return -1;
}

// closeOf is where the bracket at open closes in clean, or -1.
function closeOf(clean, open) {
  for (let i = open, depth = 0; i < clean.length; i++) {
    if (clean[i] === '(') depth++;
    else if (clean[i] === ')' && --depth === 0) return i;
  }
  return -1;
}

// splitTop is [from, to) of each comma-separated part of clean[a, b), at
// the level of a. A string is blank in clean, so a part may look empty.
function splitTop(clean, a, b) {
  const out = [];
  let start = a;
  for (let i = a, depth = 0; i < b; i++) {
    const c = clean[i];
    if (c === '(' || c === '[') depth++;
    else if (c === ')' || c === ']') depth--;
    else if (c === ',' && depth === 0) { out.push([start, i]); start = i + 1; }
  }
  out.push([start, b]);
  return out;
}

// statementAt is where the statement the cursor is in begins and ends: the
// text between the semicolons around it, whatever the rest of a script is.
export function statementAt(sql, pos) {
  const clean = blank(sql);
  const a = clean.lastIndexOf(';', pos - 1) + 1;
  const b = clean.indexOf(';', pos);
  return [a, b < 0 ? sql.length : b];
}

async function columnOptions(tok, tokStart, sql, pos) {
  // Only the tables the statement being written reads are active: a script
  // may query many others, and their columns have no place here.
  const [from0, to0] = statementAt(sql, pos);
  const refs = parseRefs(sql).filter((r) => r.start >= from0 && r.start < to0);
  const parts = tok.replace(/`/g, '').split('.');
  const qual = parts.slice(0, -1);
  const lastDot = tok.lastIndexOf('.');
  const from = tokStart + (lastDot < 0 ? (tok.startsWith('`') ? 1 : 0) : lastDot + 1);
  const cards = await Promise.all(refs.map(async (r) => {
    const t = resolve(r);
    return { ref: r, card: t ? await tableCard(t) : null };
  }));
  const options = [];
  const field = (f, owner, boost) => ({
    label: f.name, detail: [typeText(f), owner].filter(Boolean).join(' · '), type: f.type === 'RECORD' ? 'namespace' : 'property',
    info: f.description || undefined, boost,
    apply: quoteIdent(f.name) !== f.name ? (view, _c, a, b) => view.dispatch({ changes: { from: a, to: b, insert: quoteIdent(f.name) } }) : undefined,
  });
  if (qual.length) {
    // A table's name or alias lists its columns; a struct column its fields.
    const first = qual[0].toLowerCase();
    const hit = cards.find((c) => c.card && (c.ref.alias.toLowerCase() === first || c.ref.parts[c.ref.parts.length - 1].toLowerCase() === first));
    let fields = null;
    if (hit) fields = walk(columnsOf(hit.card), qual.slice(1)) || [];
    else {
      for (const c of cards) {
        const f = walk(columnsOf(c.card), qual);
        if (f) { fields = f; break; }
      }
    }
    if (fields) {
      for (const f of fields) options.push(field(f, hit ? hit.ref.alias || hit.ref.parts[hit.ref.parts.length - 1] : '', 2));
      return { from, options };
    }
    // SAFE. comes before a function.
    if (qual.length === 1 && first === 'safe') return { from, options: await functionOptions(1) };
    // Anything else, such as a CTE, offers everything below.
  }
  const names = new Set();
  // Each table once, however many times the statement reads it.
  const tables = new Map();
  for (const c of cards) {
    const name = c.ref.parts[c.ref.parts.length - 1];
    if (c.ref.alias) { options.push({ label: c.ref.alias, detail: name, type: 'class', boost: 0 }); names.add(c.ref.alias.toLowerCase()); }
    const r = resolve(c.ref);
    if (!r || !c.card) continue;
    const key = (r.project + '.' + r.dataset + '.' + r.table).toLowerCase();
    const t = tables.get(key) || { name, aliases: [], refs: 0, cols: columnsOf(c.card) };
    t.refs++;
    if (c.ref.alias && !t.aliases.includes(c.ref.alias)) t.aliases.push(c.ref.alias);
    tables.set(key, t);
  }
  for (const t of tables.values()) t.owner = t.refs === 1 && t.aliases.length ? t.aliases[0] : t.name;
  for (const o of mergeColumns([...tables.values()], cards.length > 1)) {
    names.add(o.label.toLowerCase());
    if (quoteIdent(o.label) !== o.label) o.apply = (view, _c, a, b) => view.dispatch({ changes: { from: a, to: b, insert: quoteIdent(o.label) } });
    options.push(o);
  }
  // The columns a PIVOT or UNPIVOT of the statement makes.
  if (!qual.length) {
    for (const c of madeColumns(sql, blank(sql), from0, to0)) {
      if (names.has(c.name.toLowerCase())) continue;
      names.add(c.name.toLowerCase());
      const o = { label: c.name, detail: c.by, type: 'property', boost: 2 };
      if (quoteIdent(c.name) !== c.name) o.apply = (view, _c, a, b) => view.dispatch({ changes: { from: a, to: b, insert: quoteIdent(c.name) } });
      options.push(o);
    }
  }
  // BigQuery's functions, below the columns, unless a backticked name is
  // being typed.
  if (!qual.length && !tok.startsWith('`')) options.push(...await functionOptions(1));
  // Every other word in the editor: a CTE's columns and aliases, names in
  // another statement, whatever has been typed.
  for (const w of words(sql, from, pos)) {
    if (!names.has(w.toLowerCase())) options.push({ label: w, type: 'text', boost: 0 });
  }
  return { from, options };
}

async function source(context) {
  const { state, pos } = context;
  const sql = state.doc.toString();
  const clean = blank(sql);
  // Not in a comment or string.
  if (clean[pos - 1] === ' ' && sql[pos - 1] !== ' ' && sql[pos - 1] !== undefined) return null;
  const m = /`?[\w.\-]*`?$/.exec(clean.slice(0, pos));
  const tok = m ? m[0].replace(/`$/, '') : '';
  const tokStart = pos - (m ? m[0].length : 0);
  const fromList = inFromList(clean, tokStart);
  // An argument of fixed values, as DATE_TRUNC's granularity, offers them
  // and nothing else, as soon as the argument begins.
  const values = !fromList && !tok.includes('.') && !tok.startsWith('`') && valueOptions(sql.slice(0, tokStart));
  if (values) return { from: tokStart, options: values, validFor: /^[\w()]*$/ };
  if (!tok && !context.explicit) return null;
  if (!tok && !fromList && !context.explicit) return null;
  if (fromList) {
    // Tables match by any part of their name, not the editor's prefix, so
    // the list is filtered here and asked for again at each key.
    const r = await tableOptions(tok, tokStart, clean);
    const typed = unquote(sql.slice(r.from, pos));
    const options = byParts(r.options, typed);
    if (!options.length) return null;
    return { from: r.from, options, filter: false, getMatch: (o) => o.ranges };
  }
  // After a table in FROM, PIVOT and UNPIVOT, while what is typed begins one.
  if (!tok.includes('.') && !tok.startsWith('`') && afterTable(clean, tokStart)) {
    const clauses = await clauseOptions(9);
    const begins = (text) => clauses.some((c) => c.label.startsWith(text.toUpperCase()));
    if (begins(tok)) return { from: tokStart, options: clauses, validFor: (text) => /^\w*$/.test(text) && begins(text) };
  }
  // PIVOT( takes an aggregate call, and nothing else, before FOR.
  if (!tok.includes('.') && !tok.startsWith('`') && inPivotAggregate(sql.slice(0, tokStart))) {
    return { from: tokStart, options: await functionOptions(1, true), validFor: /^\w*$/ };
  }
  const r = await columnOptions(tok, tokStart, sql, pos);
  if (!r || !r.options.length) return null;
  return { from: r.from, options: r.options, validFor: /^[\w]*$/ };
}

// ---- hover ----

// stamp formats a compact UTC timestamp like "Oct 16, 05:04 UTC" (including
// the year only when it differs from the current UTC year).
const stamp = (iso) => {
  const t = new Date(iso);
  if (!(t.getUTCFullYear() > 2000)) return '';
  const sameYear = t.getUTCFullYear() === new Date().getUTCFullYear();
  const opts = { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' };
  if (!sameYear) opts.year = 'numeric';
  return t.toLocaleString('en-US', opts) + ' UTC';
};

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// consoleURL is the table's page in the BigQuery console, as u opens it.
const consoleURL = (c) => 'https://console.cloud.google.com/bigquery?project=' + encodeURIComponent(c.project) +
  `&ws=!1m5!1m4!4m3!1s${encodeURIComponent(c.project)}!2s${encodeURIComponent(c.dataset)}!3s${encodeURIComponent(c.table)}`;

// flash shows a button's done text for a moment.
function flash(btn, text) {
  const was = btn.dataset.was || btn.textContent;
  btn.dataset.was = was;
  btn.textContent = text;
  clearTimeout(btn.flashing);
  btn.flashing = setTimeout(() => { btn.textContent = was; delete btn.dataset.was; }, 1500);
}

// cardDOM is the compact editor hover card of a table: a strip of the kind
// and the actions, the name on one line (a link to BigQuery), concise
// metadata, and a single-line scrollable schema table.
export function cardDOM(card) {
  const root = el('div', 'sc-card');
  const d = card.detail, mdl = card.model;
  const cols = columnsOf(card);
  const modelDoc = {};
  for (const c of (mdl && mdl.columns) || []) modelDoc[c.name] = c.description;
  const full = `${card.project}.${card.dataset}.${card.table}`;

  const bar = el('div', 'sc-card-bar');
  const kind = (mdl && mdl.kind) || (d && d.type && d.type.toLowerCase().replace('_', ' ')) || '';
  if (kind) bar.append(el('span', 'sc-card-kind', kind));
  const actions = el('div', 'sc-card-actions');
  const openBtn = el('button', 'sc-card-link sc-card-open', '↗\u00a0bigquery');
  const nameBtn = el('button', 'sc-card-link sc-card-copyname', '⧉\u00a0name');
  openBtn.title = 'Open in BigQuery (u, on a card K showed or you clicked in)';
  nameBtn.title = 'Copy `' + full + '` (y, on a card K showed or you clicked in)';
  actions.append(openBtn, el('span', 'sc-card-sep', ' · '), nameBtn);
  const search = el('button', 'sc-card-link', '⌕\u00a0filter');
  search.title = 'Filter the columns (f, on a card K showed or you clicked in)';
  const copyBtn = el('button', 'sc-card-link', '⧉\u00a0columns');
  if (cols.length) actions.append(el('span', 'sc-card-sep', ' │ '), search, el('span', 'sc-card-sep', ' · '), copyBtn);
  bar.append(actions);
  root.append(bar);

  // One line: when it does not fit, project.dataset gives way, never the table.
  const name = el('a', 'sc-card-nameline');
  name.href = consoleURL(card);
  name.target = '_blank';
  name.rel = 'noopener';
  name.title = full + ': open in BigQuery';
  name.append(el('span', 'sc-card-qual', `${card.project}.${card.dataset}.`), el('span', 'sc-card-name', card.table));
  root.append(name);
  openBtn.addEventListener('click', () => openLink(name.href));
  // Back-quoted, as y copies it, so it pastes into a FROM.
  nameBtn.addEventListener('click', () => {
    copy('`' + full + '`', 'copied `' + full + '`');
    flash(nameBtn, '✓\u00a0copied');
  });

  // No columns says so, so the card never looks like it failed to load.
  if (!cols.length) root.append(el('div', 'sc-card-warn', 'no schema: ' + (card.error || 'BigQuery lists no columns for it')));
  else if (card.error) root.append(el('div', 'sc-card-warn', card.error));

  const desc = (d && d.description) || (mdl && mdl.description) || '';
  if (desc) {
    const descEl = el('div', 'sc-card-desc', desc);
    descEl.title = desc;
    root.append(descEl);
  }

  const meta = [];
  if (d && d.location) meta.push(d.location);
  if (d && d.numRows >= 0) meta.push(d.numRows.toLocaleString() + ' rows');
  if (d && d.numBytes >= 0) meta.push(fmtBytes(d.numBytes));
  if (d && d.modified && stamp(d.modified)) meta.push(stamp(d.modified));
  if (mdl && mdl.file) meta.push(mdl.file);
  if (meta.length) root.append(el('div', 'sc-card-meta', meta.join(' · ')));

  if (d) {
    const layout = [];
    if (d.partitionType) layout.push(`partition ${d.partitionField || 'ingestion time'} (${d.partitionType.toLowerCase()})`);
    if (d.clusterFields && d.clusterFields.length) layout.push(`cluster ${d.clusterFields.join(', ')}`);
    if (layout.length) root.append(el('div', 'sc-card-meta', layout.join(' · ')));
  }

  if (cols.length) {
    const find = el('input', 'sc-card-find');
    find.placeholder = `Filter ${cols.length} columns…`;
    find.spellcheck = false;
    find.hidden = true;
    root.append(find);
    const table = el('div', 'sc-card-table');
    // A record's fields follow it, indented, each with its type, as the
    // BigQuery console lists them; their own fields further in.
    const rows = [];
    const addRows = (fields, depth, parent) => {
      for (const f of fields) {
        const row = el('div', depth ? 'sc-card-row sc-card-sub' : 'sc-card-row');
        const colDesc = f.description || (depth ? '' : modelDoc[f.name]) || '';
        const descSpan = el('span', 'sc-card-coldesc', colDesc);
        if (colDesc) descSpan.title = colDesc;
        const name = el('span', 'sc-card-colname', f.name);
        if (depth) name.style.paddingLeft = 2 * depth + 'ch';
        row.append(name, el('span', 'sc-card-coltype', typeText(f)), descSpan);
        table.append(row);
        const r = { row, parent, text: (f.name + '\n' + colDesc).toLowerCase() };
        rows.push(r);
        if (f.fields && f.fields.length) addRows(f.fields, depth + 1, r);
      }
    };
    addRows(cols, 0, null);
    root.append(table);
    // A row shows when it matches, or a field in it does (so the path to a
    // field stays), or the record it is in does (so a record shows whole).
    find.addEventListener('input', () => {
      const q = find.value.trim().toLowerCase();
      const hit = new Set(rows.filter((r) => !q || r.text.includes(q)));
      const onPath = new Set();
      for (const r of hit) for (let a = r.parent; a && !onPath.has(a); a = a.parent) onPath.add(a);
      for (const r of rows) {
        let shown = hit.has(r) || onPath.has(r);
        for (let a = r.parent; !shown && a; a = a.parent) shown = hit.has(a);
        r.row.hidden = !shown;
      }
    });
    find.addEventListener('keydown', (e) => e.stopPropagation()); // typing here is not the editor's
    // While the filter shows, the card keeps the size and the columns'
    // widths it had: rows going would shrink it, and a card above its line
    // would jump down to stay on it. The rows scroll within that size.
    const pin = (on) => {
      if (on) {
        const r = root.getBoundingClientRect();
        table.style.gridTemplateColumns = getComputedStyle(table).gridTemplateColumns;
        root.style.width = r.width + 'px';
        // Not height, which CodeMirror clears when a tooltip has room.
        root.style.minHeight = root.style.maxHeight = r.height + 'px';
      } else {
        table.style.gridTemplateColumns = root.style.width = root.style.minHeight = root.style.maxHeight = '';
      }
    };
    search.classList.add('sc-card-filter');
    search.addEventListener('click', () => {
      if (find.hidden) pin(true);
      else if (document.activeElement === find) root.focus(); // a keyed card keeps the keys
      find.hidden = !find.hidden;
      if (!find.hidden) find.focus();
      else { find.value = ''; find.dispatchEvent(new Event('input')); pin(false); }
    });
    // One to a line with a comma after, as they paste into a SELECT.
    copyBtn.addEventListener('click', () => {
      copy(cols.map((f) => quoteIdent(f.name)).join(',\n'), 'copied ' + cols.length + ' column names');
      flash(copyBtn, '✓\u00a0copied');
    });
  }
  return root;
}

// keyedCard gives the keys to a card K showed, as a language server's
// hover window has them after a second K: f filters its columns, u opens
// its table in BigQuery, y copies its name (as ⧉ name does), j/k (and the arrows, ctrl+d/ctrl+u, g/G) scroll
// it, and esc, q or K close it, the keys going back to the editor. In the filter, esc or enter comes back to
// the card, the filter kept. close takes the card away.
export function keyedCard(dom, view, close) {
  dom.tabIndex = -1;
  dom.classList.add('sc-card-keyed');
  const box = dom.classList.contains('sc-card') ? dom : dom.querySelector('.sc-card') || dom;
  const back = () => { close(); view.focus(); };
  const scroll = (by) => box.scrollBy({ top: by });
  dom.addEventListener('keydown', (e) => {
    const find = e.target instanceof HTMLInputElement && e.target.classList.contains('sc-card-find') ? e.target : null;
    if (find) {
      if (e.key === 'Escape' || e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); dom.focus(); }
      return; // the rest is the filter's typing
    }
    if (e.metaKey || e.altKey) return; // from the card, or a button of it a click focused
    const half = Math.max(40, box.clientHeight / 2), line = 20;
    const keys = {
      Escape: back, q: back, K: back,
      f: () => {
        const input = dom.querySelector('.sc-card-find');
        if (!input) return;
        if (input.hidden) dom.querySelector('.sc-card-filter').click(); // shows it, the card's size kept
        input.focus();
        input.select();
      },
      u: () => { const open = dom.querySelector('.sc-card-open'); if (open) open.click(); },
      y: () => { const name = dom.querySelector('.sc-card-copyname'); if (name) name.click(); },
      j: () => scroll(line), ArrowDown: () => scroll(line),
      k: () => scroll(-line), ArrowUp: () => scroll(-line),
      g: () => box.scrollTo({ top: 0 }), G: () => box.scrollTo({ top: box.scrollHeight }),
    };
    const ctrl = { d: () => scroll(half), u: () => scroll(-half) };
    const run = e.ctrlKey ? ctrl[e.key] : keys[e.key];
    if (!run) return;
    e.preventDefault();
    e.stopPropagation();
    run();
  }, true); // capturing: the filter stops its keys going further
  // Leaving it for elsewhere on the page closes it; leaving the window keeps it.
  dom.addEventListener('focusout', () => setTimeout(() => {
    if (dom.isConnected && document.hasFocus() && !dom.contains(document.activeElement)) close();
  }, 0));
  // Once the tooltip is in the page, after vim has put its cursor down.
  setTimeout(() => { if (dom.isConnected) dom.focus({ preventScroll: true }); }, 0);
  return dom;
}

// keyOnFocus lets a card the mouse showed have the keys too: a click in
// it hands it to keep, which puts it in K's place (the same DOM), keyed,
// kept until esc, q, K or a click elsewhere. That waits for the click to
// end, as the card moving under the mouse would lose it (⌕ filter's), and
// the focus goes back where the click put it.
export function keyOnFocus(dom, keep) {
  dom.tabIndex = -1;
  let pressed = false;
  const pin = () => {
    if (dom.classList.contains('sc-card-keyed') || !dom.isConnected || !dom.contains(document.activeElement)) return;
    const was = document.activeElement;
    keep(dom);
    // After keyedCard's own focus of the card.
    setTimeout(() => { if (was !== dom && dom.contains(was)) was.focus({ preventScroll: true }); }, 0);
  };
  dom.addEventListener('mousedown', () => {
    pressed = true;
    window.addEventListener('mouseup', () => setTimeout(() => { pressed = false; pin(); }, 0), { once: true, capture: true });
  });
  dom.addEventListener('focusin', () => { if (!pressed) pin(); });
  return dom;
}

// refAt is the table the text names at pos, with its parsed reference.
function refAt(view, pos) {
  const ref = parseRefs(view.state.doc.toString()).find((r) => pos >= r.start && pos <= r.end);
  const t = ref && resolve(ref);
  return t ? { ref, t } : null;
}

const hover = cm.hoverTooltip(async (view, pos) => {
  const hit = refAt(view, pos);
  if (!hit) {
    const fn = functionAt(view.state, pos);
    return fn ? { pos: fn.from, end: fn.to, above: true, create: () => ({ dom: pinHover(view, fn.from, fn.to, functionCard(fn.fn)) }) } : null;
  }
  const card = await tableCard(hit.t, true);
  if (!card) return null;
  return { pos: hit.ref.start, end: hit.ref.end, above: true, create: () => ({ dom: pinHover(view, hit.ref.start, hit.ref.end, cardDOM(card)) }) };
}, { hoverTime: 350 });

// The card K shows, kept until the cursor moves, the text changes or esc.
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

// pinHover is a card the mouse showed, which a click in it makes K's.
const pinHover = (view, pos, end, dom) => keyOnFocus(dom, (d) => view.dispatch({ effects: [cm.closeHoverTooltips, setCard.of({ pos, end, make: () => keyedCard(d, view, closeCard(view)) })] }));

// hoverAtCursor is vim's K: the card of the table or function the cursor is
// on, or off again when it is showing. It says whether there was one to show.
export async function hoverAtCursor(view) {
  if (view.state.field(cardField, false)) { view.dispatch({ effects: setCard.of(null) }); return true; }
  const head = view.state.selection.main.head;
  const keyed = (make) => () => keyedCard(make(), view, closeCard(view));
  const hit = refAt(view, head);
  if (!hit) {
    const fn = functionAt(view.state, head);
    if (!fn) return false;
    view.dispatch({ effects: setCard.of({ pos: fn.from, end: fn.to, make: keyed(() => functionCard(fn.fn)) }) });
    return true;
  }
  const card = await tableCard(hit.t, true);
  if (!card) return false;
  view.dispatch({ effects: setCard.of({ pos: hit.ref.start, end: hit.ref.end, make: keyed(() => cardDOM(card)) }) });
  return true;
}

export function completionExtensions() {
  return [
    cm.autocompletion({ override: [source], activateOnTyping: true, icons: false, maxRenderedOptions: 60, selectOnOpen: true, filterStrict: true }),
    hover,
    cardField,
    ...functionExtensions(),
    cm.EditorView.domEventHandlers({
      keydown(e, view) {
        if (e.key === 'Escape' && view.state.field(cardField, false)) view.dispatch({ effects: setCard.of(null) });
        return false;
      },
    }),
  ];
}
