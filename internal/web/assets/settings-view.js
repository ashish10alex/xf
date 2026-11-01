// The settings page (see settings.js) and the gear that opens it. Its
// Overview is the project's info (or, outside one, the tools' and
// Google's): a line of how things stand, then the rows as a list of
// labels and values, as the Dataform Tools extension's Project tab lays
// them out. Its Settings have a header with the scope (Global / Project)
// and the view (UI / JSON), a search bar, a sidebar of sections that
// follows the scroll, and a row per setting: its title and what it does
// on the left, its field and where its value comes from on the right; or
// the scope's file in the shared editor.

import { h } from './vendor/preact.mjs';
import { useState, useEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import * as cm from './vendor/codemirror.mjs';
import { S, redraw, copy } from './state.js';
import { createEditor } from './editor.js';
import * as se from './settings.js';
import { GitHosting } from './forge-view.js';

const html = htm.bind(h);
const { SET } = se;

const gear = html`<svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true"><path fill="currentColor" d="M19.4 13a7.6 7.6 0 0 0 0-2l2.1-1.6a.5.5 0 0 0 .1-.6l-2-3.5a.5.5 0 0 0-.6-.2l-2.5 1a7.3 7.3 0 0 0-1.7-1l-.4-2.6a.5.5 0 0 0-.5-.5h-4a.5.5 0 0 0-.5.4l-.4 2.7a7.3 7.3 0 0 0-1.7 1l-2.5-1a.5.5 0 0 0-.6.2l-2 3.5a.5.5 0 0 0 .1.6L4.6 11a7.6 7.6 0 0 0 0 2l-2.1 1.6a.5.5 0 0 0-.1.6l2 3.5c.1.2.4.3.6.2l2.5-1c.5.4 1.1.7 1.7 1l.4 2.6c0 .3.2.5.5.5h4c.3 0 .5-.2.5-.4l.4-2.7c.6-.3 1.2-.6 1.7-1l2.5 1c.2.1.5 0 .6-.2l2-3.5a.5.5 0 0 0-.1-.6L19.4 13ZM12 15.5a3.5 3.5 0 1 1 0-7 3.5 3.5 0 0 1 0 7Z"/></svg>`;
const lens = html`<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="2" d="M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13Zm4.7-1.8L20 20"/></svg>`;

// GearButton opens the settings page.
export function GearButton({ cls }) {
  return html`<button class=${'gear ' + (cls || '')} title="Settings (ctrl+,)" aria-label="Settings" onClick=${() => se.openSettings()}>${gear}</button>`;
}

const keyList = (list) => html`<span class="keys">${list.map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span>`;

// The sections' ids, for the sidebar's links and the scroll-spy.
const secId = (s) => 'set-sec-' + s.toLowerCase().replace(/[^a-z]+/g, '-');
const plural = (n, one) => n + ' ' + one + (n === 1 ? '' : 's');

// ---- the Overview ----

// infoGroups are the info rows by section, but the settings', which the
// Settings show.
export function infoGroups(info) {
  const groups = [];
  let skip = false;
  for (const r of info || []) {
    if (r.title) {
      skip = r.title === 'settings';
      if (!skip) groups.push({ title: r.title, rows: [] });
      continue;
    }
    if (skip) continue;
    if (!groups.length) groups.push({ title: '', rows: [] });
    groups[groups.length - 1].rows.push(r);
  }
  return groups;
}

// statusItems sum the info rows up for the Overview's top line: each
// tool and its version, the Google sign-in, the git branch and the
// editor, each ok, warn, wait (its lookup still going) or off.
export function statusItems(info) {
  const groups = infoGroups(info);
  const group = (t) => groups.find((g) => g.title === t);
  const out = [];
  const bin = group('binaries');
  if (bin) {
    bin.rows.forEach((r, i) => {
      if (!r.label || r.label === 'xf') return;
      const next = bin.rows[i + 1];
      const version = next && !next.label ? next.value || '' : '';
      if (!r.value || /^\(not (found|on PATH)\)/.test(r.value)) {
        out.push({ state: r.warn ? 'warn' : 'off', title: r.label + ' not found', note: '' });
      } else if (version === '…') {
        out.push({ state: 'wait', title: r.label, note: 'reading its version…' });
      } else {
        // The number alone: a tool's version line can name it again
        // ("dbt 2.0.8", "Google Cloud SDK 571.0.0").
        const num = /\d+(\.\d+)+/.exec(version);
        const unknown = version.startsWith('(unknown)');
        out.push({ state: r.warn || unknown ? 'warn' : 'ok', title: num && !unknown ? r.label + ' ' + num[0] : r.label, note: '', tip: r.note || '' });
      }
    });
  }
  const cloud = group('google cloud');
  if (cloud) {
    const lead = cloud.rows.find((r) => r.label === 'xf uses') || cloud.rows.find((r) => r.label === 'gcloud');
    if (lead && lead.value === '…') out.push({ state: 'wait', title: 'Google sign-in', note: 'checking…' });
    else if (lead) {
      const account = lead.value.replace(/\s*\(.*\)$/, '');
      out.push(cloud.rows.some((r) => r.warn)
        ? { state: 'warn', title: 'Google sign-in needs attention', note: account }
        : { state: 'ok', title: 'Signed in', note: account });
    }
  }
  const git = group('git');
  const branch = git && git.rows.find((r) => r.label === 'branch');
  if (branch) {
    const base = git.rows.find((r) => r.label === 'compares with');
    if (branch.value === '…') out.push({ state: 'wait', title: 'git', note: 'checking…' });
    else if (branch.value.startsWith('(not a git')) out.push({ state: 'off', title: 'Not a git repository', note: '' });
    else out.push({ state: 'ok', title: branch.value, note: base ? 'compares with ' + base.value : '' });
  }
  const editor = group('editor');
  const conn = editor && editor.rows.find((r) => r.label === 'connected');
  if (conn) {
    const opens = editor.rows.find((r) => r.label === 'o opens with');
    out.push(conn.value === 'no'
      ? { state: 'off', title: 'No editor connected', note: opens ? 'o opens ' + opens.value : '' }
      : { state: 'ok', title: conn.value === 'yes' ? 'Editor connected' : 'Editor: ' + conn.value, note: conn.note || '' });
  }
  return out;
}

// groupHead is a section's title as a heading, and the words after its
// comma as a note: "config files, lowest precedence first".
const HEADS = { 'google cloud': 'Google Cloud' };
function groupHead(title) {
  const [head, ...rest] = title.split(', ');
  return [HEADS[head] || head.charAt(0).toUpperCase() + head.slice(1), rest.join(', ')];
}

const isPath = (v) => /^(~|\/|[A-Za-z]:\\|\\\\|\.\.?\/|git@|[a-z+]+:\/\/)/.test(v || '');

// InfoValue is a row's value: a path copies on a click, and a command to
// run (a sign-in's) copies too.
function InfoValue({ r }) {
  const v = r.value || '';
  const run = /^run `(.+)`$/.exec(v);
  if (run) {
    return html`<span class="ov-v">run <button class="auth-cmd" title="Copy the command" onClick=${() => copy(run[1], 'copied: ' + run[1])}><code>${run[1]}</code></button></span>`;
  }
  const cls = 'ov-v' + (r.warn ? ' warn' : '') + (v === '…' ? ' dim' : '');
  // A span, not a button, so a long path wraps and its note follows it.
  if (isPath(v)) {
    const doCopy = () => copy(v, 'copied: ' + v);
    return html`<span class=${cls + ' ov-path'} role="button" tabindex="0" title="Copy the path" onClick=${doCopy}
      onKeyDown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); doCopy(); } }}>${v}</span>`;
  }
  return html`<span class=${cls}>${v}</span>`;
}

function InfoGroup({ g }) {
  const [head, note] = groupHead(g.title);
  return html`<section class="ov-group">
    ${g.title && html`<h3>${head}${note && html`<span class="ov-h-note">${note}</span>`}</h3>`}
    <div class="ov-rows">${g.rows.map((r) => html`<div class=${'ov-row' + (r.label ? ' lead' : '')}>
      <span class="ov-label">${r.label}</span>
      <span class="ov-val"><${InfoValue} r=${r} />${r.note && html`<span class=${'ov-note' + (r.warn ? ' warn' : '')}>${r.note}</span>`}</span>
    </div>`)}</div>
  </section>`;
}

// changedLink is the Overview's word on the settings the files set,
// which shows them.
function ChangedLink() {
  if (!SET.data) return null;
  const user = se.changedCount('user');
  const project = se.hasProject() ? se.changedCount('project') : 0;
  const text = !user && !project ? 'No settings changed'
    : !project ? plural(user, 'setting') + ' set in your file'
    : !user ? plural(project, 'setting') + ' set in the project\'s file'
    : plural(user, 'setting') + ' set in your file, ' + project + ' in the project\'s';
  return html`<button class="ov-st ov-changed" onClick=${() => {
    if (user || project) {
      if (!se.changedCount() && se.hasProject()) se.setTab(SET.tab === 'user' ? 'project' : 'user');
      se.setOnly('changed');
    }
    se.setView('settings');
  }}>${text} →</button>`;
}

function Overview() {
  const d = SET.data;
  const items = statusItems(SET.info);
  return html`<div class="ov">
    <div class="ov-status">
      ${!SET.info ? html`<span class="dim">reading…</span>` : items.map((it) => html`<span class=${'ov-st ' + it.state} title=${it.tip || it.note}>
        <i class="ov-dot"></i><b>${it.title}</b>${it.note && html`<span class="dim">${it.note}</span>`}</span>`)}
      <${ChangedLink} />
    </div>
    <${AttentionWarning} />
    ${d && d.pending.length > 0 && html`<div class="set-banner">${plural(d.pending.length, 'change')} apply when the project reopens: ${d.pending.join(', ')}
      <button class="run-btn" disabled=${SET.reopening} onClick=${() => se.reopen()}><kbd>R</kbd> ${SET.reopening ? 'reopening…' : 'reopen now'}</button></div>`}
    ${SET.info && !SET.info.length && html`<div class="dim pad">no info to show</div>`}
    <div class="ov-groups">${infoGroups(SET.info).map((g) => html`<${InfoGroup} g=${g} />`)}</div>
  </div>`;
}

// ---- the Settings ----

// sourceOf says where the value in use of k comes from, from the scope's
// point of view; warn when a command-line flag overrides the files.
function sourceOf(k) {
  if (k.flag !== null) return { text: k.from + ' flag, for this run', warn: true };
  const here = se.level(), user = se.levelOf('user'), project = se.levelOf('project');
  if (here && k.from === here.path) return { text: 'this file' };
  if (user && k.from === user.path) return { text: 'from the global file' };
  if (project && k.from === project.path) return { text: 'from the project file, over this one' };
  if (!k.from || k.from.startsWith('default')) return { text: k.from || 'default' };
  return { text: 'from ' + k.from };
}

// inUse says whether k's value in use is one to show in an empty field:
// not "(none)" or "(not found)".
const inUse = (k) => !!k.value && !/^\((none|not found)\)$/.test(k.value);

// Source is the line under a row's field: where its value comes from,
// and, when the scope's file sets it, the default and reset.
function Source({ k, i }) {
  const fv = se.fileValue(k);
  const lock = se.locked(k);
  if (SET.tab === 'project' && k.userOnly) {
    return html`<div class="set-src">Global only · <button class="link" onClick=${() => {
      se.setTab('user'); const n = se.rows().findIndex((r) => r.key === k.key); SET.cursor = Math.max(n, 0); SET.reveal = true; redraw();
    }}>switch</button></div>`;
  }
  const src = sourceOf(k);
  if (fv === null) return html`<div class=${'set-src' + (src.warn ? ' warn' : '')}>${src.text}</div>`;
  return html`<div class="set-src">
    ${src.warn ? html`<span class="warn">${src.text}</span><span> · </span>` : ''}
    <span class="set-src-file">● this file</span><span> · default ${k.default || 'none'}</span>
    ${!lock && html`<span> · </span><button class="link" title=${'take ' + k.key + ' out of this file (x)'}
      onClick=${() => { SET.cursor = i; se.save(k, '', true); }}>reset</button>`}
  </div>`;
}

// Control is the row's field: chips for a short list of choices, a
// dropdown for a long one, else an input. Set in no file, it shows the
// value in use, dimmed.
function Control({ k, i }) {
  const lock = se.locked(k);
  const fv = se.fileValue(k);
  const draft = se.draftOf(k);
  if (k.kind === 'choice' || k.kind === 'bool') {
    const choices = k.kind === 'bool' ? ['true', 'false'] : k.choices;
    if (choices.length > 5) {
      return html`<select class="set-select" disabled=${!!lock} value=${fv !== null ? fv : ''}
        onFocus=${() => { SET.cursor = i; redraw(); }}
        onChange=${(e) => { SET.cursor = i; if (e.currentTarget.value) se.save(k, e.currentTarget.value); }}>
        ${fv === null && html`<option value="" disabled>${inUse(k) ? k.value : 'not set'}</option>`}
        ${choices.map((c) => html`<option value=${c}>${c}</option>`)}
      </select>`;
    }
    return html`<span class="set-choices">${choices.map((c) => html`<button class=${'chip' + (fv === c ? ' on' : fv === null && k.value === c ? ' inuse' : '')} disabled=${!!lock}
      onClick=${() => { SET.cursor = i; se.save(k, c); }}>${c}</button>`)}</span>`;
  }
  return html`<input class="set-input" value=${draft !== undefined ? draft : fv !== null ? fv : ''} disabled=${!!lock}
    inputmode=${k.kind === 'number' ? 'decimal' : 'text'} spellcheck="false" autocomplete="off" autocapitalize="off"
    placeholder=${lock ? '' : inUse(k) ? k.value : 'not set · ' + k.hint}
    ref=${(e) => { if (e && SET.editing === k.key && document.activeElement !== e) { e.focus(); e.select(); } }}
    onFocus=${() => { SET.editing = k.key; SET.cursor = i; redraw(); }}
    onInput=${(e) => { se.setDraft(k, e.currentTarget.value); }}
    onBlur=${() => { if (SET.editing === k.key) SET.editing = ''; se.commit(k); }}
    onKeyDown=${(e) => {
      if (e.key === 'Enter') { e.preventDefault(); e.currentTarget.blur(); }
      if (e.key === 'Escape') { e.preventDefault(); delete SET.drafts[SET.tab + ':' + k.key]; SET.editing = ''; e.currentTarget.blur(); redraw(); }
    }} />`;
}

// pyreflyNote is the line under the Python language server's doc: the
// pyrefly config the project uses, or that there is none, and the
// interpreter. null while unknown, or with the server off.
export function pyreflyNote(st) {
  if (!st || st.off) return null;
  const config = st.config || '';
  const inProject = !!config && !/^(\/|[A-Za-z]:)/.test(config);
  return {
    config,
    inProject,
    // pyrefly reports no type errors without a config; with no pyrefly it can't be written for.
    canCreate: !config && !st.missing,
    interpreter: st.label || st.interpreter || '',
    where: st.where || '',
    missing: st.missing || '',
  };
}

function PyreflyNote() {
  const n = pyreflyNote(SET.pyrefly);
  if (!n) return '';
  return html`<div class="set-doc set-py">
    ${n.config
      ? html`<span>config: </span>${n.inProject
        ? html`<button class="link" title="Open it in the source pane" onClick=${() => se.openPyreflyConfig()}><code>${n.config}</code></button>`
        : html`<code>${n.config}</code>`}`
      : html`<span>config: none (pyrefly reports no type errors without one) · </span>${n.canCreate && html`<button class="link" onClick=${() => se.createPyreflyConfig()}>create pyrefly.toml</button>`}`}
    ${n.interpreter && html`<span> · interpreter: <code>${n.interpreter}</code>${n.where ? ' (' + n.where + ')' : ''}, kept in the project's session</span>`}
    ${n.missing && html`<div class="warn">${n.missing}</div>`}
  </div>`;
}

function Row({ k, i }) {
  const fv = se.fileValue(k);
  const lock = se.locked(k);
  const err = se.errorOf(k);
  const pending = SET.data.pending.includes(k.key);
  const notFound = k.value === '(not found)';
  return html`<div class=${'set-row' + (i === SET.cursor ? ' sel' : '') + (fv !== null ? ' chg' : '') + (lock ? ' locked' : '') + (notFound ? ' warn-row' : '')}
      data-key=${k.key}
      ref=${(e) => {
        if (e && i === SET.cursor && SET.reveal && !SET.editing) {
          SET.reveal = false;
          const main = i === 0 && e.closest('.set-main');
          if (main) main.scrollTop = 0;
          else e.scrollIntoView({ block: 'nearest' });
        }
      }}
      onMouseDown=${() => { if (SET.cursor !== i) { SET.cursor = i; redraw(); } }}>
    <div class="set-row-text">
      <div class="set-title">
        <span class="set-name">${k.title || k.key}</span>
        ${notFound && html`<span class="set-tag warn">not found</span>`}
        ${!k.live && html`<span class=${'set-tag' + (pending ? ' warn' : '')} title="applies when the project reopens">${pending ? 'applies on reopen' : 'on reopen'}</span>`}
        <code class=${'set-key' + (notFound ? ' warn' : '')}>${k.key}</code>
      </div>
      <div class="set-doc">${k.doc}</div>
      ${k.key === 'pythonLanguageServer' && html`<${PyreflyNote} />`}
      ${err && html`<div class="set-err">${err}</div>`}
    </div>
    <div class="set-ctl">
      <div class="set-field">
        <${Control} k=${k} i=${i} />
        ${k.key === 'prodCompilerOptions' && !lock && html`<button class=${'chip' + (fv === '' ? ' on' : '')} title="Write an empty value: the default targets are prod"
          onClick=${() => { SET.cursor = i; se.setEmpty(k); }}>default targets are prod</button>`}
      </div>
      <${Source} k=${k} i=${i} />
    </div>
  </div>`;
}

// attentionItems finds the items in info and settings keys that need user attention
// (missing tool binaries, accounts needing reauthentication, or failing credentials).
export function attentionItems(info, warnings = [], keys = []) {
  const out = [];

  // Check missing binaries for the active backend/settings (e.g. dataformBin, dbtBin)
  if (keys && keys.length) {
    const activeBackend = S.boot && S.boot.backend ? S.boot.backend : (S.mode === 'project' ? 'dataform' : '');
    for (const k of keys) {
      if (k.value === '(not found)') {
        const isDf = k.key === 'dataformBin';
        const isDbt = k.key === 'dbtBin';
        if (!isDf && !isDbt) continue;
        if (activeBackend === 'dbt' && isDf) continue;
        if (activeBackend === 'dataform' && isDbt) continue;
        if (!activeBackend && isDbt && (!S.picker || !S.picker.some((p) => p.backend === 'dbt'))) continue;

        const cmdMatch = k.from && k.from.match(/(npm i(?: -g)? [^\s,)]+|brew install [^\s,)]+|pip install [^,)]+)/);
        const cmd = cmdMatch ? cmdMatch[1] : '';
        const tool = isDf ? 'dataform' : 'dbt';
        out.push({
          message: `${tool}: could not find "${tool}" on PATH`,
          command: cmd,
          note: `Install ${tool} CLI, or set ${k.key} in settings`,
          targetKey: k.key,
        });
      }
    }
  }

  if (info) {
    for (let i = 0; i < info.length; i++) {
      const r = info[i];
      if (!r.warn) continue;
      // Don't duplicate config file warnings that d.warnings already shows.
      if (warnings.some((w) => w.includes(r.value) || r.value.includes(w))) continue;
      if (out.some((o) => (r.label && o.message.includes(r.label)) || (r.value && o.message.includes(r.value)))) continue;

      if (r.value && r.value.startsWith('run `')) {
        const cmd = r.value.replace(/^run `|`$/g, '');
        const prev = out[out.length - 1];
        if (prev && !prev.command) {
          prev.command = cmd;
          if (r.note) prev.note = r.note;
          continue;
        }
        out.push({ message: r.value, command: cmd, note: r.note });
        continue;
      }
      let cmd = '';
      if (r.note) {
        const m = r.note.match(/(npm i(?: -g)? [^\s,)]+|brew install [^\s,)]+|pip install [^,)]+)/);
        if (m) cmd = m[1];
      }
      const msg = (r.label ? r.label + ': ' : '') + r.value;
      out.push({ message: msg, command: cmd, note: r.note, label: r.label, value: r.value });
    }
  }
  return out;
}

