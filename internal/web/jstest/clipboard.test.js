import { test, before, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { shareClipboard, setLocalClipboard, clipText } from '../assets/editor.js';

const origFetch = globalThis.fetch;
const origClip = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

function setNavigatorClipboard(value) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, writable: true, value });
}

before(() => {
  shareClipboard();
});

afterEach(() => {
  setLocalClipboard(false);
  globalThis.fetch = origFetch;
  if (origClip) Object.defineProperty(navigator, 'clipboard', origClip);
  else delete navigator.clipboard;
  cm.Vim.getRegisterController().unnamedRegister.setText('');
});

// Minimal CM5-compatible adapter backed by real CodeMirror 6 EditorState + history,
// matching @replit/codemirror-vim's adapter so we exercise the real vendored Vim
// actions, undo grouping, and visual-mode replacement without a browser DOM.
function makeAdapter(doc, opts = {}) {
  let state = cm.EditorState.create({
    doc,
    extensions: [
      cm.EditorState.allowMultipleSelections.of(true),
      cm.history(),
      cm.EditorState.readOnly.of(!!opts.readOnly),
    ],
  });
  const historyUndo = cm.historyKeymap.find((k) => k.key === 'Mod-z').run;
  const userEvents = [];
  const handlers = {};

  const indexFromPos = (d, pos) => {
    const ln = Math.min(Math.max(1, pos.line + 1), d.lines);
    const line = d.line(ln);
    return Math.min(line.from + Math.max(0, pos.ch || 0), line.to);
  };
  const posFromIndex = (d, off) => {
    const line = d.lineAt(Math.min(Math.max(0, off), d.length));
    return { line: line.number - 1, ch: Math.min(Math.max(0, off), d.length) - line.from };
  };

  let adapter;
  const view = {
    get state() { return state; },
    destroyed: false,
    contentDOM: { addEventListener() {}, removeEventListener() {}, blur() {} },
    dispatch(...specs) {
      const tr = state.update(...specs);
      if (specs[0] && specs[0].userEvent) userEvents.push(specs[0].userEvent);
      state = tr.state;
      if (adapter) {
        if (tr.docChanged) {
          adapter.curOp = adapter.curOp || {};
          adapter.curOp.lastChange = true;
        }
        if (tr.selection) {
          adapter.curOp = adapter.curOp || {};
          adapter.curOp.cursorActivity = true;
        }
        if (adapter.curOp && !adapter.curOp.isVimOp) {
          adapter.onBeforeEndOperation();
        }
      }
    },
  };

  const dispatchChange = (spec) => {
    if (state.readOnly) return;
    let type = 'input.type.compose';
    if (adapter.curOp && !adapter.curOp.lastChange) type = 'input.type.compose.start';
    view.dispatch({ ...spec, userEvent: type });
  };

  adapter = {
    cm6: view,
    state: {},
    curOp: null,
    userEvents,
    on(t, f) { (handlers[t] ||= []).push(f); },
    off(t, f) { handlers[t] = (handlers[t] || []).filter((x) => x !== f); },
    signal(t, ...a) { for (const f of handlers[t] || []) f(...a); },
    getInputField: () => view.contentDOM,
    firstLine: () => 0,
    lastLine: () => state.doc.lines - 1,
    lineCount: () => state.doc.lines,
    getLine: (r) => (r < 0 || r >= state.doc.lines ? '' : state.doc.line(r + 1).text),
    getValue: () => state.doc.toString(),
    indexFromPos: (p) => indexFromPos(state.doc, p),
    posFromIndex: (i) => posFromIndex(state.doc, i),
    getOption: (k) => (k === 'tabSize' ? 4 : k === 'readOnly' ? state.readOnly : false),
    setOption(k, v) { this.state[k] = v; },
    toggleOverwrite(v) { this.state.overwrite = v; },
    getCursor(which) {
      const s = state.selection.main;
      const off = which === 'anchor' ? s.anchor : which === 'start' ? s.from : which === 'end' ? s.to : s.head;
      return posFromIndex(state.doc, off);
    },
    setCursor(line, ch) {
      const p = typeof line === 'object' ? line : { line, ch: ch || 0 };
      view.dispatch({ selection: { anchor: indexFromPos(state.doc, p) } });
    },
    listSelections: () => state.selection.ranges.map((r) => ({
      anchor: posFromIndex(state.doc, r.anchor),
      head: posFromIndex(state.doc, r.head),
    })),
    setSelections(sels, prim = 0) {
      const ranges = sels.map((x) => {
        const a = indexFromPos(state.doc, x.anchor);
        const h = indexFromPos(state.doc, x.head);
        return cm.EditorState.create({ doc: state.doc }).selection.main.constructor.range
          ? { anchor: a, head: h }
          : { anchor: a, head: h };
      });
      // Build selection ranges via state.update
      const first = ranges[0];
      view.dispatch({ selection: { anchor: first.anchor, head: first.head } });
    },
    setSelection(a, h) {
      view.dispatch({ selection: { anchor: indexFromPos(state.doc, a), head: indexFromPos(state.doc, h) } });
    },
    somethingSelected: () => state.selection.ranges.some((r) => !r.empty),
    getSelections: () => state.selection.ranges.map((r) => state.sliceDoc(r.from, r.to)),
    getSelection() { return this.getSelections().join('\n'); },
    getRange: (s, e) => state.sliceDoc(indexFromPos(state.doc, s), indexFromPos(state.doc, e)),
    replaceRange(text, s, e) {
      const from = indexFromPos(state.doc, s);
      const to = indexFromPos(state.doc, e || s);
      dispatchChange({ changes: { from, to, insert: text } });
    },
    replaceSelections(repls) {
      const changes = state.selection.ranges.map((r, i) => ({ from: r.from, to: r.to, insert: repls[i] || '' }));
      dispatchChange({ changes });
    },
    setBookmark(pos) {
      const saved = { ...pos };
      return { find: () => saved, clear() {} };
    },
    clipPos(p) {
      const ln = Math.min(Math.max(0, p.line), state.doc.lines - 1);
      const text = state.doc.line(ln + 1).text;
      return { line: ln, ch: Math.min(Math.max(0, p.ch), text.length) };
    },
    operation(fn) {
      if (!this.curOp) this.curOp = { $d: 0 };
      this.curOp.$d++;
      try {
        return fn();
      } finally {
        if (this.curOp) {
          this.curOp.$d--;
          if (!this.curOp.$d) this.onBeforeEndOperation();
        }
      }
    },
    onBeforeEndOperation() {
      this.curOp = null;
    },
    undo() {
      historyUndo({ state, dispatch: (tr) => { state = tr.state; } });
    },
  };
  cm.Vim.enterVimMode(adapter);
  return adapter;
}

