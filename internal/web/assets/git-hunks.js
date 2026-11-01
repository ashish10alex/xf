// The source pane's git changes: the lines the text changed since git's
// index (the file's base, from the server), marked in a gutter as
// gitsigns marks them, a bar beside added and changed lines and a notch
// where lines were taken out. ]g and [g (vimNextHunk, vimPrevHunk), or a
// click on a mark, open the change's peek, as VS Code's does: the lines
// it took out above the lines it put in, the characters that differ
// marked in both, until escape or the cursor leaves the change. guh
// (vimUndoHunk), or the peek's undo, puts the change's lines back. The
// marks follow the text as it is typed, and the peek as it changes.
//
// Source Control's difference (ADR 0007) shows the hunks whole: inline,
// every hunk's lines taken out above the lines put in, or side by side,
// the text beside a read-only editor of its base (oldSide), each padded
// where the other has more lines so the two line up. gsh (vimStageHunk)
// stages the hunk under the cursor: the index's version becomes the base
// with that hunk's lines put in.

import * as cm from './vendor/codemirror.mjs';
import { vimKey } from './editor.js';

// A hunk is a run of changed lines: start is the text's first line of it
// (0-based), added how many of the text's lines it has, removed the
// base's lines it took out, and chunk @codemirror/merge's Chunk of it,
// whose changes are the characters that differ.

// How far the diff looks before it settles for a rougher answer, as
// @codemirror/merge's own views do, so a big file never stalls typing.
const diffConfig = { scanLimit: 500, timeout: 200 };

const textOf = (s) => (typeof s === 'string' ? cm.Text.of(s.split('\n')) : s);

// hunksOf are the hunks that make base into text (a string or the
// editor's Text); null when there is no base.
export function hunksOf(base, text) {
  if (base == null) return null;
  const a = textOf(base), b = textOf(text);
  return cm.Chunk.build(a, b, diffConfig).map((c) => {
    // A chunk that took the last lines out starts past the text's end.
    const start = c.fromB > b.length ? b.lines : b.lineAt(c.fromB).number - 1;
    return {
      start,
      added: c.toB > c.fromB ? b.lineAt(c.endB).number - start : 0,
      removed: c.toA > c.fromA ? a.sliceString(c.fromA, c.endA).split('\n') : [],
      chunk: c,
    };
  });
}

// kindOf is how a hunk is marked: add, change, or delete on the line
// above where lines were taken out (topdelete on the first line).
const kindOf = (h) => (!h.removed.length ? 'add' : !h.added ? (h.start ? 'delete' : 'topdelete') : 'change');

// lineOf is the line a hunk's mark starts on, where ]g goes.
const lineOf = (h) => (kindOf(h) === 'delete' ? h.start - 1 : h.start);

// hunkAt is the index of the hunk on line at (0-based), -1 for none. A
// hunk that took lines out is on the line its notch is on.
const hunkAt = (hunks, at) => (hunks || []).findIndex((h) => at >= lineOf(h) && at < lineOf(h) + Math.max(h.added, 1));

class Sign extends cm.GutterMarker {
  constructor(kind) { super(); this.kind = kind; }
  eq(o) { return o.kind === this.kind; }
  toDOM() {
    const e = document.createElement('div');
    e.className = 'cm-hunk cm-hunk-' + this.kind;
    return e;
  }
}
const signs = { add: new Sign('add'), change: new Sign('change'), delete: new Sign('delete'), topdelete: new Sign('topdelete') };

// signsOf are the marks of hunks in doc.
function signsOf(doc, hunks) {
  const ranges = [];
  for (const h of hunks || []) {
    const kind = kindOf(h);
    const n = kind === 'delete' || kind === 'topdelete' ? 1 : h.added;
    for (let i = 0; i < n; i++) {
      const line = lineOf(h) + i + 1;
      if (line <= doc.lines) ranges.push(signs[kind].range(doc.line(line).from));
    }
  }
  return cm.RangeSet.of(ranges, true);
}

