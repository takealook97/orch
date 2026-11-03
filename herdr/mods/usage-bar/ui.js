import { div } from 'gpui-kit';
import { h_flex, v_flex } from 'gpui-base';
import { pctColorRole } from './format.js';

export function roleColor(t, pct) {
  return t[{ ok: 'foreground', warn: 'working', crit: 'blocked' }[pctColorRole(pct)]];
}
export function progress(t, pct, width) {
  return div().w(width).h(2).flex_none().rounded(1).bg(t.border)
    .child(div().h(2).w(`${Math.max(0, Math.min(100, pct))}%`).rounded(1).bg(roleColor(t, pct)));
}
export function cell(t, { label, value, detail = '', pct, color, detailColor, hideDetail, hideBar }) {
  const width = label === 'Week' ? (hideDetail ? 58 : 110) : label === '5h' ? 94 : 76;
  return v_flex().w(width).flex_none().items_center().justify_center().gap(1)
    .child(h_flex().items_center().gap(4).text_size(12)
      .when(!!label, el => el.child(div().text_color(t.dim).child(label)))
      .child(div().font_semibold().text_color(color ?? (pct === undefined ? t.dim : roleColor(t, pct))).child(value))
      .when(pct !== undefined && !hideBar, el => el.child(progress(t, pct, 36))))
    .child(div().h(13).text_size(10.8).text_color(detailColor ?? t.dim).child(hideDetail ? '' : detail));
}
export function groupLabel(t, account, compact) {
  return v_flex().flex_none().min_w(44).items_center().justify_center().gap(1)
    .child(div().text_size(12).font_semibold().text_color(account.dimDefault ? t.dim : t.foreground)
      .child({ codex: 'Codex', claude: 'Claude', gemini: 'Gemini' }[account.provider]))
    .when(!compact && account.provider !== 'gemini', el => el.child(div().h(13).text_size(10.8)
      .text_color(t.dim).child(account.label)));
}
export function systemRow(t, label, pct, detail, hideBar) {
  const known = Number.isFinite(pct);
  return h_flex().w(hideBar ? 112 : 156).flex_none().items_center().gap(4).text_size(10.8)
    .child(div().w(26).text_color(t.dim).child(label))
    .child(div().w(30).font_semibold().text_color(known ? roleColor(t, pct) : t.dim)
      .child(known ? `${Math.round(pct)}%` : '—'))
    .when(known && !hideBar, el => el.child(progress(t, pct, 40)))
    .child(div().text_color(t.dim).child(detail));
}
