// agent is for an agent driving the page in the browser preview: what the
// page shows as one small object (snapshot), steps through the page's
// own code paths (act), a wait for a state (waitFor) and the page log
// (log). It isn't in the first load: `const a = await xfAgent()` in the
// console loads it (diag.js). Every step is noted in the page log.
//
// act refuses what costs money or can't be undone (runs, pushes, deletes,
// discards, closing the project, quitting xf web) unless its opts say
// allowDangerous: the user's yes is the agent's to get first.

import { S, hooks, redraw, handleKey, keyName, selectAction, setFocus, openPalette, openScratchPalette, setPaletteQuery, paletteList, paletteSettled, pickFromPalette, leaveOverlay, paletteCommands, afterRedraw, toggles, running, rightShown } from './state.js';
import { diagLines, describe, note } from './diag.js';
import { SET } from './settings.js';
import { SC } from './scratch.js';
import { SRC } from './source.js';
import { T as TREE } from './tree.js';
import { G } from './scm.js';
import { F } from './search.js';
import { T as THEME } from './theme.js';
import { menuItems, pickMenuItem, closeMenu } from './ctx-menu.js';
import { vimOn } from './vim-mode.js';
import { exitKey, exitLevel } from './exit.js';

// ---- reading ----

// layer is what has the keys over the panes, the topmost first.
function layer() {
  if (menuItems()) return 'menu';
  if (THEME.pick) return 'theme';
  if (SET.open) return 'settings';
  if (S.overlay) return S.overlay;
  if (S.checks) return 'checks';
  if (S.inspector) return 'inspector';
  if (SC.open) return 'scratch';
  return '';
}

const label = (it) => it.title || it.path || it.name || it.id || it.key || '';

export function snapshot() {
  const v = S.view, st = S.state;
  return {
    mode: S.mode, project: S.key, layer: layer(), overlay: S.overlay, focus: S.focus,
    active: typeof document !== 'undefined' ? describe(document.activeElement) : '',
    selected: S.sel, view: v ? { id: v.id, name: v.name, file: v.file } : null, plainFile: S.plainFile,
    source: { shown: SRC.shown, file: SRC.file, dirty: SRC.dirty, stale: SRC.stale, loading: SRC.loading, editing: SRC.editing, inserting: SRC.inserting, conflict: !!SRC.conflict, error: SRC.error, vim: vimOn() },
    flags: toggles(), running: running(),
    panes: { zoomed: S.zoomed, pane2: S.mode === 'project' && rightShown(), lineage: S.lineageOpen, columns: S.cols.shown, log: S.showLog ? S.logTab : '', column: TREE.shown ? TREE.view : '' },
    flash: S.flash, compile: st ? { errors: st.errors || 0, warnings: st.warnings || 0, dryRunErrors: st.dryRunErrors || 0 } : null,
    palette: S.overlay === 'palette' ? { query: S.palette.query, cursor: S.palette.cursor, items: paletteList().slice(0, 20).map(label) } : null,
    menu: menuItems(),
    checks: S.checks, inspector: !!S.inspector,
    settings: SET.open ? { view: SET.view, tab: SET.tab, query: SET.query, closing: SET.closing } : null,
    scratch: SC.open ? { focus: SC.focus, cur: SC.cur && SC.cur.id, running: !!(SC.run && ['starting', 'running'].includes(SC.run.phase)), confirm: !!SC.confirm, delAsk: SC.delAsk, standalone: SC.standalone, flash: SC.flash } : null,
    theme: { choice: THEME.choice.theme, picking: !!THEME.pick },
    tree: { cursor: TREE.cursor, filter: TREE.filter, prompt: TREE.prompt ? TREE.prompt.kind : '' },
    scm: { cursor: G.cursor, busy: G.busy, failed: G.failed, prompt: G.prompt ? G.prompt.kind : '' },
    search: { query: F.query, busy: F.busy, error: F.error, prompt: !!F.prompt },
    commands: commandRows(),
    log: diagLines().slice(-10),
  };
}

