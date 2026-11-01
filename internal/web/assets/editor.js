// xf web's code editor: CodeMirror 6 with vim keys (vendor/codemirror.mjs,
// built by scripts/vendor-codemirror), shared by the Scratch view and the
// source pane. It owns every key while it has focus; ctrl+w (or :q) gives
// focus back to the page. What a view adds (its language, completions, run
// keys, what :w does) it passes to createEditor.

import * as cm from './vendor/codemirror.mjs';
import { toggleSelection, toggleRange } from './comment.js';
import { undoesWith, vimOn as vimSetting, noteVimMode } from './vim-mode.js';

const { EditorView, EditorState, keymap, Prec, tags } = cm;

// historyField is the undo history's state field, which the vendored
// bundle doesn't export by name: history() is [field, config, handlers].
const historyField = cm.history()[0];
const savedFields = { history: historyField };

// ---- BigQuery's SQL ----

// The BigQuery dialect: GoogleSQL's backtick-quoted names, and # comments.
// Its functions are colored as such once the server has listed them, but
// for a name that is a keyword or a type too, as LEFT or DATE. Standard
// SQL's words are read from its dialect, whose spec is empty: keywords
// given here replace them all.
const std = cm.StandardSQL.dialect.words;
const stdWords = (kind) => Object.keys(std).filter((w) => std[w] === kind).join(' ');
export const sqlKeywords = stdWords(std.select) + ' qualify unnest struct array offset ordinal';
export const sqlTypes = stdWords(std.varchar) + ' int64 float64 bignumeric bool string bytes json geography datetime';
const dialectOf = (functions) => cm.SQLDialect.define({
  identifierQuotes: '`',
  hashComments: true,
  keywords: sqlKeywords,
  types: sqlTypes,
  builtin: functions.join(' '),
});

// bigquerySQL is the language of a BigQuery query, its functions (names
// from builtins) colored as such.
export const bigquerySQL = (functions) => cm.sql({ dialect: dialectOf(functions), upperCaseKeywords: false });

// builtins are the names of the functions to color: one word each, and no
// keyword or type.
export function builtins(list) {
  const taken = new Set((sqlKeywords + ' ' + sqlTypes).split(' '));
  return list.map((f) => f.name.toLowerCase()).filter((n) => /^\w+$/.test(n) && !taken.has(n));
}

// ---- the look ----

const theme = EditorView.theme({
  '&': { height: '100%', color: 'var(--fg)', backgroundColor: 'transparent', fontSize: '13px' },
  '.cm-scroller': { fontFamily: 'var(--mono)', lineHeight: 'var(--lh)', overflow: 'auto' },
  '.cm-content': { caretColor: 'var(--focus)', padding: '4px 0' },
  // Opaque, so a long line scrolled sideways slides under the line
  // numbers rather than showing through them.
  '.cm-gutters': { backgroundColor: 'var(--panel)', color: 'var(--dim)', border: 'none' },
  '.cm-activeLine': { backgroundColor: 'var(--focus-glow)' },
  '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--fg)' },
  '.cm-selectionBackground, &.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground': { backgroundColor: 'var(--visual) !important' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--focus)' },
  '.cm-fat-cursor': { backgroundColor: 'var(--focus) !important', color: 'var(--bg) !important' },
  '&:not(.cm-focused) .cm-fat-cursor': { background: 'none !important', outline: '1px solid var(--focus)', color: 'inherit !important' },
  '.cm-selectionMatch': { backgroundColor: 'var(--hit)' },
  '.cm-matchingBracket': { backgroundColor: 'var(--hit)', outline: 'none' },
  '.cm-vim-panel': { fontFamily: 'var(--mono)', padding: '2px 8px', backgroundColor: 'var(--bg-2)', color: 'var(--fg)' },
  '.cm-vim-panel input': { fontFamily: 'var(--mono)', color: 'var(--fg)', background: 'transparent', border: 'none', outline: 'none' },
  '.cm-placeholder': { color: 'var(--dim)' },
});

const highlight = cm.syntaxHighlighting(cm.HighlightStyle.define([
  { tag: tags.keyword, color: 'var(--t-kw)' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: 'var(--t-fn)' },
  { tag: tags.standard(tags.name), color: 'var(--t-builtin)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--t-str)' },
  { tag: tags.number, color: 'var(--t-num)' },
  { tag: [tags.bool, tags.null, tags.atom], color: 'var(--t-const)' },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: 'var(--t-com)', fontStyle: 'italic' },
  { tag: [tags.operator, tags.punctuation], color: 'var(--t-op)' },
  { tag: tags.propertyName, color: 'var(--t-prop)' },
  { tag: tags.special(tags.name), color: 'var(--t-var)' },
  { tag: [tags.name, tags.variableName], color: 'var(--t-name)' },
  { tag: tags.typeName, color: 'var(--t-type)' },
]));

// ---- keys ----

// vimOwns says whether a CodeMirror key binding is one vim keeps: a
// ctrl+letter (ctrl+v's block, ctrl+d's half page, ctrl+a's increment), or
// escape, which CodeMirror spends on collapsing a block's cursors before
// vim can leave insert mode. Its keymaps would otherwise have them first,
// the ctrl keys as emacs keys on a Mac (ctrl+v pages down, ctrl+d deletes a
// character) and as Mod keys elsewhere. The search panel keeps its escape.
const onMac = /Mac|iPhone|iPad/.test(navigator.platform);
function vimOwns(b) {
  if (b.key === 'Escape') return !b.scope;
  const key = (onMac ? b.mac : (b.win || b.linux)) || b.key || '';
  return (onMac ? /^Ctrl-[a-z]$/ : /^(Ctrl|Mod)-[a-z]$/).test(key);
}

