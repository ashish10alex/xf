// The status line's count of the other browser windows on this xf web,
// and the list it opens, which brings one of them forward. See
// windows.js.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw } from './state.js';
import { W, windows, others, openWindows, closeWindows, bringForward } from './windows.js';

const html = htm.bind(h);

// ---- drag out to a new window ----

let suppressClickUntil = 0;
export const wasDragged = () => Date.now() < suppressClickUntil;

// startTearDrag tracks a pointer drag on a tab or header bar so pulling it
// away (or outside the browser window) pops it into its own side-by-side
// window on release, while keeping normal clicks untouched.
export function startTearDrag(e, { label = 'window', threshold = 32, verticalOnly = false, onDetach }) {
  if (e.button !== 0 || !onDetach) return;
  const el = e.currentTarget;
  const startX = e.clientX, startY = e.clientY;
  const pointerId = e.pointerId;
  let moved = false, detached = false, ghost = null;

  const ensureGhost = () => {
    if (ghost) return ghost;
    ghost = document.createElement('div');
    ghost.className = 'sc-drag-ghost';
    document.body.appendChild(ghost);
    return ghost;
  };
  const placeGhost = (ev, isDetached) => {
    const g = ensureGhost();
    g.classList.toggle('detached', isDetached);
    g.textContent = isDetached
      ? `↗ Drop to open ${label} in new window`
      : `Drag out to move ${label} to new window`;
    const vw = window.innerWidth, vh = window.innerHeight;
    const x = Math.min(Math.max(ev.clientX + 14, 12), Math.max(vw - 280, 12));
    const y = Math.min(Math.max(ev.clientY + 14, 12), Math.max(vh - 40, 12));
    g.style.left = x + 'px';
    g.style.top = y + 'px';
  };
  const cleanup = () => {
    window.removeEventListener('pointermove', onMove, true);
    window.removeEventListener('pointerup', onUp, true);
    window.removeEventListener('pointercancel', onCancel, true);
    window.removeEventListener('keydown', onKey, true);
    if (el && el.classList) el.classList.remove('dragging');
    document.body.classList.remove('sc-dragging');
    if (ghost) { ghost.remove(); ghost = null; }
    try { if (el && el.hasPointerCapture && el.hasPointerCapture(pointerId)) el.releasePointerCapture(pointerId); } catch { /* released */ }
  };
  const onMove = (ev) => {
    const dx = ev.clientX - startX, dy = ev.clientY - startY;
    if (!moved) {
      if (Math.hypot(dx, dy) < 8) return;
      moved = true;
      if (el && el.classList) el.classList.add('dragging');
      document.body.classList.add('sc-dragging');
      try { if (el && el.setPointerCapture) el.setPointerCapture(pointerId); } catch { /* unsupported */ }
    }
    const outside = ev.clientX < 4 || ev.clientY < 4 || ev.clientX > window.innerWidth - 4 || ev.clientY > window.innerHeight - 4;
    detached = outside || (verticalOnly ? Math.abs(dy) > threshold : Math.hypot(dx, dy) > threshold);
    placeGhost(ev, detached);
  };
  const onUp = (ev) => {
    const didMove = moved, didDetach = detached;
    cleanup();
    if (didMove) suppressClickUntil = Date.now() + 250;
    if (didDetach) onDetach({ screenX: ev.screenX, screenY: ev.screenY });
  };
  const onCancel = () => {
    if (moved) suppressClickUntil = Date.now() + 250;
    cleanup();
  };
  const onKey = (ev) => {
    if (ev.key === 'Escape') { ev.preventDefault(); ev.stopPropagation(); onCancel(); }
  };
  window.addEventListener('pointermove', onMove, true);
  window.addEventListener('pointerup', onUp, true);
  window.addEventListener('pointercancel', onCancel, true);
  window.addEventListener('keydown', onKey, true);
}

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
