// The settings page has two views: the Overview, the info rows under a
// line of how things stand, and the Settings, whose rows c narrows to
// those the scope's file sets. o and s switch between them.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SET, settingsKey, rows, changedCount, viewShown } from '../assets/settings.js';
import { statusItems, infoGroups } from '../assets/settings-view.js';

const project = [
  { title: 'project' }, { label: 'root', value: '/work/analytics' }, { label: 'backend', value: 'dataform', note: 'detected' },
  { title: 'binaries' }, { label: 'xf', value: '/go/bin/xf' }, { value: 'devel abc123' },
  { label: 'dataform', value: '/usr/local/bin/dataform', note: 'PATH' }, { value: '3.1.2' },
  { title: 'settings' }, { label: 'runMode', value: 'cli', note: 'default' },
  { title: 'google cloud' }, { label: 'xf uses', value: 'me@example.com' }, { value: 'user login' }, { label: 'quota', value: 'proj-1' },
  { title: 'git' }, { label: 'branch', value: 'feature/x' }, { label: 'origin', value: 'git@github.com:acme/a.git' },
  { label: 'compares with', value: 'develop', note: 'R looks for what changed since it' },
  { title: 'editor' }, { label: 'connected', value: 'no', note: 'no editor has said which file is active' }, { label: 'o opens with', value: 'code' },
];

const titles = (items) => items.map((i) => i.state + ' ' + i.title);

test('the status line sums up the tool, the sign-in, the branch and the editor', () => {
  const items = statusItems(project);
  assert.deepEqual(titles(items), ['ok dataform 3.1.2', 'ok Signed in', 'ok feature/x', 'off No editor connected']);
  assert.equal(items[1].note, 'me@example.com');
  assert.equal(items[2].note, 'compares with develop');
  assert.equal(items[3].note, 'o opens code');
});

test('a sign-in that needs attention is amber, with the account alone', () => {
  const rows = [{ title: 'google cloud' },
    { label: 'xf uses', value: 'me@example.com (reauthentication required)', warn: true },
    { value: 'run `gcloud auth application-default login`', warn: true }];
  const [item] = statusItems(rows);
  assert.equal(item.state, 'warn');
  assert.equal(item.note, 'me@example.com');
});

test('lookups still going wait; a missing tool is amber only when the rows say so', () => {
  const going = [{ title: 'binaries' }, { label: 'dataform', value: '/bin/dataform' }, { value: '…' },
    { title: 'google cloud' }, { label: 'xf uses', value: '…' }, { title: 'git' }, { label: 'branch', value: '…' }];
  assert.deepEqual(statusItems(going).map((i) => i.state), ['wait', 'wait', 'wait']);
  const missing = [{ title: 'binaries' }, { label: 'dataform', value: '(not found)', warn: true }, { label: 'dbt', value: '(not on PATH)', note: 'pip install dbt-bigquery' }];
  assert.deepEqual(titles(statusItems(missing)), ['warn dataform not found', 'off dbt not found']);
});

test("a version line that names its tool shows the number alone", () => {
  const rows = [{ title: 'binaries' }, { label: 'dbt', value: '/bin/dbt' }, { value: 'dbt 2.0.8' },
    { label: 'gcloud', value: '/bin/gcloud' }, { value: 'Google Cloud SDK 571.0.0' }];
  assert.deepEqual(titles(statusItems(rows)), ['ok dbt 2.0.8', 'ok gcloud 571.0.0']);
});

test('the Overview leaves the settings rows to the Settings', () => {
  assert.deepEqual(infoGroups(project).map((g) => g.title), ['project', 'binaries', 'google cloud', 'git', 'editor']);
});

const key = (k, user, project) => ({ key: k, title: k, section: 'Running', doc: '', hint: '', kind: 'text', value: '', from: 'default', default: '', user, project, flag: null, live: true });

beforeEach(() => {
  Object.assign(S, { mode: 'project', caps: { info: true } });
  Object.assign(SET, {
    open: true, view: 'overview', only: 'all', tab: 'user', mode: 'ui', query: '', typing: false, leaveAsk: false, closing: false, cursor: 0, json: {},
    data: { root: '/work', levels: [{ level: 'user', path: '/u.json' }, { level: 'project', path: '.xf/config.json' }], warnings: [], pending: [],
      keys: [key('runMode', null, null), key('defaultBranch', 'develop', null), key('editor', null, 'zed')] },
  });
});

test('o and s switch between the Overview and the Settings', () => {
  settingsKey('s');
  assert.equal(viewShown(), 'settings');
  settingsKey('o');
  assert.equal(viewShown(), 'overview');
});

test('without info to show, the page is the Settings alone', () => {
  S.caps = { info: false };
  assert.equal(viewShown(), 'settings');
  settingsKey('o');
  assert.equal(viewShown(), 'settings');
});

test("c shows only what the scope's file sets, and again all", () => {
  SET.view = 'settings';
  settingsKey('c');
  assert.deepEqual(rows().map((k) => k.key), ['defaultBranch']);
  assert.equal(changedCount(), 1);
  SET.tab = 'project';
  assert.deepEqual(rows().map((k) => k.key), ['editor']);
  settingsKey('c');
  assert.equal(rows().length, 3);
});

test('c on the Overview shows the changed settings', () => {
  settingsKey('c');
  assert.equal(viewShown(), 'settings');
  assert.equal(SET.only, 'changed');
});
