// What forge.js draws: the status line's merge request pill, the rows of
// its section in Source Control, and Settings › Git hosting.

import { h } from './vendor/preact.mjs';
import { useEffect } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { redraw, copy } from './state.js';
import * as fg from './forge.js';

const html = htm.bind(h);
const F = fg.F;

// ForgePill is the status line's: "!42 ✓", "+ MR", "⚿ connect"…; a
// click opens Source Control at the section.
export function ForgePill() {
  const p = fg.pill();
  if (!p) return null;
  return html`<button class=${'link forge-pill ' + p.tone + (p.stale ? ' stale' : '') + (p.going ? ' going' : '')} title=${p.title}
    onClick=${() => p.run()}>${p.text}</button><span class="dim"> · </span>`;
}

const pad2 = (n) => String(n).padStart(2, '0');
const fmtSecs = (s) => {
  if (!s) return '';
  s = Math.round(s);
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm' + pad2(s % 60) + 's';
  return Math.floor(s / 3600) + 'h' + pad2(Math.floor(s / 60) % 60) + 'm';
};

// When a pipeline, stage or job ran, from its started and finished
// (ISO times, either missing): took is how long, at the time it started
// (14:02), when the date and time, for a tooltip.
const ms = (t) => (t ? Date.parse(t) : 0);
const took = (x) => (x.started && x.finished ? (ms(x.finished) - ms(x.started)) / 1000 : 0);
const at = (t) => { const d = new Date(t); return pad2(d.getHours()) + ':' + pad2(d.getMinutes()); };
const when = (x) => {
  if (!x.started) return '';
  const day = (t) => new Date(t).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
  const sameDay = x.finished && new Date(x.started).toDateString() === new Date(x.finished).toDateString();
  return '\nstarted ' + day(x.started) + (x.finished ? ' · finished ' + (sameDay ? at(x.finished) : day(x.finished)) + (took(x) ? ' · took ' + fmtSecs(took(x)) : '') : '');
};

// ran is the pipeline row's: "2 h ago · 4m12s" once it is done,
// "started 3 min ago" while it runs.
const ran = (p) => (p.finished ? fg.ago(ms(p.finished)) + (took(p) ? ' · ' + fmtSecs(took(p)) : '') : p.started ? 'started ' + fg.ago(ms(p.started)) : '');

// Status is a status's icon in its colour.
const Status = ({ s }) => html`<span class=${'forge-st ' + (fg.statusTone[s] || 'dim') + (fg.going(s) ? ' going' : '')} title=${s}>${fg.statusIcon[s] || '·'}</span>`;

// indent is a stage's or job's padding: ch at depth 0, 3ch more for each
// trigger job it is under.
const indent = (ch, r) => ch + 3 * (r.depth || 0) + 'ch';

