// The dependency inspector's page (ADR 0033): the model and its upstream
// tables as rows or as a graph, the selected row's result below them in
// Scratch's grid, and its table's schema at the side. view.js loads this
// module when the page first opens.

import { h } from './vendor/preact.mjs';
import { useEffect, useRef, useState } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, redraw, onMac, keyName, copy } from './state.js';
import * as sc from './scratch.js';
import { ResultGrid } from './scratch-view.js';
import { Modal, ActionRow, Keys } from './view.js';
import * as ins from './inspector.js';

const html = htm.bind(h);
const { IN } = ins;
const SPIN = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const spin = () => SPIN[Math.floor(Date.now() / 90) % SPIN.length];

// useTick redraws every ms while on, for the spinners.
function useTick(on, ms = 120) {
  const [, set] = useState(0);
  useEffect(() => {
    if (!on) return undefined;
    const t = setInterval(() => set((n) => n + 1), ms);
    return () => clearInterval(t);
  }, [on, ms]);
}

export function InspectorPage() {
  useEffect(() => { document.title = 'xf · dependency inspector'; return () => { document.title = 'xf · ' + S.boot.name; }; }, []);
  const v = IN.v;
  useTick(ins.anyRunning() || Object.values(IN.dry).some((d) => d.pending) || !!(IN.confirm && IN.confirm.pending) || IN.loading);
  return html`<div class="app ins-page">
    <header class="set-top">
      <h1><span class="badge">XF</span> Dependency inspector</h1>
      <button class="ins-model ins-pick-model" title=${'Pick another model' + (v ? '\n\n' + v.model : '')} onClick=${() => { ins.openPicker(); redraw(); }}>
        <span class="ins-model-text">${v ? html`<b>${v.name}</b><span class="dim">${v.model}</span>` : html`<span class="dim">${IN.model || 'pick a model'}</span>`}</span>
        <span class="dim">▾</span></button>
      <span class="spacer"></span>
      ${IN.loading && html`<span class="dim">${spin()} walking…</span>`}
      ${v && html`<span class="ins-depth" title="How many levels upstream to walk (1 to 20)">depth
        <button class="ins-step" disabled=${v.depth <= 1} onClick=${() => ins.setDepth(v.depth - 1)}>−</button>
        <b>${v.depth}</b>
        <button class="ins-step" disabled=${v.depth >= 20} onClick=${() => ins.setDepth(v.depth + 1)}>+</button></span>`}
      ${v && html`<${PresetMenu} />`}
      <button class="link set-close" title="Close (ctrl+q)" onClick=${ins.closeInspector}>× close</button>
    </header>
    ${IN.error ? html`<div class="pad error">${IN.error}</div>` : v && html`<${Body} v=${v} />`}
    <div class="footer set-footer">
      <span class="keys"><span class="key"><kbd>tab</kbd> rows/graph</span><span class="key"><kbd>enter</kbd> in a filter: dry run</span><span class="key"><kbd>z</kbd> ${IN.zoom ? 'unzoom' : 'zoom the section clicked'}</span><span class="key"><kbd>${onMac ? '⌥⌘←/→' : 'ctrl+alt+←/→'}</kbd> results</span><span class="key"><kbd>ctrl+c</kbd> cancel runs</span><span class="key"><kbd>ctrl+q</kbd> back</span></span>
      <span class="spacer"></span>${S.flash && html`<span class="warn">${S.flash}</span>`}
    </div>
    ${IN.confirm && html`<${Confirm} c=${IN.confirm} />`}
    ${IN.pick && html`<${Picker} p=${IN.pick} />`}
  </div>`;
}

// focusCls marks the section z would zoom, the one last clicked.
const focusCls = (s) => (IN.focus === s ? ' ins-focus' : '');

