// The colour theme's button and picker, on every page: the project, the
// list of projects, the Scratch view and the settings.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw, keyName } from './state.js';
import * as th from './theme.js';

const html = htm.bind(h);
const { T } = th;

// ThemeButton is ◐ and the theme shown; a click opens the picker.
export function ThemeButton() {
  return html`<button class="link theme-toggle" title="Colour theme · click to pick (View: colour theme)"
    onClick=${() => { th.openThemePicker('theme'); redraw(); }}>◐ ${th.themeLabel()}</button>`;
}

// keepInView scrolls the list so row shows, never the page.
function keepInView(row) {
  const box = row.parentElement;
  if (row.offsetTop < box.scrollTop) box.scrollTop = row.offsetTop;
  else if (row.offsetTop + row.offsetHeight > box.scrollTop + box.clientHeight) box.scrollTop = row.offsetTop + row.offsetHeight - box.clientHeight;
}

// ThemePicker lists the themes, showing the one under the cursor as it
// moves: enter keeps it, esc goes back to the one chosen. The page stays
// in view around it, for the preview.
export function ThemePicker() {
  const p = T.pick;
  if (!p) return null;
  const rows = th.themeRows();
  const close = () => { th.closeThemePicker(); redraw(); };
  return html`<div class="overlay-back theme-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) close(); }}>
    <div class="overlay pane focused theme-picker">
      <header class="pane-title"><span class="pane-name">${p.title}${th.canScope() ? ' · ' + (p.scope === 'project' ? 'this project' : 'every project') : ''}</span></header>
      <div class="pane-body">
        <div class="query">
          <span class="prompt">></span>
          <input value=${p.query} placeholder="find theme" spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
            ref=${(e) => e && document.activeElement !== e && e.focus()}
            onInput=${(e) => { th.setThemeQuery(e.currentTarget.value); redraw(); }}
            onKeyDown=${(e) => {
              const k = keyName(e);
              if (k === 'esc' || k === 'ctrl+c') { e.preventDefault(); close(); }
              else if (k === 'tab' && th.canScope()) { e.preventDefault(); th.toggleScope(); redraw(); }
              else if (k === 'enter') { e.preventDefault(); th.pickTheme(); redraw(); }
              else if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); th.moveTheme(-1); redraw(); }
              else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); th.moveTheme(1); redraw(); }
              else if (k === 'pgup') { e.preventDefault(); th.moveTheme(-10); redraw(); }
              else if (k === 'pgdown') { e.preventDefault(); th.moveTheme(10); redraw(); }
            }} />
          <span class="dim count">${rows.length}</span>
        </div>
        <div class="scroll theme-list">
          ${!rows.length && html`<div class="dim pad">no matching theme</div>`}
          ${rows.map((r, i) => html`<div class=${'theme-row' + (i === p.cursor ? ' sel' : '')} key=${r.id}
              ref=${(e) => { if (e && i === p.cursor) keepInView(e); }}
              onMouseMove=${() => { if (th.hoverTheme(i)) redraw(); }}
              onMouseDown=${(e) => { e.preventDefault(); th.pickTheme(i); redraw(); }}>
            <span class="lead">${i === p.cursor ? '▸' : ''}</span>
            ${r.id === 'auto' ? html`<span class="swatch auto">◐</span>` : html`<span class="swatch" data-theme=${r.id}><i class="s-bg"></i><i class="s-kw"></i><i class="s-fn"></i><i class="s-str"></i><i class="s-focus"></i></span>`}
            <span class="theme-name">${r.name}</span>
            <span class="dim theme-note">${r.note}</span>
            <span class="faint theme-kind">${r.kind}</span>
          </div>`)}
        </div>
        <div class="overlay-keys"><span class="keys">${[['enter', 'keep'], ['↑/↓', 'preview'], ...(th.canScope() ? [['tab', p.scope === 'project' ? 'for every project' : 'for this project']] : []), ['esc', 'cancel']].map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span>
          ${p.slot !== 'theme' && html`<span class="faint">auto shows it while the browser is ${p.slot}</span>`}</div>
      </div>
    </div>
  </div>`;
}
