// The Scratch view: scratch queries listed beside an editor, the dry run of
// what it holds, and the result in a grid.

import { h } from './vendor/preact.mjs';
import { useEffect, useRef, useState } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, redraw, hooks, openProjects, commandsKey } from './state.js';
import { ThemeButton } from './theme-view.js';
import { WindowsPill } from './windows-view.js';
import * as sc from './scratch.js';
import { mountEditor, mountRunSQL } from './scratch-editor.js';
import { savesWith } from './vim-mode.js';

const html = htm.bind(h);
const { SC, ROW_H } = sc;
const SPIN = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const MAX_COL = 48, MIN_COL = 8;

function useTick(on, ms = 100) {
  const [, set] = useState(0);
  useEffect(() => {
    if (!on) return undefined;
    const t = setInterval(() => set((n) => n + 1), ms);
    return () => clearInterval(t);
  }, [on, ms]);
}

// ---- the editor ----

function Editor() {
  const box = useRef(null);
  useEffect(() => {
    const e = mountEditor(box.current);
    if (SC.cur) e.set(SC.cur.sql);
    sc.scheduleDry(0);
    if (SC.focus === 'editor') e.focus();
    return () => { e.view.destroy(); sc.setEditor(null); };
  }, []);
  return html`<div class=${'sc-editor' + (SC.focus === 'editor' ? ' focused' : '')} ref=${box}></div>`;
}

// ---- the list ----

function List() {
  const when = (s) => {
    const d = new Date(s.modified);
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + d.toTimeString().slice(0, 5);
  };
  return html`<aside class="sc-list">
    <${Sash} which="list" axis="x" dir=${1} min=${140} max=${(el) => el.parentElement.getBoundingClientRect().width - 300} />
    <div class="sc-list-head"><span>scratches</span><span><button class="link" title="A new empty scratch" onClick=${() => sc.newDraft()}>+ empty</button></span></div>
    <div class="sc-list-body">
      ${SC.list.map((s, i) => html`<div class=${'sc-item' + (SC.cur && SC.cur.id === s.id ? ' sel' : '')} onClick=${() => { SC.listCursor = i; sc.selectScratch(s.id); }}>
        <div class="sc-item-top"><span class="sc-from" title=${s.preview}>${s.from || 'scratch'}</span>${SC.delAsk !== s.id && html`<span class="dim sc-when">${when(s)}</span>`}
          ${SC.delAsk === s.id
            ? html`<span class="sc-del warn">delete? <button class="link error" onClick=${(e) => { e.stopPropagation(); sc.deleteScratch(s.id); }}>y</button> <button class="link" onClick=${(e) => { e.stopPropagation(); SC.delAsk = ''; redraw(); }}>n</button></span>`
            : html`<button class="link sc-del-btn" title="Delete this scratch (d)" onClick=${(e) => { e.stopPropagation(); SC.delAsk = s.id; redraw(); }}>✕</button>`}
        </div>
      </div>`)}
    </div>
  </aside>`;
}

// ---- the dry run chip ----

function DryChip() {
  const d = SC.dry;
  if (!d) return html`<span class="dim">dry run: –</span>`;
  const stale = d.pending && !d.ok && !d.error;
  if (stale) return html`<span class="dim">dry run… ${SPIN[Math.floor(Date.now() / 90) % SPIN.length]}</span>`;
  if (!d.ok) {
    return html`<span class="error" title=${d.error}>dry run ✗ ${shortErr(d.error)}</span>`;
  }
  const tone = sc.dryTone(d);
  const bits = [`dry run: ${d.accuracy === 'UNKNOWN' ? 'unknown size' : sc.fmtBytes(d.bytes)}${d.cost ? ' ≈ ' + d.cost : ''}`];
  if (d.statementType && d.statementType !== 'SELECT') bits.push(d.statementType);
  return html`<span class=${'chip dry ' + tone + (d.pending ? ' pending' : '')} title=${d.pending ? 'updating…' : 'what BigQuery would scan, from a dry run of the ' + (SC.cur && SC.cur.sql !== d.sql ? 'selection' : 'file') + (d.cost ? ', and its on-demand price ($6.25 per TiB)' : '')}>
    ${bits.join(' · ')}${tone === 'red' ? ' ⚠' : ''}${d.writes ? html` <b class="warn">changes data</b>` : ''}</span>`;
}

function shortErr(e) {
  const s = (e || '').split('\n')[0];
  return s.length > 110 ? s.slice(0, 110) + '…' : s;
}

// ---- the result ----

