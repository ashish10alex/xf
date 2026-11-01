// The file tree's pane (see tree.js), and the note the right-hand panes
// show while a file with no action is open. The column's header has a tab
// for the tree, one for Source Control (scm-view.js) and one for Search
// (search-view.js), which it shows instead.

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, redraw, setFocus, openProjects, commandsKey, projectsKey } from './state.js';
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

// ColumnTabs is the column's header: a tab for each view, Source
// Control's with how many files have changed, Search's with how many
// matches it lists, and the column's close.
function ColumnTabs() {
  if (!sc.G.loaded && !sc.G.error) sc.refresh();
  const n = sc.count();
  const tab = (view, label, key, extra) => html`<button class=${'col-tab' + (T.view === view ? ' on' : '')} title=${label + ' (' + key + ')'}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); tr.showView(view); redraw(); }}>${label}${extra}</button>`;
  const hide = T.view === 'scm' ? sc.scmKey : tr.treeKey;
  sr.restore();
  const found = sr.F.query && sr.F.result ? sr.counts().matches : 0;
  return html`<header class="pane-title col-tabs">
    <span class="pane-num">0</span>
    ${tab('files', 'files', tr.treeKey, T.view === 'files' && T.loaded && !T.git ? html`<span class="dim"> · not git</span>` : T.view === 'files' && T.truncated ? html`<span class="warn"> · first 20000</span>` : '')}
    ${tab('scm', 'source control', sc.scmKey, n ? html`<span class="col-count">${n}</span>` : '')}
    ${tab('search', 'search', sr.searchKey, found ? html`<span class="col-count">${found}${sr.F.result.truncated ? '+' : ''}</span>` : '')}
    ${tab('projects', 'projects', projectsKey + ' lists them in the palette', S.open.length > 1 ? html`<span class="col-count">${S.open.length}</span>` : '')}
    <span class="spacer"></span>
    <button class="tree-close" title=${'hide the column (' + hide + ')'}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); tr.toggleColumn(); redraw(); }}>✕</button>
  </header>`;
}

export function TreePane() {
  const list = useRef(null);
  useLayoutEffect(() => { if (T.view === 'files') centre(list.current); });
  const style = T.width ? `flex:0 0 ${T.width}px;width:${T.width}px` : '';
  if (T.view === 'scm') return html`<${ScmPane} tabs=${html`<${ColumnTabs} />`} style=${style} />`;
  if (T.view === 'search') return html`<${SearchPane} tabs=${html`<${ColumnTabs} />`} style=${style} />`;
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
    <${ColumnTabs} />
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
    <${ColumnTabs} />
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
