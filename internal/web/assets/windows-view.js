// The status line's count of the other browser windows on this xf web,
// and the list it opens, which brings one of them forward. See
// windows.js.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw } from './state.js';
import { W, windows, others, openWindows, closeWindows, bringForward } from './windows.js';

const html = htm.bind(h);

// WindowsPill is "⧉ 2 windows", this one counted, while there is another;
// a click opens the list. A lone window shows nothing.
export function WindowsPill() {
  const n = others();
  if (!n) return null;
  return html`<button class="link win-pill" title="Browser windows open on this xf web · click to list them and bring one forward"
    onClick=${(e) => { if (W.open) closeWindows(); else openWindows(e.currentTarget); }}>⧉ ${n + 1} windows</button>`;
}

// WindowsList lists the windows, oldest first, beside the pill.
export function WindowsList() {
  if (!W.open) return null;
  const list = windows();
  return html`<div class="overlay-back win-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) closeWindows(); }}>
    <div class="overlay pane focused win-list" style=${W.at}>
      <header class="pane-title"><span class="pane-name">browser windows</span></header>
      <div class="pane-body">
        <div class="scroll">
          ${list.map((w, i) => html`<div class=${'win-row' + (i === W.cursor ? ' sel' : '') + (w.self ? ' self' : '')} key=${w.id}
              onMouseMove=${() => { if (W.cursor !== i) { W.cursor = i; redraw(); } }}
              onMouseDown=${(e) => { e.preventDefault(); bringForward(w); }}>
            <span class="lead">${i === W.cursor ? '▸' : ''}</span>
            <span class="win-n">Window ${w.n}</span>
            <span class="win-label" title=${w.label}>${w.label || '…'}</span>
            ${w.self && html`<span class="faint">this window</span>`}
          </div>`)}
        </div>
        <div class="overlay-keys"><span class="keys">${[['enter', 'bring forward'], ['1–9', 'window'], ['esc', 'close']].map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>
      </div>
    </div>
  </div>`;
}
