// What python.js draws: the status line's interpreter, the interpreter
// picker and the offer to write pyrefly.toml (ADR 0018).

import { h } from './vendor/preact.mjs';
import htm from './vendor/htm.mjs';
import { redraw, hooks, keyName } from './state.js';
import { PY, interpreterLabel, interpreterRows, pickInterpreter, openInterpreterPicker, createConfig, pythonKey, closePython } from './python.js';
import { Modal } from './view.js';

const html = htm.bind(h);

// configText is what createConfig writes (pyrefly.ConfigText).
const configText = `project-includes = ["**/*.py"]
project-excludes = [
  "**/target/**",
  "**/dbt_packages/**",
  "**/node_modules/**",
  "**/.venv/**",
  "**/venv/**",
  "**/logs/**",
]`;

// grab gives the overlay's box the keys when it is drawn, as the prod
// prompt does, unless something in it has them: the editor may take focus
// back as the page loads, after the overlay opened.
const grab = (e) => e && !e.contains(document.activeElement) && e.focus();

function keys(list) {
  return html`<div class="overlay-keys"><span class="keys">${list.map(([k, d]) => html`<span class="key"><kbd>${k}</kbd> ${d}</span>`)}</span></div>`;
}

// PythonIndicator is the status line's interpreter while a Python file
// shows: a click picks another. It says when pyrefly can't run.
function PythonIndicator() {
  const st = PY.status;
  if (!PY.file || !st || st.off) return null;
  if (st.missing || PY.unavailable) {
    const why = st.missing || PY.unavailable;
    return html`<button class="link warn py-ind" title=${why} onClick=${() => { openInterpreterPicker(); }}>py: ${st.missing ? 'no pyrefly' : 'pyrefly stopped'}</button>`;
  }
  const where = st.where === 'picked' ? 'picked' : st.where ? 'found at the root' : 'pyrefly\'s own choice';
  const title = (st.interpreter || 'no interpreter given: pyrefly picks one') + '\n' + where +
    (st.config ? '\nconfig: ' + st.config : '\nno pyrefly config: type errors are off') + '\nclick to pick another';
  return html`<button class=${'link py-ind ' + (st.config ? 'dim' : 'warn')} title=${title} onClick=${() => { openInterpreterPicker(); }}>py: ${interpreterLabel(st)}${st.config ? '' : ' · no config'}</button>`;
}

function InterpreterPicker() {
  const o = PY.overlay;
  const rows = interpreterRows();
  const custom = rows[o.cursor] && rows[o.cursor].custom;
  const pick = (i) => { o.cursor = i; pickInterpreter(); };
  return html`<${Modal} title="Python interpreter for pyrefly" onClose=${() => { closePython(); redraw(); }}>
    <div class="editor-pick" tabindex="-1" ref=${(e) => !custom && grab(e)}>
      ${o.loading ? html`<p class="dim">…</p>` : html`
        <p>pyrefly resolves imports with <code>${o.current || 'its own choice'}</code>${o.where === 'picked' ? ' (picked)' : ''}. Pick another:</p>
        <p class="dim small">Kept for you in this project only, outside the repo.</p>
        <div class="ed-list">
          ${rows.map((r, i) => html`<div class=${'ed-row' + (i === o.cursor ? ' sel' : '')}
              onMouseMove=${() => { if (o.cursor !== i) { o.cursor = i; redraw(); } }}
              onClick=${() => { if (!r.custom) pick(i); else { o.cursor = i; redraw(); } }}>
            <span class="lead">${i === o.cursor ? '▸' : ''}</span>
            ${r.auto
              ? html`<span class="ed-name">found by xf</span><span class="dim">the root's .venv/ or venv/, else pyrefly's choice</span>${o.where !== 'picked' && html`<span class="ok small">current</span>`}`
              : r.custom
                ? html`<span class="ed-name">another path</span>
                  <input class="text" value=${o.custom} placeholder="e.g. ~/envs/dbt/bin/python" spellcheck="false"
                    ref=${(e) => e && custom && document.activeElement !== e && e.focus()}
                    onFocus=${() => { o.cursor = i; redraw(); }}
                    onInput=${(ev) => { o.custom = ev.currentTarget.value; o.err = ''; }}
                    onKeyDown=${(ev) => {
                      const k = keyName(ev);
                      if (k === 'enter') { ev.preventDefault(); pickInterpreter(); }
                      else if (k === 'esc') { ev.preventDefault(); closePython(); redraw(); }
                      else if (k === 'up' || k === 'down' || k === 'ctrl+n' || k === 'ctrl+p') {
                        ev.preventDefault();
                        pythonKey(k);
                        ev.currentTarget.blur();
                        redraw();
                      }
                    }} />`
                : html`<span class="ed-name">${r.where}</span><code class="dim">${r.path}</code>
                  ${o.where === 'picked' && r.path === o.current && html`<span class="ok small">current</span>`}`}
          </div>`)}
        </div>
        ${o.saving && html`<p class="warn">saving…</p>`}
        ${o.err && html`<p class="error">${o.err}</p>`}`}
      ${keys([['enter', 'use it'], ['↑/↓', 'move'], ['esc', 'cancel']])}
    </div>
  <//>`;
}

function ConfigOffer() {
  const o = PY.overlay;
  return html`<${Modal} title="no pyrefly config" onClose=${() => { closePython(); redraw(); }}>
    <div class="editor-pick" tabindex="-1" ref=${grab}>
      <p>This project has no <code>pyrefly.toml</code>, so pyrefly reports no type errors in its Python files. Write one at the project's root?</p>
      <pre class="py-config">${configText}</pre>
      <p class="dim small">It names no interpreter: that is yours, kept outside the repo (Python: select interpreter).</p>
      ${o.saving && html`<p class="warn">writing…</p>`}
      ${o.err && html`<p class="error">${o.err}</p>`}
      <div class="buttons">
        <button class="run-btn" onClick=${() => createConfig()}><kbd>y</kbd> write pyrefly.toml</button>
        <button class="plain" onClick=${() => { pythonKey('n'); redraw(); }}><kbd>n</kbd> no, don't ask again</button>
        <button class="plain" onClick=${() => { pythonKey('esc'); redraw(); }}><kbd>esc</kbd> not now</button>
      </div>
    </div>
  <//>`;
}

function PythonOverlay() {
  const o = PY.overlay;
  if (!o) return null;
  return o.kind === 'config' ? html`<${ConfigOffer} />` : html`<${InterpreterPicker} />`;
}

hooks.pythonIndicator = () => html`<${PythonIndicator} />`;
hooks.pythonOverlay = () => html`<${PythonOverlay} />`;