// The field: the base, its hunks (fresh when they are of the text as it
// is), their marks, which move with the text until the hunks are worked
// out again, and the peek: open, and the index of its hunk.
const setHunks = cm.StateEffect.define();
const setPeek = cm.StateEffect.define();
const setMode = cm.StateEffect.define();
const hunkField = cm.StateField.define({
  create: () => ({ base: null, hunks: null, signs: cm.RangeSet.empty, fresh: true, open: false, at: -1, mode: null }),
  update(v, tr) {
    let n = v;
    if (tr.docChanged) n = { ...n, signs: n.signs.map(tr.changes), fresh: false };
    for (const e of tr.effects) {
      if (e.is(setHunks)) n = { ...n, base: e.value.base, hunks: e.value.hunks, signs: signsOf(tr.newDoc, e.value.hunks), fresh: true };
      if (e.is(setPeek)) n = { ...n, open: e.value };
      if (e.is(setMode)) n = { ...n, mode: e.value };
    }
    // A difference shows every hunk as the text is, so they are worked
    // out at once; it has no peek.
    if (n.mode) {
      if (!n.fresh && n.base != null) {
        const hunks = hunksOf(n.base, tr.newDoc);
        n = { ...n, hunks, signs: signsOf(tr.newDoc, hunks), fresh: true };
      }
      return n.open || n.at >= 0 ? { ...n, open: false, at: -1 } : n;
    }
    if (!n.open) return n.at < 0 ? n : { ...n, at: -1 };
    // An open peek follows the typing, so its hunks are worked out at once.
    if (!n.fresh && n.base != null) {
      const hunks = hunksOf(n.base, tr.newDoc);
      n = { ...n, hunks, signs: signsOf(tr.newDoc, hunks), fresh: true };
    }
    // It closes when the cursor leaves its hunk.
    const at = hunkAt(n.hunks, tr.newDoc.lineAt(tr.newSelection.main.head).number - 1);
    if (at < 0) return { ...n, open: false, at: -1 };
    return at === n.at ? n : { ...n, at };
  },
  provide: (f) => cm.EditorView.decorations.compute([f], (state) => {
    const v = state.field(f);
    if (v.mode === 'inline') return inlineOf(state, v);
    if (v.mode === 'side') return sideOf(state, v);
    return peekOf(state, v);
  }),
});

// How long typing pauses before the hunks are worked out again.
const settleMs = 150;

const refresh = cm.ViewPlugin.fromClass(class {
  constructor(view) { this.view = view; this.timer = 0; }
  update(u) {
    if (!u.docChanged || u.state.field(hunkField).base == null) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      const { base, fresh } = this.view.state.field(hunkField);
      if (!fresh) this.view.dispatch({ effects: setHunks.of({ base, hunks: hunksOf(base, this.view.state.doc) }) });
    }, settleMs);
  }
  destroy() { clearTimeout(this.timer); }
});

// ---- the peek ----

// The base's lines a hunk took out, the characters that differ marked,
// with (for a hunk that put no lines in) the footer below them.
class Removed extends cm.WidgetType {
  constructor(h, foot, whole) { super(); this.h = h; this.foot = foot; this.whole = !!whole; }
  eq(o) { return o.h.chunk === this.h.chunk && o.whole === this.whole && (o.foot && o.foot.key) === (this.foot && this.foot.key); }
  toDOM(view) {
    const box = document.createElement('div');
    box.className = 'cm-peek cm-peek-top' + (this.whole ? ' cm-diff-inline' : '');
    const old = document.createElement('div');
    old.className = 'cm-peek-old';
    const c = this.h.chunk;
    // The changes' ranges are from the chunk's start, as is the text; with
    // nothing put in, every character differs and none is marked.
    const marks = this.h.added ? c.changes.filter((ch) => ch.toA > ch.fromA) : [];
    let pos = 0;
    for (const text of this.h.removed) {
      const row = document.createElement('div');
      row.className = 'cm-peek-line';
      const end = pos + text.length;
      let at = pos;
      for (const ch of marks) {
        const from = Math.max(ch.fromA, pos), to = Math.min(ch.toA, end);
        if (to <= from) continue;
        if (from > at) row.append(text.slice(at - pos, from - pos));
        const s = document.createElement('span');
        s.className = 'cm-peek-gone';
        s.textContent = text.slice(from - pos, to - pos);
        row.append(s);
        at = to;
      }
      if (at < end) row.append(text.slice(at - pos));
      old.append(row);
      pos = end + 1;
    }
    box.append(old);
    if (this.foot) box.append(this.foot.toDOM(view));
    return box;
  }
  ignoreEvent() { return true; }
}

