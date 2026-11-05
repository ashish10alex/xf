// Source Control's pane (see scm.js): the commit box, the changes,
// stashes and recent commits, and the git output. The branch's merge
// request and pipeline (forge.js) are in a region docked at the bottom,
// below a sash: git's part keeps the top of the pane as it was.

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import * as cm from './vendor/codemirror.mjs';
import { S, redraw, setFocus, hooks } from './state.js';
import { createEditor } from './editor.js';
import * as sc from './scm.js';
import { ForgeRow } from './forge-view.js';
import * as fg from './forge.js';

const html = htm.bind(h);
const G = sc.G;

const gitWord = { M: 'modified', A: 'added', D: 'deleted', T: 'type changed', U: 'untracked', C: 'in conflict', R: 'renamed' };
const conflictWord = { UU: 'both modified', AA: 'both added', DD: 'both deleted', AU: 'added by us', UA: 'added by them', DU: 'deleted by us', UD: 'deleted by them' };

// hover buttons on a row act without moving the focus.
const act = (f) => (e) => { e.stopPropagation(); e.preventDefault(); f(); redraw(); };

// Confirm is a y/n question's two buttons: yes says what it does (red when
// it loses work), no keeps things as they are. A click is the key.
export function Confirm({ yes, no, danger, onKey }) {
  return html`<div class="confirm-buttons">
    <button class=${'confirm-btn yes' + (danger ? ' danger' : '')} onMouseDown=${act(() => onKey('y'))}><kbd>y</kbd>${yes}</button>
    <button class="confirm-btn" onMouseDown=${act(() => onKey('n'))}><kbd>n</kbd>${no}</button>
  </div>`;
}

function FileRow({ r, cur, focused }) {
  const g = r.kind === 'file' && r.section === 'conflicts' ? 'C' : r.git;
  const cls = 'scm-row file' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '') + ' git-' + g + (r.file ? '' : ' outside');
  const why = [r.file || r.path, r.section === 'conflicts' ? conflictWord[r.xy] || 'in conflict' : gitWord[r.git], !r.file && 'outside the project'].filter(Boolean).join(' · ');
  const buttons = [];
  if (r.kind === 'file' && r.section === 'changes') {
    buttons.push(html`<button title="discard (x)" onMouseDown=${act(() => { G.cursor = r.key; sc.key('x'); })}>↶</button>`);
    buttons.push(html`<button title="stage (space)" onMouseDown=${act(() => { G.cursor = r.key; sc.key(' '); })}>+</button>`);
  } else if (r.kind === 'file' && r.section === 'staged') {
    buttons.push(html`<button title="unstage (space)" onMouseDown=${act(() => { G.cursor = r.key; sc.key(' '); })}>−</button>`);
  } else if (r.kind === 'file' && r.section === 'conflicts') {
    buttons.push(html`<button title="mark resolved (space)" onMouseDown=${act(() => { G.cursor = r.key; sc.key(' '); })}>+</button>`);
  }
  const depth = r.kind === 'file' ? 1 : 2;
  return html`<div class=${cls} title=${why} style=${'padding-left:' + (0.5 + depth * 1.5) + 'ch'}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="scm-name">${r.name}</span>
    ${r.dir && html`<span class="scm-dir">${r.dir}</span>`}
    <span class="spacer"></span>
    <span class="scm-buttons">${buttons}</span>
    <span class="scm-git">${g}</span>
  </div>`;
}

function SectionRow({ r, cur, focused }) {
  const cls = 'scm-row section' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '');
  const buttons = [];
  if (r.id === 'changes') {
    // Stashing is here too, as Stashes isn't listed until there is one.
    if (G.st && (G.st.staged.length || G.st.changes.length)) buttons.push(html`<button title="stash every change (s)" onMouseDown=${act(() => sc.key('s'))}>stash</button>`);
    if (r.count) {
      buttons.push(html`<button title="discard all (x)" onMouseDown=${act(() => { G.cursor = r.key; sc.key('x'); })}>↶</button>`);
      buttons.push(html`<button title="stage all (a)" onMouseDown=${act(() => sc.key('a'))}>+</button>`);
    }
  } else if (r.id === 'staged' && r.count) {
    buttons.push(html`<button title="unstage all (space)" onMouseDown=${act(() => { G.cursor = r.key; sc.key(' '); })}>−</button>`);
  } else if (r.id === 'stashes') {
    buttons.push(html`<button title="stash every change (s)" onMouseDown=${act(() => sc.key('s'))}>+</button>`);
  }
  return html`<div class=${cls} onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
    <span class="scm-label">${r.label}</span>
    <span class="spacer"></span>
    <span class="scm-buttons">${buttons}</span>
    ${r.count >= 0 && html`<span class="scm-count">${r.count}</span>`}
  </div>`;
}

