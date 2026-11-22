// Settings sections: server-owned order and sidebar groups, page-side
// warehouse filtering (keeping set, flagged, Changed, and searched keys
// visible with other: true), and collapsing all-userOnly sections on the
// Project tab.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SET, sectionsShown, rows, allCount, changedCount } from '../assets/settings.js';
import { F } from '../assets/forge.js';
import { SettingsBody, gitHostsFor } from '../assets/settings-view.js';

const SECTIONS = [
  { name: 'Dataform', group: 'Tool' },
  { name: 'dbt', group: 'Tool' },
  { name: 'BigQuery', group: 'Warehouse', warehouse: 'bigquery' },
  { name: 'Databricks', group: 'Warehouse', warehouse: 'databricks' },
  { name: 'Snowflake', group: 'Warehouse', warehouse: 'snowflake' },
  { name: 'Git', group: 'General' },
  { name: 'Editor', group: 'General' },
  { name: 'Appearance', group: 'General' },
  { name: 'App', group: 'General' },
  { name: 'Vim', group: 'General' },
];

const mk = (key, section, extra = {}) => ({
  key, title: key, section, doc: '', hint: '', kind: 'text', value: '', from: 'default', default: '',
  user: null, project: null, flag: null, live: true, userOnly: false, ...extra,
});

function dbtFixture(warehouse, overrides = {}) {
  const keys = [
    mk('dbtBin', 'dbt'),
    mk('dbtTarget', 'dbt'),
    mk('usdPerTiB', 'BigQuery', overrides.usdPerTiB),
    mk('lineageProjects', 'BigQuery', overrides.lineageProjects),
    mk('databricksWarehouse', 'Databricks', overrides.databricksWarehouse),
    mk('snowflakeWarehouse', 'Snowflake', overrides.snowflakeWarehouse),
    mk('defaultBranch', 'Git', overrides.defaultBranch),
    mk('diffStyle', 'Git', overrides.diffStyle),
    mk('forgePollSeconds', 'Git', { userOnly: true, ...overrides.forgePollSeconds }),
    mk('sourceAutosaveMs', 'Editor'),
    mk('webTheme', 'Appearance'),
    mk('editor', 'App'),
    mk('webBrowser', 'App', { userOnly: true }),
    mk('vimMode', 'Vim', { userOnly: true, ...overrides.vimMode }),
    mk('vimLeader', 'Vim', { userOnly: true }),
  ];
  return {
    root: warehouse ? '/work/dbt' : '',
    tool: warehouse ? 'dbt' : '',
    warehouse,
    levels: warehouse
      ? [{ level: 'user', path: '/u.json' }, { level: 'project', path: '.xf/config.json' }]
      : [{ level: 'user', path: '/u.json' }],
    warnings: [],
    pending: [],
    sections: SECTIONS,
    keys,
  };
}

beforeEach(() => {
  Object.assign(S, { mode: 'project', caps: { info: true } });
  Object.assign(SET, {
    open: true, view: 'settings', only: 'all', tab: 'user', mode: 'ui', query: '', typing: false,
    closing: false, cursor: 0, json: {}, data: dbtFixture('databricks'),
  });
  F.hosts = { hosts: [{ host: 'github.com', kind: 'github' }] };
});

test('inside a dbt+Databricks project, other warehouses are hidden when unset', () => {
  const secs = sectionsShown();
  assert.deepEqual(secs.map((s) => s.name), ['dbt', 'Databricks', 'Git', 'Editor', 'Appearance', 'App', 'Vim']);
  assert.equal(secs.some((s) => s.other), false);
  assert.equal(rows().some((k) => k.section === 'BigQuery' || k.section === 'Snowflake'), false);
  assert.equal(allCount(), rows().length);
});

test('outside a project (picker), every tool and warehouse section shows', () => {
  SET.data = dbtFixture('');
  SET.data.keys.unshift(mk('dataformBin', 'Dataform'));
  const secs = sectionsShown();
  assert.deepEqual(secs.map((s) => s.name), ['Dataform', 'dbt', 'BigQuery', 'Databricks', 'Snowflake', 'Git', 'Editor', 'Appearance', 'App', 'Vim']);
  assert.equal(secs.some((s) => s.other), false);
});

test('rule 3: a key set in the active file or by a flag stays visible in other warehouses', () => {
  SET.data = dbtFixture('databricks', {
    usdPerTiB: { user: '7.5' },
    snowflakeWarehouse: { flag: 'COMPUTE_WH' },
  });
  const secs = sectionsShown();
  const bq = secs.find((s) => s.name === 'BigQuery');
  const sf = secs.find((s) => s.name === 'Snowflake');
  assert.equal(bq && bq.other, true);
  assert.equal(sf && sf.other, true);
  // Only the set/flagged key from each other warehouse shows, not its unset siblings.
  assert.deepEqual(rows().filter((k) => k.section === 'BigQuery').map((k) => k.key), ['usdPerTiB']);
  assert.deepEqual(rows().filter((k) => k.section === 'Snowflake').map((k) => k.key), ['snowflakeWarehouse']);
  assert.equal(allCount(), rows().length);

  // Changed shows every set key, matching changedCount().
  SET.only = 'changed';
  assert.equal(changedCount(), 1);
  assert.deepEqual(rows().map((k) => k.key), ['usdPerTiB']);
  assert.equal(sectionsShown().some((s) => s.name === 'BigQuery' && s.other), true);
  assert.equal(sectionsShown().some((s) => s.name === 'Snowflake'), false);
});

