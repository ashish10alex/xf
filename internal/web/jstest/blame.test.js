import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EditorState } from '../assets/vendor/codemirror.mjs';
import { inlineBlame, askBlame, setBlame, blameAt, blameText, ago } from '../assets/blame.js';

const answer = {
  lines: ['a1', 'a1', '', 'b2'],
  commits: {
    a1: { author: 'Ann', mail: 'ann@x', time: 1000, summary: 'first' },
    b2: { author: 'Bo', mail: 'me@x', time: 2000, summary: 'second' },
  },
  you: 'me@x',
};

const at = (s, n) => blameAt(s, s.doc.line(n));

// Edits made while the answer was on its way, and after it, move the lines
// it names; the lines edited are not committed.
test('blame follows the edits since it was asked for', () => {
  let s = EditorState.create({ doc: 'one\ntwo\nnew\nfour\n', extensions: inlineBlame() });
  assert.equal(at(s, 1), null, 'no answer yet');
  s = s.update({ effects: askBlame.of(null) }).state;
  // A line put in above, before the answer comes.
  s = s.update({ changes: { from: 0, insert: 'zero\n' } }).state;
  s = s.update({ effects: setBlame.of(answer) }).state;
  assert.deepEqual(at(s, 1), { fresh: true });
  assert.equal(at(s, 2).summary, 'first');
  assert.equal(at(s, 3).sha, 'a1');
  assert.deepEqual(at(s, 4), { fresh: true }, 'not committed in the answer');
  assert.equal(at(s, 5).summary, 'second');
  assert.equal(at(s, 5).you, true);
  assert.equal(at(s, 2).you, false);
  assert.equal(at(s, 6), null, 'the empty last line has no answer');

  // Typing in a line after the answer makes it not committed; the next
  // line keeps its commit.
  s = s.update({ changes: { from: s.doc.line(2).to, insert: '!' } }).state;
  assert.deepEqual(at(s, 2), { fresh: true });
  assert.equal(at(s, 3).sha, 'a1');
  // Lines removed move the ones below up.
  s = s.update({ changes: { from: s.doc.line(1).from, to: s.doc.line(3).from } }).state;
  assert.equal(at(s, 1).sha, 'a1');
  assert.equal(at(s, 3).summary, 'second');

  // A new line under one (vim's o) leaves that one as it was.
  s = s.update({ changes: { from: s.doc.line(1).to, insert: '\nx' } }).state;
  assert.equal(at(s, 1).sha, 'a1');
  assert.deepEqual(at(s, 2), { fresh: true });
  assert.deepEqual(at(s, 3), { fresh: true }, 'not committed in the answer');

  s = s.update({ effects: setBlame.of(null) }).state;
  assert.equal(at(s, 1), null, 'cleared');
});

test('the annotation says who, when and what', () => {
  const now = 1000 * 1000 + 3 * 86400 * 1000;
  assert.equal(blameText({ sha: 'a1b2c3d4e5', author: 'Ann', time: 1000, summary: 'first' }, now).text, 'Ann, 3 days ago • first');
  assert.equal(blameText({ sha: 'a1', author: 'Bo', time: 1000, summary: 's', you: true }, now).text.startsWith('You, '), true);
  assert.equal(blameText({ fresh: true }).text, 'You, Uncommitted changes');
  assert.equal(ago(0, 0), 'just now');
  assert.equal(ago(0, 61 * 1000), '1 minute ago');
  assert.equal(ago(0, 400 * 86400 * 1000), '1 year ago');
  assert.equal(ago(0, 65 * 86400 * 1000), '2 months ago');
});