// commandRows are the commands legal now; a list that throws is a page
// bug the snapshot names rather than throws.
function commandRows() {
  try {
    return legalCommands().map((c) => ({ title: c.title, key: c.key || '', state: c.state || '', dangerous: !!danger(c.title) }));
  } catch (err) {
    return [{ title: '', key: '', state: 'the commands threw: ' + ((err && err.message) || err), dangerous: false }];
  }
}

export const log = (n = 50) => diagLines().slice(-n);

// ---- what act refuses ----

// risky are commands and menu rows that cost money or can't be undone.
const risky = [
  [/^Run( with|$)|^Run: query/, 'runs in the warehouse'],
  [/full refresh/i, 'a full refresh rebuilds tables'],
  [/^(Databricks|Snowflake): check the model/, 'starts the warehouse'],
  [/^Defer to prod: remove/, 'drops the proxy views'],
  [/^History: clear/, 'clears the kept runs'],
  [/remove (this )?worktree|^Remove Worktree/i, 'removes a worktree'],
  [/discard|delete|trash|^Buffers: remove|^Revert Change|undo the last replace/i, 'loses changes or files'],
  [/^Git: (commit|amend|push|pull|sync|stash|abort|continue|create)/, 'changes the repository or its remote'],
  [/^Close (project|scratch|Project)/, 'closes the project'],
  [/^Terminal: close/, 'stops a shell and what runs in it'],
];
export const danger = (title) => (risky.find(([re]) => re.test(title)) || [, ''])[1];

const confirms = ['confirmQuit', 'confirmClose', 'confirmClear', 'confirmFull', 'confirmRemoveWorktree', 'prod', 'sourceConflict', 'deferAsk'];

// keyDanger is what the key k (keyName's) would do that act refuses, here
// and now; '' for none.
function keyDanger(k, e) {
  const el = typeof document !== 'undefined' && document.activeElement;
  if (el && el.closest && el.closest('.xterm')) return 'the terminal has the keys';
  // ctrl+q goes back one level: only the last closes the project.
  if (e.ctrlKey && !e.metaKey && e.key.toLowerCase() === 'q') return exitLevel() === 'project' ? 'ctrl+q closes the project' : '';
  const enter = k === 'enter' || k === 'y';
  if (S.mode === 'picker') {
    if (S.pickerConfirm && k === 'y') return 'closes or removes a project';
    if (k === 'Delete' || k === 'shift+Delete') return 'closes or removes a project';
    if (k === 'ctrl+c' && S.overlay !== 'projects') return 'quits xf web';
    return '';
  }
  if (confirms.includes(S.overlay)) return ['esc', 'n', 'ctrl+c'].includes(k) ? '' : 'answers ' + S.overlay;
  if ((S.overlay === 'changed' || S.overlay === 'tags') && (enter || k === 'r')) return 'runs in the warehouse';
  if ((e.ctrlKey || e.metaKey) && k === 'enter') return 'runs a query in the warehouse';
  if (SC.open && (SC.confirm || SC.delAsk) && enter) return 'runs or deletes a scratch';
  if (S.overlay || SC.open || SET.open || S.checks || S.inspector) return '';
  if ((TREE.prompt && TREE.prompt.kind === 'delete') || G.prompt || F.prompt) return enter ? 'answers a prompt that changes files' : '';
  if (S.focus === 'tree' && TREE.view === 'scm' && ['A', 'P', 'p'].includes(k)) return 'changes the repository or its remote';
  if (S.focus === 'source' || S.focus === 'tree') return ''; // r and K are the panes' own there
  if (k === 'r' && S.caps.run) return 'runs in the warehouse';
  if (k === 'K' && S.caps.check) return 'starts the SQL warehouse';
  return '';
}

// ---- doing ----

function legalCommands() {
  if (S.mode !== 'project' || SET.open || S.checks || S.inspector || THEME.pick) return [];
  if (S.overlay && S.overlay !== 'palette') return [];
  if (SC.open) return hooks.scratchCommands ? hooks.scratchCommands() : [];
  return paletteCommands();
}

