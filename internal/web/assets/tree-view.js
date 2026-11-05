// The file tree's pane (see tree.js), and the note the right-hand panes
// show while a file with no action is open. The column shows the tree,
// Source Control (scm-view.js), Search (search-view.js) or the open
// projects, one at a time, which the rail's icons switch between.

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, redraw, setFocus, openProjects, commandsKey, projectsKey, hooks, onMac, toggleTerm, toggleLog } from './state.js';
import * as tr from './tree.js';
import { SRC } from './source.js';
import { readOpenWorktrees } from './picker.js';
import * as sc from './scm.js';
import { ScmPane, Confirm } from './scm-view.js';
import * as sr from './search.js';
import { SearchPane } from './search-view.js';

const html = htm.bind(h);
const T = tr.T;

const gitWord = { M: 'changed', A: 'added', U: 'untracked', C: 'in conflict' };

function Row({ r, cur, focused }) {
  const cls = 'tree-row' + (r.dir ? ' dir' : '') + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '') + (!r.dir && r.path === SRC.file ? ' active' : '') + (r.git ? ' git-' + r.git : '') + (r.errors ? ' has-errors' : '');
  const why = [r.path, r.git && gitWord[r.git], r.errors && r.errors + ' error' + (r.errors === 1 ? '' : 's'), r.actions && r.actions + ' action' + (r.actions === 1 ? '' : 's')].filter(Boolean).join(' · ');
  return html`<div class=${cls} title=${why} style=${'padding-left:' + (0.5 + r.depth * 1.5) + 'ch'}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); if (e.button === 0) tr.clickRow(r); }}
      onDblClick=${(e) => { e.stopPropagation(); tr.dblClickRow(r); }}
      onContextMenu=${(e) => openMenu(e, r)}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="tree-caret">${r.dir ? (r.open ? '▾' : '▸') : ''}</span>
    <span class="tree-name">${r.name}${r.dir ? '/' : ''}</span>
    ${r.dirty && html`<span class="tree-dirty" title="unsaved">●</span>`}
    ${r.errors > 0 && html`<span class="tree-err">${r.errors}</span>`}
    ${r.git && html`<span class="tree-git">${r.git}</span>`}
  </div>`;
}

// openMenu is a right-click in the tree: on a row, its menu, else the
// project root's. Shift+right-click gives the browser's own menu.
function openMenu(e, r) {
  if (e.shiftKey) return;
  e.preventDefault();
  e.stopPropagation();
  tr.rowMenu(r, { x: e.clientX, y: e.clientY }, e.currentTarget.closest('.tree-list'));
}

// nearest scrolls el into view within the tree's list only.
function nearest(el) {
  const box = el.closest('.tree-list');
  if (!box) return;
  const e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
  if (e.top < b.top) box.scrollTop -= b.top - e.top;
  else if (e.bottom > b.bottom) box.scrollTop += e.bottom - b.bottom;
}

// centre scrolls the list in box to the row reveal asked for (T.centre),
// once it is drawn: to the middle, as far as the list scrolls, and a
// row cmd+b revealed flashes. A reveal waits while its row isn't listed
// yet, and is dropped once the cursor has moved off it.
function centre(box) {
  const c = T.centre;
  if (!c || !box) return;
  if (T.cursor !== c.path) { T.centre = null; return; }
  const el = box.querySelector('.tree-row.cur');
  if (!el) return;
  T.centre = null;
  const e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
  if (!c.always && e.top >= b.top && e.bottom <= b.bottom) return;
  box.scrollTop += e.top - b.top - (b.height - e.height) / 2;
  if (c.always) {
    el.classList.remove('revealed');
    void el.offsetWidth; // starts the flash again
    el.classList.add('revealed');
    el.addEventListener('animationend', () => el.classList.remove('revealed'), { once: true });
  }
}

