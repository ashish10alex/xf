// A dbt model's Jinja in the source pane: inTemplate says a position is in
// a template, a tag not yet closed running to the end. The tags are read
// once per doc; asking again gives the same answers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { S } from '../assets/state.js';
import { SRC } from '../assets/source.js';
import { inTemplate } from '../assets/source-smart.js';

S.boot = { ...(S.boot || {}), backend: 'dbt' };
SRC.view = { lang: 'sql' };

// want is what inTemplate gave before its tags were kept: each tag read
// again from the text.
const JINJA = /\{\{[\s\S]*?(?:\}\}|$)|\{%[\s\S]*?(?:%\}|$)|\{#[\s\S]*?(?:#\}|$)/g;
function want(text, pos) {
  for (const m of text.matchAll(JINJA)) {
    if (m.index >= pos) break;
    const closed = /(?:\}\}|%\}|#\})$/.test(m[0]) && m[0].length > 2;
    if (pos < m.index + m[0].length || (!closed && pos === m.index + m[0].length)) return true;
  }
  return false;
}

const docs = [
  "select {{ ref('a') }} from {{ source('s', 't') }}",
  "{% if x %}\nselect 1\n{% endif %}\n",
  'select 1 {# a comment\n over lines #} from t',
  "{%- set y = 2 -%}\nselect {{- y -}} as y\n{%- for c in cols %}, {{ c }}{% endfor -%}",
  "select {{ ref('a'",
  'select 1 {%',
  'select {# not closed',
  '{{}}{%%}{##}{',
  'select 1',
  '',
];

test('inTemplate is right at every position, on a fresh doc and again', () => {
  for (const text of docs) {
    for (let pos = 0; pos <= text.length; pos++) {
      // A new state each time: nothing kept yet.
      assert.equal(inTemplate(cm.EditorState.create({ doc: text }), pos), want(text, pos), JSON.stringify(text) + ' @' + pos);
    }
    const state = cm.EditorState.create({ doc: text });
    for (let round = 0; round < 2; round++) {
      for (let pos = 0; pos <= text.length; pos++) {
        assert.equal(inTemplate(state, pos), want(text, pos), JSON.stringify(text) + ' @' + pos + ' round ' + round);
      }
      // From the end back, so the first ask is not the start.
      for (let pos = text.length; pos >= 0; pos--) {
        assert.equal(inTemplate(state, pos), want(text, pos), JSON.stringify(text) + ' @' + pos + ' back');
      }
    }
  }
});

test('just after a closed tag is SQL; the end of an unclosed tag is not', () => {
  const closed = "select {{ ref('a') }} from t";
  const end = closed.indexOf('}}') + 2;
  const state = cm.EditorState.create({ doc: closed });
  assert.equal(inTemplate(state, end - 1), true);
  assert.equal(inTemplate(state, end), false);
  assert.equal(inTemplate(state, closed.indexOf('{{')), false);
  assert.equal(inTemplate(state, closed.indexOf('{{') + 1), true);

  const open = "select {{ ref('a'";
  assert.equal(inTemplate(cm.EditorState.create({ doc: open }), open.length), true);
  const comment = 'select {# note';
  assert.equal(inTemplate(cm.EditorState.create({ doc: comment }), comment.length), true);
});

test('an edit is a new doc, read afresh', () => {
  let state = cm.EditorState.create({ doc: 'select {{ x }} from t' });
  assert.equal(inTemplate(state, 14), false);
  state = state.update({ changes: { from: 13, to: 14 } }).state; // drop the last }
  assert.equal(state.doc.toString(), 'select {{ x } from t');
  for (let pos = 0; pos <= state.doc.length; pos++) assert.equal(inTemplate(state, pos), want(state.doc.toString(), pos), '@' + pos);
});
