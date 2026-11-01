// The Search view's pane (see search.js): the box and its toggles, the
// replace field, the include and exclude fields, and the matches by file.

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, redraw, setFocus, onMac } from './state.js';
import * as sr from './search.js';
import { Confirm } from './scm-view.js';

const html = htm.bind(h);
const F = sr.F;

const act = (f) => (e) => { e.stopPropagation(); e.preventDefault(); f(); redraw(); };
const allKey = onMac ? '⌘⌥↵' : 'ctrl+alt+↵';

// nearest scrolls el into view within the list only.
function nearest(el) {
  const box = el.closest('.tree-list');
  if (!box) return;
  const e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
  if (e.top < b.top) box.scrollTop -= b.top - e.top;
  else if (e.bottom > b.bottom) box.scrollTop += e.bottom - b.bottom;
}

// Field is one of the view's text boxes; name is the option it edits.
function Field({ name, placeholder, children }) {
  const input = useRef(null);
  useLayoutEffect(() => {
    const el = input.current;
    if (el && F.box === name) {
      F.box = '';
      el.focus();
      el.select();
    }
  });
  return html`<div class="ps-field">
    <input ref=${input} value=${F[name]} placeholder=${placeholder} spellcheck="false" autocomplete="off" data-search=${name}
      onInput=${(e) => sr.set(name, e.target.value)}
      onFocus=${() => { if (S.focus !== 'tree') { setFocus('tree'); redraw(); } }}
      onKeyDown=${(e) => { if (sr.boxKey(e)) { e.preventDefault(); e.stopPropagation(); redraw(); } }} />
    ${children}
  </div>`;
}

function Toggle({ name, label, title }) {
  return html`<button class=${'ps-toggle' + (F[name] ? ' on' : '')} title=${title}
    onMouseDown=${act(() => sr.toggle(name))}>${label}</button>`;
}

function Boxes() {
  return html`<div class="ps-boxes">
    <div class="ps-line">
      <button class="ps-twist" title=${F.replacing ? 'hide the replace field' : 'replace'}
        onMouseDown=${act(() => { sr.set('replacing', !F.replacing); if (F.replacing) F.box = 'replace'; })}>${F.replacing ? '▾' : '▸'}</button>
      <${Field} name="query" placeholder="search">
        <${Toggle} name="case" label="Aa" title="match case (alt+c)" />
        <${Toggle} name="word" label="ab" title="whole word (alt+w)" />
        <${Toggle} name="regex" label=".*" title="regular expression, RE2 (alt+r)" />
      <//>
    </div>
    ${F.replacing && html`<div class="ps-line">
      <span class="ps-twist"></span>
      <${Field} name="replace" placeholder=${F.regex ? 'replace ($1 for a group)' : 'replace'}>
        <button class="ps-toggle" title=${'replace all (' + allKey + ')'} onMouseDown=${act(sr.askReplaceAll)}>all</button>
      <//>
    </div>`}
    <div class="ps-line">
      <span class="ps-twist"></span>
      <button class=${'ps-more' + (F.globs || F.include || F.exclude ? ' on' : '')} title="files to include and exclude"
        onMouseDown=${act(() => sr.set('globs', !F.globs))}>…</button>
    </div>
    ${F.globs && html`
      <div class="ps-line"><span class="ps-twist"></span><${Field} name="include" placeholder="files to include: definitions/**, *.sqlx" /></div>
      <div class="ps-line"><span class="ps-twist"></span><${Field} name="exclude" placeholder="files to exclude: *.md, includes/" /></div>`}
  </div>`;
}