function RunLine() {
  const run = SC.run;
  const running = run && ['starting', 'running', 'loading'].includes(run.phase);
  useTick(running, 100);
  if (!run) return html`<div class="sc-runline dim">cmd+enter runs the selection, or the whole file</div>`;
  const st = run.status || {};
  const elapsed = sc.fmtDuration((run.ended || Date.now()) - run.started);
  const bits = [];
  switch (run.phase) {
    case 'starting': case 'running': case 'loading':
      bits.push(html`<span class="warn">${SPIN[Math.floor(Date.now() / 90) % SPIN.length]} ${run.phase === 'loading' ? 'reading rows' : 'running'} ${elapsed}</span>`);
      if (run.phase !== 'loading') bits.push(html`<button class="link error" onClick=${() => sc.cancelRun()}>cancel (ctrl+c)</button>`);
      break;
    case 'failed': case 'cancelled':
      if (run.restored && run.phase === 'failed') { bits.push(html`<span class="error">✗ could not read this result</span>`); break; }
      bits.push(html`<span class=${run.phase === 'failed' ? 'error' : 'warn'}>${run.phase === 'failed' ? '✗ failed' : '■ cancelled'} after ${elapsed}</span>`);
      break;
    default:
      bits.push(html`<span class="ok">✓ ${elapsed}</span>`);
  }
  if (run.restored) bits.push(html`<span class="dim" title=${'A recent result, read again from BigQuery, which keeps it about 24 hours. It ran ' + new Date(run.restored).toLocaleString() + (SC.cur && run.sql !== SC.cur.sql ? ', from SQL that has changed since' : '')}>ran ${sc.ago(run.restored)}</span>`);
  if (st.statementType && st.statementType !== 'SELECT') bits.push(html`<span>${st.statementType}</span>`);
  if (run.phase === 'done') {
    if (run.schema && run.schema.length) bits.push(html`<span>${run.total.toLocaleString()} row${run.total === 1 ? '' : 's'}${run.next ? html` <span class="dim">(${run.rows.length.toLocaleString()} loaded)</span>` : ''}</span>`);
    if (st.dmlRows >= 0) bits.push(html`<span class="ok">${st.dmlRows.toLocaleString()} row${st.dmlRows === 1 ? '' : 's'} modified</span>`);
  }
  if (run.phase === 'done' && st) {
    bits.push(st.cacheHit ? html`<span class="dim">cache hit · $0</span>`
      : html`<span title=${st.cost ? 'The bytes BigQuery billed, and their on-demand price ($6.25 per TiB)' : ''}>billed ${sc.fmtBytes(st.bytesBilled)}${st.cost ? ' ≈ ' + st.cost : ''}</span>`);
  }
  const csv = run.phase === 'done' ? sc.csvURL() : '';
  const tab = run.tab === 'sql' ? 'sql' : 'rows';
  return html`<div class="sc-runline">
    <span class="set-tabs sc-tabs" role="group" aria-label="Show">
      <button class=${'tab' + (tab === 'rows' ? ' on' : '')} title="The rows this job returned" onClick=${() => sc.setResultTab('rows')}>result</button>
      <button class=${'tab' + (tab === 'sql' ? ' on' : '')} title="The SQL this job ran: the selection, if a selection ran" onClick=${() => sc.setResultTab('sql')}>SQL</button>
    </span>
    ${bits.map((b, i) => html`${i ? html`<span class="dim"> · </span>` : ''}${b}`)}
    <span class="spacer"></span>
    ${csv && html`<a class="link" href=${csv} download title="Every row of this result, streamed from BigQuery">⇩ export CSV</a>`}
    ${run.job && html`<a class="link" href=${sc.jobURL(run.job)} target="_blank" rel="noopener" title="Open the job in the BigQuery console">job ↗</a>`}
  </div>`;
}

function Children() {
  const run = SC.run;
  if (!run || !run.children.length) return null;
  return html`<div class="sc-children">
    <span class="dim">script</span>
    ${run.children.map((c, i) => html`<button class=${'chip child' + ((run.child === c.id || (!run.child && c.hasRows && i === lastRows(run.children))) ? ' on' : '') + (c.error ? ' bad' : '')}
      title=${c.error || c.query} onClick=${() => sc.showChild(c)}>${i + 1}. ${c.statementType || c.state}${c.dmlRows >= 0 ? ` · ${c.dmlRows} rows` : ''}${c.table ? ' · ' + c.table.split('.').slice(-1)[0] : ''}</button>`)}
  </div>`;
}

const lastRows = (children) => children.reduce((acc, c, i) => (c.hasRows ? i : acc), -1);

// shortType is a column's type in the header, short: ARRAY<STRUCT> for
// an array of structs whose fields the full name would list.
const shortType = (f) => {
  const t = sc.typeLabel(f);
  if (t.length <= 16) return t;
  return f.mode === 'REPEATED' ? 'ARRAY<' + sc.baseType(f) + '>' : sc.baseType(f);
};

// leavesFor is the grid's columns for run (sc.leavesOf), kept with it.
function leavesFor(run) {
  if (run.leavesSchema !== run.schema) { run.leaves = sc.leavesOf(run.schema); run.leavesSchema = run.schema; }
  return run.leaves;
}