function StashRow({ r, cur, focused }) {
  const cls = 'scm-row stash' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '');
  return html`<div class=${cls} title=${'stash@{' + r.index + '} · ' + r.subject} style="padding-left:2ch"
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
    <span class="scm-name">${r.subject}</span>
    <span class="spacer"></span>
    <span class="scm-buttons">
      <button title="apply (space)" onMouseDown=${act(() => { G.cursor = r.key; sc.key(' '); })}>apply</button>
      <button title="pop (p)" onMouseDown=${act(() => { G.cursor = r.key; sc.key('p'); })}>pop</button>
      <button title="drop (d)" onMouseDown=${act(() => { G.cursor = r.key; sc.key('d'); })}>✕</button>
    </span>
    <span class="dim">${r.age}</span>
  </div>`;
}

function CommitRow({ r, cur, focused }) {
  const cls = 'scm-row commit' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '');
  // A merge says the branch it brought in; the tooltip has the SHA and subject.
  const subj = sc.commitSubject(r.subject);
  return html`<div class=${cls} title=${r.short + ' · ' + r.subject + ' · ' + r.author + (r.unpushed ? ' · not pushed' : '')} style="padding-left:2ch"
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }}
      ref=${(el) => { if (el && cur) nearest(el); }}>
    <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
    ${r.unpushed ? html`<span class="scm-ahead" title="not pushed">↑</span>` : ''}
    <span class="scm-name">${subj.merge && html`<span class="dim scm-merge">⑂ </span>`}${subj.text}</span>
    <span class="dim scm-author" title=${r.author}>${sc.initials(r.author)}</span>
    <span class="dim">${r.age}</span>
  </div>`;
}

function Row({ r, cur, focused }) {
  if (r.key === 's:mr') return html`<${DockHead} r=${r} cur=${cur} focused=${focused} />`;
  if (r.key.startsWith('f:')) return html`<${ForgeRow} r=${r} cur=${cur} focused=${focused} onClick=${sc.clickRow} refEl=${(el) => { if (el && cur) nearest(el); }} />`;
  switch (r.kind) {
    case 'section': return html`<${SectionRow} r=${r} cur=${cur} focused=${focused} />`;
    case 'file': case 'sfile': case 'cfile': return html`<${FileRow} r=${r} cur=${cur} focused=${focused} />`;
    case 'stash': return html`<${StashRow} r=${r} cur=${cur} focused=${focused} />`;
    case 'commit': return html`<${CommitRow} r=${r} cur=${cur} focused=${focused} />`;
    case 'more': return html`<div class=${'scm-row more' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '')} style="padding-left:3.5ch"
        onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }} ref=${(el) => { if (el && cur) nearest(el); }}>more…</div>`;
    case 'note': return html`<div class="scm-row note dim" style="padding-left:3.5ch">${r.text}</div>`;
  }
  return null;
}

// nearest scrolls el into view within the list only.
function nearest(el) {
  const box = el.closest('.scm-list');
  if (!box) return;
  const e = el.getBoundingClientRect(), b = box.getBoundingClientRect();
  if (e.top < b.top) box.scrollTop -= b.top - e.top;
  else if (e.bottom > b.bottom) box.scrollTop += e.bottom - b.bottom;
}

// ---- the commit box ----

// The box's editor is made once and moved into the pane each time it
// mounts, so its text, undo and vim state stay.
let made = null;

// The subject (the first line) is amber past 72 characters.
const longSubject = cm.Decoration.mark({ class: 'scm-long' });
const subjectMarks = cm.EditorView.decorations.compute(['doc'], (state) => {
  const first = state.doc.line(1);
  if (first.length <= 72) return cm.Decoration.none;
  return cm.Decoration.set([longSubject.range(first.from + 72, first.to)]);
});