// ForgeRow is one of the section's rows in Source Control's list.
export function ForgeRow({ r, cur, focused, onClick, refEl }) {
  const cls = (more) => 'scm-row forge ' + more + (cur ? ' cur' : '') + (cur && focused ? ' focused' : '');
  const down = (e) => { e.stopPropagation(); e.preventDefault(); onClick(r); };
  switch (r.kind) {
    case 'mrask': {
      const a = r.ask;
      const what = a.noUpstream ? 'push ' + fg.F.v.branch + ' to ' + a.remote + ' first?' : 'push ' + a.ahead + ' commit' + (a.ahead === 1 ? '' : 's') + ' to ' + a.remote + ' first?';
      const btn = (k, label) => html`<button class=${'confirm-btn' + (k === 'y' ? ' yes' : '')} onMouseDown=${(e) => { e.stopPropagation(); e.preventDefault(); fg.askKey(k); redraw(); }}><kbd>${k}</kbd>${label}</button>`;
      return html`<div class="tree-prompt confirm forge-ask">
        <div>${what}</div>
        <div class="dim">${fg.longWord()} pages show what is pushed</div>
        <div class="confirm-buttons">${btn('y', 'push, then open')}${btn('s', 'open anyway')}${btn('n', 'cancel')}</div>
      </div>`;
    }
    case 'mrnote':
      return html`<div class=${'scm-row note forge-note ' + r.tone} style="padding-left:3.5ch" title=${r.text}>${r.text}</div>`;
    case 'mrcreate':
      return html`<div class=${cls('forge-create' + (r.tone ? ' ' + r.tone : ''))} style="padding-left:3.5ch" onMouseDown=${down} ref=${refEl}>
        <span class="scm-name">+ ${r.text}</span><span class="spacer"></span><kbd class="forge-key">m</kbd></div>`;
    case 'mr': {
      const m = r.mr;
      const state = m.state !== 'open' ? m.state : m.draft ? 'draft' : '';
      return html`<div class=${cls('forge-mr')} style="padding-left:3.5ch" title=${fg.mrName(m) + ' ' + m.title + '\n' + m.source + ' → ' + m.target + '\no opens it'}
          onMouseDown=${down} ref=${refEl}>
        <span class="forge-num">${fg.mrName(m)}</span>
        <span class="scm-name">${fg.shownTitle(m)}</span>
        <span class="spacer"></span>
        ${state && html`<span class=${'forge-tag ' + (m.state === 'merged' ? 'ok' : m.state === 'closed' ? 'error' : 'dim')}>${state}</span>`}
        <span class="dim forge-flow">→ ${m.target}</span>
      </div>`;
    }
    case 'mrbase':
      return html`<div class=${cls('forge-base')} style="padding-left:3.5ch" title=${'R, I and C compare with ' + r.base + '; enter makes it ' + r.target}
          onMouseDown=${down} ref=${refEl}>
        <span class="warn scm-name">targets ${r.target} · xf compares with ${r.base}</span><span class="spacer"></span>
        <span class="scm-buttons" style="display:inline-flex"><button title="compare R, I and C with the target (enter)" onMouseDown=${down}>use ${r.target}</button></span>
      </div>`;
    case 'pipe': {
      const p = r.p;
      return html`<div class=${cls('forge-pipe')} style="padding-left:3.5ch" title=${'pipeline ' + p.status + (p.sha ? ' · ' + p.sha.slice(0, 8) : '') + when(p) + '\nO opens it'}
          onMouseDown=${down} ref=${refEl}>
        <${Status} s=${p.status} />
        <span class="scm-name">pipeline${p.id ? ' #' + p.id : ''}</span>
        <span class="spacer"></span>
        <span class="dim forge-ran shrink">${ran(p)}</span>
        <kbd class="forge-key">O</kbd>
      </div>`;
    }
    case 'stage': {
      const s = r.stage;
      return html`<div class=${cls('forge-stage')} style=${'padding-left:' + indent(3.5, r)} title=${s.name + ' · ' + s.status + when(s)}
          onMouseDown=${down} ref=${refEl}>
        <span class="tree-caret">${r.open ? '▾' : '▸'}</span>
        <${Status} s=${s.status} />
        <span class="scm-name">${s.name}</span>
        <span class="spacer"></span>
        <span class="dim forge-ran">${fmtSecs(took(s))}</span>
        <span class="scm-count">${(s.jobs || []).length}</span>
      </div>`;
    }
    case 'job': {
      const j = r.job;
      // A trigger job's caret sits left of its status, which lines up
      // with its siblings'.
      const title = r.down
        ? j.name + ' · its pipeline ' + (j.downstream.id ? '#' + j.downstream.id + ' ' : '') + j.status + when(j) + '\nenter shows its jobs · o opens it'
        : j.name + ' · ' + j.status + when(j) + '\nenter opens it';
      return html`<div class=${cls('forge-job')} style=${'padding-left:' + indent(r.down ? 4.9 : 6.5, r)} title=${title}
          onMouseDown=${down} ref=${refEl}>
        ${r.down && html`<span class="tree-caret">${r.open ? '▾' : '▸'}</span>`}
        <${Status} s=${j.status} />
        <span class="scm-name">${j.name}</span>
        <span class="spacer"></span>
        ${j.started && html`<span class="dim forge-ran">${at(j.started)}</span>`}
        <span class="dim forge-ran forge-took">${fmtSecs(j.seconds)}</span>
      </div>`;
    }
  }
  return null;
}

// ---- Settings › Git hosting ----

const kinds = [['gitlab', 'GitLab'], ['github', 'GitHub']];
const kindName = (k) => (k === 'github' ? 'GitHub' : 'GitLab');

