// What markdown.js draws: pane 2's body beside a Markdown file, the file
// rendered (ADR 0027).

import { h } from './vendor/preact.mjs';
import { useLayoutEffect, useRef } from './vendor/hooks.mjs';
import htm from './vendor/htm.mjs';
import { hooks, redraw, setFocus } from './state.js';
import { shownHTML, shownError, follow, setBox } from './markdown.js';

const html = htm.bind(h);

// MarkdownBody is the rendered file. The server left out the file's raw
// HTML (markdown.go), so its HTML goes in as it comes.
function MarkdownBody() {
  const box = useRef(null);
  const drawn = useRef(null); // the HTML in the box
  const body = shownHTML();
  const err = shownError();
  useLayoutEffect(() => {
    setBox(box.current);
    return () => setBox(null);
  }, []);
  useLayoutEffect(() => {
    const el = box.current && box.current.firstElementChild;
    if (el && drawn.current !== body) { el.innerHTML = body; drawn.current = body; }
  }, [body]);
  return html`<div class="md-preview scroll" ref=${box}
      onMouseDown=${(e) => { e.stopPropagation(); setFocus('sql'); redraw(); }}
      onClick=${(e) => {
        const a = e.target instanceof Element && e.target.closest('a[href]');
        if (!a) return;
        e.preventDefault();
        follow(a);
      }}>
    <div class="md-doc"></div>
    ${err && html`<div class="error pad">${err}</div>`}
    ${!body && !err && html`<div class="dim pad">rendering…</div>`}
  </div>`;
}

hooks.markdownBody = MarkdownBody;