function Summary() {
  const v = F.result;
  let text = '';
  if (F.error) text = html`<span class="error">${F.error}</span>`;
  else if (!F.query) text = html`<span class="dim">type to search the project's files</span>`;
  else if (v) {
    const c = sr.counts();
    text = c.matches
      ? html`<span>${c.matches}${v.truncated ? '+' : ''} result${c.matches === 1 ? '' : 's'} in ${c.files} file${c.files === 1 ? '' : 's'}</span>${v.truncated ? html`<span class="warn"> · narrow the search</span>` : ''}`
      : html`<span class="dim">no results in ${v.searched} file${v.searched === 1 ? '' : 's'}</span>`;
  }
  return html`<div class="ps-summary">
    ${text}${F.busy && html`<span class="dim"> · searching…</span>`}
    <span class="spacer"></span>
    ${F.undo && html`<button class="ps-toggle" title="put back the files the last replace changed" onMouseDown=${act(sr.undoReplace)}>undo replace</button>`}
  </div>`;
}

// Match is a match's line: the text around it, and it, struck through
// beside what it becomes while the replace field shows.
function Match({ r, cur, focused }) {
  const m = r.m;
  const first = m.text.split('\n')[0];
  const more = first !== m.text;
  return html`<div class=${'tree-row ps-match' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '')} title=${r.path + ':' + (m.line + 1)}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sr.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="ps-text">
      <span class="dim">${m.before}</span><mark class=${F.replacing ? 'ps-old' : 'ps-hit'}>${first}${more ? ' ⏎' : ''}</mark>${F.replacing && html`<ins class="ps-new">${m.with}</ins>`}<span class="dim">${m.after}</span>
    </span>
    <span class="ps-line-no">${m.line + 1}</span>
    <span class="ps-acts">
      ${F.replacing && html`<button title="replace (r)" onMouseDown=${act(() => { F.cursor = r.key; sr.replaceRow(r); })}>↻</button>`}
      <button title="dismiss (x)" onMouseDown=${act(() => { F.cursor = r.key; sr.key('x'); })}>✕</button>
    </span>
  </div>`;
}

function File({ r, cur, focused }) {
  const i = r.path.lastIndexOf('/');
  return html`<div class=${'tree-row ps-file' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '')} title=${r.path}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sr.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
    <span class="tree-name">${r.path.slice(i + 1)}${i > 0 && html`<span class="ps-dir">${r.path.slice(0, i)}</span>`}</span>
    <span class="ps-acts">
      ${F.replacing && html`<button title="replace in this file (r)" onMouseDown=${act(() => { F.cursor = r.key; sr.replaceRow(r); })}>↻</button>`}
      <button title="dismiss (x)" onMouseDown=${act(() => { F.cursor = r.key; sr.key('x'); })}>✕</button>
    </span>
    <span class="ps-count">${r.count}</span>
  </div>`;
}

export function SearchPane({ tabs, style }) {
  sr.restore();
  const focused = S.focus === 'tree';
  const rows = sr.rows();
  const p = F.prompt;
  const note = focused ? 'j/k show · enter go · x dismiss' + (F.replacing ? ' · r replace' : '') + ' · F4 next' : sr.searchKey + ' searches';
  return html`<section class=${'pane tree-pane ps-pane' + (focused ? ' focused' : '')} data-pane="tree" style=${style}
      onMouseDown=${() => { setFocus('tree'); redraw(); }}>
    ${tabs}
    <div class="pane-body">
      <${Boxes} />
      <${Summary} />
      ${p && html`<div class="tree-prompt confirm">
        <div>replace <b>${p.matches}</b> match${p.matches === 1 ? '' : 'es'} in <b>${p.files}</b> file${p.files === 1 ? '' : 's'} with <code>${F.replace || '(nothing)'}</code>?</div>
        <${Confirm} yes="replace all" no="keep" danger onKey=${(k) => { sr.key(k); redraw(); }} />
      </div>`}
      <div class="tree-list">
        ${rows.map((r) => r.kind === 'file'
          ? html`<${File} key=${r.key} r=${r} cur=${r.key === F.cursor} focused=${focused} />`
          : html`<${Match} key=${r.key} r=${r} cur=${r.key === F.cursor} focused=${focused} />`)}
      </div>
    </div>
    <footer class="pane-note">${note}</footer>
  </section>`;
}
