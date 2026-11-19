// What databricks.js draws: the list of SQL warehouses and (for standalone
// Databricks scratch) ~/.databrickscfg profiles.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw, hooks } from './state.js';
import { W, P, choose, closePicker, chooseProfile, closeProfilePicker } from './databricks.js';
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
      <p class="dim small">${W.saveNote || "Saved to the project's config file as databricksWarehouse."}</p>
      ${W.loading ? html`<p class="dim">listing the workspace's SQL warehouses…</p>` : html`
        <div class="ed-list">
          ${W.list.map((w, i) => html`<div class=${'ed-row' + (i === W.cursor ? ' sel' : '')}
              onMouseMove=${() => { if (W.cursor !== i) { W.cursor = i; redraw(); } }}
              onClick=${() => choose(i)}>
            <span class="lead">${i === W.cursor ? '▸' : ''}</span>
            <span class="ed-name">${w.name}</span><code class="dim">${w.id}</code>
            ${w.size && html`<span class="dim small">${w.size}</span>`} ${stateWord(w)}
            ${W.current === w.id && html`<span class="ok small">active</span>`}
          </div>`)}
        </div>`}
      ${W.saving && html`<p class="warn">saving…</p>`}
      ${W.error && html`<p class="error">${W.error}</p>`}
      <div class="overlay-keys"><span class="keys">${[['enter', 'use it'], ['↑/↓', 'move'], ['esc', 'cancel']].map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>
    </div>
  <//>`;
}

function ProfilePicker() {
  return html`<${Modal} title="Databricks profile" onClose=${() => { closeProfilePicker(); redraw(); }}>
    <div class="editor-pick" tabindex="-1" ref=${grab}>
      ${P.loading ? html`<p class="dim">reading ~/.databrickscfg…</p>` : P.list.length ? html`
        <p>Pick a profile from <code>~/.databrickscfg</code> for standalone Databricks scratch:</p>
        <p class="dim small">Saved for standalone Databricks scratch; dbt projects keep using profiles.yml.</p>
        <div class="ed-list">
          ${P.list.map((p, i) => html`<div class=${'ed-row' + (i === P.cursor ? ' sel' : '')}
              onMouseMove=${() => { if (P.cursor !== i) { P.cursor = i; redraw(); } }}
              onClick=${() => chooseProfile(i)}>
            <span class="lead">${i === P.cursor ? '▸' : ''}</span>
            <span class="ed-name">${p.name}</span>
            <code class="dim">${p.host}</code>
            ${P.current === p.name && html`<span class="ok small">active</span>`}
          </div>`)}
        </div>` : html`
        <p>No Databricks workspace profile was found in <code>~/.databrickscfg</code>.</p>
        <p class="dim">Configure one with <code>databricks auth login --host &lt;workspace&gt;</code>, or set <code>DATABRICKS_HOST</code> and <code>DATABRICKS_TOKEN</code> before launching <code>xf web</code>.</p>
        ${P.host && html`<p class="dim small">Current workspace from environment: <code>${P.host}</code></p>`}`}
      ${P.saving && html`<p class="warn">switching…</p>`}
      ${P.error && html`<p class="error">${P.error}</p>`}
      <div class="overlay-keys"><span class="keys">${(P.list.length ? [['enter', 'use it'], ['↑/↓', 'move'], ['esc', 'cancel']] : [['esc', 'close']]).map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>
    </div>
  <//>`;
}

hooks.dbxOverlay = () => html`<${WarehousePicker} />`;
hooks.dbxProfileOverlay = () => html`<${ProfilePicker} />`;
