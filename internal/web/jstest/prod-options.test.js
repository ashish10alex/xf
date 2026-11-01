import { test } from 'node:test';
import assert from 'node:assert/strict';
import { splitOptions, joinOptions, sameAsDev, boxHint } from '../assets/prod-options.js';

test('the project and dataset flags go to their boxes, in either form', () => {
  assert.deepEqual(splitOptions('--default-database=acme-prod'), { project: 'acme-prod', dataset: '', more: '' });
  assert.deepEqual(splitOptions('--default-database prod-01 --vars=a=1'), { project: 'prod-01', dataset: '', more: '--vars=a=1' });
  assert.deepEqual(splitOptions(' --default-schema "analytics" --default-database=p '), { project: 'p', dataset: 'analytics', more: '' });
});

test('other flags stay in more flags as written', () => {
  assert.deepEqual(splitOptions(`--vars='a=1, b=2' --assertion-schema=qa`), { project: '', dataset: '', more: `--vars='a=1, b=2' --assertion-schema=qa` });
});

test('a flag with no value stays in more flags', () => {
  assert.deepEqual(splitOptions('--default-database --vars=a=1'), { project: '', dataset: '', more: '--default-database --vars=a=1' });
});

test('a quote not closed goes whole into more flags', () => {
  assert.deepEqual(splitOptions('--default-database=p --vars="a=1'), { project: '', dataset: '', more: '--default-database=p --vars="a=1' });
});

test('saving writes the = form, boxes first', () => {
  assert.equal(joinOptions({ project: ' prod-01 ', dataset: '', more: '--vars=a=1' }), '--default-database=prod-01 --vars=a=1');
  assert.equal(joinOptions({ project: '', dataset: '', more: '' }), '');
  assert.equal(joinOptions(splitOptions('--default-database prod-01 --vars=a=1')), '--default-database=prod-01 --vars=a=1');
});

const names = {
  workflow: { project: 'p-dev', dataset: 'ds', prefix: '', schemaSuffix: '', databaseSuffix: '' },
  dev: { project: 'p-dev', dataset: 'ds', prefix: '', schemaSuffix: '', databaseSuffix: '' },
};

test('prod names dev tables when nothing differs', () => {
  assert.equal(sameAsDev({ project: '', dataset: '', more: '' }, names), true);
  assert.equal(sameAsDev({ project: 'p-dev', dataset: '', more: '' }, names), true);
  assert.equal(sameAsDev({ project: 'p-prod', dataset: '', more: '' }, names), false);
  assert.equal(sameAsDev({ project: '', dataset: 'other', more: '' }, names), false);
});

test('a dev suffix, or a name flag in more flags, is not the same', () => {
  assert.equal(sameAsDev({ project: '', dataset: '', more: '' }, { ...names, dev: { ...names.dev, schemaSuffix: 'ashish' } }), false);
  assert.equal(sameAsDev({ project: '', dataset: '', more: '--schema-suffix=x' }, names), false);
  assert.equal(sameAsDev({ project: '', dataset: '', more: '' }, { workflow: names.workflow }), false);
});

test('an empty box says what it gives', () => {
  assert.equal(boxHint('project', names), 'same as dev: p-dev');
  assert.equal(boxHint('dataset', { ...names, dev: { ...names.dev, dataset: 'mine' } }), 'workflow_settings.yaml: ds');
  assert.equal(boxHint('project', null), 'e.g. acme-prod');
});
