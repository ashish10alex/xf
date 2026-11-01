// Pane 2's target section (target.js): a box between the actions and the
// SQL, open by default, with the fields of what a compile adds to the
// names it builds.

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { S } from './state.js';
import * as tg from './target.js';

const html = htm.bind(h);
const { TGT } = tg;

// Field is one of a Dataform project's three: the label, an underscore on
// the side Dataform joins it, and the text.
function Field({ d }) {
  const flag = tg.flagged(d.key);
  const text = flag != null ? flag : tg.fieldText(d.key);
  const k = TGT.keys[d.key];
  // Set in no file, the value in use is the workflow settings' own.
  const fromWorkflow = k && k.project == null && k.user == null && k.flag == null;
  const inUse = fromWorkflow && k.value && k.value !== '(none)' ? k.value : '';
  const prefix = d.key === 'tablePrefix';
  const cls = 'tgt-field' + (text ? ' set' : '') + (TGT.errors[d.key] ? ' bad' : '') + (flag != null ? ' locked' : '');
  const under = text ? html`<span class="tgt-us">_</span>` : null;
  return html`<label class=${cls} title=${flag != null ? 'set by --' + d.key.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()) + ' for this run' : TGT.errors[d.key] || d.title}>
    <span class="tgt-lab">${d.label}</span>
    ${!prefix && under}
    <input value=${text} disabled=${flag != null || TGT.saving} spellcheck="false" autocomplete="off" autocapitalize="off"
      style=${prefix && text ? 'flex:0 1 auto;width:' + (text.length + 1) + 'ch' : ''}
      placeholder=${inUse ? inUse + ' (workflow settings)' : prefix ? 'none' : 'no suffix'}
      aria-label=${d.label}
      onInput=${(e) => tg.setDraft(d.key, e.currentTarget.value)}
      onBlur=${() => tg.commit(d.key)}
      onKeyDown=${(e) => {
        if (e.key === 'Enter') { e.preventDefault(); e.currentTarget.blur(); }
        if (e.key === 'Escape') { e.preventDefault(); tg.undo(d.key); e.currentTarget.blur(); }
      }} />
    ${prefix && under}
  </label>`;
}

function DataformBody() {
  const pending = tg.dataformKeys.some((d) => TGT.pending.includes(d.key));
  return html`<div class="tgt-row">${tg.dataformKeys.map((d) => html`<${Field} key=${d.key} d=${d} />`)}</div>
    ${pending && html`<div class="tgt-foot"><button class="link warn" onMouseDown=${(e) => { e.preventDefault(); tg.reopen(); }}>reopen applies the saved values</button></div>`}`;
}

function DbtBody() {
  const v = TGT.view || {};
  const p = v.profile;
  const now = v.target || (p && p.default) || '';
  const flag = tg.flagged('dbtTarget');
  const pending = TGT.pending.includes('dbtTarget');
  const t = p && p.targets.find((x) => x.name === now);
  return html`<div class="tgt-row tgt-dbt">
      ${p && p.targets.length ? p.targets.map((x) => html`<button key=${x.name} class=${'chip tgt-chip' + (x.name === now ? ' on' : '')}
        disabled=${TGT.saving || flag != null} title=${(x.database || '?') + '.' + (x.schema || '?') + (x.name === p.default ? ' · the profile\'s default' : '')}
        onMouseDown=${(e) => { e.preventDefault(); e.stopPropagation(); tg.pickTarget(x.name); }}>${x.name}</button>`)
      : html`<span class="dim">${v.profileErr || 'no targets found'}</span>`}
      ${t && html`<span class="tgt-now"><span class="dim">→</span> ${t.database || '?'}.${t.schema || '?'}</span>`}
    </div>
    <div class="tgt-foot">
      ${p && html`<span class="dim" title=${p.file}>profiles.yml · ${p.name}</span>`}
      ${flag != null && html`<span class="dim">set by --dbt-target for this run</span>`}
      ${pending && html`<button class="link warn" onMouseDown=${(e) => { e.preventDefault(); tg.reopen(); }}>reopen applies the saved target</button>`}
    </div>`;
}

// TargetSection sits where lineage did before it became a tab (ADR 0022).
export function TargetSection() {
  if (!tg.shown()) return null;
  const dbt = S.boot.backend === 'dbt';
  if (!TGT.open) {
    return html`<section class="pane target-closed" onMouseDown=${(e) => { e.preventDefault(); tg.toggleOpen(); }}>
      <header class="pane-title"><span class="caret">▸</span><span class="pane-name">target · ${tg.summary()}${tg.reopening() ? html`<span class="warn"> · compiling with the new names…</span>` : ''}</span></header>
    </section>`;
  }
  return html`<section class=${'pane target-pane' + (tg.reopening() ? ' saving' : '')}>
    <header class="pane-title">
      <span class="caret" onMouseDown=${(e) => { e.preventDefault(); tg.toggleOpen(); }}>▾</span>
      <span class="pane-name">target${tg.reopening() ? html`<span class="warn"> · compiling with the new names…</span>` : ''}</span>
    </header>
    <div class="pane-body tgt-body">${dbt ? html`<${DbtBody} />` : html`<${DataformBody} />`}</div>
  </section>`;
}