// spans is leaves in runs of one result column: [start, end, col].
function spans(leaves) {
  const out = [];
  for (let i = 0; i < leaves.length;) {
    let j = i;
    while (j < leaves.length && leaves[j].col === leaves[i].col) j++;
    out.push([i, j, leaves[i].col]);
    i = j;
  }
  return out;
}

const headWidth = (name, f) => name.length + 1 + shortType(f).length + 4;

// widths are the grid's column widths in characters, one per leaf: the
// longest of the name and the first rows' cells, within bounds; a
// STRUCT's columns together at least as wide as its name.
function widthsOf(run, leaves) {
  const schema = run.schema;
  const w = leaves.map((lf) => (lf.sub ? headWidth(lf.sub, lf.f) : headWidth(schema[lf.col].name, schema[lf.col])));
  for (const row of run.rows.slice(0, 100)) {
    for (const line of sc.linesOf(schema, row).slice(0, 50)) {
      line.forEach((x, i) => { if (x) w[i] = Math.max(w[i], sc.preview(x.f, x.v).length + 4); });
    }
  }
  const out = w.map((n) => Math.min(Math.max(n, MIN_COL), MAX_COL));
  for (const [i, j, c] of spans(leaves)) {
    if (!leaves[i].sub) continue;
    const have = out.slice(i, j).reduce((x, y) => x + y, 0);
    const need = Math.min(headWidth(schema[c].name, schema[c]), MAX_COL);
    if (have < need) out[j - 1] += need - have;
  }
  return out;
}

// offsetsOf is the line each shown row starts on, and past the last the
// lines in all; null when every row is one line.
function offsetsOf(run, vis, count) {
  if (!sc.anyArrays(run.schema)) return null;
  if (run.offs && run.offsVis === vis && run.offsN === count && run.offsRows === run.rows) return run.offs;
  const o = new Int32Array(count + 1);
  for (let p = 0; p < count; p++) o[p + 1] = o[p] + sc.heightOf(run.schema, run.rows[vis ? vis[p] : p]);
  Object.assign(run, { offs: o, offsVis: vis, offsN: count, offsRows: run.rows });
  return o;
}

// rowAt is the shown row line L is in, by offsets o.
function rowAt(o, L) {
  let lo = 0, hi = o.length - 2;
  while (lo < hi) {
    const m = (lo + hi + 1) >> 1;
    if (o[m] <= L) lo = m; else hi = m - 1;
  }
  return lo;
}