// The peek's footer: which change of how many, its counts, and undo.
class Foot extends cm.WidgetType {
  constructor(i, n, h) {
    super();
    this.text = 'change ' + (i + 1) + ' of ' + n + ' · ' + [h.added && '+' + h.added, h.removed.length && '−' + h.removed.length].filter(Boolean).join(' ');
    this.key = this.text + vimKey('vimUndoHunk') + vimKey('vimStageHunk');
  }
  eq(o) { return o.key === this.key; }
  toDOM(view) {
    const e = document.createElement('div');
    e.className = 'cm-peek-foot';
    e.append(this.text);
    const b = document.createElement('button');
    b.className = 'cm-peek-undo';
    b.title = 'put the lines back as the index has them; u brings the change back';
    b.textContent = 'undo ' + vimKey('vimUndoHunk');
    b.addEventListener('mousedown', (ev) => ev.preventDefault());
    b.addEventListener('click', () => { undoHunk(view, peekSay.get(view)); view.focus(); });
    e.append(b);
    if (stagers.get(view)) {
      const st = document.createElement('button');
      st.className = 'cm-peek-undo';
      st.title = 'stage this change: the index gets these lines';
      st.textContent = 'stage ' + vimKey('vimStageHunk');
      st.addEventListener('mousedown', (ev) => ev.preventDefault());
      st.addEventListener('click', () => { stageHunk(view, peekSay.get(view)); view.focus(); });
      e.append(st);
    }
    return e;
  }
  ignoreEvent() { return true; }
}
class FootBlock extends Foot {
  toDOM(view) {
    const box = document.createElement('div');
    box.className = 'cm-peek cm-peek-bottom';
    box.append(super.toDOM(view));
    return box;
  }
}

const addedLine = cm.Decoration.line({ class: 'cm-peek-added' });
const addedText = cm.Decoration.mark({ class: 'cm-peek-new' });

// peekOf is the open peek's decorations: the lines taken out in a block
// above the lines put in, those tinted, their differing characters
// marked, and the footer below.
function peekOf(state, v) {
  if (!v.open || v.at < 0 || !v.hunks || !v.hunks[v.at]) return cm.Decoration.none;
  const h = v.hunks[v.at], doc = state.doc;
  const ranges = [];
  if (!h.added) {
    // Taken-out lines show where they were, footer and all.
    const foot = new Foot(v.at, v.hunks.length, h);
    const w = cm.Decoration.widget({ widget: new Removed(h, foot), block: true, side: h.start ? 1 : -1 });
    ranges.push(w.range(h.start ? doc.line(Math.min(h.start, doc.lines)).to : 0));
    return cm.Decoration.set(ranges, true);
  }
  const first = doc.line(h.start + 1), last = doc.line(Math.min(h.start + h.added, doc.lines));
  if (h.removed.length) ranges.push(cm.Decoration.widget({ widget: new Removed(h, null), block: true, side: -1 }).range(first.from));
  for (let l = first.number; l <= last.number; l++) ranges.push(addedLine.range(doc.line(l).from));
  if (h.removed.length) {
    const c = h.chunk;
    for (const ch of c.changes) {
      const from = c.fromB + ch.fromB, to = Math.min(c.fromB + ch.toB, doc.length);
      if (to > from) ranges.push(addedText.range(from, to));
    }
  }
  ranges.push(cm.Decoration.widget({ widget: new FootBlock(v.at, v.hunks.length, h), block: true, side: 1 }).range(last.to));
  return cm.Decoration.set(ranges, true);
}

// What the peek's undo says, and what stages a hunk, by view.
const peekSay = new WeakMap();
const stagers = new WeakMap();

