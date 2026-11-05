// The page's panes and overlays. They draw S (see state.js) and turn
// clicks into the same changes the keys make.

import { h } from './vendor/preact.mjs';
import { useEffect, useLayoutEffect, useRef, useState } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import * as st from './state.js';
import { ScratchPage, NewBox } from './scratch-view.js';
import './scratch.js';
import * as src from './source.js';
import './source-smart.js';
import { sourceEditorDOM } from './source-editor.js';
import { TreePane, TreeSplitter, PlainNote, Rail } from './tree-view.js';
import './tree.js';
import * as scm from './scm.js';
import { GitOutput } from './scm-view.js';
import { ForgePill } from './forge-view.js';
import * as search from './search.js';
import { DiffPane } from './diff-view.js';
import { BuffersOverlay } from './buffers-view.js';
import { ExternalsTable } from './externals-view.js';
import { TargetSection } from './target-view.js';
import { ProdForm, prodOptionsOf } from './prod-form-view.js';
import { vimKey } from './editor.js';
import { vimOn } from './vim-mode.js';

// vimPair is the keys of the editor's next and previous Hunk or Error,
// as the help shows them: ]g/[g. The settings page's Vim tab sets them.
const vimPair = (what) => vimKey('vimNext' + what) + '/' + vimKey('vimPrev' + what);

// keyOr is a help row's keys: vim's with vimMode on, else plain's.
const keyOr = (vim, plain) => (vimOn() ? vim : plain);

// plainSourceHelp is the source pane's help without vim's keys.
function plainSourceHelp() {
  const cmds = st.commandsKey;
  return [['enter', 'edit'], ['/', 'find in the file (as ' + (st.onMac ? '⌘f' : 'ctrl+f') + ' in the editor)'], ['ctrl+shift+o', 'symbols'],
    ['ctrl+w', 'leave the editor'], ['esc', 'close the completions, a card, the find panel, extra cursors or a selection (nothing else)'], [st.saveKey, 'save'], [cmds + ' discard unsaved changes', 'discard changes'],
    [cmds + ' Format: file', 'format the SQL (sqlfluff) and the config block (unsaved; ' + (st.onMac ? '⌘z' : 'ctrl+z') + ' undoes)'],
    ['right-click', 'context menu: definition, card, next/prev change and error, stage/revert the change, copy, format (shift+right-click: the browser\'s)'],
    ['mouse', 'card of a ref, a function or an includes name (constants.X)'],
    [cmds + ' Go to:', 'next/prev git change or error, the file named under the cursor, the file before'], ['ctrl+x ctrl+f', 'complete a file name'],
    [st.onMac ? '⌘/' : 'ctrl+/', 'comment out or back in (the selection\'s lines; also the Scratch editor)'], ['ctrl+shift+↑/↓', 'add a cursor above/below, to type on several lines at once (esc: one cursor again)'], ['ctrl+o/ctrl+i', 'jump back/forward'],
    [cmds + ' Buffers:', 'next/prev buffer (files opened), list, remove the one shown'], ['backspace', 'back from go to definition (outside the editor)']];
}
import { SettingsPage, GearButton } from './settings-view.js';
import { SET } from './settings.js';
import { ThemeButton, ThemePicker } from './theme-view.js';
import { WindowsPill, WindowsList } from './windows-view.js';
import { windowOf, windowsOn } from './windows.js';
import { T } from './theme.js';

const { S, vim, redraw } = st;

// scrollNearest scrolls el into view within the box that scrolls it, as
// scrollIntoView({ block: 'nearest' }) does, but never the page: on a
// phone the panes stack in a page that scrolls, and every redraw would
// pull it back to the selected row.
export function scrollNearest(el) {
  for (let box = el.parentElement; box && box !== document.body; box = box.parentElement) {
    const oy = getComputedStyle(box).overflowY;
    if ((oy === 'auto' || oy === 'scroll') && box.scrollHeight > box.clientHeight) {
      const e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
      // A row taller than the box (a squeezed pane) can't fit: its top is
      // all that shows, and one that already covers the box stays where
      // it is, or each redraw would flip between its top and its bottom.
      if (e.height > b.height) {
        if (e.top > b.top || e.bottom < b.bottom) box.scrollTop += e.top - b.top;
      } else if (e.top < b.top) box.scrollTop -= b.top - e.top;
      else if (e.bottom > b.bottom) box.scrollTop += e.bottom - b.bottom;
      return;
    }
  }
}
const html = htm.bind(h);

const LH = 20; // a code line's height in px, as style.css sets --lh
const kindClass = (short) => 'k-' + (short || '').replace(/\W/g, '');

// Pane is a box with its title set into the top border, lazygit style,
// and a note set into the bottom one.
// Pane is a titled box. api marks one whose runs go to GCP: its title
// ends "· GCP" and, unless focused, its border takes the accent. busy is
// what the title says beside a spinner while a compile runs, and old
// marks a pane drawing the previous compile (see oldness).
function Pane({ id, num, title, note, focused, children, cls, onClick, tabs, style, api, busy, old }) {
  return html`<section class=${'pane ' + (cls || '') + (focused ? ' focused' : '') + (api ? ' api' : '') + (old ? ' ' + old : '')} data-pane=${id} style=${style || ''}
      onMouseDown=${(e) => { if (onClick) onClick(e); else if (id) { if (id === 'source' && e.target instanceof Element && e.target.closest('.cm-editor')) return; st.setFocus(id); redraw(); } }}>
    <header class="pane-title">
      ${num !== undefined && html`<span class="pane-num">${num}</span>`}
      <span class="pane-name">${title}${api && html`<span class="gcp"> · GCP</span>`}</span>
      ${tabs}
      ${busy != null && html`<${Busy} label=${busy} />`}
    </header>
    <div class="pane-body">${children}</div>
    ${note && html`<footer class="pane-note">${note}</footer>`}
  </section>`;
}

// ---- the compile animation ----

const SPIN = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
function useTick(on, ms = 90) {
  const [, set] = useState(0);
  useEffect(() => {
    if (!on) return undefined;
    const t = setInterval(() => set((n) => n + 1), ms);
    return () => clearInterval(t);
  }, [on]);
}
const spinner = () => SPIN[Math.floor(Date.now() / 90) % SPIN.length];

// Busy is a pane title's spinner while a compile runs: label beside it,
// or "elapsed" for the compile's seconds so far.
function Busy({ label }) {
  useTick(true, 100);
  const text = label === 'elapsed' ? ((Date.now() - S.compileStarted) / 1000).toFixed(1) + 's' : label;
  return html`<span class="pane-busy warn">${spinner()}${text ? ' ' + text : ''}</span>`;
}

// compilingOver is whether a compile runs while the panes still draw the
// previous one's graph; oldness is the class of a pane drawing that
// graph: "pending" pulses amber while a compile runs, "stale" stays amber
// while the last compile failed and the graph is the last good one.
const compilingOver = () => !!(S.state && S.state.compiling && S.state.hasGraph);
const oldness = () => (compilingOver() ? 'pending' : S.state && S.state.stale && S.state.hasGraph ? 'stale' : '');

function Waiting() {
  useTick(true, 100);
  const secs = ((Date.now() - S.compileStarted) / 1000).toFixed(1);
  const st = S.state || {};
  return html`<div class="waiting">
    <div class="ramp"></div>
    <div><span class="warn">${spinner()} compiling${st.reason ? ' (' + st.reason + ')' : ''}…</span> <span class="dim">${secs}s</span></div>
  </div>`;
}

// ---- 1 actions ----

function ActionsPane({ fill }) {
  const v = S.view, st0 = S.state;
  let body;
  if (!st0 || (!st0.hasGraph && st0.compiling)) body = html`<${Waiting} />`;
  else if (!st0.hasGraph) body = html`<div class="dim pad">no compile result — press <kbd>e</kbd> for details, <kbd>${st.saveKey}</kbd> to retry</div>`;
  else if ((!S.sel || !v) && !(S.plainFile && S.lastPane)) body = S.plainFile ? html`<${PlainNote} />` : html`<div class="dim pad">press <kbd>${st.paletteKey}</kbd> then <kbd>#</kbd> to find an action</div>`;
  else {
    // A plain file keeps the list it was opened from, with nothing picked.
    const list = v ? v.pane : S.lastPane.pane;
    const kindW = Math.max(...list.map((a) => a.kind.length)) + 1;
    body = html`<div class="actions">
      ${v && v.externals && html`<${ExternalsTable} />`}
      ${!(v && v.externals) && list.map((a) => html`<div class=${'action' + (a.id === S.sel ? ' sel' : '')} key=${a.id}
          onMouseDown=${(e) => { e.stopPropagation(); st.clickAction(a.id); }}
          ref=${(el) => { if (el && a.id === S.sel) scrollNearest(el); }}>
        <div class="action-row">
          <span class="marker">${a.id === S.sel ? '▸' : ''}</span>
          <span class=${'kind ' + kindClass(a.short)} style=${'min-width:' + kindW + 'ch'}>${a.kind.toUpperCase()}</span>
          <span class="name">${a.id}</span>
          ${a.disabled && html`<span class="warn"> disabled</span>`}
        </div>
        ${a.details.map((d) => html`<div class="detail"><span class="dlabel">${d.label}</span>${d.dry && compilingOver()
          ? html`<span class="dvalue warn" title=${'before this compile: ' + d.value}><${Busy} label="waiting for compile…" /></span>`
          : html`<span class=${'dvalue ' + (d.tone || '')}>${d.value}</span>`}</div>`)}
      </div>`)}
      ${v && v.auth && html`<div class="warn pad">${v.auth}</div>`}
    </div>`;
  }
  const paneFile = v ? v.file : S.lastPane && S.plainFile ? S.lastPane.file : '';
  // A file of declarations is the source pane's: the table names what it lists.
  const title = v && v.externals ? 'declarations' : 'actions' + (paneFile ? ' · ' + paneFile : '');
  return html`<${Pane} id="actions" title=${title} focused=${S.focus === 'actions'} cls="actions-pane" style=${sized('actions', fill)}
    busy=${compilingOver() ? 'elapsed' : null} old=${oldness()}>${body}<//>`;
}

// ---- 2 lineage ----

// LineageBody is pane 2's lineage tab: what the action reads and what reads
// it, one per row: its kind, then its project and dataset (dim) and table,
// in which a prefix or suffix shows.
function LineageBody() {
  const v = S.view;
  const rows = st.lineageRows();
  if (!S.state || !S.state.hasGraph) return S.state && S.state.compiling ? html`<${Waiting} />` : html`<div class="dim pad">no compile result</div>`;
  if (!v) return S.plainFile ? html`<${PlainNote} />` : html`<div class="dim pad">no action selected</div>`;
  return html`<div class="lineage scroll">${rows.map((r, i) => {
    if (r.header) return html`<div class="lin-header dim">${r.header}</div>`;
    if (r.none) return html`<div class="dim lin-none">  none</div>`;
    const a = r.ref, sel = i === S.lineageCursor && S.focus === 'lineage';
    return html`<div class=${'lin-row' + (sel ? ' sel' : '') + (a.external || a.unknown ? ' ext' : '')} title=${a.id}
        onMouseDown=${(e) => { e.stopPropagation(); st.clickLineage(i, false); }}
        onDblClick=${() => st.clickLineage(i, true)}
        ref=${(el) => { if (el && sel) scrollNearest(el); }}>
      <span class="dim branch">${r.last ? '└' : '├'}</span>
      <span class=${'lin-kind kind-word ' + kindClass(a.short)}>${a.kind}</span>
      <span class="lin-name">${a.id.endsWith('.' + a.name) ? html`<span class="dim">${a.id.slice(0, -a.name.length)}</span>${a.name}` : a.id}</span>
    </div>`;
  })}</div>`;
}

// ---- 3 run ----

function Toggle({ on, k, label, flag }) {
  const box = !on ? '[ ]' : flag === 'full' ? '[!]' : '[✓]';
  return html`<button class=${'toggle toggle-' + flag + (on ? ' on' : '')} onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); st.toggleFlag(flag); redraw(); }}>
    <span class="box">${box}</span> <kbd>${k}</kbd> ${label}</button>`;
}

function RunToggles() {
  const f = S.flags;
  return html`<span class="toggles">
    <${Toggle} on=${f.deps} k="d" label="deps" flag="deps" />
    <${Toggle} on=${f.dependents} k="D" label="dependents" flag="dependents" />
    <${Toggle} on=${f.full} k="f" label="full-refresh" flag="full" />
    ${S.caps.api && html`<${Toggle} on=${f.api} k="a" label="api" flag="api" />`}
  </span>`;
}

// GitLine says what an API run runs while api is on: origin/<branch> and
// how the checkout differs from it. A count shows what it counts on hover.
function GitLine({ verb }) {
  if (!S.flags.api) return null;
  const parts = S.git ? S.git[verb] : [{ text: (verb === 'check' ? 'checks' : 'runs') + ' the pushed branch', tone: 'dim' }];
  return html`<div class="git-line">${parts.map((p) => html`<span class=${(p.tone || '') + (p.detail ? ' has-detail' : '')}
    title=${p.detail ? p.detail.join('\n') : undefined}>${p.text}</span>`)}</div>`;
}

function RunPane() {
  const p = S.view && S.view.run;
  const r = S.run;
  const full = S.flags.full;
  let tail = null;
  if (r && r.running && r.cancelling) tail = html`<span class="warn">cancelling… ${r.api ? 'ctrl+c stops watching' : 'ctrl+c force-kills'}</span>`;
  else if (r && r.running) tail = html`<span class="warn">running… <button class="link" onMouseDown=${(e) => { e.stopPropagation(); st.cancelRun(); }}>ctrl+c cancels</button></span>`;
  else if (!S.view && S.plainFile) tail = html`<span>${S.plainFile}</span><span class="dim"> · no action in this file</span>`;
  else if (S.view && p && !p.actions.length) tail = html`<span class="dim">${p.why}</span>`;
  else if (p && p.actions.length) {
    const verb = full
      ? (S.flags.api ? '▸ full-refresh in GCP' : '▸ full-refresh')
      : (S.flags.api ? '▸ run in GCP' : '▸ run');
    tail = html`<button class=${'run-btn' + (full ? ' danger' : '')} title=${p.command} onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); st.startRun(); redraw(); }}>
      <kbd>r</kbd> <span class=${full ? 'verb warn' : 'verb dim'}>${verb}</span></button>${p.defer && html`<span class="dim sep">·</span><span class="defer-prod" title="defer to prod: the run makes a proxy view at each, reading prod">${p.defer}</span>`}`;
  }
  const title = full ? html`run<span class="full-badge"> · ⚠ full-refresh</span>` : 'run';
  return html`<${Pane} id="run" title=${title} focused=${S.focus === 'run'} cls="run-pane" api=${S.flags.api}>
    <div class="run-line">
      <${RunToggles} />
      ${tail && html`<span class="dim sep">·</span>`}${tail}
    </div>
    <${GitLine} verb="run" />
    ${S.focus === 'run' && p && p.command && html`<div class="dim command">${p.command}</div>`}
  <//>`;
}

// ---- 0 sql ----

// marksOf are the spans of a line to draw over its tokens: the search's
// matches, the selection and the cursor.
function marksOf(b, r, focused) {
  const marks = [];
  const plain = b.text[r] || [];
  if (b.hlHits) {
    for (const hit of b.hits) {
      if (hit.row !== r) continue;
      marks.push([hit.from, hit.to, focused && r === b.row && hit.from === b.col ? 'hit-cur' : 'hit']);
    }
  }
  if (b.visual) {
    const [sr, sc, er, ec] = vim.selection(b);
    if (r >= sr && r <= er) {
      let from = 0, to = plain.length;
      if (b.visual === vim.VISUAL_CHAR) { if (r === sr) from = sc; if (r === er) to = ec + 1; }
      if (to > plain.length || (r < er && b.visual === vim.VISUAL_CHAR) || !plain.length) to = plain.length + 1;
      marks.push([from, to, 'visual']);
    }
  }
  if (focused && r === b.row) marks.push([b.col, b.col + 1, 'cursor']);
  return marks;
}

// codeLine draws a line's tokens with marks over them; a mark past the
// end draws a blank cell (an empty line's cursor or selection).
function codeLine(tokens, plainLen, marks) {
  if (!marks.length) return tokens.map(([c, t]) => html`<span class=${c ? 't-' + c : ''}>${t}</span>`);
  const cuts = new Set([0]);
  for (const [a, b] of marks) { cuts.add(a); cuts.add(b); }
  const classAt = (i) => marks.filter(([a, b]) => i >= a && i < b).map((m) => m[2]).join(' ');
  const out = [];
  let pos = 0;
  for (const [c, t] of tokens) {
    const chars = Array.from(t);
    let start = 0;
    for (let i = 1; i <= chars.length; i++) {
      if (i === chars.length || cuts.has(pos + i)) {
        const piece = chars.slice(start, i).join('');
        out.push(html`<span class=${((c ? 't-' + c : '') + ' ' + classAt(pos + start)).trim()}>${piece}</span>`);
        start = i;
      }
    }
    pos += chars.length;
  }
  const end = marks.filter(([a, b]) => b > plainLen && a <= plainLen);
  if (end.length) out.push(html`<span class=${end.map((m) => m[2]).join(' ')}> </span>`);
  return out;
}

