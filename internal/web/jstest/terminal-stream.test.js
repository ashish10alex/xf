// The terminal's output stream comes back when the tab shows again after
// it closed while hidden (xf web restarted): before, keys reached the
// shell and nothing printed until a reload.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { keepStream } from '../assets/terminal.js';

const opened = [];
globalThis.WebSocket = class {
  constructor(url) { this.url = url; opened.push(this); }
  close() {}
};

test('showing the tab opens the stream again once it has closed', () => {
  S.mode = 'project';
  S.key = 'p1';
  keepStream();
  assert.equal(opened.length, 1);
  assert.match(opened[0].url, /\/api\/term\/stream\?project=p1$/);

  keepStream();
  assert.equal(opened.length, 1, 'an open stream is kept');

  opened[0].onclose(); // the server went away while the tab was hidden
  keepStream();
  assert.equal(opened.length, 2, 'a closed stream is opened again');

  S.key = 'p2';
  keepStream();
  assert.equal(opened.length, 3, 'another project gets its own stream');
  assert.match(opened[2].url, /project=p2$/);
});