// AttentionWarning highlights accounts needing reauthentication, missing binaries,
// or credentials needing user attention at the top of the settings page.
function AttentionWarning() {
  const [copiedCmd, setCopiedCmd] = useState('');
  const d = SET.data;
  const items = attentionItems(SET.info, d ? d.warnings : [], d ? d.keys : []);
  // Until the info's Google rows land, the picker's sign-in warnings stand in.
  const cloudPending = !SET.info || SET.info.some((r) => r.value === '…' && (r.label === 'xf uses' || r.label === 'gcloud'));
  const alerts = [...items, ...(cloudPending && S.pickerAuth ? S.pickerAuth : [])];
  if (!alerts || !alerts.length) return null;
  const doCopy = (cmd) => {
    const sel = window.getSelection && window.getSelection().toString();
    if (sel && sel.length > 0) return;
    copy(cmd, 'copied: ' + cmd);
    setCopiedCmd(cmd);
    setTimeout(() => setCopiedCmd(''), 1800);
  };
  return html`<div class="auth-warn set-attention" role="alert">
    ${alerts.map((a) => {
      const isCopied = copiedCmd === a.command;
      return html`<div class="auth-line">
        <span class="auth-msg">⚠ ${a.message}</span>
        ${a.command && html`<span class="auth-run">run <span class=${'auth-cmd' + (isCopied ? ' copied' : '')} role="button" tabindex="0" title="Click to copy, or select text"
          onClick=${() => doCopy(a.command)}
          onKeyDown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); doCopy(a.command); } }}><code>${isCopied ? '✓ copied' : a.command}</code></span></span>`}
        ${a.note && html`<span class="dim auth-note">${a.note}</span>`}
      </div>`;
    })}
    ${se.viewShown() === 'settings' && se.hasInfo() && html`<button class="link auth-more" onClick=${() => se.setView('overview')}>details in the overview</button>`}
  </div>`;
}