// find is the one of list whose label is q, or alone holds it.
function find(list, q) {
  const low = String(q).toLowerCase();
  const exact = list.filter((x) => x.toLowerCase() === low);
  const some = exact.length ? exact : list.filter((x) => x.toLowerCase().includes(low));
  if (some.length === 1) return { i: list.indexOf(some[0]) };
  return { reason: some.length ? `"${q}" is ambiguous` : `nothing matches "${q}"`, candidates: (some.length ? some : list).slice(0, 15) };
}

const names = { esc: 'Escape', enter: 'Enter', tab: 'Tab', backspace: 'Backspace', up: 'ArrowUp', down: 'ArrowDown', left: 'ArrowLeft', right: 'ArrowRight', home: 'Home', end: 'End', pgup: 'PageUp', pgdown: 'PageDown', space: ' ', del: 'Delete', delete: 'Delete' };
const codes = { '`': 'Backquote', ',': 'Comma', '.': 'Period', '/': 'Slash', ';': 'Semicolon', "'": 'Quote', '[': 'BracketLeft', ']': 'BracketRight', '\\': 'Backslash', '-': 'Minus', '=': 'Equal', ' ': 'Space' };

// keyInit reads "ctrl+shift+p", "cmd+s", "G", "esc" into a KeyboardEvent's
// fields, code too, which main.js reads for some keys.
export function keyInit(spec) {
  if (typeof spec === 'object') return { key: '', code: '', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...spec };
  const parts = spec.length > 1 ? spec.split('+') : [spec];
  let key = parts.pop() || '+';
  const mods = new Set(parts.map((m) => m.toLowerCase()));
  key = names[key.toLowerCase()] || key;
  const shiftKey = mods.has('shift') || (key.length === 1 && key !== key.toLowerCase());
  if (shiftKey && key.length === 1) key = key.toUpperCase();
  const code = /^[a-z]$/i.test(key) ? 'Key' + key.toUpperCase() : /^\d$/.test(key) ? 'Digit' + key : codes[key] || key;
  return { key, code, shiftKey, ctrlKey: mods.has('ctrl'), metaKey: mods.has('cmd') || mods.has('meta'), altKey: mods.has('alt') || mods.has('opt') };
}

// pressKey sends the key as the browser would, through main.js's
// handlers; where there is no browser (node), to handleKey.
function pressKey(e) {
  if (typeof KeyboardEvent !== 'undefined' && typeof document !== 'undefined' && document.dispatchEvent) {
    const ev = new KeyboardEvent('keydown', { ...e, bubbles: true, cancelable: true });
    (document.activeElement || document.body).dispatchEvent(ev);
    return { prevented: ev.defaultPrevented };
  }
  const used = handleKey(keyName(e));
  redraw();
  return { used };
}

function pick(list, at, opts) {
  const { i, reason, candidates } = typeof at === 'number' ? { i: at } : find(list.map(label), at);
  if (i === undefined || !list[i]) return { reason: reason || `no row ${at}`, candidates };
  const why = danger(label(list[i]));
  if (why && !opts.allowDangerous) return { blocked: true, reason: `${label(list[i])}: ${why}` };
  return { i, picked: label(list[i]) };
}

