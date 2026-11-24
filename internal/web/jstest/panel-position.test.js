// panelPosition places the log/terminal panel at the bottom (default) or on
// the right, applies live when changed, and can be toggled from the palette.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, hooks, panelRight, togglePanelPosition, paletteCommands } from '../assets/state.js';
import '../assets/settings.js';

let posted = [];
globalThis.fetch = async (url, opts) => {
  if (opts && opts.method === 'POST') posted.push({ url, body: JSON.parse(opts.body) });
  return new Response('{}', { headers: { 'Content-Type': 'application/json' } });
};

beforeEach(() => {
  posted = [];
  Object.assign(S, { mode: 'project', showLog: false, logTab: 'log', focus: 'source', caps: { terminal: true }, open: [] });
  S.boot = { root: '/p', name: 'p', backend: 'dataform', defaultBranch: 'main', panelPosition: 'bottom', pane2Tab: 'sql', vimKeys: {} };
});

test('panelRight defaults to false and updates live on settingsChanged', () => {
  assert.equal(panelRight(), false);
  hooks.settingsChanged({ vimKeys: {}, pane2Tab: 'sql', panelPosition: 'right' });
  assert.equal(panelRight(), true);
  assert.equal(S.boot.panelPosition, 'right');
  hooks.settingsChanged({ vimKeys: {}, pane2Tab: 'sql', panelPosition: 'bottom' });
  assert.equal(panelRight(), false);
});

test('togglePanelPosition flips position and saves to user settings', async () => {
  await togglePanelPosition();
  assert.equal(panelRight(), true);
  assert.equal(posted.length, 1);
  assert.equal(posted[0].url, '/api/settings');
  assert.deepEqual(posted[0].body, { level: 'user', key: 'panelPosition', value: 'right' });

  await togglePanelPosition();
  assert.equal(panelRight(), false);
  assert.deepEqual(posted[1].body, { level: 'user', key: 'panelPosition', value: 'bottom' });
});

test('palette lists move panel command matching current position', () => {
  const titles = () => paletteCommands().map((c) => c.title);
  assert.ok(titles().includes('View: bottom panel'));
  assert.ok(titles().includes('View: move panel to right'));
  assert.ok(!titles().includes('View: move panel to bottom'));

  S.boot.panelPosition = 'right';
  assert.ok(titles().includes('View: panel (right)'));
  assert.ok(titles().includes('View: move panel to bottom'));
  assert.ok(!titles().includes('View: move panel to right'));
});