function Grid({ ctx = SC }) {
  const run = sc.viewRun(ctx);
  const box = useRef(null);
  const [h2, setH] = useState(300);
  useEffect(() => {
    const el = box.current;
    if (!el) return undefined;
    const ro = new ResizeObserver(() => setH(el.clientHeight));
    ro.observe(el);
    setH(el.clientHeight);
    return () => ro.disconnect();
  }, [!!run]);
  useEffect(() => { if (box.current && Math.abs(box.current.scrollTop - ctx.scrollTop) > 1) box.current.scrollTop = ctx.scrollTop; });
  if (!run || !run.schema) return null;
  const schema = run.schema, rows = run.rows;
  const leaves = leavesFor(run);
  if (!run.widths || run.widths.length !== leaves.length || (!run.widthsSettled && rows.length)) {
    run.widths = widthsOf(run, leaves);
    run.widthsSettled = rows.length > 0;
  }
  const vis = sc.visibleRows(run, ctx); // the loaded rows the filters keep; null: all
  const count = vis ? vis.length : rows.length;
  const offs = offsetsOf(run, vis, count);
  const lines = offs ? offs[count] : count;
  const numW = Math.max(4, String(run.total || rows.length).length + 1);
  const cols = `${numW}ch ${run.widths.map((n) => n + 'ch').join(' ')}`;
  const first = Math.max(0, Math.floor(ctx.scrollTop / ROW_H) - 8);
  const last = Math.min(lines, Math.ceil((ctx.scrollTop + h2) / ROW_H) + 8);
  const total = lines + (run.next ? 1 : 0);
  const onScroll = (e) => {
    ctx.scrollTop = e.currentTarget.scrollTop;
    if (run.next && ctx.scrollTop + h2 > (lines - 60) * ROW_H) sc.loadMore(ctx);
    redraw();
  };
  const width = `calc(${numW}ch + ${run.widths.reduce((a, b) => a + b, 0)}ch)`;
  const sel = ctx.sel;
  const out = [];
  // A row is as many lines as its arrays need (sc.linesOf); L counts lines.
  for (let L = first, p = offs ? rowAt(offs, first) : first; L < last; p++) {
    const r = vis ? vis[p] : p;
    const block = sc.linesOf(schema, rows[r]);
    for (let k = offs ? L - offs[p] : 0; k < block.length && L < last; k++, L++) {
      const cells = block[k];
      out.push(html`<div class=${'sc-row' + (p % 2 ? ' odd' : '') + (k < block.length - 1 ? ' more' : '')} style=${`grid-template-columns:${cols}`}>
        <span class="sc-num dim">${k ? '' : r + 1}</span>
        ${leaves.map((lf, i) => {
          const x = cells[i];
          const on = sel && sel.r === r && sel.c === lf.col && (sel.l === undefined || (sel.l === i && sel.k === k));
          const cls = 'sc-cell' + (lf.arr ? ' arr' : '') + (x && sc.isNumeric(x.f) ? ' num' : '') + (x && x.v === null ? ' null' : '') + (x && sc.isNested(x.f) ? ' nested' : '') + (on ? ' sel' : '');
          return html`<span class=${cls} onClick=${() => { ctx.sel = { r, c: lf.col, l: i, k }; redraw(); }}>${x ? sc.preview(x.f, x.v) : ''}</span>`;
        })}
      </div>`);
    }
  }
  // A STRUCT's header spans its fields' columns, which name them below it.
  const two = leaves.some((lf) => lf.sub);
  const heads = [];
  for (const [i, j, c] of spans(leaves)) {
    const f = schema[c], struct = !!leaves[i].sub;
    heads.push(html`<span class=${'sc-cell sc-hcell' + (sc.isNumeric(f) ? ' num' : '')} style=${two ? `grid-column:${i + 2} / span ${j - i};grid-row:${struct ? '1' : '1 / span 2'}` : ''} title=${f.name + ' · ' + sc.typeLabel(f) + (f.description ? '\n' + f.description : '')}>
      <span class=${'sc-hname' + (sc.isSortable(f) ? ' sortable' : '')} onClick=${() => sc.setSort(c, ctx)} title=${sc.isSortable(f) ? 'Click to sort: ascending, descending, off' : "BigQuery can't order by this column"}><b>${f.name}</b> <span class="dim t">${shortType(f)}</span>${ctx.sort && ctx.sort.c === c ? html` <span class="sc-sort">${ctx.sort.desc ? '▼' : '▲'}</span>` : ''}</span>
      <input class=${'sc-filter' + (ctx.filters[c] ? ' on' : '')} placeholder="filter" spellcheck="false" autocomplete="off" value=${ctx.filters[c] || ''}
        onInput=${(e) => sc.setFilter(c, e.currentTarget.value, ctx)}
        onKeyDown=${(e) => {
          if (e.key === 'Escape') { e.stopPropagation(); sc.setFilter(c, '', ctx); e.currentTarget.blur(); }
          else if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); sc.searchNow(ctx); }
        }} />
    </span>`);
    if (!struct) continue;
    for (let k = i; k < j; k++) {
      const lf = leaves[k];
      heads.push(html`<span class=${'sc-cell sc-subh' + (sc.isNumeric(lf.f) ? ' num' : '')} style=${`grid-column:${k + 2};grid-row:2`} title=${f.name + '.' + lf.sub + ' · ' + sc.typeLabel(lf.f)}><b>${lf.sub}</b> <span class="dim t">${shortType(lf.f)}</span></span>`);
    }
  }
  return html`<div class="sc-grid" ref=${box} onScroll=${onScroll} tabindex="-1">
    <div class="sc-grid-inner" style=${`width:${width}`}>
      <div class="sc-row sc-head" style=${`grid-template-columns:${cols}`}>
        <span class="sc-num" style=${two ? 'grid-column:1;grid-row:1 / span 2' : ''}></span>
        ${heads}
      </div>
      <div style=${`height:${first * ROW_H}px`}></div>
      ${out}
      ${vis && !vis.length && html`<div class="dim pad">no loaded row matches${!run.next ? '' : run.searchPending ? ' · searching all rows…' : ' · search all rows to look beyond them'}</div>`}
      <div style=${`height:${Math.max(0, total - last) * ROW_H}px`} class="sc-tail">${run.next && html`<div class="dim pad">${run.loadingMore ? 'loading more rows…' : 'more rows load as you scroll'}</div>`}</div>
    </div>
  </div>`;
}