// ---- the sidebar and its scroll-spy ----

// spyUntil holds the highlight on a section the sidebar scrolls to while
// the smooth scroll runs, so it doesn't flick through the ones passed.
let spyUntil = 0;

function goTo(s) {
  SET.section = s;
  spyUntil = Date.now() + 700;
  const el = document.getElementById(secId(s));
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  redraw();
}

// spy sets the section scrolled to: the last whose heading is in the top
// quarter of the cards or above it (or the last, at the bottom of the
// page).
function spy(main, names) {
  if (Date.now() < spyUntil) return;
  const top = main.getBoundingClientRect().top + main.clientHeight / 4;
  let at = names[0] || '';
  for (const s of names) {
    const el = document.getElementById(secId(s));
    if (el && el.getBoundingClientRect().top <= top) at = s;
  }
  if (main.scrollTop > 0 && main.scrollTop + main.clientHeight >= main.scrollHeight - 2 && names.length) {
    const last = names.filter((s) => document.getElementById(secId(s))).pop();
    const el = last && document.getElementById(secId(last));
    if (el && el.getBoundingClientRect().top < main.getBoundingClientRect().bottom - 40) at = last;
  }
  if (at !== SET.section) { SET.section = at; redraw(); }
}

// useSpy watches the section headings: crossing the top of the cards
// moves the highlight.
function useSpy(main, names) {
  const key = names.join('|');
  useEffect(() => {
    const el = main.current;
    if (!el || typeof IntersectionObserver === 'undefined') return undefined;
    const io = new IntersectionObserver(() => spy(el, names), { root: el, rootMargin: '0px 0px -75% 0px', threshold: [0, 1] });
    for (const s of names) { const h2 = document.getElementById(secId(s)); if (h2) io.observe(h2); }
    const onScroll = () => { if (el.scrollTop + el.clientHeight >= el.scrollHeight - 2) spy(el, names); };
    el.addEventListener('scroll', onScroll, { passive: true });
    spy(el, names);
    return () => { io.disconnect(); el.removeEventListener('scroll', onScroll); };
  }, [key, SET.tab, SET.mode, SET.view, SET.only]);
}

