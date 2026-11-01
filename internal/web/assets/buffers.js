// The source pane's buffers, as vim's: every file the pane has shown, in
// the order it first showed them, each with a number it keeps while it is
// listed. <leader>] and <leader>[ (:bn, :bp) step through them, wrapping
// at the ends; <leader>b (:ls) lists them to pick one; :b N or :b name
// goes to one, and :bd takes one out of the list. Going to a buffer saves
// unsaved changes first, and stays put when the save fails (a conflict
// opens, as :w's does). It selects the action the buffer last showed, and
// adds nothing to the jump list; ctrl+^ comes back. A buffer whose file
// is gone is dropped as a step or the list reaches it.
//
// The list is the project's: the server keeps it while the project is open
// (POST /api/buffers), so a reload has it, and another tab of the project
// too. As vim's, it starts empty each time xf opens the project, so files
// opened one after the other are next to each other in it.

import { S, post, redraw, hooks, say } from './state.js';
import { SRC, save, openBuffer, altFileName } from './source.js';
import { vimKey } from './editor.js';

const maxBuffers = 200; // as the server keeps

// BUF is the list of the project at root: {n, file, action}, action ''
// for a file shown without one; next is the number the next buffer gets.
export const BUF = { root: null, list: [], next: 1 };

// shown are the files the pane has shown since the page loaded, the last
// first.
let shown = [];

// own takes up the list of the project shown, once it is another.
function own() {
  const root = (S.boot && S.boot.root) || '';
  if (BUF.root === root) return;
  BUF.root = root;
  shown = [];
  const b = S.boot && S.boot.resume && S.boot.resume.buffers;
  BUF.list = b && Array.isArray(b.list) ? b.list.filter((x) => x && x.file).map((x) => ({ n: x.n, file: x.file, action: x.action || '' })) : [];
  BUF.next = Math.max((b && b.next) || 1, ...BUF.list.map((x) => x.n + 1));
  BP.actions = {};
}

function keep() {
  const v = { list: BUF.list.map((b) => ({ ...b })), next: BUF.next };
  // The boot is what the tab has of the project, should it show it again.
  if (S.boot && S.boot.resume) S.boot.resume.buffers = v;
  post('/api/buffers', v).catch(() => {});
}

// noteBuffer adds file to the list as the pane shows it, with action the
// one it shows it for.
export function noteBuffer(file, action) {
  if (!file) return;
  own();
  shown = [file, ...shown.filter((f) => f !== file)].slice(0, maxBuffers);
  const b = BUF.list.find((x) => x.file === file);
  if (b) { noteAction(file, action); return; }
  BUF.list.push({ n: BUF.next++, file, action: action || '' });
  if (BUF.list.length > maxBuffers) BUF.list.shift();
  keep();
}

// recentFiles are the buffers' files, those shown since the page loaded
// first, the last shown first, then the rest, the last listed first. The
// palette lists them first (docs/adr/0014).
export function recentFiles() {
  own();
  const listed = BUF.list.map((b) => b.file);
  return [...shown.filter((f) => listed.includes(f)), ...listed.reverse().filter((f) => !shown.includes(f))];
}
hooks.recentFiles = recentFiles;

// noteAction keeps action as the one buffer file last showed.
export function noteAction(file, action) {
  if (!action) return;
  own();
  const b = BUF.list.find((x) => x.file === file);
  if (b && b.action !== action) { b.action = action; keep(); }
}

// The file action id was last shown in, so a view of id that an edit
// renamed finds the action its file defines now (state.js).
hooks.bufferFile = (id) => {
  own();
  const b = BUF.list.find((x) => x.action === id);
  return b ? b.file : '';
};

function drop(b) {
  BUF.list = BUF.list.filter((x) => x !== b);
  if (!BUF.list.length) BUF.next = 1;
  keep();
}

// label is how a message names buffer b.
function label(b) {
  const i = BUF.list.indexOf(b);
  return 'buffer ' + b.n + ' · ' + b.file + (i >= 0 ? ' (' + (i + 1) + ' of ' + BUF.list.length + ')' : '');
}

