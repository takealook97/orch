import test from 'node:test';
import assert from 'node:assert/strict';
import { pctColorRole, formatReset, formatCouponExpiry, formatBytes, pickAccounts, layoutMode } from './format.js';

process.env.TZ = 'Asia/Seoul';
const now = new Date('2026-11-03T13:00:00+09:00');
test('percentage color boundaries', () => {
  for (const [pct, expected] of [[0, 'ok'], [49.9, 'ok'], [50, 'warn'], [79.9, 'warn'], [80, 'crit'], [100, 'crit']]) {
    assert.equal(pctColorRole(pct), expected);
  }
});
test('English reset labels round before choosing the duration unit', () => {
  for (const [iso, expected] of [
    ['2026-11-03T12:59:00+09:00', 'soon'],
    ['2026-11-03T13:00:00+09:00', 'soon'],
    ['2026-11-03T13:42:00+09:00', 'in 42m'],
    ['2026-11-03T13:59:00+09:00', 'in 59m'],
    ['2026-11-03T13:59:59+09:00', 'in 1h 0m'],
    ['2026-11-03T15:13:00+09:00', 'in 2h 13m'],
    ['2026-11-04T12:59:00+09:00', 'in 23h 59m'],
    ['2026-11-04T12:59:59+09:00', 'Tomorrow 12:59'],
    ['2026-11-04T13:00:00+09:00', 'Tomorrow 13:00'],
    ['2026-11-04T15:37:06+09:00', 'Tomorrow 15:37'],
    ['2026-11-10T10:59:59+09:00', 'Tue 11/10 10:59'],
  ]) assert.equal(formatReset(iso, now), expected);
  assert.equal(formatReset(null, now), '');
  assert.equal(formatReset('invalid', now), '');
});
test('calendar and coupon dates use local time in a non-KST timezone', () => {
  const original = process.env.TZ;
  try {
    process.env.TZ = 'America/Los_Angeles';
    assert.equal(formatReset('2026-11-10T01:59:00Z', now), 'Mon 11/9 17:59');
    assert.deepEqual(formatCouponExpiry('2026-12-01T00:00:00Z', now), { text: 'until 11/30', urgent: false });
    // Calendar days stay consecutive across the spring DST change.
    const beforeDST = new Date('2027-03-13T12:00:00-08:00');
    assert.equal(formatCouponExpiry('2027-03-15T12:00:00-07:00', beforeDST).text, 'D-2');
  } finally { process.env.TZ = original; }
});
test('coupon expiry handles urgency and absent coupons', () => {
  for (const [iso, expected] of [
    ['2026-12-01T00:00:00+09:00', { text: 'until 12/1', urgent: false }],
    ['2026-11-05T00:00:00+09:00', { text: 'D-2', urgent: true }],
    ['2026-11-06T13:00:00+09:00', { text: 'D-3', urgent: true }],
    ['2026-11-06T13:00:01+09:00', { text: 'until 11/6', urgent: false }],
    ['2026-11-02T00:00:00+09:00', { text: 'D-0', urgent: true }],
    [null, { text: '—', urgent: false }], ['invalid', { text: '—', urgent: false }],
  ]) assert.deepEqual(formatCouponExpiry(iso, now), expected);
});
test('memory uses binary gigabytes', () => {
  assert.equal(formatBytes(22.1 * 1024 ** 3), '22.1G');
  assert.equal(formatBytes(36 * 1024 ** 3), '36G');
  assert.equal(formatBytes(0), '0G');
  assert.equal(formatBytes(-1), '—');
  assert.equal(formatBytes(undefined), '—');
});
const codex = { provider: 'codex', home: '/work', id: 'work', label: 'work@example.com', terminals: ['t1'], five_hour: null, coupons: { available: 0 } };
const claude = { provider: 'claude', home: '', id: 'default', label: 'me', terminals: ['t2'] };
const gemini = { provider: 'gemini', id: 'agy', label: '', five_hour: { used_pct: 12 }, weekly: { used_pct: 34 } };
test('selected response supplies one account per provider in fixed order', () => {
  const picked = pickAccounts({ accounts: [claude, gemini, codex] });
  assert.deepEqual(picked.map(account => account.provider), ['codex', 'claude', 'gemini']);
  assert.equal(picked[0].label, 'work');
  assert.equal(picked[0].five_hour, null);
  assert.equal(picked[0].coupons.available, 0);
  assert.equal(picked[2].label, '');
  assert.equal(codex.label, 'work@example.com');
});
test('missing providers yield placeholders', () => {
  const picked = pickAccounts({ accounts: [claude] });
  assert.deepEqual(picked.map(account => account.provider), ['codex', 'claude', 'gemini']);
  assert.equal(picked[0].label, '');
  assert.equal(picked[0].five_hour, undefined);
  assert.equal(picked[1].label, 'me');
  assert.equal(picked[2].label, '');
});

test('layout adapts to the estimated card widths', () => {
  const full = ['codex', 'claude', 'gemini'].map(provider => ({ provider, five_hour: { used_pct: 10 }, weekly: { used_pct: 20 } }));
  assert.deepEqual(layoutMode(1600, full), { bars: true, accountLine: true, weekDetail: true });
  assert.deepEqual(layoutMode(1160, full), { bars: false, accountLine: true, weekDetail: true });
  assert.deepEqual(layoutMode(400, full), { bars: false, accountLine: false, weekDetail: false });
  assert.deepEqual(layoutMode(1100, pickAccounts(null)), { bars: true, accountLine: true, weekDetail: true });
});