// FilterBar says what the column filters and the sort keep and do, and
// offers to do it to every row rather than the rows that are loaded.
function FilterBar({ ctx = SC }) {
  const run = ctx.run;
  const active = sc.activeFilters(ctx);
  const sorting = !!ctx.sort;
  const sub = run && run.sub;
  const on = sub && run.subOn;
  useTick((on && ['starting', 'running', 'loading'].includes(sub.phase)) || !!(run && run.searchPending), 100);
  if (!run || !run.schema || !(active.length || (sorting && (run.next || on)))) return null;
  const what = active.length ? (sorting ? 'search and sort' : 'search') : 'sort';
  const bits = [];
  if (run.searchPending) {
    bits.push(html`<span class="warn">${SPIN[Math.floor(Date.now() / 90) % SPIN.length]} ${what}ing all ${run.total.toLocaleString()} rows in BigQuery…</span>`);
  } else if (on) {
    if (['starting', 'running', 'loading'].includes(sub.phase)) bits.push(html`<span class="warn">${SPIN[Math.floor(Date.now() / 90) % SPIN.length]} ${what}ing all ${run.total.toLocaleString()} rows in BigQuery…</span>`);
    else if (sub.phase === 'failed') bits.push(html`<span class="error">✗ ${what} failed: ${shortErr(sub.error)}</span>`);
    else {
      const st = sub.status || {};
      bits.push(html`<span class="ok">${active.length ? `${sub.total.toLocaleString()} of ${run.total.toLocaleString()} rows match` : `all ${run.total.toLocaleString()} rows, sorted`}</span>`,
        html`<span class="dim">${st.cacheHit ? 'cache hit' : 'billed ' + sc.fmtBytes(st.bytesBilled || 0)}</span>`);
    }
    bits.push(html`<button class="link" onClick=${() => sc.showLoadedRows(ctx)}>back to loaded rows</button>`);
  } else {
    const vis = sc.visibleRows(run, ctx) || [];
    bits.push(html`<span>${active.length ? `${vis.length.toLocaleString()} of ${run.rows.length.toLocaleString()}${run.next ? ' loaded' : ''} rows match` : `the ${run.rows.length.toLocaleString()} loaded rows are sorted`}${active.length && sorting && run.next ? ', sorted' : ''}</span>`);
    if (run.next) bits.push(html`<button class="link" title="Do it to every row in BigQuery, over the table it keeps this result in (enter in a filter)" onClick=${() => sc.searchAll(false, false, ctx)}>${what} all ${run.total.toLocaleString()} rows${run.searchBytes !== undefined && active.length ? ` (scans ${sc.fmtBytes(run.searchBytes)})` : ''}</button>`);
  }
  if (active.length) bits.push(html`<button class="link" onClick=${() => sc.clearFilters(ctx)}>✕ clear filters</button>`);
  if (sorting) bits.push(html`<button class="link" onClick=${() => sc.clearSort(ctx)}>✕ clear sort</button>`);
  return html`<div class="sc-filterbar">${bits.map((b, i) => html`${i ? html`<span class="dim"> · </span>` : ''}${b}`)}</div>`;
}

// ---- the selected cell ----

function Tree({ f, v, name, depth }) {
  const nested = sc.isNested(f) && v !== null && v !== undefined;
  const [open, setOpen] = useState(depth < 3);
  const t = sc.typeLabel(f);
  const head = html`<span class="tr-name">${name}</span> <span class="dim t">${t.length > 30 ? sc.baseType(f) : t}</span>`;
  if (!nested) {
    const isBytes = sc.baseType(f) === 'BYTES' && v && typeof v === 'object';
    return html`<div class="tr-row" style=${`padding-left:${depth * 14}px`}>${head}
      <span class=${'tr-val' + (v === null ? ' null' : '')}>${v === null ? 'NULL' : isBytes ? `${v.len} bytes · base64 ${v.b64}` : String(v)}</span></div>`;
  }
  let kids;
  if (f.mode === 'REPEATED') {
    const e = { ...f, mode: '' };
    kids = v.map((x, i) => html`<${Tree} f=${e} v=${x} name=${'[' + i + ']'} depth=${depth + 1} />`);
  } else if (sc.baseType(f) === 'STRUCT') {
    kids = (f.fields || []).map((s, i) => html`<${Tree} f=${s} v=${v[i]} name=${s.name} depth=${depth + 1} />`);
  } else {
    const e = { type: (f.rangeElementType && f.rangeElementType.type) || 'STRING' };
    kids = [html`<${Tree} f=${e} v=${v[0]} name="start" depth=${depth + 1} />`, html`<${Tree} f=${e} v=${v[1]} name="end" depth=${depth + 1} />`];
  }
  return html`<div>
    <div class="tr-row tr-open" style=${`padding-left:${depth * 14}px`} onClick=${() => setOpen(!open)}>${open ? '▾' : '▸'} ${head} <span class="dim">${f.mode === 'REPEATED' ? v.length + ' items' : ''}</span></div>
    ${open && kids}
  </div>`;
}

