// The source pane's editor: the shared editor (editor.js) with the file's
// language (sqlx.js), and its saves and reloads (source.js). There is one,
// made the first time the pane shows and kept while the page lives: the
// pane moves it from place to place (zoomed, side by side) rather than
// making it again, so its unsaved text and undo history stay.

import * as cm from './vendor/codemirror.mjs';
import { createEditor, formatText } from './editor.js';
import { languageFor } from './sqlx.js';
import { gitHunks, jumpToHunk, undoHunk, stageHunk } from './git-hunks.js';
import { SRC, setSourceEditor, save, discard, onEdit, onMode, onLeave, sourceJumps } from './source.js';
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
hooks.formatCommands = () => (unformatable() ? [] : [['Format: file', ':format', formatSource]]);

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
    altFile: () => { if (hooks.altFile) hooks.altFile(); },
    buffer: (dir) => stepBuffer(dir),
    buffers: () => openBuffers(),
    bufferDelete: () => deleteCommand(''),
    files: () => { openPalette(); redraw(); },
    // gsh stages through Source Control (scm.js).
    // A right-click, the menu key and shift+F10 open the context menu
    // (source-menu.js).
    extensions: [cm.EditorView.domEventHandlers({ contextmenu: onContextMenu, keydown: onMenuKey }), gitHunks(say, (view, text, base) => (hooks.stageText ? hooks.stageText(view, text, base) : say('no git here'))), ...(hooks.sourceExtensions ? hooks.sourceExtensions() : [])],
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
  hooks.sourceShown = (v) => ed.setLanguage(languageFor(v.lang, dbt()));
  // Autosave waits for the end of insert mode.
  cm.getCM(ed.view).on('vim-mode-change', (e) => onMode(e.mode));
  setSourceEditor(ed);
  return ed;
}