function SQLBody() {
  const b = S.buf;
  const v = S.view;
  const focused = S.focus === 'sql';
  const el = useRef(null);
  const charW = useRef(8);
  const drag = useRef(false);

  useLayoutEffect(() => {
    const box = el.current;
    if (!box) return undefined;
    const measure = () => {
      const hgt = Math.max(Math.floor(box.clientHeight / LH), 1);
      if (hgt !== b.height) { b.height = hgt; vim.follow(b); redraw(); }
      const probe = box.querySelector('.probe');
      if (probe) charW.current = probe.getBoundingClientRect().width / 10;
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    return () => ro.disconnect();
  }, [!!v]);

  useLayoutEffect(() => {
    const box = el.current;
    if (!box) return;
    if (Math.abs(box.scrollTop - b.offset * LH) > 1) box.scrollTop = b.offset * LH;
    const cur = box.querySelector('.cursor');
    if (cur && focused) {
      const gutter = box.querySelector('.gutter');
      const gw = gutter ? gutter.getBoundingClientRect().width : 0;
      const x = cur.offsetLeft;
      if (x < box.scrollLeft + gw) box.scrollLeft = Math.max(x - gw - box.clientWidth / 2, 0);
      else if (x + cur.offsetWidth > box.scrollLeft + box.clientWidth) box.scrollLeft = x - box.clientWidth / 2;
    }
  });

  if (!S.state || !S.state.hasGraph) {
    return S.state && S.state.compiling ? html`<${Waiting} />` : html`<div class="dim pad">no compile result — press <kbd>e</kbd> for details, <kbd>${st.saveKey}</kbd> to retry</div>`;
  }
  if (!v) return S.plainFile ? html`<${PlainNote} />` : html`<div class="dim pad">press <kbd>${st.paletteKey}</kbd> then <kbd>#</kbd> to find an action</div>`;

  const n = b.text.length;
  const first = Math.max(b.offset - 10, 0), last = Math.min(b.offset + b.height + 10, n);
  const gw = String(n).length;
  const posAt = (e) => {
    const box = el.current;
    const rect = box.getBoundingClientRect();
    const gutter = box.querySelector('.gutter');
    const g = gutter ? gutter.getBoundingClientRect().width : 0;
    const row = Math.floor((e.clientY - rect.top + box.scrollTop) / LH);
    const col = Math.floor((e.clientX - rect.left + box.scrollLeft - g - 8) / charW.current);
    return [Math.min(Math.max(row, 0), n - 1), Math.max(col, 0)];
  };
  const rows = [];
  for (let r = first; r < last; r++) {
    const tokens = v.sql.lines[r] || [];
    const marks = v.sql.empty ? [] : marksOf(b, r, focused);
    rows.push(html`<div class=${'code-line' + (r === b.row && focused ? ' current' : '')} style=${'top:' + r * LH + 'px'} key=${r}>
      ${!v.sql.empty && html`<span class="gutter" style=${'width:' + (gw + 2) + 'ch'}>${r + 1}</span>`}
      <span class=${'code' + (v.sql.empty ? ' dim' : '')}>${codeLine(tokens, b.text[r] ? b.text[r].length : 0, marks)}</span>
    </div>`);
  }
  return html`<div class="sql-scroll" ref=${el}
      onScroll=${(e) => {
        const off = Math.round(e.currentTarget.scrollTop / LH);
        if (off !== b.offset) { vim.scrollView(b, off - b.offset); redraw(); }
      }}
      onMouseDown=${(e) => {
        if (e.button !== 0 || v.sql.empty) return;
        st.setFocus('sql');
        const [r, c] = posAt(e);
        b.visual = vim.VISUAL_NONE;
        vim.moveTo(b, r, c);
        drag.current = { r, c, moved: false };
        const move = (ev) => {
          const [r2, c2] = posAt(ev);
          if (!b.visual) { b.visual = vim.VISUAL_CHAR; b.vrow = drag.current.r; b.vcol = b.col; }
          vim.moveTo(b, r2, c2);
          redraw();
        };
        const up = () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', up); };
        window.addEventListener('mousemove', move);
        window.addEventListener('mouseup', up);
        redraw();
      }}>
    <span class="probe" aria-hidden="true">0000000000</span>
    <div class="sql-lines" style=${'height:' + n * LH + 'px'}>${rows}</div>
  </div>`;
}

function SQLSearchLine() {
  const s = S.sqlSearch;
  if (!s.typing) return null;
  return html`<div class="search-line">/<input autofocus value=${s.query} spellcheck="false"
    ref=${(e) => e && document.activeElement !== e && e.focus()}
    onInput=${(e) => st.setSQLSearchQuery(e.currentTarget.value)}
    onKeyDown=${(e) => {
      if (e.key === 'Enter') { e.preventDefault(); st.endSQLSearch(true); }
      if (e.key === 'Escape') { e.preventDefault(); st.endSQLSearch(false); }
    }} /></div>`;
}

function ColumnsBody() {
  const v = S.view;
  const c = S.cols;
  if (!v) return S.plainFile ? html`<${PlainNote} />` : html`<div class="dim pad">no action selected</div>`;
  if (v.columns.reason) return html`<div class=${'pad ' + (v.columns.failed ? 'error' : 'dim')}>${v.columns.reason}</div>`;
  const fields = st.filteredColumns();
  const total = v.columns.fields.length;
  return html`<div class="columns">
    ${total > 0 && html`<div class="col-search-bar">
      <div class=${'col-search-box' + (c.name ? ' active' : '')}>
        <span class="col-search-icon" aria-hidden="true">⌕</span>
        <input class="col-search-input" value=${c.name} placeholder="Search columns by name or description… (/)" spellcheck="false" autocomplete="off"
          ref=${(e) => e && c.typing && c.focusType !== true && document.activeElement !== e && e.focus()}
          onInput=${(e) => { c.name = e.currentTarget.value; c.cursor = 0; redraw(); }}
          onFocus=${() => { c.typing = true; c.focusType = false; st.setFocus('sql'); }}
          onBlur=${() => { if (!c.focusType) c.typing = false; }}
          onKeyDown=${(e) => colFilterKey(e)} />
        ${c.name && html`<button class="col-search-clear" title="Clear search (esc)" onMouseDown=${(e) => { e.preventDefault(); c.name = ''; c.cursor = 0; redraw(); }}>×</button>`}
      </div>
      <div class=${'col-search-box type-box' + (c.type ? ' active' : '')}>
        <input class="col-search-input" value=${c.type} placeholder="Filter type (e.g. INT64)…" spellcheck="false" autocomplete="off"
          ref=${(e) => e && c.typing && c.focusType === true && document.activeElement !== e && e.focus()}
          onInput=${(e) => { c.type = e.currentTarget.value; c.cursor = 0; redraw(); }}
          onFocus=${() => { c.typing = true; c.focusType = true; st.setFocus('sql'); }}
          onBlur=${() => { if (c.focusType) c.typing = false; }}
          onKeyDown=${(e) => colFilterKey(e)} />
        ${c.type && html`<button class="col-search-clear" title="Clear type filter" onMouseDown=${(e) => { e.preventDefault(); c.type = ''; c.cursor = 0; redraw(); }}>×</button>`}
      </div>
      <span class="dim col-count">${fields.length === total ? `${total} column${total === 1 ? '' : 's'}` : `${fields.length} of ${total}`}</span>
    </div>`}
    <div class="table-scroll">
      <table class="grid">
        <thead><tr><th>Column</th><th>Type</th><th>Mode</th><th>Description</th></tr></thead>
        <tbody>${fields.map((f, i) => html`<tr class=${i === c.cursor && S.focus === 'sql' ? 'sel' : ''}
            ref=${(e) => { if (e && i === c.cursor) scrollNearest(e); }}
            onMouseDown=${() => { c.cursor = i; st.setFocus('sql'); redraw(); }}>
          <td class="mono">${f.name}</td><td class="dim">${f.type}</td><td class="dim">${(f.mode || '').toUpperCase() === 'NULLABLE' ? '' : f.mode}</td>
          <td class="desc">${f.description}</td></tr>`)}</tbody>
      </table>
      ${!fields.length && html`<div class="dim pad">no column matches</div>`}
    </div>
  </div>`;
}

function colFilterKey(e) {
  const c = S.cols;
  if (e.key === 'Tab') { e.preventDefault(); c.focusType = !c.focusType; redraw(); }
  if (e.key === 'Enter' || e.key === 'Escape') {
    e.preventDefault();
    c.typing = false;
    if (e.key === 'Escape') { c.name = ''; c.type = ''; }
    e.currentTarget.blur();
    redraw();
  }
}

function SQLPane() {
  const v = S.view;
  const on = st.sqlTabOn();
  const errs = on === 'errors';
  const lin = on === 'lineage';
  const tab = (name, label) => html`<span class=${'tab' + (on === name ? ' on' : '')}
    onMouseDown=${(e) => { e.stopPropagation(); st.pickSQLTab(name); st.setFocus(name === 'lineage' ? 'lineage' : 'sql'); redraw(); }}>${label}</span>`;
  const counts = v ? ` ↑${v.lineage.up.length} ↓${v.lineage.down.length}` : '';
  const lineageTab = tab('lineage', 'lineage' + counts);
  const title = st.hasErrors()
    ? html`${tab('errors', html`<span class="error">errors</span>`)} │ ${tab('sql', 'sql (stale)')} │ ${tab('columns', 'columns')} │ ${lineageTab}`
    : html`${tab('sql', 'sql')} │ ${tab('columns', 'columns')} │ ${lineageTab}`;
  const bits = [];
  if (errs || lin) {
    // The errors are the project's; lineage says what it is in its rows.
  } else if (v) {
    // Which action it is, the actions pane says.
    if (v.uncompiled) bits.push('not compiled');
    else if (v.sqlNote) bits.push(v.sqlNote);
    if (!S.cols.shown) { const note = vim.searchNote(S.buf); if (note) bits.push(note); }
  }
  // Which SQL an incremental table shows: its incremental run or its full
  // rebuild. The shown one is underlined like a tab; a click picks one.
  const side = (incr, label) => html`<span class=${'tab' + (S.incremental === incr ? ' on' : '')}
    onMouseDown=${(e) => { e.stopPropagation(); if (S.incremental !== incr) st.toggleIncremental(); redraw(); }}>${label}</span>`;
  const inc = v && v.hasIncremental
    ? html`<span class="variant" title="SQL for an incremental run or a full rebuild (i)"><span class="dim"> · </span>${side(true, 'incremental')}<span class="dim"> / </span>${side(false, 'full')}</span>` : null;
  return html`<${Pane} id="sql" focused=${S.focus === 'sql' || S.focus === 'lineage'} cls="sql-pane"
      busy=${compilingOver() && !errs ? '' : null} old=${errs ? '' : oldness()}
      title=${html`${title}${bits.length ? html`<span class="dim"> · ${bits.join(' · ')}</span>` : ''}${lin ? null : inc}${S.zoomed ? html`<span class="dim"> · zoom (esc)</span>` : ''}`}
      note=${lin && st.lineageRows().length ? 'double-click jumps · ⌫ back' : ''}>
    ${errs ? html`<${ErrorsBody} />` : lin ? html`<${LineageBody} />` : S.cols.shown ? html`<${ColumnsBody} />` : html`<${SQLBody} />`}
    ${!errs && !lin && html`<${SQLSearchLine} />`}
  <//>`;
}

// ErrorsBody is pane 2's errors tab: the failing compile's errors by
// kind, each with the file:line that enter or a click takes pane 1 to.
function ErrorsBody() {
  const e = S.errors;
  const rows = st.errorRows();
  const box = useRef(null);
  useLayoutEffect(() => {
    const el = box.current && box.current.querySelector('.err-row.cur');
    if (el) el.scrollIntoView({ block: 'nearest' });
  });
  if (!e) return html`<div class="dim pad">…</div>`;
  const count = e.errors.length + (e.compileErr ? 1 : 0);
  const meta = [];
  if (e.took) meta.push(e.took.toFixed(2) + 's');
  if (e.at) meta.push(new Date(e.at).toTimeString().slice(0, 8));
  const cmd = e.tool + ' compile';
  const focused = S.focus === 'sql';
  const cur = Math.min(S.errCursor, rows.length - 1);
  const pick = (i) => (ev) => { ev.stopPropagation(); st.setFocus('sql'); st.actOnErrorRow(i); redraw(); };
  return html`<div class="errs scroll" ref=${box}>
    <div class="errs-head"><span class="error">✗ ${e.tool} compile failed</span><span class="dim"> · ${count} error${count === 1 ? '' : 's'}</span></div>
    ${(meta.length || e.bin) && html`<div class="dim errs-meta">${meta.join(' · ')}${e.bin ? html`${meta.length ? ' · ' : ''}via <span class="mono">${e.bin}</span>` : ''}</div>`}
    ${rows.map((r, i) => {
      const cls = 'err-row' + (i === cur ? ' cur' : '') + (i === cur && focused ? ' focused' : '');
      if (r.fix) return html`<div class=${cls + ' err-fix'} onMouseDown=${pick(i)}><${FixBox} fix=${r.fix} run=${() => st.runFix()} /></div>`;
      if (r.group) {
        const open = !!S.errOpen[r.group.kind];
        return html`<div class=${cls + ' err-group'} onMouseDown=${pick(i)}>
          <span class="caret">${open ? '▾' : '▸'}</span> ${r.group.title} <span class="err-count">${r.group.list.length}</span></div>`;
      }
      const x = r.err;
      const [first, ...rest] = x.message.split('\n');
      const more = x.snippet || rest.join('\n').trim();
      const where = x.file ? x.file + (x.line ? ':' + x.line : '') : '';
      return html`<div class=${cls + ' err-item'} onMouseDown=${pick(i)}>
        <div class="err-msg">${first}</div>
        ${more && html`<pre class="err-snippet">${more}</pre>`}
        ${where && html`<div class="err-where"><span class="link">${where}</span></div>`}
      </div>`;
    })}
    <div class="dim errs-hint">run <code>${cmd}</code><button class="log-copy" title=${cmd}
      onMouseDown=${(ev) => { ev.stopPropagation(); ev.preventDefault(); st.copy(cmd, 'copied the command'); }}>copy</button> in a terminal for the full output</div>
  </div>`;
}

// FixBox is a command that may fix a failed compile or run, which run
// types into the terminal tab: drawn to be seen, in the errors tab and
// the log.
function FixBox({ fix, run }) {
  const act = (f) => (ev) => { ev.stopPropagation(); ev.preventDefault(); f(); redraw(); };
  return html`<div class="fix-box">
    <div class="fix-head">⚑ fix</div>
    <div class="fix-why">${fix.why}</div>
    <div class="fix-cmd"><span class="dim">$ </span>${fix.command}</div>
    <div class="fix-actions">
      ${S.caps.terminal && html`<button class="fix-run" title="type it into the terminal tab and press enter" onMouseDown=${act(run)}>▸ run in terminal</button>`}
      <button class="fix-copy" title=${fix.command} onMouseDown=${act(() => st.copy(fix.command, 'copied the command'))}>copy</button>
    </div>
  </div>`;
}

// ---- 5 source ----

// SourceBox holds the source pane's editor, which outlives it.
function SourceBox() {
  const box = useRef(null);
  useLayoutEffect(() => {
    const ed = sourceEditorDOM();
    box.current.appendChild(ed.box);
    ed.view.requestMeasure();
    if (SRC.editing && !S.overlay) ed.focus();
    return () => { if (ed.box.parentNode === box.current) box.current.removeChild(ed.box); };
  }, []);
  return html`<div class="src-box" ref=${box}></div>`;
}

const SRC = src.SRC;

function SourcePane() {
  const v = S.view;
  const bits = [];
  if (SRC.file) bits.push(SRC.file);
  let msg = '';
  if (!v && !S.plainFile) msg = 'no action selected';
  else if (v && !v.file) msg = 'this action has no file of its own';
  else if (SRC.error) msg = SRC.error;
  else if (SRC.loading && !SRC.view) msg = 'loading…';
  const state = [];
  if (SRC.saving) state.push(html`<span class="dim">saving…</span>`);
  else if (SRC.dirty) state.push(html`<span class="warn">unsaved</span>`);
  if (SRC.stale) state.push(html`<span class="error">changed on disk${src.autosaveMs() ? ' · autosave paused' : ''}</span>`);
  if (SRC.view && SRC.view.readOnly) state.push(html`<span class="dim">read-only: ${SRC.view.readOnly}</span>`);
  if (S.zoomed && S.focus === 'source') state.push(html`<span class="dim">zoom (esc)</span>`);
  // The file's path alone names the pane; autosave on says nothing.
  const parts = [...bits, ...state];
  const title = html`${parts.map((x, i) => html`${i ? html`<span class="dim"> · </span>` : ''}${x}`)}`;
  const w = st.paneSize('source');
  // With pane 2 hidden, the source pane is all the width there is.
  const style = w && !S.zoomed && st.rightShown() ? `flex:0 0 ${w}%` : '';
  // Source Control's difference (ADR 0007): the editor, when it shows the
  // file on disk, is its right side.
  const d = scm.diffNow();
  // The source is the file itself, never old; only its diagnostics wait
  // for the compile.
  return html`<${Pane} id="source" num="1" focused=${S.focus === 'source'} cls=${'source-pane' + (SRC.editing ? ' editing' : '') + (d ? ' diffing' : '')} style=${style} title=${title}
      busy=${compilingOver() ? '' : null}>
    ${d ? html`<${DiffPane} d=${d}><${SourceBox} /><//>` : html`<${SourceBox} />`}
    ${msg && !d && html`<div class="src-msg dim">${msg}</div>`}
  <//>`;
}

// marked is a difference row's text, its differing characters tinted as
// the git peek tints them.
function marked(r) {
  if (!r.marks.length) return r.text;
  const cls = r.op === '-' ? 'cm-peek-gone' : 'cm-peek-new';
  const out = [];
  let at = 0;
  for (const [from, to] of r.marks) {
    if (from > at) out.push(r.text.slice(at, from));
    out.push(html`<span class=${cls}>${r.text.slice(from, to)}</span>`);
    at = to;
  }
  if (at < r.text.length) out.push(r.text.slice(at));
  return out;
}

// SourceConflict is a refused save: the file changed on disk since the
// pane loaded it.
function SourceConflict() {
  const c = SRC.conflict;
  if (!c) return null;
  let diff = null;
  if (SRC.diffing) {
    const ed = sourceEditorDOM();
    const rows = src.diffRows(c.text, ed.doc());
    diff = rows.length
      ? html`<pre class="src-diff">${rows.map((r) => (r ? html`<div class=${r.op === '-' ? 'del' : r.op === '+' ? 'add' : ''}>${r.op} ${marked(r)}</div>` : html`<div class="dim">⋯</div>`))}</pre>`
      : html`<p class="dim">no difference: the file on disk is your text</p>`;
  }
  const keys = [['r', 'reload: drop your changes'], ['o', 'overwrite with yours'], ['d', SRC.diffing ? 'hide the difference' : 'show the difference'], ['esc', 'keep editing']];
  return html`<${Modal} title=${'⚠ ' + c.file + ' changed on disk'} cls="confirm amber" wide=${SRC.diffing} onClose=${() => { src.conflictCancel(); }}>
    <p class="warn">Another program changed this file after the pane loaded it. Saving now would lose that change.</p>
    ${SRC.diffing && html`<p class="dim"><span class="del">- on disk only</span> · <span class="add">+ yours only</span></p>`}
    ${diff}
    <div class="buttons">${keys.map(([k, d]) => html`<button class=${k === 'o' ? 'run-btn danger' : k === 'r' ? 'run-btn' : 'plain'}
      onClick=${() => { src.conflictKey(k); redraw(); }}><kbd>${k}</kbd> ${d}</button>`)}</div>
  <//>`;
}

// ---- 4 log ----

function JobsTable({ jobs, cursorOn }) {
  // A run that has not reported yet is loading, not empty: a workflow being
  // watched takes a few seconds to read.
  const loading = !!(S.run && S.run.running && (S.run.ran || S.run.seq));
  useTick(loading, 100);
  if (loading && (!jobs || !jobs.rows.length)) {
    const what = S.run.watching ? 'reading the workflow' : S.run.api ? 'waiting for Dataform' : 'waiting for the first job';
    return html`<div class="waiting"><div class="ramp"></div><div><span class="warn">${spinner()} ${what}…</span></div></div>`;
  }
  if (!jobs) return html`<div class="dim pad">no BigQuery jobs: nothing has been run here</div>`;
  if (!jobs.rows.length) return html`<div class="dim pad">no BigQuery jobs yet: each is listed as it starts</div>`;
  const rows = st.jobsRows(jobs);
  const failures = jobs.rows.some((r) => r.failure);
  const sort = S.jobsSort;
  const hit = S.jobsQuery.trim().toLowerCase().split(/\s+/)[0];
  const head = (c) => html`<th class=${(c.num ? 'num ' : '') + 'sortable' + (sort.col === c.id ? ' sorted' : '')}
      title=${'sort by ' + c.label.toLowerCase() + ' (S goes round the columns)'}
      onMouseDown=${(e) => { e.preventDefault(); st.setFocus('log'); st.sortJobsBy(c.id); redraw(); }}>${c.label}${sort.col === c.id ? (sort.desc ? ' ▼' : ' ▲') : ''}</th>`;
  return html`<div class="jobs-view">
    <${JobsBar} jobs=${jobs} shown=${rows.length} />
    <div class="table-scroll"><table class="grid jobs">
      <thead><tr>${st.JOB_COLS.map(head)}${failures && html`<th>Failure</th>`}</tr></thead>
      <tbody>${rows.map((r, i) => html`<tr class=${cursorOn && i === S.jobsCursor ? 'sel' : ''}
          ref=${(e) => { if (e && cursorOn && i === S.jobsCursor) scrollNearest(e); }}
          onMouseDown=${() => { S.jobsCursor = i; redraw(); }}
          onDblClick=${() => r.jobUrl && st.openLink(r.jobUrl)}>
        <td class="mono">${mark(r.target, hit)}</td><td class=${stateTone(r.state)}>${r.state}</td><td class="num">${r.duration}</td><td class="num">${r.slot}</td>
        <td class="num">${r.billed}</td><td class="num">${r.cost}</td>${failures && html`<td class="error">${r.failure}</td>`}</tr>`)}</tbody>
    </table>
    ${!rows.length && html`<div class="dim pad">no job matches “${S.jobsQuery}”: esc clears the search</div>`}</div>
  </div>`;
}

// JobsBar is over the jobs table: its search, how far the run has got
// and what its jobs have cost so far.
function JobsBar({ jobs, shown }) {
  const s = jobs.summary;
  const t = jobs.total;
  const n = jobs.rows.length;
  const typing = S.jobsTyping;
  const leave = (el, clear) => { if (clear) st.setJobsQuery(''); S.jobsTyping = false; el.blur(); redraw(); };
  let progress = null;
  if (s && s.actions) {
    const done = s.succeeded + s.failed + s.skipped;
    const seg = (k, cls) => k > 0 && html`<span class=${cls} style=${{ width: (100 * k) / s.actions + '%' }}></span>`;
    const part = (k, cls, label) => k > 0 && html`<span class=${cls}>${label}</span>`;
    progress = html`<span class="jobs-progress" title=${done + ' of ' + s.actions + ' actions done'}>
        ${seg(s.succeeded, 'ok')}${seg(s.failed, 'bad')}${seg(s.skipped, 'skip')}${seg(s.running, 'run')}</span>
      <span class="jobs-done"><b>${done}</b>/${s.actions} done</span>
      ${part(s.running, 'warn', `▶ ${s.running} running`)}${part(s.pending, 'dim', `· ${s.pending} pending`)}
      ${part(s.failed, 'error', `✗ ${s.failed} failed`)}${part(s.skipped, 'dim', `– ${s.skipped} skipped`)}`;
  }
  const stat = (label, v, cls = '') => v && html`<span class="jobs-stat"><span class="dim">${label}</span> <span class=${cls}>${v}</span></span>`;
  const jobsN = t.target.match(/\((\d+) BigQuery job/);
  return html`<div class="col-search-bar jobs-bar">
    <div class=${'col-search-box' + (S.jobsQuery ? ' active' : '')}>
      <span class="col-search-icon" aria-hidden="true">⌕</span>
      <input class="col-search-input" value=${S.jobsQuery} placeholder="Search jobs… (/)" title="Search by target, state or failure" spellcheck="false" autocomplete="off"
        ref=${(e) => e && typing && document.activeElement !== e && e.focus()}
        onInput=${(e) => { st.setJobsQuery(e.currentTarget.value); redraw(); }}
        onFocus=${() => { if (!S.jobsTyping) { S.jobsTyping = true; st.setFocus('log'); redraw(); } }}
        onBlur=${() => { if (S.jobsTyping) { S.jobsTyping = false; redraw(); } }}
        onKeyDown=${(e) => {
          if (e.key === 'Escape') { e.preventDefault(); leave(e.currentTarget, true); }
          else if (e.key === 'Enter' || e.key === 'ArrowDown') { e.preventDefault(); leave(e.currentTarget, false); }
        }} />
      ${S.jobsQuery && html`<button class="col-search-clear" title="Clear search (esc)" onMouseDown=${(e) => { e.preventDefault(); st.setJobsQuery(''); redraw(); }}>×</button>`}
    </div>
    ${shown !== n && html`<span class="dim col-count">${shown} of ${n}</span>`}
    <div class="jobs-summary">
      ${progress}
      ${stat('elapsed', s && s.elapsed)}
      ${stat('jobs', jobsN && jobsN[1])}
      ${stat('slot', t.slot)}
      ${stat('billed', t.billed)}
      ${stat('est.', t.cost, 'jobs-cost')}
      ${s && s.reading > 0 && S.run && S.run.running && html`<span class="jobs-stat warn">${spinner()} reading ${s.reading} job cost${s.reading === 1 ? '' : 's'}…</span>`}
    </div>
  </div>`;
}

const stateTone = (s) => (s.startsWith('✓') ? 'ok' : s.startsWith('✗') ? 'error' : s.startsWith('▶') ? 'warn' : 'dim');

function LogLines() {
  const r = S.run;
  const el = useRef(null);
  useLayoutEffect(() => {
    const box = el.current;
    if (box && S.logFollow) box.scrollTop = box.scrollHeight;
  });
  if (!r || !r.seq) return html`<div class="dim pad">no log yet: r runs the selected action</div>`;
  return html`<div class="log-scroll" ref=${el}
      onScroll=${(e) => { const b = e.currentTarget; S.logFollow = b.scrollTop + b.clientHeight >= b.scrollHeight - 2; }}>
    ${r.lines.map((l) => l.fix ? html`<${FixBox} fix=${l.fix} run=${() => st.runFix(true)} />` : html`<div class=${'log-line ' + (l.tone || '')}>${logText(l)}${l.copy && html` <button class="log-copy" title=${l.copy}
        onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); st.copy(l.copy, 'copied the command'); }}>copy</button>`}</div>`)}
  </div>`;
}

// logText is a log line's text, in the tool's colours when it had any.
function logText(l) {
  if (!l.spans) return l.text || ' ';
  return l.spans.map((s) => (s.c ? html`<span class=${s.c}>${s.t}</span>` : s.t));
}

function HistoryList() {
  const hv = S.history;
  if (hv.err) return html`<div class="error pad">could not read the kept runs: ${hv.err}</div>`;
  if (!hv.rows.length) return html`<div class="dim pad">no runs kept yet: the runs xf starts here are listed</div>`;
  return html`<div class="table-scroll"><table class="grid">
    <thead><tr><th>When</th><th>Via</th><th class="num">Jobs</th><th>State</th><th class="num">Duration</th><th class="num">Billed</th><th class="num">Est. cost</th><th>Command</th></tr></thead>
    <tbody>${hv.rows.map((r, i) => html`<tr class=${i === S.historyCursor && S.focus === 'log' ? 'sel' : ''}
        ref=${(e) => { if (e && i === S.historyCursor) scrollNearest(e); }}
        onMouseDown=${() => { S.historyCursor = i; redraw(); }} onDblClick=${() => st.openKept(r)}>
      <td>${r.when}</td><td>${r.via}</td><td class="num">${r.jobs}</td><td class=${stateTone(r.state)}>${r.state}</td>
      <td class="num">${r.duration}</td><td class="num">${r.billed}</td><td class="num">${r.cost}</td><td class="dim mono cmd" title=${r.deferred ? 'read from prod:\n' + r.deferred.join('\n') : undefined}>${r.deferred && html`<span class="defer-prod">→ ${r.deferred.length} from prod · </span>`}${r.command}</td></tr>`)}</tbody>
  </table></div>`;
}

// TermBody is the terminal tab's box, which terminal.js fills: it loads
// the first time the tab shows.
function TermBody({ focused }) {
  const box = useRef(null);
  useEffect(() => {
    const el = box.current;
    import('./terminal.js').then((m) => m.mountTerm(el, focused));
  });
  return html`<div class="term-box" ref=${box}></div>`;
}

function LogPane() {
  const r = S.run;
  const focused = S.focus === 'log';
  const tabs = S.caps.history;
  const termTab = S.caps.terminal;
  let title = 'log', body;
  const jobs = st.jobsShown();
  if (tabs || termTab) {
    const n = jobs ? jobs.rows.length : 0;
    const tab = (id, label) => html`<span class=${'tab' + (S.logTab === id ? ' on' : '')}
      onMouseDown=${(e) => { e.stopPropagation(); S.logTab = id; st.setFocus('log'); if (id === 'history') st.get('/api/history').then((hv) => { S.history = hv; redraw(); }); redraw(); }}>${label}</span>`;
    title = tabs ? html`${tab('log', 'log')} │ ${tab('jobs', n ? `jobs (${n})` : 'jobs')} │ ${tab('history', 'history')}` : tab('log', 'log');
    if (termTab) title = html`${title} │ ${tab('term', 'terminal')}`;
  }
  let rest = '';
  if (S.logTab === 'term' && termTab) {
    // An overlay (the palette, say) keeps the keys while it is open.
    body = html`<${TermBody} focused=${focused && !S.overlay} />`;
  } else if (S.logTab === 'history' && tabs) {
    if (S.historyOpen && S.historyJobs) {
      rest = S.historyJobs.title;
      body = html`<${JobsTable} jobs=${S.historyJobs} cursorOn=${focused} />`;
    } else body = html`<${HistoryList} />`;
  } else if (S.logTab === 'jobs' && tabs) {
    if (!(r && r.ran) && S.history.last) rest = 'last run · ' + S.history.last.title;
    body = html`<${JobsTable} jobs=${jobs} cursorOn=${focused} />`;
  } else {
    body = html`<${LogLines} />`;
  }
  if (!rest && r && r.seq && S.logTab !== 'history' && S.logTab !== 'term') {
    rest = [r.label, r.title].filter(Boolean).join(' · ');
    if (r.running) rest += r.cancelling ? (r.api ? ' · ctrl+c stops watching' : ' · ctrl+c force-kills') : ' · ctrl+c cancels';
  }
  return html`<${Pane} id="log" focused=${focused} cls="log-pane" style=${sized('log', S.zoomed)}
      title=${html`${title}${rest ? html`<span class="dim"> · ${rest}</span>` : ''}`}
      note=${r && r.url ? html`<a href=${r.url} target="_blank" rel="noopener">u workflow invocation ↗</a>` : ''}>${body}<//>`;
}

// ---- the status line and the footer ----

function StatusLine() {
  const s = S.state;
  const b = S.boot;
  useTick((s && s.compiling) || !!scm.G.busy, 100);
  const parts = [];
  if (b.target) parts.push('target ' + b.target);
  if (s && s.hasGraph) {
    parts.push(s.actions + ' actions');
    if (s.coreVersion) parts.push((b.backend === 'dbt' ? 'dbt ' : 'core ') + s.coreVersion);
  }
  if (s && s.compiledAt) {
    const t = new Date(s.compiledAt);
    parts.push(`compiled ${t.toTimeString().slice(0, 8)} (${s.took.toFixed(1)}s)${s.saved ? ', saved' : ''}`);
  }
  let state;
  if (!s || s.compiling) {
    state = html`${s && s.outdated ? html`<span class="warn">outdated · </span>` : ''}<span class="warn">${spinner()} compiling…${s && (s.reason || s.outdated) ? ' (' + (s.reason || s.outdated) + ')' : ''}</span>`;
  } else if (s.errors > 0) {
    state = html`${s.stale ? html`<span class="warn">stale · </span>` : ''}<button class="link error" onClick=${() => { st.showErrorsOrSay(); redraw(); }}>✗ ${s.errors} compile error${s.errors === 1 ? '' : 's'} (e)</button>`;
  } else {
    state = html`<span class="ok">idle</span>${s.dryRunErrors > 0 ? html` · <button class="link error" onClick=${() => st.openErrors()}>✗ ${s.dryRunErrors} dry run error${s.dryRunErrors === 1 ? '' : 's'} (e)</button>` : ''}${FileDryRun()}`;
  }
  const r = S.run;
  return html`<div class="status">
    <span class="badge">${b.backend.toUpperCase()}</span>
    <${ProjectButton} /><span class="dim"> · </span><${BranchIndicator} /><span class="dim status-parts" title=${parts.join(' · ')}>${parts.map((p) => p + ' · ').join('')}</span>${state}
    ${r && r.running && html`<span class="dim"> · </span><span class="warn">▶ running ${r.short}${r.api ? ' in GCP' : ''} (ctrl+c cancels)</span>`}
    ${scm.G.busy && html`<span class="dim"> · </span><span class="warn git-busy" title="Source Control's ✕ (ctrl+g) stops it">${spinner()} git: ${scm.G.busy}…</span>`}
    <span class="spacer"></span>
    ${!S.connected && html`<span class="error">● reconnecting to xf web…</span>`}
    ${S.caps.follow && html`<span class=${'follow-ind ' + (!S.following || st.followWaits() ? 'warn' : 'dim')}>${!S.following ? 'follow paused (F)' : st.followWaits() ? 'follow waits: unsaved source' : 'following editor'}</span>`}
    ${S.caps.defer && html`<${DeferIndicator} />`}${st.hooks.pythonIndicator && st.hooks.pythonIndicator()}<${WindowsPill} /><${ThemeButton} />
  </div>`;
}

// DeferIndicator is the status line's defer to prod switch: a click turns
// it on or off for every client of the project's engine.
function DeferIndicator() {
  const s = S.state;
  const on = !!(s && s.defer);
  const err = s && s.deferError;
  const title = on
    ? 'Defer to prod is on: upstream tables not built in dev are read from prod in dry runs and runs.' + (err ? '\nNot applied: ' + err : '') + '\nClick to turn it off.'
    : 'Defer to prod is off: click to read upstream tables not built in dev from prod.';
  return html`<button class=${'link defer-ind ' + (on ? (err ? 'warn' : 'defer-prod') : 'dim')} title=${title}
    onClick=${() => st.toggleDefer()}>${on ? (err ? '⚠ defer to prod' : '→ defer to prod') : 'defer off'}</button>`;
}

// FileDryRun is the status line's failed dry run of the file the page
// shows: "this file: Not found: Table …", cut short when narrow, the
// action and whole message on hover.
function FileDryRun() {
  const f = src.fileDryRun();
  if (!f) return null;
  const more = f.count > 1 ? ` (+${f.count - 1} more in this file)` : '';
  const title = f.name + (f.label ? ' (' + f.label + ')' : '') + ': ' + f.msg + more + '\ne lists them all';
  return html`<span class="dim"> · this file: </span><button class="link error file-dry" title=${title}
    onClick=${() => st.openErrors()}>${f.msg}${more}</button>`;
}

// ProjectButton is the status line's project, which opens the projects
// to switch to: "finance (+2)" with two others open, lit while one of
// them runs.
export function ProjectButton() {
  const others = S.open.filter((o) => o.key !== S.key);
  const running = others.filter((o) => o.running);
  const title = 'Switch or add a project (' + (st.onMac ? '⇧⌘p' : 'ctrl+shift+p') + ' › Project: switch or add)' +
    (others.length ? '\nalso open: ' + others.map((o) => o.name + (o.running ? ' (running)' : '')).join(', ') : '');
  return html`<button class=${'link proj-btn' + (running.length ? ' warn' : ' dim')} title=${title} onClick=${() => { st.openProjects(); redraw(); }}>${S.boot.name}${others.length ? ` (+${others.length})` : ''}${running.length ? ' ▶' : ''}</button>`;
}

// BranchIndicator is the status line's branch: "⎇ main ●3 ↑2 ↓1", whose
// hover lists the files and commits it counts. A click opens B.
function BranchIndicator() {
  const parts = S.branch;
  if (!parts || !parts.length) return null;
  const detail = parts.flatMap((p) => p.detail || []);
  const title = [...detail, (detail.length ? '\n' : '') + 'B switches branch'].join('\n');
  return html`<button class="link branch-ind" title=${title} onClick=${() => st.openBranches()}>${parts.map((p) =>
    html`<span class=${p.tone || ''}>${p.text}</span>`)}</button>${S.caps.changed && html`<button class="link base-ind dim"
    title="R, I and C compare with this branch · click to change it" onClick=${() => { st.openBase(); redraw(); }}> vs ${S.boot.defaultBranch}</button>`}<span class="dim"> · </span><${ForgePill} />`;
}

// Keys is a bar of keys and what they do. A tap on a key presses it, for
// a phone, which has no keyboard but the one that types into a box.
export function Keys({ list }) {
  return html`<span class="keys">${list.map(([k, d]) => html`<span class="key">${chipKeys(k).map((c, i) => html`${i ? '/' : ''}<kbd
    class=${c.seq ? 'tap' : ''} onClick=${c.seq && ((e) => { e.preventDefault(); press(c.seq); })}>${c.label}</kbd>`)} ${d}</span>`)}</span>`;
}

const keyAlias = { '⌫': 'backspace', '↑': 'up', '↓': 'down', '←': 'left', '→': 'right', del: 'Delete' };

// chipKeys splits a key's label into the keys it names, each with the
// presses it is: "j/k" is j or k, "hjkl" h, j, k or l, "yy" y twice.
// ctrl+c, which quits xf web, is not pressed by a tap.
function chipKeys(label) {
  const parts = label === '/' ? ['/'] : label.includes('/') ? label.split('/') : label === 'hjkl' ? [...label] : [label];
  return parts.map((p) => {
    p = p.trim();
    if (p === st.paletteKey) return { label: p, seq: ['palette'] };
    if (!p || p === 'ctrl+c' || p === st.saveKey || /\s|-./.test(p)) return { label: p };
    if (/^(.)\1$/.test(p)) return { label: p, seq: [p[0], p[0]] };
    return { label: p, seq: [keyAlias[p] || p] };
  });
}

function press(seq) {
  for (const k of seq) st.handleKey(k);
  redraw();
}

function Footer() {
  if (S.flash) return html`<div class="footer warn">${S.flash}</div>`;
  const b = S.buf;
  let list = [];
  const target = (ref) => {
    if (!ref) return [];
    const out = [];
    if (ref.consoleUrl) out.push(['u', 'BigQuery']);
    out.push([ref.file ? 'y/Y' : 'y', ref.file ? 'copy name/path' : 'copy name']);
    return out;
  };
  // The footer leaves out vim's own keys (hjkl, j/k, yy, ]g, gd, :w…);
  // ? lists them all.
  if (st.columnsFocused()) list = [['y', 'copy name'], ['Y', 'name and type']];
  else if (S.focus === 'sql' && st.errorsTabShown()) {
    const onFix = !!(st.errorRows()[S.errCursor] || {}).fix;
    const enter = !onFix ? 'open group / go to line' : S.caps.terminal ? 'run in terminal' : 'copy command';
    list = [['enter', enter], ['y', 'copy'], [st.saveKey, 'compile']];
  }
  else if (S.focus === 'sql' && b.visual) list = [['y', 'copy selection'], ['esc', 'cancel']];
  else if (S.focus === 'sql' && b.hlHits) list = [['esc', 'clear search']];
  else if (S.focus === 'source' && SRC.editing) list = [['ctrl+w', 'leave editor'], [st.saveKey, 'save'], ['ctrl+shift+o', 'symbols'], ['right-click', 'menu']];
  else if (S.focus === 'source') list = [['enter', 'edit'], ['E', 'hide source']];
  else if (S.focus === 'actions') list = [['enter', 'show its sql'], ...target(S.view)];
  else if (S.focus === 'lineage') {
    const rows = st.lineageRows();
    const r = rows[S.lineageCursor];
    list = [...target(r && r.ref), ['⌫', 'back'], ['tab', 'next tab'], ['O', 'sql']];
  } else if (S.focus === 'run') {
    if (S.caps.changed) list.push(['R', 'run changed actions']);
    list.push(['T', 'run tags'], ['d', 'deps'], ['D', 'dependents'], ['f', 'full refresh']);
    if (S.caps.api) list.push(['a', 'run via Dataform API']);
  } else if (S.focus === 'log') {
    if (st.logTabs().length > 1) list.push([st.logTabLabel, 'next tab']);
    if (S.logTab === 'history') list = [[st.logTabLabel, 'next tab'], ['enter', 'open run'], ['⌫', 'back'], ['X', 'clear history']];
    const jobs = st.shownJobs();
    if (jobs) {
      // As the TUI: u opens an API run's page, else the selected job.
      const row = st.jobsRows(jobs)[S.jobsCursor];
      list = [...(row && row.jobUrl ? [['enter', 'BigQuery job']] : []), ['S', 'sort'], ...list.filter(([k]) => k === st.logTabLabel || k === '⌫' || k === 'X')];
      if (jobs.url) list.unshift(['u', 'open run']);
      else if (row && row.jobUrl) list.unshift(['u', 'BigQuery job']);
    } else if (S.logTab === 'history') {
      const row = S.history.rows[S.historyCursor];
      if (row && row.url) list.unshift(['u', 'open run']);
    } else if (S.run && S.run.url) list.unshift(['u', 'open run']);
  }
  // / searches the focused pane, as in vim; the palette has its own key.
  let find = [];
  if (st.columnsFocused()) find = S.view && S.view.columns.fields.length ? [['/', 'filter']] : [];
  else if (S.focus === 'log' && st.shownJobs()) find = [['/', 'search']];
  else if (S.focus === 'sql' && b.text.length) find = [['/', 'search']];
  else if (S.focus === 'source') find = [['/', 'search']];
  if (S.focus === 'source' && SRC.editing) return html`<div class="footer"><${Keys} list=${list} /></div>`;
  // In the terminal every other key is the shell's: only xf's own show.
  if (S.focus === 'log' && S.logTab === 'term' && S.caps.terminal) {
    return html`<div class="footer"><${Keys} list=${[[st.paletteKey, 'find'], [st.commandsKey, 'commands'],
      [st.onMac ? '⌘z' : 'alt+z', S.zoomed ? 'unzoom' : 'zoom'], [st.logTabLabel, 'next tab'], ['ctrl+j', 'hide'], ['ctrl+1', 'source']]} /></div>`;
  }
  list = [[st.paletteKey, 'find'], ...find, ...list, ['z', S.zoomed ? 'unzoom' : 'zoom'], ['r', 'run'], ['?', 'help'], ...(['actions', 'lineage', 'run', 'defer', 'sql'].includes(S.focus) && !S.zoomed ? [['esc', 'projects']] : [])];
  return html`<div class="footer"><${Keys} list=${list} /></div>`;
}

// ---- overlays ----

export function Modal({ title, cls, children, wide, onClose, api }) {
  // A click outside closes it, leaving the keys where closing put them.
  return html`<div class="overlay-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) { e.preventDefault(); (onClose || st.closeOverlay)(); redraw(); } }}>
    <div class=${'overlay pane focused ' + (cls || '') + (wide ? ' wide' : '') + (api ? ' api' : '')}>
      <header class="pane-title"><span class="pane-name">${title}${api && html`<span class="gcp"> · GCP</span>`}</span></header>
      <div class="pane-body">${children}</div>
    </div>
  </div>`;
}

// pathTail is the end of a project path, its last n parts: what tells
// files apart in a project of a few deep folders.
function pathTail(path, n) {
  const parts = path.split('/');
  return parts.length > n ? '…/' + parts.slice(-n).join('/') : path;
}

// underline marks where any of words (lower case) is in text.
export function underline(text, words) {
  if (!words.length || !text) return text;
  const lower = text.toLowerCase();
  const hit = new Array(text.length).fill(false);
  for (const w of words) {
    for (let i = lower.indexOf(w); i >= 0; i = lower.indexOf(w, i + w.length)) hit.fill(true, i, i + w.length);
  }
  const out = [];
  let start = 0;
  for (let i = 1; i <= text.length; i++) {
    if (i === text.length || hit[i] !== hit[start]) {
      const piece = text.slice(start, i);
      out.push(hit[start] ? html`<u>${piece}</u>` : piece);
      start = i;
    }
  }
  return out;
}

// A symbol's kind as its chip and its filter tab name it.
const symKindLabel = (k) => ({ bq_table: 'TABLE' })[k] || (k || '').toUpperCase();
const symKindPlural = (k) => ({ bq_table: 'tables' })[k] || k + 's';

// symName is a symbol's name with a ref's call, ref(" and "), faint
// around what it names.
function symName(it, words) {
  const m = it.kind === 'ref' && /^([^'"`]*['"`])(.*)(['"`][^'"`]*)$/.exec(it.name);
  if (!m) return underline(it.name, words);
  return html`<span class="faint">${m[1]}</span>${underline(m[2], words)}<span class="faint">${m[3]}</span>`;
}

const baseName = (path) => path.slice(path.lastIndexOf('/') + 1);
const gitWords = { M: 'changed', A: 'added', U: 'untracked', C: 'in conflict' };

// fileWhy is a palette file row's tooltip, as the tree's rows have.
function fileWhy(f) {
  return [f.path, f.git && gitWords[f.git], f.errors && f.errors + ' error' + (f.errors === 1 ? '' : 's'), f.actions && f.actions + ' action' + (f.actions === 1 ? '' : 's')].filter(Boolean).join(' · ');
}

// ProjectPick is a row of the palette's open projects: the name and path,
// and what the project is doing, as the column's projects tab marks it.
function ProjectPick({ it, i, words }) {
  const p = S.palette;
  const here = it.key === S.key;
  const errors = it.errors || it.dryRunErrors || 0;
  const unsaved = it.unsaved || [];
  return html`<div class=${'pal-row two' + (i === p.cursor ? ' sel' : '')} key=${it.key}
      title=${[it.root, here && 'shown in this tab', unsaved.length && 'unsaved: ' + unsaved.join(', ')].filter(Boolean).join('\n')}
      ref=${(e) => { if (e && i === p.cursor) scrollNearest(e); }}
      onMouseMove=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
      onMouseDown=${(e) => { e.preventDefault(); st.pickFromPalette(i, e.shiftKey); redraw(); }}>
    <div class="pal-text">
      <div class="pal-name">${underline(it.name, words)}${here && html` <span class="faint">· this tab</span>`}</div>
      <div class="pal-where">${underline(it.root || '', words)}</div>
    </div>
    ${it.compiling && !it.running && html`<span class="warn proj-mark">◌</span>`}
    ${it.running && html`<span class="warn proj-mark">▶</span>`}
    ${errors > 0 && html`<span class="error proj-mark">✗${errors}</span>`}
    ${unsaved.length > 0 && html`<span class="warn proj-mark">●</span>`}</div>`;
}

// ActionRow is a found action (/api/find) as the palette lists it: its
// name, where it is and its kind, with words underlined. The dependency
// inspector's model picker lists them the same way.
export function ActionRow({ it, sel, words, onHover, onPick }) {
  return html`<div class=${'pal-row two' + (sel ? ' sel' : '') + (it.external || it.disabled ? ' ext' : '') + (it.changed ? ' changed' : '')}
      title=${it.id + (it.changed ? ' · changed on this branch' : '')}
      ref=${(e) => { if (e && sel) scrollNearest(e); }}
      onMouseMove=${onHover}
      onMouseDown=${(e) => { e.preventDefault(); onPick(); }}>
    <div class="pal-text">
      <div class="pal-name">${underline(it.name, words)}</div>
      <div class="pal-where">${underline(it.label.slice(0, Math.max(it.label.length - it.name.length - 1, 0)), words)}${it.file && html`<span class="faint"> · </span><span class="faint">${underline(pathTail(it.file, 3), words)}</span>`}</div>
    </div>
    <span class=${'kind-chip ' + kindClass(it.short)}>${it.disabled ? 'off ' : ''}${it.kind}</span></div>`;
}

function Palette() {
  const p = S.palette;
  const list = st.paletteList();
  const commands = st.commandQuery() !== null;
  const projects = st.projectQuery() !== null;
  const symbols = st.symbolQuery() !== null;
  const files = st.fileQuery() !== null;
  const pf = files ? st.paletteFilesState() : null;
  const subject = S.view ? ' · ' + (symbols ? (S.view.file || (S.view.schema + '.' + S.view.name)) : (S.view.schema + '.' + S.view.name)) : '';
  const title = p.scratch ? 'scratch commands' : projects ? 'open projects' : symbols ? 'symbols' + subject : commands ? 'commands' + subject : files ? 'find file' : 'find action';
  const kinds = symbols ? st.symbolKinds() : [];
  const total = projects ? S.open.length : symbols ? (p.symbols ? p.symbols.length : 0) : commands ? p.commands.length : files ? (pf.files || []).length : p.total;
  const count = projects ? list.length : symbols ? list.length : commands ? list.length : files ? st.matchedFiles().length : p.found;
  const move = (n) => { p.cursor = Math.min(Math.max(p.cursor + n, 0), Math.max(list.length - 1, 0)); redraw(); };
  // The query's words, but for its $kind ones, are underlined where they
  // are in a row's name, dataset or path.
  const words = projects
    ? st.projectQuery().toLowerCase().split(/\s+/).filter(Boolean)
    : symbols
    ? (st.symbolQuery() || '').toLowerCase().split(/\s+/).filter(Boolean)
    : commands ? [] : (files ? st.fileQuery() : st.actionQuery()).toLowerCase().split(/\s+/).filter((w) => w && !w.startsWith('$'));
  const empty = projects ? 'no matching open project' : symbols ? (p.symbols ? 'no matching symbols' : 'loading symbols…')
    : files ? (pf.error || (pf.files ? 'no matching files' : 'loading files…'))
    : !commands && !(S.state && S.state.hasGraph) ? 'no actions yet: waiting for a compile'
    : p.badKind ? (p.badKind === '$' ? 'kinds: ' : `no kind ${p.badKind}, only `) + S.boot.kinds.map((k) => '$' + k).join(' ') : 'no matches';
  return html`<${Modal} title=${title} cls="palette">
    <div class="query">
      <span class="prompt">${(commands || symbols || projects) ? '' : '>'}</span>
      <input value=${p.query} placeholder=${projects ? 'find open project' : symbols ? 'find symbol, table or :line' : commands ? 'find command' : files ? 'find file · # action · > command · @ symbol' : 'find action'} spellcheck="false" autocomplete="off"
        ref=${(e) => e && document.activeElement !== e && e.focus()}
        onInput=${(e) => st.setPaletteQuery(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = st.keyName(e);
          if (k === 'esc' || k === 'ctrl+c') { e.preventDefault(); st.cancelPalette(); redraw(); }
          else if (k === 'enter') { e.preventDefault(); st.pickFromPalette(undefined, e.shiftKey); redraw(); }
          // The palette's key again moves down, as in VS Code's quick open:
          // off a Mac a tab's ctrl+n is the browser's, so ctrl+p is the
          // pair's half that reaches the page. An app window has both.
          else if (st.paletteKeyMovesDown && st.isPaletteKey(e)) { e.preventDefault(); move(1); }
          else if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); move(-1); }
          else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); move(1); }
          else if (k === 'pgup') { e.preventDefault(); move(-15); }
          else if (k === 'pgdown') { e.preventDefault(); move(15); }
          else if (symbols && (k === 'tab' || k === 'shift+tab')) { e.preventDefault(); st.cycleSymbolKind(k === 'tab' ? 1 : -1); redraw(); }
          else if (k === '/' && !p.query) e.preventDefault(); // already finding
        }} />
      <span class="dim count">${count}/${total}</span>
    </div>
    ${kinds.length > 1 && html`<div class="sym-filter">
      <span class="faint">filter</span>
      ${[['', 'all', kinds.reduce((n, [, c]) => n + c, 0)], ...kinds.map(([k, c]) => [k, symKindPlural(k), c])].map(([k, label, c]) => html`<button
        class=${'sym-tab' + ((p.symKind || '') === k ? ' on' : '')} key=${k}
        onMouseDown=${(e) => { e.preventDefault(); p.symKind = k; p.cursor = 0; redraw(); }}>${label} <span class="dim">(${c})</span></button>`)}
      <span class="faint sym-cycle">tab to cycle</span>
    </div>`}
    <div class="palette-list scroll">
      ${!list.length && html`<div class="dim pad">${empty}</div>`}
      ${list.map((it, i) => (projects
        ? html`<${ProjectPick} it=${it} i=${i} words=${words} />`
        : it.run
        ? html`<div class=${'pal-row cmd' + (i === p.cursor ? ' sel' : '')} key=${it.title}
              ref=${(e) => { if (e && i === p.cursor) scrollNearest(e); }}
              onMouseMove=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
              onMouseDown=${(e) => { e.preventDefault(); st.pickFromPalette(i); redraw(); }}>
            <span class="lead">${i === p.cursor ? '▸' : ''}</span><span class="pal-name">${it.title}</span>
            <span class=${'state ' + (it.state === 'on' ? 'ok' : 'dim')}>${it.state || ''}</span>${it.key && html`<kbd>${it.key}</kbd>`}</div>`
        : files
        ? html`<div class=${'pal-row two file' + (i === p.cursor ? ' sel' : '') + (it.git ? ' git-' + it.git : '') + (it.errors ? ' has-errors' : '')}
              key=${it.path} title=${fileWhy(it)}
              ref=${(e) => { if (e && i === p.cursor) scrollNearest(e); }}
              onMouseMove=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
              onMouseDown=${(e) => { e.preventDefault(); st.pickFromPalette(i); redraw(); }}>
            <div class="pal-text">
              <div class="pal-name">${underline(baseName(it.path), words)}</div>
              ${it.path.includes('/') && html`<div class="pal-where">${underline(it.path.slice(0, it.path.lastIndexOf('/')), words)}</div>`}
            </div>
            ${it.actions > 1 && html`<span class="dim pal-actions">${it.actions} actions</span>`}
            ${it.errors > 0 && html`<span class="tree-err">${it.errors}</span>`}
            ${it.git && html`<span class="tree-git">${it.git}</span>`}</div>`
        : symbols
        ? html`<div class=${'pal-row sym' + (i === p.cursor ? ' sel' : '')} key=${it.name + '-' + it.from}
              title=${it.name + ' · ' + it.kind + ' (line ' + it.line + ')'}
              ref=${(e) => { if (e && i === p.cursor) scrollNearest(e); }}
              onMouseMove=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
              onMouseDown=${(e) => { e.preventDefault(); st.pickFromPalette(i); redraw(); }}>
            <span class=${'sym-chip ' + kindClass(it.kind)}>${symKindLabel(it.kind)}</span>
            <span class="sym-name">${symName(it, words)}</span>
            <span class="sym-line"><span class="faint">line</span> :${it.line}</span>
            ${i === p.cursor && html`<kbd class="sym-enter">enter ↵</kbd>`}</div>`
        : html`<${ActionRow} key=${it.id} it=${it} sel=${i === p.cursor} words=${words}
              onHover=${() => { if (p.cursor !== i) { p.cursor = i; redraw(); } }}
              onPick=${() => { st.pickFromPalette(i); redraw(); }} />`))}
    </div>
    <div class="overlay-keys"><${Keys} list=${p.scratch
      ? [['enter', 'run'], ['↑/↓', 'move'], ['esc', 'cancel']]
      : projects
      ? [['enter', S.openIn === 'new-tab' ? 'open in a new tab' : 'open here'], ['shift+enter', S.openIn === 'new-tab' ? 'open here' : 'open in a new tab'], ['↑/↓', 'move'], ['esc', 'cancel']]
      : symbols
      ? [['enter', 'jump'], ['↑/↓', 'move'], ...(kinds.length > 1 ? [['tab', 'filter by kind']] : []), ['esc', 'cancel']]
      : files
      ? [['enter', 'open'], ['↑/↓', 'move'], ['#', 'actions'], ['$kind', 'find one kind'], ['>', 'commands'], ['@', 'symbols'], ['esc', 'cancel']]
      : [['enter', 'select'], ['↑/↓', 'move'], ['$kind', 'find one kind'], ['>', 'commands'], ['ctrl+shift+o', 'symbols in model'], ['@', 'symbols'], ['esc', 'cancel']]} /></div>
  <//>`;
}

// helpSections lists every key, as the TUI's ? does, leaving out what this
// project can't do.
function helpSections() {
  const c = S.caps;
  const global = [[st.paletteKey, st.paletteKeyMovesDown ? 'find file (again: down the list)' : 'find file'], ['# in it', 'find action'], ['/', 'search the focused pane'], ['$kind', 'find one kind'], [st.commandsKey + ' or >', 'commands'], [']', 'next action in file'], ['[', 'prev action in file'],
    [st.saveKey, 'save and compile'], ['i', 'incremental/full'], ['E', 'show/hide source'], ['ctrl+,', 'settings and project info'], ['s', 'scratch query from this model'], ['e', 'errors'], ...(c.terminal ? [['ctrl+j/⌘j', 'show/hide terminal'], [st.onMac ? '⌘z/⌥z in it' : 'alt+z in it', 'zoom terminal'], ['L', 'show/hide log']] : [['ctrl+j/⌘j', 'show/hide log']])];
  if (c.branches) global.push(['B', 'switch branch']);
  if (c.follow) global.push(['F', 'follow editor on/off']);
  if (c.open) global.push(['o', 'open in editor']);
  global.push(['?', 'help'], ['ctrl+q', 'go back one level: close the page, else the project (asks only if something would be lost)'], ['ctrl+c', 'cancel run']);
  const run = [];
  if (c.run) run.push(['r', 'run']);
  if (c.changed) run.push(['R', 'run changed actions']);
  if (c.run) run.push(['T', 'run tags']);
  if (c.impact) run.push(['I', 'column impact of changes']);
  if (c.cost) run.push(['C', 'query cost of changes']);
  if (c.run) run.push(['d', 'deps'], ['D', 'dependents'], ['f', 'full refresh']);
  if (c.api) run.push(['a', 'run via Dataform API']);
  const panes = [[st.onMac ? '⌘b' : 'ctrl+b', 'show/hide the file tree'], ['ctrl+g', 'show/hide source control'], [st.projectsKey, 'show/hide open projects'], [st.revealKey, 'show the open file in the file tree'], [search.searchKey + (st.onMac ? '/ctrl+shift+f' : ''), 'search in files'], ['F4/shift+F4', 'next/prev search match'], ['ctrl+0', 'file tree / source control / search'], ['ctrl+1', 'source (left)'], ['ctrl+2', 'inspector / sql (right)'], ['ctrl+3', 'log (bottom)'], [st.rightKey(), 'show/hide pane 2 (right)'],
    ['z', 'zoom pane'], ['esc', 'unzoom'], ['O', 'lineage tab (again: the sql)'], ...(c.terminal ? [['ctrl+j/⌘j', 'show/hide terminal'], [st.onMac ? '⌘z/⌥z in it' : 'alt+z in it', 'zoom terminal'], ['L', 'show/hide log']] : [['ctrl+j/⌘j', 'show/hide log']])];
  const target = [['u', 'open in BigQuery'], ['y', 'copy `table` name'], ['Y', 'copy file path (again: absolute)']];
  const log = [['g', 'top'], ['G', 'bottom'], ['u', 'open run in browser'], ['y', 'copy the run\'s command for a terminal']];
  if (c.history || c.terminal) log.push([(st.onMac ? '⌥⌘→/t' : 'ctrl+alt+→/t'), 'next tab: ' + [...(c.history ? ['log', 'jobs', 'history'] : ['log']), ...(c.terminal ? ['terminal'] : [])].join(', ') + (st.onMac ? ' (⌥⌘← back; from the terminal too)' : ' (ctrl+alt+← back; from the terminal too)')]);
  if (c.history) log.push(['enter', 'open BigQuery job'], ['/', 'search the jobs (esc clears)'], ['S', 'sort the jobs by the next column'], ['X', 'clear history']);
  const right = [
    ['move', [['j/↓', 'down'], ['k/↑', 'up'], ...(st.ctrlNMoves ? [['ctrl+n/p', 'down/up, in boxes too']] : []), ['g', 'top'], ['G', 'bottom'], ['ctrl+d', 'page down'], ['ctrl+u', 'page up']]],
    ['actions', [['enter', 'show its sql'], ...target]],
    ['sql, columns, lineage, errors', [['tab', 'next tab (shift+tab back)'], ['c', 'sql, columns, lineage in turn, and go there (from any pane but source and tree)'], ['O', 'the lineage tab, and again the sql']]],
    ['target', [['enter', 'in a field: save it and reopen the project'], ['esc', 'in a field: put it back'], ['▾', 'fold the section']]],
    ['lineage', [['double-click', 'jump'], ['backspace', 'back'], ...target]],
    ['log', log],
  ];
  if (c.changed) right.push(['R run changed', [['space', 'check/uncheck file'], ['x', 'check/uncheck all files'], ['v', 'show/hide the command it runs'], ['b', 'compare with another branch']]]);
  if (c.run) right.push(['T run tags', [['space', 'check/uncheck tag'], ['enter', 'run checked tags'], ['esc', 'd D f a options (again: close)']]]);
  if (c.branches) right.push(['B switch branch', [['enter', 'switch (git switch), or make the branch typed (git switch -c)'], ['shift+enter', 'open the branch beside this one, in a git worktree (a new one asks its name, xf_ first)'], ['ctrl+r', 'fetch origin'], ['esc', 'close']]]);
  if (c.branches && c.changed) right.push(['b compare with (from R, I, C)', [['enter', 'compare with, saved for the project'], ['ctrl+r', 'fetch origin'], ['esc', 'back']]]);
  if (c.impact || c.cost) right.push([[c.impact && 'I', c.cost && 'C'].filter(Boolean).join('/') + ' checks page', [['y', 'copy report as Markdown'], ['c', 'check again'],
    ...(c.impact && c.cost ? [['tab', 'Impact/Cost'], ['I/C', 'that tab']] : []), ...(c.api ? [['a', 'working tree/pushed branch']] : []), ...(c.branches ? [['b', 'compare with another branch']] : []), ['j/k', 'scroll'], ['esc', 'back (asks: y goes)']]]);
  right.push([', settings', [['o/s', 'Overview/Settings'], ['j/k', 'move'], ['enter', 'edit, or next choice'], ['h/l', 'choice'], ['x', 'take a key out of the file'], ['c', 'changed only / all'], ['tab', 'Global/Project'], ['/', 'filter'], ['r', 'read the Overview again'], ['R', 'reopen the project'], ['esc', 'back (asks: y goes)']]]);
  right.push(['1 source', !vimOn() ? plainSourceHelp() : [['enter', 'edit (vim keys)'], ['/', 'search (vim)'], ['ctrl+shift+o', 'symbols'], ['ctrl+w/:q', 'leave the editor'], [':w/⌘s', 'save'], [':e!', 'discard changes'], [':format/:fmt' + (vimKey('vimFormat') ? ' ' + vimKey('vimFormat') : ''), 'format the SQL (sqlfluff) and the config block (unsaved; u undoes)'], [vimPair('Hunk'), 'next/prev git change'], [vimKey('vimUndoHunk'), 'undo the git change under the cursor'], [vimKey('vimStageHunk'), 'stage the git change under the cursor (unstage, in a staged difference)'], [vimPair('Error'), 'next/prev error'], [vimKey('vimHover') + '/mouse', 'card of a ref, a function or an includes name (constants.X); ' + vimKey('vimHover') + "'s card has the keys: f filters its columns (esc or enter: back to the card), u opens its table in BigQuery, y copies its name, j/k scroll, esc gives the keys back"], [vimKey('vimDefinition'), 'go to definition'], ['gf/gF', 'go to the file named under the cursor (gF: at the line after it)'], ['ctrl+x ctrl+f', 'complete a file name (insert mode)'], ['gc{motion}/gcc/' + (st.onMac ? '⌘/' : 'ctrl+/'), 'comment out or back in (the motion\'s lines, the line, the selection; also the Scratch editor)'], [vimKey('vimMenu') + '/right-click', 'context menu (shift+right-click: the browser\'s)'], ['ctrl+o/ctrl+i', 'jump back/forward'], ['ctrl+^', 'the file before'], [vimKey('vimBufferNext') + '/' + vimKey('vimBufferPrev') + ' :bn/:bp', 'next/prev buffer (files opened)'], [vimKey('vimBuffers') + ' :ls', 'list buffers'], [vimKey('vimBufferDelete') + ' :bd', 'remove the buffer shown'], [':b N/:bd N', 'go to / remove a buffer'], [vimKey('vimFiles'), 'find a file (as ' + st.paletteKey + ')'], ['backspace', 'back from gd (outside the editor)']]]);
  if (c.inspector) right.push([st.commandsKey + ' Dependency inspector (a page; the mouse does the rest)', [['tab', 'rows / graph'], ['enter', 'in a filter box: dry run it (the global one: every ticked row); shift+enter: a new line'], [st.onMac ? '⌥⌘←/→' : 'ctrl+alt+←/→', 'the result before / next (rows that have run)'], ['z', 'zoom the section last clicked (rows or graph, result, schema); again to unzoom'], ['ctrl+c', 'cancel the queued and running queries'], ['esc', 'unzoom, or close what is open on the page; in the search box, clear it'], ['ctrl+q', 'back']]]);
  right.push(['markdown files (pane 2: preview)', [['j/k, ctrl+d/u, g/G', 'scroll the preview'], ['click a link', 'a heading scrolls there, a project file opens in the source pane, a web page in the browser']]]);
  right.push(['python files (pyrefly)', [[keyOr(vimKey('vimHover') + '/mouse', 'mouse'), "pyrefly's hover (its links go there)"], [keyOr(vimKey('vimDefinition'), 'right-click'), 'go to definition (a library file opens read-only)'], ['ctrl+space', 'completions'], ['( or ,', 'the call\'s signature'], [keyOr(vimPair('Error'), 'right-click'), 'next/prev error'], [st.commandsKey + ' Python: select interpreter', 'or click py: in the status line'], [st.commandsKey + ' Python: create pyrefly config', 'write pyrefly.toml']]]);
  right.push(['0 file tree', [['j/k', 'move'], ['l/enter', 'open file or folder'], ['h', 'close folder / to its folder'], ['a', 'new file (end with / for a folder)'], ['r', 'rename or move'], ['m', 'move (the folder selected)'], ['d', 'delete (to the trash)'], ['y', 'copy absolute path'], ['Y', 'copy relative path'], ['/', 'filter'], ['W', 'close all folders'], ['R', 'list again']]]);
  right.push(['0 source control', [['j/k', 'move'], ['enter', 'difference / open or close'], ['space', 'stage, unstage, mark resolved; on a stash: apply'], ['a', 'stage all'], ['x', 'discard (a copy to the trash)'],
    ['c', 'commit box (' + scm.commitKey + ' commits, esc esc back)'], ['A', 'amend HEAD'], ['P/p', 'push / pull (fast-forward only); on a stash p pops'], ['f', 'fetch origin'], ['s', 'stash every change'], ['d', 'drop the stash'],
    ['n', 'new branch (at the commit under the cursor)'], ['y', 'copy the commit\'s SHA'], ['o', 'open the file'], ['/', 'filter'], ['R', 'read the status again']]]);
  right.push(['0 source control: merge request (GitLab, GitHub)', [['tab', 'to the merge request below and back'], ['o', 'open the merge request (on a job: the job)'], ['O', 'open the last pipeline'], ['m', 'create a merge request (asks to push first)'],
    ['enter', 'open the row: the merge request, pipeline, job; a stage, or a trigger job\'s pipeline, opens or closes'], ['click the status line\'s !42', 'show this section'], [st.commandsKey + ' Git: refresh MR', 'ask the host again']]]);
  right.push(['0 search', [['alt+c/w/r', 'match case / whole word / regex (in the box)'], ['enter/↓', 'to the matches (in the box)'], ['esc', 'back to the source (in the box)'], ['j/k', 'move, showing the match'], ['enter', 'go to the match'], ['h/l', 'close/open a file\'s matches'],
    ['x', 'dismiss a match or file'], ['r', 'replace the match or the file\'s (with the replace field shown)'], [(st.onMac ? '⌘⌥↵' : 'ctrl+alt+↵'), 'replace all'], ['/ or esc', 'back to the box'], ['R', 'search again']]]);
  right.push(['difference (source pane)', [['t', 'side by side / inline (outside the editor)'], ['esc', 'end it'], [keyOr(vimKey('vimStageHunk'), 'right-click'), 'stage or unstage the change under the cursor']]]);
  right.push(['columns', [['/', 'filter by name (tab: by type)'], ['y', 'copy column name'], ['Y', 'copy name and type']]]);
  right.push(['sql (vim keys)', [['h/l', 'left/right'], ['w/W', 'next word/WORD'], ['b/B', 'prev word/WORD'], ['0/^', 'line start'], ['$', 'line end'],
    ['ctrl+e/y', 'scroll line'], ['v', 'select'], ['V', 'select lines'], ['y', 'copy selection'], ['yy/Y', 'copy line'], ['o', 'other end'],
    ['/', 'search'], ['n/N', 'next/prev match'], ['*/#', 'search word under cursor']]]);
  return [[['global', global], ['run', run], ['panes', panes]], right];
}

function SearchBox({ o, matches }) {
  if (!o.typing && !o.query) return null;
  return html`<div class="search-line">/<input value=${o.query} spellcheck="false" readOnly=${!o.typing}
    ref=${(e) => e && o.typing && document.activeElement !== e && e.focus()}
    onInput=${(e) => { o.query = e.currentTarget.value; o.cur = 0; const m = matches(); if (m.length) st.scrollToMatch(m[0]); redraw(); }}
    onKeyDown=${(e) => {
      if (e.key === 'Enter') { e.preventDefault(); o.typing = false; e.currentTarget.blur(); redraw(); }
      if (e.key === 'Escape') { e.preventDefault(); o.typing = false; o.query = ''; e.currentTarget.blur(); redraw(); }
    }} />
    <span class="dim">${o.query ? `${matches().length ? (o.cur + 1) + ' of ' + matches().length : 'no match'}` : ''}</span></div>`;
}

function mark(text, q, current) {
  if (!q || !text) return text;
  const i = text.toLowerCase().indexOf(q);
  if (i < 0) return text;
  return html`${text.slice(0, i)}<mark class=${current ? 'cur' : ''}>${text.slice(i, i + q.length)}</mark>${text.slice(i + q.length)}`;
}

function Help() {
  const cols = helpSections();
  const rows = [];
  cols.forEach((col) => col.forEach(([title, list]) => { if (!list.length) return; rows.push({ title }); list.forEach(([key, desc]) => rows.push({ key, desc })); }));
  st.setHelpRows(rows);
  const q = S.help.query.trim().toLowerCase();
  const matches = st.helpMatches();
  const curRow = matches[S.help.cur];
  let n = 0;
  const column = (col) => col.filter(([, list]) => list.length).map(([title, list]) => {
    const ti = n++;
    return html`<div class="help-sec">
      <div class="help-title" data-match=${ti}>${mark(title, q, curRow === ti)}</div>
      ${list.map(([key, desc]) => {
        const i = n++;
        // A tap on a row closes the keys and presses its (first) key.
        const seq = chipKeys(key)[0].seq;
        return html`<div class=${'help-row' + (seq ? ' tap' : '')} data-match=${i} onClick=${seq && (() => { st.handleKey('esc'); press(seq); })}>
          <kbd>${mark(key, q, curRow === i)}</kbd><span>${mark(desc, q, curRow === i)}</span></div>`;
      })}
    </div>`;
  });
  return html`<${Modal} title="keys" wide=${true}>
    <div class="help scroll"><div class="help-col">${column(cols[0])}</div><div class="help-col">${column(cols[1])}</div></div>
    <${SearchBox} o=${S.help} matches=${st.helpMatches} />
    <div class="overlay-keys"><${Keys} list=${[['/', 'search'], ['n/N', 'next/prev'], ['j/k', 'scroll'], ['esc', 'close']]} /></div>
  <//>`;
}

function ErrorsOverlay() {
  const e = S.errors;
  let body = html`<div class="dim pad">…</div>`;
  if (e) {
    const out = [];
    if (e.compileErr || e.errors.length) {
      out.push(html`<div class="sec-title">compile errors</div>`);
      if (e.compileErr) out.push(html`<div class="error">${e.tool} compile failed</div><pre>${e.compileErr}</pre>`);
      e.errors.forEach((x) => out.push(html`<div class="error">${x.file}</div><pre>${x.message}</pre>`));
      if (e.stale) out.push(html`<div class="dim">showing the last good compile until the project compiles cleanly</div>`);
    }
    if (e.dryRun.length || e.auth) {
      out.push(html`<div class="sec-title">dry run errors</div>`);
      if (e.auth) out.push(html`<div class="warn">${e.auth}</div>`);
      e.dryRun.forEach((x) => out.push(html`<div class="error"><button class="link error" onClick=${() => { S.overlay = null; st.selectAction(x.id); }}>${x.name}${x.label ? ' (' + x.label + ')' : ''}</button><span class="dim">${x.file ? ' · ' + x.file : ''}</span></div><pre>${x.msg}</pre>`));
    }
    if (!out.length) out.push(html`<div class="dim">no errors</div>`);
    body = out;
  }
  return html`<${Modal} title="errors" wide=${true} cls=${S.errorsZoomed ? 'zoomed' : ''}>
    <div class="scroll errors">${body}</div>
    <div class="overlay-keys"><${Keys} list=${[['esc', 'close'], ['y', 'copy'], [st.saveKey, 'compile'], ['z', S.errorsZoomed ? 'unzoom' : 'zoom']]} /></div>
  <//>`;
}

function ChangedOverlay() {
  useTick(S.changed && S.changed.loading, 100);
  const c = S.changed;
  const res = c.res;
  const listed = st.changedListed();
  const header = [];
  const comparing = html`<div><span class="warn">${spinner()} comparing ${S.flags.api ? 'the pushed branch' : 'the working tree'} with ${S.boot.defaultBranch}…</span></div>`;
  if (c.loading && res) header.push(comparing, html`<div class="dim">${res.summary || res.noChanges}</div>`);
  else if (c.loading) {
    header.push(comparing, html`<div class="dim">${S.boot.backend === 'dbt'
      ? 'The first time today, this parses the merge-base, then dbt lists what state:modified selects.'
      : 'The first time today, this compiles the merge-base, which takes about as long as a compile.'}</div>`);
  } else if (c.err) {
    header.push(html`<pre class="error">${c.err.message}</pre>`);
    if (c.err.kind === 'stale') header.push(html`<div class="dim">e shows the compile errors</div>`);
    else if (c.err.kind === 'compile') header.push(html`<div class="dim">${st.saveKey} tries again</div>`);
  } else if (res && !res.count) header.push(html`<div>${res.noChanges}</div>`);
  else if (res) {
    header.push(html`<div><b>${res.count} changed action${res.count === 1 ? '' : 's'}</b> <${BaseTitle} title=${res.title} /></div>`, html`<div>${res.summary}</div>`);
    if (c.preview && c.preview.checked < c.preview.files) header.push(html`<div>${c.preview.checked} of ${c.preview.files} files checked</div>`);
  }
  const rows = res && !c.err ? res.rows : [];
  const cursorRows = st.changedCursorRows();
  const curRow = cursorRows[c.cursor];
  const pv = c.preview;
  const run = listed && pv && pv.run;
  return html`<${Modal} title="R run changed" wide=${true} api=${S.flags.api}>
    <div class="changed-head">${header}</div>
    <div class=${'scroll changed-list' + (c.loading ? ' dimmed' : '')}>
      ${rows.map((r) => {
        const skipped = !r.removed && c.skip[r.file];
        const isCur = listed && r === curRow;
        const click = () => { if (!listed || r.removed) return; c.cursor = cursorRows.indexOf(r); redraw(); };
        if (r.kind === 'removed') return html`<div class="dim ch-removed">${r.label}</div>`;
        if (r.kind === 'file') {
          return html`<div class=${'ch-file' + (isCur ? ' sel' : '') + (skipped || r.removed ? ' dim' : '')} onMouseDown=${click}
              ref=${(e) => { if (e && isCur) scrollNearest(e); }}>
            ${!r.removed && html`<button class="box" onMouseDown=${(e) => { e.stopPropagation(); c.skip[r.file] = !c.skip[r.file]; st.changedPreview(); redraw(); }}>${skipped ? '[ ]' : '[x]'}</button>`}
            <span class="dim">${r.file}</span>${r.from && html`<span class="dim">  ${r.from}</span>`}</div>`;
        }
        if (r.kind === 'asserts') {
          return html`<div class=${'ch-asserts dim' + (isCur ? ' sel' : '')} onMouseDown=${click}><span>${r.label}</span><span>${r.tail}</span></div>`;
        }
        return html`<div class=${'ch-action' + (isCur ? ' sel' : '') + (skipped || r.removed || r.mark !== '●' ? ' dim' : '')} onMouseDown=${click}
            ref=${(e) => { if (e && isCur) scrollNearest(e); }}>
          <span class=${r.mark === '●' && !skipped ? 'warn' : 'dim'}>${r.mark}</span>
          <span class="ch-name">${r.label}</span><span class="dim">${r.tail}</span></div>`;
      })}
    </div>
    <div class="changed-foot">
      <div><${RunToggles} /></div>
      <${GitLine} verb="run" />
      ${S.flags.full && run && html`<div class="warn">⚠ full refresh: incremental tables lose their rows and are rebuilt</div>`}
      ${pv && pv.stillNote && html`<div class="warn">${pv.stillNote}</div>`}
      ${listed && pv && !pv.run && html`<div class="warn">nothing checked · space includes a file, x all</div>`}
      ${run && pv.command && S.changedCommand && html`<div class="dim command changed-command">${pv.command}</div>`}
      <div class="overlay-keys">
        ${run && html`<button class=${'run-btn' + (S.flags.full ? ' danger' : '')} onClick=${() => { st.handleKey('r'); redraw(); }}><kbd>r/enter</kbd> run${S.flags.full ? ' full refresh of' : ''}${S.flags.api ? ' in GCP' : ''} ${pv.label}</button>`}
        <${Keys} list=${[['esc', 'close'], ...(listed ? [['space', 'toggle file'], ['x', 'all'], ['j/k', 'move']] : []),
          ...(S.caps.branches && !c.loading ? [['b', 'compare with']] : []),
          ...(run && pv.command ? [['v', S.changedCommand ? 'hide command' : 'show command']] : [])]} />
      </div>
    </div>
  <//>`;
}

function BranchesOverlay() {
  const b = S.branches;
  const make = st.newBranchName(b);
  // The projects tab's "New worktree…" lists the branches to open in a
  // worktree, where enter does what shift+enter does in B (ADR 0032).
  const wt = !!b.worktree;
  const title = wt ? 'new worktree of ' + b.projectName : 'B switch branch';
  if (b.naming) return html`<${WorktreeName} b=${b} title=${title} />`;
  const enterWord = b.cursor === b.list.length && make ? (wt ? 'make branch in a worktree' : 'make branch')
    : wt ? (b.list[b.cursor] && b.list[b.cursor].worktree ? 'open its worktree' : 'open in a worktree') : 'switch';
  return html`<${Modal} title=${title} onClose=${() => { S.overlay = null; S.branches = null; }}>
    <div class="query">
      <span class="prompt">></span>
      <input value=${b.query} placeholder=${wt ? 'find a branch to open in a worktree, or name a new one' : 'find a branch, or name a new one'} spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
        ref=${(e) => e && document.activeElement !== e && e.focus()}
        onInput=${(e) => st.setBranchesQuery(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = st.keyName(e);
          if (k === 'esc') { e.preventDefault(); S.overlay = null; S.branches = null; redraw(); }
          else if (k === 'enter' && e.shiftKey) { e.preventDefault(); st.openWorktree(); }
          else if (k === 'enter') { e.preventDefault(); st.switchBranch(); }
          else if (k === 'ctrl+r') { e.preventDefault(); st.fetchBranches(); }
          else if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); st.moveBranches(-1); redraw(); }
          else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); st.moveBranches(1); redraw(); }
        }} />
      <span class="dim count">${b.list.length}/${b.total}</span>
    </div>
    ${b.note && !wt && html`<div class="warn pad branch-note">${b.note}</div>`}
    ${b.detached && html`<div class="dim pad branch-note">HEAD is detached at ${b.detached}</div>`}
    <div class="scroll branch-list">
      ${b.loading && html`<div class="dim pad">${spinner()} listing branches…</div>`}
      ${b.list.map((r, i) => html`<div class=${'branch-row' + (i === b.cursor ? ' sel' : '')}
          ref=${(e) => { if (e && i === b.cursor) scrollNearest(e); }}
          onMouseMove=${() => { if (b.cursor !== i) { b.cursor = i; redraw(); } }}
          onMouseDown=${(e) => { e.preventDefault(); if (e.shiftKey) st.openWorktree(i); else st.switchBranch(i); }}>
        <span class="lead">${i === b.cursor ? '▸' : ''}</span>
        <span class=${'branch-name' + (r.current ? ' ok' : '')}>${r.name}</span>
        <span class="dim">${[r.current && 'current', r.remote && 'remote', r.worktree && 'in ' + r.worktree, r.age].filter(Boolean).join(' · ')}</span></div>`)}
      ${make && html`<div class=${'branch-row branch-new' + (b.cursor === b.list.length ? ' sel' : '')}
          ref=${(e) => { if (e && b.cursor === b.list.length) scrollNearest(e); }}
          onMouseMove=${() => { if (b.cursor !== b.list.length) { b.cursor = b.list.length; redraw(); } }}
          onMouseDown=${(e) => { e.preventDefault(); st.switchBranch(b.list.length); }}>
        <span class="lead">${b.cursor === b.list.length ? '▸' : ''}</span>
        <span class="branch-name">+ make branch <b>${make}</b>${wt ? ' in a new worktree' : ''}</span>
        <span class="dim">${wt ? 'from its HEAD' : 'git switch -c, from HEAD'}</span></div>`}
      ${!b.loading && !b.list.length && !make && html`<div class="dim pad">no matches</div>`}
    </div>
    <div class="changed-foot">
      ${b.busy ? html`<div class="warn">${spinner()} ${b.busy}…</div>`
        : b.err ? html`<pre class="error branch-err">${b.err}</pre>`
        : html`<div class="dim">remote branches as of the last fetch · type a new name to make a branch${wt ? ' in a new worktree' : ''}</div>`}
      <div class="overlay-keys"><${Keys} list=${[['enter', enterWord],
        ...(!wt && b.list[b.cursor] ? [['shift+enter', b.list[b.cursor].worktree ? 'open its worktree' : 'open in a worktree']] : []),
        ['ctrl+r', 'fetch origin'], ['↑/↓', 'move'], ['esc', 'close']]} /></div>
    </div>
  <//>`;
}

// WorktreeName is the step before a new worktree is made: its name,
// the branch's to start with, selected so typing replaces it. The folder
// is xf_ and the name, which the line under it shows as it is typed.
function WorktreeName({ b, title }) {
  const n = b.naming;
  const folder = st.worktreeFolder(n.name.trim() || n.placeholder);
  return html`<${Modal} title=${title} onClose=${() => { S.overlay = null; S.branches = null; }}>
    <div class="query">
      <span class="prompt">name</span>
      <input class="worktree-name" value=${n.name} placeholder=${n.placeholder} spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
        ref=${(e) => { if (e && document.activeElement !== e) { e.focus(); e.select(); } }}
        onInput=${(e) => st.setWorktreeName(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = st.keyName(e);
          if (k === 'esc') { e.preventDefault(); st.cancelWorktreeName(); }
          else if (k === 'enter') { e.preventDefault(); st.nameWorktree(); }
        }} />
    </div>
    <div class="pad worktree-folder">
      <div><span class="dim">folder</span> .xf/worktrees/<b>${folder}</b></div>
      <div><span class="dim">branch</span> ${n.branch}${n.made ? html` <span class="dim">· new, from HEAD</span>` : ''}</div>
    </div>
    <div class="changed-foot">
      ${b.busy ? html`<div class="warn">${spinner()} ${b.busy}…</div>`
        : b.err ? html`<pre class="error branch-err">${b.err}</pre>`
        : html`<div class="dim">name the worktree, or keep the branch's name · xf_ marks the worktrees xf makes</div>`}
      <div class="overlay-keys"><${Keys} list=${[['enter', 'make the worktree'], ['esc', 'back to the branches']]} /></div>
    </div>
  <//>`;
}

// BaseOverlay is b's picker: the branch R, I and C compare with.
function BaseOverlay() {
  const b = S.base;
  return html`<${Modal} title="b compare with" onClose=${() => { st.closeBase(); redraw(); }}>
    <div class="query">
      <span class="prompt">></span>
      <input value=${b.query} placeholder="find branch" spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
        ref=${(e) => e && document.activeElement !== e && e.focus()}
        onInput=${(e) => st.setBaseQuery(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = st.keyName(e);
          if (k === 'esc') { e.preventDefault(); st.closeBase(); redraw(); }
          else if (k === 'enter') { e.preventDefault(); st.pickBase(); }
          else if (k === 'ctrl+r') { e.preventDefault(); st.fetchBases(); }
          else if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); st.moveBase(-1); redraw(); }
          else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); st.moveBase(1); redraw(); }
        }} />
      <span class="dim count">${b.list.length}/${b.total}</span>
    </div>
    <div class="dim pad branch-note">R, I and C compare with this branch: origin's when it has one, as of the last fetch</div>
    <div class="scroll branch-list">
      ${b.loading && html`<div class="dim pad">${spinner()} listing branches…</div>`}
      ${b.list.map((r, i) => html`<div class=${'branch-row' + (i === b.cursor ? ' sel' : '')}
          ref=${(e) => { if (e && i === b.cursor) scrollNearest(e); }}
          onMouseMove=${() => { if (b.cursor !== i) { b.cursor = i; redraw(); } }}
          onMouseDown=${(e) => { e.preventDefault(); st.pickBase(i); }}>
        <span class="lead">${i === b.cursor ? '▸' : ''}</span>
        <span class=${'branch-name' + (r.base ? ' ok' : '')}>${r.name}</span>
        <span class="dim">${r.tags}</span></div>`)}
      ${!b.loading && !b.list.length && html`<div class="dim pad">no matches</div>`}
    </div>
    <div class="changed-foot">
      ${b.busy ? html`<div class="warn">${spinner()} ${b.busy}…</div>`
        : b.err ? html`<pre class="error branch-err">${b.err}</pre>`
        : html`<div class="dim">saved as this project's defaultBranch</div>`}
      <div class="overlay-keys"><${Keys} list=${[['enter', 'compare with'], ['ctrl+r', 'fetch origin'], ['↑/↓', 'move'], ['esc', 'back']]} /></div>
    </div>
  <//>`;
}

// BaseTitle is R's "in feat vs origin/develop @ abc1234 (working tree)",
// its "vs origin/develop" a button that opens b.
function BaseTitle({ title }) {
  const i = title.startsWith('vs ') ? 0 : title.indexOf(' vs ') + 1;
  const j = title.indexOf(' @ ', i);
  if (i < 0 || j < 0 || !S.caps.branches) return html`<span class="dim">${title}</span>`;
  return html`<span class="dim">${title.slice(0, i)}</span><button class="link base-link" title="b compares with another branch"
    onClick=${() => { st.openBase(); }}>${title.slice(i, j)}</button><span class="dim">${title.slice(j)}</span>`;
}

function TagsOverlay() {
  const t = S.tags;
  const [checked, actions] = st.tagsRun();
  return html`<${Modal} title="T run tags" api=${S.flags.api}>
    <div class="query">
      <span class="prompt">></span>
      <input value=${t.query} placeholder="find tag" spellcheck="false" readOnly=${t.options}
        ref=${(e) => e && !t.options && document.activeElement !== e && e.focus()}
        onInput=${(e) => st.setTagsQuery(e.currentTarget.value)}
        onKeyDown=${(e) => {
          const k = st.keyName(e);
          if (t.options) return;
          if (k === 'esc') { e.preventDefault(); t.options = true; e.currentTarget.blur(); redraw(); }
          else if (k === 'enter') { e.preventDefault(); st.runTags(); redraw(); }
          else if (k === 'space') { e.preventDefault(); st.toggleTag(); redraw(); }
          else if (k === 'up' || k === 'ctrl+p') { e.preventDefault(); t.cursor = Math.max(t.cursor - 1, 0); redraw(); }
          else if (k === 'down' || k === 'ctrl+n') { e.preventDefault(); t.cursor = Math.min(t.cursor + 1, Math.max(t.list.length - 1, 0)); redraw(); }
        }} />
      <span class="dim count">${t.list.length}/${t.total}</span>
    </div>
    <div class="scroll tag-list">
      ${t.list.map((tag, i) => html`<div class=${'tag-row' + (i === t.cursor ? ' sel' : '')}
          ref=${(e) => { if (e && i === t.cursor) scrollNearest(e); }}
          onMouseDown=${(e) => { e.preventDefault(); t.cursor = i; st.toggleTag(); redraw(); }}>
        <span class=${t.checked[tag.name] ? 'ok' : 'dim'}>${t.checked[tag.name] ? '[x]' : '[ ]'}</span>
        <span class="tag-name">${tag.name}</span>
        <span class="dim">${tag.actions.length} action${tag.actions.length === 1 ? '' : 's'}</span></div>`)}
      ${!t.list.length && html`<div class="dim pad">no matches</div>`}
    </div>
    <div class="changed-foot">
      <div>${checked.length ? html`run <b>${checked.join(', ')}</b> <span class="dim">· ${actions} action${actions === 1 ? '' : 's'}</span>` : html`<span class="dim">nothing checked · space checks a tag</span>`}</div>
      <div class=${t.options ? '' : 'faded'}><${RunToggles} /></div>
      <${GitLine} verb="run" />
      ${S.flags.api && html`<div class="dim">the counts are your working tree's</div>`}
      <div class="overlay-keys"><${Keys} list=${t.options
        ? [['d D f a', 'options'], ['space', 'check'], ['enter', 'run'], ['/', 'search'], ['esc', 'close']]
        : [['space', 'check'], ['enter', 'run'], ['↑/↓', 'move'], ['esc', 'run options']]} /></div>
    </div>
  <//>`;
}

function CheckHeader({ which, o, verb }) {
  useTick(o.loading, 100);
  const out = [];
  const what = o.api ? 'the pushed branch' : 'the working tree';
  if (o.loading) {
    const partial = which === 'cost' && o.report && o.report.basePending;
    out.push(html`<div><span class="warn">${spinner()} ${partial ? 'estimating the same actions at the merge-base…' : verb(what)}</span>  <span class="dim">${o.progress || 'Working out the changed actions'}</span></div>`);
    if (!o.report) {
      out.push(html`<div class="dim">${which === 'impact'
        ? 'Dry runs every changed table, view and incremental table, compares it with the table in BigQuery, then asks Dataplex who reads the columns it drops or retypes. The first time today, this also compiles the merge-base.'
        : 'Dry runs every changed action with a query, as it is on the branch and as it was at the merge-base. Dry runs are free and run nothing. The first time today, this also compiles the merge-base.'}</div>`);
    }
  } else if (o.err) {
    out.push(html`<pre class="error">${o.err.message}</pre><div class="dim">${o.err.kind === 'stale' ? 'e shows the compile errors' : 'c tries again'}</div>`);
  } else if (o.report) {
    if (st.checkStale(o)) out.push(html`<div class="warn">⚠ the branch has changed since this was checked at ${o.report.checkedAt} · c checks again</div>`);
    else out.push(html`<div class="dim">checked at ${o.report.checkedAt}${o.report.headLabel ? ' · ' + o.report.headLabel : ''}</div>`);
  }
  return out;
}

const raw = (s) => html`<span dangerouslySetInnerHTML=${{ __html: s || '' }}></span>`;

// The Checks page: I and C fill the window, as settings does, with a tab
// each (ADR 0010). The tables take its width, the words keep to a line
// you can read.
function ChecksPage() {
  useEffect(() => { document.title = 'xf · checks'; return () => { document.title = 'xf · ' + S.boot.name; }; }, []);
  const which = S.checks;
  const o = S[which];
  const tabs = st.checksTabs();
  const key = (k) => () => { st.checksKey(k); redraw(); };
  const tab = (w, label, k) => html`<button class=${'tab' + (which === w ? ' on' : '')} title=${k + ' · tab goes round'}
    onClick=${key(k)}><kbd>${k}</kbd> ${label}</button>`;
  const against = (api, label) => html`<button class=${'tab' + (o.api === api ? ' on' : '')} title="a switches"
    onClick=${o.api === api ? undefined : key('a')}>${label}</button>`;
  return html`<div class=${'app checks-page' + (o.api ? ' api' : '')}>
    <header class="set-top">
      <h1><span class="badge">XF</span> Checks${o.api && html`<span class="gcp"> · GCP</span>`}</h1>
      <span class="dim">${S.boot.name}</span>
      ${tabs.length > 1 && html`<span class="set-tabs" role="group" aria-label="Check">${tabs.map((w) => w === 'impact' ? tab(w, 'Column impact', 'I') : tab(w, 'Query cost', 'C'))}</span>`}
      <span class="spacer"></span>
      ${S.caps.branches && html`<button class="link base-link" title="b compares with another branch" onClick=${key('b')}><kbd>b</kbd> vs ${S.boot.defaultBranch}</button>`}
      ${S.caps.api && html`<span class="set-tabs" role="group" aria-label="Checked from"><span class="tab-key"><kbd>a</kbd></span>${against(false, 'Working tree')}${against(true, 'Pushed branch')}</span>`}
      <button class="run-btn" disabled=${!o.report || o.loading} onClick=${key('y')}><kbd>y</kbd> Copy Markdown</button>
      <button class="run-btn" disabled=${o.loading} onClick=${key('c')}><kbd>c</kbd> Check again</button>
      <button class="link set-close" title="Close (ctrl+q)" onClick=${() => { st.closeChecks(); redraw(); }}>× close</button>
    </header>
    <div class="checks-main">
      <div class="changed-head"><${CheckHeader} which=${which} o=${o} verb=${which === 'impact'
        ? (w) => `checking the column impact of ${w} against ${S.boot.defaultBranch}…`
        : (w) => `estimating what ${w} changes against ${S.boot.defaultBranch} would scan…`} /></div>
      ${which === 'impact' ? html`<${ImpactReport} o=${o} />` : html`<${CostReport} o=${o} />`}
      <${GitLine} verb="check" />
    </div>
    <div class="footer set-footer"><${Keys} list=${[['j/k', 'scroll'], ...(tabs.length > 1 ? [['tab', 'Impact/Cost']] : []), ['ctrl+q', 'back']]} />
      <span class="spacer"></span>${S.flash && html`<span class="warn">${S.flash}</span>`}</div>
    ${S.overlay === 'prod' && S.prod && html`<${ProdPrompt} />`}
    ${S.overlay === 'base' && S.base && html`<${BaseOverlay} />`}
  </div>`;
}

// A table scrolls sideways in its own box, so the page never does.
const wrapTable = (t) => html`<div class="table-wrap">${t}</div>`;

function ImpactReport({ o }) {
  const r = o.report;
  const pg = r && r.page;
  if (!pg) return null;
  return html`<div class=${'report' + (o.loading ? ' dimmed' : '')}>
      <h2 class="report-title">${raw(pg.Title)}</h2>
      <p class=${'verdict v-' + pg.Verdict}><b>${raw(pg.Summary)}</b> <span class="dim">${raw(pg.Context)}</span></p>
      ${r.unset && html`<p class="warn">${r.unset}</p>`}
      ${r.empty ? html`<p class="dim">No table, view or incremental table changed.</p>` : wrapTable(html`<table class="grid ledger">
        <thead><tr><th>#</th><th></th><th>Model</th><th>What changed</th><th>Who is affected</th></tr></thead>
        <tbody>${pg.Rows.map((row) => html`<tr>
          <td class="dim">${row.Number}</td><td><span class=${'mark m-' + row.Class}>${row.Label}</span></td>
          <td>${raw(row.Model)}</td><td>${raw(row.Changed)}</td>
          <td>${(row.Affected || []).map((a) => html`<div class=${(a.ToFix ? 'tofix' : '') + (a.None ? ' dim' : '')}>${raw(a.HTML)}</div>`)}</td></tr>`)}</tbody>
      </table>`)}
      ${pg.ReadersTotal > 0 && html`<h3>Every reader <span class="dim">(${pg.ReadersTotal})</span></h3>
        ${wrapTable(html`<table class="grid"><thead><tr><th>Reader</th><th>Reads</th><th>Of</th><th>On this branch</th></tr></thead>
        <tbody>${(pg.Readers || []).map((e) => html`<tr><td>${raw(e.Reader)}</td><td>${raw(e.Reads)}</td><td><span class="dim">${e.Row} · </span>${raw(e.Of)}</td><td class="dim">${raw(e.Note)}</td></tr>`)}
        ${pg.MoreReaders > 0 && html`<tr><td class="dim">+${pg.MoreReaders} more</td><td></td><td></td><td></td></tr>`}</tbody></table>`)}`}
      <div class="legend">${(pg.Legend || []).map((l) => html`<span><span class=${'mark m-' + l.Class}>${l.Label}</span> ${l.Text}</span>`)}</div>
      ${(pg.Notes || []).filter((n) => !r.unset || n.replace(/<[^>]*>/g, '') !== r.unset.slice(2)).map((n) => html`<p class="dim small">${raw(n)}</p>`)}
    </div>`;
}

// costCells are a run's scans and cost: bold when the total counts them,
// else dim; a dash when there is no such run or it isn't known.
const costCells = (f, counted) => f && f.scans
  ? html`<td class=${'num' + (counted ? ' counted' : ' dim')} title=${counted ? 'in the total' : ''}>${f.scans}</td><td class=${'num' + (counted ? '' : ' dim')}>${f.cost}</td>`
  : html`<td class="num dim">–</td><td class="num dim">–</td>`;

function CostReport({ o }) {
  const r = o.report && o.report.report;
  if (!r) return null;
  return html`<div class=${'report' + (o.loading && !o.report.basePending ? ' dimmed' : '')}>
      ${r.compared && html`<div>${r.compared}</div>`}
      ${(r.dryRunIn || r.compiled) && html`<div class="dim">${[
        r.dryRunIn && html`dry run in <span class="fg">${r.dryRunIn}</span>`,
        r.compiled && `compiled with ${r.compiled}`,
      ].filter(Boolean).map((x, i) => html`${i ? ' · ' : ''}${x}`)}</div>`}
      ${r.fromDev && html`<div class="dim">${r.fromDev}</div>`}
      ${r.unset && html`<p class="warn">${r.unset}</p>`}
      <p class="total"><b>${r.total}</b>${r.later && html`<br /><span class="dim">${r.later}</span>`}</p>
      ${r.empty ? html`<p class="dim">${r.empty}</p>` : r.rows.length > 0 && wrapTable(html`<table class="grid">
        <thead><tr><th rowspan="2">Action</th><th rowspan="2">Type</th>
          <th colspan="2" class="run" title="A table's run, or an incremental table's full rebuild">Full run</th>
          <th colspan="2" class="run" title="Each run of an incremental table once it is built">Incremental run</th>
          <th rowspan="2" title=${'Against the same action at the merge-base' + (r.compared ? ', ' + r.compared.split(' vs ')[1] : '')}>Change</th><th rowspan="2">Notes</th></tr>
          <tr><th class="num">Scans</th><th class="num">Cost</th><th class="num">Scans</th><th class="num">Cost</th></tr></thead>
        <tbody>${r.rows.map((x) => html`<tr><td class="mono">${x.action}</td><td class="dim">${x.type}</td>
          ${costCells(x.full, x.counted === 'full')}${costCells(x.incremental, x.counted === 'incremental')}
          <td class="dim change">${x.change}${x.was && html`<br />was ${x.was}`}</td><td class=${x.warn ? 'warn' : 'dim'}>${x.notes}</td></tr>`)}</tbody>
      </table>`)}
      ${r.baseError && html`<p class="warn">${r.baseError}</p>`}
      ${r.notEstimated && html`<${NotEstimated} n=${r.notEstimated} />`}
      <p class="dim small">${r.footnote}</p>
    </div>`;
}

// tableName draws dataset.name with the dataset dim, so the names stand out.
const tableName = (t) => {
  const i = t.lastIndexOf('.');
  return html`<span class="mono"><span class="dim">${t.slice(0, i + 1)}</span>${t.slice(i + 1)}</span>`;
};
const chipClass = { new: 'm-new', missing: 'm-review', other: 'm-none' };

// NotEstimated is the actions C couldn't price, grouped by root cause:
// 1. Missing tables in BigQuery (not added on this branch) that need checking.
// 2. New tables added on this branch, with the actions waiting on each one.
// 3. Other reasons (external tables, scripts), grouped by why.
function NotEstimated({ n }) {
  const where = n.where || 'BigQuery';
  return html`<h3>Not estimated <span class="dim">${n.count} ${n.count === 1 ? 'action' : 'actions'}, not in the total</span></h3>
    <div class="cost-chips">${n.chips.map((c) => html`<span class=${'mark ' + chipClass[c.kind]}>${c.text}</span>`)}</div>
    ${n.missing && wrapTable(html`<table class="grid not-estimated missing-alert">
      <thead><tr><th>Reads a missing table</th><th>Type</th><th>Missing in <span class="as-is">${where}</span></th><th>Status</th></tr></thead>
      <tbody>${n.missing.map((x) => html`<tr>
        <td>${x.assertions
          ? html`<details><summary class="dim">${x.assertions.length} assertions</summary>${x.assertions.map((a) => html`<div>${tableName(a)}</div>`)}</details>`
          : tableName(x.action)}</td>
        <td class="dim">${x.type}</td><td>${tableName(x.reads)}</td>
        <td class="status"><span class=${'mark ' + chipClass[x.kind]}>${x.status}</span>
          <span class="dim small">${x.note}</span></td></tr>`)}</tbody>
    </table>`)}
    ${n.waiting && wrapTable(html`<table class="grid not-estimated">
      <thead><tr><th>New table on this branch (not built in <span class="as-is">${where}</span> yet)</th><th>Blocks (${n.waiting.reduce((s, g) => s + g.count, 0)})</th></tr></thead>
      <tbody>${n.waiting.map((g) => html`<tr>
        <td>${tableName(g.reads)} <span class="mark m-new" title="A dry run needs every table it reads to exist. A table this branch adds exists only once a run builds it.">not built yet</span></td>
        <td><div class="cost-deps">${(g.actions || []).map((a) => html`<span class="cost-dep">${tableName(a.action)} <span class="dim small">${a.type}</span></span>`)}${g.assertions && g.assertions.length > 0 && html`<details class="cost-asserts"><summary class="dim">${g.assertions.length} ${g.assertions.length === 1 ? 'assertion' : 'assertions'}</summary><div class="cost-assert-list">${g.assertions.map((a) => html`<div>${tableName(a)}</div>`)}</div></details>`}</div></td></tr>`)}</tbody>
    </table>`)}
    ${n.other && wrapTable(html`<table class="grid not-estimated">
      <thead><tr><th>Why BigQuery gave no byte count</th><th>Actions (${n.other.reduce((s, g) => s + g.actions.length, 0)})</th></tr></thead>
      <tbody>${n.other.map((g) => html`<tr>
        <td class="dim">${g.why}</td>
        <td><div class="cost-deps">${g.actions.map((x) => html`<span class="cost-dep">${tableName(x.action)} <span class="dim small">${x.type}</span>${x.new && html` <span class="mark m-new">new</span>`}</span>`)}</div></td></tr>`)}</tbody>
    </table>`)}`;
}

function ProdPrompt() {
  const p = S.prod;
  useTick(p.saving, 100);
  const key = (e) => {
    e.stopPropagation(); // the prompt has the keys
    if (e.key === 'Escape') { e.preventDefault(); st.answerProd(true); }
  };
  const errLines = p.err ? p.err.trim().split('\n').slice(0, 2).join('\n') : '';
  const empty = !prodOptionsOf(p);
  return html`<${Modal} title="compare with prod" onClose=${() => st.answerProd(true)}>
    <div class="prod" tabindex="-1" onKeyDown=${key}>
      <p class="dim">The checks compare the branch with the tables this project's prod runs build. Left empty, they use its default targets: if those are dev tables, a run of the branch there hides what it drops.</p>
      <p>Where this project's prod runs build:</p>
      <${ProdForm} holder=${p} options="" disabled=${p.saving} focus=${true}
        onInput=${() => { p.err = ''; }} onDone=${(enter) => { if (enter && !p.saving) st.answerProd(false); }} />
      ${p.saving && html`<p class="warn">${spinner()} ${empty ? 'saving…' : 'compiling with these options…'}</p>`}
      ${p.err && html`<pre class="error">${errLines}</pre><p class="dim">More flags are flags of dataform compile, such as --vars=env=prod.</p>`}
      <div class="buttons">
        <button class="run-btn" disabled=${p.saving} onClick=${() => st.answerProd(false)}><kbd>enter</kbd> save to .xf/config.json</button>
        <button class="plain" onClick=${() => st.answerProd(true)}><kbd>esc</kbd> not now</button>
      </div>
    </div>
  <//>`;
}

// WatchPrompt takes the ID (or console URL) of a workflow xf did not start,
// to follow it and show what each of its BigQuery jobs cost.
function WatchPrompt() {
  const w = S.watch;
  useTick(w.busy, 100);
  const key = (e) => {
    e.stopPropagation(); // the prompt has the keys
    if (e.key === 'Escape') { e.preventDefault(); st.closeWatch(); redraw(); return; }
    if (e.key === 'Enter') { e.preventDefault(); st.submitWatch(); }
  };
  return html`<${Modal} title="watch a workflow" onClose=${() => st.closeWatch()} api=${true}>
    <div class="prod" tabindex="-1" onKeyDown=${key}>
      <p class="dim">Paste the ID of a workflow, or the URL of its page in the Cloud console. xf adds it to the history as external and shows what each action's BigQuery job cost; if it is still running, xf follows it until it ends.</p>
      <label class="opt on">
        <span class="prompt">></span>
        <input class="text" value=${w.input} placeholder="1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" spellcheck="false" disabled=${w.busy}
          ref=${(e) => e && !w.busy && document.activeElement !== e && e.focus()}
          onInput=${(e) => { w.input = e.currentTarget.value; w.err = ''; }} /></label>
      <p class="dim small">An ID alone is looked for in this project's Dataform repository; the URL says which repository, location and project.</p>
      ${w.busy && html`<p class="warn">${spinner()} looking it up…</p>`}
      ${w.err && html`<p class="error">${w.err}</p>`}
      <div class="buttons">
        <button class="run-btn" disabled=${w.busy} onClick=${() => st.submitWatch()}><kbd>enter</kbd> watch</button>
        <button class="plain" onClick=${() => { st.closeWatch(); redraw(); }}><kbd>esc</kbd> cancel</button>
      </div>
    </div>
  <//>`;
}

// EditorPicker changes the editor o opens files with: a window editor on
// PATH, or a command of your own, saved for every project or this one.
function EditorPicker() {
  const e = S.editor;
  useTick(e.saving, 100);
  const rows = st.editorRows();
  const custom = rows[e.cursor] && rows[e.cursor].custom;
  return html`<${Modal} title="editor for o" onClose=${() => { S.overlay = null; S.editor = null; redraw(); }}>
    <div class="editor-pick">
      ${e.loading ? html`<p class="dim">…</p>` : html`
        <p class=${e.blocked ? 'warn' : ''}>${e.blocked
          ? html`o opens files with <code>${e.current}</code>, which runs in a terminal, so xf web can't open it. Pick an editor with a window:`
          : html`o opens files with <code>${e.current}</code>${e.usable ? '' : ', which xf web can\'t run'}. Pick another:`}</p>
        <p class="dim small">now from ${e.from}. When an editor runs xf lsp (the VS Code extension, neovim's plugin), o uses that one instead.</p>
        <div class="ed-list">
          ${rows.map((r, i) => html`<div class=${'ed-row' + (i === e.cursor ? ' sel' : '') + (r.found ? '' : ' missing')}
              onMouseMove=${() => { if (r.found && e.cursor !== i) { e.cursor = i; redraw(); } }}
              onClick=${() => { if (!r.found) return; e.cursor = i; if (!r.custom) st.pickEditor(); redraw(); }}>
            <span class="lead">${i === e.cursor ? '▸' : ''}</span>
            ${r.custom
              ? html`<span class="ed-name">another command</span>
                  <input class="text" value=${e.custom} placeholder="e.g. code --reuse-window" spellcheck="false"
                    ref=${(el) => el && custom && document.activeElement !== el && el.focus()}
                    onFocus=${() => { e.cursor = i; redraw(); }}
                    onInput=${(ev) => { e.custom = ev.currentTarget.value; e.err = ''; }}
                    onKeyDown=${(ev) => {
                      const k = st.keyName(ev);
                      if (k === 'enter') { ev.preventDefault(); st.pickEditor(); }
                      else if (k === 'esc') { ev.preventDefault(); S.overlay = null; S.editor = null; redraw(); }
                      else if (k === 'up' || k === 'down' || k === 'tab' || k === 'ctrl+n' || k === 'ctrl+p') {
                        ev.preventDefault();
                        st.editorKey(k === 'ctrl+n' ? 'down' : k === 'ctrl+p' ? 'up' : k);
                        ev.currentTarget.blur();
                        redraw();
                      }
                    }} />`
              : html`<span class="ed-name">${r.name}</span><code class="dim">${r.command}</code>
                  ${r.command === e.current && html`<span class="ok small">current</span>`}
                  ${!r.found && html`<span class="dim small">not on PATH</span>`}`}
          </div>`)}
        </div>
        <label class="ed-scope" onMouseDown=${(ev) => { ev.preventDefault(); e.project = !e.project; redraw(); }}>
          <span class=${e.project ? 'dim' : 'ok'}>${e.project ? '( )' : '(•)'}</span> every project <span class="dim">(your config)</span>
          <span class=${e.project ? 'ok' : 'dim'}>${e.project ? '(•)' : '( )'}</span> this project <span class="dim">(.xf/config.json, shared with the team)</span>
        </label>
        ${e.saving && html`<p class="warn">${spinner()} saving…</p>`}
        ${e.err && html`<p class="error">${e.err}</p>`}`}
      <div class="overlay-keys"><${Keys} list=${[['enter', e.open ? 'save and open the file' : 'save'], ['↑/↓', 'move'], ['tab', 'every project / this one'], ['esc', 'cancel']]} /></div>
    </div>
  <//>`;
}

function Confirm() {
  const o = S.overlay;
  if (o === 'confirmQuit') {
    const r = S.run;
    let ask = `close ${S.boot.name}?`;
    if (r && r.running && r.api) ask = `a run is in progress and carries on in GCP: close ${S.boot.name}?`;
    else if (r && r.running) ask = `a run is in progress: closing cancels it. close ${S.boot.name}?`;
    const others = alsoOpenIn(S.key);
    return html`<${Modal} title="close project" cls="confirm">
      <p class="warn">${ask}</p>
      ${others && html`<p>It is open in ${others} too: ${others.includes(',') || others.includes(' and ') ? 'they go' : 'it goes'} to the project shown before.</p>`}
      ${S.quitUnsaved.length > 0 && html`<p class="warn">${unsavedWords(S.quitUnsaved)}</p>`}
      ${S.quitTerm && html`<p class="warn">${S.quitTerm === 'a command' ? 'a command' : html`<code>${S.quitTerm}</code>`} is running in the terminal: closing stops it.</p>`}
      <p class="dim">This tab goes to the project it showed before, or the projects. xf web keeps running: ctrl+c in its terminal, or in the projects page, stops it.</p>
      <div class="buttons"><button class="run-btn" onClick=${() => st.closeProject()}><kbd>y</kbd>/<kbd>ctrl+q</kbd> close</button>
        <button class="plain" onClick=${() => { S.overlay = null; redraw(); }}><kbd>any other key</kbd> stay</button></div>
    <//>`;
  }
  // The projects tab's Remove Worktree and the palette's (ADR 0032).
  if (o === 'confirmRemoveWorktree') {
    const c = S.removeWorktree;
    if (!c) return null;
    const others = c.key && alsoOpenIn(c.key);
    const keep = () => { if (c.busy) return; S.overlay = null; S.removeWorktree = null; st.say('worktree kept'); };
    // A press keeps the focus where it was, so nothing (the source pane's
    // editor taking its keys back) moves it while the click is under way.
    const hold = (e) => e.preventDefault();
    return html`<${Modal} title="remove worktree" cls="confirm" onClose=${keep}>
      <p class="warn">remove the worktree ${c.name}?</p>
      <p>Its folder${c.folder ? html` <code>${c.folder}</code>` : ''} goes and xf forgets it; its branch stays.${c.key === S.key ? ' This tab goes to the project it showed before.' : ''}${others ? ' It is open in ' + others + ' too.' : ''}</p>
      ${c.unsaved.length > 0 && html`<p class="warn">${unsavedWords(c.unsaved)}</p>`}
      ${c.busy ? html`<p class="warn">${spinner()} removing the worktree…</p>`
        : c.err ? html`<pre class="error branch-err">${c.err}</pre>`
        : c.dirty ? html`<pre class="error branch-err">${c.dirty}</pre><p class="warn">remove it anyway, losing them?</p>`
        : html`<p class="dim">If it holds uncommitted or untracked work, xf names it and asks again.</p>`}
      <div class="buttons"><button class="run-btn danger" disabled=${!!c.busy} onMouseDown=${hold} onClick=${() => st.removeListedWorktree(c)}><kbd>y</kbd>/<kbd>enter</kbd> ${c.err ? 'try again' : c.dirty ? 'remove anyway' : 'remove'}</button>
        <button class="plain" disabled=${!!c.busy} onMouseDown=${hold} onClick=${keep}><kbd>any other key</kbd> keep it</button></div>
    <//>`;
  }
  if (o === 'confirmClear') {
    const n = S.history.rows.length;
    return html`<${Modal} title="clear history" cls="confirm">
      <p class="warn">forget the ${n} kept run${n === 1 ? '' : 's'} of this project?</p>
      <div class="buttons"><button class="run-btn" onClick=${() => { S.overlay = null; st.clearHistory(); }}><kbd>y</kbd> clear</button>
        <button class="plain" onClick=${() => { S.overlay = null; redraw(); }}><kbd>any other key</kbd> keep them</button></div>
    <//>`;
  }
  const c = S.confirm;
  const names = c.names.length > 9 ? [...c.names.slice(0, 8), `… and ${c.names.length - 8} more`] : c.names;
  const f = S.flags;
  const withWhat = f.deps && f.dependents ? 'deps and dependents' : f.deps ? 'deps' : f.dependents ? 'dependents' : '';
  return html`<${Modal} title="⚠ confirm full refresh" cls="confirm amber">
    <p class="warn"><b>Full refresh drops and rebuilds:</b></p>
    <ul class="names">${names.map((n) => html`<li>${n}</li>`)}${withWhat && html`<li class="dim">+ ${c.names.length === 1 ? 'its' : 'their'} ${withWhat}</li>`}</ul>
    <p class="dim">${S.boot.backend === 'dbt' ? 'Incremental models lose their existing rows and seeds are reloaded: both rebuilt from scratch.' : 'Incremental tables lose their existing rows and are rebuilt from scratch.'}</p>
    <div class="buttons"><button class="run-btn danger" onClick=${() => { S.overlay = null; st.launch(c.req); }}><kbd>y</kbd> run full refresh</button>
      <button class="plain" onClick=${() => { S.overlay = null; redraw(); }}><kbd>any other key</kbd> cancel</button></div>
  <//>`;
}

// DeferAsk is what a run asks before it starts with defer to prod, or
// what turning it off offers (ADR 0031).
function DeferAsk() {
  const { ask, remove } = S.deferAsk;
  const detail = ask.detail || [];
  const shown = detail.length > 12 ? [...detail.slice(0, 11), `… and ${detail.length - 11} more`] : detail;
  return html`<${Modal} title=${remove ? 'defer to prod: proxy views' : 'defer to prod'} cls="confirm amber">
    <p class="warn">${ask.title}</p>
    ${shown.length > 0 && html`<ul class="names mono">${shown.map((n) => html`<li>${n}</li>`)}</ul>`}
    ${ask.note && html`<p class="dim">${ask.note}</p>`}
    <div class="buttons">${ask.choices.map((c, i) => html`<button class=${i === 0 ? 'run-btn' : 'plain'} onClick=${() => st.answerDefer(i)}><kbd>${i + 1}</kbd> ${c.label}</button>`)}
      <button class="plain" onClick=${() => st.answerDefer(-1)}><kbd>any other key</kbd> ${remove ? 'keep them' : 'cancel'}</button></div>
  <//>`;
}

function Overlay() {
  switch (S.overlay) {
    case 'palette': return html`<${Palette} />`;
    case 'help': return html`<${Help} />`;
    case 'errors': return html`<${ErrorsOverlay} />`;
    case 'changed': return S.changed && html`<${ChangedOverlay} />`;
    case 'tags': return S.tags && html`<${TagsOverlay} />`;
    case 'branches': return S.branches && html`<${BranchesOverlay} />`;
    case 'base': return S.base && html`<${BaseOverlay} />`;
    case 'prod': return S.prod && html`<${ProdPrompt} />`;
    case 'watch': return S.watch && html`<${WatchPrompt} />`;
    case 'editor': return S.editor && html`<${EditorPicker} />`;
    case 'scratchNew': return html`<${NewBox} />`;
    case 'confirmQuit': case 'confirmClear': case 'confirmFull': case 'confirmRemoveWorktree': return html`<${Confirm} />`;
    case 'deferAsk': return S.deferAsk && html`<${DeferAsk} />`;
    case 'projects': return html`<${Picker} overlay=${true} />`;
    case 'sourceConflict': return html`<${SourceConflict} />`;
    case 'gitOutput': return html`<${GitOutput} />`;
    case 'buffers': return html`<${BuffersOverlay} />`;
    case 'python': return st.hooks.pythonOverlay ? st.hooks.pythonOverlay() : null;
  }
  return null;
}

// ---- pages ----

// sized is a pane's height as dragged, or nothing to let it size itself;
// a zoomed pane fills the body. A dragged pane still gives way when the
// window is too short for it, as the SQL keeps its minimum.
function sized(id, fill) {
  const h = st.paneSize(id);
  return h && !fill ? `flex:0 1 ${h}px;max-height:none` : '';
}

// Splitter is the handle between two stacked panes, which drags the size
// of the pane id: the one above it, or with below the one under it. A
// double-click gives the pane its own size back.
function Splitter({ id, below }) {
  return html`<div class="splitter" title="drag to resize · double-click to reset"
    onDblClick=${() => { st.setPaneSize(id, 0); redraw(); }}
    onMouseDown=${(e) => {
      e.preventDefault();
      const pane = document.querySelector(`[data-pane="${id}"]`);
      const body = document.querySelector('.body');
      if (!pane || !body) return;
      const y0 = e.clientY, h0 = pane.getBoundingClientRect().height;
      const max = body.getBoundingClientRect().height - 140;
      document.body.classList.add('resizing');
      const move = (ev) => {
        const dy = ev.clientY - y0;
        st.setPaneSize(id, Math.round(Math.min(Math.max(h0 + (below ? -dy : dy), 48), max)));
        redraw();
      };
      const up = () => {
        document.body.classList.remove('resizing');
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
    }}><span></span></div>`;
}

// VSplitter is the vertical handle between the full-height source column on
// the left and the inspector + compiled SQL column on the right.
function VSplitter() {
  return html`<div class="v-splitter" title="drag to resize · double-click to reset"
    onDblClick=${() => { st.setPaneSize('source', 0); redraw(); }}
    onMouseDown=${(e) => {
      e.preventDefault();
      const ws = document.querySelector('.workspace');
      if (!ws) return;
      const rect = ws.getBoundingClientRect();
      if (!rect.width) return;
      document.body.classList.add('v-resizing');
      const move = (ev) => {
        const pct = ((ev.clientX - rect.left) / rect.width) * 100;
        st.setPaneSize('source', Math.round(Math.min(Math.max(pct, 25), 75)));
        redraw();
      };
      const up = () => {
        document.body.classList.remove('v-resizing');
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
    }}><span></span></div>`;
}

// RightPane wraps the right-hand side (actions, lineage, run, compiled SQL)
// as pane 2 when the source pane is on the left. Its title is the number
// alone: the actions pane inside says which action and file it shows.
// PreviewPane is pane 2 beside a Markdown file: the file rendered
// (markdown.js, ADR 0027).
function PreviewPane() {
  const Body = st.hooks.markdownBody;
  return html`<${Pane} id="sql" focused=${S.focus === 'sql'} cls="sql-pane md-pane" title=${html`preview${S.zoomed ? html`<span class="dim"> · zoom (esc)</span>` : ''}`}>
    ${Body ? html`<${Body} />` : html`<div class="dim pad">…</div>`}
  <//>`;
}

// DeferSection is pane 2's list of what defer to prod reads the action's
// upstream tables from (ADR 0031), while it is on.
function DeferSection() {
  const d = S.view && S.view.defer;
  if (!d) return null;
  const rows = d.rows || [];
  let body;
  if (d.loading) body = html`<div class="dim">${spinner()} looking up which upstream tables are built in dev…</div>`;
  else if (d.error) body = html`<div class="warn" title=${d.error}>not applied: ${d.error}</div>`;
  else if (!rows.length) body = html`<div class="dim">no upstream tables to read from prod</div>`;
  else {
    // One line per label and dataset, its tables after it, so a dataset
    // read for several tables is written once.
    body = html`<div class="defer-grid">${st.deferGroups(rows).map((g) => html`
      <span class=${'defer-label defer-' + g.tone}>${g.head}</span>
      <span class="defer-ds mono" title=${g.ds}>${g.ds}</span>
      <span class="defer-tables">${g.rows.map((r) => {
        const name = r.prod || r.dev;
        const t = name.slice(name.lastIndexOf('.') + 1);
        return html`<span class="defer-table" title=${r.hover}>${r.url ? html`<a class="mono" href=${r.url} target="_blank" rel="noopener">${t}</a>` : html`<span class="mono">${t}</span>`}${r.note && html`<span class="dim"> ${r.note}</span>`}</span>`;
      })}</span>`)}</div>`;
  }
  const n = rows.filter((r) => r.tone === 'prod' || r.label.startsWith('⚠ prod')).length;
  // A click gives it the keys, so z zooms it.
  return html`<section class=${'pane defer-pane' + (S.focus === 'defer' ? ' focused' : '')} data-pane="defer"
      onMouseDown=${() => { st.setFocus('defer'); redraw(); }}>
    <header class="pane-title"><span class="pane-name">defer to prod${n ? html`<span class="dim"> · ${n} read from prod</span>` : ''}${d.stale ? html`<span class="warn"> · changed on this branch</span>` : ''}${S.zoomed && S.focus === 'defer' ? html`<span class="dim"> · zoom (esc)</span>` : ''}</span></header>
    <div class="pane-body defer-body">${body}</div>
  </section>`;
}

function RightPane({ children }) {
  const rightFocused = ['actions', 'lineage', 'run', 'defer', 'sql'].includes(S.focus);
  return html`<section class=${'pane right-pane' + (rightFocused ? ' right-focused' : '')} data-pane="right"
      onMouseDown=${(e) => { if (e.target === e.currentTarget) { st.setFocus('sql'); redraw(); } }}>
    <header class="pane-title">
      <span class="pane-num">2</span>
    </header>
    <div class="right-col">${children}</div>
  </section>`;
}

// Project lays the panes out: when the source pane is open it takes the full
// height on the left (1) while actions, lineage, run and the compiled SQL
// sit inside the right-hand pane (2); the log spans the full width across the bottom.
function Project() {
  const panes = {
    actions: html`<${ActionsPane} fill=${S.zoomed} />`, lineage: html`<${SQLPane} />`, run: html`<${RunPane} />`,
    sql: html`<${SQLPane} />`, log: html`<${LogPane} />`, source: html`<${SourcePane} />`, defer: html`<${DeferSection} />`,
  };
  const preview = st.previewShown();
  if (preview) panes.sql = html`<${PreviewPane} />`;
  const rightPane = preview ? html`<${RightPane}>${panes.sql}<//>` : html`<${RightPane}>
    ${panes.actions}
    <${Splitter} id="actions" />
    <${TargetSection} />
    ${panes.defer}
    ${S.caps.run && !(S.view && S.view.external) && panes.run}
    ${panes.sql}
  <//>`;
  let body;
  if (S.zoomed && panes[S.focus]) {
    body = html`<div class="body zoomed">${panes[S.focus]}</div>`;
  } else {
    body = html`<div class="body">
      ${!SRC.shown ? rightPane : st.rightShown() ? html`<div class="workspace">${panes.source}<${VSplitter} />${rightPane}</div>`
        : html`<div class="workspace right-hidden">${panes.source}</div>`}
      ${S.showLog && html`<${Splitter} id="log" below=${true} />`}
      ${S.showLog && panes.log}
    </div>`;
  }
  // The file tree is a column on the far left, beside everything else
  // (ADR 0006); zoomed, the pane zoomed is all there is. The row is there
  // with the tree hidden too, so cmd+b doesn't remount the panes: the
  // source pane's editor would lose the keys.
  // The rail of icons is left of it (ADR 0035), and goes with a zoom too.
  const tree = st.hooks.treeShown && st.hooks.treeShown() && !S.zoomed;
  body = html`<div class="main-row">${!S.zoomed && html`<${Rail} />`}${tree && html`<${TreePane} />`}${tree && html`<${TreeSplitter} />`}${body}</div>`;
  const compiling = S.state && S.state.compiling;
  return html`<div class="app">
    <div class=${'compile-bar' + (compiling ? ' on' : '')}></div>
    ${body}
    <${StatusLine} />
    <${Footer} />
    <${Overlay} />
  </div>`;
}

// Picker is the projects xf has opened: the page with none open, or with
// overlay the list over a project's page that opens another (ADR 0008).
function Picker({ overlay }) {
  // The columns line up: as wide as the longest name and backend.
  const nameW = Math.max(14, ...S.picker.map((p) => (p.child ? p.worktree.length + 3 : p.name.length))) + 1;
  // Each project's branch, "…" until the server has read them all.
  const pb = S.pickerBranches;
  const branchOf = (p) => (p.found ? p.branch : pb ? pb[p.root] || '' : '…');
  // A worktree's project under its main checkout's goes by its folder.
  const nameOf = (p) => (p.child ? '└ @' + p.worktree : p.name);
  const branchW = Math.min(28, Math.max(1, ...S.picker.map((p) => branchOf(p).length))) + 1;
  const cols = `grid-template-columns:2ch ${nameW}ch ${branchW}ch 9ch minmax(0,1fr) auto`;
  const words = S.pickerQuery.toLowerCase().split(/\s+/).filter(Boolean);
  // Where enter opens, and shift+enter: over a project as
  // webOpenProjectIn says, on this page here.
  const newTab = overlay && S.openIn === 'new-tab';
  const where = (n) => (n ? 'open in a new tab' : 'open here');
  const list = html`<div class="picker-list">
        <div class="query">
          <span class="prompt">></span>
          <input value=${S.pickerQuery} placeholder=${S.pickerTotal ? 'search projects by name, path or tool, or type a path: ~/…' : S.canFind ? 'type a folder to find the projects in it, or a project\'s path: ~/…' : 'type a path to a project: ~/…'}
            spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
            ref=${(e) => e && document.activeElement !== e && e.focus()}
            onInput=${(e) => st.setPickerQuery(e.currentTarget.value)}
            onBlur=${(e) => { const el = e.currentTarget; setTimeout(() => (S.mode === 'picker' || S.overlay === 'projects') && !T.pick && el.focus(), 0); }}
            onKeyDown=${(e) => {
              let k = st.keyName(e);
              if (k === 'enter' && e.shiftKey) k = 'shift+enter';
              if (k === 'Delete' && e.shiftKey) k = 'shift+Delete';
              if (k === 'ctrl+n') k = 'down';
              if (k === 'ctrl+p') k = 'up';
              if (st.pickerKey(k)) { e.preventDefault(); redraw(); }
            }} />
          ${!S.paths && html`<span class="dim count">${S.picker.length}/${S.pickerTotal}</span>`}
        </div>
        ${S.paths ? html`<${PathRows} />` : html`
        ${!S.picker.length && (S.pickerTotal || !S.canFind
          ? html`<div class="dim pad">${S.pickerTotal ? 'no project matches' + (S.pickerQuery ? ' · esc clears the search' : '') : 'no projects yet · type a path, e.g. ~/repos/…'}</div>`
          : html`<div class="find-empty"><p>No projects yet.</p><${FindButton} /><p class="dim">or type the path to a Dataform or dbt project above</p></div>`)}
        ${S.picker.map((p, i) => html`<div class=${'pick-row' + (i === S.pickerCursor ? ' sel' : '') + (afterOpen(i) ? ' after-open' : '')} style=${cols}
          ref=${(e) => { if (e && i === S.pickerCursor) scrollNearest(e); }}
          onMouseMove=${() => { if (S.pickerCursor !== i) { S.pickerCursor = i; redraw(); } }}
          onClick=${(e) => st.pickProject(i, false, e.shiftKey)}>
        <span class="lead">${i === S.pickerCursor ? '▸' : ''}</span>
        <span class=${'pick-name' + (p.child ? ' pick-child' : '') + (p.found ? ' dim' : '')} title=${p.name}>${underline(nameOf(p), words)}</span><span class="pick-meta"><span class="pick-branch" title=${branchOf(p)}>${branchOf(p)}</span><span class=${'kind-word ' + (p.backend === 'dbt' ? 'k-inc' : 'k-tbl')}>${underline(p.backend, words)}</span>
        <span class="dim pick-root" title=${p.root}>${underline(p.path, words)}</span></span><span class="dim pick-when"><${Badges} p=${p} />${p.when}</span></div>`)}`}
      </div>`;
  const f = S.find;
  // shift+del removes a worktree (ADR 0032).
  const cur = S.picker[S.pickerCursor];
  const rm = cur && cur.worktree ? [['shift+del', 'remove worktree']] : [];
  // On a folder that is no project, enter finds the projects in it.
  const pathRow = S.paths && S.paths[S.pickerCursor];
  const findHere = S.canFind && pathRow && !pathRow.backend && pathRow.search;
  const pathOpen = (here, there) => (findHere ? [['enter', 'find projects in it']] : [['enter', here], ['shift+enter', there]]);
  const keys = f
    ? (f.searching ? [['enter', 'add those found so far'], ['space', 'tick'], ['↑/↓', 'move'], ['esc', 'stop searching']]
      : [['enter', 'add the ticked'], ['space', 'tick'], ['ctrl+a', 'all or none'], ['↑/↓', 'move'], ['esc', 'back']])
    : overlay
    ? (S.paths ? [...pathOpen(where(newTab), where(!newTab)), ['tab', 'complete'], ['ctrl+y', 'copy path'], ['esc', 'close']]
      : [['enter', where(newTab)], ['shift+enter', where(!newTab)], ['↑/↓', 'move'], ['ctrl+y', 'copy path'], ['del', delWord()], ...rm, ['esc', 'close']])
    : S.paths ? [...pathOpen('open', 'new tab'), ['tab', 'complete'], ['↑/↓', 'move'], ['ctrl+y', 'copy path'], ['esc', 'clear'], ['ctrl+c', 'quit']]
      : [['enter', 'open'], ['shift+enter', 'new tab'], ['↑/↓', 'move'], ['ctrl+y', 'copy path'], ['del', delWord()], ...rm, ['esc', 'clear search'], ['ctrl+c', 'quit']];
  const body = f ? html`<${FindList} />` : list;
  const sure = S.pickerConfirm;
  const footer = sure && sure.remove && sure.dirty
    ? html`<div class="footer warn">the worktree ${sure.name}: ${sure.dirty}. <kbd>y</kbd> removes it anyway, losing them; any other key keeps it</div>`
    : sure && sure.remove
    ? html`<div class="footer warn">remove the worktree ${sure.name}? its folder${sure.folder ? ' ' + sure.folder : ''} goes and xf forgets it; its branch stays.${sure.key && alsoOpenIn(sure.key) ? ' it is open in ' + alsoOpenIn(sure.key) + '.' : ''} <kbd>y</kbd> removes, any other key keeps it</div>`
    : sure
    ? html`<div class="footer warn">close ${sure.name}?${alsoOpenIn(sure.key) ? ' it is open in ' + alsoOpenIn(sure.key) + '.' : ''}${sure.unsaved.length ? ' ' + unsavedWords(sure.unsaved) : ''} <kbd>y</kbd> closes, any other key keeps it</div>`
    : S.flash ? html`<div class="footer warn">${S.flash}</div>` : html`<div class="footer"><${Keys} list=${keys} /></div>`;
  if (overlay) {
    return html`<div class="overlay-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) { S.overlay = null; redraw(); } }}>
      <div class="picker picker-over">
        <h1>switch or add a project<span class="dim">${'\u00a0· ' + (newTab ? 'in a new tab' : 'in this tab')}</span></h1>
        ${body}${footer}
      </div>
    </div>`;
  }
  return html`<div class="app picker-page">
    <div class="picker">
      <h1><span class="badge">XF</span> pick a project<span class="spacer"></span><${WindowsPill} /><${ThemeButton} /><${GearButton} /></h1>
      <p class="dim">${f
        ? html`Ticked projects join this list, ready to open; xf doesn't open them yet.`
        : S.pickerTotal && S.canFind
        ? html`These are the projects xf has opened or found. Type a path (<code>~/…</code> or <code>/…</code>) to open another, or <${FindButton} link=${true} />.`
        : S.pickerTotal
        ? html`These are the projects xf has opened or <code>xf search</code> found; type a path (<code>~/…</code> or <code>/…</code>) to open another.`
        : S.canFind
        ? html`xf hasn't opened a project yet. Type a folder, such as <code>~/repos</code>, to find the Dataform and dbt projects in it.`
        : html`xf hasn't opened a project yet. Type the path to a Dataform or dbt project: <code>~/…</code> or <code>/…</code>.`}</p>
      <${AuthWarning} />
      ${body}${footer}
    </div>
  </div>`;
}

// afterOpen says the picker's row i starts the projects no tab shows,
// under the open ones: the first that is neither open nor a worktree
// under the row above it.
function afterOpen(i) {
  const p = S.picker[i];
  if (p.open || p.child || i === 0) return false;
  const above = S.picker.slice(0, i);
  return above.some((x) => x.open) && !above.some((x) => !x.open && !x.child);
}

// unsavedWords says which files tabs have unsaved changes in, which
// closing the project leaves in those tabs, unsaved.
function unsavedWords(files) {
  const shown = files.slice(0, 3).join(', ') + (files.length > 3 ? ' and ' + (files.length - 3) + ' more' : '');
  return 'A tab has unsaved changes in ' + shown + ': it keeps them, still unsaved.';
}

// delWord is what del does to the project under the cursor.
function delWord() {
  const p = S.picker[S.pickerCursor];
  return p && p.open ? 'close' : 'forget';
}

// alsoOpenIn words the other browser windows on the project key names:
// "Window 2", "Window 2 and Window 3"; '' for none.
function alsoOpenIn(key) {
  const ns = windowsOn(key).others;
  const ws = ns.map((n) => 'Window ' + n);
  return ws.length > 1 ? ws.slice(0, -1).join(', ') + ' and ' + ws[ws.length - 1] : ws[0] || '';
}

// openWord is what a picker row says of a project a tab shows: this
// tab's, the browser window that shows it, or just open (no window of
// this browser shows it).
// Badges are what a picker row says of its project as it is now: whether
// a tab shows it, and while open, whether it compiles or runs and its
// errors; and whether tabs have unsaved changes to its files.
function Badges({ p }) {
  const o = S.open.find((x) => x.key === p.key);
  const unsaved = (o ? o.unsaved : p.unsaved) || [];
  const bits = [];
  if (S.pickerNew && S.pickerNew.has(p.root)) bits.push(html`<span class="pick-new">new</span>`);
  if (p.open) bits.push(html`<span class="pick-open">${openWord(p)}</span>`);
  if (o && o.compiling) bits.push(html`<span class="warn">compiling…</span>`);
  if (o && o.running) bits.push(html`<span class="warn">running…</span>`);
  if (o && o.errors) bits.push(html`<span class="error">${o.errors} error${o.errors === 1 ? '' : 's'}</span>`);
  else if (o && o.dryRunErrors) bits.push(html`<span class="error">${o.dryRunErrors} dry run error${o.dryRunErrors === 1 ? '' : 's'}</span>`);
  if (unsaved.length) bits.push(html`<span class="warn" title=${unsaved.join('\n')}>● unsaved</span>`);
  return bits.map((b, i) => html`${b}${i < bits.length - 1 || p.when ? ' · ' : ''}`);
}

function openWord(p) {
  if (p.key === S.key && S.mode === 'project') return 'this tab';
  const n = windowOf(p.key);
  return n ? 'open · Window ' + n : 'open';
}

// AuthWarning is the picker's banner for a Google sign-in that needs
// attention, which the server looks up after the picker shows.
function AuthWarning() {
  const alerts = S.pickerAuth;
  if (!alerts || !alerts.length) return null;
  const [copiedCmd, setCopiedCmd] = useState('');
  const doCopy = (cmd) => {
    const sel = window.getSelection && window.getSelection().toString();
    if (sel && sel.length > 0) return;
    st.copy(cmd, 'copied: ' + cmd);
    setCopiedCmd(cmd);
    setTimeout(() => setCopiedCmd(''), 1800);
  };
  return html`<div class="auth-warn" role="alert">
    ${alerts.map((a) => {
      const isCopied = copiedCmd === a.command;
      return html`<div class="auth-line">
        <span class="auth-msg">⚠ ${a.message}</span>
        <span class="auth-run">run <span class=${'auth-cmd' + (isCopied ? ' copied' : '')} role="button" tabindex="0" title="Click to copy, or select text"
          onClick=${() => doCopy(a.command)}
          onKeyDown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); doCopy(a.command); } }}><code>${isCopied ? '✓ copied' : a.command}</code></span></span>
        ${a.note && html`<span class="dim auth-note">${a.note}</span>`}
      </div>`;
    })}
    <button class="link auth-more" onClick=${() => st.hooks.openSettings()}>details in settings</button>
  </div>`;
}

// PathRows are the picker's rows while its query is a path: the folder
// typed, then the folders it completes to, each labelled with the tool
// whose project it is the root of. The folder typed, when it is no
// project, is the row that finds the projects in it.
function PathRows() {
  return S.paths.map((r, i) => {
    const sel = i === S.pickerCursor;
    const find = S.canFind && r.search && !r.backend;
    const label = find && !r.folder
      ? html`<span class="find-go">⌕ find projects in it</span>`
      : html`${r.backend && html`<span class=${'kind-word ' + (r.backend === 'dbt' ? 'k-inc' : 'k-tbl')}>${r.backend}</span>`}${r.note && html` <span class="dim">${r.note}</span>`}${find && sel && html`<span class="dim">enter finds projects in it · tab goes in</span>`}`;
    return html`<div class=${'pick-row path-row' + (sel ? ' sel' : '') + (r.more ? ' more' : '') + (find && !r.folder ? ' find-row-go' : '')}
    ref=${(e) => { if (e && sel) scrollNearest(e); }}
    onMouseMove=${() => { if (S.pickerCursor !== i) { S.pickerCursor = i; redraw(); } }}
    onClick=${(e) => st.pickProject(i, true, e.shiftKey)}>
    <span class="lead">${sel ? '▸' : ''}</span>
    <span class=${'pick-root' + (r.backend || find ? '' : ' dim')} title=${r.root || ''}>${r.path}</span>
    <span class="path-label">${label}</span>
  </div>`;
  });
}

// FindButton starts finding projects: it types ~/ in the picker, which
// lists the home folder's folders to pick one from, the home folder
// itself first.
function FindButton({ link }) {
  const go = (e) => { e.preventDefault(); st.setPickerQuery('~/'); };
  return link
    ? html`<button class="link find-link" onMouseDown=${go}>find the projects in a folder</button>`
    : html`<button class="run-btn find-btn" onMouseDown=${go}>⌕ Find projects in a folder…</button>`;
}

// FindList is the picker while it finds the projects in a folder: those
// found so far, each ticked to add unless the picker lists it already,
// and the buttons that add them or go back.
function FindList() {
  const f = S.find;
  const fresh = f.rows.filter((r) => !r.known);
  const ticked = fresh.filter((r) => r.on).length;
  const known = f.rows.length - fresh.length;
  const nameW = Math.min(36, Math.max(12, ...f.rows.map((r) => r.name.length))) + 1;
  const cols = `grid-template-columns:2ch 3ch ${nameW}ch 9ch minmax(0,1fr) auto`;
  const count = f.rows.length + ' found' + (f.searching ? ' so far' : '') + (known ? ' · ' + known + ' listed already' : '');
  const empty = f.error ? '' : f.searching ? 'looking in every folder below ' + f.shown + '…' : 'no Dataform or dbt projects in ' + f.shown;
  return html`<div class="picker-list find-list">
      <div class="query">
        <span class="prompt">⌕</span>
        <span class="find-title" title=${f.shown}>${f.searching ? 'finding projects in ' : 'projects in '}<b>${f.shown}</b></span>
        <span class="dim count">${count}</span>
      </div>
      <div class=${'find-progress' + (f.searching ? ' on' : '')}></div>
      ${f.error && html`<div class="error pad">${f.error}</div>`}
      ${!f.rows.length && empty && html`<div class="dim pad">${empty}</div>`}
      ${f.rows.map((r, i) => html`<div class=${'pick-row find-row' + (i === f.cursor ? ' sel' : '') + (r.known ? ' known' : '')} style=${cols}
        ref=${(e) => { if (e && i === f.cursor) scrollNearest(e); }}
        onMouseMove=${() => { if (f.cursor !== i) { f.cursor = i; redraw(); } }}
        onClick=${() => { f.cursor = i; st.toggleFound(i); redraw(); }}>
        <span class="lead">${i === f.cursor ? '▸' : ''}</span>
        <span class=${'find-tick' + (r.on ? ' on' : '')} role="checkbox" aria-checked=${r.on ? 'true' : 'false'} aria-disabled=${r.known ? 'true' : 'false'}>${r.known ? '·' : r.on ? '✓' : ''}</span>
        <span class="pick-name" title=${r.name}>${r.name}</span>
        <span class=${'kind-word ' + (r.backend === 'dbt' ? 'k-inc' : 'k-tbl')}>${r.backend}</span>
        <span class="dim pick-root" title=${r.root}>${r.rel === '.' ? r.path : r.rel}</span>
        <span class="dim pick-when">${r.known ? 'listed already' : ''}</span>
      </div>`)}
      <div class="find-actions">
        <button class="run-btn" disabled=${!ticked || f.adding} onMouseDown=${(e) => { e.preventDefault(); st.addFound(); }}>${f.adding ? 'adding…' : 'Add ' + ticked + ' project' + (ticked === 1 ? '' : 's')}</button>
        <button class="plain" onMouseDown=${(e) => { e.preventDefault(); st.leaveFind(); redraw(); }}>${f.searching ? 'Stop searching' : 'Back'}</button>
      </div>
    </div>`;
}

st.hooks.projectsOverlay = () => html`<${Picker} overlay=${true} />`;
st.hooks.paletteOverlay = () => html`<${Palette} />`;

export function App() {
  return html`<${Page} /><${ThemePicker} /><${WindowsList} />`;
}

// LostNotice covers the page while xf web can't be reached (st.lostShown):
// it was stopped, or the stream keeps dropping. The page behind it takes
// no keys or clicks, which could only fail, and comes back as it was if
// xf web does, on the same port.
export function LostNotice() {
  if (!st.lostShown()) return null;
  const stopped = S.lost === 'stopped';
  const unsaved = st.unsavedHere();
  const shown = unsaved.slice(0, 3).join(', ') + (unsaved.length > 3 ? ' and ' + (unsaved.length - 3) + ' more' : '');
  return html`<div class="overlay-back lost-back">
    <div class="overlay pane focused confirm lost" role="alertdialog" aria-labelledby="lost-title">
      <header class="pane-title"><span class="pane-name" id="lost-title">${stopped ? '■ xf web has stopped' : '● xf web can\'t be reached'}</span></header>
      <div class="pane-body">
        <p>${stopped ? 'It was stopped in its terminal (ctrl+c), or its process ended.' : 'This window lost its connection to xf web: it may have stopped or crashed.'} Nothing here can be saved, compiled or run until it is back.</p>
        ${unsaved.length > 0 && html`<p class="warn">Unsaved changes in ${shown}: closing this window loses them.</p>`}
        <p class="dim">Start it again with <code>xf web</code>. This window picks up where it was if xf web comes back at ${location.host}; if not, close it.</p>
        <p class="dim lost-retry">trying again…</p>
      </div>
    </div>
  </div>`;
}

function Page() {
  switch (S.mode) {
    case 'loading': return html`<div class="app center"><div class="ramp wide"></div></div>`;
    case 'error': return html`<div class="app center"><p class="error">${S.flash}</p></div>`;
    case 'gone': return html`<div class="app center gone"><p><span class="badge">XF</span> xf web has quit.</p><p class="dim">Start it again with <code>xf web</code>.</p></div>`;
  }
  if (SET.open) return html`<${SettingsPage} />`;
  if (S.mode === 'picker') return html`<${Picker} />`;
  if (S.checks && S[S.checks]) return html`<${ChecksPage} />`;
  if (S.scratch && S.scratch.open) return html`<${ScratchPage} />`;
  if (S.inspector) return html`<${InspectorHost} />`;
  return html`<${Project} />`;
}

// InspectorHost draws the dependency inspector's page (ADR 0033), whose
// module is loaded the first time it opens: it is not in the first load.
let inspectorPage = null, inspectorLoading = null;
function InspectorHost() {
  if (inspectorPage) return html`<${inspectorPage} />`;
  if (!inspectorLoading) {
    inspectorLoading = import('./inspector-view.js').then((m) => { inspectorPage = m.InspectorPage; redraw(); });
  }
  return html`<div class="app"><div class="dim pad">loading the dependency inspector…</div></div>`;
}
