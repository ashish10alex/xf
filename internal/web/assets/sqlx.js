// The source pane's languages: Dataform's .sqlx, JavaScript, a dbt
// model's SQL with Jinja, and Terraform's HCL.
//
// .sqlx follows the Dataform Tools extension's TextMate grammar
// (vscode-dataform-tools/sqlx.grammar.json), ported by hand, since
// CodeMirror can't read TextMate grammars: BigQuery SQL, with JavaScript in
// config { }, js { } and ${ }, which CodeMirror's JavaScript tokenizer
// colours. A JavaScript island ends at the brace that closes the one it
// opened with; braces in its strings, template literals and comments don't
// count. pre_operations { } and post_operations { } hold SQL.

import * as cm from './vendor/codemirror.mjs';
import { sqlKeywords, sqlTypes } from './editor.js';

const js = cm.javascriptMode;

// The grammar's keyword groups, with BigQuery's that the Scratch editor
// knows.
const KEYWORDS = new Set((sqlKeywords + ' ' + [
  'select all distinct except replace from where in is null like between and or with group by rollup order limit having window',
  'union intersect for system time of offset system_time tablesample percent',
  'inner cross full left right outer join on using',
  'insert into values delete update set merge matched default truncate',
  'create temporary temp drop column schema table function view materialized external snapshot clone if not exists partition',
  'cluster options data type rows range unbounded preceding following current row returns language deterministic procedure model',
  'add cascade restrict rename to grant revoke assert export pivot unpivot qualify include exclude over ignore respect nulls',
  'first last interval at zone begin exception error out inout call declare elseif loop while do break leave continue iterate',
  'return execute immediate raise message case when then else end as asc desc recursive struct array unnest lateral natural',
  'cast safe_cast extract',
].join(' ')).toLowerCase().split(/\s+/));
const TYPES = new Set((sqlTypes + ' array boolean bytes date datetime geography numeric decimal bignumeric bigdecimal float float64 int64 int smallint integer bigint tinyint byteint bool record struct string time timestamp json interval range').toLowerCase().split(/\s+/));
const CONSTANTS = new Set(['true', 'false', 'null']);
// Words a ( can follow that aren't functions; any other word before a ( is
// a call.
const NOT_CALLS = new Set('in exists over as values using on not and or when then else case select from where join with by table into insert partition window qualify having all any some recursive options returns interval struct array cluster except replace'.split(' '));
// A name after these is a table's, which the grammar colours as a function.
const TABLE_BEFORE = new Set(['from', 'join', 'update', 'into', 'table', 'using', 'clone', 'view']);
const JINJA_KEYWORDS = new Set('if elif else endif for endfor in set endset macro endmacro call endcall filter endfilter not and or is true false none do block endblock extends include import from with endwith raw endraw as recursive loop'.split(' '));

const fullCopy = (state) => {
  if (!state || typeof state !== 'object') return state;
  const out = {};
  for (const k in state) out[k] = Array.isArray(state[k]) ? state[k].slice() : state[k];
  return out;
};

function startState(unit, jinja) {
  return {
    unit: unit || 2,
    jinja,         // a dbt model's SQL: {{ }}, {% %} and {# #} are templates
    comment: false, // in a /* */ comment
    quote: '',     // the quote of the string the line continues: ', ", ''', """ or `
    quoteKind: '', // its style: 'string', or 'table' for a table's backticked name
    tableNext: false,
    pending: '',   // a block keyword waiting for its {: 'config', 'js' or 'sql'
    sqlBraces: 0,  // the pre_operations/post_operations blocks open
    island: '',    // the JavaScript island the line is in: 'config', 'js' or 'interp'
    js: null,      // its tokenizer's state
    depth: 0,      // its braces open
    tag: '',       // the Jinja tag the line is in: '}}', '%}' or '#}'
  };
}

function copyState(s) {
  const out = { ...s };
  out.js = s.js && fullCopy(s.js);
  return out;
}

