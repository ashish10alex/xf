import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EditorState } from '../assets/vendor/codemirror.mjs';
import { replaceWith, formatText } from '../assets/editor.js';

// view stands in for an EditorView: the state and dispatch of one.
function fakeView(doc, anchor) {
  const v = {
    state: EditorState.create({ doc, selection: { anchor } }),
    dispatched: 0,
    dispatch(spec) { v.dispatched++; v.state = v.state.update(spec).state; },
  };
  return v;
}

test('format changes only what differs, and the cursor stays on its text', () => {
  const before = 'select a,b from t\nwhere x=1';
  const after = 'SELECT a\n, b\nFROM t\nWHERE x = 1';
  const v = fakeView(before, before.indexOf(' t') + 1); // on the t, which no change touches
  replaceWith(v, after);
  assert.equal(v.state.doc.toString(), after);
  assert.equal(v.dispatched, 1, 'one transaction is one undo step');
  assert.equal(v.state.doc.sliceString(v.state.selection.main.head, v.state.selection.main.head + 1), 't');
});

test('the same text dispatches nothing', () => {
  const v = fakeView('select 1', 0);
  replaceWith(v, 'select 1');
  assert.equal(v.dispatched, 0);
});

const editorOf = (v) => ({ view: v, doc: () => v.state.doc.toString() });

test('format says what happened and applies the answer', async () => {
  const v = fakeView('select 1', 0);
  const said = [];
  const ok = await formatText(editorOf(v), async (text) => ({ text: text.toUpperCase(), changed: true }), (m) => said.push(m));
  assert.equal(ok, true);
  assert.equal(v.state.doc.toString(), 'SELECT 1');
  assert.deepEqual(said, ['formatting…', 'formatted: u undoes it']);
});

test('format on formatted text says so and changes nothing', async () => {
  const v = fakeView('SELECT 1', 0);
  const said = [];
  await formatText(editorOf(v), async (text) => ({ text, changed: false }), (m) => said.push(m));
  assert.equal(v.dispatched, 0);
  assert.equal(said.at(-1), 'already formatted');
});

test('a part left as it was is said, and the rest applied', async () => {
  const v = fakeView('config {type:"view"}', 0);
  const said = [];
  const note = 'formatted the config block, not the SQL: sqlfluff not found: uv tool install sqlfluff';
  await formatText(editorOf(v), async () => ({ text: 'config {\n  type: "view"\n}', changed: true, note }), (m) => said.push(m));
  assert.equal(v.state.doc.toString(), 'config {\n  type: "view"\n}');
  assert.equal(said.at(-1), note + ' (u undoes it)');
});

test('an error is said and the text stays', async () => {
  const v = fakeView('select (', 0);
  const said = [];
  const ok = await formatText(editorOf(v), async () => { throw new Error('line 1, column 8: Found unparsable section'); }, (m) => said.push(m));
  assert.equal(ok, false);
  assert.equal(v.state.doc.toString(), 'select (');
  assert.equal(said.at(-1), 'line 1, column 8: Found unparsable section');
});

test('text typed while sqlfluff ran is kept, and the answer dropped', async () => {
  const v = fakeView('select 1', 0);
  const said = [];
  await formatText(editorOf(v), async (text) => {
    v.dispatch({ changes: { from: 8, insert: ' ' } });
    return { text: text.toUpperCase(), changed: true };
  }, (m) => said.push(m));
  assert.equal(v.state.doc.toString(), 'select 1 ');
  assert.equal(said.at(-1), 'typed while formatting: not applied');
});

test('empty text is not sent', async () => {
  const v = fakeView('  \n', 0);
  let called = false;
  await formatText(editorOf(v), async () => { called = true; return {}; }, () => {});
  assert.equal(called, false);
});