const tick = () => new Promise((r) => setTimeout(r, 15));

test('local clipboard: p pastes from /api/clipboard without calling navigator.clipboard.readText', async () => {
  setLocalClipboard(true);
  let browserReadCalled = false;
  setNavigatorClipboard({
    readText: async () => { browserReadCalled = true; throw new Error('NotAllowedError'); },
    writeText: async () => {},
  });
  globalThis.fetch = async (url) => {
    assert.equal(url, '/api/clipboard');
    return { ok: true, json: async () => ({ text: 'CLIP' }) };
  };

  const a = makeAdapter('hello world');
  cm.Vim.handleKey(a, 'p', 'user');
  await tick();
  assert.equal(a.getValue(), 'hCLIPello world');
  assert.equal(browserReadCalled, false, 'should not prompt browser clipboard-read when local /api/clipboard works');
});

test('visual mode p replaces selection with system clipboard and exits visual mode', async () => {
  setLocalClipboard(true);
  globalThis.fetch = async () => ({ ok: true, json: async () => ({ text: 'PASTED' }) });

  const a = makeAdapter('hello world');
  cm.Vim.handleKey(a, 'v', 'user');
  cm.Vim.handleKey(a, 'e', 'user');
  assert.equal(a.state.vim.visualMode, true);
  assert.equal(a.getSelection(), 'hello');

  cm.Vim.handleKey(a, 'p', 'user');
  await tick();
  assert.equal(a.getValue(), 'PASTED world');
  assert.equal(a.state.vim.visualMode, false);
});