// showChip scrolls the row of section chips (a phone's sidebar) to the
// one highlighted when it is out of view.
function showChip(e) {
  const row = e.parentElement;
  if (!e.offsetParent || row.scrollWidth <= row.clientWidth) return;
  const r = e.getBoundingClientRect(), box = row.getBoundingClientRect();
  if (r.left < box.left || r.right > box.right) row.scrollLeft += r.left - box.left - 16;
}

// Sidebar is All and Changed, which pick the rows shown, then the
// sections, which scroll to theirs.
function Sidebar({ names, counts, cls }) {
  const n = se.changedCount();
  const only = (o, label, count) => html`<button class=${'set-nav-item set-nav-only' + (SET.only === o ? ' on' : '')}
    onClick=${() => se.setOnly(o)}>${label}<span class="set-nav-n">${count}</span></button>`;
  return html`<nav class=${cls} aria-label="Sections">
    ${only('all', 'All', SET.data.keys.length)}
    ${only('changed', 'Changed', n)}
    <span class="set-nav-head">Sections</span>
    ${names.map((s) => html`<button
      class=${'set-nav-item' + (SET.section === s ? ' sec' : '') + (!counts[s] ? ' none' : '')}
      ref=${(e) => { if (e && SET.section === s) showChip(e); }}
      onClick=${() => goTo(s)}>${s}<span class="set-nav-n">${counts[s] || ''}</span></button>`)}
  </nav>`;
}