// HostForm adds a host, or changes one's token, as a settings row.
function HostForm() {
  const a = F.add;
  const keys = (e) => {
    if (e.key === 'Enter') { e.preventDefault(); fg.saveHost(); }
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); F.add = null; redraw(); }
    e.stopPropagation();
  };
  const scopes = a.kind === 'github' ? 'repo, or read access to pull requests, actions and checks' : 'read_api';
  return html`<div class="set-row sel forge-form">
    <div class="set-row-text">
      <div class="set-title"><span class="set-name">${a.known ? 'Token for ' + a.host : 'Add a host'}</span></div>
      <div class="set-doc">${a.known ? 'A token with ' + scopes + '.' : 'A GitLab or GitHub host, e.g. gitlab.example.com, and a token with ' + scopes + '.'}</div>
      ${a.test && html`<div class=${'set-src ' + (a.test.startsWith('✓') ? 'ok' : a.test.startsWith('✗') ? 'error' : '')}>${a.test}</div>`}
    </div>
    <div class="set-ctl">
      ${!a.known && html`<input class="set-input" placeholder="host" value=${a.host} spellcheck="false" autocomplete="off" autocapitalize="off"
        ref=${(e) => { if (e && !a.focused) { a.focused = true; e.focus(); } }}
        onInput=${(e) => { a.host = e.currentTarget.value; }} onKeyDown=${keys} />`}
      ${!a.known && html`<div class="set-field">${kinds.map(([k, label]) => html`<button class=${'chip' + (a.kind === k ? ' on' : '')} onClick=${() => { a.kind = k; redraw(); }}>${label}</button>`)}</div>`}
      <input class="set-input" type="password" placeholder="token" value=${a.token}
        spellcheck="false" autocomplete="off" ref=${(e) => { if (e && a.known && !a.focused) { a.focused = true; e.focus(); } }}
        onInput=${(e) => { a.token = e.currentTarget.value; }} onKeyDown=${keys} />
      <div class="set-field">
        <button class="chip on" onClick=${() => fg.saveHost()}>save</button>
        <button class="chip" disabled=${!a.host.trim() || !a.token.trim()} onClick=${() => fg.testHost(null, a)}>test</button>
        <button class="link" onClick=${() => { F.add = null; redraw(); }}>cancel</button>
      </div>
    </div>
  </div>`;
}

// HostCard is a host as a settings row: its kind and token on the left,
// what to do and where the token comes from on the right.
function HostCard({ hv }) {
  const from = hv.from === 'file' ? 'tokens.json' : hv.from ? '$' + hv.from : '';
  return html`<div class=${'set-row forge-host' + (hv.from === 'file' ? ' chg' : '')}>
    <div class="set-row-text">
      <div class="set-title">
        <span class="set-name">${hv.host}</span>
        <span class="set-tag">${kindName(hv.kind)}</span>
        ${hv.builtin && !hv.entry && html`<span class="set-tag">built in</span>`}
      </div>
      <div class="set-doc">${hv.token ? html`token <code>${hv.token}</code>` : html`<span class="warn">no token</span>`}
        · <a class="link" href=${hv.tokenPage} target="_blank" rel="noopener" title="the host's page for making a token">make a token ↗</a></div>
      ${hv.test && html`<div class=${'set-src ' + (hv.test.startsWith('✓') ? 'ok' : hv.test.startsWith('✗') ? 'error' : '')}>${hv.test}</div>`}
    </div>
    <div class="set-ctl">
      <div class="set-field">
        <button class="chip" onClick=${() => fg.startAdd(hv)}>${hv.token && hv.from === 'file' ? 'change token' : 'set token'}</button>
        <button class="chip" disabled=${!hv.token} onClick=${() => fg.testHost(hv)}>test</button>
      </div>
      <div class="set-src">${from ? html`<span class=${hv.from === 'file' ? 'set-src-file' : ''}>${hv.from === 'file' ? '● ' : ''}${from}</span>` : 'no token'}
        ${hv.entry && html`<span> · </span><button class="link" title="take it out of tokens.json" onClick=${() => fg.deleteHost(hv)}>remove</button>`}</div>
    </div>
  </div>`;
}

// GitHosting is the hosts xf reads merge requests and pipelines from,
// each with its token (ADR 0020).
export function GitHosting() {
  useEffect(() => { fg.loadHosts(); }, []);
  const v = F.hosts;
  return html`<div class="forge-hosts">
    <p class="dim set-file">The hosts xf web shows the branch's merge request and pipeline from, read-only, each with a token of xf's own${v && v.path ? html` in <button class="link" title="Copy the path" onClick=${() => copy(v.path, 'copied: ' + v.path)}>${v.path}</button>` : ''}. $GITLAB_TOKEN and $GITHUB_TOKEN win for gitlab.com and github.com, and stand in for a host of their kind with no token here.</p>
    ${F.hostsError && html`<div class="set-err set-block">${F.hostsError}</div>`}
    ${!v ? html`<div class="dim pad">reading…</div>` : v.hosts.map((hv) => html`<${HostCard} key=${hv.host} hv=${hv} />`)}
    ${F.add ? html`<${HostForm} />` : html`<div class="forge-add-row"><button class="chip forge-add" onClick=${() => fg.startAdd(null)}>+ add a host</button></div>`}
  </div>`;
}
