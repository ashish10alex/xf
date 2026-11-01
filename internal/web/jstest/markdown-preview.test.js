// Beside a Markdown file pane 2 is the file rendered (ADR 0027): autoHidePane2
// leaves it, its keys scroll the preview, and the panes it stands for give
// their keys to it.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, rightShown, showPlainFile, previewShown, setFocus, handleKey } from '../assets/state.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

let lang = '';
let keys = [];

beforeEach(() => {
  Object.assign(S, { mode: 'project', overlay: null, zoomed: false, rightHidden: false, rightPeek: '', flash: '', focus: 'source', sel: null, view: null, plainFile: '', lineageOpen: false });
  S.boot = { root: '/p', autoHidePane2: true };
  hooks.sourceShownNow = () => true;
  hooks.sourceLang = () => lang;
  hooks.markdownKey = (k) => { keys.push(k); return k === 'j'; };
  lang = '';
  keys = [];
});

test('pane 2 stays beside a Markdown file', () => {
  lang = 'markdown';
  showPlainFile('README.md');
  assert.equal(previewShown(), true);
  assert.equal(rightShown(), true);
});

test('pane 2 still hides beside another file with no action', () => {
  lang = 'python';
  showPlainFile('scripts/load.py');
  assert.equal(previewShown(), false);
  assert.equal(rightShown(), false);
});

test("pane 2's keys go to the preview", () => {
  lang = 'markdown';
  showPlainFile('README.md');
  setFocus('sql');
  assert.equal(handleKey('j'), true);
  assert.deepEqual(keys, ['j']);
});

test('the panes the preview stands for give it their keys', () => {
  lang = 'markdown';
  showPlainFile('README.md');
  S.lineageOpen = true;
  for (const p of ['actions', 'lineage', 'run', 'sql']) {
    setFocus(p);
    assert.equal(S.focus, 'sql', p);
  }
});