// ---- the JSON view ----

let made = null; // the editor, made the first time the view shows

function jsonEditorDOM() {
  if (made) return made;
  const box = document.createElement('div');
  box.className = 'set-json-cm';
  const ed = createEditor(box, {
    language: cm.json(),
    save: () => se.saveFile(),
    leave: () => redraw(),
    lensWarnings: true,
    say: (msg) => { S.flash = msg || ''; redraw(); },
    onUpdate: (u) => { if (u.docChanged) se.onJSONEdit(ed.doc()); },
  });
  made = {
    box,
    view: ed.view,
    doc: ed.doc,
    set: ed.set,
    focus: ed.focus,
    // mark shows the server's marks ({line, key, severity, message}): the
    // key's name underlined, else the line, and the message after it.
    mark(marks) {
      const doc = ed.view.state.doc;
      const diags = (marks || []).map((m) => {
        const line = doc.line(Math.min(Math.max(m.line, 1), doc.lines));
        let from = line.from + (line.text.length - line.text.trimStart().length);
        let to = Math.max(line.to, from);
        const at = m.key ? line.text.indexOf('"' + m.key + '"') : -1;
        if (at >= 0) { from = line.from + at; to = from + m.key.length + 2; }
        if (to === from && to < doc.length) to++;
        return { from, to, severity: m.severity === 'warning' ? 'warning' : 'error', message: m.message, source: 'xf config' };
      });
      ed.setMarks(diags);
    },
  };
  se.setJSONEditor(made);
  return made;
}