const ops = {
  key(spec, opts) {
    const e = keyInit(spec);
    const why = keyDanger(keyName(e), e);
    if (why && !opts.allowDangerous) return { blocked: true, reason: why };
    return pressKey(e);
  },
  command(title, opts) {
    const cmds = legalCommands();
    if (!cmds.length) return { reason: 'no commands here: ' + (layer() || S.mode) };
    const p = pick(cmds, title, opts);
    if (p.i === undefined) return p;
    // As a pick in the palette: its use is counted, and the keys go back
    // as they do after one.
    if (SC.open) openScratchPalette(); else openPalette('>');
    const i = paletteList().findIndex((c) => c.title === p.picked);
    pickFromPalette(i);
    redraw();
    return { picked: p.picked };
  },
  async palette(args = {}, opts) {
    const { query, pick: at } = typeof args === 'string' ? { query: args } : args;
    if (S.mode !== 'project' || SET.open || S.checks || S.inspector) return { reason: 'no palette here: ' + (layer() || S.mode) };
    if (S.overlay !== 'palette') {
      if (SC.open) { openScratchPalette(); if (query !== undefined) setPaletteQuery(query); } else openPalette(query || '');
      if (S.overlay !== 'palette') { redraw(); return { reason: S.flash || 'the palette did not open' }; }
    } else if (query !== undefined) setPaletteQuery(query);
    await paletteSettled();
    redraw();
    if (at === undefined) return {};
    const p = pick(paletteList(), at, opts);
    if (p.i === undefined) return p;
    pickFromPalette(p.i);
    redraw();
    return { picked: p.picked };
  },
  selectAction(id) {
    if (S.mode !== 'project') return { reason: 'no project open' };
    selectAction(id);
    return S.sel === id ? {} : { reason: S.flash || 'not selected' };
  },
  async openFile(args) {
    const { path, ...o } = typeof args === 'string' ? { path: args } : args;
    if (!hooks.openFile) return { reason: 'no file tree here' };
    await hooks.openFile(path, o);
    redraw();
    return {};
  },
  focus(pane) {
    if (S.mode !== 'project') return { reason: 'no project open' };
    setFocus(pane);
    redraw();
    return S.focus === pane ? {} : { reason: S.flash || 'the keys went to ' + S.focus };
  },
  // closeOverlay closes the topmost layer as its esc or ctrl+q does, and
  // never the project.
  closeOverlay() {
    const was = layer();
    if (was === 'menu') closeMenu();
    else if (S.overlay) { leaveOverlay(); redraw(); }
    else if (!was || (SC.open && SC.standalone)) return { reason: was ? 'closing the standalone Scratch closes it all' : 'nothing to close' };
    else exitKey();
    return { closed: was };
  },
  pickMenu(at, opts) {
    const items = menuItems();
    if (!items) return { reason: 'no menu open' };
    const p = pick(items.map((it) => ({ ...it, title: it.label })), at, opts);
    if (p.i === undefined) return p;
    if (items[p.i].off) return { reason: `${p.picked}: ${items[p.i].off}` };
    pickMenuItem(p.i);
    return { picked: p.picked };
  },
};

const brief = (v) => (v === undefined ? '' : (typeof v === 'string' ? v : JSON.stringify(v)).slice(0, 80));

// act does op with args, through the page's own code, and answers {ok}
// with what it did and the snapshot after the draw it caused; blocked
// says it was refused as dangerous.
export async function act(op, args, opts = {}) {
  let r;
  if (!ops[op]) r = { reason: `no op "${op}": ${Object.keys(ops).join(', ')}` };
  else {
    try { r = (await ops[op](args, opts || {})) || {}; } catch (err) { r = { reason: String((err && err.message) || err) }; }
  }
  const ok = !r.reason;
  note(`agent ${op} ${brief(args)} → ${ok ? 'ok' : (r.blocked ? 'blocked: ' : 'failed: ') + r.reason}`);
  await new Promise((res) => setTimeout(res, 0)); // the draw it asked for
  return { ok, op, ...r, blocked: !!r.blocked, snapshot: snapshot() };
}

// ---- waiting ----

// waitFor resolves with the snapshot once predicate holds of it, checking
// after each draw and every 100ms (a hidden preview draws nothing until
// it shows); it rejects after timeoutMs saying where the page was.
export function waitFor(predicate, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    let done = false, last, err;
    const end = () => { done = true; off(); clearInterval(tick); clearTimeout(limit); };
    const check = () => {
      if (done) return;
      last = snapshot();
      try { if (!predicate(last)) return; } catch (e) { err = e; return; }
      end();
      resolve(last);
    };
    const off = afterRedraw(check);
    const tick = setInterval(check, 100);
    const limit = setTimeout(() => {
      end();
      const s = last || snapshot();
      reject(new Error(`waitFor: not after ${timeoutMs}ms (mode ${s.mode}, layer ${s.layer || 'none'}, focus ${s.focus}, flash "${s.flash}")` + (err ? `; the predicate threw: ${err.message}` : '')));
    }, timeoutMs);
    check();
  });
}