function Body({ v }) {
  const rows = ins.tableNodes();
  const shown = ins.shownNodes();
  const z = IN.zoom;
  const ticked = ins.ticked().length;
  const running = ins.anyRunning();
  return html`<div class="ins-main">
    <div class="ins-filters">
      <label class="ins-global">
        <span class="ins-lbl">Filter</span>
        <textarea class="ins-in ins-wrap" rows="1" spellcheck="false" autocomplete="off" placeholder="e.g. order_id = 'A1' or created_date >= '2024-01-01'"
          title="enter dry runs every ticked row; shift+enter starts a new line; pasting tidies the spaces, as leaving the box does"
          value=${IN.global} onInput=${(e) => ins.setGlobal(e.currentTarget.value)}
          onPaste=${(e) => ins.pasteFilter(e, ins.setGlobal)}
          onBlur=${ins.tidyAll}
          onKeyDown=${(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ins.dryRunAll(); } }} />
      </label>
      <label class="ins-check" title="Every row uses the filter above; off, each row has its own"><input type="checkbox" checked=${IN.applyAll} onChange=${(e) => ins.setApplyAll(e.currentTarget.checked)} /> apply to all</label>
      <label class="ins-desc">
        <span class="ins-lbl">Description</span>
        <input class="ins-in" spellcheck="false" placeholder="optional, saved with a preset (e.g. QA debug for incident #123)"
          value=${IN.description} onInput=${(e) => ins.setDescription(e.currentTarget.value)} />
      </label>
    </div>
    <div class="ins-bar">
      <span class="set-tabs" role="group" aria-label="View">
        <button class=${'tab' + (IN.tab === 'rows' ? ' on' : '')} onClick=${() => { IN.tab = 'rows'; redraw(); }}>rows <span class="dim">${shown.length < rows.length ? shown.length + '/' : ''}${rows.length}</span></button>
        <button class=${'tab' + (IN.tab === 'graph' ? ' on' : '')} onClick=${() => { IN.tab = 'graph'; redraw(); }}>graph</button>
      </span>
      <span class="ins-search">
        <input class="ins-in" type="search" spellcheck="false" autocomplete="off" placeholder="search tables"
          title="Shows only the tables whose name or table has this; esc clears it"
          value=${IN.search} onInput=${(e) => ins.setSearch(e.currentTarget.value)}
          onKeyDown=${(e) => { if (e.key === 'Escape') { e.preventDefault(); if (IN.search) ins.setSearch(''); else e.currentTarget.blur(); } }} />
      </span>
      <label class="ins-check" title="Show only the ticked rows"><input type="checkbox" checked=${IN.tickedOnly} onChange=${(e) => ins.setTickedOnly(e.currentTarget.checked)} /> ticked only <span class="dim">${ticked}</span></label>
      <button class="link" title="Add a table that isn't upstream of the model, picked as the palette picks an action" onClick=${() => { ins.openPicker(true); redraw(); }}>+ add table</button>
      ${v.missing && v.missing.length > 0 && html`<span class="warn" title=${'Added by hand, but no action has these names now:\n' + v.missing.join('\n')}>
        ${v.missing.length} added table${v.missing.length === 1 ? '' : 's'} not found <button class="link" onClick=${ins.forgetMissing}>forget</button></span>`}
      ${v.defer && html`<span class=${'ins-defer' + (v.deferError ? ' warn' : '')} title="Rows read what their dry run reads: a table not built in dev is read from prod">
        defer to prod ${v.deferLoading ? html`<span class="dim">${spin()} working out what it reads from prod…</span>` : v.deferError ? '· ' + v.deferError : 'on'}</span>`}
      <span class="spacer"></span>
      <button class="run-btn" disabled=${!ticked} onClick=${ins.dryRunAll}>Dry run all <span class="dim">${ticked}</span></button>
      <button class="run-btn" disabled=${!ticked} onClick=${ins.askRunAll}>Run all <span class="dim">${ticked}</span></button>
      ${running && html`<button class="run-btn danger" onClick=${() => ins.cancelRuns()}><span class="verb">Cancel runs</span> <span class="dim">ctrl+c</span></button>`}
      ${z && html`<button class="link" title="Show every section again" onClick=${ins.zoom}>zoomed: ${z === 'top' ? IN.tab : z} · unzoom (z or esc)</button>`}
    </div>
    <div class=${'ins-split' + (z ? ' ins-zoomed' : '')}>
      ${z !== 'schema' && html`<div class="ins-left">
        ${z !== 'result' && html`<div class=${'ins-top' + focusCls('top')} onPointerDown=${() => ins.focusSection('top')}>
          ${IN.tab === 'rows' ? html`<${Rows} rows=${shown} all=${rows.length} />` : html`<${Graph} v=${v} />`}
        </div>`}
        ${z !== 'top' && html`<${Result} />`}
      </div>`}
      ${IN.schema && (!z || z === 'schema') && html`<${Schema} s=${IN.schema} />`}
    </div>
  </div>`;
}

// ---- rows ----

