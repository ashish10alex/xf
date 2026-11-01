import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S, say, sayMs } from '../assets/state.js';

test('a said message goes after sayMs, unless another took its place', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  say('opening the pipeline in the browser');
  t.mock.timers.tick(sayMs - 1);
  assert.equal(S.flash, 'opening the pipeline in the browser');
  t.mock.timers.tick(1);
  assert.equal(S.flash, '');

  say('first');
  t.mock.timers.tick(sayMs / 2);
  say('second');
  t.mock.timers.tick(sayMs / 2);
  assert.equal(S.flash, 'second');
  t.mock.timers.tick(sayMs / 2);
  assert.equal(S.flash, '');

  // A warning set straight on S.flash stays until a key.
  say('said');
  S.flash = 'file watching is off';
  t.mock.timers.tick(sayMs);
  assert.equal(S.flash, 'file watching is off');
  S.flash = '';
});