// enterIsland starts a JavaScript island whose opening brace the stream
// just read.
function enterIsland(state, kind, stream) {
  state.island = kind;
  state.js = js.startState(state.unit);
  state.depth = 1;
  if (kind !== 'interp') primeIsland(state.js, kind, stream);
}

// primeIsland has the JavaScript tokenizer read the island's opening as
// JavaScript would see it, so a new line in it is indented as JavaScript
// is: a config's brace opens an object (fed as `(  {`), a js block's a
// block (`;  {`). The text fed keeps the line's columns: the keyword's first
// letter turned into that ( or ;, the rest blanks, so the line's
// indentation, where the closing brace goes, stays as it is.
function primeIsland(jsState, kind, stream) {
  const at = stream.pos - 1; // the brace
  let text = stream.string.slice(0, at).replace(/\S/g, ' ');
  const first = stream.string.search(/\S/);
  if (first >= 0 && first < at) text = text.slice(0, first) + (kind === 'config' ? '(' : ';') + text.slice(first + 1);
  text += '{';
  const fed = new stream.constructor(text, stream.tabSize, stream.indentUnit);
  while (!fed.eol()) {
    fed.start = fed.pos;
    js.token(fed, jsState);
    if (fed.pos === fed.start) fed.next();
  }
}

// islandToken reads a token of the JavaScript island the stream is in.
function islandToken(stream, state) {
  const style = js.token(stream, state.js);
  const text = stream.current();
  const literal = style && /string|comment|regexp/.test(style);
  if (!literal) {
    for (const ch of text) {
      if (ch === '{') state.depth++;
      else if (ch === '}' && --state.depth === 0) {
        const kind = state.island;
        state.island = '';
        state.js = null;
        if (kind === 'interp' && state.quote) return state.quoteKind === 'table' ? 'variableName.function' : 'string';
        return 'punctuation';
      }
    }
  }
  // A config's keys are properties, as the grammar's object members are,
  // and a name called is a function: ref(), self(), when(). The tokenizer
  // reads a config as an object, so it calls its keys property.
  if (style && /^(variable|property$)/.test(style)) {
    if (state.island === 'config' && stream.match(/^\s*:/, false)) return 'propertyName';
    if (stream.match(/^\s*\(/, false)) return 'variableName.function';
  }
  return style || null;
}

// stringToken reads a string's text up to its end, or up to a ${ in a
// .sqlx file, which starts an island whose end comes back to the string.
function stringToken(stream, state, sqlx) {
  const q = state.quote;
  while (!stream.eol()) {
    if (sqlx && stream.match('${', false)) {
      if (stream.current().length) break; // the text before it first
      stream.match('${');
      enterIsland(state, 'interp', stream);
      return 'punctuation';
    }
    if (q.length === 1 && q !== '`' && stream.peek() === '\\') { stream.next(); stream.next(); continue; }
    if (stream.match(q)) { state.quote = ''; break; }
    stream.next();
  }
  if (state.quote && q.length === 1 && stream.eol()) state.quote = ''; // a one-line string ends with its line
  return state.quoteKind === 'table' ? 'variableName.function' : 'string';
}

function jinjaToken(stream, state) {
  const closer = state.tag;
  if (closer === '#}') {
    while (!stream.eol()) { if (stream.match('#}')) { state.tag = ''; break; } stream.next(); }
    return 'comment';
  }
  if (stream.eatSpace()) return null;
  if (stream.match(closer) || stream.match('-' + closer)) { state.tag = ''; return 'punctuation'; }
  if (stream.match(/^"(?:[^"\\]|\\.)*"?/) || stream.match(/^'(?:[^'\\]|\\.)*'?/)) return 'string';
  if (stream.match(/^\d+(\.\d+)?/)) return 'number';
  const word = stream.match(/^[A-Za-z_]\w*/);
  if (word) {
    if (JINJA_KEYWORDS.has(word[0])) return 'keyword';
    return stream.match(/^\s*\(/, false) ? 'variableName.function' : 'variableName';
  }
  stream.next();
  return 'operator';
}

function sqlToken(stream, state, sqlx) {
  if (stream.sol() && state.quote.length === 1 && state.quote !== '`') state.quote = '';
  if (state.island) return islandToken(stream, state);
  if (state.tag) return jinjaToken(stream, state);
  if (state.comment) {
    while (!stream.eol()) { if (stream.match('*/')) { state.comment = false; break; } stream.next(); }
    return 'comment';
  }
  if (state.quote) return stringToken(stream, state, sqlx);
  if (stream.eatSpace()) return null;

  const tableNext = state.tableNext;
  state.tableNext = false;

  if (state.pending && stream.eat('{')) {
    const kind = state.pending;
    state.pending = '';
    if (kind === 'sql') { state.sqlBraces++; return 'punctuation'; }
    enterIsland(state, kind, stream);
    return 'punctuation';
  }
  state.pending = '';

  if (state.jinja) {
    const open = stream.match(/^\{[{%#]-?/);
    if (open) {
      state.tag = { '{': '}}', '%': '%}', '#': '#}' }[open[0][1]];
      if (state.tag === '#}') return jinjaToken(stream, state);
      return 'punctuation';
    }
  }
  if (sqlx && stream.match('${')) { enterIsland(state, 'interp', stream); return 'punctuation'; }
  if (stream.match('--') || (stream.peek() === '#' && !(state.jinja && stream.match(/^#\}/, false)))) { stream.skipToEnd(); return 'comment'; }
  if (stream.match('/*')) { state.comment = true; return sqlToken(stream, state, sqlx) || 'comment'; }

  if (sqlx && state.sqlBraces === 0) {
    const block = stream.match(/^(config|js|pre_operations|post_operations)\b(?=\s*\{)/);
    if (block) {
      state.pending = block[1] === 'config' ? 'config' : block[1] === 'js' ? 'js' : 'sql';
      return 'keyword';
    }
  }
  if (sqlx && state.sqlBraces > 0 && stream.eat('}')) { state.sqlBraces--; return 'punctuation'; }

  const quote = stream.match(/^[rRbB]{0,2}('''|"""|'|"|`)/);
  if (quote) {
    state.quote = quote[1];
    state.quoteKind = quote[1] === '`' && tableNext ? 'table' : 'string';
    return stringToken(stream, state, sqlx);
  }
  if (stream.match(/^(\d+\.\d*|\.\d+|\d+)([eE][+-]?\d+)?\b/)) return 'number';
  if (stream.match(/^@@?\w+/)) return 'variableName.special';

  const word = stream.match(/^[A-Za-z_]\w*/);
  if (word) {
    const w = word[0].toLowerCase();
    const call = stream.match(/^\s*\(/, false);
    if (CONSTANTS.has(w)) return w === 'null' ? 'null' : 'bool';
    if (TABLE_BEFORE.has(w)) { state.tableNext = true; return 'keyword'; }
    if (tableNext && !KEYWORDS.has(w)) return 'variableName.function';
    if (call && !NOT_CALLS.has(w)) return 'variableName.function';
    if (TYPES.has(w)) return 'typeName';
    if (KEYWORDS.has(w)) return 'keyword';
    return 'variableName';
  }
  if (stream.match(/^(<>|[<>!=]=|\|\||<<|>>|[-+*\/%~&|^<>=])/)) return 'operator';
  stream.next();
  return 'punctuation';
}

// JS marks each token of a JavaScript island, its braces and spaces too,
// with a tag no highlighter styles, so the SQL around them can be told.
const JS = 'xfjs';
const tokenTable = { [JS]: cm.tags.processingInstruction };

const isJS = new RegExp('(^|_)' + JS + '$');

function islandMarked(stream, state, sqlx) {
  const was = state.island;
  const style = sqlToken(stream, state, sqlx);
  return was || state.island ? (style ? style + ' ' + JS : JS) : style;
}

function streamOf(name, sqlx, jinja) {
  return cm.StreamLanguage.define({
    name,
    startState: (unit) => startState(unit, jinja),
    copyState,
    token: (stream, state) => islandMarked(stream, state, sqlx),
    indent: (state, textAfter, cx) => (state.island && state.js ? js.indent(state.js, textAfter, cx) : null),
    languageData: { commentTokens: { line: '--', block: { open: '/*', close: '*/' } } },
    tokenTable,
  });
}

// inJavaScript says whether what is just before pos is a JavaScript
// island's: in config { }, js { } or ${ }, or its brace.
export function inJavaScript(state, pos) {
  const node = cm.syntaxTree(state).resolveInner(pos, -1);
  return node.from < pos && isJS.test(node.name);
}

// sqlText is a .sqlx file's text with its JavaScript (config { }, js { },
// ${ }) blanked to spaces, so what reads SQL sees only SQL, at the same
// offsets. Text not parsed yet is taken as SQL.
const sqlTexts = new WeakMap();
export function sqlText(state) {
  const tree = cm.syntaxTree(state);
  const known = sqlTexts.get(state.doc);
  if (known && known.tree === tree) return known.text;
  const text = state.doc.toString();
  let out = '';
  let at = 0;
  tree.iterate({
    enter(n) {
      if (!isJS.test(n.name)) return;
      out += text.slice(at, n.from) + text.slice(n.from, n.to).replace(/[^\n]/g, ' ');
      at = n.to;
    },
  });
  out += text.slice(at);
  sqlTexts.set(state.doc, { tree, text: out });
  return out;
}

// ---- HCL ----
//
// The language of Terraform's .tf and .tfvars files and other .hcl files:
// a block's type and labels, attribute names, references, function calls,
// strings with their ${ } and %{ } and heredocs, and comments. CodeMirror's
// legacy modes have none.

const HCL_KEYWORDS = new Set(['for', 'in', 'if', 'else', 'endif', 'endfor']);
// HCL_ROOTS start a reference: var.region, local.name, each.value, …
const HCL_ROOTS = new Set(['var', 'local', 'module', 'data', 'each', 'count', 'path', 'terraform', 'self']);

// The state's stack holds what the text is inside: a quoted string
// ({ kind: 'str' }), a heredoc ({ kind: 'heredoc', tag }) or an
// interpolation ({ kind: 'interp', depth }, depth counting its own braces).
const hclStart = () => ({ stack: [], comment: false, header: false, dot: false });
const hclCopy = (s) => ({ ...s, stack: s.stack.map((c) => ({ ...c })) });

// hclString is the next piece of a string: its text up to an
// interpolation or its end, or the ${ or %{ that opens one.
function hclString(stream, state, quoted) {
  if (stream.match(/^[$%]\{/)) {
    state.stack.push({ kind: 'interp', depth: 0 });
    return 'punctuation';
  }
  while (!stream.eol()) {
    if (stream.match(/^([$%])\1\{/)) continue; // $${ is a literal ${
    if (stream.match(/^[$%]\{/, false)) return 'string';
    const ch = stream.next();
    if (quoted && ch === '\\') stream.next();
    else if (quoted && ch === '"') { state.stack.pop(); return 'string'; }
  }
  if (quoted) state.stack.pop(); // a quoted string ends with its line
  return 'string';
}

function hclToken(stream, state) {
  const top = state.stack[state.stack.length - 1];
  if (state.comment) {
    if (stream.match(/^.*?\*\//)) state.comment = false;
    else stream.skipToEnd();
    return 'comment';
  }
  if (top && top.kind === 'heredoc') {
    if (stream.sol() && stream.match(new RegExp('^\\s*' + top.tag + '\\s*$'))) {
      state.stack.pop();
      return 'string';
    }
    return hclString(stream, state, false);
  }
  if (top && top.kind === 'str') return hclString(stream, state, true);

  if (stream.sol() && !top) state.header = false;
  if (stream.eatSpace()) return null;
  const dot = state.dot;
  state.dot = false;

  if (stream.match('#') || stream.match('//')) { stream.skipToEnd(); return 'comment'; }
  if (stream.match('/*')) {
    state.comment = true;
    return hclToken(stream, state);
  }
  const here = stream.match(/^<<-?([A-Za-z_]\w*)/);
  if (here) {
    state.stack.push({ kind: 'heredoc', tag: here[1] });
    stream.skipToEnd();
    return 'string';
  }
  if (stream.peek() === '"') {
    // A block's labels: resource "google_bigquery_dataset" "raw" {
    if (state.header && stream.match(/^"(?:[^"\\]|\\.)*"?/)) return 'typeName';
    stream.next();
    state.stack.push({ kind: 'str' });
    return 'string';
  }
  if (top && top.kind === 'interp') {
    if (stream.eat('{')) { top.depth++; return 'punctuation'; }
    if (stream.eat('}')) {
      if (top.depth-- === 0) state.stack.pop();
      return 'punctuation';
    }
  }
  if (stream.match(/^\d+(\.\d+)?([eE][+-]?\d+)?/)) return 'number';

  const word = stream.match(/^[A-Za-z_][\w-]*/);
  if (word) {
    const w = word[0];
    if (dot) return 'propertyName';
    if (w === 'true' || w === 'false') return 'bool';
    if (w === 'null') return 'null';
    if (stream.match(/^\s*\(/, false)) return 'variableName.function';
    if (stream.match(/^\s*=(?![=>])/, false)) return 'propertyName';
    if (HCL_KEYWORDS.has(w)) return 'keyword';
    const lineStart = !top && !stream.string.slice(0, stream.start).trim();
    if (lineStart && stream.match(/^\s*("|\{|[A-Za-z_])/, false)) {
      state.header = true;
      return 'keyword';
    }
    if (HCL_ROOTS.has(w) && stream.match(/^\s*\./, false)) return 'variableName.special';
    return 'variableName';
  }
  if (stream.eat('.')) {
    if (stream.match('..')) return 'operator';
    state.dot = true;
    return 'punctuation';
  }
  if (stream.eat('{')) { state.header = false; return 'punctuation'; }
  if (stream.match(/^(=>|==|!=|<=|>=|&&|\|\||[-+*\/%!<>=?:~])/)) return 'operator';
  stream.next();
  return 'punctuation';
}

const hclMode = {
  name: 'hcl',
  startState: hclStart,
  copyState: hclCopy,
  token: hclToken,
  languageData: { commentTokens: { line: '#', block: { open: '/*', close: '*/' } } },
};

const sqlxLanguage = streamOf('sqlx', true, false);
const dbtLanguage = streamOf('dbt', false, true);
const jsLanguage = cm.StreamLanguage.define(js);
// The other files the source pane opens from the file tree: a project's
// settings and schema files, a dbt Python model, scripts, Terraform.
const otherModes = { json: cm.jsonMode, yaml: cm.yamlMode, toml: cm.tomlMode, python: cm.pythonMode, shell: cm.shellMode, hcl: hclMode };
const otherLanguages = {};

// languageFor is the language of a file the server calls lang ("sqlx",
// "js", "sql", "json", "yaml", "toml", "python", "shell", "hcl"); dbt says
// a .sql file is a dbt model's. Anything else is plain text.
export function languageFor(lang, dbt) {
  switch (lang) {
    case 'sqlx': return sqlxLanguage;
    case 'js': return jsLanguage;
    case 'sql': return dbt ? dbtLanguage : streamOf('sql', false, false);
  }
  if (!otherModes[lang]) return [];
  return (otherLanguages[lang] ||= cm.StreamLanguage.define(otherModes[lang]));
}