// insertOnly keeps a default binding of enter, backspace, delete or tab
// (shift or not) to insert mode. Those keymaps come before vim's handler,
// so in normal mode enter would break the line rather than move down,
// backspace and delete would edit rather than move and x, and tab would
// indent. Outside insert mode the binding declines and vim has the key;
// tab, which vim's normal mode leaves alone, does nothing there rather
// than take focus out of the editor.
const vimPlainKey = /^(Shift-)?(Enter|Backspace|Delete|Tab)$/;
function inserting(view) {
  const adapter = cm.getCM(view);
  const vim = adapter && adapter.state.vim;
  return !vim || vim.insertMode;
}
function insertOnly(b) {
  if (!vimPlainKey.test(b.key || '')) return b;
  const tab = /Tab$/.test(b.key);
  const only = (f) => f && ((v) => (inserting(v) ? f(v) : tab));
  return { ...b, run: only(b.run), shift: only(b.shift) };
}

// ---- vim or not ----

// The vimMode setting gives every editor vim's keys; off, they have
// CodeMirror's own, as VS Code's: the default keymap whole, and esc
// leaving the editor once nothing is left for it to close. setVimMode
// switches every editor made, at once. Vim's handler and the default
// keymap each sit in a compartment where they always were, so with vim
// on the editor is as it was before the setting.
let vimOn = vimSetting();
const vimSlot = new cm.Compartment();
const keysSlot = new cm.Compartment();
const defaultKeys = () => [...cm.defaultKeymap, ...cm.historyKeymap, ...cm.searchKeymap, cm.indentWithTab];
const vimExt = (on) => (on ? cm.vim() : []);
const keysExt = (on) => cm.keymap.of(on ? defaultKeys().filter((b) => !vimOwns(b)).map(insertOnly) : defaultKeys());

// vimMode says whether the editors have vim's keys.
export const vimMode = () => vimOn;

// setVimMode gives every editor vim's keys (on) or the usual ones.
export function setVimMode(on) {
  on = !!on;
  noteVimMode(on);
  if (on === vimOn) return;
  vimOn = on;
  for (const ref of views) {
    const view = ref.deref();
    if (!view || view.destroyed) views.delete(ref);
    else applyVim(view);
  }
}

function applyVim(view) {
  view.dispatch({ effects: [vimSlot.reconfigure(vimExt(vimOn)), keysSlot.reconfigure(keysExt(vimOn))] });
  const o = owners.get(view);
  if (o && o.vimChanged) o.vimChanged(vimOn);
}

const bindingOf = (keys, key) => keys.find((b) => b.key === key && b.run);
const closeSearch = bindingOf(cm.searchKeymap, 'Escape');
const simplifySelection = bindingOf(cm.defaultKeymap, 'Escape');
const openSearch = bindingOf(cm.searchKeymap, 'Mod-f');

// openFind opens the find panel in view, as cmd+f (ctrl+f) does.
export function openFind(view) { if (openSearch) openSearch.run(view); }

// escapeOff is esc without vim: it closes the first thing open of the
// completions, the card, the mouse's hover card, the find panel and the
// extra cursors or the selection; with none, leave() gives the keys back
// to the page.
function escapeOff(view, leave) {
  if (cm.completionStatus(view.state)) { cm.closeCompletion(view); return true; }
  if (view.state.field(cardField, false)) { view.dispatch({ effects: setCard.of(null) }); return true; }
  if (view.dom.querySelector('.cm-tooltip-hover')) { view.dispatch({ effects: cm.closeHoverTooltips }); return true; }
  if (view.dom.querySelector('.cm-panel.cm-search') && closeSearch && closeSearch.run(view)) return true;
  if (simplifySelection && simplifySelection.run(view)) return true;
  const sel = view.state.selection.main;
  if (!sel.empty) { view.dispatch({ selection: { anchor: sel.head } }); return true; }
  leave(view);
  return true;
}

// isBlock says whether the selection is a ctrl+v block that covers text: a
// range per line.
export function isBlock(view) {
  const ranges = view.state.selection.ranges;
  return ranges.length > 1 && ranges.some((r) => !r.empty);
}

// Vim's ex commands and K are global; each goes to the editor it was typed
// in, by the options that editor was made with.
const owners = new WeakMap(); // EditorView → its options
const ownerOf = (adapter) => adapter && owners.get(adapter.cm6);
const exDefined = new Set();

// defineEx has :name (or :short) call the editor's ex[name], or say it has
// none.
function defineEx(name, short) {
  if (exDefined.has(name)) return;
  exDefined.add(name);
  cm.Vim.defineEx(name, short, (adapter, params) => {
    const o = ownerOf(adapter);
    const f = o && o.ex && o.ex[name];
    if (f) f(params);
    else if (o && o.say) o.say('not an editor command here: ' + name);
  });
}

// ---- format ----

