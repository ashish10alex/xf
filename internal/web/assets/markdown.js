// A Markdown file in the source pane shows rendered in pane 2 (ADR 0027).
// The server renders it (internal/web/markdown.go) from the editor's
// text, so the preview follows typing, unsaved changes and all, a moment
// after each edit. markdown-view.js draws it.
//
// source.js loads this module the first time the pane shows a Markdown
// file.

import { S, post, redraw, hooks, openLink, say } from './state.js';
import { SRC, sourceEditor } from './source.js';
import './markdown-view.js';

export const MD = {
  file: '',  // the file html is of
  html: '',
  error: '',
};

// renderMs is how long after an edit the text goes to be rendered.
const renderMs = 150;

let seq = 0;
let timer = null;
// box is the preview's scroll box while it shows (markdown-view.js).
let box = null;
export const setBox = (el) => { box = el; };

const isMarkdown = () => !!(SRC.view && SRC.view.lang === 'markdown' && SRC.view.file === SRC.file);

async function render() {
  if (!isMarkdown()) return;
  const file = SRC.file;
  const ed = sourceEditor();
  const text = ed ? ed.doc() : SRC.view.text;
  const n = ++seq;
  try {
    const r = await post('/api/markdown', { file, text });
    if (n !== seq) return;
    Object.assign(MD, { file, html: r.html, error: '' });
  } catch (e) {
    if (n !== seq) return;
    Object.assign(MD, { file, error: e.message });
  }
  redraw();
}

// markdownShown is told of each file the source pane loads.
export function markdownShown(v) {
  clearTimeout(timer);
  if (v && v.lang === 'markdown') render();
}

// markdownEdited is told of each edit in the source pane.
function markdownEdited() {
  if (!isMarkdown()) return;
  clearTimeout(timer);
  timer = setTimeout(render, renderMs);
}

// shownHTML is the preview of the file the pane shows, '' before it has
// come.
export const shownHTML = () => (MD.file === SRC.file ? MD.html : '');
export const shownError = () => (MD.file === SRC.file ? MD.error : '');

// follow is a click on a link in the preview: a place in the file
// scrolls there, a file of the project opens in the source pane, a web
// page opens in the browser, and any other path is said to be missing.
export function follow(a) {
  const href = a.getAttribute('href') || '';
  if (href.startsWith('#')) {
    const to = box && box.querySelector('[id="md-' + CSS.escape(decodeURIComponent(href.slice(1))) + '"]');
    if (to) to.scrollIntoView({ block: 'start' });
    else say('no heading ' + href + ' in ' + SRC.file);
    return;
  }
  const file = a.dataset.file;
  if (file) {
    if (hooks.openFile) hooks.openFile(file, { edit: false });
    return;
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith('//')) openLink(a.href);
  else if (href) say('no file ' + href + ' in the project');
}

// markdownKey is the preview's keys while pane 2 has them: it scrolls,
// with the SQL pane's motions.
function markdownKey(k) {
  if (!box) return false;
  const line = 40, half = box.clientHeight / 2;
  const by = {
    j: line, down: line, 'ctrl+n': line, 'ctrl+e': line,
    k: -line, up: -line, 'ctrl+p': -line, 'ctrl+y': -line,
    'ctrl+d': half, pgdown: half,
    'ctrl+u': -half, pgup: -half,
  }[k];
  if (by) { box.scrollBy({ top: by }); return true; }
  if (k === 'g' || k === 'home') { box.scrollTop = 0; return true; }
  if (k === 'G' || k === 'end') { box.scrollTop = box.scrollHeight; return true; }
  return false;
}

hooks.markdownShown = markdownShown;
hooks.markdownEdited = markdownEdited;
hooks.markdownKey = markdownKey;

// The file that brought this module in.
if (S.plainFile && isMarkdown()) render();
