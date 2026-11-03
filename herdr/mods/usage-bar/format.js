export function hudCommand(machine, args) {
  if (!machine) return ['/Users/deepeet/.local/bin/orch', ['hud', ...args], {}];
  return ['sh', ['-c', 'b="$HOME/.local/libexec/orch-hud"; [ -x "$b" ] || b="$HOME/.local/bin/orch"; exec "$b" hud "$@"', 'hud', ...args], { on: machine }];
}

// Calendar labels follow local time, including daylight saving time.
const dayMS = 24 * 60 * 60 * 1000;
const day = date => Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / dayMS;
const clock = date => `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
export function pctColorRole(pct) {
  return pct >= 80 ? 'crit' : pct >= 50 ? 'warn' : 'ok';
}
export function formatReset(iso, now = new Date()) {
  const reset = new Date(iso);
  if (!iso || !Number.isFinite(reset.getTime())) return '';
  if (reset <= now) return 'soon';
  return `${reset.getMonth() + 1}/${reset.getDate()} ${clock(reset)}`;
}
export function formatCouponExpiry(iso, now = new Date()) {
  const expires = new Date(iso);
  if (!iso || !Number.isFinite(expires.getTime())) return { text: '—', urgent: false };
  return { text: `~${expires.getMonth() + 1}/${expires.getDate()} ${clock(expires)}`, urgent: expires - now <= 3 * dayMS };
}
export function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return `${Number((bytes / 1024 ** 3).toFixed(1))}G`;
}
export function pickAccounts(usage) {
  return ['codex', 'claude', 'gemini'].map(provider => {
    const account = usage?.accounts?.find(account => account.provider === provider) ?? { provider };
    return { ...account, label: (account.label ?? '').split('@')[0] };
  });
}

// Dimensions shared by the provider cards and their width estimates.
export const cellWidth = label => label === 'coupon' ? 52 : 96;
export const labelWidth = accountLine => accountLine ? 96 : 48;
export function layoutMode(width, accounts) {
  const mode = { bars: true, accountLine: true };
  const needed = () => accounts.reduce((sum, account) => sum + labelWidth(mode.accountLine) +
    cellWidth('weekly') + (account.provider === 'codex' ? cellWidth('coupon') + 8 : 0) +
    16 + 2 + 8 + 8, 170 + 40 - (mode.bars ? 0 : 44));
  if (width < needed()) mode.bars = false;
  if (width < needed()) mode.accountLine = false;
  return mode;
}
