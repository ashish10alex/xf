// Inline blame in the source pane, as GitLens's current line blame: after
// the cursor's line, faint, who last changed it, how long ago, and the
// commit's subject; a hover gives the commit and its date. The palette's
// "View: inline git blame" turns it on and off (source-editor.js, which
// loads this module once it is on: it isn't in the first load).
//
// The blame is git's of the editor's text (POST /api/source/blame), asked
// for as a file loads (open, reload, a compile, a commit) and as it is
// turned on. Edits made since move the lines it names, and the lines
// edited say "Uncommitted changes" at once, without asking git again.

import * as cm from './vendor/codemirror.mjs';
import { post } from './state.js';

// The field is {data, doc, since, asked}: data git's answer, for doc, the
// text it was asked about; since the edits made after that; asked, while
// an answer is on its way, the text sent and the edits made since.
export const setBlame = cm.StateEffect.define();
export const askBlame = cm.StateEffect.define();
const none = { data: null, doc: null, since: null, asked: null };
const blameField = cm.StateField.define({
  create: () => none,
  update(v, tr) {
    if (tr.docChanged && (v.since || v.asked)) {
      v = { ...v, since: v.since && v.since.compose(tr.changes), asked: v.asked && { doc: v.asked.doc, since: v.asked.since.compose(tr.changes) } };
    }
    for (const e of tr.effects) {
      if (e.is(askBlame)) v = { ...v, asked: { doc: tr.state.doc, since: cm.ChangeSet.empty(tr.state.doc.length) } };
      if (e.is(setBlame)) v = e.value && v.asked ? { data: e.value, doc: v.asked.doc, since: v.asked.since, asked: null } : none;
    }
    return v;
  },
});

// touched says whether the edits in since went inside line, a line of the
// text now. An edit at its edge (a line put in or taken out above or
// below) doesn't count: blameAt compares the line's text for those.
function touched(since, line) {
  let hit = false;
  since.iterChangedRanges((_fromA, _toA, fromB, toB) => {
    if (fromB === toB ? fromB > line.from && fromB < line.to : fromB < line.to && toB > line.from) hit = true;
  });
  return hit;
}

// blameAt is what the blame says of line (a line of state's doc): its
// commit {sha, author, mail, time, summary, you}, {fresh: true} for a
// line not committed, or null when there is no answer for it.
export function blameAt(state, line) {
  const b = state.field(blameField, false);
  if (!b || !b.data) return null;
  if (touched(b.since, line)) return { fresh: true };
  const was = b.doc.lineAt(b.since.invertedDesc.mapPos(line.from, 1));
  if (was.text !== line.text) return { fresh: true };
  const old = was.number - 1;
  if (old >= b.data.lines.length) return null;
  const sha = b.data.lines[old];
  if (!sha) return { fresh: true };
  const c = b.data.commits[sha];
  return c ? { sha, ...c, you: !!b.data.you && c.mail === b.data.you } : null;
}

// ago is how long ago secs (Unix seconds) was, as GitLens says it.
export function ago(secs, now = Date.now()) {
  const s = Math.max(0, Math.round(now / 1000 - secs));
  const units = [[365 * 86400, 'year'], [30 * 86400, 'month'], [7 * 86400, 'week'], [86400, 'day'], [3600, 'hour'], [60, 'minute']];
  for (const [n, name] of units) {
    const k = Math.floor(s / n);
    if (k >= 1) return k + ' ' + name + (k > 1 ? 's' : '') + ' ago';
  }
  return 'just now';
}

// blameText is the annotation of what blameAt said, and its hover.
export function blameText(c, now = Date.now()) {
  if (c.fresh) return { text: 'You, Uncommitted changes', title: 'Not committed yet' };
  const who = c.you ? 'You' : c.author;
  const when = new Date(c.time * 1000);
  return {
    text: `${who}, ${ago(c.time, now)} • ${c.summary}`,
    title: `${c.sha.slice(0, 8)} · ${c.author}${c.mail ? ' <' + c.mail + '>' : ''}\n${when.toLocaleString()}\n\n${c.summary}`,
  };
}

class BlameWidget extends cm.WidgetType {
  constructor(text, title) { super(); this.text = text; this.title = title; }
  eq(o) { return o.text === this.text && o.title === this.title; }
  toDOM() {
    const s = document.createElement('span');
    s.className = 'cm-blame';
    s.textContent = this.text;
    s.title = this.title;
    return s;
  }
  ignoreEvent() { return false; }
}

// The annotation goes after the main cursor's line, while nothing is
// selected across lines.
function decorationsOf(state) {
  const sel = state.selection.main;
  const line = state.doc.lineAt(sel.head);
  if (!sel.empty && state.doc.lineAt(sel.anchor).number !== line.number) return cm.Decoration.none;
  const c = blameAt(state, line);
  if (!c) return cm.Decoration.none;
  const { text, title } = blameText(c);
  return cm.Decoration.set([cm.Decoration.widget({ widget: new BlameWidget(text, title), side: 1 }).range(line.to)]);
}

const blameDecorations = cm.StateField.define({
  create: (state) => decorationsOf(state),
  update: (deco, tr) => (tr.docChanged || tr.selection || tr.effects.some((e) => e.is(setBlame)) ? decorationsOf(tr.state) : deco),
  provide: (f) => cm.EditorView.decorations.from(f),
});

// inlineBlame is the source editor's extension.
export const inlineBlame = () => [blameField, blameDecorations];

// ---- asking git ----

let seq = 0;
let blamed = ''; // the file the blame shown is of

// refreshBlame asks for file's blame as view has it, when on and the file
// is in git (base, the file's index version, is '' for a file git doesn't
// track and null outside git); else it clears what is shown.
export function refreshBlame(view, file, base, on) {
  const n = ++seq;
  if (!on || !file || base == null || base === '') {
    blamed = '';
    if (view.state.field(blameField).data) view.dispatch({ effects: setBlame.of(null) });
    return;
  }
  // Another file's blame goes at once; the same file's stays until the
  // answer, so a reload doesn't flicker.
  view.dispatch({ effects: file === blamed ? askBlame.of(null) : [setBlame.of(null), askBlame.of(null)] });
  blamed = file;
  post('/api/source/blame', { file, text: view.state.doc.toString() }).then(
    (b) => { if (n === seq) view.dispatch({ effects: setBlame.of(b) }); },
    () => { if (n === seq) view.dispatch({ effects: setBlame.of(null) }); },
  );
}
