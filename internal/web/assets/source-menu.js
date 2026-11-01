// The source pane's context menu: a right-click in the editor opens it at
// the pointer, and the menu key, shift+F10 or gm (vimMenu) at the cursor.
// Shift+right-click, or a right-click while it is open, gives the
// browser's own menu instead.
//
// As in VS Code, a right-click outside the selection moves the cursor
// there first, so the items act on what was clicked; inside it the
// selection stays, for Copy and Cut. The editor leaves insert and visual
// mode, and each item does what its key does from normal mode, Paste
// apart: it goes back to insert mode when it was opened there.
//
// It holds what is about the place clicked, and the file's paths; what
// is about the whole file or action (symbols, errors, run) is on keys
// and in the palette. Stage and Revert Change show only on a git change;
// the rest always show, dimmed with the reason when they can't be done.

import * as cm from './vendor/codemirror.mjs';
import { S, redraw, hooks, copy, onMac, absPath, openLink, say } from './state.js';
import { SRC, isLibraryFile } from './source.js';
import { vimKey, clipCopy, clipText } from './editor.js';
import { hunkUnder, undoHunk, stageHunk } from './git-hunks.js';
import { refUnder } from './source-smart.js';
import { showMenu, menuOpen, closeMenu } from './ctx-menu.js';

const { Vim, getCM } = cm;

let openSeq = 0;    // drops a menu whose items arrive after another opened
let keyedAt = 0;    // when a key last opened one, so its contextmenu event is the same

const mod = (k) => (onMac ? '⌘' + k.toUpperCase() : 'ctrl+' + k);

// ---- opening ----

// onContextMenu is the editor's contextmenu event.
export function onContextMenu(e, view) {
  if (e.shiftKey) return false;
  if (menuOpen()) { closeMenu(); return false; }
  e.preventDefault();
  // A key that opens the browser's menu sends this too, after its keydown.
  if (Date.now() - keyedAt < 500) return true;
  const fromKey = e.button !== 2 && e.clientX === 0 && e.clientY === 0;
  if (fromKey) { openAtCursor(view); return true; }
  const pos = view.posAtCoords({ x: e.clientX, y: e.clientY }, false);
  open(view, pos, { x: e.clientX, y: e.clientY });
  return true;
}

// onMenuKey opens the menu at the cursor for the menu key and shift+F10.
export function onMenuKey(e, view) {
  if (!(e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey))) return false;
  e.preventDefault();
  keyedAt = Date.now();
  if (!menuOpen()) openAtCursor(view);
  return true;
}

// openAtCursor is gm: the menu just below the cursor.
export function openAtCursor(view) {
  if (menuOpen()) return;
  const head = view.state.selection.main.head;
  const c = view.coordsAtPos(head) || view.contentDOM.getBoundingClientRect();
  open(view, null, { x: c.left, y: c.bottom + 2 });
}

// open settles the cursor and the mode (pos is where a right-click put
// the pointer, null to keep the cursor), then shows the items at xy.
async function open(view, pos, xy) {
  const seq = ++openSeq;
  const adapter = getCM(view);
  const vim = adapter && adapter.state.vim;
  const wasInsert = !!(vim && vim.insertMode);
  const sel = view.state.selection.main;
  const inside = pos != null && !sel.empty && pos >= sel.from && pos <= sel.to;
  // Where a paste goes with no range: where the pointer or the cursor was
  // (leaving insert mode moves vim's cursor back one).
  const at = pos != null && !inside ? pos : sel.head;
  if (!view.hasFocus) view.focus();
  if (wasInsert || (vim && vim.visualMode && pos != null && !inside)) Vim.handleKey(adapter, '<Esc>', 'user');
  if (pos != null && !inside) view.dispatch({ selection: { anchor: pos } });
  // The range Copy, Cut and Paste act on: a selection kept, else none.
  const range = inside || (!sel.empty && pos == null) ? { from: sel.from, to: sel.to } : null;
  const items = await itemsFor(view, range || { from: at, to: at, empty: true }, wasInsert);
  if (seq !== openSeq || menuOpen()) return;
  show(view, items, xy);
}

// ---- the items ----