function boxEditor() {
  if (made) return made;
  const el = document.createElement('div');
  el.className = 'scm-cm';
  const leave = () => { setFocus('tree'); redraw(); };
  const ed = createEditor(el, {
    doc: sc.getDraft(),
    placeholder: 'message (' + sc.commitKey + ' commits)',
    keys: [
      // tab goes on to the box's first button, where enter presses it.
      { key: 'Tab', run: (v) => { const b = v.dom.closest('.scm-commit')?.querySelector('.scm-commit-buttons button:not(:disabled)'); if (!b) return false; b.focus(); return true; }, preventDefault: true },
      { key: 'Mod-Enter', run: () => { sc.commit(); return true; }, preventDefault: true },
      { key: 'Ctrl-Enter', run: () => { sc.commit(); return true; }, preventDefault: true },
    ],
    save: () => sc.commit(),
    leave,
    say: (m) => { S.flash = m; redraw(); },
    extensions: [
      cm.EditorView.lineWrapping,
      subjectMarks,
      // A second escape, in normal mode, goes back to the list.
      cm.Prec.highest(cm.EditorView.domEventHandlers({
        keydown(e, view) {
          if (e.key !== 'Escape') return false;
          const vim = cm.getCM(view)?.state.vim;
          if (vim && !vim.insertMode && !vim.visualMode && !vim.inputState.keyBuffer.length && !vim.inputState.operator) {
            view.contentDOM.blur();
            leave();
            return true;
          }
          return false;
        },
      })),
    ],
    onFocus: () => { S.focus = 'tree'; redraw(); },
    onUpdate: (u) => { if (u.docChanged) sc.setDraft(u.state.doc.toString()); },
  });
  made = { el, ed };
  sc.box.ed = ed;
  return made;
}

// typeInBox focuses the box at the end of its text, in insert mode.
function typeInBox(ed) {
  const view = ed.view;
  view.focus();
  view.dispatch({ selection: { anchor: view.state.doc.length } });
  const c = cm.getCM(view);
  if (c && c.state.vim && !c.state.vim.insertMode) cm.Vim.handleKey(c, 'A', 'user');
}

// A focused button of the box takes its own enter, space and tab, which
// the list's keys would have; escape goes back to the list.
function buttonKeys(e) {
  if (!(e.target instanceof HTMLButtonElement) || e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    e.stopPropagation();
    if (e.target.disabled) return;
    e.target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  } else if (e.key === 'Tab') {
    e.stopPropagation();
  } else if (e.key === 'Escape') {
    e.preventDefault();
    e.stopPropagation();
    e.target.blur();
    setFocus('tree');
    redraw();
  }
}

function CommitBox() {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const b = boxEditor();
    ref.current.appendChild(b.el);
    b.ed.view.requestMeasure();
    return () => { if (b.el.parentNode === ref.current) ref.current.removeChild(b.el); };
  }, []);
  useLayoutEffect(() => {
    if (!sc.box.typeNext) return;
    sc.box.typeNext = false;
    typeInBox(boxEditor().ed);
  });
  const st = G.st;
  const op = st && st.op;
  let main = 'Commit', title = 'commit what is staged (' + sc.commitKey + ')';
  if (G.amend) { main = 'Amend'; title = 'amend HEAD with what is staged and this message (' + sc.commitKey + ')'; }
  if (op === 'merge') { main = 'Commit merge'; title = 'finish the merge, with git\'s message when the box is empty (' + sc.commitKey + ')'; }
  const can = sc.commitState();
  return html`<div class="scm-commit">
    <div class=${'scm-box' + (G.amend ? ' amending' : '')} ref=${ref}></div>
    <div class="scm-commit-buttons" onKeyDown=${buttonKeys}>
      ${op && op !== 'merge'
        ? html`<button class="run-btn" title=${'continue the ' + op} onMouseDown=${act(sc.continueOp)}>Continue ${op}</button>`
        : html`<button class="run-btn" disabled=${!can.commit} title=${can.commit ? title : 'nothing to commit'} onMouseDown=${act(() => sc.commit())}>${main}</button>`}
      ${!op && html`<button class="plain" disabled=${!can.all} title=${can.all ? 'stage every change, the new files too, then commit' : 'nothing to commit'} onMouseDown=${act(() => sc.commit({ all: true }))}>Commit all</button>`}
      ${G.amend && html`<span class="warn">amending HEAD</span>`}
    </div>
  </div>`;
}

// ---- prompts ----

