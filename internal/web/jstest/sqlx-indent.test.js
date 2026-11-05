// Enter in a .sqlx file's config { } or js { } indents the new line as
// JavaScript would: one unit in from the block's line, and a closing
// brace back to it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { languageFor } from '../assets/sqlx.js';

const enter = cm.defaultKeymap.find((b) => b.key === 'Enter').run;

// indentAfter is the indentation Enter gives a new line after each line.
function indentAfter(doc) {
  const base = cm.EditorState.create({ doc, extensions: languageFor('sqlx', false) });
  const out = [];
  for (let i = 1; i <= base.doc.lines; i++) {
    const view = { state: base.update({ selection: { anchor: base.doc.line(i).to } }).state };
    view.dispatch = (tr) => { view.state = tr.state; };
    enter(view);
    out.push(view.state.doc.line(i + 1).text.length);
  }
  return out;
}

test('a new line in a config is indented one unit in', () => {
  assert.deepEqual(indentAfter(`config {
  type: "table",
  columns: docs.column_description,
  assertions: {
    uniqueKey: ["ORDER_NUMBER"]
  }
}
SELECT 1`), [2, 2, 2, 4, 4, 2, 0, 0]);
});

test('a nested object after a comma', () => {
  assert.deepEqual(indentAfter(`config {
  type: "incremental",
  uniqueKey: ["order_id"],
  bigquery: {
    partitionBy: "order_date",
    updatePartitionFilter: "x"
  }
}`), [2, 2, 2, 4, 4, 4, 2, 0]);
});

test('an indented config and a js block indent from their own line', () => {
  assert.deepEqual(indentAfter(`  config {
    type: "view"
  }
js {
  const a = 1;
}`), [4, 4, 2, 2, 2, 0]);
});

test('a closing brace goes back to its block line', () => {
  const doc = 'config {\n  type: "table",\n  }';
  const state = cm.EditorState.create({ doc, extensions: languageFor('sqlx', false) });
  // Reindenting the last line (as typing its brace does) puts it at 0.
  const line = state.doc.line(3);
  const view = { state: state.update({ selection: { anchor: line.to } }).state };
  view.dispatch = (tr) => { view.state = tr.state; };
  const reindent = cm.defaultKeymap.find((b) => b.key === 'Mod-Alt-\\').run;
  reindent(view);
  assert.equal(view.state.doc.line(3).text, '}');
});