// Prompt is a/r's box: the path to make or rename to.
function Prompt() {
  const p = T.prompt;
  const input = useRef(null);
  useLayoutEffect(() => {
    const el = input.current;
    if (el && document.activeElement !== el) {
      el.focus();
      if (p.kind === 'rename' && p.move) {
        // The folder it is in is selected, to type another.
        el.setSelectionRange(0, Math.max(0, p.value.lastIndexOf('/')));
      } else if (p.kind === 'rename') {
        // The name, not its folder or type, is selected, as editors do.
        const slash = p.value.lastIndexOf('/') + 1, dot = p.value.lastIndexOf('.');
        el.setSelectionRange(slash, dot > slash && !p.dir ? dot : p.value.length);
      } else el.setSelectionRange(p.value.length, p.value.length);
    }
  }, [p.kind, p.path, p.move]);
  if (p.kind === 'delete') {
    const what = p.dir ? p.path + '/' + (p.count ? ' and its ' + p.count + ' file' + (p.count === 1 ? '' : 's') : '') : p.path;
    return html`<div class="tree-prompt confirm">
      <div>move <b>${what}</b> to the trash?</div>
      <${Confirm} yes="move to trash" no="keep" danger onKey=${tr.key} />
    </div>`;
  }
  if (p.check) {
    const c = p.check;
    const names = c.renames.map((x) => x.id.split('.').pop() + ' → ' + x.name).join(', ');
    return html`<div class="tree-prompt confirm">
      <div>renames action <b>${names}</b></div>
      ${c.refs > 0
        ? html`<div class="warn">${c.refs} action${c.refs === 1 ? '' : 's'} in ${c.files} file${c.files === 1 ? '' : 's'} refer${c.refs === 1 ? 's' : ''} to it by its old name and will break</div>`
        : html`<div class="dim">nothing else refers to it</div>`}
      <${Confirm} yes="rename" no="go back" danger=${c.refs > 0} onKey=${tr.key} />
    </div>`;
  }
  const label = p.kind === 'new' ? 'new file (end with / for a folder)' : p.move ? 'move ' + p.path + ' to (a new folder is made)' : 'rename or move ' + p.path;
  return html`<div class="tree-prompt">
    <div class="dim">${label}</div>
    <input ref=${input} value=${p.value} spellcheck="false" autocomplete="off" disabled=${p.busy}
      onInput=${(e) => { p.value = e.target.value; }}
      onKeyDown=${(e) => {
        if (e.key === 'Enter') { e.preventDefault(); tr.submitPrompt(); }
        else if (e.key === 'Escape') { e.preventDefault(); tr.cancelPrompt(); setFocus('tree'); }
      }}
      onBlur=${() => { if (T.prompt === p && !p.busy) tr.cancelPrompt(); }} />
  </div>`;
}

function Filter() {
  const input = useRef(null);
  useLayoutEffect(() => { if (T.filtering && input.current && document.activeElement !== input.current) input.current.focus(); });
  if (!T.filtering && !T.filter) return null;
  return html`<div class="tree-filter">
    <span class="dim">/</span>
    <input ref=${input} value=${T.filter} spellcheck="false" autocomplete="off" readOnly=${!T.filtering}
      onInput=${(e) => tr.setFilter(e.target.value)}
      onKeyDown=${(e) => {
        if (e.key === 'Enter') { e.preventDefault(); tr.endFilter(true); }
        else if (e.key === 'Escape') { e.preventDefault(); tr.endFilter(false); }
        else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); tr.key(e.key === 'ArrowDown' ? 'down' : 'up'); redraw(); }
      }}
      onBlur=${() => { if (T.filtering) tr.endFilter(true); }} />
  </div>`;
}

// ---- the rail ----