function JSONView() {
  const box = useRef(null);
  useEffect(() => {
    const ed = jsonEditorDOM();
    box.current.appendChild(ed.box);
    se.showFile();
    return () => { if (ed.box.parentNode === box.current) box.current.removeChild(ed.box); };
  }, []);
  const j = se.jsonOf();
  const lvl = se.level();
  const state = j.saving ? html`<span class="dim">saving…</span>`
    : j.draft !== null ? html`<span class="warn">● not saved${se.jsonAutosave() ? '' : ' · :w or ' + (/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘s' : 'ctrl+s') + ' saves'}</span>`
    : html`<span class="dim">${se.jsonAutosave() ? 'autosave' : ':w saves'}</span>`;
  return html`<div class="set-json">
    ${j.conflict && html`<div class="set-banner set-conflict">file changed on disk
      <span class="spacer"></span>
      <button class="run-btn" onClick=${() => se.reloadFile()}>Reload</button>
      <button class="run-btn" onClick=${() => se.keepMine()}>Keep mine</button></div>`}
    <div class="set-json-head">
      ${j.loaded && !j.found ? html`<span class="dim">no file yet: made when you first save</span>` : html`<span class="dim">${lvl && lvl.level === 'project' ? 'Project' : 'Global'} file</span>`}
      <span class="spacer"></span>
      ${j.status && html`<span class=${j.bad || j.status.startsWith('not saved') ? 'error' : 'dim'}>${j.status}</span>`}
      ${state}
      ${j.draft !== null && html`<button class="link" onClick=${() => se.discardFile()}>discard</button>`}
    </div>
    <div class="set-json-box" ref=${box}></div>
  </div>`;
}

