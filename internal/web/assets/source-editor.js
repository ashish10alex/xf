// The source pane's editor: the shared editor (editor.js) with the file's
// language (sqlx.js), and its saves and reloads (source.js). There is one,
// made the first time the pane shows and kept while the page lives: the
// pane moves it from place to place (zoomed, side by side) rather than
// making it again, so its unsaved text and undo history stay.

import * as cm from './vendor/codemirror.mjs';
import { createEditor, formatText, jumpToError } from './editor.js';
import { languageFor } from './sqlx.js';
import { gitHunks, jumpToHunk, undoHunk, stageHunk } from './git-hunks.js';import { SRC, setSourceEditor, save, discard, onEdit, onMode, onLeave, sourceJumps, altFile, altFileName } from './source.js';
import { S, redraw, setFocus, hooks, say, openPalette, post } from './state.js';
import { onContextMenu, onMenuKey, openAtCursor } from './source-menu.js';
import { stepBuffer, openBuffers, bufferCommand, deleteCommand } from './buffers.js';
import { goToFile, completeFileName } from './source-paths.js';

let made = null;

// formatable says why the file shown can't be formatted, or '' when it can.
export function unformatable() {
  const v = SRC.view;
  if (!v) return 'no file to format';
  if (v.readOnly) return v.file + " can't be formatted here: " + v.readOnly;
  if (S.boot && S.boot.backend === 'dbt') return "formatting is for Dataform projects: a dbt model's Jinja isn't hidden from sqlfluff";
  if (v.lang !== 'sqlx' && v.lang !== 'sql') return 'formatting is for .sqlx and .sql files';
  return '';
}

// formatSource formats the file in the pane, in the editor: its SQL and
// its config block.
export function formatSource() {
  const why = unformatable();
  if (why) { say(why); return; }
  if (!made) { say('no file to format'); return; }
  const file = SRC.file;
  formatText(made, (text) => post('/api/format', { file, text }), say);
}

hooks.formatSource = formatSource;
hooks.unformatable = unformatable;
hooks.formatCommands = () => (unformatable() ? [] : [['Format: file', '', formatSource]]);

// sourceCommands are the palette's commands for what vim's keys and ex
// commands do in the editor, so they can be done without vim too. Each
// moves the editor's cursor, and the keys go back to the editor after.
hooks.sourceCommands = () => {
  if (!made || !SRC.view) return [];
  const v = made.view;
  const out = [
    ['Go to: next git change in file', '', () => jumpToHunk(v, 1, say)],
    ['Go to: previous git change in file', '', () => jumpToHunk(v, -1, say)],
    ['Go to: next error in file', '', () => jumpToError(v, 1, say)],
    ['Go to: previous error in file', '', () => jumpToError(v, -1, say)],
    ['Go to: file named under the cursor', '', () => goToFile(v, false)],
  ];
  if (altFileName()) out.push(['Go to: the file before', '', altFile, altFileName()]);
  out.push(['Buffers: next buffer', '', () => stepBuffer(1)]);
  out.push(['Buffers: previous buffer', '', () => stepBuffer(-1)]);
  out.push(['Buffers: remove the buffer shown', '', () => deleteCommand('')]);
  if (SRC.dirty) out.push(['File: discard unsaved changes', '', discard, SRC.file]);
  out.push(['View: inline git blame', '', toggleBlame, blameOn ? 'on' : 'off']);
  return out;
};

// ---- inline blame (blame.js) ----

// Inline blame is on or off for this browser, off at first. Its module
// comes once it is on, into blameSlot.
const blameKey = 'xf-web:blame';
let blameOn = false;
try { blameOn = localStorage.getItem(blameKey) === '1'; } catch { /* no storage */ }
const blameSlot = new cm.Compartment();
let blameMod = null;

// blameShown asks for the blame of v, the file view shows, or clears it
// when off.
function blameShown(view, v) {
  if (!blameOn && !blameMod) return;
  const ask = (m) => m.refreshBlame(view, v ? v.file : '', v ? v.base : null, blameOn);
  if (blameMod) { ask(blameMod); return; }
  import('./blame.js').then((m) => {
    if (!blameMod) { blameMod = m; view.dispatch({ effects: blameSlot.reconfigure(m.inlineBlame()) }); }
    if (SRC.view === v) ask(m);
  }, (e) => say(e.message));
}

function toggleBlame() {
  blameOn = !blameOn;
  try { localStorage.setItem(blameKey, blameOn ? '1' : '0'); } catch { /* no storage */ }
  if (made) blameShown(made.view, SRC.view);
  say(blameOn ? 'inline git blame on' : 'inline git blame off');
}

// sourceEditorDOM is the editor's element, made on first use.
export function sourceEditorDOM() {
  if (!made) {
    const box = document.createElement('div');
    box.className = 'src-cm';
    made = mountSourceEditor(box);
    made.box = box;
  }
  return made;
}

