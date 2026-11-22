// A recent commit's row: its author as a badge of initials, and a merge
// as the branch it brought in.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { initials, commitSubject, parseCommit, layoutCommitGraph, compactRefs } from '../assets/scm.js';

test('initials are the first and last names\' first letters', () => {
  assert.equal(initials('Kanmi Oyebola'), 'KO');
  assert.equal(initials('mary jane watson'), 'MW');
  assert.equal(initials('  Kanmi   Oyebola  '), 'KO');
});

test('a one-word name gives its first two letters', () => {
  assert.equal(initials('Sam'), 'SA');
  assert.equal(initials('ashish10alex'), 'AS');
  assert.equal(initials('J'), 'J');
});

test('no name, no initials', () => {
  assert.equal(initials(''), '');
  assert.equal(initials('   '), '');
  assert.equal(initials(undefined), '');
});

test('a merge of a branch says the branch', () => {
  assert.deepEqual(commitSubject("Merge branch 'etracker_holds'"), { merge: true, text: 'etracker_holds' });
  assert.deepEqual(commitSubject("Merge branch 'etracker_holds' into 'main'"), { merge: true, text: 'etracker_holds' });
  assert.deepEqual(commitSubject("Merge branch 'feat/x' into main"), { merge: true, text: 'feat/x' });
  assert.deepEqual(commitSubject("Merge branch 'main' of github.com:o/r"), { merge: true, text: 'main' });
});

test('a merge of a remote-tracking branch says it', () => {
  assert.deepEqual(commitSubject("Merge remote-tracking branch 'origin/main'"), { merge: true, text: 'origin/main' });
});

test('a GitHub pull request\'s merge says its branch', () => {
  assert.deepEqual(commitSubject('Merge pull request #42 from octo/fix-login'), { merge: true, text: 'fix-login' });
  assert.deepEqual(commitSubject('Merge pull request #7 from octo/feat/nested'), { merge: true, text: 'feat/nested' });
});

test('any other subject is as it is', () => {
  assert.deepEqual(commitSubject('Fix the parser'), { merge: false, text: 'Fix the parser' });
  assert.deepEqual(commitSubject('Merge the two lists'), { merge: false, text: 'Merge the two lists' });
  assert.deepEqual(commitSubject(''), { merge: false, text: '' });
});

test('parseCommit splits conventional commit type/scope and merge branches', () => {
  assert.deepEqual(parseCommit('feat(web): add blame minimap'), { merge: false, type: 'feat', scope: 'web', breaking: false, text: 'add blame minimap' });
  assert.deepEqual(parseCommit('chore(A2R): use current calendar'), { merge: false, type: 'chore', scope: 'A2R', breaking: false, text: 'use current calendar' });
  assert.deepEqual(parseCommit('fix!: drop legacy table'), { merge: false, type: 'fix', scope: '', breaking: true, text: 'drop legacy table' });
  assert.deepEqual(parseCommit("Merge branch 'inv_dev' into 'develop'"), { merge: true, type: '', scope: '', breaking: false, text: 'inv_dev' });
  assert.deepEqual(parseCommit('Migrate DPR to Order Processing'), { merge: false, type: '', scope: '', breaking: false, text: 'Migrate DPR to Order Processing' });
});

test('layoutCommitGraph places side branches and merges on smooth lanes', () => {
  // M (merge of B into A) -> B (side branch) -> A (base)
  const g = layoutCommitGraph([
    { sha: 'M', parents: ['A', 'B'] },
    { sha: 'B', parents: ['A'] },
    { sha: 'A', parents: [] },
  ]);
  assert.equal(g.maxCols, 2);
  assert.equal(g.rows[0].lane, 0);
  assert.equal(g.rows[0].merge, true);
  assert.equal(g.rows[1].lane, 1);
  // B curves back into A's lane (0)
  assert.ok(g.rows[1].segs.some((s) => s.kind === 'out' && s.from === 1 && s.to === 0));
  assert.equal(g.rows[2].lane, 0);
});

test('compactRefs folds matching remote ref into a compact cloud badge', () => {
  const refs = compactRefs([
    { name: 'archive_proj', kind: 'head' },
    { name: 'origin/archive_proj', kind: 'remote' },
    { name: 'origin/develop', kind: 'remote' },
  ]);
  assert.deepEqual(refs.map((r) => [r.name, r.compact]), [
    ['archive_proj', false],
    ['origin/archive_proj', true],
    ['origin/develop', false],
  ]);
});