// replaceWith turns the editor's text into text as one change, which is
// one undo step, changing only the characters that differ (the cursor
// stays where it was, as near as the new text allows).
export function replaceWith(view, text) {
  const before = view.state.doc, after = cm.Text.of(text.split('\n'));
  const changes = [];
  for (const c of cm.Chunk.build(before, after, { timeout: 500 })) {
    const inner = c.toA > c.fromA && c.toB > c.fromB ? c.changes : [];
    if (!inner.length) { changes.push({ from: c.fromA, to: c.toA, insert: after.sliceString(c.fromB, c.toB) }); continue; }
    for (const ch of inner) changes.push({ from: c.fromA + ch.fromA, to: c.fromA + ch.toA, insert: after.sliceString(c.fromB + ch.fromB, c.fromB + ch.toB) });
  }
  if (changes.length) view.dispatch({ changes });
}

let formatting = false;

// formatText sends the editor's text through call (the server's
// formatter: it answers {text, changed, note}) and puts what comes back in
// the editor, unsaved, as one undo step. Text typed meanwhile is kept: the
// answer is for the text sent. say shows the outcome, or the note when a
// part was left as it was (ADR 0024).
export async function formatText(ed, call, say) {
  if (formatting) { say('already formatting'); return false; }
  const sent = ed.doc();
  if (!sent.trim()) { say('nothing to format'); return false; }
  formatting = true;
  say('formatting…');
  try {
    const res = await call(sent);
    if (ed.doc() !== sent) { say('typed while formatting: not applied'); return false; }
    if (!res.changed) { say(res.note || 'already formatted'); return true; }
    replaceWith(ed.view, res.text);
    say(res.note ? res.note + ' (' + undoesWith() + ' undoes it)' : 'formatted: ' + undoesWith() + ' undoes it');
    return true;
  } catch (e) {
    say(e.message);
    return false;
  } finally {
    formatting = false;
  }
}

// ---- go to definition ----

// firstUse moves to the first use of the word under the cursor.
function firstUse(view) {
  const word = view.state.wordAt(view.state.selection.main.head);
  if (!word) return;
  const m = new RegExp('\\b' + view.state.sliceDoc(word.from, word.to) + '\\b').exec(view.state.doc.toString());
  if (m) jumpWithin(view, m.index);
}

// Vim keeps one jump list (G, gg, /, n, %, gd), which ctrl+o and ctrl+i
// walk. An editor whose jumps go further than its own text (the source
// pane's, from file to file) passes its own as opts.jumps, with vim's
// calls: add(adapter, from, to) records a jump between two of the
// editor's positions, move(adapter, n) steps n entries and answers the
// one there ({find() → position}) or nothing (it went elsewhere), and
// find(adapter, n) peeks without moving. The rest keep vim's.
let jumpsInstalled = false;
function installJumps() {
  if (jumpsInstalled) return;
  jumpsInstalled = true;
  const g = cm.Vim.getVimGlobalState_();
  const vims = g.jumpList;
  const own = (adapter) => { const o = ownerOf(adapter); return o && o.jumps; };
  g.jumpList = {
    get cachedCursor() { return vims.cachedCursor; },
    set cachedCursor(c) { vims.cachedCursor = c; },
    add: (a, from, to) => (own(a) || vims).add(a, from, to),
    move: (a, n) => (own(a) || vims).move(a, n),
    find: (a, n) => (own(a) || vims).find(a, n),
  };
}

// jumpWithin moves the cursor to pos, remembered in vim's jump list so
// ctrl+o comes back. Without vim the editor's own jump list, where it has
// one, remembers it (jumps.addAt, with the two offsets).
export function jumpWithin(view, pos) {
  const adapter = cm.getCM(view);
  if (!adapter) {
    const from = view.state.selection.main.head;
    view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
    const o = owners.get(view);
    if (o && o.jumps && o.jumps.addAt) o.jumps.addAt(from, pos);
    return;
  }
  const from = adapter.getCursor();
  view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
  cm.Vim.getVimGlobalState_().jumpList.add(adapter, from, adapter.getCursor());
}

// ---- the card at the cursor ----

// showCard shows build()'s element in a card at pos until the cursor
// moves, the text changes or escape: what ]e says of an error, ]g of a
// change.
const setCard = cm.StateEffect.define();
const cardField = cm.StateField.define({
  create: () => null,
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setCard)) return e.value;
    return v && (tr.docChanged || tr.selection) ? null : v;
  },
  provide: (f) => cm.showTooltip.compute([f], (state) => {
    const v = state.field(f);
    return v ? { pos: v.pos, above: false, create: () => ({ dom: v.build() }) } : null;
  }),
});

export function showCard(view, pos, build) {
  // After vim has put its cursor down, which would close a card set with it.
  setTimeout(() => view.dispatch({ effects: setCard.of({ pos, build }) }), 0);
}

// cardOf is a card's element: its lines (each [class, text]) and a dim
// line below them.
export function cardOf(lines, meta) {
  const root = document.createElement('div');
  root.className = 'cm-card';
  for (const [cls, text] of lines) {
    const row = document.createElement('div');
    row.className = cls;
    row.textContent = text;
    root.append(row);
  }
  const m = document.createElement('div');
  m.className = 'cm-card-meta';
  m.textContent = meta;
  root.append(m);
  return root;
}

// ---- jumping between errors ----

