// The SQL pane's read-only vim buffer, as the TUI's (internal/ui/buffer.go
// and search.go): a cursor, h/j/k/l and word motions, charwise and
// linewise visual selections that y copies, and / * # n N searches. It is
// view state only, so it lives in the page; the text comes from xf.

export const VISUAL_NONE = 0, VISUAL_CHAR = 1, VISUAL_LINE = 2;

export function newBuffer() {
  return {
    text: [],       // plain lines, as arrays of code points
    row: 0, col: 0,
    want: 0,        // the column j/k aim for; -1 is the end of the line
    offset: 0,      // first visible row
    height: 20,     // visible rows
    visual: VISUAL_NONE, vrow: 0, vcol: 0,
    pendingY: false,
    search: { text: '', word: false, back: false },
    hits: [],       // {row, from, to}
    hlHits: false,
  };
}

const isSpace = (c) => /\s/u.test(c);
const isWordChar = (c) => c === '_' || /[\p{L}\p{N}]/u.test(c);

// charClass groups characters as vim's word motions do: blanks, keyword
// characters and punctuation; for WORD motions every non-blank is one.
export function charClass(c, big) {
  if (isSpace(c)) return 0;
  if (big || isWordChar(c)) return 1;
  return 2;
}

const lastCol = (l) => Math.max(l.length - 1, 0);
const line = (b) => b.text[b.row] || [];

export function firstNonBlank(l) {
  for (let i = 0; i < l.length; i++) if (!isSpace(l[i])) return i;
  return lastCol(l);
}

// setText replaces the content, keeping the cursor where it can.
export function setText(b, plain, reset) {
  b.text = plain.map((l) => Array.from(l));
  b.visual = VISUAL_NONE;
  b.pendingY = false;
  b.hits = find(b.search, b.text);
  if (reset) {
    b.row = b.col = b.want = b.offset = 0;
  }
  b.row = Math.min(b.row, Math.max(b.text.length - 1, 0));
  clampCol(b);
  follow(b);
}

function clampCol(b) { b.col = Math.min(Math.max(b.col, 0), lastCol(line(b))); }

export function setCol(b, c) {
  b.col = c;
  clampCol(b);
  b.want = b.col;
  follow(b);
}

function applyWant(b) {
  b.col = b.want < 0 ? lastCol(line(b)) : Math.min(b.want, lastCol(line(b)));
}

export function moveRow(b, n) {
  b.row = Math.min(Math.max(b.row + n, 0), Math.max(b.text.length - 1, 0));
  applyWant(b);
  follow(b);
}

export function gotoRow(b, r) {
  b.row = Math.min(Math.max(r, 0), Math.max(b.text.length - 1, 0));
  setCol(b, firstNonBlank(line(b)));
}

function maxOffset(b) { return Math.max(b.text.length - b.height, 0); }

function clampOffset(b) { b.offset = Math.min(Math.max(b.offset, 0), maxOffset(b)); }

// follow scrolls so the cursor is visible.
export function follow(b) {
  if (b.row < b.offset) b.offset = b.row;
  if (b.row >= b.offset + b.height) b.offset = b.row - b.height + 1;
  clampOffset(b);
}

// halfPage scrolls the view and the cursor together (ctrl+d, ctrl+u).
export function halfPage(b, dir) {
  const n = Math.max(Math.floor(b.height / 2), 1) * dir;
  b.offset += n;
  clampOffset(b);
  moveRow(b, n);
}

// scrollView scrolls without moving the cursor unless it would leave the
// view (ctrl+e, ctrl+y, the wheel).
export function scrollView(b, n) {
  b.offset += n;
  clampOffset(b);
  if (b.row < b.offset) b.row = b.offset;
  else if (b.row >= b.offset + b.height) b.row = b.offset + b.height - 1;
  else return;
  b.row = Math.min(b.row, Math.max(b.text.length - 1, 0));
  applyWant(b);
}

// wordForward is w (W when big): the start of the next word, across
// lines; an empty line counts as a word.
export function wordForward(b, big) {
  if (!b.text.length) return;
  let r = b.row, c = b.col;
  let l = b.text[r];
  if (c < l.length) {
    const cls = charClass(l[c], big);
    if (cls !== 0) while (c < l.length && charClass(l[c], big) === cls) c++;
  }
  for (;;) {
    l = b.text[r];
    while (c < l.length && charClass(l[c], big) === 0) c++;
    if (c < l.length) break;
    if (r === b.text.length - 1) { c = lastCol(l); break; }
    r++; c = 0;
    if (b.text[r].length === 0) break;
  }
  b.row = r;
  setCol(b, c);
}

// wordBackward is b (B when big): the start of the previous word.
export function wordBackward(b, big) {
  if (!b.text.length) return;
  let r = b.row, c = b.col;
  const back = () => {
    if (c > 0) c--;
    else if (r === 0) return false;
    else { r--; c = b.text[r].length; }
    return true;
  };
  if (!back()) return;
  for (;;) {
    const l = b.text[r];
    if (c < l.length && charClass(l[c], big) !== 0) break;
    if (l.length === 0) { b.row = r; setCol(b, 0); return; }
    if (!back()) { b.row = 0; setCol(b, 0); return; }
  }
  const l = b.text[r];
  const cls = charClass(l[c], big);
  while (c > 0 && charClass(l[c - 1], big) === cls) c--;
  b.row = r;
  setCol(b, c);
}

export function toggleVisual(b, mode) {
  if (b.visual === mode) b.visual = VISUAL_NONE;
  else if (b.visual === VISUAL_NONE) { b.visual = mode; b.vrow = b.row; b.vcol = b.col; }
  else b.visual = mode;
}