// The rail's icons: line drawings on a 24px grid, in the text's colour.
const icon = (body) => html`<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
const icons = {
  files: icon(html`<path d="M14 3H9a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V8z" /><path d="M14 3v5h5" /><path d="M4 8v11a2 2 0 0 0 2 2h8" />`),
  search: icon(html`<circle cx="11" cy="11" r="6.5" /><path d="m20 20-4.3-4.3" />`),
  scm: icon(html`<circle cx="6" cy="18" r="2.5" /><circle cx="6" cy="6" r="2.5" /><circle cx="18" cy="8" r="2.5" /><path d="M6 8.5v7" /><path d="M18 10.5c0 4-4 5-10 6" />`),
  projects: icon(html`<rect x="3" y="8" width="13" height="13" rx="2" /><path d="M8 8V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-3" />`),
  term: icon(html`<rect x="3" y="4" width="18" height="16" rx="2" /><path d="m7 10 3 2.5L7 15" /><path d="M12.5 15H17" />`),
  settings: icon(html`<path d="M20 7h-9" /><path d="M14 17H4" /><circle cx="17" cy="17" r="3" /><circle cx="7" cy="7" r="3" />`),
};

// Rail is the thin column of icons on the far left (ADR 0035): the
// column's four views, the log pane's terminal and the settings, each
// doing what its key does, so a mouse finds them and the title teaches
// the key. The one showing is lit.
export function Rail() {
  if (!sc.G.loaded && !sc.G.error) sc.refresh();
  const n = sc.count();
  const shows = (view) => T.shown && T.view === view;
  const btn = (name, title, on, count, act, cls) => html`<button class=${'rail-btn' + (on ? ' on' : '') + (cls ? ' ' + cls : '')} title=${title} aria-label=${title}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); if (e.button === 0) { act(); redraw(); } }}>${icons[name]}${count}</button>`;
  const badge = (k, cls) => (k ? html`<span class=${'rail-count' + (cls ? ' ' + cls : '')}>${k}</span>` : '');
  const term = S.caps.terminal;
  const jKey = onMac ? '⌘j' : 'ctrl+j';
  return html`<nav class="rail">
    ${btn('files', 'Files (' + tr.treeKey + ')', shows('files'), '', () => tr.toggleView('files'))}
    ${btn('search', 'Search (' + sr.searchKey + ')', shows('search'), '', () => (shows('search') ? tr.toggleView('search') : sr.openSearch()))}
    ${btn('scm', 'Source Control (' + sc.scmKey + ')', shows('scm'), badge(n, 'changes'), () => tr.toggleView('scm'))}
    ${btn('projects', 'Projects (' + projectsKey + ')', shows('projects'), badge(S.open.length > 1 ? S.open.length : 0), () => tr.toggleView('projects'))}
    ${btn('term', (term ? 'Terminal (' : 'Log (') + jKey + ')', S.showLog && (!term || S.logTab === 'term'), '', () => (term ? toggleTerm() : toggleLog()))}
    ${btn('settings', 'Settings (ctrl+,)', false, '', () => hooks.openSettings(), 'rail-end')}
  </nav>`;
}

// ColumnTitle is the column's header, set in its border as every pane's
// title is: its number and the view it shows, with Source Control's
// count of changed files, Search's of matches and the open projects'.
// The rail (ADR 0035) switches the view and hides the column.
function ColumnTitle() {
  let extra = '';
  if (T.view === 'files') {
    if (T.loaded && !T.git) extra = html`<span class="dim"> · not git</span>`;
    else if (T.truncated) extra = html`<span class="warn"> · first 20000</span>`;
  } else if (T.view === 'scm') {
    if (!sc.G.loaded && !sc.G.error) sc.refresh();
    const n = sc.count();
    if (n) extra = html`<span class="col-count">${n}</span>`;
  } else if (T.view === 'search') {
    sr.restore();
    const found = sr.F.query && sr.F.result ? sr.counts().matches : 0;
    if (found) extra = html`<span class="col-count">${found}${sr.F.result.truncated ? '+' : ''}</span>`;
  } else if (T.view === 'projects' && S.open.length > 1) {
    extra = html`<span class="col-count">${S.open.length}</span>`;
  }
  const name = { files: 'files', scm: 'source control', search: 'search', projects: 'projects' }[T.view];
  return html`<header class="pane-title col-title">
    <span class="pane-num">0</span><span class="col-name">${name}${extra}</span>
  </header>`;
}

export function TreePane() {
  const list = useRef(null);
  useLayoutEffect(() => { if (T.view === 'files') centre(list.current); });
  const style = T.width ? `flex:0 0 ${T.width}px;width:${T.width}px` : '';
  if (T.view === 'scm') return html`<${ScmPane} header=${html`<${ColumnTitle} />`} style=${style} />`;
  if (T.view === 'search') return html`<${SearchPane} header=${html`<${ColumnTitle} />`} style=${style} />`;
  if (T.view === 'projects') return html`<${ProjectsPane} style=${style} />`;
  const focused = S.focus === 'tree';
  const rows = tr.rows();
  let body;
  if (T.error && !T.loaded) body = html`<div class="dim pad">${T.error}</div>`;
  else if (!T.loaded) body = html`<div class="dim pad">listing…</div>`;
  else if (!rows.length) body = html`<div class="dim pad">${T.filter ? 'no file matches' : 'no files'}</div>`;
  else body = rows.map((r) => html`<${Row} key=${r.path} r=${r} cur=${r.path === T.cursor} focused=${focused} />`);
  const note = focused ? 'a new · r rename · d delete' : tr.treeKey + ' hides';
  return html`<section class=${'pane tree-pane' + (focused ? ' focused' : '')} data-pane="tree" style=${style}
      onMouseDown=${() => { setFocus('tree'); redraw(); }}>
    <${ColumnTitle} />
    <div class="pane-body">
      <${Filter} />
      ${T.prompt && html`<${Prompt} />`}
      <div class="tree-list" ref=${list} onContextMenu=${(e) => openMenu(e, null)}>${body}</div>
    </div>
    <footer class="pane-note">${note}</footer>
  </section>`;
}

// ProjectsPane is the column's projects tab: the projects open in xf web
// (ADR 0008) with what each is doing, and under each its worktrees that
// aren't open, dimmed (ADR 0032). A click opens one as the switcher
// does, shift+click the other way; the last row opens the switcher, for a
// project not open yet.
function ProjectsPane({ style }) {
  const focused = S.focus === 'tree';
  readOpenWorktrees();
  const rows = tr.projectRows();
  const cursor = rows.some((o) => o.key === T.projCursor) ? T.projCursor : S.key;
  return html`<section class=${'pane tree-pane projects-pane' + (focused ? ' focused' : '')} data-pane="tree" style=${style}
      onMouseDown=${() => { setFocus('tree'); redraw(); }}>
    <${ColumnTitle} />
    <div class="pane-body"><div class="tree-list">
      ${rows.map((o) => html`<${ProjectRow} key=${o.key} o=${o} cur=${o.key === cursor} focused=${focused} />`)}
      <div class="tree-row proj-row proj-more" title=${'switch to or add any project (' + commandsKey + ' › Project: switch or add)'}
        onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); openProjects(); redraw(); }}>
        <span class="proj-lead">+</span><span class="proj-name">add another project…</span>
      </div>
    </div></div>
    <footer class="pane-note">${focused ? 'enter opens' : tr.treeKey + ' hides'}</footer>
  </section>`;
}

function ProjectRow({ o, cur, focused }) {
  const here = o.key === S.key;
  const unsaved = o.unsaved || [];
  const errors = o.errors || o.dryRunErrors || 0;
  const what = [
    o.root,
    o.found && 'on ' + o.branch + ', not open yet',
    o.compiling && 'compiling',
    o.running && 'running',
    o.errors && o.errors + ' compile error' + (o.errors === 1 ? '' : 's'),
    !o.errors && o.dryRunErrors && o.dryRunErrors + ' dry run error' + (o.dryRunErrors === 1 ? '' : 's'),
    unsaved.length && 'unsaved: ' + unsaved.join(', '),
  ].filter(Boolean);
  const newTab = S.openIn === 'new-tab';
  const hint = here ? 'shown in this tab' : 'click opens ' + (newTab ? 'in a new tab' : 'here') + ' · shift+click ' + (newTab ? 'here' : 'in a new tab');
  const cls = 'tree-row proj-row' + (here ? ' here' : '') + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '') + (o.found ? ' proj-found' : '');
  return html`<div class=${cls} title=${[...what, hint].join('\n')}
      ref=${(el) => { if (el && cur) nearest(el); }}
      onMouseDown=${(e) => { if (e.button !== 0) return; e.stopPropagation(); e.preventDefault(); T.projCursor = o.key; setFocus('tree'); tr.openProjectRow(o, e.shiftKey); redraw(); }}
      onContextMenu=${(e) => { setFocus('tree'); tr.openProjectMenu(e, o); }}>
    <span class="proj-lead">${here ? '▸' : ''}</span>
    <span class=${'proj-name' + (o.child ? ' proj-child' : '')}>${o.child ? '└ @' + o.worktree : o.name}</span>
    ${o.compiling && !o.running && html`<span class="warn proj-mark">◌</span>`}
    ${o.running && html`<span class="warn proj-mark">▶</span>`}
    ${errors > 0 && html`<span class="error proj-mark">✗${errors}</span>`}
    ${unsaved.length > 0 && html`<span class="warn proj-mark">●</span>`}
  </div>`;
}

// TreeSplitter is the handle between the tree and the rest, which drags
// its width; a double-click gives it its own width back.
export function TreeSplitter() {
  return html`<div class="v-splitter tree-splitter" title="drag to resize · double-click to reset"
    onDblClick=${() => { tr.setTreeWidth(0); redraw(); }}
    onMouseDown=${(e) => {
      e.preventDefault();
      const pane = document.querySelector('[data-pane="tree"]');
      if (!pane) return;
      const x0 = e.clientX, w0 = pane.getBoundingClientRect().width;
      document.body.classList.add('v-resizing');
      const move = (ev) => {
        T.width = Math.round(Math.min(Math.max(w0 + ev.clientX - x0, 140), window.innerWidth * 0.5));
        redraw();
      };
      const up = () => {
        document.body.classList.remove('v-resizing');
        tr.setTreeWidth(T.width);
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
    }}><span></span></div>`;
}

// PlainNote is what the panes that show an action say while a file with
// no action is open: its path, and that it has none.
export function PlainNote() {
  return html`<div class="pad plain-note"><span>${S.plainFile}</span><span class="dim"> · no action in this file</span></div>`;
}

export const treeShown = () => !!tr.T.shown;