// The next and previous error (]e and [e) wrap at the ends, as
// vim.diagnostic.goto_next does, and show its message in a card.
export function jumpToError(view, dir, say) {
  const list = [];
  cm.forEachDiagnostic(view.state, (d, from) => list.push({ from, message: d.full || d.message, source: d.source || '' }));
  if (!list.length) { if (say) say('no errors'); return; }
  list.sort((a, b) => a.from - b.from);
  const head = view.state.selection.main.head;
  let i = dir > 0 ? list.findIndex((d) => d.from > head) : list.findLastIndex((d) => d.from < head);
  if (i < 0) i = dir > 0 ? 0 : list.length - 1;
  const d = list[i];
  view.dispatch({ selection: { anchor: d.from }, scrollIntoView: true });
  showCard(view, d.from, () => cardOf([['cm-card-error', d.message]], [d.source, i + 1 + ' of ' + list.length].filter(Boolean).join(' · ')));
}

// ---- xf's vim keys ----

// The commands xf adds to vim's normal mode, by the config key that names
// their keys (config.VimKeys in Go, where the defaults are too: these are
// the keys until the page has the server's). setVimKeys maps them; the
// settings page's Vim tab changes them.
const vimCommands = {
  vimNextHunk: { keys: ']g', run: (a, o) => (o && o.hunk ? o.hunk(a.cm6, 1) : say(o, 'no git changes here')) },
  vimPrevHunk: { keys: '[g', run: (a, o) => (o && o.hunk ? o.hunk(a.cm6, -1) : say(o, 'no git changes here')) },
  vimUndoHunk: { keys: 'guh', run: (a, o) => (o && o.undoHunk ? o.undoHunk(a.cm6) : say(o, 'no git changes here')) },
  vimStageHunk: { keys: 'gsh', run: (a, o) => (o && o.stageHunk ? o.stageHunk(a.cm6) : say(o, 'no git changes here to stage')) },
  vimNextError: { keys: ']e', run: (a, o) => jumpToError(a.cm6, 1, o && o.say) },
  vimPrevError: { keys: '[e', run: (a, o) => jumpToError(a.cm6, -1, o && o.say) },
  // The documentation of what is under the cursor, as in an editor with a
  // language server.
  vimHover: { keys: 'K', run: (a, o) => { if (o && o.hover) o.hover(a.cm6); } },
  // Where what is under the cursor is defined; an editor that can't ask
  // a language server goes to the first use of the word, as vim's own gd
  // does.
  vimDefinition: { keys: 'gd', run: (a, o) => (o && o.definition ? o.definition(a.cm6) : firstUse(a.cm6)) },
  // The editor's context menu at the cursor, as a right-click opens it
  // at the pointer (the source pane's: source-menu.js).
  vimMenu: { keys: 'gm', run: (a, o) => (o && o.menu ? o.menu(a.cm6) : say(o, 'no menu here')) },
  // The source pane's buffers, the files it has shown: the previous and
  // next one, and their list (buffers.js).
  vimBufferPrev: { keys: '<leader>[', run: (a, o) => (o && o.buffer ? o.buffer(-1) : say(o, 'no buffers here')) },
  vimBufferNext: { keys: '<leader>]', run: (a, o) => (o && o.buffer ? o.buffer(1) : say(o, 'no buffers here')) },
  vimBuffers: { keys: '<leader>b', run: (a, o) => (o && o.buffers ? o.buffers() : say(o, 'no buffers here')) },
  // :bd, the buffer shown out of the list.
  vimBufferDelete: { keys: '<leader>x', run: (a, o) => (o && o.bufferDelete ? o.bufferDelete() : say(o, 'no buffers here')) },
  // The palette's file finder, as cmd+p (ctrl+p off a Mac) opens it.
  vimFiles: { keys: '<leader>f', run: (a, o) => (o && o.files ? o.files() : say(o, 'no files here')) },
  // Format the file's SQL and config block (ADR 0024): :format, with a key
  // only when one is set; no keys, no mapping.
  vimFormat: { keys: '', run: (a, o) => (o && o.format ? o.format() : say(o, 'nothing to format here')) },
};
// <leader> in a command's keys is vimLeader's keys, as vim's mapleader.
const leaderDefault = '<Space>';
const leaderRE = /<leader>/gi;
const hasLeader = /<leader>/i;
const say = (o, msg) => { if (o && o.say) o.say(msg); };

let vimDefined = false;
const vimMapped = {}; // config key → the keys mapped now

// Vim runs a key's own command at once, though longer keys start with
// it, so while a command uses the leader, vim's own mapping of the leader
// goes: <Space>'s l, in normal mode (visual mode keeps it). Another
// leader's own mapping, a comma's say, comes back on a reload.
let leaderTaken = null;
function takeLeader(leader) {
  if (leaderTaken === leader) return;
  if (leaderTaken === '<Space>') { cm.Vim.unmap('<Space>', 'visual'); cm.Vim.noremap('<Space>', 'l'); }
  leaderTaken = leader;
  if (!leader) return;
  cm.Vim.unmap(leader); // the keymap's own, which has no mode
  if (leader === '<Space>') cm.Vim.noremap('<Space>', 'l', 'visual');
}