// Rows lists the rows the search shows, of all there are.
function Rows({ rows, all: total }) {
  const all = rows.length > 0 && rows.every((n) => IN.rows[n.key] && IN.rows[n.key].on);
  return html`<div class="ins-rows table-wrap">
    <table class="grid ins-table">
      <thead><tr>
        <th><input type="checkbox" title=${rows.length < total ? 'Tick or untick the rows shown' : 'Tick or untick every row'} checked=${all} onChange=${(e) => ins.setAllOn(e.currentTarget.checked)} /></th>
        <th class="num">depth</th><th>table</th><th>filter</th><th>dry run</th><th>run</th><th></th>
      </tr></thead>
      <tbody>${rows.map((n) => html`<${Row} key=${n.key} n=${n} />`)}</tbody>
    </table>
    ${!rows.length && html`<div class="dim pad">${!total ? 'nothing to query: the model has no table'
      : IN.search.trim() ? html`no ${IN.tickedOnly ? 'ticked ' : ''}table has “${IN.search.trim()}”: <button class="link" onClick=${() => ins.setSearch('')}>clear the search</button>`
        : html`no row is ticked: <button class="link" onClick=${() => ins.setTickedOnly(false)}>show them all</button>`}</div>`}
  </div>`;
}

function Row({ n }) {
  const r = IN.rows[n.key] || { on: true, filter: '' };
  const sel = IN.sel === n.key;
  return html`<tr class=${(sel ? 'sel' : '') + (r.on ? '' : ' ins-off')} onMouseDown=${() => { if (!sel) ins.select(n.key); }}>
    <td><input type="checkbox" checked=${r.on} onChange=${(e) => ins.setOn(n.key, e.currentTarget.checked)} /></td>
    <td class="num dim" title=${n.depth < 0 ? 'Added by hand: not upstream of the model' : ''}>${n.depth < 0 ? '+' : n.depth}</td>
    <td class="ins-name">
      <b title=${n.read}>${n.name}</b> <span class="dim">${n.kind}</span>
      ${n.label && html`<div class=${'ins-read t-' + n.tone} title=${n.hover}>${n.label}</div>`}
      ${n.depth === 0 && html`<div class="ins-read dim">the model</div>`}
      ${n.depth < 0 && html`<div class="ins-read dim">added by hand · <button class="link" title="Take it off the page" onClick=${(e) => { e.stopPropagation(); ins.removeTable(n.key); }}>remove</button></div>`}
    </td>
    <td class="ins-filter">
      <textarea class=${'ins-in ins-lines' + (!IN.applyAll && IN.global && r.filter === IN.global ? ' ins-same' : '')} rows=${Math.min(5, ins.filterOf(n.key).split('\n').length)} spellcheck="false" autocomplete="off" readonly=${IN.applyAll}
        title=${(IN.applyAll ? 'apply to all is on: the filter above is every row\'s' : (r.filter === IN.global ? 'the same as the filter above; ' : '') + 'enter dry runs it; shift+enter starts a new line') + (ins.filterOf(n.key) ? '\n\n' + ins.filterOf(n.key) : '')}
        placeholder="no filter (full table scan)" value=${ins.filterOf(n.key)}
        onInput=${(e) => ins.setRowFilter(n.key, e.currentTarget.value)}
        onPaste=${(e) => { if (!IN.applyAll) ins.pasteFilter(e, (t) => ins.setRowFilter(n.key, t)); }}
        onBlur=${(e) => { const t = ins.tidyFilter(e.currentTarget.value, true); if (!IN.applyAll && t !== r.filter) ins.setRowFilter(n.key, t); }}
        onKeyDown=${(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ins.dryRun(n.key); } }} />
    </td>
    <td class="ins-dry"><${Dry} k=${n.key} /></td>
    <td class="ins-run"><${RunCell} k=${n.key} /></td>
    <td class="ins-acts"><${Acts} n=${n} /></td>
  </tr>`;
}

function Dry({ k }) {
  const d = IN.dry[k];
  const now = ins.dryOf(k);
  const noFilter = !ins.filterOf(k).trim();
  if (d && d.pending) return html`<span class="dim">${spin()}</span>`;
  if (!now) {
    return html`<button class="link" onClick=${() => ins.dryRun(k)}>dry run</button>${noFilter ? html` <span class="warn" title="No filter: a full table scan">⚠</span>` : ''}`;
  }
  if (!now.ok) return html`<span class="error ins-err" title=${now.error}>✗ ${now.error}</span>`;
  const big = now.bytes >= now.confirmBytes, warn = now.bytes >= now.warnBytes;
  return html`<span class=${big ? 'error' : warn ? 'warn' : 'ok'} title=${now.accuracy === 'UNKNOWN' ? 'BigQuery could not estimate the bytes ahead' : now.cost ? '≈ ' + now.cost + ' on demand' : ''}>${now.text}</span>
    ${now.cost && html` <span class="dim">${now.cost}</span>`}${noFilter ? html` <span class="warn" title="No filter: a full table scan">⚠</span>` : ''}`;
}

