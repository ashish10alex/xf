import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EditorState } from '../assets/vendor/codemirror.mjs';
import { inlineBlame, askBlame, setBlame, setBlameInline, blameAt, blameText, summarizeBlame, ago } from '../assets/blame.js';

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

test('summarizeBlame groups contiguous lines by person, uncommitted first then most recent', () => {
  const data = {
    lines: ['a1', 'a1', 'a2', 'b1', 'b1', '', 'a2', 'a2', 'c1'],
    commits: {
      a1: { author: 'Alice', mail: 'Alice@Co', time: 1000, summary: 'initial' },
      a2: { author: 'Alice Chen', mail: 'alice@co', time: 3000, summary: 'fix filter' },
      b1: { author: 'Bob', mail: 'bob@co', time: 2000, summary: 'add join' },
      c1: { author: 'Me', mail: 'me@co', time: 4000, summary: 'footer' },
    },
    you: 'me@co',
  };
  let s = EditorState.create({ doc: '1\n2\n3\n4\n5\n6\n7\n8\n9\n', extensions: inlineBlame(false) });
  assert.equal(summarizeBlame(s), null);
  s = s.update({ effects: askBlame.of(null) }).state;
  s = s.update({ effects: setBlame.of(data) }).state;

  const sum = summarizeBlame(s);
  assert.equal(sum.totalLines, 9);
  assert.deepEqual(sum.people.map((p) => p.author), ['Uncommitted', 'Me', 'Alice Chen', 'Bob']);

  const [fresh, me, alice, bob] = sum.people;
  assert.equal(fresh.fresh, true);
  assert.deepEqual(fresh.ranges.map((r) => [r.start, r.end]), [[6, 6]]);
  assert.equal(me.you, true);
  assert.deepEqual(me.ranges.map((r) => [r.start, r.end]), [[9, 9]]);
  assert.equal(alice.lines, 5);
  assert.equal(alice.summary, 'fix filter');
  assert.deepEqual(alice.ranges.map((r) => [r.start, r.end, r.summary]), [
    [1, 3, 'fix filter'],
    [7, 8, 'fix filter'],
  ]);
  assert.deepEqual(bob.ranges.map((r) => [r.start, r.end]), [[4, 5]]);

  // An edit shifts downstream ranges and marks the edited line uncommitted live.
  s = s.update({ changes: { from: 0, insert: 'top\n' } }).state;
  const after = summarizeBlame(s);
  assert.equal(after.totalLines, 10);
  assert.deepEqual(after.people[0].ranges.map((r) => [r.start, r.end]), [[1, 1], [7, 7]]);
  assert.deepEqual(after.people.find((p) => p.author === 'Bob').ranges.map((r) => [r.start, r.end]), [[5, 6]]);

  // Toggling inline visibility keeps the blame data for the summary.
  s = s.update({ effects: setBlameInline.of(true) }).state;
  assert.equal(summarizeBlame(s).totalLines, 10);
});
