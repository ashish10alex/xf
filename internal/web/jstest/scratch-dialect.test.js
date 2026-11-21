// The editors' functions follow the project's warehouse: BigQuery's, or
// Databricks' (ADR 0042), each asked for once.
import { test } from 'node:test';
import assert from 'node:assert/strict';

const asked = [];
const fn = (name, extra = {}) => ({ name, snippet: name + '()', detail: name + '()', url: '', signatures: [{ label: name + '()', params: [], returns: '' }], ...extra });
globalThis.fetch = async (path) => {
  asked.push(String(path));
  const list = String(path).includes('dialect=databricks')
    ? [fn('TIMESTAMPADD'), fn('SUM', { aggregate: true }), fn('PIVOT', { clause: true })]
    : String(path).includes('dialect=snowflake')
      ? []
      : [fn('SAFE_CAST'), fn('SUM', { aggregate: true })];
  return new Response(JSON.stringify(list));
};

const { S } = await import('../assets/state.js');
const F = await import('../assets/scratch-functions.js');

test('a BigQuery project gets BigQuery\'s functions, with SAFE.', async () => {
  S.caps = {};
  await F.loadFunctions();
  assert.ok(F.isFunctionName('safe_cast'));
  assert.ok(!F.isFunctionName('timestampadd'));
  assert.equal(F.functionNamed('SAFE.SUM').name, 'SUM');
});

test('a Databricks project gets Databricks\' functions, and SAFE. is a schema', async () => {
  S.caps = { warehouse: 'databricks' };
  assert.equal(F.dialect(), 'databricks');
  await F.loadFunctions();
  assert.ok(F.isFunctionName('timestampadd'));
  assert.ok(!F.isFunctionName('safe_cast'));
  assert.equal(F.functionNamed('safe.sum'), undefined);
  assert.deepEqual((await F.functionOptions(1, true)).map((o) => o.label), ['SUM']);
  assert.deepEqual((await F.clauseOptions(9)).map((o) => o.label), ['PIVOT']);
});

test('a Snowflake project uses snowflake dialect and double-quoted identifiers', async () => {
  S.caps = { warehouse: 'snowflake' };
  assert.equal(F.dialect(), 'snowflake');
  await F.loadFunctions();
  assert.ok(!F.isFunctionName('safe_cast'));
  assert.equal(F.functionNamed('safe.sum'), undefined);
  const { quoteIdent } = await import('../assets/scratch-complete.js');
  assert.equal(quoteIdent('my col'), '"my col"');
  assert.equal(quoteIdent('a"b'), '"a""b"');
  const { tablesSite } = await import('../assets/state.js');
  assert.equal(tablesSite(), 'Snowsight');
});

test('each dialect is asked for once', async () => {
  S.caps = {};
  await F.loadFunctions();
  S.caps = { warehouse: 'databricks' };
  await F.loadFunctions();
  S.caps = { warehouse: 'snowflake' };
  await F.loadFunctions();
  assert.deepEqual(asked, ['/api/scratch/functions', '/api/scratch/functions?dialect=databricks', '/api/scratch/functions?dialect=snowflake']);
});
