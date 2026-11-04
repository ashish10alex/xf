// The picker finds the projects in a folder, as xf search does: enter on
// a folder that is no project lists the projects below it as they come,
// each ticked unless the picker lists it already, and enter adds the
// ticked ones.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { pickerKey, stopFind } from '../assets/picker.js';

// answer fakes the server: the folder search's lines, the add, and the
// boot that lists the projects after it.
let asked = [];
function answer({ lines, picker }) {
  asked = [];
  globalThis.fetch = async (path, opts = {}) => {
    asked.push({ path, body: opts.body ? JSON.parse(opts.body) : null });
    if (path === '/api/picker/find') {
      const text = lines.map((l) => JSON.stringify(l)).join('\n') + '\n';
      return new Response(text, { status: 200 });
    }
    if (path === '/api/picker/add') {
      const n = JSON.parse(opts.body).projects.length;
      return new Response(JSON.stringify({ added: n }), { status: 200 });
    }
    if (path.startsWith('/api/boot')) return new Response(JSON.stringify({ picker, total: picker.length }), { status: 200 });
    return new Response('{}', { status: 200 });
  };
}

const settle = () => new Promise((r) => setTimeout(r, 10));

beforeEach(() => {
  stopFind(true);
  Object.assign(S, { mode: 'picker', key: '', overlay: null, canFind: true, pickerQuery: '~/repos', pickerCursor: 0, pickerConfirm: null, flash: '',
    paths: [{ root: '/h/repos', path: '~/repos', search: true, note: 'not a project' }] });
});

test('enter on a folder that is no project finds the projects in it, and adds the ticked', async () => {
  answer({
    lines: [
      { found: { root: '/h/repos/shop', path: '~/repos/shop', name: 'shop', backend: 'dataform' } },
      { found: { root: '/h/repos/old', path: '~/repos/old', name: 'old', backend: 'dbt', known: true } },
      { found: { root: '/h/repos/ads', path: '~/repos/ads', name: 'ads', backend: 'dbt' } },
      { done: true },
    ],
    picker: [{ root: '/h/repos/old', name: 'old' }, { root: '/h/repos/ads', name: 'ads' }, { root: '/h/repos/shop', name: 'shop' }],
  });
  pickerKey('enter');
  assert.ok(S.find && S.find.searching, 'the search shows, going');
  assert.deepEqual(asked[0], { path: '/api/picker/find', body: { path: '~/repos' } });
  await settle();
  const f = S.find;
  assert.equal(f.searching, false);
  assert.deepEqual(f.rows.map((r) => r.name + (r.on ? ' ✓' : '')), ['shop ✓', 'old', 'ads ✓']);
  // One listed already can't be ticked; space unticks the others.
  pickerKey('down');
  pickerKey('space');
  assert.equal(f.rows[1].on, false);
  pickerKey('down');
  pickerKey('space');
  assert.equal(f.rows[2].on, false);
  // ctrl+a ticks every one that isn't listed, or none when all are.
  pickerKey('ctrl+a');
  assert.deepEqual(f.rows.map((r) => r.on), [true, false, true]);
  pickerKey('ctrl+a');
  assert.deepEqual(f.rows.map((r) => r.on), [false, false, false]);
  pickerKey('enter');
  assert.match(S.flash, /none ticked/);
  assert.ok(S.find, 'nothing ticked keeps the search');
  pickerKey('space'); // ads, under the cursor
  pickerKey('enter');
  await settle();
  assert.deepEqual(asked.find((a) => a.path === '/api/picker/add').body, { projects: [{ root: '/h/repos/ads', backend: 'dbt' }] });
  assert.equal(S.find, null, 'back to the picker');
  assert.equal(S.pickerQuery, '');
  assert.equal(S.picker[S.pickerCursor].root, '/h/repos/ads', 'the cursor is on the project added');
  assert.ok(S.pickerNew.has('/h/repos/ads'));
  assert.match(S.flash, /added 1 project from ~\/repos/);
});

test('esc stops a search going, keeping what it found, then goes back', () => {
  globalThis.fetch = () => new Promise(() => {}); // never answers
  pickerKey('enter');
  assert.ok(S.find.searching);
  pickerKey('esc');
  assert.ok(S.find && !S.find.searching, 'stopped, still shown');
  pickerKey('esc');
  assert.equal(S.find, null);
  assert.equal(S.pickerQuery, '~/repos', 'back to the folder typed');
});

test('a folder that is a project still opens', () => {
  S.paths = [{ root: '/h/repos/shop', path: '~/repos/shop', backend: 'dataform' }];
  asked = [];
  globalThis.fetch = (path) => { asked.push(path); return new Promise(() => {}); }; // the page doesn't go anywhere
  pickerKey('enter');
  assert.equal(S.find, null);
  assert.deepEqual(asked, ['/api/pick']);
});