// setVimKeys maps each command to keys[name], or with none to its
// default, <leader> written out as keys.vimLeader.
export function setVimKeys(keys) {
  if (!vimDefined) {
    vimDefined = true;
    for (const [name, c] of Object.entries(vimCommands)) {
      cm.Vim.defineAction('xf:' + name, (adapter) => c.run(adapter, ownerOf(adapter)));
    }
    // ctrl+^ is vim's alternate file: the editor's altFile, where it has
    // files to go between (the source pane).
    cm.Vim.defineAction('xf:altFile', (adapter) => {
      const o = ownerOf(adapter);
      if (o && o.altFile) o.altFile(); else say(o, 'no alternate file');
    });
    for (const k of ['<C-^>', '<C-6>']) cm.Vim.mapCommand(k, 'action', 'xf:altFile', {}, { context: 'normal' });
    // gf is vim's go to the file named under the cursor, gF at the line
    // written after it: the editor's goToFile, where it has files.
    cm.Vim.defineAction('xf:goToFile', (adapter, args) => {
      const o = ownerOf(adapter);
      if (o && o.goToFile) o.goToFile(adapter.cm6, !!(args && args.line)); else say(o, 'no files here');
    });
    cm.Vim.mapCommand('gf', 'action', 'xf:goToFile', {}, { context: 'normal' });
    cm.Vim.mapCommand('gF', 'action', 'xf:goToFile', { line: true }, { context: 'normal' });
    // gc comments out the lines a motion covers, or the visual selection's,
    // or brings them back in, as vim-commentary's: gcc the line, 3gcc
    // three, gcip the paragraph, . again (comment.js).
    cm.Vim.defineOperator('xf:comment', (adapter, args, ranges) => {
      let from = Infinity, to = -1;
      for (const r of ranges) {
        const [a, b] = r.anchor.line < r.head.line || (r.anchor.line === r.head.line && r.anchor.ch <= r.head.ch) ? [r.anchor, r.head] : [r.head, r.anchor];
        from = Math.min(from, a.line);
        // An end at the start of a line, as a linewise motion's, leaves that line out.
        to = Math.max(to, b.ch === 0 && b.line > a.line ? b.line - 1 : b.line);
      }
      const ch = toggleRange(adapter.cm6, from + 1, to + 1);
      return { line: from, ch: ch ?? ranges[0].anchor.ch };
    });
    cm.Vim.mapCommand('gc', 'operator', 'xf:comment', {}, {});
  }
  const leader = (keys && keys.vimLeader) || leaderDefault;
  const want = (name) => ((keys && keys[name]) || vimCommands[name].keys).replace(leaderRE, leader);
  const changed = Object.keys(vimCommands).filter((name) => (vimMapped[name] || '') !== want(name));
  // Every old mapping goes first, so two commands can swap keys.
  for (const name of changed) if (vimMapped[name]) cm.Vim.unmap(vimMapped[name], 'normal');
  for (const name of changed) {
    if (want(name)) cm.Vim.mapCommand(want(name), 'action', 'xf:' + name, {}, { context: 'normal' });
    vimMapped[name] = want(name);
  }
  const usesLeader = Object.keys(vimCommands).some((name) => hasLeader.test((keys && keys[name]) || vimCommands[name].keys));
  takeLeader(usesLeader ? leader : null);
}

// vimKey is the keys of a command, for the help and the menus; without
// vim, none.
export const vimKey = (name) => (vimOn ? vimMapped[name] || vimCommands[name].keys : '');

// ---- holding keys back for a mapping ----

// Vim runs a command as soon as the keys typed are one, so a mapping that
// begins with another command's keys would never run: guh would be gu
// (lowercase) then h. As vim's timeoutlen does, keys typed in normal mode
// that begin one of these commands' keys are held back until they make
// them, or until the next key or a second shows they don't; vim then has
// them, so gu{motion} still lowercases.
const holdMs = 1000;
const tokens = (keys) => keys.match(/<[^>]+>|./gsu) || [];
let holdInstalled = false;

function installHold() {
  if (holdInstalled) return;
  holdInstalled = true;
  const Vim = cm.Vim;
  const pass = Vim.multiSelectHandleKey.bind(Vim);
  let held = [], heldBy = null, timer = 0;
  const release = () => {
    clearTimeout(timer);
    const keys = held, adapter = heldBy;
    held = [];
    heldBy = null;
    for (const k of keys) pass(adapter, k, 'user');
  };
  Vim.multiSelectHandleKey = (adapter, key, origin) => {
    if (held.length && heldBy !== adapter) release();
    const vim = adapter.state.vim;
    const st = vim && vim.inputState;
    const idle = st && !vim.insertMode && !vim.visualMode && !st.keyBuffer.length && !st.operator && !st.registerName;
    if (origin !== 'user' || (!held.length && !idle)) return pass(adapter, key, origin);
    const typed = [...held, key];
    let begins = false;
    for (const [name, keys] of Object.entries(vimMapped)) {
      const want = tokens(keys);
      if (want.length < 2 || want.length < typed.length || typed.some((k, i) => k !== want[i])) continue;
      if (want.length > typed.length) { begins = true; continue; }
      clearTimeout(timer);
      held = [];
      heldBy = null;
      vim.status = '';
      vimCommands[name].run(adapter, ownerOf(adapter));
      return true;
    }
    if (begins) {
      held = typed;
      heldBy = adapter;
      clearTimeout(timer);
      timer = setTimeout(release, holdMs);
      return true;
    }
    held = typed.slice(0, -1);
    release();
    return pass(adapter, key, origin);
  };
}

