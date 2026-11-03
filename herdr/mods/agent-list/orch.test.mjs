import { test } from 'node:test';
import assert from 'node:assert/strict';
import { workerIndex, isHidden, headSuffix } from './orch.js';

test('singular worker includes English working count', () => {
  const index = workerIndex({ runs: [{ head_terminal: 'head', workers: [{ terminal: 't1', status: 'prompted' }] }] });
  assert.equal(headSuffix('head', index), ' · 1 worker (1 working)');
});

test('empty or invalid data leaves agents visible', () => {
  for (const hud of [null, {}, { runs: null }, { runs: [null, { workers: null }] }]) {
    const index = workerIndex(hud);
    assert.equal(isHidden({ address: 't1' }, index), false);
    assert.equal(headSuffix('t1', index), '');
  }
});

test('hides workers in every status and counts active workers', () => {
  const statuses = ['spawned', 'prompted', 'done', 'closed', 'lost'];
  const index = workerIndex({ runs: [{ head_terminal: 'head', workers: statuses.map((status, i) => ({ terminal: `t${i}`, status })) }] });
  statuses.forEach((_, i) => assert.equal(isHidden({ address: `t${i}` }, index), true));
  assert.equal(isHidden({ terminal: 't0' }, index), true);
  assert.equal(isHidden({ address: 't0/child' }, index), false);
  assert.equal(headSuffix('head', index), ' · 3 workers (2 working)');
  assert.equal(headSuffix('other', index), '');
});

test('omits working count when none are working', () => {
  const index = workerIndex({ runs: [{ head_terminal: 'head', workers: [{ terminal: 't1', status: 'done' }] }] });
  assert.equal(headSuffix('head', index), ' · 1 worker');
});