// ---- the page ----

function SettingsBody({ list, sections }) {
  const d = SET.data;
  const lvl = se.level();
  const q = SET.query.trim();
  let n = -1;
  return html`
    <${AttentionWarning} />
    ${d.pending.length > 0 && html`<div class="set-banner">${plural(d.pending.length, 'change')} apply when the project reopens: ${d.pending.join(', ')}
      <button class="run-btn" disabled=${SET.reopening} onClick=${() => se.reopen()}><kbd>R</kbd> ${SET.reopening ? 'reopening…' : 'reopen now'}</button></div>`}
    ${SET.mode === 'json' ? html`<${JSONView} />` : html`
      ${lvl && lvl.error && html`<div class="set-err set-block">can't edit this file here: ${lvl.error} · the JSON view can fix it</div>`}
      ${d.warnings.length > 0 && html`<div class="set-warn">${d.warnings.map((w) => html`<div>⚠ ${w}</div>`)}</div>`}
      <p class="dim set-file">${SET.tab === 'user' ? 'Your settings, for every project.' : 'This project\'s settings, which win over yours: commit the file to share them.'}</p>
      ${sections.map((s) => {
        const ks = list.filter((k) => k.section === s);
        if (!ks.length) return null;
        return html`<section class="set-section"><h2 id=${secId(s)}>${s}</h2>
          ${s === se.GIT && !q && html`<${GitHosting} />`}
          ${s === se.VIM && html`<p class="dim set-file">The keys of the editor's own commands in vim's normal mode (the source pane, Scratch and this page's JSON), as vim writes them, e.g. ]g or ${'<C-j>'}.</p>`}
          ${ks.map((k) => { n++; return html`<${Row} k=${k} i=${n} />`; })}</section>`;
      })}
      ${!list.length && html`<div class="dim pad">${q ? 'no setting matches · esc clears the search' : SET.only === 'changed' ? 'this file sets no setting · c shows them all' : 'no settings'}</div>`}
    `}`;
}

export function SettingsPage() {
  const d = SET.data;
  const list = se.rows();
  const lvl = se.level();
  const main = useRef(null);
  const project = se.hasProject();
  const view = se.viewShown();
  const sections = se.sectionsShown();
  const counts = {};
  for (const k of list) counts[k.section] = (counts[k.section] || 0) + 1;
  const settingsUI = view === 'settings' && SET.mode === 'ui';
  useSpy(main, settingsUI && d ? sections : []);
  const scope = (t, label) => html`<button class=${'tab' + (SET.tab === t ? ' on' : '')} disabled=${t === 'project' && !project}
    onClick=${() => { se.setTab(t); redraw(); }}>${label}${se.hasDraft(t) ? html`<span class="set-dot" title="unsaved JSON draft">●</span>` : ''}</button>`;
  const mode = (m, label) => html`<button class=${'tab' + (SET.mode === m ? ' on' : '')} onClick=${() => se.setMode(m)}>${label}${m === 'json' && se.hasDraft(se.levelName()) ? html`<span class="set-dot" title="unsaved JSON draft">●</span>` : ''}</button>`;
  const tab = (v, label, extra) => html`<button role="tab" aria-selected=${view === v} class=${'set-view' + (view === v ? ' on' : '')} onClick=${() => se.setView(v)}>${label}${extra}</button>`;
  const n = se.changedCount();
  const status = S.flash ? html`<span class="warn">${S.flash}</span>`
    : SET.failed ? html`<span class="error">${SET.failed}</span>`
    : se.hasDraft() ? html`<span class="warn">unsaved JSON draft</span>`
    : html`<span class="dim" title=${SET.saved}>All changes saved</span>`;
  const other = se.hasInfo() ? [view === 'overview' ? ['s', 'settings'] : ['o', 'overview']] : [];
  const keys = view === 'overview'
    ? [...other, ['j/k', 'scroll'], ['r', 'refresh'], ['c', 'changed settings'], ['/', 'search settings'], ...(d && d.pending.length ? [['R', 'reopen']] : []), ['ctrl+q', 'back']]
    : SET.mode === 'json'
      ? [...other, ['enter', 'edit'], ['ctrl+q', 'back'], [':w', 'save'], ...(project ? [['tab', 'Global/Project']] : []), ['ctrl+w', 'leave the editor']]
      : [...other, ['j/k', 'move'], ['enter', 'edit / next choice'], ['h/l', 'choice'], ['x', 'reset'], ['c', SET.only === 'changed' ? 'all' : 'changed only'], ...(project ? [['tab', 'Global/Project']] : []), ['/', 'search'], ...(d && d.pending.length ? [['R', 'reopen']] : []), ['ctrl+q', 'back']];
  return html`<div class="app settings-page">
    <header class="set-top">
      <h1><span class="badge">XF</span> Settings</h1>
      ${se.hasInfo() && html`<span class="set-views" role="tablist" aria-label="Page">
        ${tab('overview', S.mode === 'picker' ? 'Tools & sign-in' : 'Overview', '')}
        ${tab('settings', 'Settings', n ? html`<span class="set-view-n">${n} changed</span>` : '')}
      </span>`}
      <span class="spacer"></span>
      ${view === 'settings' && html`
        ${lvl && html`<button class="set-path" title="Copy the path" onClick=${() => copy(lvl.path, 'copied: ' + lvl.path)}>${lvl.path}${lvl.found ? '' : html`<span class="dim"> · new</span>`}</button>`}
        <span class="set-tabs" role="group" aria-label="Scope">${scope('user', 'Global')}${project ? scope('project', 'Project') : ''}</span>
        <span class="set-tabs" role="group" aria-label="View">${mode('ui', 'UI')}${mode('json', 'JSON')}</span>`}
      ${view === 'overview' && html`<button class="link set-refresh" title="Read the info again (r)" onClick=${() => se.refreshInfo()}>refresh</button>`}
      <button class="link set-close" title="Close (esc)" onClick=${() => se.closeSettings()}>× close</button>
    </header>
    ${SET.closing && se.hasDraft() && html`<div class="set-banner set-closing" role="alertdialog">Unsaved JSON draft${Object.keys(SET.json).filter((l) => se.hasDraft(l)).length > 1 ? 's' : ''}: save before closing?
      <span class="spacer"></span>
      <button class="run-btn" onClick=${() => se.closeSaving()}><kbd>s</kbd> Save</button>
      <button class="run-btn" onClick=${() => se.closeDiscarding()}><kbd>d</kbd> Discard</button>
      <button class="link" onClick=${() => { SET.closing = false; redraw(); }}>cancel</button></div>`}
    ${SET.error && html`<p class="error">${SET.error}</p>`}
    ${view === 'overview' ? html`<div class="set-body ov-body"><div class="set-main" ref=${main}><${Overview} /></div></div>`
    : !d ? html`<div class="dim pad">reading…</div>` : html`
      ${SET.mode === 'ui' && html`<div class="set-search">
        <span class="set-search-icon">${lens}</span>
        <input value=${SET.query} placeholder="Search settings" spellcheck="false" autocomplete="off" aria-label="Search settings"
          ref=${(e) => { if (e && SET.typing && document.activeElement !== e) e.focus(); }}
          onFocus=${() => { if (!SET.typing) { SET.typing = true; redraw(); } }}
          onInput=${(e) => { SET.query = e.currentTarget.value; SET.cursor = 0; SET.reveal = true; redraw(); }}
          onBlur=${() => { SET.typing = false; redraw(); }}
          onKeyDown=${(e) => {
            if (e.key === 'Escape') { e.preventDefault(); SET.query = ''; SET.typing = false; e.currentTarget.blur(); redraw(); }
            if (e.key === 'Enter' || e.key === 'ArrowDown') { e.preventDefault(); SET.typing = false; e.currentTarget.blur(); redraw(); }
          }} />
        ${SET.query.trim() ? html`<span class="dim">${list.length} found</span>` : html`<kbd>/</kbd>`}
      </div>
      <${Sidebar} names=${sections} counts=${counts} cls="set-nav set-nav-chips" />`}
      <div class=${'set-body' + (SET.mode === 'json' ? ' json' : '')}>
        ${SET.mode === 'ui' && html`<${Sidebar} names=${sections} counts=${counts} cls="set-nav set-nav-side" />`}
        <div class="set-main" ref=${main}><${SettingsBody} list=${list} sections=${sections} /></div>
      </div>
    `}
    <div class="footer set-footer">${keyList(keys)}<span class="spacer"></span>${status}</div>
  </div>`;
}
