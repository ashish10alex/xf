// cmd+shift+p in the Scratch view opens the palette on Scratch's own
// commands (scratch.js's scratchCommands), not the project's.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, pickFromPalette, openScratchPalette, cancelPalette, paletteList, commandQuery, fileQuery, actionQuery, symbolQuery, projectQuery, openPalette, setPaletteQuery } from '../assets/state.js';
import { SC } from '../assets/scratch.js';

beforeEach(() => {
  Object.assign(SC, {
    open: true, standalone: false, focus: 'results', cur: { id: 'scratch_1', project: 'p', location: 'EU', sql: 'SELECT 1' },
    run: null, sel: null, filters: {}, sort: null, recent: [], listOpen: false, hideResults: false,
    leaveAsk: '', confirm: null, delAsk: '', recentOpen: '',
  });
  S.overlay = null;
  S.view = null; S.sel = null;
});

const titles = () => paletteList().map((c) => c.title);

test('it lists Scratch commands only, with or without the >', () => {
  openScratchPalette();
  assert.equal(S.overlay, 'palette');
  assert.ok(S.palette.scratch);
  const all = titles();
  assert.ok(all.includes('Run: query (the selection, else the file)'));
  assert.ok(all.includes('Scratch: delete this scratch'));
  assert.ok(all.includes('Project: switch or add'));
  assert.ok(!all.includes('Check: compile'), 'a project command');
  for (const q of ['>del', 'del', '  del']) {
    setPaletteQuery(q);
    assert.equal(commandQuery(), 'del', q);
    for (const f of [fileQuery, actionQuery, symbolQuery, projectQuery]) assert.equal(f(), null, q);
    assert.deepEqual(titles(), ['Scratch: delete this scratch'], q);
  }
  cancelPalette();
  assert.equal(S.overlay, null);
});

test('the project palette is itself again after it', () => {
  openScratchPalette();
  cancelPalette();
  SC.open = false;
  openPalette('>');
  assert.equal(S.palette.scratch, false);
  setPaletteQuery('');
  assert.equal(fileQuery(), '');
  S.overlay = null;
});

test('the commands follow what the view shows', () => {
  openScratchPalette();
  let all = titles();
  for (const t of ['Results: clear filters', 'Copy: cell', 'View: results', 'Run: cancel']) assert.ok(!all.includes(t), t);
  SC.run = { phase: 'done', job: { project: 'p', location: 'EU', id: 'j1' }, schema: [{ name: 'a', type: 'INT64' }], rows: [['1']] };
  SC.filters = { 0: '1' };
  SC.sel = { r: 0, c: 0 };
  openScratchPalette();
  all = titles();
  for (const t of ['Results: clear filters', 'Copy: cell', 'Copy: row as JSON', 'View: results', 'Results: open the job in BigQuery']) assert.ok(all.includes(t), t);
});

test('standalone, nothing goes back to a project', () => {
  SC.standalone = true;
  openScratchPalette();
  const all = titles();
  assert.ok(!all.includes('Project: switch or add'));
  assert.ok(!all.includes('Close scratch: back to the project'));
  assert.ok(all.includes('Scratch: new empty query'));
});

test('a pick runs the command and closes the palette', () => {
  openScratchPalette();
  setPaletteQuery('scratch list');
  assert.equal(SC.listOpen, false);
  pickFromPalette(0);
  assert.equal(SC.listOpen, true);
  assert.equal(S.overlay, null);
});