// gitHunks is the extension: the field, its gutter (a click on a mark
// opens its peek), keeping it current as the text changes, and escape
// closing the peek in normal mode. say tells the user what happened;
// stage(view, text, base), when given, makes text the index's version
// of the file, for gsh (see stageHunk).
export const gitHunks = (say, stage) => [
  hunkField,
  refresh,
  cm.ViewPlugin.define((view) => { peekSay.set(view, say); if (stage) stagers.set(view, stage); return {}; }),
  hunkWatch,
  cm.gutter({
    class: 'cm-hunk-gutter',
    markers: (v) => v.state.field(hunkField).signs,
    domEventHandlers: {
      mousedown(view, line) {
        const at = view.state.doc.lineAt(line.from).number - 1;
        if (hunkAt(view.state.field(hunkField).hunks, at) < 0) return false;
        view.dispatch({ selection: { anchor: line.from }, effects: setPeek.of(true) });
        view.focus();
        return true;
      },
    },
  }),
  cm.Prec.highest(cm.EditorView.domEventHandlers({
    keydown(e, view) {
      if (e.key !== 'Escape' || !view.state.field(hunkField).open) return false;
      const vim = cm.getCM(view)?.state.vim;
      if (!vim || (!vim.insertMode && !vim.visualMode)) view.dispatch({ effects: setPeek.of(false) });
      return false;
    },
  })),
];

// setBase compares the text with base (null: nothing to compare with)
// from now on.
export function setBase(view, base) {
  const b = base == null ? null : base;
  view.dispatch({ effects: setHunks.of({ base: b, hunks: hunksOf(b, view.state.doc) }) });
}

// baseOf is the base the text is compared with.
export const baseOf = (view) => view.state.field(hunkField).base;

// hunkUnder says whether the cursor's line is in a hunk: a change guh
// undoes and gsh stages.
export function hunkUnder(view) {
  const { base } = view.state.field(hunkField);
  if (base == null) return false;
  const doc = view.state.doc;
  return hunkAt(hunksOf(base, doc) || [], doc.lineAt(view.state.selection.main.head).number - 1) >= 0;
}

// undoHunk puts the lines of the hunk under the cursor back as the base
// has them, as gitsigns' reset_hunk does: one change, which u undoes.
// A hunk that took lines out is under the cursor on the line its notch
// is on.
export function undoHunk(view, say) {
  const { base } = view.state.field(hunkField);
  if (base == null) { if (say) say('no git changes: the file is not in git'); return; }
  const doc = view.state.doc;
  const at = doc.lineAt(view.state.selection.main.head).number - 1;
  const hunks = hunksOf(base, doc) || [];
  const h = hunks[hunkAt(hunks, at)];
  if (!h) { if (say) say('no git change under the cursor'); return; }
  const old = h.removed.join('\n');
  let change;
  if (!h.added) {
    // Taken-out lines go back before the line that followed them.
    change = h.start < doc.lines
      ? { from: doc.line(h.start + 1).from, insert: old + '\n' }
      : { from: doc.length, insert: '\n' + old };
  } else if (!h.removed.length) {
    // Added lines go, with their line ends.
    const last = h.start + h.added;
    change = last < doc.lines
      ? { from: doc.line(h.start + 1).from, to: doc.line(last + 1).from }
      : { from: h.start ? doc.line(h.start).to : 0, to: doc.length };
  } else {
    change = { from: doc.line(h.start + 1).from, to: doc.line(h.start + h.added).to, insert: old };
  }
  view.dispatch({ changes: change, selection: { anchor: change.from }, userEvent: 'input.undohunk', scrollIntoView: true });
  if (say) say('change undone: u brings it back');
}