function RunCell({ k }) {
  const r = ins.lastJob(k);
  if (!r) return html`<button class="link" onClick=${() => ins.runRow(k)}>run</button>`;
  const stale = r.sql !== ins.sqlOf(k) && !ins.isRunning(r);
  let what;
  if (r.phase === 'queued') what = html`<span class="dim">queued</span>`;
  else if (r.phase === 'running') what = html`<span class="warn">${spin()} running</span>`;
  else if (r.phase === 'failed') what = html`<span class="error ins-err" title=${r.error}>✗ failed</span>`;
  else if (r.phase === 'cancelled') what = html`<span class="dim">cancelled</span>`;
  else what = html`<span class=${r.rows ? 'ok' : 'warn'} title=${r.cost ? 'billed ' + r.cost : ''}>${r.rows.toLocaleString()} row${r.rows === 1 ? '' : 's'}</span>`;
  return html`${what}${stale && html` <span class="dim" title="The filter changed since: run again to see its rows">(older)</span>`}
    ${ins.isRunning(r)
      ? html` <button class="link" title="Cancel this query" onClick=${() => ins.cancelRuns(k)}>✕</button>`
      : html` <button class="link" onClick=${() => ins.runRow(k)}>${r.kept && !stale ? 'run again' : 'run'}</button>`}`;
}

function Acts({ n }) {
  const act = (label, title, f) => html`<button class="link ins-act" title=${title} onClick=${(e) => { e.stopPropagation(); f(n.key); }}>${label}</button>`;
  return html`${act('schema', 'Its columns', ins.openSchema)}
    ${act('scratch', 'Open the query in a new scratch', ins.openInScratch)}
    ${act('copy', 'Copy the query', ins.copyQuery)}
    ${n.url && act('bq', 'Open the table in the BigQuery console', ins.openTable)}
    ${n.file && act('file', 'Open ' + n.file + ' (leaves the page)', ins.openFile)}`;
}

// ---- the graph ----

const NODE_W = 230, NODE_H = 48, COL_W = 300, ROW_H = 66;

// layout puts each depth in a column, the model rightmost, ordering a
// column by where its readers sit so edges cross less; the tables added by
// hand, joined to none, get a column of their own right of the model.
function layout(v) {
  const byDepth = [], added = [];
  for (const n of v.nodes) {
    if (n.depth < 0) added.push(n);
    else (byDepth[n.depth] = byDepth[n.depth] || []).push(n);
  }
  const pos = {};
  const readers = {};
  for (const e of v.edges) (readers[e.to] = readers[e.to] || []).push(e.from);
  const maxD = byDepth.length - 1;
  byDepth.forEach((col, d) => {
    if (d > 0) {
      const at = (n) => {
        const ys = (readers[n.id] || []).map((id) => pos[id] && pos[id].i).filter((i) => i !== undefined);
        return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : 0;
      };
      col.sort((a, b) => at(a) - at(b));
    }
    col.forEach((n, i) => { pos[n.id] = { i, x: (maxD - d) * COL_W, y: i * ROW_H }; });
  });
  added.forEach((n, i) => { pos[n.id] = { i, x: (maxD + 1) * COL_W, y: i * ROW_H }; });
  const cols = added.length ? [...byDepth, added] : byDepth;
  const tallest = Math.max(...cols.map((c) => c.length));
  cols.forEach((col) => {
    const off = ((tallest - col.length) * ROW_H) / 2;
    for (const n of col) pos[n.id].y += off;
  });
  return { pos, w: (cols.length - 1) * COL_W + NODE_W, h: (tallest - 1) * ROW_H + NODE_H };
}

