import { div } from 'gpui-kit';
import { h_flex, v_flex } from 'gpui-base';
import { pctColorRole } from './format.js';

export function roleColor(t, pct) {
  return t[{ ok: 'foreground', warn: 'working', crit: 'blocked' }[pctColorRole(pct)]];
}

export function cell(t, { label, value, detail, pct, color, detailColor, hideDetail, hideBar, dim }) {
  const ink = dim ? t.dim : color ?? roleColor(t, pct ?? 0);
  return v_flex().w(100).min_w(92).flex_none().px(6).gap(2)
    .child(h_flex().items_center().text_size(12)
      .child(div().text_color(t.dim).child(label))
      .child(div().flex_1())
      .child(div().font_semibold().text_color(ink).child(value)))
    .when(pct !== undefined && !hideBar, el => el.child(div().h(2).w_full().rounded(1).bg(t.border)
      .child(div().h(2).w(`${Math.max(0, Math.min(100, pct))}%`).rounded(1).bg(ink))))
    .when(!hideDetail, el => el.child(div().text_size(10.8).text_color(dim ? t.dim : detailColor ?? t.dim).child(detail)));
}

export function divider(t) {
  return div().w(1).h(24).mx(10).flex_none().bg(t.border);
}

export function groupLabel(t, account, compact) {
  return v_flex().min_w(compact ? 48 : 80).max_w(140).flex_none().gap(2).pr(6)
    .child(div().text_size(12).font_semibold().text_color(account.dimDefault || account.stale || account.error ? t.dim : t.foreground)
      .child(`${account.provider === 'codex' ? 'Codex' : 'Claude'}${account.stale || account.error ? ' ·' : ''}`))
    .when(!compact, el => el.child(div().text_size(10.8).text_color(t.dim).truncate()
      .child(`${account.label} ${account.plan ?? ''}`.trim())));
}