function mountSourceEditor(parent) {
  const dbt = () => !!(S.boot && S.boot.backend === 'dbt');
  const ed = createEditor(parent, {
    readOnly: true,
    save: () => save(),
    keys: [
      { key: 'Mod-Shift-o', run: () => { if (hooks.openSymbols) hooks.openSymbols(); return true; }, preventDefault: true },
      { key: 'Mod-Shift-O', run: () => { if (hooks.openSymbols) hooks.openSymbols(); return true; }, preventDefault: true },
      { key: 'Ctrl-Shift-o', run: () => { if (hooks.openSymbols) hooks.openSymbols(); return true; }, preventDefault: true },
      { key: 'Ctrl-Shift-O', run: () => { if (hooks.openSymbols) hooks.openSymbols(); return true; }, preventDefault: true },
    ],
    // :e reads the file again; with unsaved changes it needs :e!.
    ex: {
      edit: (params) => {
        if (SRC.dirty && !(params && (params.argString || '').trim().startsWith('!'))) { say('unsaved changes: :e! discards them'); return; }
        discard();
      },
      symbols: () => { if (hooks.openSymbols) hooks.openSymbols(); },
      // The file's SQL (sqlfluff) and config block (ADR 0024); nothing is saved.
      format: () => formatSource(),
      fmt: () => formatSource(),
      // The buffers (buffers.js), as vim's.
      bnext: () => stepBuffer(1),
      bprevious: () => stepBuffer(-1),
      buffer: (params) => bufferCommand(params && params.argString),
      buffers: () => openBuffers(),
      ls: () => openBuffers(),
      bdelete: (params) => deleteCommand(params && params.argString),
    },
    exShort: { edit: 'e', format: 'fo', fmt: 'fmt', symbols: 'sym', bnext: 'bn', bprevious: 'bp', buffer: 'b', buffers: 'buffers', ls: 'ls', bdelete: 'bd' },
    leave: () => { SRC.editing = false; redraw(); },
    hover: (view) => (hooks.sourceHover ? hooks.sourceHover(view) : say('no table under the cursor')),
    definition: (view) => (hooks.sourceDefinition ? hooks.sourceDefinition(view) : say('no definition under the cursor')),
    hunk: (view, dir) => jumpToHunk(view, dir, say),
    undoHunk: (view) => undoHunk(view, say),
    stageHunk: (view) => stageHunk(view, say),
    menu: (view) => openAtCursor(view),
    format: () => formatSource(),
    goToFile: (view, line) => goToFile(view, line),
    completeFile: (view) => completeFileName(view),
    jumps: sourceJumps,
    // A switch of vimMode starts out of insert mode.
    vimChanged: (on) => { onMode('normal'); if (on) followMode(); },
    altFile: () => { if (hooks.altFile) hooks.altFile(); },
    buffer: (dir) => stepBuffer(dir),
    buffers: () => openBuffers(),
    bufferDelete: () => deleteCommand(''),
    files: () => { openPalette(); redraw(); },
    // gsh stages through Source Control (scm.js).
    // A right-click, the menu key and shift+F10 open the context menu
    // (source-menu.js).
    extensions: [cm.EditorView.domEventHandlers({ contextmenu: onContextMenu, keydown: onMenuKey }), gitHunks(say, (view, text, base) => (hooks.stageText ? hooks.stageText(view, text, base) : say('no git here'))), blameSlot.of([]), ...(hooks.sourceExtensions ? hooks.sourceExtensions() : [])],
    onFocus: () => { SRC.editing = true; setFocus('source'); redraw(); },
    onBlur: () => { SRC.editing = false; onLeave(); redraw(); },
    onUpdate: (u) => {
      if (u.docChanged) onEdit(u);
      if (hooks.sourceUpdate) hooks.sourceUpdate(u);
      // The declarations table (externals.js) follows the cursor's line.
      if (u.selectionSet && SRC.view && hooks.sourceCursor) {
        const head = u.state.selection.main.head, line = u.state.doc.lineAt(head);
        hooks.sourceCursor(SRC.file, line.number - 1, head - line.from);
      }
    },
    say,
  });
  hooks.sourceShown = (v) => {
    ed.setLanguage(languageFor(v.lang, dbt()));
    blameShown(ed.view, v);
  };
  // The file is as it was, but a commit may have changed its blame.
  hooks.sourceRechecked = () => blameShown(ed.view, SRC.view);
  // Autosave waits for the end of insert mode; without vim, for a pause
  // in typing (source.js). Vim switched on afresh is a new adapter.
  const followMode = () => { const adapter = cm.getCM(ed.view); if (adapter) adapter.on('vim-mode-change', (e) => onMode(e.mode)); };
  followMode();
  setSourceEditor(ed);
  return ed;
}