// nodeStatus is what a node says of its row, as the extension's graph
// does: its tone (the colour of its bar, tint and dot), a mark, a label and
// in full for its hover. The latest wins: a run, unless the filter changed
// since and the new one has a dry run.
function nodeStatus(n) {
  if (!n.table) return { tone: 'none', mark: '', label: n.kind + ' · no table' };
  const r = ins.lastJob(n.key), d = ins.dryOf(n.key);
  if (r && (ins.isRunning(r) || r.sql === ins.sqlOf(n.key) || !d)) {
    const older = r.sql !== ins.sqlOf(n.key) && !ins.isRunning(r) ? ' (older)' : '';
    if (r.phase === 'queued') return { tone: 'running', mark: '…', label: 'queued' };
    if (r.phase === 'running') return { tone: 'running', mark: '◌', label: 'running…' };
    if (r.phase === 'failed') return { tone: 'failed', mark: '✗', label: 'error · ' + r.error, full: r.error };
    if (r.phase === 'cancelled') return { tone: '', mark: '–', label: 'cancelled' };
    if (!r.rows) return { tone: 'empty', mark: '⚠', label: 'no rows returned' + older };
    return { tone: 'rows', mark: '✓', label: `${r.rows.toLocaleString()} row${r.rows === 1 ? '' : 's'}${r.cost ? ' · ' + r.cost : ''}${older}` };
  }
  if (d && d.pending) return { tone: 'running', mark: '◌', label: 'dry running…' };
  if (d && !d.ok) return { tone: 'failed', mark: '✗', label: 'dry run error · ' + d.error, full: d.error };
  if (d) return { tone: 'dry', mark: '✓', label: 'dry run ok · ' + d.text + (d.cost ? ' · ' + d.cost : '') };
  return { tone: '', mark: '', label: n.label || n.kind };
}

const clip = (s, n) => (s.length > n ? s.slice(0, n - 1) + '…' : s);

function Graph({ v }) {
  const box = useRef(null);
  const g = IN.graph = IN.graph || { x: 0, y: 0, k: 1, fit: '' };
  const { pos, w, h: ht } = layout(v);
  const sig = v.model + ':' + v.depth + ':' + v.nodes.length + ':' + IN.zoom;
  useEffect(() => {
    if (g.fit === sig || !box.current) return;
    // The tools' line at the bottom is kept clear.
    const r = box.current.getBoundingClientRect(), room = r.height - 34;
    const k = Math.min(1, (r.width - 40) / w, (room - 24) / ht);
    g.k = Math.max(k, 0.25);
    g.x = (r.width - w * g.k) / 2;
    g.y = Math.max(12, (room - ht * g.k) / 2);
    g.fit = sig;
    redraw();
  });
  const onWheel = (e) => {
    e.preventDefault();
    const r = box.current.getBoundingClientRect();
    const px = e.clientX - r.left, py = e.clientY - r.top;
    const k = Math.min(Math.max(g.k * Math.exp(-e.deltaY * 0.0015), 0.2), 2.5);
    g.x = px - ((px - g.x) * k) / g.k;
    g.y = py - ((py - g.y) * k) / g.k;
    g.k = k;
    redraw();
  };
  const onDown = (e) => {
    if (e.button !== 0 || e.target.closest('.ins-node')) return;
    const x0 = e.clientX, y0 = e.clientY, gx = g.x, gy = g.y;
    const move = (ev) => { g.x = gx + ev.clientX - x0; g.y = gy + ev.clientY - y0; redraw(); };
    const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };
  return html`<div class="ins-graph" ref=${box} onWheel=${onWheel} onPointerDown=${onDown}>
    <svg width="100%" height="100%">
      <defs><marker id="ins-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" class="ins-arrowhead" /></marker></defs>
      <g transform=${`translate(${g.x},${g.y}) scale(${g.k})`}>
        ${v.edges.map((e) => {
          const a = pos[e.to], b = pos[e.from];
          if (!a || !b) return null;
          const x1 = a.x + NODE_W, y1 = a.y + NODE_H / 2, x2 = b.x, y2 = b.y + NODE_H / 2, mx = (x1 + x2) / 2;
          const on = IN.sel && (ins.nodeOf(IN.sel) || {}).id;
          return html`<path class=${'ins-edge' + (on === e.to || on === e.from ? ' on' : '')} d=${`M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2 - 2},${y2}`} marker-end="url(#ins-arrow)" />`;
        })}
        ${v.nodes.map((n) => {
          const p = pos[n.id];
          const st = nodeStatus(n);
          const narrowed = ins.narrowed(), hit = narrowed && ins.matches(n);
          const x = st.mark ? 26 : 14;
          return html`<g class=${'ins-node t-' + (st.tone || 'idle') + (IN.sel === n.key ? ' sel' : '') + (n.table && !(IN.rows[n.key] || {}).on ? ' off' : '') + (narrowed ? (hit ? ' hit' : ' miss') : '')}
            transform=${`translate(${p.x},${p.y})`} onClick=${() => n.table && ins.select(n.key)}>
            <title>${n.read}${n.hover ? '\n' + n.hover : ''}${st.mark ? '\n\n' + (st.full ? st.label.slice(0, st.label.indexOf(' · ')) + ':\n' + st.full : st.label) : ''}</title>
            <rect class="ins-node-box" width=${NODE_W} height=${NODE_H} rx="6" />
            <rect class="ins-node-bar" width="4" height=${NODE_H - 8} x="1" y="4" rx="2" />
            ${n.depth <= 0 && html`<text x=${NODE_W - 10} y="15" text-anchor="end" class="ins-node-root">${n.depth === 0 ? 'model' : 'added'}</text>`}
            <text x="14" y="20" class="ins-node-name">${clip(n.name, n.depth <= 0 ? 22 : 27)}</text>
            ${st.mark && html`<text x="14" y="37" class="ins-node-mark">${st.mark}</text>`}
            <text x=${x} y="37" class="ins-node-sub">${clip(st.label, st.mark ? 28 : 30)}</text>
          </g>`;
        })}
      </g>
    </svg>
    <div class="ins-graph-tools">
      <button class="link" title="Fit the graph to the view" onClick=${() => { g.fit = ''; redraw(); }}>fit</button>
      <span class="dim">drag to pan · scroll to zoom · click a table to select it</span>
    </div>
  </div>`;
}

