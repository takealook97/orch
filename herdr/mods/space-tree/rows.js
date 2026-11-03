import { div, svg, image } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { run, shared } from "herdr";
import { nameElement } from "./rename.js";

const asset = name => `mods/space-tree/${name}.svg`;
const tone = (t, status) => t[status] ?? t.dim;
const dot = (row, t) => div().size(6).rounded(3).flex_none().bg(tone(t, row.status));
const glyph = (name, t) => svg(asset(name)).size(13).flex_none().text_color(t.dim);

function icon(row, t) {
  if (row.kind === "machine") return glyph(row.id ? "server" : "monitor", t);
  if (row.kind === "space") {
    if (row.data.parentId) return glyph("branch", t);
    const color = (shared("space.colors") ?? {})[row.id] ?? "ansi6";
    return h_flex().size(18).flex_none().rounded(5).bg(t[color] ?? t.accent)
      .items_center().justify_center().text_size(10.5).font_bold()
      .text_color(t.accent_foreground).child([...row.name][0]?.toUpperCase() ?? "?");
  }
  if (row.kind === "tab") return div().w(14).text_color(t.dim).child("▤");
  const program = row.data.program;
  if (program?.icon) return program.tint ? svg(program.icon).size(14).flex_none().text_color(t.muted) : image(program.icon).size(14).flex_none();
  return div().w(14).text_color(t.dim).child("›_");
}
function gitTail(row, t) {
  const g = row.data.git;
  if (!g) return div();
  const same = value => value?.toLowerCase() === row.name.toLowerCase();
  const branch = same(g.branch) || ["main", "master"].includes(g.branch) ? "" : g.branch ?? "detached";
  const repo = row.data.parentId || same(g.name) ? "" : g.name;
  return h_flex().min_w(0).gap(3).text_size(11.5).text_color(t.dim)
    .child(div().truncate().child([repo, branch].filter(Boolean).join(" · ")))
    .when(g.ahead > 0, el => el.child(`↑${g.ahead}`))
    .when(g.behind > 0, el => el.child(`↓${g.behind}`));
}
export function treeRow(view, row, t) {
  const stop = (_e, cx) => cx.stop_propagation();
  const name = nameElement(view, row, t);
  const main = h_flex().w_full().min_w(0).items_center().gap(5)
    .child(div().w(9).flex_none().text_color(t.border).text_size(11).child(row.guide))
    .child(row.foldable ? div().id(`fold-${row.key}`).size(12).flex_none()
      .on_mouse_down("left", stop).on_mouse_up("left", stop)
      .on_click((_e, cx) => { cx.stop_propagation(); view.toggle(row, cx); })
      .child(svg(asset(row.open ? "chevron-down" : "chevron-right")).size(12).text_color(t.dim)) : div().w(12).flex_none())
    .child(icon(row, t))
    .child(row.kind === "pane" ? v_flex().flex_1().min_w(0).gap(1).child(name)
      .child(div().truncate().text_size(10.8).text_color(t.dim).child(row.sub)) : name);
  if (row.kind === "space") main.child(gitTail(row, t));
  if (row.kind === "tab") main.child(div().text_size(11).text_color(t.dim).child(String(row.count)));
  if (row.badge) main.child(div().flex_none().px(3).rounded(4).text_size(10)
    .when(view.orchStale, el => el.opacity(0.5))
    .text_color(t.dim).child(row.badge));
  if (row.kind === "machine") {
    const machine = view.machines.find(m => m.name === row.id) ?? view.reconnecting[row.id]?.machine;
    const repair = view.reconnecting[row.id];
    const rebuilt = view.rebuilt(machine) || !!repair;
    const connected = row.id == null || machine?.state === "connected" || row.data.layout === "ready";
    if (rebuilt) main.child(div().id(`connect-${row.key}`).text_size(11).text_color(t.working)
      .tooltip(repair?.error || "Connect to rebuilt machine")
      .on_mouse_down("left", stop).on_click((_e, cx) => { cx.stop_propagation(); view.reconnect(machine, cx); })
      .child(repair?.busy ? "Connecting…" : "Connect"));
    else if (!connected) main.child(div().text_size(11).text_color(t.dim).child((machine?.state ?? row.data.layout ?? "asleep").replace(/_/g, " ")));
    if (connected) main.child(div().id(`add-${row.key}`).px(3).text_color(t.dim).hover(s => s.text_color(t.foreground))
      .on_mouse_down("left", stop).on_click((_e, cx) => { cx.stop_propagation(); run("space.new", { machine: row.id ?? "" }); }).child("+"));
    main.child(div().size(6).flex_none().rounded(3).bg(connected ? t.done : t.dim));
  } else main.child(dot(row, t));
  const el = h_flex().id(row.key).relative().h(row.kind === "pane" ? 36 : row.kind === "space" ? row.data.parentId ? 28 : 30 : 26)
    .flex_none().pl(2 + row.depth * 14).pr(6).items_center().rounded(5).text_size(row.kind === "pane" ? 12.5 : 12)
    .text_color(row.kind === "machine" ? t.muted : row.orch && view.orchStale ? t.dim : t.foreground)
    .bg(row.active ? row.kind === "pane" ? t.raised : t.selected : row.key === view.highlight ? t.raised : t.chrome)
    .hover(s => s.bg(t.hover))
    .on_click((_e, cx) => view.select(row, cx))
    .on_mouse_down("right", (_e, cx) => { cx.stop_propagation(); view.contextMenu(row); })
    .when(row.kind === "pane" && row.active, e => e.child(div().absolute().left(0).top(0).bottom(0).w(2).bg(t.accent)))
    .child(main);
  if (row.kind === "space") {
    const i = row.data.order;
    el.on_mouse_down("left", () => { view.pressed = i; view.over = i; })
      .on_mouse_move((_e, cx) => view.dragOver(row, cx))
      .on_mouse_up("left", (_e, cx) => view.dropSpace(row, cx))
      .when(view.pressed === i && view.over !== i, e => e.opacity(0.5))
      .when(view.pressed !== null && view.over === i && view.pressed !== i, e => e.child(div().absolute().left(0).right(0).h(2).bg(t.accent)
        .when(view.pressed < i, line => line.bottom(0)).when(view.pressed > i, line => line.top(0))));
  }
  return el;
}
