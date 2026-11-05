// The column's projects tab: a click on an open project opens it here or
// in a new tab, as webOpenProjectIn says (shift+click does the other), and
// the column shows the tab in the project it opens.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { cycleProject, nextOpenProject, switchProject } from '../assets/picker.js';
import { T, key, projectRows, toggleProjects } from '../assets/tree.js';

globalThis.fetch = async () => new Response('{}', { headers: { 'Content-Type': 'application/json' } });

let opened;
beforeEach(() => {
  opened = [];
  globalThis.open = (url) => { opened.push(url); return {}; };
  globalThis.location = { href: '/?project=finance' };
  localStorage.clear();
  Object.assign(S, {
    mode: 'project', key: 'finance', openIn: 'same-tab', switching: false, flash: '',
    open: [{ key: 'finance', name: 'finance', root: '/r/finance' }, { key: 'shop', name: 'shop', root: '/r/shop' }],
  });
});

const tick = () => new Promise((r) => setTimeout(r, 0));
const column = (root) => JSON.parse(localStorage.getItem('xf-web:tree:' + root) || '{}');

test('a click opens the project in this tab, with the column on the projects tab', async () => {
  switchProject('shop', false);
  await tick();
  assert.equal(location.href, '/?project=shop');
  assert.deepEqual(opened, []);
  assert.deepEqual(column('/r/shop'), { shown: true, view: 'projects' });
});

test('shift+click opens it in a new tab', async () => {
  switchProject('shop', true);
  await tick();
  assert.deepEqual(opened, ['/?project=shop']);
  assert.equal(location.href, '/?project=finance');
});

test('with webOpenProjectIn new-tab, a click opens a new tab and shift+click here', async () => {
  S.openIn = 'new-tab';
  switchProject('shop', false);
  await tick();
  assert.deepEqual(opened, ['/?project=shop']);
  switchProject('shop', true);
  await tick();
  assert.equal(location.href, '/?project=shop');
});

test('this tab\'s project, or one not open, does nothing', async () => {
  switchProject('finance', false);
  switchProject('gone', false);
  await tick();
  assert.equal(location.href, '/?project=finance');
  assert.deepEqual(opened, []);
});

test('showing the tab keeps the column\'s other settings', async () => {
  localStorage.setItem('xf-web:tree:/r/shop', JSON.stringify({ shown: false, view: 'scm', width: 300 }));
  switchProject('shop', false);
  await tick();
  assert.deepEqual(column('/r/shop'), { shown: true, view: 'projects', width: 300 });
});

test('the rows are by name, whatever order xf web sends', () => {
  S.open = [{ key: 'shop', name: 'shop' }, { key: 'Finance', name: 'Finance' }, { key: 'ads', name: 'ads' }];
  assert.deepEqual(projectRows().map((o) => o.key), ['ads', 'Finance', 'shop']);
});

test('j/k move from this tab\'s project and enter opens the row', async () => {
  S.boot = { root: '/r/finance', name: 'finance' };
  localStorage.setItem('xf-web:tree:/r/finance', JSON.stringify({ shown: true, view: 'projects' }));
  key('j');
  assert.equal(T.projCursor, 'shop');
  key('j');
  assert.equal(T.projCursor, 'shop');
  key('enter');
  await tick();
  assert.equal(location.href, '/?project=shop');
  key('k');
  assert.equal(T.projCursor, 'finance');
});

test('next open project goes round the rows by name, in this tab', async () => {
  S.open = [{ key: 'shop', name: 'shop' }, { key: 'finance', name: 'finance' }, { key: 'ads', name: 'ads' }];
  S.openIn = 'new-tab';
  assert.equal(nextOpenProject().key, 'shop');
  cycleProject();
  await tick();
  assert.equal(location.href, '/?project=shop');
  S.key = 'shop';
  cycleProject();
  await tick();
  assert.equal(location.href, '/?project=ads');
  assert.deepEqual(opened, []);
});

test('with no other project open, cycling stays and says so', async () => {
  S.open = [{ key: 'finance', name: 'finance' }];
  assert.equal(nextOpenProject(), null);
  cycleProject();
  await tick();
  assert.equal(location.href, '/?project=finance');
  assert.match(S.flash, /no other project/);
});

test('cmd+shift+w shows the projects tab with the keys, and again hides it', () => {
  S.boot = { root: '/r/finance', name: 'finance' };
  toggleProjects(); // restores the column for /r/finance first
  Object.assign(T, { shown: false, view: 'files' });
  S.focus = 'sql';
  toggleProjects();
  assert.deepEqual([T.shown, T.view, S.focus], [true, 'projects', 'tree']);
  toggleProjects();
  assert.equal(T.shown, false);
  assert.notEqual(S.focus, 'tree');
});