// ---- the result ----

function Result() {
  const k = IN.sel;
  const n = k && ins.nodeOf(k);
  const sec = { class: 'ins-result sc-result' + focusCls('result'), onPointerDown: () => ins.focusSection('result') };
  if (!n) return html`<section ...${sec}><div class="dim pad">select a row to see its result</div></section>`;
  const r = ins.lastJob(k);
  const ctx = ins.resultOf(k);
  const run = ctx.run;
  // The dry run of the query as typed now: its error in full, which the
  // row has room only to start, or what it would scan.
  const d = ins.dryOf(k);
  const dry = d && !d.pending && (d.ok
    ? !r && html` · the dry run says it scans <b>${d.text}</b>${d.cost ? ' (≈ ' + d.cost + ')' : ''}`
    : html`<pre class="sc-error ins-dry-err"><b>dry run failed:</b> ${d.error}</pre>`);
  let body;
  if (!r) body = html`<div class="dim pad">not run yet: <button class="link" onClick=${() => ins.runRow(k)}>run</button> it, or tick rows and Run all${d && d.ok ? dry : ''}</div>`;
  else if (r.phase === 'queued') body = html`<div class="dim pad">queued: up to 4 queries run at once</div>`;
  else if (r.phase === 'running') body = html`<div class="warn pad">${spin()} running in BigQuery…</div>`;
  else if (r.phase === 'cancelled') body = html`<div class="dim pad">the query was cancelled</div>`;
  else if (r.phase === 'failed') body = html`<pre class="sc-error">${r.error}</pre>`;
  else if (!run || run.phase === 'running' || run.phase === 'loading' || run.phase === 'starting') body = html`<div class="dim pad">${spin()} reading rows…</div>`;
  else if (run.phase === 'failed') body = html`<pre class="sc-error">${run.error}</pre>`;
  else body = html`<${ResultGrid} ctx=${ctx} />`;
  const job = r && r.job;
  // The row's query as typed now, a preview while a filter is written; a
  // result of an older one says so.
  const sql = ins.sqlOf(k);
  const keys = ins.resultKeys(), at = keys.indexOf(k);
  const cycleKey = onMac ? '⌥⌘' : 'ctrl+alt+';
  return html`<section ...${sec}>
    <div class="ins-result-head">
      ${keys.length > (at < 0 ? 0 : 1) && html`<span class="ins-cycle">
        <button class="ins-step" title=${'The result before (' + cycleKey + '←)'} onClick=${() => ins.cycleResult(-1)}>‹</button>
        <span class="dim">${at < 0 ? '–' : at + 1}/${keys.length}</span>
        <button class="ins-step" title=${'The next result (' + cycleKey + '→)'} onClick=${() => ins.cycleResult(1)}>›</button></span>`}
      <b>${n.name}</b>
      ${r && !ins.isRunning(r) && r.sql !== sql && html`<span class="warn" title=${'These rows are of the query run before the filter changed:\n\n' + r.sql}>rows of an earlier query: run again for this one</span>`}
      <span class="spacer"></span>
      ${r && r.phase === 'done' && html`<span class="dim">${r.rows.toLocaleString()} rows${r.cost ? ' · billed ' + r.cost : ''}${r.kept ? ' · kept from earlier' : ''}</span>`}
      <button class="link" title="Copy this query" onClick=${() => copy(sql, 'copied the query')}>copy</button>
      ${job && html`<a class="link" href=${sc.jobURL(job)} target="_blank" rel="noopener" title="Open the job in the BigQuery console">job ↗</a>`}
    </div>
    <pre class="ins-sql">${ins.sqlTokens(sql).map(([c, t]) => html`<span class=${c ? 't-' + c : ''}>${t}</span>`)}</pre>
    ${d && !d.ok && dry}
    ${body}
    ${ctx.confirm && html`<div class="ins-inline-ask warn">${ctx.confirm.why.join(' ')}
      <button class="run-btn danger" onClick=${() => { ctx.confirm = null; sc.searchAll(true, false, ctx); }}>search anyway</button>
      <button class="link" onClick=${() => { ctx.confirm = null; redraw(); }}>cancel</button></div>`}
  </section>`;
}

