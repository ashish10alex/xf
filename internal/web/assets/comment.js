// Commenting lines out and back in, as vim-commentary's gc and VS Code's
// cmd+/ do (editor.js binds both). A file comments out with its language's
// line comment: -- in SQL (.sqlx, .sql, Scratch), // in JavaScript (a .js
// file, and a .sqlx file's config { } and js { }), # in Python. Lines
// already commented come back in whichever way they were: SQL's -- or #,
// or a /* */ around them.

import * as cm from './vendor/codemirror.mjs';

// sqlx.js marks each token of a .sqlx file's JavaScript with this.
const jsIsland = /(^|_)xfjs$/;
const jsStyle = { line: '//', block: { open: '/*', close: '*/' }, also: [] };

// styleOf is how the lines at starts (a position in each) comment out:
// {line, block, also}, also being the line comments taken out besides
// line; null for a language without comments.
function styleOf(state, starts) {
  const tree = cm.syntaxTree(state);
  const inJS = (pos) => {
    const node = tree.resolveInner(pos, 1);
    return node.from <= pos && node.to > pos && jsIsland.test(node.name);
  };
  if (starts.every(inJS)) return jsStyle;
  const tokens = state.languageDataAt('commentTokens', starts[0])[0];
  if (!tokens || !(tokens.line || tokens.block)) return null;
  // BigQuery's SQL comments a line with # as well as --.
  return { line: tokens.line || '', block: tokens.block || null, also: tokens.line === '--' ? ['#'] : [] };
}

// toggleLines is the change that comments lines from to to (1-based) out,
// or back in when every one of them that has text is commented already. It
// is null when the language has no comments.
export function toggleLines(state, from, to) {
  const lines = [];
  for (let n = from; n <= to; n++) lines.push(state.doc.line(n));
  const filled = lines.filter((l) => /\S/.test(l.text));
  const indentOf = (l) => /^\s*/.exec(l.text)[0].length;
  const style = styleOf(state, filled.length ? filled.map((l) => l.from + indentOf(l)) : [lines[0].from]);
  if (!style) return null;

  // Every line commented: each loses its comment, and a space after it.
  const markers = [style.line, ...style.also].filter(Boolean);
  const markerOf = (l) => markers.find((m) => l.text.startsWith(m, indentOf(l)));
  if (filled.length && filled.every(markerOf)) {
    return filled.map((l) => {
      const at = l.from + indentOf(l);
      const m = markerOf(l);
      const space = l.text[indentOf(l) + m.length] === ' ' ? 1 : 0;
      return { from: at, to: at + m.length + space };
    });
  }

  // One /* */ around them all: it comes out, and a line that held only
  // its open or close goes.
  const block = style.block && filled.length && blockAround(state, filled, style.block);
  if (block) return block;

  // Otherwise each line with text is commented at the least indent among
  // them, a blank line left as it is. Lines all blank get one each.
  if (!style.line) return wrapBlock(filled.length ? filled : lines, style.block);
  const take = filled.length ? filled : lines;
  const indent = Math.min(...take.map(indentOf));
  return take.map((l) => ({ from: l.from + indent, insert: style.line + ' ' }));
}

// blockAround is the change that takes out the /* */ that starts the first
// of lines and ends the last, when that is one comment; else null.
function blockAround(state, lines, { open, close }) {
  const first = lines[0], last = lines[lines.length - 1];
  const start = first.from + /^\s*/.exec(first.text)[0].length;
  const end = last.from + last.text.trimEnd().length;
  const text = state.doc.sliceString(start, end);
  if (!text.startsWith(open) || !text.endsWith(close) || text.length < open.length + close.length) return null;
  if (text.indexOf(close, open.length) !== text.length - close.length) return null;
  const bare = first !== last && first.text.trim() === open && last.text.trim() === close;
  if (bare && last.number === first.number + 1) return [{ from: first.from, to: last.to }];
  const changes = [];
  if (first.text.trim() === open && first !== last) {
    changes.push({ from: first.from, to: Math.min(first.to + 1, state.doc.length) });
  } else {
    changes.push({ from: start, to: start + open.length + (text[open.length] === ' ' ? 1 : 0) });
  }
  if (last.text.trim() === close && first !== last) {
    changes.push({ from: last.from - 1, to: last.to });
  } else {
    const space = text[text.length - close.length - 1] === ' ' && text.length > open.length + close.length + 1 ? 1 : 0;
    changes.push({ from: end - close.length - space, to: end });
  }
  return changes;
}

// wrapBlock puts a /* */ around lines, for a language with no line comment.
function wrapBlock(lines, { open, close }) {
  const first = lines[0], last = lines[lines.length - 1];
  return [
    { from: first.from + /^\s*/.exec(first.text)[0].length, insert: open + ' ' },
    { from: last.to, insert: ' ' + close },
  ];
}

// toggleSelection comments out (or back in) the lines of view's selection,
// for cmd+/. False when it can't: read-only, or no comments in the language.
export function toggleSelection(view) {
  const { state } = view;
  if (state.readOnly) return false;
  let from = Infinity, to = 0;
  for (const r of state.selection.ranges) {
    from = Math.min(from, state.doc.lineAt(r.from).number);
    // A selection that ends at the start of a line leaves that line out.
    const end = state.doc.lineAt(r.to);
    to = Math.max(to, r.to > r.from && end.from === r.to && end.number > 1 ? end.number - 1 : end.number);
  }
  const changes = toggleLines(state, from, Math.max(from, to));
  if (!changes) return false;
  view.dispatch({ changes, userEvent: 'input.comment', scrollIntoView: true });
  return true;
}

// toggleRange comments out (or back in) lines from to to (1-based), for
// vim's gc. It returns the first line's first non-blank column, where
// vim's cursor goes, or null when nothing changed.
export function toggleRange(view, from, to) {
  if (view.state.readOnly) return null;
  const changes = toggleLines(view.state, from, to);
  if (!changes) return null;
  view.dispatch({ changes, userEvent: 'input.comment' });
  const line = view.state.doc.line(Math.min(from, view.state.doc.lines));
  return /^\s*/.exec(line.text)[0].length;
}
