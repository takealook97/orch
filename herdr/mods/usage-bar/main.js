const ORCH = "/Users/deepeet/.local/bin/orch";

import { View } from 'gpui-kit';
import { h_flex, v_flex } from 'gpui-base';
import { exec, spaces, panes, agents, theme } from 'herdr';
import { formatReset, formatCouponExpiry, formatBytes, pickAccounts } from './format.js';
import { cell, groupLabel, systemRow } from './ui.js';

export default class UsageBar extends View {
  init(_props, cx) {
    this.sys = null;
    this.usage = null;
    this.defaults = null;
    this.stale = { sys: false, usage: false, defaults: false };
    this.busy = {};
    this.request = {};
    this.width = 1200;
    this.terminals = [];
    this.terminalKey = null;
    this.observedKey = null;
    this.agentStatus = new Map();
    this.applyContext(this.context(), cx);
    this.refresh('sys', cx);
    this.refresh('defaults', cx);
    cx.timer.every(3000, cx => this.refresh('sys', cx));
    cx.timer.every(60000, cx => this.refresh('usage', cx));
    cx.timer.every(120000, cx => this.refresh('defaults', cx));
  }

  context() {
    const active = spaces().find(space => space.active);
    const tabs = new Set((active?.tabs ?? []).map(tab => tab.id));
    const terminals = [...new Set(panes().filter(pane => tabs.has(pane.tab) && pane.terminal &&
      !pane.terminal.includes('/')).map(pane => pane.terminal))].sort();
    const statuses = agents().filter(agent => agent.space === active?.id && !agent.address.includes('/'))
      .map(agent => [agent.address, agent.status]).sort(([a], [b]) => a.localeCompare(b));
    const terminalKey = `${active?.id ?? ''}:${terminals.join(',')}`;
    return { terminals, statuses, terminalKey, key: JSON.stringify([terminalKey, statuses]) };
  }

  scheduleUsage(delay, cx) {
    this.debounce?.cancel();
    this.debounce = cx.timer.after(delay, cx => this.refresh('usage', cx));
  }

  applyContext(snapshot, cx) {
    const changed = snapshot.terminalKey !== this.terminalKey;
    const completed = !changed && snapshot.statuses.some(([address, status]) =>
      this.agentStatus.get(address) === 'working' && ['done', 'idle', 'blocked'].includes(status));
    this.terminals = snapshot.terminals;
    this.terminalKey = snapshot.terminalKey;
    this.agentStatus = new Map(snapshot.statuses);
    this.observedKey = snapshot.key;
    if (changed || completed) this.scheduleUsage(changed ? 1000 : 2000, cx);
  }

  refresh(kind, cx, force = false) {
    const key = kind === 'usage' ? this.terminalKey : kind;
    if (kind === 'usage' && !this.terminals.length) return;
    if (this.busy[kind]) {
      if (force || this.busy[kind] !== key) this.request[kind] = { force };
      return;
    }
    this.busy[kind] = key;
    const args = ['hud', kind === 'sys' ? 'sys' : 'usage'];
    if (kind === 'usage') args.push('--terminals', this.terminals.join(','));
    if (kind !== 'sys') args.push('--max-age', kind === 'usage' ? '45s' : '120s');
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
        // Preserve windows only for the same account; Gemini and defaults may
        // have no home, so provider + home alone is insufficient identity.
        if (kind !== 'sys') data.accounts = data.accounts.map(account => {
          const prior = this[kind]?.accounts.find(old => old.provider === account.provider &&
            old.home === account.home && old.id === account.id);
          if (kind === 'defaults' && account.error && prior && !prior.error) return { ...prior, stale: true };
          return account.error && prior ? { ...prior, ...account,
            five_hour: account.five_hour ?? prior.five_hour,
            weekly: account.weekly ?? prior.weekly,
            coupons: account.coupons ?? prior.coupons, stale: true } : account;
        });
        this[kind] = data;
        this.stale[kind] = false;
      } catch (_error) {
        if (kind !== 'usage' || key === this.terminalKey) this.stale[kind] = true;
      } finally {
        delete this.busy[kind];
        cx.notify();
        const pending = this.request[kind];
        delete this.request[kind];
        if (pending) this.refresh(kind, cx, pending.force);
      }
    });
  }

  refreshAll(cx) {
    this.refresh('sys', cx);
    this.refresh('usage', cx, true);
    this.refresh('defaults', cx, true);
  }

  percentCell(t, label, window, now) {
    const past = window?.resets_at && new Date(window.resets_at) <= now;
    const pct = past ? 0 : window?.used_pct;
    const known = Number.isFinite(pct);
    const reset = window ? formatReset(window.resets_at, now) : '';
    return cell(t, { label, value: known ? `${Math.round(pct)}%` : '—',
      pct: known ? pct : undefined, detail: reset ? `↻ ${reset}` : '',
      hideDetail: label === 'Week' && this.width < 700, hideBar: this.width < 980 });
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
    const accounts = pickAccounts(this.usage, this.defaults, this.terminals);
    const group = id => h_flex().id(id).h(36).items_center().flex_none().px(8).gap(10)
      .rounded(6).bg(t.raised).border(1).border_color(t.border).cursor_pointer()
      .tooltip('Click to refresh usage')
      .on_mouse_down('left', (_event, cx) => cx.stop_propagation())
      .on_click((_event, cx) => this.refreshAll(cx));
    const { cpu_pct: cpu, mem_used_bytes: used, mem_total_bytes: total } = this.sys ?? {};
    const memory = total > 0 ? used / total * 100 : undefined;
    const detail = total > 0 ? `${formatBytes(used).replace(/G$/, '')}/${formatBytes(total)}` : '';
    const groups = [group('system').child(v_flex().items_center().justify_center().gap(1)
      .opacity(this.stale.sys ? 0.6 : 1)
      .child(systemRow(t, 'CPU', cpu, '', this.width < 980))
      .child(systemRow(t, 'RAM', memory, detail, this.width < 980)))];
    for (const account of accounts) {
      const stale = account.stale || !!account.error || this.stale[account.dimDefault ? 'defaults' : 'usage'];
      const content = h_flex().items_center().gap(10).opacity(stale || account.dimDefault ? 0.6 : 1)
        .child(groupLabel(t, account, this.width < 820))
        .child(this.percentCell(t, '5h', account.five_hour, now))
        .child(this.percentCell(t, 'Week', account.weekly, now));
      if (account.provider === 'codex') {
        const count = account.coupons?.available;
        const expiry = count > 0 ? formatCouponExpiry(account.coupons.next_expires_at, now) : { text: '—', urgent: false };
        content.child(cell(t, { value: count === undefined ? '— resets' : `${count} resets`, detail: expiry.text,
          color: count ? t.accent : t.dim, detailColor: expiry.urgent ? t.blocked : t.dim }));
      }
      groups.push(group(`account-${account.provider}`).child(content));
    }
    return h_flex().id('root').size_full().min_w(0).px(12).gap(8).items_center()
      .bg(t.chrome).border_b(1).border_color(t.border)
      .on_size_change((size, cx) => { if (this.width !== size.width) { this.width = size.width; cx.notify(); } })
      .child(h_flex().id('groups').flex_1().min_w(0).items_center().gap(8).overflow_x_scrollbar().children(groups));
  }
}
