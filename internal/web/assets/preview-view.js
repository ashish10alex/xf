// The log pane's preview tab: the selected action's rows, in Scratch's
// result panel (scratch-view.js).

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { onMac } from './state.js';
import { ResultPanel } from './scratch-view.js';
import { PV, answer, cancelPreview } from './preview.js';

const html = htm.bind(h);

export const previewKeyLabel = onMac ? '⌘enter' : 'ctrl+enter';

// previewTitle is what the tab's pane title says after the tabs.
export function previewTitle() {
  if (!PV.name || !(PV.run || PV.waiting || PV.error || PV.ask)) return '';
  return 'preview of ' + PV.name;
}

export function PreviewBody() {
  let body;
  if (PV.ask) {
    body = html`<div class="pad pv-ask">
      ${PV.ask.why.map((w) => html`<p class=${PV.ask.danger ? 'warn' : ''}>${w}</p>`)}
      <p><button class=${'run-btn' + (PV.ask.danger ? ' danger' : '')} onClick=${() => answer(true)}>preview anyway (y)</button>
        <button class="link" onClick=${() => answer(false)}>cancel (n)</button></p>
    </div>`;
  } else if (PV.waiting) {
    const what = { saving: 'saving the file…', compiling: 'compiling, to preview the SQL as saved…', starting: 'dry running, then starting the query…' }[PV.waiting];
    body = html`<div class="dim pad">${what}</div>`;
  } else if (PV.error) {
    body = html`<pre class="sc-error">${PV.error}</pre>`;
  } else if (!PV.run) {
    body = html`<div class="dim pad">${previewKeyLabel} in an action's file runs its query in BigQuery and shows the rows here</div>`;
  }
  if (body) return html`<div class="pv-body">${body}</div>`;
  return html`<div class="pv-body sc-result"><${ResultPanel} ctx=${PV} cancel=${() => cancelPreview(false)} csvName=${PV.name || 'preview'} /></div>`;
}
