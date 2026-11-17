// What databricks.js draws: the list of SQL warehouses to pick the one a
// Databricks project's checks run on.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw, hooks } from './state.js';
import { W, choose, closePicker } from './databricks.js';
import { Modal } from './view.js';

const html = htm.bind(h);

const grab = (e) => e && !e.contains(document.activeElement) && e.focus();

// stateWord says what a check on the warehouse costs in waiting.
function stateWord(w) {
  if (w.state === 'RUNNING') return html`<span class="ok small">running</span>`;
  const start = w.serverless ? 'starts in seconds' : 'takes minutes to start';
  return html`<span class="dim small">${(w.state || '').toLowerCase()} · ${start}</span>`;
}

function WarehousePicker() {
  return html`<${Modal} title="SQL warehouse" onClose=${() => { closePicker(); redraw(); }}>
    <div class="editor-pick" tabindex="-1" ref=${grab}>
      <p>Checks and queries run on a SQL warehouse, which bills while it runs; a stopped one starts. Pick the warehouse they use:</p>
      <p class="dim small">Saved to the project's config file as databricksWarehouse.</p>
      ${W.loading ? html`<p class="dim">listing the workspace's SQL warehouses…</p>` : html`
        <div class="ed-list">
          ${W.list.map((w, i) => html`<div class=${'ed-row' + (i === W.cursor ? ' sel' : '')}
              onMouseMove=${() => { if (W.cursor !== i) { W.cursor = i; redraw(); } }}
              onClick=${() => choose(i)}>
            <span class="lead">${i === W.cursor ? '▸' : ''}</span>
            <span class="ed-name">${w.name}</span><code class="dim">${w.id}</code>
            ${w.size && html`<span class="dim small">${w.size}</span>`} ${stateWord(w)}
          </div>`)}
        </div>`}
      ${W.saving && html`<p class="warn">saving…</p>`}
      ${W.error && html`<p class="error">${W.error}</p>`}
      <div class="overlay-keys"><span class="keys">${[['enter', 'use it'], ['↑/↓', 'move'], ['esc', 'cancel']].map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>
    </div>
  <//>`;
}

hooks.dbxOverlay = () => html`<${WarehousePicker} />`;
