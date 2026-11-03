// Calendar labels follow local time, including daylight saving time.
const dayMS = 24 * 60 * 60 * 1000;
const weekdays = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const day = date => Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / dayMS;
const clock = date => `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
export function pctColorRole(pct) {
  return pct >= 80 ? 'crit' : pct >= 50 ? 'warn' : 'ok';
}
export function formatReset(iso, now = new Date()) {
  const reset = new Date(iso);
  if (!iso || !Number.isFinite(reset.getTime())) return '';
  if (reset <= now) return 'soon';
  const minutes = Math.ceil((reset - now) / 60000);
  if (minutes < 60) return `in ${minutes}m`;
  if (minutes < 1440) return `in ${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  if (day(reset) - day(now) === 1) return `Tomorrow ${clock(reset)}`;
  return `${weekdays[reset.getDay()]} ${reset.getMonth() + 1}/${reset.getDate()} ${clock(reset)}`;
}
export function formatCouponExpiry(iso, now = new Date()) {
  const expires = new Date(iso);
  if (!iso || !Number.isFinite(expires.getTime())) return { text: '—', urgent: false };
  if (expires - now <= 3 * dayMS) return { text: `D-${Math.max(0, day(expires) - day(now))}`, urgent: true };
  return { text: `until ${expires.getMonth() + 1}/${expires.getDate()}`, urgent: false };
}
export function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return `${Number((bytes / 1024 ** 3).toFixed(1))}G`;
}
export function pickAccounts(usage) {
  return ['codex', 'claude', 'gemini'].map(provider => {
    const account = usage?.accounts?.find(account => account.provider === provider) ?? { provider };
    return { ...account, label: provider === 'gemini' ? '' : (account.label ?? '').split('@')[0] };
  });
}

// These dimensions are shared with the rendered cells and card labels.
export const cellWidth = (label, missing, hideDetail = false) =>
  missing ? 44 : label === 'Week' ? (hideDetail ? 58 : 92) : label === '5h' ? 76 : 64;
export const labelWidth = accountLine => accountLine ? 64 : 48;
export function layoutMode(width, accounts) {
  const mode = { bars: true, accountLine: true, weekDetail: true };
  const needed = () => accounts.reduce((sum, account) => {
    const fiveMissing = !Number.isFinite(account.five_hour?.used_pct);
    const weekMissing = !Number.isFinite(account.weekly?.used_pct);
    const cells = cellWidth('5h', fiveMissing) + cellWidth('Week', weekMissing, !mode.weekDetail);
    // Padding, borders, inner gaps, and the gap following each provider.
    return sum + labelWidth(mode.accountLine) + cells + 16 + 2 + 16 + 8 +
      (account.provider === 'codex' ? cellWidth('', false) + 8 : 0);
  }, 170 + 40 - (mode.bars ? 0 : 44));
  if (width < needed()) mode.bars = false;
  if (width < needed()) mode.accountLine = false;
  if (width < needed()) mode.weekDetail = false;
  return mode;
}
