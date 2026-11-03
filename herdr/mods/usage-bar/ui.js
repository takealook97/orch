import { div } from 'gpui-kit';
import { h_flex, v_flex } from 'gpui-base';
import { pctColorRole, labelWidth } from './format.js';

export function roleColor(t, pct) {
  return t[{ ok: 'foreground', warn: 'working', crit: 'blocked' }[pctColorRole(pct)]];
}
export function progress(t, pct, width) {
  return div().w(width).h(2).flex_none().rounded(1).bg(t.border)
    .child(div().h(2).w(`${Math.max(0, Math.min(100, pct))}%`).rounded(1).bg(roleColor(t, pct)));
}
export function cell(t, { label, value, detail = '', pct, color, detailColor, hideBar }) {
  const missing = value === '—';
  return v_flex().flex_none().justify_center().gap(1)
    .child(h_flex().h(16).items_center().gap(4).text_size(12)
      .child(div().font_semibold().text_color(color ?? (pct === undefined ? t.dim : roleColor(t, pct))).child(value))
      .when(pct !== undefined && !hideBar, el => el.child(progress(t, pct, 28))))
    .child(div().h(15).text_size(10.5).text_color(detailColor ?? t.dim).child(missing ? '' : detail));
}
export function groupLabel(t, account, compact) {
  return v_flex().flex_none().max_w(labelWidth(!compact)).justify_center().gap(1)
    .child(h_flex().h(16).items_center().text_size(12).font_semibold().text_color(t.foreground)
      .child(div().truncate().child(account.provider)))
    .when(!compact && !!account.label, el => el.child(div().h(15).text_size(10.5)
      .text_color(t.dim).truncate().child(account.label)));
}
export function systemRow(t, label, pct, detail, hideBar) {
  const known = Number.isFinite(pct);
  return h_flex().flex_none().items_center().gap(4).text_size(10.8)
    .child(div().w(26).text_color(t.dim).child(label))
    .child(div().w(30).font_semibold().text_color(known ? roleColor(t, pct) : t.dim)
      .child(known ? `${Math.round(pct)}%` : '—'))
    .when(known && !hideBar, el => el.child(progress(t, pct, 32)))
    .child(div().text_color(t.dim).child(detail));
}
