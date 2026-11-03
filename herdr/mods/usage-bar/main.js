const ORCH = "/Users/deepeet/.local/bin/orch";

import { View, div } from 'gpui-kit';
import { h_flex } from 'gpui-base';
import { exec, spaces, panes, theme } from 'herdr';
import { formatReset, formatCouponExpiry, formatBytes, pickAccounts } from './format.js';
import { cell, divider, groupLabel } from './ui.js';

export default class UsageBar extends View {
  init(_props, cx) {
    this.sys = null;
    this.usage = null;
    this.defaults = null;
    this.stale = { sys: false, usage: false, defaults: false };
    this.lastOk = {};
    this.busy = {};
    this.request = {};
    this.width = 1200;
    this.terminals = [];
    this.terminalKey = null;
    this.syncTerminals(cx);
    this.refresh('sys', cx);
    this.refresh('defaults', cx);
    cx.timer.every(5000, cx => this.refresh('sys', cx));
    cx.timer.every(60000, cx => this.refresh('usage', cx));
    cx.timer.every(300000, cx => this.refresh('defaults', cx));
  }

  syncTerminals(cx) {
    const active = spaces().find(space => space.active);
    const tabs = new Set((active?.tabs ?? []).map(tab => tab.id));
    const terminals = [...new Set(panes().filter(pane => tabs.has(pane.tab) && pane.terminal &&
      !pane.terminal.includes('/')).map(pane => pane.terminal))].sort();
    const key = `${active?.id ?? ''}:${terminals.join(',')}`;
    if (this.terminalKey === key) return;
    this.terminalKey = key;
    this.terminals = terminals;
    this.usage = null;
    this.stale.usage = false;
    this.debounce?.cancel();
    this.debounce = cx.timer.after(1000, cx => this.refresh('usage', cx));
  }

  refresh(kind, cx, force = false) {
    const key = kind === 'usage' ? this.terminalKey : kind;
    if (kind === 'usage' && !this.terminals.length) return;
    if (this.busy[kind] === key) {
      if (force) this.request[kind] = true;
      return;
    }
    this.busy[kind] = key;
    const args = ['hud', kind === 'sys' ? 'sys' : 'usage'];
    if (kind === 'usage') args.push('--terminals', this.terminals.join(','));
    if (kind !== 'sys') args.push('--max-age', '120s');
    if (force && kind !== 'sys') args.push('--refresh');
    cx.spawn(async cx => {
      try {
        const result = await exec(ORCH, args);
        if (result.code !== 0) throw new Error('orch hud unavailable');
        const data = JSON.parse(result.stdout);
        if (!data || (kind === 'sys'
          ? !['cpu_pct', 'mem_used_bytes', 'mem_total_bytes'].every(field => Number.isFinite(data[field])) || data.mem_total_bytes <= 0
          : !Array.isArray(data.accounts))) throw new Error('invalid hud data');
        if (kind === 'usage' && key !== this.terminalKey) return;
        // Partial provider failures retain their prior windows when available.
        if (kind !== 'sys') data.accounts = data.accounts.map(account => {
          const prior = this[kind]?.accounts.find(old => old.provider === account.provider && old.home === account.home);
          // Preserve the last ready default as such; the stale flag communicates
          // a failed refresh without making fallback selection discard it.
          if (kind === 'defaults' && account.error && prior && !prior.error) return { ...prior, stale: true };
          return account.error && prior ? { ...prior, ...account,
            five_hour: account.five_hour ?? prior.five_hour,
            weekly: account.weekly ?? prior.weekly,
            coupons: account.coupons ?? prior.coupons, stale: true } : account;
        });
        this[kind] = data;
        this.stale[kind] = false;
        this.lastOk[kind] = new Date();
      } catch (_error) {
        if (kind !== 'usage' || key === this.terminalKey) this.stale[kind] = true;
      } finally {
        if (this.busy[kind] === key) delete this.busy[kind];
        cx.notify();
        if (this.request[kind]) {
          delete this.request[kind];
          this.refresh(kind, cx, true);
        }
      }
    });
  }

  refreshAll(cx) {
    this.refresh('sys', cx);
    this.refresh('usage', cx, true);
    this.refresh('defaults', cx, true);
  }