// jumpToHunk goes to the next hunk after the cursor's line (dir 1) or
// the one before it (dir -1), wrapping around, and opens its peek.
export function jumpToHunk(view, dir, say) {
  const { base } = view.state.field(hunkField);
  if (base == null) { if (say) say('no git changes: the file is not in git'); return; }
  const list = hunksOf(base, view.state.doc) || [];
  if (!list.length) { if (say) say('no changes since the index'); return; }
  const doc = view.state.doc;
  const at = doc.lineAt(view.state.selection.main.head).number - 1;
  let i = dir > 0 ? list.findIndex((h) => lineOf(h) > at) : list.findLastIndex((h) => lineOf(h) < at);
  if (i < 0) i = dir > 0 ? 0 : list.length - 1;
  const pos = doc.line(Math.min(lineOf(list[i]) + 1, doc.lines)).from;
  view.dispatch({
    selection: { anchor: pos },
    effects: [setHunks.of({ base, hunks: list }), setPeek.of(true), cm.EditorView.scrollIntoView(pos, { y: 'center' })],
  });
}

// ---- the difference ----

// setDiffMode shows view's hunks whole: 'inline', 'side' (beside an
// oldSide editor), or null for the gutter and peek alone.
export function setDiffMode(view, mode) {
  view.dispatch({ effects: setMode.of(mode || null) });
}

// onHunks has f told the field ({base, hunks}) whenever its base or hunks
// change, and at once; null stops it.
const watchers = new WeakMap();
export function onHunks(view, f) {
  if (f) { watchers.set(view, f); f(view.state.field(hunkField)); } else watchers.delete(view);
}

const hunkWatch = cm.ViewPlugin.fromClass(class {
  constructor(view) { this.view = view; }
  update(u) {
    const a = u.startState.field(hunkField), b = u.state.field(hunkField);
    if (a.hunks === b.hunks && a.base === b.base) return;
    const f = watchers.get(this.view);
    if (f) queueMicrotask(() => f(this.view.state.field(hunkField)));
  }
});

// Spacer is a run of blank lines where the other side has more.
class Spacer extends cm.WidgetType {
  constructor(lines) { super(); this.lines = lines; }
  eq(o) { return o.lines === this.lines; }
  toDOM(view) {
    const e = document.createElement('div');
    e.className = 'cm-diff-spacer';
    e.style.height = this.lines * view.defaultLineHeight + 'px';
    return e;
  }
  ignoreEvent() { return true; }
}
const spacer = (lines, side) => cm.Decoration.widget({ widget: new Spacer(lines), block: true, side });

// inlineOf is every hunk as the peek shows one: the lines taken out above
// the lines put in, without the footer.
function inlineOf(state, v) {
  if (!v.hunks || !v.hunks.length) return cm.Decoration.none;
  const doc = state.doc, ranges = [];
  for (const h of v.hunks) {
    if (!h.added) {
      const w = cm.Decoration.widget({ widget: new Removed(h, null, true), block: true, side: h.start ? 1 : -1 });
      ranges.push(w.range(h.start ? doc.line(Math.min(h.start, doc.lines)).to : 0));
      continue;
    }
    newLines(doc, h, ranges);
    const first = doc.line(h.start + 1);
    if (h.removed.length) ranges.push(cm.Decoration.widget({ widget: new Removed(h, null, true), block: true, side: -1 }).range(first.from));
  }
  return cm.Decoration.set(ranges, true);
}

// newLines tints a hunk's lines put in, their differing characters marked.
function newLines(doc, h, ranges) {
  const first = doc.line(h.start + 1), last = doc.line(Math.min(h.start + h.added, doc.lines));
  for (let l = first.number; l <= last.number; l++) ranges.push(addedLine.range(doc.line(l).from));
  if (!h.removed.length) return last;
  const c = h.chunk;
  for (const ch of c.changes) {
    const from = c.fromB + ch.fromB, to = Math.min(c.fromB + ch.toB, doc.length);
    if (to > from) ranges.push(addedText.range(from, to));
  }
  return last;
}

// sideOf is the text's side of a side-by-side difference: the lines put
// in tinted, and blank lines where the base has more.
function sideOf(state, v) {
  if (!v.hunks || !v.hunks.length) return cm.Decoration.none;
  const doc = state.doc, ranges = [];
  for (const h of v.hunks) {
    const gone = h.removed.length;
    if (!h.added) {
      if (h.start < doc.lines) ranges.push(spacer(gone, -1).range(doc.line(h.start + 1).from));
      else ranges.push(spacer(gone, 1).range(doc.length));
      continue;
    }
    const last = newLines(doc, h, ranges);
    if (gone > h.added) ranges.push(spacer(gone - h.added, 1).range(last.to));
  }
  return cm.Decoration.set(ranges, true);
}

