// Scratch's table completion: where a table goes (after FROM, DROP TABLE,
// UPDATE, …), which tables a statement reads, and how a name matches by any
// part of it, or by a few of its words in order, the best matches first.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { partMatch, byParts, inFromList, parseRefs, blank } from '../assets/scratch-complete.js';

const roi = 'daily_marketing_reports.0300_PROMOTIONS_DATA_ROI';

test('typed text matches a table name by its parts', () => {
  for (const [label, typed, want] of [
    [roi, '', { rank: 0, ranges: [] }],
    [roi, 'daily', { rank: 0, ranges: [0, 5] }],
    [roi, '0300_pro', { rank: 1, ranges: [24, 32] }],
    [roi, 'HOL', null],
    // Each word begins a part, in order; any case.
    [roi, '0300_ROI', { rank: 2, ranges: [24, 28, 45, 48] }],
    [roi, '0300_ROI_', { rank: 2, ranges: [24, 28, 45, 48] }],
    [roi, 'daily_0300_data', { rank: 2, ranges: [0, 5, 24, 28, 40, 44] }],
    [roi, 'roi_0300', null],
    // A word must begin a part, not sit in one.
    [roi, '0300_DA', { rank: 2, ranges: [24, 28, 40, 42] }],
    [roi, '0300_ATA', null],
  ]) {
    assert.deepEqual(partMatch(label, typed), want, label + ' / ' + typed);
  }
});

test('the best matches come first, then by boost', () => {
  const options = [
    { label: 'daily_marketing_reports.0300_PROMOTIONS_DATA_ROI', boost: 3 },
    { label: 'roi.0300_ROI_SUMMARY', boost: 1 },
    { label: '0300_ROI', boost: 1 },
    { label: 'daily_marketing_reports.050_HOLIDAYS', boost: 3 },
  ];
  assert.deepEqual(byParts(options, '0300_roi').map((o) => o.label), [
    '0300_ROI', 'roi.0300_ROI_SUMMARY', 'daily_marketing_reports.0300_PROMOTIONS_DATA_ROI',
  ]);
});

test('a table goes after FROM, JOIN and the statements that name one', () => {
  for (const sql of [
    'SELECT * FROM ', 'SELECT * FROM a JOIN ', 'SELECT * FROM a, ',
    'DROP TABLE ', 'drop table if exists ', 'DROP VIEW ', 'DROP MATERIALIZED VIEW IF EXISTS ',
    'TRUNCATE TABLE ', 'ALTER TABLE ', 'INSERT INTO ', 'INSERT ', 'MERGE INTO ', 'MERGE t USING ',
    'UPDATE ', 'DELETE ', 'DELETE FROM ', 'CREATE TABLE x CLONE ', 'CREATE TABLE x COPY ',
  ]) assert.equal(inFromList(blank(sql), sql.length), true, sql);
  for (const sql of [
    'SELECT ', 'SELECT a, ', 'SELECT * FROM a WHERE ', 'CREATE TABLE ',
    'MERGE t USING s ON a WHEN MATCHED THEN UPDATE ', 'WHEN NOT MATCHED THEN INSERT ', 'WHEN MATCHED THEN DELETE ',
  ]) assert.equal(inFromList(blank(sql), sql.length), false, sql);
});

test('a statement reads the tables it names, with their aliases', () => {
  const refs = (sql) => parseRefs(sql).map((r) => [r.text, r.alias]);
  assert.deepEqual(refs('SELECT * FROM a JOIN b ON x'), [['a', ''], ['b', '']]);
  assert.deepEqual(refs('SELECT * FROM ds.a o JOIN ds.b AS c ON x'), [['ds.a', 'o'], ['ds.b', 'c']]);
  assert.deepEqual(refs('SELECT * FROM ds.orders'), [['ds.orders', '']]);
  assert.deepEqual(refs('DROP TABLE IF EXISTS ds.t'), [['ds.t', '']]);
  assert.deepEqual(refs('UPDATE ds.t SET a = 1'), [['ds.t', '']]);
  assert.deepEqual(refs('DELETE FROM ds.t WHERE a'), [['ds.t', '']]);
  assert.deepEqual(refs('DELETE ds.t d WHERE a'), [['ds.t', 'd']]);
  assert.deepEqual(refs('MERGE ds.t USING ds.s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = 1'), [['ds.t', ''], ['ds.s', '']]);
});
