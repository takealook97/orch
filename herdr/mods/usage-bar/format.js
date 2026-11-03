// Pure formatting helpers. Calendar labels use Korea Standard Time.
const KST = 9 * 60 * 60 * 1000;
const dayMS = 24 * 60 * 60 * 1000;
const weekdays = ['일', '월', '화', '수', '목', '금', '토'];
const local = date => new Date(date.getTime() + KST);
const day = date => Math.floor((date.getTime() + KST) / dayMS);
const clock = date => `${String(date.getUTCHours()).padStart(2, '0')}:${String(date.getUTCMinutes()).padStart(2, '0')}`;

export function pctColorRole(pct) {
  return pct >= 80 ? 'crit' : pct >= 50 ? 'warn' : 'ok';
}

export function formatReset(isoString, now = new Date()) {
  const reset = new Date(isoString);
  if (!isoString || !Number.isFinite(reset.getTime())) return '↻ —';
  const remaining = reset - now;
  if (remaining <= 0) return '↻ 곧 갱신';
  const minutes = Math.ceil(remaining / 60000);
  if (remaining < 3600000) return `↻ ${minutes}분`;
  if (remaining < dayMS) return `↻ ${Math.floor(minutes / 60)}시간 ${minutes % 60}분`;
  const date = local(reset);
  if (day(reset) - day(now) <= 1) return `↻ 내일 ${clock(date)}`;
  return `↻ ${date.getUTCMonth() + 1}/${date.getUTCDate()}(${weekdays[date.getUTCDay()]}) ${clock(date)}`;
}

export function formatCouponExpiry(iso, now = new Date()) {
  const expires = new Date(iso);
  if (!iso || !Number.isFinite(expires.getTime())) return { text: '—', urgent: false };
  const days = Math.max(0, day(expires) - day(now));
  if (expires - now <= 3 * dayMS) return { text: `D-${days}`, urgent: true };
  const date = local(expires);
  return { text: `~${date.getUTCMonth() + 1}/${date.getUTCDate()}까지`, urgent: false };
}

export function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return `${Number((bytes / 1024 ** 3).toFixed(1))}G`;
}

export function pickAccounts(usage, defaults, terminals = []) {
  const addresses = new Set(terminals);
  return ['codex', 'claude'].flatMap(provider => {
    const active = (usage?.accounts ?? []).filter(account => account.provider === provider &&
      account.terminals?.some(address => addresses.has(address)));
    if (active.length) return active.map(account => ({ ...account, dimDefault: false }));
    const fallback = (defaults?.accounts ?? []).find(account => account.provider === provider && !account.error);
    return fallback ? [{ ...fallback, label: `${fallback.label || 'default'} (기본)`, dimDefault: true }] : [];
  });
}