  percentCell(t, label, window, dim, now) {
    const past = window?.resets_at && new Date(window.resets_at) <= now;
    const pct = past ? 0 : window?.used_pct;
    const known = Number.isFinite(pct);
    return cell(t, { label, value: known ? `${Math.round(pct)}%` : '—',
      pct: known ? pct : undefined, detail: formatReset(window?.resets_at, now),
      hideBar: label === '주간' && this.width < 660, dim });
  }

  render(cx) {
    this.syncTerminals(cx);
    const t = theme(), now = new Date();
    const accounts = pickAccounts(this.usage, this.defaults, this.terminals);
    const stale = this.stale.sys || (this.terminals.length > 0 && this.stale.usage) ||
      (accounts.some(account => account.dimDefault) && this.stale.defaults) || accounts.some(account => account.stale || account.error);
    const times = accounts.map(account => account.checked_at).filter(Boolean);
    const at = times.length ? new Date(Math.min(...times.map(time => new Date(time).getTime())))
      : this.lastOk.usage ?? this.lastOk.defaults ?? this.lastOk.sys;
    const stamp = at && Number.isFinite(at.getTime())
      ? `${String(at.getHours()).padStart(2, '0')}:${String(at.getMinutes()).padStart(2, '0')}` : '—';
    const group = id => h_flex().id(id).items_center().flex_none().cursor_pointer()
      .tooltip('클릭: 사용량 다시 읽기')
      .on_mouse_down('left', (_event, cx) => cx.stop_propagation())
      .on_click((_event, cx) => this.refreshAll(cx));
    const groups = [];
    if (this.sys) {
      const { cpu_pct: cpu, mem_used_bytes: used, mem_total_bytes: total } = this.sys;
      groups.push(group('system')
        .child(cell(t, { label: 'CPU', value: `${Math.round(cpu)}%`, pct: cpu, detail: 'CPU',
          hideDetail: this.width < 900, dim: this.stale.sys }))
        .child(cell(t, { label: 'RAM', value: `${Math.round(used / total * 100)}%`, pct: used / total * 100,
          detail: `${formatBytes(used).replace(/G$/, '')}/${formatBytes(total)}`,
          hideDetail: this.width < 900, dim: this.stale.sys })));
    }
    for (const [index, account] of accounts.entries()) {
      if (groups.length) groups.push(divider(t));
      const dim = account.dimDefault || account.stale || !!account.error || this.stale[account.dimDefault ? 'defaults' : 'usage'];
      const node = group(`account-${account.provider}-${index}`)
        .child(groupLabel(t, { ...account, stale: account.stale || this.stale[account.dimDefault ? 'defaults' : 'usage'] }, this.width < 780));
      if (account.five_hour) node.child(this.percentCell(t, '5h', account.five_hour, dim, now));
      node.child(this.percentCell(t, '주간', account.weekly, dim, now));
      if (account.provider === 'codex') {
        const count = account.coupons?.available ?? 0;
        const expiry = count > 0 ? formatCouponExpiry(account.coupons?.next_expires_at, now) : { text: '—', urgent: false };
        node.child(cell(t, { label: '쿠폰', value: `🎟 ${count}`, detail: expiry.text,
          color: count ? t.accent : t.dim, detailColor: expiry.urgent ? t.blocked : t.dim, dim }));
      }
      groups.push(node);
    }
    const unavailable = !this.sys && !this.usage && !this.defaults && Object.values(this.stale).some(Boolean);
    return h_flex().id('root').size_full().min_w(0).px(12).gap(0).items_center()
      .bg(t.chrome).border_b(1).border_color(t.border)
      .on_size_change((size, cx) => { if (this.width !== size.width) { this.width = size.width; cx.notify(); } })
      .child(h_flex().id('groups').flex_1().min_w(0).items_center().overflow_x_scrollbar()
        .children(unavailable ? [div().text_size(12).text_color(t.dim).child('orch hud 없음')] : groups))
      .child(div().flex_none().pl(8).text_size(10.8).text_color(stale ? t.working : t.dim)
        .child(`${stale ? '⚠ ' : ''}${stamp}`));
  }
}