function Prompt() {
  const p = G.prompt;
  const input = useRef(null);
  useLayoutEffect(() => {
    const el = input.current;
    if (el && document.activeElement !== el) el.focus();
  }, [p.kind]);
  if (p.kind === 'discard') {
    const what = p.all ? 'every change (' + p.count + ' file' + (p.count === 1 ? '' : 's') + ')' : p.name;
    return html`<div class="tree-prompt confirm">
      <div>discard <b>${what}</b>?</div>
      <div class="dim">${p.untracked ? 'it isn\'t in git: it goes to the trash' : 'a copy of it as it is goes to the trash first'}</div>
      <${Confirm} yes="discard" no="keep" danger onKey=${sc.key} />
    </div>`;
  }
  if (p.kind === 'drop') {
    return html`<div class="tree-prompt confirm">
      <div>drop <b>stash@{${p.index}}</b> ${p.subject}?</div>
      <${Confirm} yes="drop" no="keep" danger onKey=${sc.key} />
    </div>`;
  }
  if (p.kind === 'commitAll') {
    return html`<div class="tree-prompt confirm">
      <div>nothing staged: stage <b>every change (${p.count} file${p.count === 1 ? '' : 's'})</b> and commit?</div>
      <div class="dim">the new files too, as Commit all does</div>
      <${Confirm} yes="stage and commit" no="not now" onKey=${sc.key} />
    </div>`;
  }
  if (p.kind === 'abort') {
    return html`<div class="tree-prompt confirm">
      <div>abort the <b>${p.op}</b>?</div>
      <div class="dim">everything goes back as it was before it began: the conflicts you resolved too</div>
      <${Confirm} yes="abort" no="go on" danger onKey=${sc.key} />
    </div>`;
  }
  const label = p.kind === 'branch' ? 'new branch' + (p.from ? ' at ' + p.short : ' from HEAD, with your changes') : 'stash every change, the new files too: message (optional)';
  return html`<div class="tree-prompt">
    <div class="dim">${label}</div>
    <input ref=${input} value=${p.value} spellcheck="false" autocomplete="off"
      onInput=${(e) => { p.value = e.target.value; }}
      onKeyDown=${(e) => {
        if (e.key === 'Enter') { e.preventDefault(); sc.submitPrompt(); setFocus('tree'); }
        else if (e.key === 'Escape') { e.preventDefault(); sc.cancelPrompt(); setFocus('tree'); }
      }}
      onBlur=${() => { if (G.prompt === p) sc.cancelPrompt(); }} />
  </div>`;
}

function Filter() {
  const input = useRef(null);
  useLayoutEffect(() => { if (G.filtering && input.current && document.activeElement !== input.current) input.current.focus(); });
  if (!G.filtering && !G.filter) return null;
  return html`<div class="tree-filter">
    <span class="dim">/</span>
    <input ref=${input} value=${G.filter} spellcheck="false" autocomplete="off" readOnly=${!G.filtering}
      onInput=${(e) => sc.setFilter(e.target.value)}
      onKeyDown=${(e) => {
        if (e.key === 'Enter') { e.preventDefault(); sc.endFilter(true); }
        else if (e.key === 'Escape') { e.preventDefault(); sc.endFilter(false); }
        else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); sc.key(e.key === 'ArrowDown' ? 'down' : 'up'); redraw(); }
      }}
      onBlur=${() => { if (G.filtering) sc.endFilter(true); }} />
  </div>`;
}

// ---- the pane ----

// Branch is the header's branch and how it stands with its upstream, and
// the remote's buttons.
function Branch() {
  const st = G.st;
  if (!st || st.notGit) return null;
  const name = st.branch || '(detached ' + st.head + ')';
  return html`<div class="scm-branch">
    <span class="scm-branch-name" title=${st.upstream ? 'tracks ' + st.upstream : 'no upstream: P pushes it to origin'}>⎇ ${name}</span>
    ${st.ahead > 0 && html`<span class="scm-ahead" title=${st.ahead + ' to push'}>↑${st.ahead}</span>`}
    ${st.behind > 0 && html`<span class="scm-behind" title=${st.behind + ' to pull'}>↓${st.behind}</span>`}
    <span class="spacer"></span>
    <${Remote} busy="pulling" title="pull (p): fast-forward only" run=${sc.pull} icon="↓" />
    <${Remote} busy="pushing" title="push (P)" run=${sc.push} icon="↑" />
    <${Remote} busy="syncing" title="sync: pull, then push" run=${sc.sync} icon="⇅" />
    <${Remote} busy="fetching" title="fetch origin (f)" run=${sc.fetch} icon="⟳" />
    <button class="scm-icon" title="the git output: every command xf ran" onMouseDown=${act(sc.openOutput)}>≡</button>
  </div>`;
}