// Sash is the handle on the edge of a panel, which drags the panel's size:
// which is the key in SC.size; axis 'x' or 'y'; from the edge the panel
// grows away from, -1 when dragging up or left makes it larger (the editor
// grows with the pointer, the detail against it). max says how large it
// can get. A double-click gives the panel its own size back.
function Sash({ which, axis, dir, max, min = 120 }) {
  return html`<div class=${'sc-sash ' + axis} title="drag to resize · double-click to reset"
    onDblClick=${() => { sc.setSize(which, 0); redraw(); }}
    onPointerDown=${(e) => {
      e.preventDefault();
      e.stopPropagation();
      const panel = e.currentTarget.parentElement;
      const r = panel.getBoundingClientRect();
      const p0 = axis === 'x' ? e.clientX : e.clientY, s0 = axis === 'x' ? r.width : r.height;
      const hi = Math.max(min, max(panel));
      document.body.classList.add(axis === 'x' ? 'v-resizing' : 'resizing');
      const move = (ev) => {
        const d = (axis === 'x' ? ev.clientX : ev.clientY) - p0;
        sc.setSize(which, Math.round(Math.min(Math.max(s0 + dir * d, min), hi)));
        redraw();
      };
      const up = () => {
        document.body.classList.remove('v-resizing', 'resizing');
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
      };
      window.addEventListener('pointermove', move);
      window.addEventListener('pointerup', up);
    }}></div>`;
}

function Detail({ ctx = SC }) {
  const run = sc.viewRun(ctx);
  if (!run || !run.schema || !ctx.sel) return null;
  const { r, c } = ctx.sel;
  const f = run.schema[c], v = run.rows[r] && run.rows[r][c];
  if (v === undefined) return null;
  const scalar = !sc.isNested(f);
  return html`<aside class="sc-detail" style=${ctx.size.detail ? `flex:0 0 ${ctx.size.detail}px` : ''}>
    <${Sash} which="detail" axis="x" dir=${-1} max=${(el) => el.parentElement.getBoundingClientRect().width - 160} />
    <div class="sc-detail-head"><b>${f.name}</b> <span class="dim" title=${sc.typeLabel(f)}>row ${r + 1} · ${sc.typeLabel(f).length > 24 ? shortType(f) : sc.typeLabel(f)}</span>
      <span class="spacer"></span>
      <button class="link" onClick=${() => sc.copyCell(r, c, ctx)}>copy cell</button>
      <button class="link" onClick=${() => sc.copyRow(r, ctx)}>copy row</button>
      <button class="link" onClick=${() => { ctx.sel = null; redraw(); }}>✕</button></div>
    <div class="sc-detail-body">
      ${scalar && sc.baseType(f) !== 'BYTES' && v !== null && html`<pre class="sc-text">${sc.baseType(f) === 'JSON' ? prettyJSON(v) : String(v)}</pre>`}
      ${(!scalar || sc.baseType(f) === 'BYTES' || v === null) && html`<${Tree} f=${f} v=${v} name=${f.name} depth=${0} />`}
    </div>
  </aside>`;
}

function prettyJSON(s) {
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; }
}

// RunSQL is the SQL tab's read-only editor of the SQL the run ran.
function RunSQL({ sql }) {
  const box = useRef(null);
  const ed = useRef(null);
  useEffect(() => {
    const e = mountRunSQL(box.current);
    ed.current = e;
    return () => { e.view.destroy(); ed.current = null; };
  }, []);
  useEffect(() => { if (ed.current) ed.current.set(sql); }, [sql]);
  return html`<div class="sc-sql" ref=${box}></div>`;
}

function Result() {
  const run = SC.run;
  if (run && run.tab === 'sql') {
    return html`<section class="sc-result">
      <${RunLine} />
      <div class="sc-result-body">
        ${run.sql ? html`<${RunSQL} sql=${run.sql} />` : html`<div class="dim pad">this run kept no SQL</div>`}
      </div>
    </section>`;
  }
  const vr = sc.viewRun();
  let body = null;
  if (vr !== run && vr.phase !== 'done') {
    body = vr.phase === 'failed' ? html`<pre class="sc-error">${vr.error}</pre>` : html`<div class="dim pad">searching…</div>`;
  } else if (run && run.phase === 'failed') body = html`<pre class="sc-error">${run.error}</pre>`;
  else if (run && run.phase === 'cancelled') body = html`<div class="dim pad">the job was cancelled</div>`;
  else if (run && run.phase === 'done' && (!run.schema || !run.schema.length)) {
    const st = run.status || {};
    body = html`<div class="pad">
      ${st.dmlRows >= 0 ? html`<p class="ok">${st.dmlRows.toLocaleString()} row${st.dmlRows === 1 ? '' : 's'} modified by ${st.statementType}.</p>` : html`<p class="ok">${st.statementType || 'The statement'} ran; it returns no rows.</p>`}
      ${run.children.length > 0 && html`<p class="dim">${run.children.length} statements ran; pick one above to see its rows.</p>`}
    </div>`;
  }
  return html`<section class="sc-result">
    <${RunLine} />
    <${Children} />
    <${FilterBar} />
    <div class="sc-result-body">
      ${body || html`<${Grid} />`}
      <${Detail} />
    </div>
  </section>`;
}

