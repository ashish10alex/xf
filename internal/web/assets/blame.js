// Inline blame in the source pane, as GitLens's current line blame: after
// the cursor's line, faint, who last changed it, how long ago, and the
// commit's subject. A hover shows a card with the commit, its date and,
// from GitLab or GitHub, links to its page and to the merge request that
// brought it in. The palette's
// "View: inline git blame" turns it on and off (source-editor.js, which
// loads this module once it is on: it isn't in the first load).
//
// The blame is git's of the editor's text (POST /api/source/blame), asked
// for as a file loads (open, reload, a compile, a commit) and as it is
// turned on. Edits made since move the lines it names, and the lines
// edited say "Uncommitted changes" at once, without asking git again.

import * as cm from './vendor/codemirror.mjs';
import { get, post, openLink } from './state.js';

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

// blameText is the annotation of what blameAt said.
export function blameText(c, now = Date.now()) {
  if (c.fresh) return { text: 'You, Uncommitted changes' };
  return { text: `${c.you ? 'You' : c.author}, ${ago(c.time, now)} • ${c.summary}` };
}

class BlameWidget extends cm.WidgetType {
  constructor(c) { super(); this.c = c; this.text = blameText(c).text; }
  eq(o) { return o.text === this.text && o.c.sha === this.c.sha; }
  toDOM() {
    const s = document.createElement('span');
    s.className = 'cm-blame';
    s.textContent = this.text;
    s.addEventListener('mouseenter', () => showCard(s, this.c));
    s.addEventListener('mouseleave', () => hideSoon());
    return s;
  }
  destroy(dom) { if (card && card.anchor === dom) hideCard(); }
  ignoreEvent() { return false; }
}

// ---- the hover card ----

// The card shows the commit as git has it at once; its page on the host
// and the merge request that brought it in come from the host (GET
// /api/forge/commit, which keeps the answers), when xf knows the host.
let card = null; // {el, anchor}
let hideTimer = 0;
const commits = new Map(); // sha → the host's answer, or its promise

function hideCard() {
  clearTimeout(hideTimer);
  if (card) { card.el.remove(); card = null; }
}

// A scroll anywhere, or a key, takes the card away: it is placed where
// the annotation was.
window.addEventListener('scroll', () => { if (card) hideCard(); }, true);
window.addEventListener('keydown', () => { if (card) hideCard(); }, true);

// hideSoon hides the card unless the pointer reaches it first.
function hideSoon() {
  clearTimeout(hideTimer);
  hideTimer = setTimeout(hideCard, 250);
}

const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
};

const link = (text, url, cls) => {
  const a = el('a', cls, text);
  a.href = url;
  a.addEventListener('click', (e) => { e.preventDefault(); openLink(url); hideCard(); });
  return a;
};

function showCard(anchor, c) {
  clearTimeout(hideTimer);
  if (card && card.anchor === anchor) return;
  hideCard();
  const box = el('div', 'blame-card');
  box.addEventListener('mouseenter', () => clearTimeout(hideTimer));
  box.addEventListener('mouseleave', () => hideSoon());
  if (c.fresh) {
    box.append(el('div', 'blame-card-msg', 'Not committed yet'), el('div', 'dim', 'This line changed since the last commit.'));
  } else {
    const head = el('div', 'blame-card-head');
    head.append(el('b', '', c.you ? c.author + ' (you)' : c.author));
    if (c.mail) head.append(' ', el('span', 'dim', c.mail));
    head.append(el('span', 'dim', ' · ' + ago(c.time) + ' · ' + new Date(c.time * 1000).toLocaleString()));
    const sha = el('div', 'blame-card-row');
    sha.append(el('span', 'dim', 'commit '), el('span', 'blame-card-sha', c.sha.slice(0, 8)));
    const mr = el('div', 'blame-card-row dim', 'looking for its merge request…');
    box.append(head, el('div', 'blame-card-msg', c.summary), sha, mr);
    hostFor(c.sha).then((v) => fillHost(v, c.sha, sha, mr), (e) => { mr.textContent = e.message; });
  }
  document.body.append(box);
  card = { el: box, anchor };
  place(box, anchor);
}

// place puts the card under the annotation, or above it near the
// window's bottom, inside the window.
function place(box, anchor) {
  const r = anchor.getBoundingClientRect();
  const w = box.offsetWidth, h = box.offsetHeight;
  const left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8));
  const top = r.bottom + 4 + h > window.innerHeight - 8 ? Math.max(8, r.top - 4 - h) : r.bottom + 4;
  box.style.left = left + 'px';
  box.style.top = top + 'px';
}

function hostFor(sha) {
  let v = commits.get(sha);
  if (!v) {
    v = get('/api/forge/commit?sha=' + sha);
    commits.set(sha, v);
    // A failed answer isn't kept: the next hover asks again.
    v.then((a) => { if (a.state !== 'ok') commits.delete(sha); }, () => commits.delete(sha));
  }
  return v;
}

// fillHost fills the card's rows with the host's answer v.
function fillHost(v, sha, shaRow, mrRow) {
  if (!card || !mrRow.isConnected) return;
  const word = v.kind === 'github' ? 'pull request' : 'merge request';
  if (v.url) {
    shaRow.replaceChildren(el('span', 'dim', 'commit '), link(sha.slice(0, 8), v.url, 'blame-card-sha'));
  }
  mrRow.className = 'blame-card-row';
  mrRow.replaceChildren();
  switch (v.state) {
    case 'ok':
      if (v.mr) {
        const m = v.mr;
        mrRow.append(link((v.kind === 'github' ? '#' : '!') + m.number + ' ' + m.title, m.url, 'blame-card-mr'), ' ', el('span', 'blame-card-state ' + m.state, m.draft ? 'draft' : m.state));
      } else mrRow.append(el('span', 'dim', 'no ' + word + ': pushed straight to its branch, or not pushed yet'));
      break;
    case 'connect':
      mrRow.append(el('span', 'dim', 'add a token for ' + (v.kind === 'github' ? 'GitHub' : 'GitLab') + ' in Settings › Git hosting to see its ' + word));
      break;
    case 'token':
      mrRow.append(el('span', 'dim', 'the host refused the token: renew it in Settings › Git hosting'));
      break;
    case 'off':
      mrRow.append(el('span', 'dim', 'no GitLab or GitHub remote xf knows'));
      break;
    default:
      mrRow.append(el('span', 'dim', v.error || "couldn't ask the host"));
  }
  if (card) place(card.el, card.anchor);
}

// The annotation goes after the main cursor's line, while nothing is
// selected across lines.
function decorationsOf(state) {
  const sel = state.selection.main;
  const line = state.doc.lineAt(sel.head);
  if (!sel.empty && state.doc.lineAt(sel.anchor).number !== line.number) return cm.Decoration.none;
  const c = blameAt(state, line);
  if (!c) return cm.Decoration.none;
  return cm.Decoration.set([cm.Decoration.widget({ widget: new BlameWidget(c), side: 1 }).range(line.to)]);
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
