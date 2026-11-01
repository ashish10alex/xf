// gc and cmd+/ comment lines out and back in, in each file's own way.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { languageFor } from '../assets/sqlx.js';
import { toggleLines } from '../assets/comment.js';

// toggle is text with lines from to to (1-based) toggled, in lang.
function toggle(lang, text, from, to = from) {
  const state = cm.EditorState.create({ doc: text, extensions: languageFor(lang, false) });
  const changes = toggleLines(state, from, to);
  return changes ? state.update({ changes }).state.doc.toString() : null;
}

test('sql and sqlx comment out with --, at the least indent, blank lines kept', () => {
  for (const lang of ['sql', 'sqlx']) {
    assert.equal(toggle(lang, 'select a,\n\n    b\n  from t', 1, 4), '-- select a,\n\n--     b\n--   from t');
    assert.equal(toggle(lang, '  select 1\n  from t', 1, 2), '  -- select 1\n  -- from t');
  }
});

test('sqlx brings back lines commented with --, # or /* */', () => {
  assert.equal(toggle('sqlx', '-- select 1\n  # from t', 1, 2), 'select 1\n  from t');
  assert.equal(toggle('sqlx', '--select 1', 1), 'select 1');
  assert.equal(toggle('sqlx', '/* select 1 */', 1), 'select 1');
  assert.equal(toggle('sqlx', '/*\nselect 1\nfrom t\n*/', 1, 4), 'select 1\nfrom t');
  assert.equal(toggle('sqlx', '/* select 1\nfrom t */', 1, 2), 'select 1\nfrom t');
  // Two comments on a line are not one around it: it is commented out.
  assert.equal(toggle('sqlx', '/* a */ select 1 /* b */', 1), '-- /* a */ select 1 /* b */');
  // Some lines commented, some not: all are commented out.
  assert.equal(toggle('sqlx', '-- a\nb', 1, 2), '-- -- a\n-- b');
});

test("a .sqlx file's config { } comments out as JavaScript", () => {
  const text = 'config {\n  type: "table",\n  tags: ["a"]\n}\nselect 1';
  const out = toggle('sqlx', text, 2, 3);
  assert.equal(out, 'config {\n  // type: "table",\n  // tags: ["a"]\n}\nselect 1');
  assert.equal(toggle('sqlx', out, 2, 3), text);
  // The whole block, its config line being SQL's, comments out with --.
  assert.equal(toggle('sqlx', text, 1, 4), '-- config {\n--   type: "table",\n--   tags: ["a"]\n-- }\nselect 1');
});

test('js comments out with //, python with #', () => {
  assert.equal(toggle('js', 'const a = 1;\nmodule.exports = { a };', 1, 2), '// const a = 1;\n// module.exports = { a };');
  assert.equal(toggle('js', '// const a = 1;', 1), 'const a = 1;');
  assert.equal(toggle('js', '/* const a = 1; */', 1), 'const a = 1;');
  assert.equal(toggle('python', 'def f():\n    return 1', 1, 2), '# def f():\n#     return 1');
  assert.equal(toggle('python', '# def f():\n#     return 1', 1, 2), 'def f():\n    return 1');
});

test('a blank line gets a comment, and a file without comments is left alone', () => {
  assert.equal(toggle('sql', '', 1), '-- ');
  assert.equal(toggle('sql', '-- ', 1), '');
  assert.equal(toggle('json', '{"a": 1}', 1), null);
});
