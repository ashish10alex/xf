// The list of the source pane's buffers (<leader>b, :ls): a row each, its
// number, % on the file shown and # on the alternate one, as vim's :ls,
// then the file as the palette's find file shows one: its name, the folder
// under it. Typing narrows it; enter goes to a buffer, del takes one out,
// esc gives the keys back to the editor.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { keyName } from './state.js';
import { BP, bufferRows, setBuffersQuery, moveBuffers, closeBuffers, pickBuffer, removeRow } from './buffers.js';
import { Modal, scrollNearest, underline } from './view.js';

const html = htm.bind(h);

const dirName = (path) => path.slice(0, Math.max(path.lastIndexOf('/'), 0));
const baseName = (path) => path.slice(path.lastIndexOf('/') + 1);

const keys = [['enter', 'go to'], ['del', 'remove'], ['↑/↓', 'move'], ['esc', 'close']];

export function BuffersOverlay() {
  const rows = bufferRows();
  const words = BP.query.toLowerCase().split(/\s+/).filter(Boolean);
  return html`<${Modal} title="buffers" onClose=${closeBuffers}>
    <div class="query">
      <span class="prompt">></span>
      <input value=${BP.query} placeholder="find buffer" spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
        ref=${(e) => e && document.activeElement !== e && e.focus()}
        onInput=${(e) => setBuffersQuery(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = keyName(e);
          const done = () => e.preventDefault();
          if (k === 'esc' || k === 'ctrl+c') { done(); closeBuffers(); }
          else if (k === 'enter') { done(); pickBuffer(); }
          else if (k === 'Delete') { done(); removeRow(); }
          else if (k === 'up' || k === 'ctrl+p') { done(); moveBuffers(-1); }
          else if (k === 'down' || k === 'ctrl+n') { done(); moveBuffers(1); }
        }} />
      <span class="dim count">${rows.length}</span>
    </div>
    <div class="scroll palette-list">
      ${rows.map((r, i) => html`<div class=${'pal-row two file buf-row' + (i === BP.cursor ? ' sel' : '')}
          key=${r.n} title=${[r.file, ...r.names].join(' · ')}
          ref=${(e) => { if (e && i === BP.cursor) scrollNearest(e); }}
          onMouseMove=${() => { if (BP.cursor !== i) { BP.cursor = i; moveBuffers(0); } }}
          onMouseDown=${(e) => { e.preventDefault(); pickBuffer(i); }}>
        <span class="dim buf-n">${r.n}</span>
        <span class=${'buf-mark ' + (r.cur ? 'ok' : 'dim')}>${r.cur ? '%' : r.alt ? '#' : ''}</span>
        <div class="pal-text">
          <div class="pal-name">${underline(baseName(r.file), words)}</div>
          ${r.file.includes('/') && html`<div class="pal-where">${underline(dirName(r.file), words)}</div>`}
        </div>
        ${r.names.length > 1 && html`<span class="dim pal-actions">${r.names.length} actions</span>`}</div>`)}
      ${!rows.length && html`<div class="dim pad">no matches</div>`}
    </div>
    <div class="changed-foot">
      <div class="overlay-keys"><span class="keys">${keys.map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>
    </div>
  <//>`;
}
