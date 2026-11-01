// The declarations table (externals.js): a row per declaration or source
// of the file, its full name and when its table was last updated, with copy,
// console and preview on the row under the mouse and the selected one.
// Above it, a card for the selected one (ADR 0019, moved by ADR 0025;
// full names by ADR 0026).

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { S, copyTarget, openTable, redraw, toggleLineage } from './state.js';
import { EXT, clickExternal, externalRows, freshness, groupOf, moveInExternals, previewExternal, setFilter, toggleCollapsed } from './externals.js';
import { scrollNearest } from './view.js';

const html = htm.bind(h);

function filterKey(e) {
  const stop = () => { e.preventDefault(); e.stopPropagation(); };
  switch (e.key) {
    case 'Escape':
      stop();
      if (EXT.filter) setFilter('');
      else e.currentTarget.blur();
      return;
    case 'ArrowDown': stop(); moveInExternals(1); return;
    case 'ArrowUp': stop(); moveInExternals(-1); return;
    case 'Enter': {
      stop();
      const row = externalRows().find((r) => r.id === S.sel);
      if (row) clickExternal(row);
    }
  }
}

const noMouse = (f) => (e) => { e.stopPropagation(); e.preventDefault(); f(); };

function RowActions({ row }) {
  const act = (f) => (e) => { e.stopPropagation(); e.preventDefault(); f(row); };
  return html`<span class="ext-actions">
    <button title="Copy the name" onMouseDown=${act(copyTarget)}>⧉</button>
    ${row.consoleUrl && html`<button title="Open in the BigQuery console" onMouseDown=${act(openTable)}>↗</button>`}
    ${row.consoleUrl && S.caps.scratch && html`<button title="Preview: SELECT * … LIMIT 100 in Scratch, to run" onMouseDown=${act(previewExternal)}>▶</button>`}
  </span>`;
}

// FullName is a row's project.dataset.table: the project dim and the
// first to give way when there is no room, then the dataset; the name is
// never cut.
function FullName({ row }) {
  const g = groupOf(row);
  if (!g) return html`<span class="ext-full"><span class="ext-full-name">${row.id}</span></span>`;
  const i = g.indexOf('.');
  return html`<span class="ext-full">${i >= 0 && html`<span class="ext-full-project dim">${g.slice(0, i + 1)}</span>`}<span class="ext-full-set dim">${g.slice(i + 1)}.</span><span class="ext-full-name">${row.name}</span></span>`;
}

// ExternalCard is the selected declaration: its name in full, how old its
// table is, its type, columns and users, and what to do with it.
function ExternalCard({ row }) {
  const v = S.view && S.view.id === row.id ? S.view : null;
  const f = freshness(row.updated);
  const cols = v && v.columns && !v.columns.reason ? v.columns.fields.length : null;
  const users = v ? v.lineage.down.length : null;
  const preview = row.consoleUrl && S.caps.scratch;
  const btn = (label, title, act) => html`<button class="ext-btn" title=${title} onMouseDown=${noMouse(act)}>${label}</button>`;
  return html`<div class="ext-card">
    <div class="ext-card-head">
      <span class="ext-card-name">${row.name}</span>
      ${v && v.tableType && html`<span class="ext-kind">${v.tableType.toLowerCase().replace(/_/g, ' ')}</span>`}
      ${preview && html`<button class="ext-btn primary" title="SELECT * … LIMIT 100 in Scratch, to run" onMouseDown=${noMouse(() => previewExternal(row))}>▶ Preview</button>`}
    </div>
    <div class="ext-card-id dim" title=${row.id}>${row.id}</div>
    <div class="ext-card-tags">
      ${f.tone !== 'none' && html`<span class=${'ext-tag ' + f.tone} title=${row.updated.value}><span class=${'fdot ' + f.tone}></span>${f.when ? 'updated ' + f.when + ' · ' + f.text : f.text}</span>`}
      ${cols != null && html`<span class="ext-tag">${cols} columns</span>`}
      ${users != null && html`<button class="ext-tag ext-tag-btn" title="Show the lineage (O)" onMouseDown=${noMouse(() => { toggleLineage(); redraw(); })}>used by ${users}</button>`}
      ${row.at && html`<span class="ext-tag">line ${row.at.line + 1}</span>`}
    </div>
    <div class="ext-card-btns">
      ${btn('⧉ Copy name', 'Copy the name', () => copyTarget(row))}
      ${row.consoleUrl && btn('↗ Console', 'Open in the BigQuery console', () => openTable(row))}
      ${row.at && btn('⤶ Go to line', 'Put the source pane\'s cursor on its name', () => clickExternal(row))}
    </div>
  </div>`;
}

export function ExternalsTable() {
  const all = S.view.externals;
  const rows = externalRows();
  const count = rows.length === all.length ? `${all.length}` : `${rows.length} of ${all.length}`;
  const header = html`<div class="ext-head">
    <span class="caret" onMouseDown=${(e) => { e.stopPropagation(); toggleCollapsed(); }}>${EXT.collapsed ? '▸' : '▾'}</span>
    <span class="ext-title" onMouseDown=${(e) => { e.stopPropagation(); toggleCollapsed(); }}>declarations in this file</span>
    <span class="dim ext-count">${count}</span>
    ${!EXT.collapsed && html`<input class="ext-filter" value=${EXT.filter} placeholder="Filter…" spellcheck="false" autocomplete="off"
      onMouseDown=${(e) => e.stopPropagation()} onInput=${(e) => setFilter(e.currentTarget.value)} onKeyDown=${filterKey} />`}
  </div>`;
  const picked = all.find((r) => r.id === S.sel);
  const card = picked && html`<${ExternalCard} row=${picked} />`;
  if (EXT.collapsed) return html`${card}<div class="externals collapsed">${header}</div>`;
  return html`${card}<div class="externals">
    ${header}
    <div class="ext-scroll">
      <table class="grid ext-table">
        <tbody>${rows.map((r) => {
          const sel = r.id === S.sel, f = freshness(r.updated);
          return html`<tr key=${r.id} class=${(sel ? 'sel' : '') + (r.at ? '' : ' noline')}
              title=${r.at ? r.id : 'no line names it (a loop builds the name), so the editor can\'t go to it'}
              onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); clickExternal(r); }}
              ref=${(el) => { if (el && sel) scrollNearest(el); }}>
            <td class="marker">${sel ? '▸' : ''}</td>
            <td class="ext-dot"><span class=${'fdot ' + f.tone} title=${f.tone === 'none' ? 'not looked up yet: select it' : ''}></span></td>
            <td class="mono ext-name"><${FullName} row=${r} /></td>
            <td class=${'ext-updated ' + (f.tone === 'err' ? 'error' : 'dim')} title=${r.updated ? r.updated.value : ''}>${f.text}</td>
            <td class="ext-act"><${RowActions} row=${r} /></td>
          </tr>`;
        })}</tbody>
      </table>
      ${!rows.length && html`<div class="dim pad">no declaration matches</div>`}
    </div>
  </div>`;
}