// ---- the schema panel ----

function flatten(fields, depth = 0, out = []) {
  for (const f of fields || []) {
    out.push({ f, depth });
    if (f.fields) flatten(f.fields, depth + 1, out);
  }
  return out;
}

function Schema({ s }) {
  const n = ins.nodeOf(s.key);
  return html`<aside class=${'ins-schema' + focusCls('schema')} onPointerDown=${() => ins.focusSection('schema')}>
    <div class="sc-detail-head"><b>${n ? n.name : s.table}</b> <span class="dim">schema</span>
      <span class="spacer"></span><button class="link" title="Close (esc)" onClick=${ins.closeSchema}>✕</button></div>
    <div class="dim ins-schema-table">${s.table}</div>
    <div class="table-scroll">
      ${s.loading ? html`<div class="dim pad">${spin()} reading its columns…</div>`
        : s.error ? html`<div class="error pad">${s.error}</div>`
          : html`<table class="grid">
            <thead><tr><th>Column</th><th>Type</th><th>Mode</th><th>Description</th></tr></thead>
            <tbody>${flatten(s.fields).map(({ f, depth }) => html`<tr>
              <td class="mono" style=${`padding-left:${6 + depth * 14}px`}>${f.name}</td><td class="dim">${f.type}</td>
              <td class="dim">${(f.mode || '').toUpperCase() === 'NULLABLE' ? '' : f.mode}</td><td class="desc">${f.description}</td></tr>`)}</tbody>
          </table>`}
    </div>
  </aside>`;
}

// ---- presets ----

function PresetMenu() {
  const cur = IN.presets.find((p) => p.id === IN.preset);
  return html`<span class="ins-presets">
    <button class="ins-model" title="Load a saved preset of this model's filters" onClick=${(e) => {
      // The menu is fixed under the button, inside the window.
      const r = e.currentTarget.getBoundingClientRect(), w = Math.min(480, innerWidth - 16);
      IN.presetsAt = `left:${Math.max(8, Math.min(r.left, innerWidth - w - 8))}px;top:${r.bottom + 6}px;width:${w}px`;
      IN.presetsOpen = !IN.presetsOpen;
      redraw();
    }}>
      preset: ${cur ? html`<b>${ins.presetName(cur)}</b>` : html`<span class="dim">none</span>`} <span class="dim">${IN.presets.length} ▾</span></button>
    <button class="run-btn" title=${cur ? 'Save the filters over ' + ins.presetName(cur) : 'Save the filters as a new preset'} onClick=${() => ins.savePreset(false)}>Save</button>
    ${cur && html`<button class="run-btn" title="Save the filters as a new preset" onClick=${() => ins.savePreset(true)}>Save as new</button>`}
    ${IN.presetsOpen && html`<div class="ins-menu-back" onMouseDown=${() => { IN.presetsOpen = false; redraw(); }}></div>`}
    ${IN.presetsOpen && html`<div class="ctx-menu ins-preset-list" style=${IN.presetsAt}>
      <div class="dim sc-recent-head" title=${IN.presetsPath}>saved presets of this model · yours, not in git</div>
      <div class="ctx-row" onClick=${() => ins.applyPreset(null)}><span class="ctx-label dim">none: keep what is typed, edit no preset</span></div>
      ${IN.presets.map((p) => html`<div class=${'ctx-row ins-preset' + (p.id === IN.preset ? ' cur' : '')} onClick=${() => ins.applyPreset(p)}>
        <span class="ctx-label"><b>${ins.presetName(p)}</b> <span class="dim">${sc.ago(Date.parse(p.updated))}</span>
          <div class="dim ins-preset-filter">${p.global || '(no global filter)'}</div></span>
        <button class="link" title="Delete this preset" onClick=${(e) => { e.stopPropagation(); ins.deletePreset(p.id); }}>delete</button>
      </div>`)}
      ${!IN.presets.length && html`<div class="dim pad">none saved yet: Save keeps the filters as one</div>`}
    </div>`}
  </span>`;
}

