// The Scratch view's SQL editor: the shared editor (editor.js) with
// Scratch's completions, its run keys and its saves.
//
// cmd+enter (ctrl+enter elsewhere) runs the selection, else the whole file.

import * as cm from './vendor/codemirror.mjs';
import { createEditor, bigquerySQL, builtins, isBlock, formatText } from './editor.js';
import { completionExtensions, hoverAtCursor } from './scratch-complete.js';
import { loadFunctions } from './scratch-functions.js';
import { SC, setEditor, refreshMeta, scratchSay, onEdit, scheduleDry, runScratch, flushSave, cancelRun, isRunning } from './scratch.js';
import { redraw, post } from './state.js';

// scope is the SQL a run runs: the selection, else the whole file.
function scopeOf(view) {
  const sel = view.state.selection.main;
  if (!sel.empty) return view.state.sliceDoc(sel.from, sel.to);
  return view.state.doc.toString();
}

export function mountEditor(parent, onFocus) {
  const run = () => { runScratch(); return true; };

  const ed = createEditor(parent, {
    language: bigquerySQL([]),
    placeholder: 'SELECT … then cmd+enter runs it',
    extensions: [...completionExtensions()],
    keys: [
      { key: 'Mod-Enter', run },
      { key: 'Ctrl-Enter', run },
      { key: 'Ctrl-c', run: () => isRunning() && (cancelRun(), true) },
    ],
    // sqlfluff formats the query (ADR 0024); nothing is saved but by the
    // scratch's own autosave.
    ex: { run: () => runScratch(), refresh: () => refreshMeta(), format: () => format(), fmt: () => format() },
    exShort: { run: 'ru', refresh: 'ref', format: 'fo', fmt: 'fmt' },
    format: () => format(),
    save: () => flushSave(),
    leave: () => { SC.focus = 'results'; redraw(); },
    hover: (view) => hoverAtCursor(view).then((shown) => { if (!shown) scratchSay('no table or function under the cursor'); }),
    onFocus: () => { SC.focus = 'editor'; SC.leaveAsk = ''; onFocus && onFocus(); redraw(); },
    onUpdate: (u) => {
      if (u.docChanged) onEdit();
      else if (u.selectionSet) scheduleDry(400);
    },
    say: scratchSay,
  });
  const view = ed.view;
  function format() { formatText(ed, (text) => post('/api/format', { scratch: true, text }), scratchSay); }

  loadFunctions().then((F) => { if (F) ed.setLanguage(bigquerySQL(builtins(F.list))); });

  const handle = {
    doc: ed.doc,
    scope: () => scopeOf(view),
    block: () => isBlock(view),
    set: (text) => ed.set(text),
    // scopeOffset is where the scope begins in the text: 0 for the whole
    // file, the start of the selection else.
    scopeOffset() {
      const sel = view.state.selection.main;
      return sel.empty ? 0 : sel.from;
    },
    // mark underlines the place a SQL error names, with its message at the
    // end of the line, or clears the marks.
    mark(info) {
      let diags = [];
      if (info) {
        const lines = info.sql.split('\n');
        let off = 0;
        for (let i = 0; i < Math.min(info.line - 1, lines.length); i++) off += lines[i].length + 1;
        off += Math.max(info.col - 1, 0);
        const doc = view.state.doc;
        const from = Math.min(info.base + off, doc.length);
        const word = /^(?:[\w.`$-]+|\S)/.exec(doc.sliceString(from, Math.min(from + 80, doc.length)));
        diags = [{ from, to: Math.min(from + (word ? word[0].length : 1), doc.length), severity: 'error', message: info.message }];
        if (from >= doc.length && doc.length) diags = [{ from: doc.length - 1, to: doc.length, severity: 'error', message: info.message }];
      }
      ed.setMarks(diags);
    },
    focus: ed.focus,
    blur: ed.blur,
    // normal leaves vim's insert or visual mode, as esc does.
    normal() {
      const cmv = cm.getCM(view);
      const vim = cmv && cmv.state.vim;
      if (!vim) return;
      if (vim.insertMode) cm.Vim.exitInsertMode(cmv);
      else if (vim.visualMode) cm.Vim.exitVisualMode(cmv);
    },
    format,
    save: ed.save,
    restore: ed.restore,
    view,
  };
  setEditor(handle);
  return handle;
}

// mountRunSQL is the results' SQL tab: the SQL a run ran, read only, with
// vim's keys to move, search, select and yank part of it (a CTE to run on
// its own). ctrl+w gives the keys back to the results.
export function mountRunSQL(parent) {
  const ed = createEditor(parent, {
    language: bigquerySQL([]),
    readOnly: true,
    say: scratchSay,
    leave: () => { SC.focus = 'results'; redraw(); },
  });
  loadFunctions().then((F) => { if (F) ed.setLanguage(bigquerySQL(builtins(F.list))); });
  return ed;
}
