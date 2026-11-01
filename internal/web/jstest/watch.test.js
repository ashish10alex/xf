// Watching a workflow xf did not start: a palette command opens a prompt
// for its ID; a refusal stays in the prompt, and a started watch closes it
// and shows the jobs tab, where the cost table is.

import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { S, closeOverlay, handleKey } from '../assets/state.js';
import { openWatch, submitWatch } from '../assets/log-tabs.js';

let sent;
const answer = (body) => {
  globalThis.fetch = async (url, init) => {
    sent.push({ url: String(url), body: init && init.body ? JSON.parse(init.body) : null });
    return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  };
};

beforeEach(() => {
  sent = [];
  Object.assign(S, { mode: 'project', overlay: null, checks: null, showLog: false, logTab: 'log', watch: null });
  S.caps = { ...S.caps, history: true };
});

test('the prompt takes the keys itself, and the palette keys wait', () => {
  openWatch();
  assert.equal(S.overlay, 'watch');
  assert.equal(handleKey('x'), false);
  assert.equal(handleKey('palette'), true);
  assert.equal(S.overlay, 'watch');
  closeOverlay();
  assert.equal(S.overlay, null);
  assert.equal(S.watch, null);
});

test('an empty box is not sent', async () => {
  openWatch();
  await submitWatch();
  assert.equal(sent.length, 0);
  assert.match(S.watch.err, /paste a workflow ID/);
});

test('a refusal stays in the prompt', async () => {
  openWatch();
  S.watch.input = 'bad id';
  answer({ ok: false, message: '"bad id" is not a workflow ID' });
  await submitWatch();
  assert.equal(S.overlay, 'watch');
  assert.equal(S.watch.busy, false);
  assert.equal(S.watch.err, '"bad id" is not a workflow ID');
});

test('a watch that starts closes the prompt and shows the jobs', async () => {
  openWatch();
  S.watch.input = ' 1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee ';
  answer({ ok: true });
  await submitWatch();
  assert.deepEqual(sent.map((s) => [s.url.replace(/^.*\/api/, '/api'), s.body.input.trim()]), [['/api/watch', '1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee']]);
  assert.equal(S.overlay, null);
  assert.equal(S.watch, null);
  assert.equal(S.showLog, true);
  assert.equal(S.logTab, 'jobs');
});
