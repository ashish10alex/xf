// cmd+j hides the Scratch results and gives the keys back to the editor,
// in normal mode, from wherever they were; showing them again leaves the
// keys where they are. The SQL tab is the run's own.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { SC, setEditor, toggleResults, setResultTab } from '../assets/scratch.js';

let calls;
beforeEach(() => {
  calls = [];
  setEditor({ normal: () => calls.push('normal'), focus: () => calls.push('focus'), blur() {} });
  Object.assign(SC, { open: true, run: { sql: 'select 1', phase: 'done' }, hideResults: false, focus: 'results' });
});

const tick = () => new Promise((r) => setTimeout(r, 0));

test('hiding the results focuses the editor in normal mode', async () => {
  toggleResults();
  assert.equal(SC.hideResults, true);
  assert.equal(SC.focus, 'editor');
  await tick();
  assert.deepEqual(calls, ['normal', 'focus']);
});

test('showing them again leaves the keys where they are', async () => {
  SC.hideResults = true;
  SC.focus = 'editor';
  toggleResults();
  assert.equal(SC.hideResults, false);
  assert.equal(SC.focus, 'editor');
  await tick();
  assert.deepEqual(calls, []);
});

test('the SQL tab belongs to the run, so a new run opens on its rows', () => {
  setResultTab('sql');
  assert.equal(SC.run.tab, 'sql');
  SC.run = { sql: 'select 2', phase: 'starting' };
  assert.equal(SC.run.tab, undefined);
});
