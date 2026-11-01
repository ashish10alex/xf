import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { moveBranches, newBranchName } from '../assets/branches.js';

const switcher = (query, names) => ({ query, listedFor: query, loading: false, cursor: 0,
  list: names.map((name) => ({ name })) });

test('a name no branch has is offered as a new branch, spaces as dashes', () => {
  assert.equal(newBranchName(switcher('my feature', ['main'])), 'my-feature');
  assert.equal(newBranchName(switcher('  fix ', [])), 'fix');
});

test('no new branch for an existing name, an empty query, or a list of another query', () => {
  assert.equal(newBranchName(switcher('main', ['main', 'main-old'])), '');
  assert.equal(newBranchName(switcher('  ', ['main'])), '');
  assert.equal(newBranchName({ ...switcher('mai', ['main']), listedFor: 'ma' }), '');
  assert.equal(newBranchName({ ...switcher('new', []), loading: true }), '');
});

test('the cursor reaches the new-branch row below the list', () => {
  S.branches = switcher('feat', ['feature-a', 'feature-b']);
  moveBranches(5);
  assert.equal(S.branches.cursor, 2);
  S.branches = switcher('feature-a', ['feature-a']);
  moveBranches(5);
  assert.equal(S.branches.cursor, 0);
  S.branches = null;
});
