// The prod settings as forms (ADR 0037): prodCompilerOptions as a prod
// project, a prod dataset and more flags, in the compare with prod dialog
// and on the settings page; dbtProdTarget as a pick of the profile's
// targets.

import { h } from './vendor/preact.mjs';
import { useEffect } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { S, get, redraw } from './state.js';
import { splitOptions, joinOptions, sameAsDev, boxHint } from './prod-options.js';

const html = htm.bind(h);

// prodNames is /api/target as the forms read it: a Dataform project's
// workflow and dev names, a dbt project's profile and target; null until
// asked, and asked again each time a form opens.
let prodNames = null;
let asked = '';

function useProdNames() {
  useEffect(() => {
    const root = (S.boot && S.boot.root) || '';
    if (S.mode !== 'project' || asked === root + ':' + (S.state && S.state.compiles)) return;
    asked = root + ':' + (S.state && S.state.compiles);
    get('/api/target').then((v) => { prodNames = v; redraw(); }).catch(() => { asked = ''; });
  }, []);
  return prodNames;
}

// prodFields are a form's boxes for options, kept on holder so a redraw
// doesn't lose what was typed: holder.fields, and holder.more for whether
// more flags is open.
export function prodFields(holder, options) {
  if (!holder.fields) {
    holder.fields = splitOptions(options || '');
    holder.more = !!holder.fields.more;
  }
  return holder.fields;
}

// prodOptionsOf are holder's boxes as prodCompilerOptions.
export const prodOptionsOf = (holder) => joinOptions(holder.fields || {});

// ProdForm draws holder's boxes; onInput is told of each change, onDone
// of leaving a box or enter. focus puts the cursor in the first box;
// unset says nothing sets the key, so empty boxes say nothing yet.
export function ProdForm({ holder, options, disabled, focus, unset, onInput, onDone }) {
  const names = useProdNames();
  const f = prodFields(holder, options);
  const set = (box) => (e) => { f[box] = e.currentTarget.value; if (onInput) onInput(); redraw(); };
  const key = (e) => { if (e.key === 'Enter' && onDone) { e.preventDefault(); onDone(true); } };
  // Moving between the form's own boxes is not leaving it.
  const leave = (e) => { if (onDone && !(e.relatedTarget && e.currentTarget.contains(e.relatedTarget))) onDone(false); };
  const box = (name, label) => html`<label class="pf-row">
    <span class="pf-label">${label}</span>
    <input class="set-input" value=${f[name]} placeholder=${boxHint(name, names)} spellcheck="false" autocomplete="off" disabled=${disabled}
      ref=${(e) => { if (e && focus && name === 'project' && !holder.focused) { holder.focused = true; e.focus(); } }}
      onInput=${set(name)} onKeyDown=${key} />
  </label>`;
  const typed = !!joinOptions(f);
  const empty = !typed && !unset;
  const same = (typed || !unset) && names && sameAsDev(f, names);
  return html`<div class="prod-form" onFocusOut=${leave}>
    ${box('project', 'Prod project')}
    ${box('dataset', 'Prod dataset')}
    ${holder.more
      ? html`<label class="pf-row">
          <span class="pf-label">More flags</span>
          <input class="set-input" value=${f.more} placeholder="e.g. --vars=env=prod" spellcheck="false" autocomplete="off" disabled=${disabled}
            ref=${(e) => { if (e && holder.focusMore) { holder.focusMore = false; e.focus(); } }}
            onInput=${set('more')} onKeyDown=${key} />
        </label>`
      : html`<button class="link pf-more" disabled=${disabled} onMouseDown=${(e) => e.preventDefault()}
          onClick=${() => { holder.more = holder.focusMore = true; redraw(); }}>▸ more flags</button>`}
    ${(empty || same) && html`<div class=${'pf-note' + (same ? ' warn' : '')}>
      ${empty ? 'all empty: the default targets are prod' : ''}${empty && same ? ' · ' : ''}${same ? '⚠ prod names the same tables as dev' : ''}
    </div>`}
  </div>`;
}

// TargetPick is dbtProdTarget's control: the profile's targets, the one
// xf compiles with marked dev, with free text when no profile is read.
export function TargetPick({ value, inUse, disabled, onPick, children }) {
  const names = useProdNames();
  const prof = names && names.profile;
  if (!prof || !prof.targets || !prof.targets.length) return children;
  const dev = names.target || prof.default;
  return html`<span class="set-choices">
    ${prof.targets.map((t) => html`<button class=${'chip' + (value === t.name ? ' on' : value === null && inUse === t.name ? ' inuse' : '')} disabled=${disabled}
      title=${[t.database, t.schema].filter(Boolean).join('.')} onClick=${() => onPick(t.name)}>${t.name}${t.name === dev ? html` <span class="dim">dev</span>` : ''}</button>`)}
  </span>
  ${value && value === dev && html`<div class="pf-note warn">⚠ ${value} is the target xf compiles with (dev): prod names the same tables as dev</div>`}`;
}
