const ORCH = "/Users/deepeet/.local/bin/orch";

import { View } from 'gpui-kit';
import { h_flex, v_flex } from 'gpui-base';
import { exec, spaces, agents, theme } from 'herdr';
import { formatReset, formatCouponExpiry, formatBytes, pickAccounts, layoutMode } from './format.js';
import { cell, groupLabel, systemRow } from './ui.js';

export default class UsageBar extends View {
  init(_props, cx) {
    this.sys = null;
    this.usage = null;
    this.stale = { sys: false, usage: false };
    this.busy = {};
    this.request = {};
    this.width = 1200;
    this.observedKey = null;
    this.agentStatus = new Map();
    this.applyContext(this.context(), cx);
    this.refresh('sys', cx);
    this.refresh('usage', cx);
    cx.timer.every(1000, cx => this.refresh('sys', cx));
    cx.timer.every(60000, cx => this.refresh('usage', cx));
  }

  context() {
    const active = spaces().find(space => space.active);
    const statuses = agents().filter(agent => agent.space === active?.id && !agent.address.includes('/'))
      .map(agent => [agent.address, agent.status]).sort(([a], [b]) => a.localeCompare(b));
    return { space: active?.id, statuses, key: JSON.stringify([active?.id, statuses]) };
  }

  scheduleUsage(delay, cx) {
    this.debounce?.cancel();
    this.debounce = cx.timer.after(delay, cx => this.refresh('usage', cx));
  }

  applyContext(snapshot, cx) {
    const completed = snapshot.space === this.observedSpace && snapshot.statuses.some(([address, status]) =>
      this.agentStatus.get(address) === 'working' && ['done', 'idle', 'blocked'].includes(status));
    this.observedSpace = snapshot.space;
    this.agentStatus = new Map(snapshot.statuses);
    this.observedKey = snapshot.key;
    if (completed) this.scheduleUsage(2000, cx);
  }

  refresh(kind, cx) {
    if (this.busy[kind]) {
      this.request[kind] = true;
      return;
    }
    this.busy[kind] = true;
    const args = ['hud', kind === 'sys' ? 'sys' : 'usage'];
    if (kind === 'usage') args.push('--selected', '--max-age', '45s');
    cx.spawn(async cx => {
      let quiet = false;
      try {
        const result = await exec(ORCH, args);
        if (result.code !== 0) throw new Error('orch hud unavailable');
        const data = JSON.parse(result.stdout);
        if (!data || (kind === 'sys'
          ? !['cpu_pct', 'mem_used_bytes', 'mem_total_bytes'].every(field => Number.isFinite(data[field])) || data.mem_total_bytes <= 0
          : !Array.isArray(data.accounts))) throw new Error('invalid hud data');
        if (kind !== 'sys') data.accounts = data.accounts.map(account => {
          const prior = this[kind]?.accounts.find(old => old.provider === account.provider &&
            old.home === account.home && old.id === account.id);
          return account.error && prior ? { ...prior, ...account,
            five_hour: account.five_hour ?? prior.five_hour,
            weekly: account.weekly ?? prior.weekly,
            coupons: account.coupons ?? prior.coupons, stale: true } : account;
        });
        const before = kind === 'sys' ? sysSignature(this.sys, this.stale.sys) : null;
        this[kind] = data;
        this.stale[kind] = false;
        // A 1 s sys poll redraws the window only when a shown number changes.
        if (kind === 'sys' && sysSignature(data, false) === before) quiet = true;
      } catch (_error) {
        this.stale[kind] = true;
      } finally {
        delete this.busy[kind];
        if (!quiet) cx.notify();
        const pending = this.request[kind];
        delete this.request[kind];
        if (pending) this.refresh(kind, cx);
      }
    });
  }

  refreshAll(cx) {
    this.refresh('sys', cx);
    this.refresh('usage', cx);
  }

  percentCell(t, window, now, mode) {
    const past = window?.resets_at && new Date(window.resets_at) <= now;
    const pct = past ? 0 : window?.used_pct;
    const known = Number.isFinite(pct);
    const reset = window ? formatReset(window.resets_at, now) : '';
    return cell(t, { label: 'weekly', value: known ? `${Math.round(pct)}%` : '—',
      pct: known ? pct : undefined, detail: reset ? `↻ ${reset}` : '',
      hideBar: !mode.bars });
  }

  render(cx) {
    // Render only compares and schedules. Apply context after render, and
    // keep the last response visible until its replacement arrives.
    const snapshot = this.context();
    if (snapshot.key !== this.observedKey) {
      this.observation?.cancel();
      this.observation = cx.timer.after(0, cx => { this.applyContext(snapshot, cx); cx.notify(); });
    }
    const t = theme(), now = new Date();
    const accounts = pickAccounts(this.usage);
    const mode = layoutMode(this.width, accounts);
    const group = id => h_flex().id(id).h(38).items_center().flex_none().px(10).gap(8)
      .rounded(6).bg(t.chrome).border(1).border_color(t.border).cursor_pointer()
      .tooltip('Click to refresh usage')
      .on_mouse_down('left', (_event, cx) => cx.stop_propagation())
      .on_click((_event, cx) => this.refreshAll(cx));
    const { cpu_pct: cpu, mem_used_bytes: used, mem_total_bytes: total } = this.sys ?? {};
    const memory = total > 0 ? used / total * 100 : undefined;
    const detail = total > 0 ? `${formatBytes(used).replace(/G$/, '')}/${formatBytes(total)}` : '';
    const system = group('system').child(v_flex().items_center().justify_center().gap(3)
      .opacity(this.stale.sys ? 0.6 : 1)
      .child(systemRow(t, 'CPU', cpu, '', !mode.bars))
      .child(systemRow(t, 'RAM', memory, detail, !mode.bars)));
    const groups = [];
    for (const account of accounts) {
      const stale = account.stale || !!account.error || this.stale.usage;
      const content = h_flex().items_center().gap(12).opacity(stale ? 0.6 : 1)
        .child(groupLabel(t, account, !mode.accountLine))
        .child(this.percentCell(t, account.weekly, now, mode));
      if (account.provider === 'codex') {
        const count = account.coupons?.available;
        const expiry = count > 0 ? formatCouponExpiry(account.coupons.next_expires_at, now) : { text: '—', urgent: false };
        content.child(cell(t, { label: 'coupon', value: count === undefined ? '—' : `${count} resets`, detail: expiry.text,
          color: count ? t.accent : t.dim, detailColor: expiry.urgent ? t.blocked : t.dim }));
      }
      groups.push(group(`account-${account.provider}`).child(content));
    }
    return h_flex().id('root').size_full().min_w(0).px(12).gap(8).items_center()
      .bg(t.chrome).border_b(1).border_color(t.border)
      .on_size_change((size, cx) => { if (this.width !== size.width) { this.width = size.width; cx.notify(); } })
      .child(h_flex().id('groups').flex_none().min_w(0).items_center().gap(8).children(groups))
      .child(h_flex().flex_1().min_w(0))
      .child(system);
  }
}

function sysSignature(sys, stale) {
  if (!sys) return '';
  return [Math.round(sys.cpu_pct), Math.round(sys.mem_used_bytes / sys.mem_total_bytes * 100),
    (sys.mem_used_bytes / 2 ** 30).toFixed(0), stale].join('|');
}
