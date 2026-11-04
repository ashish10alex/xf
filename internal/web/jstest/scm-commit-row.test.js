// A recent commit's row: its author as a badge of initials, and a merge
// as the branch it brought in.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { initials, commitSubject } from '../assets/scm.js';

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