// ---- the system clipboard ----

// Yanks, deletes and changes to the unnamed register also go to the system
// clipboard, and p and P paste from it, as vim's clipboard=unnamedplus
// does. The clipboard keeps only text: when it still holds what vim last
// put there, p pastes the register itself, which knows whether that was
// whole lines or a block. Where the browser gives no clipboard (http other
// than localhost) or refuses a read, p pastes the register.
let clipboardShared = false;

function shareClipboard() {
  if (clipboardShared) return;
  clipboardShared = true;
  const Vim = cm.Vim;
  const rc = Vim.getRegisterController();
  const clip = () => navigator.clipboard;

  const push = rc.pushText.bind(rc);
  rc.pushText = (name, operator, text, linewise, blockwise) => {
    push(name, operator, text, linewise, blockwise);
    if (name && name !== '"' && rc.isValidRegister(name)) return; // "a, "_, and "+ (which writes itself)
    if (clip()) clip().writeText(rc.unnamedRegister.toString()).catch(() => {});
  };

  Vim.defineAction('paste', function (cmAdapter, args, vim) {
    const register = rc.getRegister(args.registerName);
    const own = () => this.continuePaste(cmAdapter, args, vim, register.toString(), register);
    const unnamed = !args.registerName || args.registerName === '"';
    // A macro's keys run one after another, so its pastes can't wait.
    if (!(unnamed || args.registerName === '+') || !clip() || Vim.getVimGlobalState_().macroModeState.isPlaying) { own(); return; }
    clip().readText().then((text) => {
      if (text === register.toString()) { own(); return; }
      // Text from elsewhere that ends a line pastes as lines, as in vim.
      const shape = { linewise: text.endsWith('\n'), blockwise: false };
      this.continuePaste(cmAdapter, args, vim, text, shape);
    }, own);
  });
}

// clipCopy puts the text from..to in the unnamed register, and so on the
// clipboard, as y does (d with cut, which takes it out too).
export function clipCopy(view, from, to, cut) {
  const text = view.state.sliceDoc(from, to);
  cm.Vim.getRegisterController().pushText('"', cut ? 'delete' : 'yank', text, false, false);
  if (cut) view.dispatch({ changes: { from, to }, selection: { anchor: from }, userEvent: 'delete.cut' });
  return text;
}

// clipText is what a paste puts in: the clipboard's text, or the unnamed
// register's where the browser gives no clipboard or refuses the read.
export async function clipText() {
  const own = cm.Vim.getRegisterController().getRegister('"').toString();
  if (!navigator.clipboard) return own;
  try { return await navigator.clipboard.readText(); } catch { return own; }
}

// ---- the error lens ----

// The line an error names is tinted, and its message follows the line's
// text, so it reads without hovering the underline.
class LensWidget extends cm.WidgetType {
  constructor(message, warning) { super(); this.message = message; this.warning = warning; }
  eq(other) { return other.message === this.message && other.warning === this.warning; }
  toDOM() {
    const span = document.createElement('span');
    span.className = 'cm-lens' + (this.warning ? ' cm-lens-warn' : '');
    // BigQuery ends a message with where it is, which the line shows.
    span.textContent = this.message.replace(/\s*at \[\d+:\d+\]\s*$/, '');
    span.title = this.message;
    return span;
  }
}

const setLens = cm.StateEffect.define();
const lensField = cm.StateField.define({
  create: () => cm.Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) if (e.is(setLens)) deco = e.value;
    return deco;
  },
  provide: (f) => EditorView.decorations.from(f),
});

// lensOf is the lens of each error, and with warnings of each warning
// too: the first of a line names it, an error before a warning.
function lensOf(doc, diags, warnings) {
  const ranges = [];
  const seen = new Set();
  const shown = diags.filter((d) => d.severity === 'error' || (warnings && d.severity === 'warning'));
  shown.sort((a, b) => (a.severity === 'error' ? 0 : 1) - (b.severity === 'error' ? 0 : 1));
  for (const d of shown) {
    const line = doc.lineAt(Math.min(d.from, doc.length));
    if (seen.has(line.number)) continue;
    seen.add(line.number);
    const warning = d.severity !== 'error';
    ranges.push(cm.Decoration.line({ class: warning ? 'cm-lens-line cm-lens-warn-line' : 'cm-lens-line' }).range(line.from));
    ranges.push(cm.Decoration.widget({ widget: new LensWidget(d.message, warning), side: 1 }).range(line.to));
  }
  return cm.Decoration.set(ranges.sort((a, b) => a.from - b.from || a.value.startSide - b.value.startSide), true);
}

// ---- wrapping long lines ----

// The wrapLines setting wraps long lines in every editor, rather than
// scrolling them sideways, but those held unwrapped: a side-by-side
// difference, whose two sides line up a line at a time.
let wrapOn = false;
const wrapping = new cm.Compartment();
const views = new Set(); // WeakRefs of the editors made
const unwrapped = new WeakSet(); // the editors held unwrapped

function applyWrap(view) {
  if (view.destroyed) return;
  const want = wrapOn && !unwrapped.has(view);
  if (want === (wrapping.get(view.state) === EditorView.lineWrapping)) return;
  view.dispatch({ effects: wrapping.reconfigure(want ? EditorView.lineWrapping : []) });
}

