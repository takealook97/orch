import test from 'node:test';
import assert from 'node:assert/strict';
import { pctColorRole, formatReset, formatCouponExpiry, formatBytes, pickAccounts, layoutMode, cellWidth, labelWidth } from './format.js';
const couponText = iso => { const d = new Date(iso); return `~${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`; };

process.env.TZ = 'Asia/Seoul';
const now = new Date('2026-11-03T13:00:00+09:00');
test('percentage color boundaries', () => {
  for (const [pct, expected] of [[0, 'ok'], [49.9, 'ok'], [50, 'warn'], [79.9, 'warn'], [80, 'crit'], [100, 'crit']]) {
    assert.equal(pctColorRole(pct), expected);
  }
});
test('reset labels use local weekday and 24-hour time', () => {
  for (const [iso, expected] of [
    ['2026-11-03T12:59:00+09:00', 'soon'],
    ['2026-11-03T13:00:00+09:00', 'soon'],
    ['2026-11-03T13:42:00+09:00', '11/3 13:42'],
    ['2026-11-04T15:37:06+09:00', '11/4 15:37'],
    ['2026-11-08T01:05:00+09:00', '11/8 01:05'],
    ['2026-11-10T10:59:59+09:00', '11/10 10:59'],
  ]) assert.equal(formatReset(iso, now), expected);
  assert.equal(formatReset(null, now), '');
  assert.equal(formatReset('invalid', now), '');
});
test('calendar and coupon dates use local time in a non-KST timezone', () => {
  const original = process.env.TZ;
  try {
    process.env.TZ = 'America/Los_Angeles';
    assert.equal(formatReset('2026-11-10T01:59:00Z', now), '11/9 17:59');
    assert.deepEqual(formatCouponExpiry('2026-12-01T00:00:00Z', now), { text: couponText('2026-12-01T00:00:00Z'), urgent: false });
    // Calendar days stay consecutive across the spring DST change.
    const beforeDST = new Date('2027-03-13T12:00:00-08:00');
    assert.equal(formatCouponExpiry('2027-03-15T12:00:00-07:00', beforeDST).urgent, true);
  } finally { process.env.TZ = original; }
});
test('coupon expiry handles urgency and absent coupons', () => {
  for (const [iso, expected] of [
    ['2026-12-01T00:00:00+09:00', { text: couponText('2026-12-01T00:00:00+09:00'), urgent: false }],
    ['2026-11-05T00:00:00+09:00', { text: couponText('2026-11-05T00:00:00+09:00'), urgent: true }],
    ['2026-11-06T13:00:00+09:00', { text: couponText('2026-11-06T13:00:00+09:00'), urgent: true }],
    ['2026-11-06T13:00:01+09:00', { text: couponText('2026-11-06T13:00:01+09:00'), urgent: false }],
    ['2026-11-02T00:00:00+09:00', { text: couponText('2026-11-02T00:00:00+09:00'), urgent: true }],
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
  assert.equal(pickAccounts({ accounts: [{ ...gemini, label: 'gem@example.com' }] })[2].label, 'gem');
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

test('layout hides bars before account lines at compact widths', () => {
  const accounts = ['codex', 'claude', 'gemini'].map(provider => ({ provider }));
  const fullWidth = 170 + 40 + accounts.length * (labelWidth(true) + cellWidth('weekly') + 16 + 2 + 8 + 8) + cellWidth('coupon') + 8;
  assert.deepEqual(layoutMode(fullWidth, accounts), { bars: true, accountLine: true });
  assert.deepEqual(layoutMode(fullWidth - 1, accounts), { bars: false, accountLine: true });
  assert.deepEqual(layoutMode(fullWidth - 45, accounts), { bars: false, accountLine: false });
  assert.deepEqual(layoutMode(fullWidth, pickAccounts(null)), { bars: true, accountLine: true });
});