// Remote is a button that goes to origin: it spins while its command
// runs, and is dimmed while another git command does.
function Remote({ busy, title, run, icon }) {
  const mine = G.busy === busy;
  return html`<button class=${'scm-icon' + (mine ? ' running' : G.busy ? ' idle' : '')} disabled=${!!G.busy}
    title=${mine ? busy + '…' : title} onMouseDown=${act(run)}>${mine ? html`<span class="scm-spin"></span>` : icon}</button>`;
}

function Status() {
  if (G.busy) {
    return html`<div class="scm-status busy"><span class="scm-spin"></span><span>${G.busy}…</span><span class="spacer"></span>
      <button class="scm-icon" title="stop it" onMouseDown=${act(sc.cancelGit)}>✕</button></div>`;
  }
  if (G.failed) {
    const last = G.failed.trim().split('\n').filter(Boolean).pop() || G.failed;
    return html`<div class="scm-status failed" title="show the git output" onMouseDown=${act(sc.openOutput)}>
      <span>${last}</span><span class="spacer"></span><span class="dim">output ›</span></div>`;
  }
  return null;
}

function OpBanner() {
  const st = G.st;
  if (!st || !st.op) return null;
  const left = st.conflicts.length;
  return html`<div class="scm-op">
    <div><b>${st.op}</b> in progress${left ? html` · <span class="warn">${left} in conflict</span>` : ' · conflicts resolved'}</div>
    <div class="scm-op-buttons">
      <button class="run-btn" disabled=${left > 0} title=${left ? 'resolve and stage every conflict first' : 'finish the ' + st.op}
        onMouseDown=${act(sc.continueOp)}>${st.op === 'merge' ? 'Commit' : 'Continue'}</button>
      <button class="plain danger" title=${'abort the ' + st.op} onMouseDown=${act(sc.askAbort)}>Abort</button>
    </div>
  </div>`;
}

// ---- the merge request's region ----

// dockH is the region's height, the same in every project; dockKey keeps
// it in the browser.
const dockKey = 'xf-web:scm-dock';
const dockDefault = 240;
let dockH = (() => {
  try { return Number(localStorage.getItem(dockKey)) || dockDefault; } catch { return dockDefault; }
})();

// DockHead is the region's header bar (the s:mr row): the host and the
// project, its buttons, and folded the pill instead.
function DockHead({ r, cur, focused }) {
  const v = fg.F.v;
  const gh = v.kind === 'github';
  const p = !r.open && fg.pill();
  const where = v.host + (v.path ? '/' + v.path : '');
  return html`<div class=${'scm-row scm-dock-head' + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '')} title=${where}
      onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); sc.clickRow(r); }}>
    <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
    <span class=${gh ? 'gh-mark' : 'gl-mark'}></span>
    <span class="scm-dock-host">${gh ? 'GitHub' : 'GitLab'}</span>
    <span class="scm-dock-proj">${v.path || v.host}</span>
    <span class="spacer"></span>
    ${r.open
      ? html`<span class="scm-dock-btns">
          <button title=${'open the ' + fg.longWord() + ' (o)'} onMouseDown=${act(fg.openMR)}>↗</button>
          <button title=${'ask the host again (Git: refresh ' + fg.word() + ')'} onMouseDown=${act(fg.refresh)}>⟳</button></span>`
      : p && html`<span class=${'scm-dock-pill ' + p.tone + (p.stale ? ' stale' : '') + (p.going ? ' going' : '')} title=${p.title}>${p.text}</span>`}
  </div>`;
}