// swapVisualEnds is o: the other end of the selection.
export function swapVisualEnds(b) {
  [b.row, b.vrow] = [b.vrow, b.row];
  [b.col, b.vcol] = [b.vcol, b.col];
  b.want = b.col;
  follow(b);
}

export function selection(b) {
  let [sr, sc, er, ec] = [b.vrow, b.vcol, b.row, b.col];
  if (sr > er || (sr === er && sc > ec)) [sr, sc, er, ec] = [er, ec, sr, sc];
  return [sr, sc, er, ec];
}

// yankVisual is the selected text and its line count; it ends visual mode
// with the cursor at the start of the selection, as vim does.
export function yankVisual(b) {
  let [sr, sc, er, ec] = selection(b);
  const parts = [];
  for (let r = sr; r <= er; r++) {
    const l = b.text[r];
    let from = 0, to = l.length;
    if (b.visual === VISUAL_CHAR) {
      if (r === sr) from = Math.min(sc, l.length);
      if (r === er) to = Math.min(ec + 1, l.length);
    }
    parts.push(l.slice(from, Math.max(to, from)).join(''));
  }
  if (b.visual === VISUAL_LINE) sc = firstNonBlank(b.text[sr]);
  b.visual = VISUAL_NONE;
  b.row = sr;
  setCol(b, sc);
  return [parts.join('\n'), er - sr + 1];
}

export const yankLine = (b) => line(b).join('');

export function moveTo(b, row, col) {
  b.row = Math.min(Math.max(row, 0), Math.max(b.text.length - 1, 0));
  setCol(b, col);
}

// find lists a search's matches: smart case (any capital makes it case
// sensitive), and for * and # whole words whatever their case.
export function find(p, text) {
  let q = Array.from(p.text || '');
  if (!q.length) return [];
  let fold = p.word || !q.some((c) => c !== c.toLowerCase());
  if (fold) q = q.map((c) => c.toLowerCase());
  const hits = [];
  text.forEach((l, row) => {
    for (let c = 0; c + q.length <= l.length; c++) {
      let ok = true;
      for (let i = 0; i < q.length; i++) {
        const ch = fold ? l[c + i].toLowerCase() : l[c + i];
        if (ch !== q[i]) { ok = false; break; }
      }
      if (!ok) continue;
      const end = c + q.length;
      if (p.word && ((c > 0 && charClass(l[c - 1], false) === 1) || (end < l.length && charClass(l[end], false) === 1))) continue;
      hits.push({ row, from: c, to: end });
      c = end - 1;
    }
  });
  return hits;
}

export function setSearch(b, p) {
  b.search = p;
  b.hits = find(p, b.text);
  b.hlHits = p.text !== '';
}

// nextHit is the match after (or before) the cursor, and whether that
// went round the end.
export function nextHit(b, forward) {
  const n = b.hits.length;
  const after = (h, strict) => h.row > b.row || (h.row === b.row && (strict ? h.from > b.col : h.from >= b.col));
  if (forward) {
    const i = b.hits.findIndex((h) => after(h, true));
    return i < 0 ? [0, true] : [i, false];
  }
  let i = b.hits.findIndex((h) => after(h, false));
  i = (i < 0 ? n : i) - 1;
  return i < 0 ? [n - 1, true] : [i, false];
}

export function curHit(b) {
  return b.hits.findIndex((h) => h.row === b.row && h.from === b.col);
}

export function gotoHit(b, i) {
  b.row = b.hits[i].row;
  setCol(b, b.hits[i].from);
}

// step goes to the next match (d 1) or the previous (d -1), as n and N
// do; it answers what the footer should say, if anything.
export function step(b, d) {
  if (!b.search.text) return 'no search yet: * searches the word under the cursor, / the zoomed sql (z)';
  b.hlHits = true;
  if (!b.hits.length) return 'not found: ' + b.search.text;
  const forward = (d > 0) !== b.search.back;
  const [i, wrapped] = nextHit(b, forward);
  gotoHit(b, i);
  if (wrapped) return forward ? 'search hit bottom, continuing at top' : 'search hit top, continuing at bottom';
  return '';
}

export function wordAtCursor(b) {
  const l = line(b);
  let c = b.col;
  while (c < l.length && charClass(l[c], false) !== 1) c++;
  if (c >= l.length) return ['', 0];
  let from = c, to = c;
  while (from > 0 && charClass(l[from - 1], false) === 1) from--;
  while (to < l.length && charClass(l[to], false) === 1) to++;
  return [l.slice(from, to).join(''), from];
}

// searchWord is * (and # when back).
export function searchWord(b, back) {
  const [word, from] = wordAtCursor(b);
  if (!word) return 'no word under the cursor';
  b.col = from;
  setSearch(b, { text: word, word: true, back });
  return step(b, 1);
}

// searchNote is the search's part of the pane's title.
export function searchNote(b) {
  if (!b.hlHits) return '';
  let note = '/' + b.search.text;
  const cur = curHit(b);
  if (!b.hits.length) note += ' · no match';
  else if (cur >= 0) note += ` · ${cur + 1} of ${b.hits.length}`;
  else if (b.hits.length === 1) note += ' · 1 match';
  else note += ` · ${b.hits.length} matches`;
  return note;
}

export const pos = (b) => ({ row: b.row, col: b.col, want: b.want, offset: b.offset });

export function restore(b, p) {
  b.row = Math.min(p.row, Math.max(b.text.length - 1, 0));
  b.col = p.col; b.want = p.want; b.offset = p.offset;
  clampCol(b);
  clampOffset(b);
}