// ---- the base's side ----

// The read-only editor beside the text: the base, the lines taken out
// tinted red, their differing characters marked, and blank lines where
// the text has more. setOld gives it the text's hunks.
const setOldHunks = cm.StateEffect.define();
const oldField = cm.StateField.define({
  create: () => null,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setOldHunks)) return e.value;
    return tr.docChanged ? null : v;
  },
  provide: (f) => cm.EditorView.decorations.compute([f], (state) => oldOf(state.doc, state.field(f))),
});

const removedLine = cm.Decoration.line({ class: 'cm-diff-removed' });
const removedText = cm.Decoration.mark({ class: 'cm-peek-gone' });

function oldOf(doc, hunks) {
  if (!hunks || !hunks.length) return cm.Decoration.none;
  const ranges = [];
  for (const h of hunks) {
    const c = h.chunk, gone = h.removed.length;
    if (!gone) {
      // Before the base's line the lines were put in front of, or past
      // its end.
      if (c.fromA <= doc.length && doc.lineAt(c.fromA).from === c.fromA) ranges.push(spacer(h.added, -1).range(c.fromA));
      else ranges.push(spacer(h.added, 1).range(doc.length));
      continue;
    }
    const first = doc.lineAt(Math.min(c.fromA, doc.length)).number;
    const last = doc.line(Math.min(first + gone - 1, doc.lines));
    for (let l = first; l <= last.number; l++) ranges.push(removedLine.range(doc.line(l).from));
    if (h.added) {
      for (const ch of c.changes) {
        const from = c.fromA + ch.fromA, to = Math.min(c.fromA + ch.toA, doc.length);
        if (to > from) ranges.push(removedText.range(from, to));
      }
    }
    if (h.added > gone) ranges.push(spacer(h.added - gone, 1).range(last.to));
  }
  return cm.Decoration.set(ranges, true);
}

// oldSide is the base editor's extension.
export const oldSide = () => [oldField];

// setOld shows hunks (the text's, of this editor's text) in a base editor.
export function setOld(view, hunks) {
  view.dispatch({ effects: setOldHunks.of(hunks || null) });
}

// ---- staging a hunk ----

// stagedText is base with hunk h of doc put in: the index's version once
// h is staged.
export function stagedText(base, doc, h) {
  const a = textOf(base);
  const c = h.chunk;
  const from = Math.min(c.fromA, a.length), to = Math.min(c.toA, a.length);
  const put = doc.sliceString(Math.min(c.fromB, doc.length), Math.min(c.toB, doc.length));
  return a.sliceString(0, from) + put + a.sliceString(to);
}

// unstagedText is doc with hunk h's lines put back as base has them: the
// index's version once h, staged, is unstaged.
export function unstagedText(base, doc, h) {
  const a = textOf(base);
  const c = h.chunk;
  const old = a.sliceString(Math.min(c.fromA, a.length), Math.min(c.toA, a.length));
  return doc.sliceString(0, Math.min(c.fromB, doc.length)) + old + doc.sliceString(Math.min(c.toB, doc.length));
}

// stageHunk stages the hunk under the cursor, as gitsigns' stage_hunk
// does: the index's version becomes the base with its lines put in, the
// text's unsaved changes included. In a staged file's difference, where
// the text is the index and the base HEAD, the stager unstages it.
export function stageHunk(view, say) {
  const stage = stagers.get(view);
  const { base } = view.state.field(hunkField);
  if (!stage || base == null) { if (say) say('no git changes here to stage'); return; }
  const doc = view.state.doc;
  const at = doc.lineAt(view.state.selection.main.head).number - 1;
  const hunks = hunksOf(base, doc) || [];
  const h = hunks[hunkAt(hunks, at)];
  if (!h) { if (say) say('no git change under the cursor'); return; }
  stage(view, stagedText(base, doc, h), base, h);
}
