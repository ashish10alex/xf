// Source Control's difference in the source pane (ADR 0007): what a file
// is compared with on the left, read-only, and the file on the right, the
// two lined up and scrolling together; or inline, the right side alone
// with the lines taken out shown above the lines that replaced them. A
// change to a file of the project has the source pane's own editor on the
// right, which edits the file; the rest (a staged file, a commit's, a
// stash's) are read-only on both sides. Outside the editor, t flips the
// view and escape ends it; in it, escape stays vim's.

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef, useState } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import * as cm from './vendor/codemirror.mjs';
import { S, redraw, say } from './state.js';
import { createEditor, holdUnwrapped } from './editor.js';
import { languageFor } from './sqlx.js';
import { gitHunks, oldSide, setOld, onHunks, setBase, baseOf, hunksOf, setDiffMode, jumpToHunk, stageHunk } from './git-hunks.js';
import { sourceEditorDOM } from './source-editor.js';
import * as sc from './scm.js';

const html = htm.bind(h);

// langOf is a path's language, as the server's langOf names it.
function langOf(path) {
  const ext = (path.match(/\.[^./]+$/) || [''])[0].toLowerCase();
  return { '.sqlx': 'sqlx', '.js': 'js', '.sql': 'sql', '.json': 'json', '.ipynb': 'json', '.yaml': 'yaml', '.yml': 'yaml',
    '.toml': 'toml', '.py': 'python', '.sh': 'shell', '.bash': 'shell', '.zsh': 'shell',
    '.tf': 'hcl', '.tfvars': 'hcl', '.hcl': 'hcl' }[ext] || '';
}
const dbt = () => !!(S.boot && S.boot.backend === 'dbt');

// narrowAt is the width under which the difference shows inline.
const narrowAt = 720;

// syncScroll keeps b scrolled as a is, and a as b.
function syncScroll(a, b) {
  let busy = false;
  const follow = (from, to) => () => {
    if (busy) return;
    busy = true;
    to.scrollTop = from.scrollTop;
    requestAnimationFrame(() => { busy = false; });
  };
  const ab = follow(a, b), ba = follow(b, a);
  a.addEventListener('scroll', ab);
  b.addEventListener('scroll', ba);
  return () => { a.removeEventListener('scroll', ab); b.removeEventListener('scroll', ba); };
}

// readOnly is a read-only editor of text in parent.
function readOnly(parent, path, extra) {
  const ed = createEditor(parent, { readOnly: true, say, ...extra });
  ed.setLanguage(languageFor(langOf(path), dbt()));
  return ed;
}

export function DiffPane({ d, children }) {
  const wrap = useRef(null);
  const oldBox = useRef(null);
  const newBox = useRef(null);
  const [width, setWidth] = useState(0);
  const mode = width && width < narrowAt ? 'inline' : sc.diffMode();
  const eds = useRef({ old: null, neu: null });

  // The width decides side by side or inline.
  useLayoutEffect(() => {
    const el = wrap.current;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // A read-only difference's right side: the new text, compared with the
  // old.
  useLayoutEffect(() => {
    if (d.live) return undefined;
    const ed = readOnly(newBox.current, d.path, {
      extensions: [gitHunks(say, sc.diffStager(d))],
      hunk: (view, dir) => jumpToHunk(view, dir, say),
      stageHunk: (view) => stageHunk(view, say),
    });
    eds.current.neu = ed;
    return () => { ed.view.destroy(); eds.current.neu = null; };
  }, [d]);

  useLayoutEffect(() => {
    const ed = eds.current.neu;
    if (d.live || !ed) return;
    ed.set(d.v.new.text, true);
    setBase(ed.view, d.v.old.exists ? d.v.old.text : '');
  }, [d, d.key]);

  const right = () => (d.live ? sourceEditorDOM().view : eds.current.neu && eds.current.neu.view);

  // The mode, on the right side; the base's editor beside it.
  useLayoutEffect(() => {
    const view = right();
    if (!view) return undefined;
    setDiffMode(view, mode);
    if (mode !== 'side') return () => { if (d.live) setDiffMode(view, null); };
    // The sides line up a line at a time, so neither wraps.
    holdUnwrapped(view, true);
    const old = readOnly(oldBox.current, d.path, { extensions: [oldSide()], wrap: false });
    eds.current.old = old;
    let text = null;
    onHunks(view, (f) => {
      const base = f.base == null ? '' : f.base;
      if (base !== text) { text = base; old.set(base, true); }
      setOld(old.view, f.hunks);
    });
    const off = syncScroll(old.view.scrollDOM, view.scrollDOM);
    old.view.scrollDOM.scrollTop = view.scrollDOM.scrollTop;
    return () => {
      off();
      onHunks(view, null);
      old.view.destroy();
      eds.current.old = null;
      holdUnwrapped(view, false);
      if (d.live) setDiffMode(view, null);
    };
  }, [d, mode]);

  // A difference opens on its first change, as VS Code's does: a file
  // whose change is far down would otherwise look the same on both sides.
  useLayoutEffect(() => {
    const view = right();
    if (!view) return;
    const base = baseOf(view);
    const first = (hunksOf(base, view.state.doc) || [])[0];
    if (!first) return;
    const doc = view.state.doc;
    const line = Math.min(Math.max(first.added ? first.start + 1 : first.start, 1), doc.lines);
    const pos = doc.line(line).from;
    view.dispatch({ selection: { anchor: pos }, effects: cm.EditorView.scrollIntoView(pos, { y: 'center' }) });
  }, [d]);

  const v = d.v;
  const what = d.side === 'changes' ? 'index ⟷ working tree' : d.side === 'staged' ? 'HEAD ⟷ index' : (v.oldLabel || 'none') + ' ⟷ ' + v.newLabel;
  const path = d.file || d.path;
  return html`<div class=${'diff-pane ' + mode} ref=${wrap}>
    <div class="diff-bar">
      <span class="diff-what">${what}</span>
      <span class="dim diff-path">${path}${!d.live ? ' · read-only' : ''}</span>
      <span class="spacer"></span>
      ${!v.old.exists && html`<span class="dim">new file</span>`}
      ${!v.new.exists && html`<span class="dim">deleted</span>`}
      <button class="plain" title="side by side or inline (t)" disabled=${width && width < narrowAt}
        onMouseDown=${(e) => { e.preventDefault(); sc.toggleDiffMode(); }}>${mode === 'side' ? 'inline' : 'side by side'}</button>
      <button class="tree-close" title="end the difference (esc)" onMouseDown=${(e) => { e.preventDefault(); sc.endDiff(); }}>✕</button>
    </div>
    <div class="diff-cols">
      ${mode === 'side' && html`<div key="old" class="diff-col old" ref=${oldBox}></div>`}
      <div key="new" class="diff-col new" ref=${d.live ? null : newBox}>${d.live ? children : null}</div>
    </div>
  </div>`;
}