// filesNow asks the server how files are now ({file, gone, actions}).
async function filesNow(files) {
  const v = await post('/api/buffers/files', { files });
  return v.files || [];
}

// goToBuffer shows buffer b, saving the file shown first: 'ok', 'gone'
// (b is dropped) or 'stay' (a failed save, which says why).
async function goToBuffer(b, gone = []) {
  const prefix = gone.length ? gone.join(', ') + (gone.length > 1 ? ' are' : ' is') + ' gone · ' : '';
  if (b.file === SRC.file) { say(prefix + label(b)); return 'ok'; }
  let f;
  try { [f] = await filesNow([b.file]); } catch (e) { say(e.message); return 'stay'; }
  if (!f || f.gone) { drop(b); return 'gone'; }
  if (SRC.dirty && !(await save())) return 'stay';
  const ids = f.actions.map((a) => a.id);
  BP.actions[b.file] = f.actions;
  if (!openBuffer(b.file, ids.includes(b.action) ? b.action : ids[0] || '')) return 'stay';
  say(prefix + label(b));
  return 'ok';
}

// stepBuffer is :bn (dir 1) and :bp (dir -1), wrapping at the ends; the
// file shown when it isn't a buffer (:bd took it out) is before the first.
export async function stepBuffer(dir) {
  own();
  const gone = [];
  while (BUF.list.length) {
    const l = BUF.list;
    const i = l.findIndex((x) => x.file === SRC.file);
    if (i >= 0 && l.length === 1) { say((gone.length ? gone.join(', ') + ' gone · ' : '') + 'only one buffer: ' + l[0].file); return; }
    const b = l[i < 0 ? (dir > 0 ? 0 : l.length - 1) : (i + dir + l.length) % l.length];
    if ((await goToBuffer(b, gone)) !== 'gone') return;
    gone.push(b.file);
  }
  say((gone.length ? gone.join(', ') + ' gone · ' : '') + 'no buffers');
}

// bufferArg is the buffer :b and :bd name: a number, or a part of a
// file's path that only one buffer has (a whole file name wins).
function bufferArg(arg) {
  own();
  const a = (arg || '').trim();
  if (/^\d+$/.test(a)) {
    const b = BUF.list.find((x) => x.n === Number(a));
    return b || 'no buffer ' + a;
  }
  const low = a.toLowerCase();
  const m = BUF.list.filter((x) => x.file.toLowerCase().includes(low));
  if (m.length === 1) return m[0];
  const whole = m.filter((x) => x.file.toLowerCase() === low || x.file.toLowerCase().endsWith('/' + low));
  if (whole.length === 1) return whole[0];
  return m.length ? 'more than one buffer matches ' + a + ': ' + m.map((x) => x.n).join(', ') : 'no buffer matches ' + a;
}

// bufferCommand is :b [N | name]: alone, it names the buffer shown.
export async function bufferCommand(arg) {
  if (!(arg || '').trim()) {
    own();
    const b = BUF.list.find((x) => x.file === SRC.file);
    say(b ? label(b) : 'no buffer shown');
    return;
  }
  const b = bufferArg(arg);
  if (typeof b === 'string') { say(b); return; }
  if ((await goToBuffer(b)) === 'gone') say(b.file + ' is gone');
}

// deleteCommand is :bd [N | name]: the buffer shown when none is named.
export function deleteCommand(arg) {
  own();
  const b = (arg || '').trim() ? bufferArg(arg) : BUF.list.find((x) => x.file === SRC.file);
  if (!b) { say('no buffer shown'); return; }
  if (typeof b === 'string') { say(b); return; }
  deleteBuffer(b);
}

// deleteBuffer takes b out of the list. For the file shown, the pane
// first goes to the alternate file's buffer, else the one before it (the
// one after, for the first); with no other, the file stays shown.
async function deleteBuffer(b) {
  if (b.file !== SRC.file) { drop(b); say('removed buffer ' + b.n + ' · ' + b.file); return; }
  const gone = [];
  for (;;) {
    const others = BUF.list.filter((x) => x !== b);
    if (!others.length) { drop(b); say('removed buffer ' + b.n + ' · ' + b.file + ': no other buffers'); return; }
    const i = BUF.list.indexOf(b);
    const t = others.find((x) => x.file === altFileName()) || BUF.list[i - 1] || BUF.list[i + 1];
    const r = await goToBuffer(t, gone);
    if (r === 'stay') return;
    if (r === 'ok') { drop(b); say('removed buffer ' + b.n + ' · ' + label(t)); return; }
    gone.push(t.file);
  }
}

