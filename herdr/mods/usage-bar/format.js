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
