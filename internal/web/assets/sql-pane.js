// The SQL and columns panes' keys: the vim buffer's motions, / and its
// search, and the columns list.

import { S, copy, filteredColumns, redraw, say, setFocus, toggleColumns, yank } from './state.js';
import * as vim from './vim.js';

function startSQLSearch() {
  const b = S.buf;
  S.sqlSearch = { typing: true, query: '', from: vim.pos(b), prev: b.search, prevHl: b.hlHits };
}

export function searchSQLFromPalette() {
  if (S.cols.shown) toggleColumns();
  setFocus('sql');
  if (!S.buf.text.length) { say('no SQL to search'); return; }
  startSQLSearch();
}

export function setSQLSearchQuery(q) {
  const b = S.buf, s = S.sqlSearch;
  s.query = q;
  vim.restore(b, s.from);
  vim.setSearch(b, { text: q, word: false, back: false });
  if (b.hits.length) vim.gotoHit(b, vim.nextHit(b, true)[0]);
  redraw();
}

export function endSQLSearch(accept) {
  const b = S.buf, s = S.sqlSearch;
  s.typing = false;
  vim.restore(b, s.from);
  if (!accept || !s.query) { vim.setSearch(b, s.prev); b.hlHits = s.prevHl; }
  if (accept) { const msg = vim.step(b, 1); if (msg) say(msg); }
  redraw();
}

export function sqlKey(k) {
  const b = S.buf;
  if (!b.text.length) return false;
  if (b.pendingY) {
    b.pendingY = false;
    if (k === 'y') { yank(vim.yankLine(b), 1, true); return true; }
  }
  const msg = (m) => { if (m) say(m); return true; };
  switch (k) {
    case 'k': case 'up': case 'ctrl+p': vim.moveRow(b, -1); return true;
    case 'j': case 'down': case 'ctrl+n': vim.moveRow(b, 1); return true;
    case 'h': case 'left': vim.setCol(b, b.col - 1); return true;
    case 'l': case 'right': vim.setCol(b, b.col + 1); return true;
    case 'w': case 'W': vim.wordForward(b, k === 'W'); return true;
    case 'b': case 'B': vim.wordBackward(b, k === 'B'); return true;
    case '0': vim.setCol(b, 0); return true;
    case '^': vim.setCol(b, vim.firstNonBlank(b.text[b.row])); return true;
    case '$': vim.setCol(b, b.text[b.row].length); b.want = -1; return true;
    case 'g': case 'home': vim.gotoRow(b, 0); return true;
    case 'G': case 'end': vim.gotoRow(b, b.text.length - 1); return true;
    case 'ctrl+d': case 'pgdown': vim.halfPage(b, 1); return true;
    case 'ctrl+u': case 'pgup': vim.halfPage(b, -1); return true;
    case 'ctrl+e': vim.scrollView(b, 1); return true;
    case 'ctrl+y': vim.scrollView(b, -1); return true;
    case 'v': vim.toggleVisual(b, vim.VISUAL_CHAR); return true;
    case 'V': vim.toggleVisual(b, vim.VISUAL_LINE); return true;
    case 'esc':
      if (b.visual) { b.visual = vim.VISUAL_NONE; return true; }
      if (b.hlHits) { b.hlHits = false; return true; }
      return false;
    case '/':
      if (!b.text.length) { say('no SQL to search'); return true; }
      startSQLSearch();
      return true;
    case 'n': return msg(vim.step(b, 1));
    case 'N': return msg(vim.step(b, -1));
    case '*': return msg(vim.searchWord(b, false));
    case '#': return msg(vim.searchWord(b, true));
    case 'o':
      if (b.visual) { vim.swapVisualEnds(b); return true; }
      return false;
    case 'y':
      if (b.visual) {
        const linewise = b.visual === vim.VISUAL_LINE;
        const [text, lines] = vim.yankVisual(b);
        yank(text, lines, linewise);
        return true;
      }
      b.pendingY = true;
      return true;
    case 'Y': yank(vim.yankLine(b), 1, true); return true;
  }
  return false;
}

export function columnsKey(k) {
  const fields = filteredColumns();
  const c = S.cols;
  const move = (n) => { c.cursor = Math.min(Math.max(c.cursor + n, 0), Math.max(fields.length - 1, 0)); };
  switch (k) {
    case 'j': case 'down': move(1); return true;
    case 'k': case 'up': move(-1); return true;
    case 'ctrl+d': case 'pgdown': move(10); return true;
    case 'ctrl+u': case 'pgup': move(-10); return true;
    case 'g': case 'home': c.cursor = 0; return true;
    case 'G': case 'end': c.cursor = Math.max(fields.length - 1, 0); return true;
    case '/':
      if (!S.view || !S.view.columns.fields.length) { say('no columns to filter'); return true; }
      c.typing = true;
      return true;
    case 'esc':
      if (c.name || c.type) { c.name = ''; c.type = ''; c.cursor = 0; return true; }
      return false;
    case 'y': { const f = fields[c.cursor]; if (f) copy(f.name, 'copied ' + f.name); return true; }
    case 'Y': { const f = fields[c.cursor]; if (f) copy(f.name + ' ' + f.type, 'copied ' + f.name + ' ' + f.type); return true; }
  }
  return false;
}
