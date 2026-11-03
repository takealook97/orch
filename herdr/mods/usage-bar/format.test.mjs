import test from 'node:test';
import assert from 'node:assert/strict';
import { pctColorRole, formatReset, formatCouponExpiry, formatBytes, pickAccounts } from './format.js';

const now = new Date('2026-11-03T13:00:00+09:00');
test('percentage roles include exact warning and critical boundaries', () => {
  for (const [pct, expected] of [[0, 'ok'], [49.9, 'ok'], [50, 'warn'], [79.9, 'warn'], [80, 'crit'], [100, 'crit']]) {
    assert.equal(pctColorRole(pct), expected);
  }
});

test('reset labels cover expired, minutes, hours, tomorrow, and calendar dates in KST', () => {
  for (const [iso, expected] of [
    ['2026-11-03T12:59:00+09:00', '곧 갱신'],
    ['2026-11-03T13:00:00+09:00', '곧 갱신'],
    ['2026-11-03T13:42:00+09:00', '42분'],
    ['2026-11-03T13:59:00+09:00', '59분'],
    ['2026-11-03T14:00:00+09:00', '1시간 0분'],
    ['2026-11-03T15:13:00+09:00', '2시간 13분'],
    ['2026-11-04T12:00:00+09:00', '23시간 0분'],
    ['2026-11-04T13:00:00+09:00', '내일 13:00'],
    ['2026-11-04T15:37:06+09:00', '내일 15:37'],
    ['2026-11-05T10:59:00+09:00', '11/5(목) 10:59'],
    ['2026-11-10T10:59:59+09:00', '11/10(화) 10:59'],
    ['2026-11-09T01:59:00Z', '11/9(월) 10:59'],
  ]) assert.equal(formatReset(iso, now), `↻ ${expected}`);
  assert.equal(formatReset(null, now), '↻ —');
  assert.equal(formatReset('invalid', now), '↻ —');
});

test('coupon expiry includes three-day boundary, D-2, expired and absent coupons', () => {
  assert.deepEqual(formatCouponExpiry('2026-11-30T00:00:00+09:00', now), { text: '~11/30까지', urgent: false });
  assert.deepEqual(formatCouponExpiry('2026-11-05T00:00:00+09:00', now), { text: 'D-2', urgent: true });
  assert.deepEqual(formatCouponExpiry('2026-11-06T13:00:00+09:00', now), { text: 'D-3', urgent: true });
  assert.deepEqual(formatCouponExpiry('2026-11-06T13:00:01+09:00', now), { text: '~11/6까지', urgent: false });
  assert.deepEqual(formatCouponExpiry('2026-11-02T00:00:00+09:00', now), { text: 'D-0', urgent: true });
  assert.deepEqual(formatCouponExpiry(null, now), { text: '—', urgent: false });
  assert.deepEqual(formatCouponExpiry('invalid', now), { text: '—', urgent: false });
});

test('memory uses binary gigabytes with one useful decimal', () => {
  assert.equal(formatBytes(22.1 * 1024 ** 3), '22.1G');
  assert.equal(formatBytes(36 * 1024 ** 3), '36G');
  assert.equal(formatBytes(0), '0G');
  assert.equal(formatBytes(-1), '—');
  assert.equal(formatBytes(undefined), '—');
});

const codex = { provider: 'codex', home: '/work', label: 'work', terminals: ['t1'], five_hour: null, coupons: { available: 0 } };
const claude = { provider: 'claude', home: '', label: 'me', terminals: ['t2'] };
test('active accounts follow terminal membership and Codex precedes Claude', () => {
  const usage = { accounts: [claude, codex, { ...codex, home: '/other', terminals: ['t3'] }] };
  const picked = pickAccounts(usage, null, ['t1', 't2']);
  assert.deepEqual(picked.map(account => account.provider), ['codex', 'claude']);
  assert.ok(picked.every(account => account.dimDefault === false));
  assert.equal(picked[0].five_hour, null);
  assert.equal(picked[0].coupons.available, 0);
  assert.equal(codex.dimDefault, undefined);
});

test('each absent provider uses only its first account without error', () => {
  const defaults = { accounts: [
    { ...codex, error: 'offline' }, { ...claude, label: 'default Claude' },
    { ...codex, label: 'ready' }, { ...codex, label: 'later' },
  ] };
  const picked = pickAccounts({ accounts: [claude] }, defaults, ['t2']);
  assert.equal(picked[0].label, 'ready (기본)');
  assert.equal(picked[0].dimDefault, true);
  assert.equal(picked[1].label, 'me');
  assert.equal(picked[1].dimDefault, false);
  assert.deepEqual(pickAccounts(null, null), []);
  assert.deepEqual(pickAccounts(null, { accounts: [{ ...codex, error: 'offline' }] }), []);
  assert.equal(pickAccounts(null, { accounts: [{ ...codex, label: '' }] })[0].label, 'default (기본)');
  assert.equal(pickAccounts({ accounts: [{ ...codex, terminals: undefined }] }, null, ['t1']).length, 0);
});
