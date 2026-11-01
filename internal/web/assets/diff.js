// The conflict's difference (view.js): how the file on disk becomes the
// editor's text, as @codemirror/merge's Chunk works it out, the
// characters that differ marked as the source pane's git peek marks them.

import { Chunk, Text } from './vendor/codemirror.mjs';

// How long the diff looks before it settles for a rougher answer, so a
// big file never stalls the page.
const diffConfig = { timeout: 500 };

// lineOf is the line (1-based) pos is on; past the end, the line after it.
const lineOf = (t, pos) => (pos > t.length ? t.lines + 1 : t.lineAt(pos).number);

// rowsOf are a chunk's lines on one side (from to to, in t), each with
// the ranges of its characters that differ, from the line's start. Each
// change's range on that side is [change[f], change[g]).
function rowsOf(t, from, to, changes, f, g, op) {
  if (to <= from) return [];
  const lines = t.sliceString(from, Math.max(from, to - 1)).split('\n');
  const rows = [];
  let pos = 0;
  for (const text of lines) {
    const end = pos + text.length;
    const marks = [];
    for (const ch of changes) {
      const a = Math.max(ch[f], pos), b = Math.min(ch[g], end);
      if (b > a) marks.push([a - pos, b - pos]);
    }
    rows.push({ op, text, marks });
    pos = end + 1;
  }
  return rows;
}

// lineRows are every line of how a becomes b: op ' ' (in both), '-'
// (only in a) or '+' (only in b), with the ranges of the characters that
// differ in a changed line. A chunk with lines on only one side marks
// none, as all of them differ.
function lineRows(a, b) {
  const ta = Text.of(a.split('\n')), tb = Text.of(b.split('\n'));
  const rows = [];
  let next = 1; // b's first line not yet in rows
  const same = (to) => { for (; next < to; next++) rows.push({ op: ' ', text: tb.line(next).text, marks: [] }); };
  for (const c of Chunk.build(ta, tb, diffConfig)) {
    same(lineOf(tb, c.fromB));
    const both = c.toA > c.fromA && c.toB > c.fromB;
    const changes = both ? c.changes : [];
    const gone = rowsOf(ta, c.fromA, c.toA, changes, 'fromA', 'toA', '-');
    const added = rowsOf(tb, c.fromB, c.toB, changes, 'fromB', 'toB', '+');
    next += added.length;
    // A change at a file's end without a line end takes in the line
    // before it, unchanged: those lines are left out of it.
    const kept = (x, y) => x && y && x.text === y.text && !x.marks.length && !y.marks.length;
    let head = 0, tail = 0;
    while (kept(gone[head], added[head])) head++;
    while (gone.length - tail > head && added.length - tail > head && kept(gone[gone.length - 1 - tail], added[added.length - 1 - tail])) tail++;
    const mid = (r) => r.slice(head, r.length - tail);
    let g = mid(gone), d = mid(added);
    if (!g.length || !d.length) [g, d] = [g.map((r) => ({ ...r, marks: [] })), d.map((r) => ({ ...r, marks: [] }))];
    rows.push(...added.slice(0, head).map((r) => ({ ...r, op: ' ' })), ...g, ...d, ...added.slice(added.length - tail).map((r) => ({ ...r, op: ' ' })));
  }
  same(tb.lines + 1);
  return rows;
}

// withContext keeps the changed rows and context rows around each, with
// a gap marker (null) between runs.
function withContext(rows, context) {
  const keep = new Array(rows.length).fill(false);
  rows.forEach(({ op }, i) => {
    if (op === ' ') return;
    for (let k = Math.max(0, i - context); k <= Math.min(rows.length - 1, i + context); k++) keep[k] = true;
  });
  const out = [];
  let gap = false;
  rows.forEach((r, i) => {
    if (keep[i]) { out.push(r); gap = false; } else if (!gap && out.length) { out.push(null); gap = true; }
  });
  if (out[out.length - 1] === null) out.pop();
  return out;
}

// The last texts compared, and their rows: the page redraws often while
// the difference is shown.
let last = { a: null, b: null, rows: null };

// diffRows are the changed lines of how a becomes b and three lines
// around each, each {op, text, marks}, with null between runs; none when
// a is b.
export function diffRows(a, b) {
  if (last.a !== a || last.b !== b) last = { a, b, rows: withContext(lineRows(a, b), 3) };
  return last.rows;
}
