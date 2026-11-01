// File names in the source pane's text, as vim reads them:
//
//   - gf opens the file named under the cursor (or after it on the line),
//     gF at the line written after the name (file.sqlx:12). The name is
//     looked for beside the file shown, then from the project's root, as
//     written and then with .js, .sqlx… added, as vim's path=.,, and
//     suffixesadd do (POST /api/source/findfile); ctrl+o comes back.
//   - ctrl+x ctrl+f in insert mode completes a file name, as vim's
//     i_CTRL-X_CTRL-F: the names in the folder typed so far (from the
//     root, or from the file's folder after ./ or ../), the other
//     completions put aside until it closes. Picking a folder goes on into
//     it (POST /api/source/paths).

import * as cm from './vendor/codemirror.mjs';
import { S, post, redraw } from './state.js';
import { SRC, goToPlace } from './source.js';
import { jumpWithin } from './editor.js';

function say(msg) { S.flash = msg || ''; redraw(); }

// A file name's characters, as vim's isfname reads them, but for the ones
// that part words of code ($ , # % =).
const nameChar = /[\p{L}\p{N}_.\/~+-]/u;
const nameBefore = /[\p{L}\p{N}_.\/~+-]*$/u;

// nameAt is the file name in text at col, or the first after it:
// {name, from, to, line, col}, line (1-based) and col (0-based) what is
// written after the name (file:12, file:12:3, file(12), file @ 12), or
// null with no name.
export function nameAt(text, col) {
  let from = Math.min(col, text.length);
  if (!nameChar.test(text.charAt(from))) {
    while (from < text.length && !nameChar.test(text.charAt(from))) from++;
    if (from >= text.length) return null;
  }
  while (from > 0 && nameChar.test(text.charAt(from - 1))) from--;
  let to = from;
  while (to < text.length && nameChar.test(text.charAt(to))) to++;
  // A sentence's full stop isn't the name's.
  let name = text.slice(from, to);
  if (/[^.]\.+$/.test(name)) name = name.replace(/\.+$/, '');
  to = from + name.length;
  const rest = text.slice(to);
  const m = /^:(\d+)(?::(\d+))?/.exec(rest) || /^\((\d+)\)/.exec(rest) || /^ ?@ ?(\d+)/.exec(rest);
  return { name, from, to, line: m ? Number(m[1]) : 0, col: m && m[2] ? Number(m[2]) - 1 : 0 };
}

// splitTyped is a file name being typed as {dir, base}: the folder up to
// its last /, and the name in it so far.
export function splitTyped(typed) {
  const i = typed.lastIndexOf('/');
  return { dir: typed.slice(0, i + 1), base: typed.slice(i + 1) };
}

// pathOptions are the entries ({name, dir}) whose names begin with base,
// folders ending in /; names starting with a dot only when base does, as
// a glob of base* finds them.
export function pathOptions(entries, base) {
  const low = base.toLowerCase();
  const hidden = base.startsWith('.');
  return entries
    .filter((e) => (hidden || !e.name.startsWith('.')) && e.name.toLowerCase().startsWith(low))
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

// ---- gf ----

// goToFile is gf (withLine false) and gF in the editor view.
export async function goToFile(view, withLine) {
  const head = view.state.selection.main.head;
  const line = view.state.doc.lineAt(head);
  const at = nameAt(line.text, head - line.from);
  if (!at || !at.name) { say('no file name under the cursor'); return; }
  const file = SRC.file;
  let res;
  try { res = await post('/api/source/findfile', { file, name: at.name }); } catch (e) { say(e.message); return; }
  if (file !== SRC.file) return;
  if (!res.file) { say(res.folder ? res.folder + ' is a folder' : "can't find " + at.name + ' in the project'); return; }
  const to = withLine && at.line ? { line: at.line - 1, col: at.col } : null;
  if (res.file === file) {
    if (!to) { say('already in ' + file); return; }
    const doc = view.state.doc;
    const l = doc.line(Math.min(to.line + 1, doc.lines));
    jumpWithin(view, l.from + Math.min(to.col, l.length));
    return;
  }
  goToPlace({ file: res.file, action: res.action || '', line: to ? to.line : 0, col: to ? to.col : 0, last: !to });
}

// ---- ctrl+x ctrl+f ----

// pathMode is on from ctrl+x ctrl+f until the completions close: the
// completions are then file names alone.
const setPathMode = cm.StateEffect.define();
const pathMode = cm.StateField.define({
  create: () => false,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setPathMode)) v = e.value;
    return v;
  },
});
const inPathMode = (state) => state.field(pathMode, false) === true;

// completeFileName is ctrl+x ctrl+f.
export function completeFileName(view) {
  view.dispatch({ effects: setPathMode.of(true) });
  cm.startCompletion(view);
}

// unlessPaths keeps completion source src quiet while file names are
// completed.
export const unlessPaths = (src) => (context) => (inPathMode(context.state) ? null : src(context));

// pathSource completes file names while pathMode is on.
export async function pathSource(context) {
  if (!inPathMode(context.state)) return null;
  const tok = context.matchBefore(nameBefore);
  const typed = tok ? tok.text : '';
  // A key that ends the name (a space, a quote) ends the completions.
  if (!typed && !context.explicit) return null;
  const { dir, base } = splitTyped(typed);
  let res;
  try { res = await post('/api/source/paths', { file: SRC.file, dir }); } catch { return null; }
  if (context.aborted) return null;
  const entries = pathOptions(res.entries || [], base);
  if (!entries.length) { if (context.explicit) say('no file names match ' + (typed || 'here')); return null; }
  const options = entries.map((e) => (e.dir
    ? { label: e.name + '/', type: 'folder', apply: intoFolder(e.name + '/') }
    : { label: e.name, type: 'file' }));
  return { from: context.pos - base.length, options, filter: false };
}

// intoFolder puts a folder's name in and goes on completing in it.
function intoFolder(name) {
  return (view, _c, from, to) => {
    view.dispatch({ changes: { from, to, insert: name }, selection: { anchor: from + name.length }, effects: setPathMode.of(true), userEvent: 'input.complete' });
    cm.startCompletion(view);
  };
}

// pathExtensions are the source editor's: pathMode, which ends as the
// completions close.
export const pathExtensions = () => [
  pathMode,
  cm.EditorView.updateListener.of((u) => {
    if (!inPathMode(u.state) || cm.completionStatus(u.state)) return;
    if (u.transactions.some((tr) => tr.effects.some((e) => e.is(setPathMode)))) return;
    setTimeout(() => {
      if (!u.view.destroyed && inPathMode(u.view.state) && !cm.completionStatus(u.view.state)) u.view.dispatch({ effects: setPathMode.of(false) });
    }, 0);
  }),
];