test('rule 3: search finds hidden warehouse keys and marks their section as other', () => {
  SET.query = 'snowflake';
  const secs = sectionsShown();
  const sf = secs.find((s) => s.name === 'Snowflake');
  assert.equal(sf && sf.other, true);
  assert.deepEqual(rows().map((k) => k.key), ['snowflakeWarehouse']);
});

test('Project tab collapses all-userOnly sections (Vim) until searched', () => {
  SET.tab = 'project';
  const secs = sectionsShown();
  const vim = secs.find((s) => s.name === 'Vim');
  const app = secs.find((s) => s.name === 'App');
  assert.equal(vim && vim.collapsed, true);
  assert.equal(app && app.collapsed, false, 'App has project-editable editor');
  // Collapsed Vim rows are excluded from rows() so j/k cursor stays aligned.
  assert.equal(rows().some((k) => k.section === 'Vim'), false);
  assert.equal(allCount(), rows().length);

  // Searching uncollapses Vim so matching keys can be found and switched to.
  SET.query = 'vimLeader';
  const searched = sectionsShown().find((s) => s.name === 'Vim');
  assert.equal(searched && searched.collapsed, false);
  assert.deepEqual(rows().map((k) => k.key), ['vimLeader']);
});

// Walk a Preact vnode tree collecting every element of a matching component or class.
function findNodes(node, pred, out = []) {
  if (!node || typeof node !== 'object') return out;
  if (Array.isArray(node)) { node.forEach((c) => findNodes(c, pred, out)); return out; }
  if (pred(node)) out.push(node);
  if (node.props && node.props.children) findNodes(node.props.children, pred, out);
  return out;
}

const vtext = (n) => (n == null || typeof n === 'boolean' ? '' : typeof n !== 'object' ? String(n) : Array.isArray(n) ? n.map(vtext).join('') : vtext(n.props && n.props.children));

test('Changed view shows GitHosting only when forgePollSeconds is among changed rows', () => {
  SET.data = dbtFixture('databricks', { defaultBranch: { project: 'develop' } });
  SET.tab = 'project';
  SET.only = 'changed';
  assert.equal(gitHostsFor(rows(), ''), null);

  SET.data = dbtFixture('databricks', { forgePollSeconds: { user: '15' } });
  SET.tab = 'user';
  SET.only = 'changed';
  const hosts = gitHostsFor(rows(), '');
  assert.ok(Array.isArray(hosts) && hosts.length === 1);
});

test('SettingsBody places GitHosting before forgePollSeconds, hides Vim intro when collapsed, and marks other warehouses', () => {
  // 1. In Git, defaultBranch and diffStyle come before GitHosting, and forgePollSeconds after.
  const list = rows();
  const sections = sectionsShown();
  const tree = SettingsBody({ list, sections, hosts: gitHostsFor(list, '') });
  const gitSec = findNodes(tree, (n) => n.type === 'section' && findNodes(n, (c) => c.type === 'h2' && c.props.id === 'set-sec-git').length > 0)[0];
  const order = findNodes(gitSec, (n) => (n.props && n.props.k && n.props.k.key) || (typeof n.type === 'function' && n.type.name === 'GitHosting'))
    .map((n) => (n.props && n.props.k ? n.props.k.key : 'GitHosting'));
  assert.deepEqual(order, ['defaultBranch', 'diffStyle', 'GitHosting', 'forgePollSeconds']);

  // 2. Collapsed Vim on Project tab shows Global only · switch and hides the Vim intro paragraph.
  SET.tab = 'project';
  const projTree = SettingsBody({ list: rows(), sections: sectionsShown(), hosts: gitHostsFor(rows(), '') });
  const vimSec = findNodes(projTree, (n) => n.type === 'section' && findNodes(n, (c) => c.type === 'h2' && c.props.id === 'set-sec-vim').length > 0)[0];
  const vimText = vtext(vimSec);
  assert.match(vimText, /Global only/);
  assert.doesNotMatch(vimText, /Vim mode gives the editors/);

  // 3. Other-warehouse section matched by search shows the "not used by this project" note.
  SET.tab = 'user';
  SET.query = 'snowflake';
  const searchTree = SettingsBody({ list: rows(), sections: sectionsShown(), hosts: gitHostsFor(rows(), 'snowflake') });
  const sfHead = findNodes(searchTree, (n) => n.type === 'h2' && n.props.id === 'set-sec-snowflake')[0];
  assert.match(vtext(sfHead), /not used by this project/);
});