// ---- dialogs ----

function Confirm({ c }) {
  const one = c.keys.length === 1;
  const name = (k) => (ins.nodeOf(k) || { name: k }).name;
  const n = c.keys.length - (c.failed || []).length;
  return html`<${Modal} title=${one ? 'run ' + name(c.keys[0]) + '?' : `run ${c.keys.length} queries?`} cls="confirm amber" onClose=${ins.dismissConfirm}>
    ${c.pending ? html`<p class="dim">${spin()} dry running ${one ? 'it' : 'them'} first…</p>` : html`
      ${n > 0 && html`<p>${one ? 'It scans' : n < c.keys.length ? `The ${n} that can run scan` : 'They scan'} <b>${sc.fmtBytes(c.total)}</b>${c.cost ? html`, about <b>$${c.cost.toFixed(c.cost < 1 ? 3 : 2)}</b> on demand` : ''}${c.unknown ? ' (some sizes BigQuery could not estimate)' : ''}.</p>`}
      ${c.noFilter.length > 0 && html`<p class="warn">⚠ full scan, with no filter: ${c.noFilter.map(name).join(', ')}</p>`}
      ${c.failed.length > 0 && html`<p class="error">✗ their dry run failed, so they won't run: ${c.failed.map(name).join(', ')}</p>`}
      ${!one && html`<p class="dim">At most 4 run at once; ctrl+c cancels them.</p>`}`}
    <div class="ins-actions">
      <button class="run-btn danger" disabled=${c.pending || !n} onClick=${ins.confirmRun}><span class="verb">Run${one ? '' : ' ' + n}</span> <span class="dim">enter</span></button>
      <button class="link" onClick=${ins.dismissConfirm}>cancel <span class="dim">esc</span></button>
    </div>
  <//>`;
}

// Picker is the palette's find action, picking the model to inspect: the
// same query line, rows and keys.
function Picker({ p }) {
  const words = p.query.toLowerCase().split(/\s+/).filter((w) => w && !w.startsWith('$'));
  const move = (n) => { p.cursor = Math.min(Math.max(p.cursor + n, 0), Math.max(p.items.length - 1, 0)); redraw(); };
  const empty = p.loading ? 'loading…'
    : p.badKind ? (p.badKind === '$' ? 'kinds: ' : `no kind ${p.badKind}, only `) + S.boot.kinds.map((k) => '$' + k).join(' ') : 'no matches';
  return html`<${Modal} title=${p.add ? 'dependency inspector · add a table' : 'dependency inspector · pick a model'} cls="palette" onClose=${() => { IN.pick = null; }}>
    <div class="query">
      <span class="prompt">></span>
      <input value=${p.query} placeholder=${p.add ? 'find a table to add' : 'find a model'} spellcheck="false" autocomplete="off"
        ref=${(e) => e && document.activeElement !== e && e.focus()}
        onInput=${(e) => ins.findModels(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = keyName(e);
          if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); move(-1); }
          else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); move(1); }
          else if (k === 'pgup') { e.preventDefault(); move(-15); }
          else if (k === 'pgdown') { e.preventDefault(); move(15); }
          else if (k === 'enter' || k === 'esc' || k === 'ctrl+c') { e.preventDefault(); e.stopPropagation(); ins.inspectorKey(k === 'ctrl+c' ? 'esc' : k); }
        }} />
      <span class="dim count">${p.items.length}/${p.total}</span>
    </div>
    <div class="palette-list scroll">
      ${!p.items.length && html`<div class="dim pad">${empty}</div>`}
      ${p.items.map((it, i) => html`<${ActionRow} key=${it.id} it=${it} sel=${i === p.cursor} words=${words}
        onHover=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
        onPick=${() => ins.pickModel(it.id)} />`)}
    </div>
    <div class="overlay-keys"><${Keys} list=${[['enter', p.add ? 'add to the rows (one listed: tick it)' : 'inspect'], ['↑/↓', 'move'], ['$kind', 'find one kind'], ['esc', 'cancel']]} /></div>
  <//>`;
}