// ---- the list (<leader>b, :ls) ----

// BP is the list's overlay: what is typed to narrow it, the row the
// cursor is on, and each file's actions as the server last said.
export const BP = { query: '', cursor: 0, actions: {}, loading: false };
let listSeq = 0;

export async function openBuffers() {
  own();
  if (!BUF.list.length) { say('no buffers'); return; }
  BP.query = '';
  BP.cursor = Math.max(0, BUF.list.findIndex((x) => x.file === SRC.file));
  S.overlay = 'buffers';
  BP.loading = true;
  redraw();
  const seq = ++listSeq;
  try {
    const files = await filesNow(BUF.list.map((x) => x.file));
    if (seq !== listSeq) return;
    const gone = files.filter((f) => f.gone).map((f) => f.file);
    for (const f of files) BP.actions[f.file] = f.actions;
    if (gone.length) {
      const cur = bufferRows()[BP.cursor];
      BUF.list = BUF.list.filter((x) => !gone.includes(x.file));
      if (!BUF.list.length) BUF.next = 1;
      keep();
      BP.cursor = Math.max(0, bufferRows().findIndex((r) => cur && r.n === cur.n));
      say(gone.join(', ') + (gone.length > 1 ? ' are' : ' is') + ' gone');
    }
  } catch (e) {
    if (seq === listSeq) say(e.message);
  }
  BP.loading = false;
  redraw();
}

// bufferRows are the list's rows as the query narrows them: every word
// typed is in the buffer's number, path or action names.
export function bufferRows() {
  const words = BP.query.toLowerCase().split(/\s+/).filter(Boolean);
  const alt = altFileName();
  return BUF.list.map((b) => ({ ...b, cur: b.file === SRC.file, alt: b.file === alt, names: (BP.actions[b.file] || []).map((a) => a.name) }))
    .filter((r) => { const hay = (r.n + ' ' + r.file + ' ' + r.names.join(' ')).toLowerCase(); return words.every((w) => hay.includes(w)); });
}

export function setBuffersQuery(q) { BP.query = q; BP.cursor = 0; redraw(); }

// moveBuffers moves the cursor n rows, wrapping past either end so
// ctrl+n and ctrl+p keep cycling.
export function moveBuffers(n) {
  const len = bufferRows().length;
  BP.cursor = len ? (((BP.cursor + n) % len) + len) % len : 0;
  redraw();
}

// closeBuffers shuts the list, the keys back to the editor in normal
// mode.
export function closeBuffers() {
  listSeq++;
  if (S.overlay === 'buffers') S.overlay = null;
  if (hooks.sourceReturn) hooks.sourceReturn();
  redraw();
}

// pickBuffer goes to row i's buffer.
export function pickBuffer(i = BP.cursor) {
  const r = bufferRows()[i];
  if (!r) return;
  const b = BUF.list.find((x) => x.n === r.n);
  closeBuffers();
  if (b) goToBuffer(b).then((res) => { if (res === 'gone') say(b.file + ' is gone'); });
}

// removeRow is del on row i: its buffer leaves the list, as :bd.
export async function removeRow(i = BP.cursor) {
  const r = bufferRows()[i];
  const b = r && BUF.list.find((x) => x.n === r.n);
  if (!b) return;
  await deleteBuffer(b);
  if (!BUF.list.length) { closeBuffers(); return; }
  BP.cursor = Math.min(BP.cursor, Math.max(bufferRows().length - 1, 0));
  redraw();
}

hooks.openBuffers = openBuffers;

// bufferCommands are the palette's: the list, with how many it has.
hooks.bufferCommands = () => (BUF.list.length
  ? [['Buffers: show open buffers', vimKey('vimBuffers'), openBuffers, BUF.list.length + ' open']]
  : []);