test('visual mode P replaces selection and preserves register (Neovim v_P)', async () => {
  setLocalClipboard(false);
  setNavigatorClipboard({ readText: async () => 'REPL', writeText: async () => {} });

  const a = makeAdapter('first second');
  cm.Vim.handleKey(a, 'v', 'user');
  cm.Vim.handleKey(a, 'e', 'user');
  cm.Vim.handleKey(a, 'P', 'user');
  await tick();
  assert.equal(a.getValue(), 'REPL second');
  assert.equal(a.state.vim.visualMode, false);
  assert.equal(cm.Vim.getRegisterController().unnamedRegister.toString(), 'REPL');
});

test('visual line V + p replaces the line with clipboard text', async () => {
  setLocalClipboard(true);
  globalThis.fetch = async () => ({ ok: true, json: async () => ({ text: 'replaced\r\n' }) });

  const a = makeAdapter('one\ntwo\nthree');
  cm.Vim.handleKey(a, 'V', 'user');
  cm.Vim.handleKey(a, 'p', 'user');
  await tick();
  assert.equal(a.getValue(), 'replaced\ntwo\nthree');
  assert.equal(a.state.vim.visualMode, false);
});

test('async clipboard paste keeps its own undo step and supports dot repeat', async () => {
  setLocalClipboard(true);
  globalThis.fetch = async () => ({ ok: true, json: async () => ({ text: 'ZZ' }) });
  cm.Vim.getRegisterController().unnamedRegister.setText('OLD');

  const a = makeAdapter('abcdef');
  cm.Vim.handleKey(a, 'x', 'user');
  assert.equal(a.getValue(), 'bcdef');

  cm.Vim.handleKey(a, 'p', 'user');
  await tick();
  assert.equal(a.getValue(), 'bZZcdef');
  // The paste dispatched as a new undo step ('input.type.compose.start'), not merged into 'x'.
  assert.equal(a.userEvents.at(-1), 'input.type.compose.start');
  a.undo();
  assert.equal(a.getValue(), 'bcdef', 'undoing paste leaves the earlier x deletion intact');

  // Dot repeat pastes 'ZZ' rather than the stale 'OLD' register.
  cm.Vim.handleKey(a, '.', 'user');
  assert.equal(a.getValue(), 'bZZcdef');
});

test('falls back to navigator.clipboard if /api/clipboard fails, and to register if both fail', async () => {
  setLocalClipboard(true);
  globalThis.fetch = async () => ({ ok: false, status: 500 });
  setNavigatorClipboard({ readText: async () => 'FROM_BROWSER', writeText: async () => {} });

  const a = makeAdapter('ab');
  cm.Vim.handleKey(a, 'p', 'user');
  await tick();
  assert.equal(a.getValue(), 'aFROM_BROWSERb');

  // When both fail, fall back to vim's unnamed register.
  setNavigatorClipboard({ readText: async () => { throw new Error('NotAllowedError'); }, writeText: async () => {} });
  cm.Vim.getRegisterController().unnamedRegister.setText('REG');
  const b = makeAdapter('xy');
  cm.Vim.handleKey(b, 'p', 'user');
  await tick();
  assert.equal(b.getValue(), 'xREGy');
});

test('clipText uses /api/clipboard when local and normalizes CRLF', async () => {
  setLocalClipboard(true);
  globalThis.fetch = async () => ({ ok: true, json: async () => ({ text: 'a\r\nb' }) });
  assert.equal(await clipText(), 'a\nb');
});

test('yank falls back to POST /api/clipboard when navigator.clipboard.writeText fails', async () => {
  setLocalClipboard(true);
  const posted = [];
  globalThis.fetch = async (url, init) => {
    posted.push({ url, method: init.method, body: JSON.parse(init.body) });
    return { ok: true, json: async () => ({ ok: true }) };
  };
  setNavigatorClipboard({ writeText: async () => { throw new Error('NotAllowedError'); } });

  cm.Vim.getRegisterController().pushText('"', 'yank', 'hello', false, false);
  await tick();
  assert.deepEqual(posted, [{ url: '/api/clipboard', method: 'POST', body: { text: 'hello' } }]);
});
