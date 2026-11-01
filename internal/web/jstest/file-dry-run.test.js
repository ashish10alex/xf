import { test } from 'node:test';
import assert from 'node:assert/strict';
import { S } from '../assets/state.js';
import { SRC, fileDryRun } from '../assets/source.js';

const failed = { count: 2, name: 'mart.orders', msg: 'Not found: Table p:raw.orders' };

test("the status line's dry run error is the source pane's file's, else the selected action's", () => {
  S.state = { dryRunByFile: { 'definitions/orders.sqlx': failed } };
  S.view = { file: 'definitions/customers.sqlx' };
  SRC.shown = true;
  SRC.file = 'definitions/orders.sqlx';
  assert.deepEqual(fileDryRun(), failed);

  SRC.file = 'definitions/customers.sqlx';
  assert.equal(fileDryRun(), null);

  SRC.shown = false;
  S.view = { file: 'definitions/orders.sqlx' };
  assert.deepEqual(fileDryRun(), failed);

  S.state = { dryRunByFile: undefined };
  assert.equal(fileDryRun(), null);
  S.state = null;
  S.view = null;
  SRC.shown = true;
  SRC.file = '';
});