// ResultGrid is a result's rows, with their filters, sort and selected
// cell, in a view other than Scratch's (sc.resultView): the dependency
// inspector's. Its owner says how the run stands.
export function ResultGrid({ ctx }) {
  return html`<${FilterBar} ctx=${ctx} />
    <div class="sc-result-body">
      <${Grid} ctx=${ctx} />
      <${Detail} ctx=${ctx} />
    </div>`;
}

// ---- recent results ----

// RecentMenu lists the last runs that returned rows; picking one opens its
// scratch on that result (h, then j/k and enter, or its number).
function RecentMenu() {
  if (!SC.recentOpen) return null;
  const name = (id) => (SC.list.find((s) => s.id === id) || {}).from || 'scratch';
  return html`<div class="overlay-back win-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) sc.closeRecent(); }}>
    <div class="ctx-menu sc-recent" role="menu">
      <div class="sc-recent-head dim">recent results · kept by BigQuery about 24 hours</div>
      ${SC.recent.map((r, i) => {
        const gone = sc.recentGone(r);
        return html`<div class=${'ctx-row sc-recent-row' + (i === SC.recentCursor ? ' cur' : '') + (gone ? ' off' : '')} role="menuitem"
          title=${r.sql} onMouseEnter=${() => { SC.recentCursor = i; redraw(); }} onClick=${() => sc.openRecent(r)}>
          <span class="ctx-key">${i + 1}</span>
          <span class="ctx-label"><b>${name(r.scratch)}</b> <span class="dim">${firstSQL(r.sql)}</span></span>
          <span class="ctx-key">${r.rows.toLocaleString()} row${r.rows === 1 ? '' : 's'} · ${gone ? 'expired' : sc.ago(Date.parse(r.ended))}</span>
        </div>`;
      })}
    </div>
  </div>`;
}

function firstSQL(sql) {
  const l = (sql || '').split('\n').map((x) => x.trim()).find((x) => x && !x.startsWith('--')) || '';
  return l.length > 60 ? l.slice(0, 60) + '…' : l;
}

// ---- the page ----

export function NewBox() {
  const n = SC.newForm;
  if (S.overlay !== 'scratchNew' || !n) return null;
  const v = S.view || {};
  return html`<div class="overlay-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) { sc.cancelNew(); redraw(); } }}>
    <div class="overlay pane focused confirm">
      <header class="pane-title"><span class="pane-name">new scratch query<span class="dim"> · esc cancels</span></span></header>
      <div class="pane-body">
        <p>from <b>${v.name || v.label || S.sel}</b></p>
        <div class="sc-new-opts">
          <button class="sc-new-opt" disabled=${n.busy} onClick=${() => sc.createScratch('compiled')}>
            <kbd>c</kbd><span class="sc-new-name">compiled SQL</span><span class="dim">the model's query as it runs</span></button>
          <button class="sc-new-opt" disabled=${n.busy} onClick=${() => sc.createScratch('columns')}>
            <kbd>s</kbd><span class="sc-new-name">SELECT its columns</span><span class="dim">one to a line; the table must exist</span></button>
        </div>
        ${n.busy && html`<p class="warn">making it…</p>`}
        ${n.error && html`<p class="error">${n.error}</p>`}
      </div>
    </div>
  </div>`;
}

function ConfirmBox() {
  const c = SC.confirm;
  if (!c) return null;
  return html`<div class="overlay-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) sc.dismissConfirm(); }}>
    <div class=${'overlay pane focused confirm ' + (c.danger ? 'amber' : '')}>
      <header class="pane-title"><span class="pane-name">⚠ really ${c.action === 'searchAll' ? 'search every row' : 'run this'}?</span></header>
      <div class="pane-body">
        ${c.why.map((w) => html`<p class="warn"><b>${w}</b></p>`)}
        <p class="dim">Do you really want to ${c.action === 'searchAll' ? 'search' : 'run'} it?</p>
        <div class="buttons"><button class="run-btn danger" onClick=${() => sc.confirmRun()}><kbd>y</kbd> ${c.action === 'searchAll' ? 'search' : 'run'}</button>
          <button class="plain" onClick=${() => sc.dismissConfirm()}><kbd>any other key</kbd> cancel</button></div>
      </div>
    </div>
  </div>`;
}