// setWrap turns wrapping on or off in every editor.
export function setWrap(on) {
  wrapOn = !!on;
  for (const ref of views) {
    const view = ref.deref();
    if (!view || view.destroyed) views.delete(ref);
    else applyWrap(view);
  }
}

// holdUnwrapped keeps view's lines unwrapped (on) whatever wrapLines
// says, or lets them follow it again.
export function holdUnwrapped(view, on) {
  if (on) unwrapped.add(view); else unwrapped.delete(view);
  applyWrap(view);
}

// ---- the editor ----

// createEditor makes an editor in parent. opts:
//   doc          the text it starts with
//   language     its language (an extension), which setLanguage changes
//   placeholder  what an empty editor says
//   extensions   the view's own (completion, hover, signature help…)
//   keys         the view's own key bindings, before vim's
//   ex           the view's ex commands, by name: write and quit come with
//                defaults (save, leave); short names are in exShort
//   save         what :w and cmd+s do
//   leave        what ctrl+w and :q do after giving focus back
//   hover        what K does, given the view
//   definition   what gd does, given the view
//   hunk         what ]g (dir 1) and [g (dir -1) do, given the view and dir
//   undoHunk     what guh does, given the view
//   stageHunk    what gsh does, given the view
//   menu         what gm does, given the view: opens a context menu
//   goToFile     what gf (and gF, line true) does, given the view and line
//   completeFile what ctrl+x ctrl+f does in insert mode, given the view
//   jumps        its own jump list, for ctrl+o and ctrl+i (see installJumps);
//                without vim, its addAt(from, to) and step(n) do them
//   vimChanged   called with whether vim is on when setVimMode switches it
//   onFocus      called when the editor gets focus, onBlur when it loses it
//   onUpdate     called with each update but those set() makes
//   readOnly     whether the text can't be changed
//   say          shows a message
//   lensWarnings whether a warning's message follows its line too, as an
//                error's does
//   wrap         false to keep its lines unwrapped whatever wrapLines says
// It returns a handle: { view, doc(), set(text, keepCursor), save(light),
// restore(json), setLanguage(ext),
// setMarks(diags), setReadOnly(on), focus(), blur() }.
export function createEditor(parent, opts) {
  let quiet = false; // set while the page replaces the text
  const madeWithVim = vimOn; // what extensions (and so restore) start with
  const language = new cm.Compartment();
  const editable = new cm.Compartment();
  const undo = new cm.Compartment(); // the undo history, which set() starts again

  const leave = (view) => {
    view.contentDOM.blur();
    if (opts.leave) opts.leave();
  };
  const save = () => { if (opts.save) opts.save(); return true; };

  // ctrl+x in insert mode waits for the key after it, as vim's ctrl+x
  // mode: ctrl+f then completes a file name (opts.completeFile). Any other
  // key ends the wait and does what it does.
  let ctrlX = false;
  const startCtrlX = (v) => {
    if (!opts.completeFile || !inserting(v) || !v.state.selection.main.empty) return false;
    ctrlX = true;
    if (opts.say) opts.say('-- ^X mode (^F file names)');
    return true;
  };
  const afterCtrlX = (v) => {
    if (!ctrlX) return false;
    ctrlX = false;
    if (opts.say) opts.say('');
    opts.completeFile(v);
    return true;
  };

  const extensions = [
    // ctrl+n and ctrl+p move through the completions as vim's popup menu
    // does; with none open they are vim's. It comes before vim's own
    // handler, which would otherwise have the keys first.
    Prec.highest(keymap.of([
      { key: 'Ctrl-n', run: (v) => cm.completionStatus(v.state) === 'active' && cm.moveCompletionSelection(true)(v), preventDefault: true },
      { key: 'Ctrl-p', run: (v) => cm.completionStatus(v.state) === 'active' && cm.moveCompletionSelection(false)(v), preventDefault: true },
      // tab takes the completion in focus, as VS Code's does; with none
      // open it indents, or moves to a snippet's next argument.
      { key: 'Tab', run: (v) => cm.completionStatus(v.state) === 'active' && cm.acceptCompletion(v) },
      // Escape closes the completions and goes on to vim, which leaves
      // insert mode: one press, as in vim, not one for each. Without vim
      // it closes one thing open, or leaves (escapeOff).
      { key: 'Escape', run: (v) => {
        if (!vimOn) return escapeOff(v, leave);
        if (cm.completionStatus(v.state)) cm.closeCompletion(v);
        return false;
      } },
      { key: 'Ctrl-x', run: startCtrlX },
      { key: 'Ctrl-f', run: afterCtrlX },
    ])),
    // vim goes next, so its keys win over the default keymap's.
    vimSlot.of(vimExt(vimOn)),
    Prec.highest(keymap.of([
      ...(opts.keys || []),
      // Without vim, ctrl+o and ctrl+i walk the editor's own jump list, as
      // vim's do; with vim they are vim's.
      ...(opts.jumps && opts.jumps.step ? [
        { key: 'Ctrl-o', run: () => !vimOn && (opts.jumps.step(-1), true) },
        { key: 'Ctrl-i', run: () => !vimOn && (opts.jumps.step(1), true) },
      ] : []),
      { key: 'Mod-s', run: save, preventDefault: true },
      // cmd+/ (ctrl+/ off a Mac) comments out the selection's lines, or
      // brings them back in, as VS Code's.
      { key: 'Mod-/', run: (v) => { toggleSelection(v); return true; }, preventDefault: true },
      { key: 'Ctrl-w', run: (v) => { leave(v); return true; }, preventDefault: true },
    ])),
    // ctrl+v's block is a range per line.
    EditorState.allowMultipleSelections.of(true),
    undo.of(cm.history()),
    keysSlot.of(keysExt(vimOn)),
    cm.lineNumbers(),
    cm.highlightActiveLine(),
    cm.highlightActiveLineGutter(),
    cm.drawSelection(),
    cm.bracketMatching(),
    cm.highlightSelectionMatches(),
    language.of(opts.language || []),
    wrapping.of([]),
    editable.of(EditorState.readOnly.of(!!opts.readOnly)),
    highlight,
    theme,
    opts.placeholder ? cm.placeholder(opts.placeholder) : [],
    ...(opts.extensions || []),
    lensField,
    cardField,
    EditorView.domEventHandlers({
      keydown(e, v) {
        if (ctrlX && !/^(Control|Shift|Alt|Meta)$/.test(e.key) && !(e.ctrlKey && /^[xf]$/i.test(e.key))) {
          ctrlX = false;
          if (opts.say) opts.say('');
        }
        // Without vim, escapeOff closes these, one a press.
        if (e.key === 'Escape' && vimOn && v.state.field(cardField, false)) v.dispatch({ effects: setCard.of(null) });
        // The mouse's hover card stays until the pointer leaves it, so esc closes it too.
        if (e.key === 'Escape' && vimOn && v.dom.querySelector('.cm-tooltip-hover')) v.dispatch({ effects: cm.closeHoverTooltips });
        return false;
      },
      focus: () => { if (opts.onFocus) opts.onFocus(); },
      blur: () => { if (opts.onBlur) opts.onBlur(); },
    }),
    EditorView.updateListener.of((u) => {
      if (!quiet && opts.onUpdate) opts.onUpdate(u);
    }),
  ];
  const view = new EditorView({ parent, state: EditorState.create({ doc: opts.doc || '', extensions }) });

  owners.set(view, { ...opts, ex: { write: save, quit: () => leave(view), ...(opts.ex || {}) } });
  if (opts.wrap === false) unwrapped.add(view);
  views.add(new WeakRef(view));
  applyWrap(view);
  shareClipboard();
  defineEx('write', 'w');
  defineEx('quit', 'q');
  for (const [name, short] of Object.entries(opts.exShort || {})) defineEx(name, short);
  if (!vimDefined) setVimKeys();
  installHold();
  installJumps();

  return {
    view,
    doc: () => view.state.doc.toString(),
    // set replaces the text, the cursor at the top, or with keepCursor where
    // it was (as near as the new text allows) with the scroll kept. Another
    // text (no keepCursor: another file) starts a new undo history, so u
    // never brings back the text before it, which autosave would then
    // write over this file. The same file read again (keepCursor) keeps its
    // history, as vim's undoreload does, and the same text adds nothing.
    set(text, keepCursor) {
      if (keepCursor && text === view.state.doc.toString()) return;
      quiet = true;
      try {
        const head = Math.min(view.state.selection.main.head, text.length);
        const top = view.scrollDOM.scrollTop;
        view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, selection: { anchor: keepCursor ? head : 0 },
          effects: keepCursor ? [] : undo.reconfigure([]) });
        if (!keepCursor) view.dispatch({ effects: undo.reconfigure(cm.history()) });
        view.scrollDOM.scrollTop = keepCursor ? top : 0;
      } finally { quiet = false; }
    },
    // save is the text, selection and undo history as JSON, for a tab's
    // workspace (workspace.js); light leaves the history out. restore puts
    // one back, the language and read-only kept.
    save: (light) => view.state.toJSON(light ? undefined : savedFields),
    restore(json) {
      const lang = language.get(view.state);
      const ro = editable.get(view.state);
      quiet = true;
      try {
        view.setState(EditorState.fromJSON(json, { extensions }));
        view.dispatch({ effects: [language.reconfigure(lang || []), editable.reconfigure(ro || [])] });
        applyWrap(view);
        if (madeWithVim !== vimOn) applyVim(view);
        // The history goes back in its compartment, where set() can start
        // it again: fromJSON would add it outside. It is taken out first,
        // or the empty one would be kept over the saved one.
        if (json.history) {
          const past = EditorState.fromJSON(json, { extensions: cm.history() }, savedFields).field(historyField);
          view.dispatch({ effects: undo.reconfigure([]) });
          view.dispatch({ effects: undo.reconfigure([historyField.init(() => past), cm.history()]) });
        }
      } finally { quiet = false; }
    },
    setLanguage: (ext) => view.dispatch({ effects: language.reconfigure(ext) }),
    setReadOnly: (on) => view.dispatch({ effects: editable.reconfigure(EditorState.readOnly.of(!!on)) }),
    // setMarks underlines each diagnostic ({from, to, severity, message}),
    // an error's message at the end of its line; none clears them.
    setMarks(diags) {
      const list = diags || [];
      view.dispatch(cm.setDiagnostics(view.state, list), { effects: setLens.of(lensOf(view.state.doc, list, opts.lensWarnings)) });
    },
    focus: () => view.focus(),
    blur: () => view.contentDOM.blur(),
  };
}