// range is what Copy, Cut and Paste act on: the selection kept, or with
// empty the place a paste goes. Each item is {label, key, off, run, stay}: off is why it can't be done
// (dimmed), stay that the editor keeps focus after it. A '-' separates
// groups.
async function itemsFor(view, range, wasInsert) {
  const plain = !!S.plainFile;
  const action = !plain && S.view ? S.view : null;
  const file = SRC.file;
  // A Python file's definitions are pyrefly's (python.js), with or without
  // an action; it has no SQL refs.
  const python = !!(SRC.view && SRC.view.lang === 'python');
  const ro = !SRC.view ? 'no file loaded' : SRC.view.readOnly ? 'read-only: ' + SRC.view.readOnly : '';

  const nav = [
    { label: 'Go to Definition', key: vimKey('vimDefinition'), off: plain && !python ? "needs an action's file" : '', run: () => hooks.sourceDefinition && hooks.sourceDefinition(view), stay: true },
    { label: 'Show Card', key: vimKey('vimHover'), run: () => hooks.sourceHover && hooks.sourceHover(view), stay: true },
  ];

  const git = [];
  if (hunkUnder(view)) {
    git.push({ label: 'Stage Change', key: vimKey('vimStageHunk'), run: () => stageHunk(view, say), stay: true });
    git.push({ label: 'Revert Change', key: vimKey('vimUndoHunk'), off: ro, run: () => undoHunk(view, say), stay: true });
  }

  // The table items take the ref under the cursor, else this file's action
  // (as y and u do).
  const ref = python ? null : await refUnder(view).catch(() => null);
  const name = ref ? '`' + [ref.project, ref.dataset, ref.table].filter(Boolean).join('.') + '`' : action ? '`' + action.id + '`' : '';
  const url = ref ? ref.consoleUrl : action ? action.consoleUrl : '';
  const table = [
    { label: 'Copy Table Name', key: ref ? '' : 'y', off: name ? '' : 'no ref or action here', run: () => copy(name, 'copied ' + name), stay: true },
    { label: 'Open in BigQuery', key: ref ? '' : 'u', off: url ? '' : name ? noTable(ref ? ref.kind : action.kind) : 'no ref or action here', run: () => { openLink(url); say('opening ' + name + ' in BigQuery'); }, stay: true },
  ];

  // A library file gd showed is named by its path already.
  const paths = isLibraryFile(file)
    ? [{ label: 'Copy Path', run: () => copy(file, 'copied ' + file), stay: true }]
    : [
      { label: 'Copy Path', run: () => { const abs = absPath(file); copy(abs, 'copied ' + abs); }, stay: true },
      { label: 'Copy Relative Path', key: 'Y', run: () => copy(file, 'copied ' + file), stay: true },
    ];

  const clip = [
    { label: 'Cut', key: mod('x'), off: ro, run: () => cut(view, range, true), stay: true },
    { label: 'Copy', key: mod('c'), run: () => cut(view, range, false), stay: true },
    { label: 'Paste', key: mod('v'), off: ro, run: () => paste(view, range, wasInsert), stay: true },
  ];

  // The whole file's SQL and config block, whatever the click was on (ADR 0024).
  const fmt = [{ label: 'Format File', key: ':format', off: hooks.unformatable ? hooks.unformatable() : '', run: () => hooks.formatSource && hooks.formatSource(), stay: true }];

  return [...nav, '-', ...(git.length ? [...git, '-'] : []), ...table, '-', ...paths, '-', ...fmt, '-', ...clip];
}

// noTable is why there is no table to open: an operation without
// hasOutput, an ephemeral model and the like build none.
function noTable(kind) {
  if (kind === 'operation') return 'no table: an operation without hasOutput';
  return kind ? 'no table: a ' + kind + ' has none' : 'no table to open';
}

// cut copies the range kept, or the word at the cursor, as y does; with
// take (Cut) it takes it out too, as d does.
function cut(view, range, take) {
  let r = range;
  if (r.empty) {
    const w = view.state.wordAt(range.from);
    if (!w) { say('nothing to ' + (take ? 'cut' : 'copy') + ' here'); return; }
    r = { from: w.from, to: w.to };
  }
  leaveVisual(view);
  const text = clipCopy(view, r.from, r.to, take);
  const n = Array.from(text).length;
  say((take ? 'cut ' : 'copied ') + n + ' character' + (n === 1 ? '' : 's'));
}

// paste puts the clipboard's text (or the register's) at the cursor, in
// place of the range kept, and goes back to insert mode when the menu was
// opened in it.
async function paste(view, range, wasInsert) {
  const text = await clipText();
  if (!text) { say('nothing to paste'); return; }
  const r = range;
  leaveVisual(view);
  if (wasInsert) Vim.handleKey(getCM(view), 'i', 'user');
  view.dispatch({ changes: { from: r.from, to: r.to, insert: text }, selection: { anchor: r.from + text.length }, userEvent: 'input.paste', scrollIntoView: true });
}

function leaveVisual(view) {
  const adapter = getCM(view);
  if (adapter && adapter.state.vim && adapter.state.vim.visualMode) Vim.handleKey(adapter, '<Esc>', 'user');
}

// ---- the menu ----

// show opens the menu (ctx-menu.js); an item with stay gives the editor
// focus back before it runs, so the mode and selection are as set.
function show(view, items, xy) {
  showMenu(items, xy, { scrollEl: view.scrollDOM, onPick: (it) => { if (it.stay) view.focus(); } });
}