export function ScratchPage() {
  useEffect(() => { document.title = 'xf · scratch'; return () => { document.title = 'xf · ' + S.boot.name; }; }, []);
  const k = (list) => list.map(([a, b]) => html`<span class="key"><kbd>${a}</kbd> ${b}</span>`);
  const focus = SC.focus;
  return html`<div class="app scratch-app">
    <div class="status">
      ${SC.standalone ? html`<span class="badge">XF</span>` : html`<button class="link" onClick=${() => sc.closeScratchView()} title="Back to the project (ctrl+q)">← ${S.boot.name}</button><span class="dim"> · </span><button class="link" onClick=${() => { openProjects(); redraw(); }} title="Switch or add a project">⇄ projects</button><span class="dim"> · </span>`}<button class="link" onClick=${() => sc.toggleList()} title="Show or hide the scratch list (cmd+b)">☰ scratches</button>
      <span class="dim"> · </span><span>scratch</span>
      ${SC.cur && SC.cur.from && html`<span class="dim"> · from ${SC.cur.from}</span>`}
      <span class="spacer"></span>
      <${WindowsPill} /><${ThemeButton} />
    </div>
    ${S.overlay === 'projects' && hooks.projectsOverlay && hooks.projectsOverlay()}
    ${S.overlay === 'palette' && hooks.paletteOverlay && hooks.paletteOverlay()}
    <div class=${'sc-body' + (SC.listOpen ? '' : ' no-list')} style=${SC.listOpen && SC.size.list ? `grid-template-columns:${SC.size.list}px minmax(0,1fr)` : ''}>
      ${SC.listOpen && html`<${List} />`}
      <main class="sc-main">
        <section class=${'sc-editor-pane' + (focus === 'editor' ? ' focused' : '') + (SC.run && !SC.hideResults ? '' : ' full')} style=${SC.run && !SC.hideResults && SC.size.editor ? `flex:0 0 ${SC.size.editor}px` : ''}>
          ${SC.run && !SC.hideResults && html`<${Sash} which="editor" axis="y" dir=${1} min=${100} max=${(el) => el.parentElement.getBoundingClientRect().height - 120} />`}
          <div class="sc-toolbar">
            <button class="run-btn" onClick=${() => sc.runScratch()} disabled=${sc.isRunning() || !SC.cur}>▶ run <kbd>${/Mac|iPhone|iPad/.test(navigator.userAgent) ? '⌘↵' : 'ctrl+↵'}</kbd></button>
            <${DryChip} />
            <span class="spacer"></span>
            ${SC.cur && html`<span class="sc-target" title="The project the query is billed to and the location it runs in, kept in the scratch's first line">
              <input class="sc-target-in" value=${SC.cur.project || ''} placeholder="project" spellcheck="false" autocomplete="off" onChange=${(e) => sc.setTarget(e.currentTarget.value.trim(), SC.cur.location || '')} onKeyDown=${(e) => { if (e.key === 'Enter') e.currentTarget.blur(); }} />
              <input class="sc-target-in loc" value=${SC.cur.location || ''} placeholder="location" spellcheck="false" autocomplete="off" onChange=${(e) => sc.setTarget(SC.cur.project || '', e.currentTarget.value.trim())} onKeyDown=${(e) => { if (e.key === 'Enter') e.currentTarget.blur(); }} />
            </span>`}
            ${SC.recent.length > 0 && html`<button class="link" onClick=${() => sc.toggleRecent()} title="The last ${SC.recent.length} results, to open again (h)">◷ recent</button>`}
            <button class="link" onClick=${() => sc.refreshMeta()} title="Forget what is known of tables and columns, for completion and hover (:refresh)">↻</button>
            ${SC.run && html`<button class="link" onClick=${() => sc.toggleResults()} title="Show or hide the results (cmd+j)">${SC.hideResults ? '▴ show results' : '▾ hide results'}</button>`}
            ${SC.cur && html`<span class="dim" title="The scratch file">${SC.cur.id ? SC.cur.id + '.sql' : 'new · saved when you type'}</span>`}
          </div>
          ${SC.cur ? html`<${Editor} />` : null}
        </section>
        ${SC.run && !SC.hideResults && html`<${Result} />`}
      </main>
    </div>
    ${SC.flash ? html`<div class="footer warn">${SC.flash}</div>`
      : html`<div class="footer">${focus === 'editor'
        ? k([[commandsKey, 'commands'], ['ctrl+w', 'leave editor'], ['⌘↵', 'run selection or file'], [savesWith(), 'save'], ['⌘J', 'results'], ['⌘B', 'scratches'], ...(SC.standalone ? [] : [['ctrl+q', 'back']])])
        : k([[commandsKey, 'commands'], ['i', 'edit'], ['⌘↵/r', 'run'], ['ctrl+c', 'cancel run'], ['j/k', 'switch scratch'], ['h', 'recent results'], ['⌘J', 'results'], ['⌘B/b', 'scratches'], ['n', 'new'], ['d', 'delete'], ['y/Y', 'copy cell/row'], ...(SC.standalone ? [] : [['ctrl+q', 'back']])])}</div>`}
    <${NewBox} />
    <${ConfirmBox} />
    <${RecentMenu} />
  </div>`;
}