// Sash is the handle above the region: a drag sets its height, kept on
// release.
function Sash() {
  return html`<div class="scm-sash" title="drag to resize"
    onPointerDown=${(e) => {
      e.preventDefault();
      e.stopPropagation();
      const dock = e.currentTarget.nextElementSibling;
      const body = e.currentTarget.closest('.pane-body');
      if (!dock || !body) return;
      const y0 = e.clientY, h0 = dock.getBoundingClientRect().height;
      // As the region's max-height: git keeps at least 120px and 40%.
      const bh = body.getBoundingClientRect().height;
      const most = Math.max(Math.min(bh - 120, bh * 0.6), 90);
      document.body.classList.add('resizing');
      const move = (ev) => {
        dockH = Math.round(Math.min(Math.max(h0 - (ev.clientY - y0), 90), most));
        redraw();
      };
      const up = () => {
        document.body.classList.remove('resizing');
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
        try { localStorage.setItem(dockKey, String(dockH)); } catch { /* no storage */ }
      };
      window.addEventListener('pointermove', move);
      window.addEventListener('pointerup', up);
    }}></div>`;
}

export function ScmPane({ header, style }) {
  const focused = S.focus === 'tree';
  let body, dock = null;
  const st = G.st;
  if (!G.loaded && G.error) body = html`<div class="dim pad">${G.error}</div>`;
  else if (!G.loaded) body = html`<div class="dim pad">reading git…</div>`;
  else if (st.notGit) body = html`<div class="dim pad">not a git repository</div>`;
  else if (st.error) body = html`<div class="error pad">${st.error}</div>`;
  else {
    const rows = sc.rows();
    const row = (r) => html`<${Row} key=${r.key} r=${r} cur=${r.key === G.cursor} focused=${focused} />`;
    body = rows.filter((r) => !sc.inDock(r)).map(row);
    const head = rows.find((r) => r.key === 's:mr');
    if (head) {
      dock = head.open
        ? html`<${Sash} /><div class="scm-dock" style=${'height:' + dockH + 'px'}>
            ${row(head)}
            <div class="tree-list scm-list scm-dock-list">${rows.filter((r) => sc.inDock(r) && r !== head).map(row)}</div>
          </div>`
        : html`<div class="scm-dock folded">${row(head)}</div>`;
    }
  }
  const inDock = sc.inDock(sc.current());
  const note = !focused ? sc.scmKey + ' hides'
    : inDock ? 'o open · O pipeline · tab git · ? keys'
    : dock ? 'space stage · c commit · tab ' + fg.word() + ' · ? keys'
    : 'space stage · c commit · ? keys';
  return html`<section class=${'pane tree-pane scm-pane' + (focused ? ' focused' : '')} data-pane="tree" style=${style}
      onMouseDown=${() => { setFocus('tree'); redraw(); }}>
    ${header}
    <div class="pane-body">
      <${Branch} />
      <${Status} />
      <${OpBanner} />
      ${G.loaded && st && !st.notGit && html`<${CommitBox} />`}
      <${Filter} />
      ${G.prompt && html`<${Prompt} />`}
      <div class="tree-list scm-list">${body}</div>
      ${dock}
    </div>
    <footer class="pane-note">${note}</footer>
  </section>`;
}

// ---- the git output ----

export function GitOutput() {
  const list = G.output || [];
  const end = useRef(null);
  useLayoutEffect(() => { if (end.current) end.current.scrollIntoView({ block: 'end' }); }, [list.length]);
  return html`<div class="overlay-back" onMouseDown=${(e) => { if (e.target === e.currentTarget) sc.closeOutput(); }}>
    <div class="overlay pane focused wide git-output">
      <header class="pane-title"><span class="pane-name">git output<span class="dim"> · every command Source Control ran · esc closes</span></span></header>
      <div class="pane-body">
        ${!list.length && html`<div class="dim pad">no git commands yet</div>`}
        ${list.map((e) => html`<div class=${'git-out-entry' + (e.ok ? '' : ' failed')}>
          <div class="git-out-cmd"><span class="dim">${new Date(e.at).toLocaleTimeString()}</span> <span>$ ${e.args.join(' ')}</span>
            <span class="dim"> · ${e.took < 1 ? Math.round(e.took * 1000) + ' ms' : e.took.toFixed(1) + ' s'}</span>${e.ok ? '' : html` <span class="error">failed</span>`}</div>
          ${e.out && html`<pre class="git-out-text">${e.out}</pre>`}
        </div>`)}
        <div ref=${end}></div>
      </div>
    </div>
  </div>`;
}

export function gitOutputKey(k) {
  if (k === 'esc' || k === 'q' || k === 'enter') { sc.closeOutput(); return true; }
  return true;
}

hooks.gitOutputKey = gitOutputKey;
